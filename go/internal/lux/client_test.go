package lux

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	c := NewClient("192.168.1.100", 8000, "AAAAAAAAAA", "1234567890")
	if c.host != "192.168.1.100" {
		t.Errorf("host = %q, want '192.168.1.100'", c.host)
	}
	if c.port != 8000 {
		t.Errorf("port = %d, want 8000", c.port)
	}
	if c.datalog != "AAAAAAAAAA" {
		t.Errorf("datalog = %q, want 'AAAAAAAAAA'", c.datalog)
	}
	if c.serial != "1234567890" {
		t.Errorf("serial = %q, want '1234567890'", c.serial)
	}
	if c.guard == nil {
		t.Error("guard should not be nil")
	}
	if c.holding == nil {
		t.Error("holding map should be initialized")
	}
	if c.input == nil {
		t.Error("input map should be initialized")
	}
}

func TestCloseNilConn(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "BBBBBBBBBB")
	if err := c.Close(); err != nil {
		t.Errorf("Close with nil conn returned error: %v", err)
	}
}

func TestConnectInvalidHost(t *testing.T) {
	c := NewClient("192.0.2.1", 1, "AAAAAAAAAA", "BBBBBBBBBB") // non-routable
	c.conn = nil
	// We can't easily test a real connect failure without a long timeout,
	// but we can test that ReadHold/ReadInput/WriteSingle fail when not connected.
	if err := c.ReadHold(0, 10); err == nil {
		t.Error("ReadHold without connection should return error")
	}
	if err := c.ReadInput(0, 10); err == nil {
		t.Error("ReadInput without connection should return error")
	}
	if err := c.WriteSingle(60, 100); err == nil {
		t.Error("WriteSingle without connection should return error")
	}
}

func TestUpdateRegisters(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "BBBBBBBBBB")

	// Update holding registers
	holdingPkt := &Packet{
		RegisterType: "holding",
		Registers:    map[uint16]uint16{21: 0x1234, 60: 100},
	}
	c.updateRegisters(holdingPkt)

	v, ok := c.GetHolding(21)
	if !ok || v != 0x1234 {
		t.Errorf("GetHolding(21) = (%d, %v), want (0x1234, true)", v, ok)
	}
	v, ok = c.GetHolding(60)
	if !ok || v != 100 {
		t.Errorf("GetHolding(60) = (%d, %v), want (100, true)", v, ok)
	}

	// Update input registers
	inputPkt := &Packet{
		RegisterType: "input",
		Registers:    map[uint16]uint16{0: 0x04, 1: 3500},
	}
	c.updateRegisters(inputPkt)

	v, ok = c.GetInput(0)
	if !ok || v != 0x04 {
		t.Errorf("GetInput(0) = (%d, %v), want (4, true)", v, ok)
	}
	v, ok = c.GetInput(1)
	if !ok || v != 3500 {
		t.Errorf("GetInput(1) = (%d, %v), want (3500, true)", v, ok)
	}

	// Unknown register should return false
	_, ok = c.GetHolding(9999)
	if ok {
		t.Error("GetHolding(9999) should return false")
	}
	_, ok = c.GetInput(9999)
	if ok {
		t.Error("GetInput(9999) should return false")
	}
}

func TestUpdateRegistersNilPacket(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "BBBBBBBBBB")
	// Should not panic
	c.updateRegisters(&Packet{})
	c.updateRegisters(&Packet{RegisterType: "holding", Registers: nil})
}

func TestAllHoldingAllInput(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "BBBBBBBBBB")

	c.updateRegisters(&Packet{
		RegisterType: "holding",
		Registers:    map[uint16]uint16{21: 100, 60: 200},
	})
	c.updateRegisters(&Packet{
		RegisterType: "input",
		Registers:    map[uint16]uint16{0: 1, 1: 2, 2: 3},
	})

	holding := c.AllHolding()
	if len(holding) != 2 {
		t.Errorf("AllHolding() has %d entries, want 2", len(holding))
	}
	if holding[21] != 100 || holding[60] != 200 {
		t.Errorf("AllHolding() = %v", holding)
	}
	// Verify it's a copy
	holding[21] = 999
	v, _ := c.GetHolding(21)
	if v != 100 {
		t.Error("AllHolding should return a copy, not the internal map")
	}

	input := c.AllInput()
	if len(input) != 3 {
		t.Errorf("AllInput() has %d entries, want 3", len(input))
	}
}

func TestWriteGuardStats(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "BBBBBBBBBB")
	count, writes := c.WriteGuardStats()
	if count != 0 || len(writes) != 0 {
		t.Errorf("WriteGuardStats() = (%d, %d entries), want (0, 0)", count, len(writes))
	}
}

// buildResponsePacket builds a protocol=5 TranslatedData response with register data.
func buildResponsePacket(regType byte, startReg uint16, values []uint16) []byte {
	valueLen := len(values) * 2
	// Total: header(2) + proto(2) + framelen(2) + unknown(1) + tcpfunc(1) +
	//        datalog(10) + datalen(2) + addr(1) + devfunc(1) + inverter(10) +
	//        startreg(2) + vlb(1) + values(N) + crc(2)
	total := 35 + valueLen + 2
	pkt := make([]byte, total)
	pkt[0], pkt[1] = 0xa1, 0x1a
	binary.LittleEndian.PutUint16(pkt[2:4], 5)               // Protocol=5
	binary.LittleEndian.PutUint16(pkt[4:6], uint16(total-6)) // Frame length
	pkt[6] = 0x01                                            // Unknown
	pkt[7] = FuncTranslatedData
	copy(pkt[8:18], "AAAAAAAAAA")
	binary.LittleEndian.PutUint16(pkt[18:20], uint16(total-20)) // Data length
	pkt[20] = 0x01                                              // Address=inverter
	pkt[21] = regType
	copy(pkt[22:32], "1234567890")
	binary.LittleEndian.PutUint16(pkt[32:34], startReg)
	pkt[34] = byte(valueLen) // VLB
	for i, v := range values {
		binary.LittleEndian.PutUint16(pkt[35+i*2:35+i*2+2], v)
	}
	crc := CRC16Modbus(pkt[20 : total-2])
	binary.LittleEndian.PutUint16(pkt[total-2:], crc)
	return pkt
}

func TestListenWithPipe(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	c.conn = client

	// Build a holding response packet with 2 registers
	pkt := buildResponsePacket(ModbusReadHolding, 21, []uint16{0xABCD, 0x1111})

	var listenErr error
	received := make(chan struct{})
	go func() {
		listenErr = c.Listen(func(p *Packet) {
			close(received)
		})
	}()

	// Write the packet from server side
	_, err := server.Write(pkt)
	if err != nil {
		t.Fatalf("server.Write: %v", err)
	}

	// Wait for callback
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("Listen callback not called within timeout")
	}

	// Verify registers were stored
	v, ok := c.GetHolding(21)
	if !ok || v != 0xABCD {
		t.Errorf("GetHolding(21) = (%04x, %v), want (ABCD, true)", v, ok)
	}

	// Close to stop Listen
	server.Close()
	client.Close()
	_ = listenErr
}

func TestListenInputRegisters(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	c.conn = client

	pkt := buildResponsePacket(ModbusReadInput, 0, []uint16{0x04, 3500, 3200})

	received := make(chan struct{})
	go func() {
		c.Listen(func(p *Packet) {
			close(received)
		})
	}()

	server.Write(pkt)

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("Listen callback not called")
	}

	v, ok := c.GetInput(0)
	if !ok || v != 0x04 {
		t.Errorf("GetInput(0) = (%d, %v), want (4, true)", v, ok)
	}
	v, ok = c.GetInput(1)
	if !ok || v != 3500 {
		t.Errorf("GetInput(1) = (%d, %v), want (3500, true)", v, ok)
	}

	server.Close()
	client.Close()
}

func TestListenNilCallback(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	c.conn = client

	pkt := buildResponsePacket(ModbusReadHolding, 60, []uint16{100})

	done := make(chan error, 1)
	go func() {
		done <- c.Listen(nil)
	}()

	server.Write(pkt)
	// Give time for processing
	time.Sleep(50 * time.Millisecond)

	// Verify register was still stored despite nil callback
	v, ok := c.GetHolding(60)
	if !ok || v != 100 {
		t.Errorf("GetHolding(60) = (%d, %v), want (100, true)", v, ok)
	}

	server.Close()
	client.Close()
	<-done
}

func TestReadHoldWithPipe(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	c.conn = client

	received := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 1024)
		n, _ := server.Read(buf)
		received <- buf[:n]
	}()

	err := c.ReadHold(21, 10)
	if err != nil {
		t.Fatalf("ReadHold: %v", err)
	}

	data := <-received
	if len(data) != 38 {
		t.Fatalf("ReadHold packet length = %d, want 38", len(data))
	}
	if data[21] != ModbusReadHolding {
		t.Errorf("DeviceFunction = %02x, want %02x", data[21], ModbusReadHolding)
	}

	server.Close()
	client.Close()
}

func TestReadInputWithPipe(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	c.conn = client

	received := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 1024)
		n, _ := server.Read(buf)
		received <- buf[:n]
	}()

	err := c.ReadInput(0, 40)
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}

	data := <-received
	if data[21] != ModbusReadInput {
		t.Errorf("DeviceFunction = %02x, want %02x", data[21], ModbusReadInput)
	}

	server.Close()
	client.Close()
}

func TestWriteSingleWithPipe(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	c.conn = client

	// Pre-populate holding so we can verify old value logging
	c.updateRegisters(&Packet{
		RegisterType: "holding",
		Registers:    map[uint16]uint16{64: 50},
	})

	received := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 1024)
		n, _ := server.Read(buf)
		received <- buf[:n]
	}()

	err := c.WriteSingle(64, 100)
	if err != nil {
		t.Fatalf("WriteSingle: %v", err)
	}

	data := <-received
	if data[21] != ModbusWriteSingle {
		t.Errorf("DeviceFunction = %02x, want %02x", data[21], ModbusWriteSingle)
	}
	reg := binary.LittleEndian.Uint16(data[32:34])
	val := binary.LittleEndian.Uint16(data[34:36])
	if reg != 64 || val != 100 {
		t.Errorf("Write reg=%d val=%d, want reg=64 val=100", reg, val)
	}

	// Verify write was logged
	count, writes := c.WriteGuardStats()
	if count != 1 {
		t.Errorf("WritesInLastMinute = %d, want 1", count)
	}
	if len(writes) != 1 || writes[0].OldValue != 50 || writes[0].NewValue != 100 {
		t.Errorf("Write log = %+v", writes)
	}

	server.Close()
	client.Close()
}

func TestWriteSingleProtected(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	c.conn = client

	err := c.WriteSingle(30, 100) // Protected register
	if err == nil {
		t.Error("WriteSingle to protected register should return error")
	}

	server.Close()
	client.Close()
}

func TestWriteSingleRejectsBeforeSending(t *testing.T) {
	tests := []struct {
		name    string
		reg     uint16
		value   uint16
		wantMsg string
	}{
		{"comm address not writable", 15, 2, "not writable"},
		{"charge voltage not writable", 99, 560, "not writable"},
		{"soc out of range", 67, 150, "out of range"},
		{"invalid schedule time", 68, 25, "invalid schedule time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No connection: a rejected write must fail validation before
			// ever reaching the "not connected" check.
			c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
			err := c.WriteSingle(tt.reg, tt.value)
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("WriteSingle(%d, %d) err = %v, want containing %q", tt.reg, tt.value, err, tt.wantMsg)
			}
			if n, _ := c.WriteGuardStats(); n != 0 {
				t.Errorf("rejected write was recorded (%d writes)", n)
			}
		})
	}
}

// serveHolding answers the next request read from server with a holding
// response for start/values (nil values = read the request, never answer).
func serveHolding(server net.Conn, start uint16, values []uint16) {
	go func() {
		buf := make([]byte, 1024)
		if _, err := server.Read(buf); err != nil {
			return
		}
		if values != nil {
			server.Write(buildResponsePacket(ModbusReadHolding, start, values))
		}
	}()
}

func TestReadHoldingRange(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	defer server.Close()
	c.conn = client

	serveHolding(server, 66, []uint16{100, 80, 22, 6})
	got, err := c.ReadHoldingRange(66, 4, 2*time.Second)
	if err != nil {
		t.Fatalf("ReadHoldingRange: %v", err)
	}
	want := map[uint16]uint16{66: 100, 67: 80, 68: 22, 69: 6}
	for r, v := range want {
		if got[r] != v {
			t.Errorf("reg %d = %d, want %d", r, got[r], v)
		}
	}

	// The connection stays usable for a second read (same-connection verify).
	serveHolding(server, 21, []uint16{0x8FC3})
	got, err = c.ReadHoldingRange(21, 1, 2*time.Second)
	if err != nil || got[21] != 0x8FC3 {
		t.Fatalf("second ReadHoldingRange = %v, %v", got, err)
	}
}

func TestReadHoldingRangeIgnoresStaleCache(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	defer server.Close()
	c.conn = client

	// A value cached before the request must not satisfy the read.
	c.updateRegisters(&Packet{RegisterType: "holding", Registers: map[uint16]uint16{21: 0x9999}})
	serveHolding(server, 21, nil)
	if _, err := c.ReadHoldingRange(21, 1, 200*time.Millisecond); err == nil {
		t.Fatal("ReadHoldingRange succeeded with no response; stale cache was used")
	}
	if _, ok := c.GetHolding(21); ok {
		t.Error("stale cached value survived a failed read")
	}
}

func TestReadHoldingRangePartial(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	defer server.Close()
	c.conn = client

	serveHolding(server, 66, []uint16{100, 80})
	got, err := c.ReadHoldingRange(66, 4, 200*time.Millisecond)
	if err == nil {
		t.Fatalf("ReadHoldingRange returned %v with only 2 of 4 registers", got)
	}
	if !strings.Contains(err.Error(), "2 of 4") {
		t.Errorf("err = %v, want it to report 2 of 4 received", err)
	}
}

func TestListenDropsBadCRC(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	defer server.Close()
	c.conn = client

	bad := buildResponsePacket(ModbusReadInput, 26, []uint16{2520})
	bad[len(bad)-1] ^= 0xFF // corrupt CRC
	good := buildResponsePacket(ModbusReadInput, 27, []uint16{410})

	var seen []uint16
	got := make(chan struct{})
	go c.Listen(func(p *Packet) {
		seen = append(seen, p.StartRegister)
		close(got)
	})
	server.Write(bad)
	server.Write(good)

	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("good packet never delivered")
	}
	if len(seen) != 1 || seen[0] != 27 {
		t.Errorf("callback saw frames starting at %v, want only [27]", seen)
	}
	if _, ok := c.GetInput(26); ok {
		t.Error("bad-CRC frame was stored")
	}
	if n := c.DroppedBadCRC(); n != 1 {
		t.Errorf("DroppedBadCRC = %d, want 1", n)
	}
}

func TestReadHoldingRangeDropsBadCRC(t *testing.T) {
	c := NewClient("1.2.3.4", 8000, "AAAAAAAAAA", "1234567890")
	server, client := net.Pipe()
	defer server.Close()
	c.conn = client

	go func() {
		buf := make([]byte, 1024)
		server.Read(buf)
		bad := buildResponsePacket(ModbusReadHolding, 21, []uint16{0})
		bad[len(bad)-1] ^= 0xFF
		server.Write(bad)
	}()
	if got, err := c.ReadHoldingRange(21, 1, 200*time.Millisecond); err == nil {
		t.Fatalf("ReadHoldingRange accepted a bad-CRC frame: %v", got)
	}
}
