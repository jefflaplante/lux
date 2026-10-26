package cmd

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/jefflaplante/lux/internal/lux"
)

// fakeToken is a completed mqtt.Token with a fixed error.
type fakeToken struct{ err error }

func (t fakeToken) Wait() bool                     { return true }
func (t fakeToken) WaitTimeout(time.Duration) bool { return true }
func (t fakeToken) Done() <-chan struct{}          { c := make(chan struct{}); close(c); return c }
func (t fakeToken) Error() error                   { return t.err }

type pubMsg struct {
	topic    string
	retained bool
	payload  string
}

// fakeMQTT records publishes; topics listed in fail return a failed token.
type fakeMQTT struct {
	mu   sync.Mutex
	msgs []pubMsg
	fail map[string]error
}

func (f *fakeMQTT) Publish(topic string, qos byte, retained bool, payload interface{}) mqtt.Token {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, pubMsg{topic, retained, payload.(string)})
	return fakeToken{f.fail[topic]}
}

func (f *fakeMQTT) count(topic string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.msgs {
		if m.topic == topic {
			n++
		}
	}
	return n
}

func (f *fakeMQTT) last(topic string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.msgs) - 1; i >= 0; i-- {
		if f.msgs[i].topic == topic {
			return f.msgs[i].payload, true
		}
	}
	return "", false
}

func newTestPublisher(f *fakeMQTT, staleThreshold int) *publisher {
	return &publisher{
		mqtt:   f,
		prefix: "lux",
		retain: true,
		log:    &pubLogger{quiet: true, w: io.Discard},
		stale:  lux.NewStaleDetector(staleThreshold),
	}
}

func inputFrame(start uint16, vals ...uint16) *lux.Packet {
	regs := make(map[uint16]uint16, len(vals))
	for i, v := range vals {
		regs[start+uint16(i)] = v
	}
	return &lux.Packet{RegisterType: "input", StartRegister: start, RegisterCount: uint16(len(vals)), Registers: regs, CRCValid: true}
}

func TestPublisherSkipsStaleFrames(t *testing.T) {
	f := &fakeMQTT{}
	p := newTestPublisher(f, 3)
	frozen := func() *lux.Packet { return inputFrame(0, 0x0C, 3500, 3200, 0, 517) }

	for i := 0; i < 5; i++ {
		p.handlePacket(frozen())
	}
	// Frames 1-2 publish; 3-5 are frozen repeats and must not.
	if n := f.count("lux/inverter_1/pv_voltage_1/state"); n != 2 {
		t.Errorf("pv_voltage_1 published %d times, want 2", n)
	}
	if n := f.count("lux/inverter_1/last_seen/state"); n != 2 {
		t.Errorf("last_seen refreshed %d times, want 2 (stale frames must not refresh it)", n)
	}
	if p.staleSkipped != 3 {
		t.Errorf("staleSkipped = %d, want 3", p.staleSkipped)
	}

	// A live frame resumes publishing and refreshes last_seen.
	p.handlePacket(inputFrame(0, 0x0C, 3501, 3200, 0, 517))
	if n := f.count("lux/inverter_1/last_seen/state"); n != 3 {
		t.Errorf("last_seen refreshed %d times after live frame, want 3", n)
	}
}

func TestPublisherHoldingDoesNotRefreshLastSeen(t *testing.T) {
	f := &fakeMQTT{}
	p := newTestPublisher(f, 3)
	hold := &lux.Packet{RegisterType: "holding", StartRegister: 68, RegisterCount: 1,
		Registers: map[uint16]uint16{68: encodeTime(22, 0)}, CRCValid: true}

	for i := 0; i < 4; i++ {
		p.handlePacket(hold)
	}
	if n := f.count("lux/inverter_1/grid_charge_slot_1_start/state"); n != 4 {
		t.Errorf("holding topic published %d times, want 4 (config is never stale)", n)
	}
	if n := f.count("lux/inverter_1/last_seen/state"); n != 0 {
		t.Errorf("last_seen refreshed %d times by config frames, want 0", n)
	}
}

func TestPublisherLoadPowerClampedOnFrozenFrame(t *testing.T) {
	f := &fakeMQTT{}
	p := newTestPublisher(f, 3)
	regs := make([]uint16, 28)
	regs[16], regs[26] = 1, 2520 // observed frozen tuple
	p.handlePacket(inputFrame(0, regs...))
	if got, _ := f.last("lux/inverter_1/load_power/state"); got != "0" {
		t.Errorf("load_power = %q, want \"0\"", got)
	}
}

func TestPublisherLogsBadCRCOccasionally(t *testing.T) {
	var buf strings.Builder
	p := newTestPublisher(&fakeMQTT{}, 3)
	p.log = &pubLogger{w: &buf}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }

	p.noteBadCRC(1) // first drop logs
	p.noteBadCRC(5) // within the interval: silent
	now = now.Add(2 * dropLogInterval)
	p.noteBadCRC(9) // interval elapsed: logs again
	p.noteBadCRC(9) // no new drops: silent

	if n := strings.Count(buf.String(), "bad CRC"); n != 2 {
		t.Errorf("logged bad CRC %d times, want 2:\n%s", n, buf.String())
	}
}
