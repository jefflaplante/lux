package lux

import (
	"strings"
	"testing"
)

// findMapping returns the mapping for topic on register reg, or nil.
func findMapping(table map[uint16][]MQTTMapping, reg uint16, topic string) *MQTTMapping {
	for i, m := range table[reg] {
		if m.Topic == topic {
			return &table[reg][i]
		}
	}
	return nil
}

// Expected values below come from the Solar Assistant A/B capture (2026-10-26).
func TestRegisterMQTTTopics(t *testing.T) {
	tests := []struct {
		name  string
		table map[uint16][]MQTTMapping
		reg   uint16
		topic string
		raw   uint16
		want  string
	}{
		{"ac_output_voltage from EPS voltage", InputMQTTTopics, 20, "inverter_1/ac_output_voltage", 2452, "245.2"},
		{"shutdown_battery_voltage", HoldingMQTTTopics, 158, "inverter_1/shutdown_battery_voltage", 400, "40.0"},
		{"stop_discharge_voltage", HoldingMQTTTopics, 158, "inverter_1/stop_discharge_voltage", 400, "40.0"},
		{"grid_charge_stop_voltage", HoldingMQTTTopics, 159, "inverter_1/grid_charge_stop_voltage", 590, "59.0"},
		{"temperature 42C", InputMQTTTopics, 65, "inverter_1/temperature", 42, "107.6"},
		{"temperature 43C", InputMQTTTopics, 65, "inverter_1/temperature", 43, "109.4"},
		{"temperature 0C", InputMQTTTopics, 65, "inverter_1/temperature", 0, "32.0"},
		{"temperature -1C (int16, not 117995F)", InputMQTTTopics, 65, "inverter_1/temperature", 0xFFFF, "30.2"},
		{"temperature -10C", InputMQTTTopics, 65, "inverter_1/temperature", 0xFFF6, "14.0"},
		{"grid_frequency still i15", InputMQTTTopics, 15, "inverter_1/grid_frequency", 6001, "60.0"},
		{"device_mode protocol name for 0x0C", InputMQTTTopics, 0, "inverter_1/device_mode", 0x0C, "PV Charge On-grid"},
		{"device_mode unknown 0x12", InputMQTTTopics, 0, "inverter_1/device_mode", 0x12, "Unknown(0x12)"},
		{"device_mode fallback", InputMQTTTopics, 0, "inverter_1/device_mode", 0x08, "PV Charge"},
		{"device_mode unknown", InputMQTTTopics, 0, "inverter_1/device_mode", 0x99, "Unknown(0x99)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := findMapping(tt.table, tt.reg, tt.topic)
			if m == nil {
				t.Fatalf("register %d has no mapping for %s", tt.reg, tt.topic)
			}
			if m.Format == nil {
				t.Fatalf("register %d topic %s has nil Format", tt.reg, tt.topic)
			}
			if got := m.Format(tt.raw); got != tt.want {
				t.Errorf("Format(%d) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestRemovedMQTTMappings(t *testing.T) {
	tests := []struct {
		name  string
		table map[uint16][]MQTTMapping
		reg   uint16
	}{
		{"input 14 (DCI, not output voltage)", InputMQTTTopics, 14},
		{"input 64 (internal temp, not SA temperature)", InputMQTTTopics, 64},
		{"holding 166 (switch-to-grid voltage, reads 0)", HoldingMQTTTopics, 166},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if m, ok := tt.table[tt.reg]; ok {
				t.Errorf("register %d still mapped: %+v", tt.reg, m)
			}
		})
	}
}

func TestComputedTopics(t *testing.T) {
	tests := []struct {
		name  string
		regs  map[uint16]uint16
		topic string
		want  string
	}{
		{"battery_power charging is positive", map[uint16]uint16{10: 5, 11: 3}, "total/battery_power", "2"},
		{"battery_power discharging is negative", map[uint16]uint16{10: 3, 11: 5}, "total/battery_power", "-2"},
		{"load_power includes net grid", map[uint16]uint16{16: 1550, 26: 0, 27: 410}, "inverter_1/load_power", "1960"},
		{"load_power net of export", map[uint16]uint16{16: 1453, 26: 11, 27: 500}, "inverter_1/load_power", "1942"},
		{"load_power without grid regs", map[uint16]uint16{16: 1550}, "inverter_1/load_power", "1550"},
		{"battery_temperature from input 67", map[uint16]uint16{67: 885}, "total/battery_temperature", "88.5"},
		{"grid_power unchanged", map[uint16]uint16{26: 0, 27: 410}, "inverter_1/grid_power", "410"},
		// Frozen dongle frame observed 2026-10-26: i16=1, i26=2520 gave -2519 W.
		{"load_power clamped at zero on frozen frame", map[uint16]uint16{16: 1, 26: 2520, 27: 0}, "inverter_1/load_power", "0"},
		{"battery_temperature negative (int16)", map[uint16]uint16{67: 0xFFCE}, "total/battery_temperature", "-5.0"},
		// ac_output_frequency: i23 (EPS/output freq) preferred, i15 (grid) as fallback.
		{"ac_output_frequency grid-tied uses i23", map[uint16]uint16{15: 6000, 23: 6000}, "inverter_1/ac_output_frequency", "60.0"},
		{"ac_output_frequency outage uses i23", map[uint16]uint16{15: 0, 23: 6050}, "inverter_1/ac_output_frequency", "60.5"},
		{"ac_output_frequency falls back to i15", map[uint16]uint16{15: 5990, 23: 0}, "inverter_1/ac_output_frequency", "59.9"},
		{"ac_output_frequency i15 only", map[uint16]uint16{15: 5990}, "inverter_1/ac_output_frequency", "59.9"},
		{"ac_output_frequency nothing plausible", map[uint16]uint16{15: 0, 23: 0}, "inverter_1/ac_output_frequency", "0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ComputedTopics(tt.regs)[tt.topic]
			if !ok {
				t.Fatalf("topic %s not computed", tt.topic)
			}
			if got != tt.want {
				t.Errorf("%s = %q, want %q", tt.topic, got, tt.want)
			}
		})
	}
}

func TestStatusCodesNotRenamed(t *testing.T) {
	want := map[uint16]string{
		0x00: "Standby",
		0x04: "PV On-grid",
		0x08: "PV Charge",
		0x0C: "PV Charge On-grid",
		0x10: "Battery On-grid",
	}
	for code, name := range want {
		if got := StatusCodes[code]; got != name {
			t.Errorf("StatusCodes[0x%02X] = %q, want %q", code, got, name)
		}
	}
}

func TestACOutputFrequencyNotMappedFromI15(t *testing.T) {
	if m := findMapping(InputMQTTTopics, 15, "inverter_1/ac_output_frequency"); m != nil {
		t.Error("ac_output_frequency still mapped straight from i15 (wrong during a grid outage)")
	}
}

func TestStaleDetector(t *testing.T) {
	in := func(start uint16, vals ...uint16) *Packet {
		regs := make(map[uint16]uint16, len(vals))
		for i, v := range vals {
			regs[start+uint16(i)] = v
		}
		return &Packet{RegisterType: "input", StartRegister: start, RegisterCount: uint16(len(vals)), Registers: regs}
	}
	hold := func(vals ...uint16) *Packet {
		p := in(0, vals...)
		p.RegisterType = "holding"
		return p
	}

	tests := []struct {
		name      string
		threshold int
		frames    []*Packet
		want      []bool
	}{
		{"live frames never stale", 3,
			[]*Packet{in(0, 1, 2), in(0, 1, 3), in(0, 1, 4)}, []bool{false, false, false}},
		{"third identical is stale", 3,
			[]*Packet{in(0, 7, 2520), in(0, 7, 2520), in(0, 7, 2520), in(0, 7, 2520)}, []bool{false, false, true, true}},
		{"change resets the run", 3,
			[]*Packet{in(0, 5), in(0, 5), in(0, 5), in(0, 6), in(0, 6)}, []bool{false, false, true, false, false}},
		{"blocks tracked independently", 2,
			[]*Packet{in(0, 5), in(40, 5), in(0, 5), in(40, 6)}, []bool{false, false, true, false}},
		{"holding config blocks exempt", 2,
			[]*Packet{hold(1), hold(1), hold(1)}, []bool{false, false, false}},
		{"threshold below 2 disables", 1,
			[]*Packet{in(0, 5), in(0, 5), in(0, 5)}, []bool{false, false, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewStaleDetector(tt.threshold)
			for i, f := range tt.frames {
				if got := d.Observe(f); got != tt.want[i] {
					t.Errorf("frame %d: Observe = %v, want %v", i, got, tt.want[i])
				}
			}
		})
	}
}

func TestMasterFlagTopicNames(t *testing.T) {
	want := map[int]string{
		0:  "inverter_1/eps_enable",
		4:  "inverter_1/anti_island_enable",
		6:  "inverter_1/grid_on_power_soft_start_enable",
		7:  "inverter_1/grid_charge",
		10: "inverter_1/forced_discharge",
		11: "inverter_1/charge_priority",
		15: "inverter_1/export_to_grid",
	}
	for bit, topic := range want {
		if got := MasterFlagTopics[bit]; got != topic {
			t.Errorf("MasterFlagTopics[%d] (%s) = %q, want %q", bit, MasterFunctionBits[bit], got, topic)
		}
	}
	for bit, topic := range MasterFlagTopics {
		if strings.Contains(topic, "peak_shaving") {
			t.Errorf("bit %d (%s) still published as %q", bit, MasterFunctionBits[bit], topic)
		}
	}
}
