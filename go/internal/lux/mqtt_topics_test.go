package lux

import "testing"

func TestMQTTInputTopicMappings(t *testing.T) {
	tests := []struct {
		name       string
		reg        uint16
		wantTopics []string
	}{
		{"battery charge today", 33, []string{"inverter_1/battery_energy_in"}},
		{"battery discharge today", 34, []string{"inverter_1/battery_energy_out"}},
	}
	for _, tc := range tests {
		maps, ok := InputMQTTTopics[tc.reg]
		if !ok {
			t.Fatalf("%s: InputMQTTTopics[%d] missing", tc.name, tc.reg)
		}
		if len(maps) != len(tc.wantTopics) {
			t.Fatalf("%s: InputMQTTTopics[%d] has %d mappings, want %d", tc.name, tc.reg, len(maps), len(tc.wantTopics))
		}
		for i, m := range maps {
			if m.Topic != tc.wantTopics[i] {
				t.Errorf("%s: InputMQTTTopics[%d][%d].Topic = %q, want %q", tc.name, tc.reg, i, m.Topic, tc.wantTopics[i])
			}
		}
	}
}

func TestMQTTFmtDiv10ThroughMapping(t *testing.T) {
	tests := []struct {
		name string
		reg  uint16
		raw  uint16
		want string
	}{
		{"charge today example", 33, 232, "23.2"},
		{"discharge today example", 34, 199, "19.9"},
		{"zero", 33, 0, "0.0"},
		{"u16 boundary", 34, 65535, "6553.5"},
	}
	for _, tc := range tests {
		maps, ok := InputMQTTTopics[tc.reg]
		if !ok {
			t.Fatalf("%s: InputMQTTTopics[%d] missing", tc.name, tc.reg)
		}
		if maps[0].Format == nil {
			t.Fatalf("%s: InputMQTTTopics[%d].Format is nil", tc.name, tc.reg)
		}
		if got := maps[0].Format(tc.raw); got != tc.want {
			t.Errorf("%s: Format(%d) = %q, want %q", tc.name, tc.raw, got, tc.want)
		}
	}
}

func TestMQTTEnergyScaleMatchesRegisterDefs(t *testing.T) {
	for _, reg := range []uint16{33, 34} {
		def, ok := InputRegisters[reg]
		if !ok {
			t.Fatalf("InputRegisters[%d] missing", reg)
		}
		if def.Divisor != 10 {
			t.Errorf("InputRegisters[%d].Divisor = %v, want 10 (mapping uses fmtDiv10; metadata must agree)", reg, def.Divisor)
		}
	}
}
