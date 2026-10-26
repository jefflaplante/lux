package lux

import "testing"

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
		{"device_mode Solar/Battery", InputMQTTTopics, 0, "inverter_1/device_mode", 0x0C, "Solar/Battery"},
		{"device_mode Solar/Grid", InputMQTTTopics, 0, "inverter_1/device_mode", 0x12, "Solar/Grid"},
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
