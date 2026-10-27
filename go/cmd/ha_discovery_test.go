package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// haPayload mirrors the discovery config fields the tests assert on.
type haPayload struct {
	Name              string `json:"name"`
	UniqueID          string `json:"unique_id"`
	StateTopic        string `json:"state_topic"`
	DeviceClass       string `json:"device_class"`
	StateClass        string `json:"state_class"`
	UnitOfMeasurement string `json:"unit_of_measurement"`
	AvailabilityTopic string `json:"availability_topic"`
	PayloadAvailable  string `json:"payload_available"`
	PayloadNotAvail   string `json:"payload_not_available"`
	Device            struct {
		Identifiers  []string `json:"identifiers"`
		Name         string   `json:"name"`
		Manufacturer string   `json:"manufacturer"`
	} `json:"device"`
}

func decodeHAPayload(t *testing.T, raw string) haPayload {
	t.Helper()
	var p haPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("discovery payload is not valid JSON: %v\n%s", err, raw)
	}
	return p
}

func TestHADiscoveryMessagesPayload(t *testing.T) {
	tests := []struct {
		name       string
		topic      string
		stateTopic string
		uniqueID   string
	}{
		{
			name:       "battery energy in",
			topic:      "homeassistant/sensor/lux/battery_energy_in/config",
			stateTopic: "lux/inverter_1/battery_energy_in/state",
			uniqueID:   "lux_battery_energy_in",
		},
		{
			name:       "battery energy out",
			topic:      "homeassistant/sensor/lux/battery_energy_out/config",
			stateTopic: "lux/inverter_1/battery_energy_out/state",
			uniqueID:   "lux_battery_energy_out",
		},
	}

	msgs, err := haDiscoveryMessages(haDefaultDiscoveryPrefix, "lux")
	if err != nil {
		t.Fatalf("haDiscoveryMessages: %v", err)
	}
	byTopic := make(map[string]string, len(msgs))
	for _, m := range msgs {
		byTopic[m.topic] = m.payload
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, ok := byTopic[tt.topic]
			if !ok {
				t.Fatalf("no discovery message on %q; got topics %v", tt.topic, keys(byTopic))
			}
			p := decodeHAPayload(t, raw)
			checks := []struct{ field, got, want string }{
				{"state_topic", p.StateTopic, tt.stateTopic},
				{"unique_id", p.UniqueID, tt.uniqueID},
				{"device_class", p.DeviceClass, "energy"},
				{"state_class", p.StateClass, "total_increasing"},
				{"unit_of_measurement", p.UnitOfMeasurement, "kWh"},
				{"availability_topic", p.AvailabilityTopic, "lux/inverter_1/online/state"},
				{"payload_available", p.PayloadAvailable, "true"},
				{"payload_not_available", p.PayloadNotAvail, "false"},
			}
			for _, c := range checks {
				if c.got != c.want {
					t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
				}
			}
			if p.Name == "" {
				t.Error("name is empty")
			}
			if p.Device.Name != "EG4 18kPV lux publisher" {
				t.Errorf("device.name = %q, want %q", p.Device.Name, "EG4 18kPV lux publisher")
			}
			if p.Device.Manufacturer != "lux" {
				t.Errorf("device.manufacturer = %q, want %q", p.Device.Manufacturer, "lux")
			}
			if len(p.Device.Identifiers) != 1 || p.Device.Identifiers[0] != "lux_publisher" {
				t.Errorf("device.identifiers = %v, want [lux_publisher]", p.Device.Identifiers)
			}
		})
	}
}

func TestHADiscoveryMessagesShareDevice(t *testing.T) {
	msgs, err := haDiscoveryMessages(haDefaultDiscoveryPrefix, "lux")
	if err != nil {
		t.Fatalf("haDiscoveryMessages: %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("got %d discovery messages, want at least 2", len(msgs))
	}
	first := decodeHAPayload(t, msgs[0].payload)
	seen := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		p := decodeHAPayload(t, m.payload)
		if strings.Join(p.Device.Identifiers, ",") != strings.Join(first.Device.Identifiers, ",") {
			t.Errorf("%s device.identifiers = %v, want %v (sensors must group under one device)",
				m.topic, p.Device.Identifiers, first.Device.Identifiers)
		}
		if seen[p.UniqueID] {
			t.Errorf("duplicate unique_id %q", p.UniqueID)
		}
		seen[p.UniqueID] = true
	}
}

func TestHADiscoveryUniqueIDStability(t *testing.T) {
	tests := []struct {
		name         string
		prefix       string
		wantUniqueID string // for battery_energy_in
		wantTopic    string
	}{
		{"default prefix", "lux", "lux_battery_energy_in",
			"homeassistant/sensor/lux/battery_energy_in/config"},
		{"custom prefix", "garage", "lux_battery_energy_in",
			"homeassistant/sensor/lux/battery_energy_in/config"},
		{"nested prefix stays in state topic only", "home/solar", "lux_battery_energy_in",
			"homeassistant/sensor/lux/battery_energy_in/config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := haDiscoveryMessages(haDefaultDiscoveryPrefix, tt.prefix)
			if err != nil {
				t.Fatalf("haDiscoveryMessages: %v", err)
			}
			b, err := haDiscoveryMessages(haDefaultDiscoveryPrefix, tt.prefix)
			if err != nil {
				t.Fatalf("haDiscoveryMessages: %v", err)
			}
			for i := range a {
				if a[i] != b[i] {
					t.Errorf("message %d differs between builds:\n%+v\n%+v", i, a[i], b[i])
				}
			}
			if a[0].topic != tt.wantTopic {
				t.Errorf("topic = %q, want %q", a[0].topic, tt.wantTopic)
			}
			if got := decodeHAPayload(t, a[0].payload).UniqueID; got != tt.wantUniqueID {
				t.Errorf("unique_id = %q, want %q", got, tt.wantUniqueID)
			}
			// unique_id and config topic are prefix-independent (stable identity),
			// but the state_topic must follow the configured prefix.
			wantState := tt.prefix + "/inverter_1/battery_energy_in/state"
			if got := decodeHAPayload(t, a[0].payload).StateTopic; got != wantState {
				t.Errorf("state_topic = %q, want %q", got, wantState)
			}
		})
	}
}

func TestHADiscoveryCustomDiscoveryPrefix(t *testing.T) {
	msgs, err := haDiscoveryMessages("ha", "lux")
	if err != nil {
		t.Fatalf("haDiscoveryMessages: %v", err)
	}
	for _, m := range msgs {
		if !strings.HasPrefix(m.topic, "ha/sensor/") || !strings.HasSuffix(m.topic, "/config") {
			t.Errorf("topic %q, want ha/sensor/<entity>/config", m.topic)
		}
	}
}

func TestPublisherOnConnectDiscovery(t *testing.T) {
	wantTopics := []string{
		"homeassistant/sensor/lux/battery_energy_in/config",
		"homeassistant/sensor/lux/battery_energy_out/config",
	}
	tests := []struct {
		name    string
		enabled bool
		want    int // discovery messages per connect
	}{
		{"disabled publishes no discovery", false, 0},
		{"enabled publishes sensor configs", true, len(wantTopics)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeMQTT{}
			p := newTestPublisher(f, 3)
			p.retain = false // discovery must be retained regardless of --retain
			if tt.enabled {
				p.haDiscovery = true
				p.haDiscoveryPrefix = haDefaultDiscoveryPrefix
			}

			p.onConnect()
			p.onConnect() // reconnect republishes

			var discovery []pubMsg
			for _, m := range f.msgs {
				if strings.HasPrefix(m.topic, haDefaultDiscoveryPrefix+"/") {
					discovery = append(discovery, m)
				}
			}
			if got, want := len(discovery), 2*tt.want; got != want {
				t.Fatalf("published %d discovery messages over 2 connects, want %d", got, want)
			}
			if !tt.enabled {
				return
			}
			for _, topic := range wantTopics {
				if n := f.count(topic); n != 2 {
					t.Errorf("%s published %d times, want 2 (once per connect)", topic, n)
				}
			}
			for _, m := range discovery {
				if !m.retained || m.qos != 1 {
					t.Errorf("%s published qos=%d retained=%v, want qos=1 retained=true", m.topic, m.qos, m.retained)
				}
				decodeHAPayload(t, m.payload)
			}
		})
	}
}

func TestPublishHADiscoveryFlagDefaults(t *testing.T) {
	tests := []struct {
		flag string
		want string
	}{
		{"ha-discovery", "false"},
		{"ha-discovery-prefix", haDefaultDiscoveryPrefix},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			f := publishCmd.Flags().Lookup(tt.flag)
			if f == nil {
				t.Fatalf("--%s not defined", tt.flag)
			}
			if f.DefValue != tt.want {
				t.Errorf("default --%s = %q, want %q", tt.flag, f.DefValue, tt.want)
			}
			if !strings.Contains(publishCmd.Long, "--"+tt.flag) {
				t.Errorf("publish Long help does not document --%s", tt.flag)
			}
		})
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
