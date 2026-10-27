package cmd

import (
	"encoding/json"
	"fmt"
)

// haDefaultDiscoveryPrefix is Home Assistant's default MQTT discovery prefix.
const haDefaultDiscoveryPrefix = "homeassistant"

// haInverterNode is the topic segment all inverter metrics live under.
const haInverterNode = "inverter_1"

// Home Assistant sensor metadata for the battery energy counters. Identity is
// fixed (not derived from --prefix) so entity_ids and config topics survive
// prefix changes; state topics always track the configured prefix.
const (
	haDeviceClassEnergy         = "energy"
	haStateClassTotalIncreasing = "total_increasing"
	haUnitKWh                   = "kWh"
	haDiscoveryNodeID           = "lux"
	haDeviceIdentifier          = "lux_publisher"
	haDeviceName                = "EG4 18kPV lux publisher"
	haManufacturer              = "lux"
	haModel                     = "18kPV"
)

// haSensor describes one state topic exposed to Home Assistant discovery.
type haSensor struct {
	metric      string // topic segment under {prefix}/inverter_1/
	name        string
	deviceClass string
	stateClass  string
	unit        string
}

// haSensors are the topics announced via discovery: exactly the energy state
// topics that exist in the MQTT publish map (i33/i34). Registers 33/34 are
// the inverter's daily-cumulative counters that reset at local midnight;
// state_class total_increasing makes HA treat that reset as a new cycle
// instead of a negative delta, so no extra reset logic is needed here.
var haSensors = []haSensor{
	{"battery_energy_in", "Battery Energy In", haDeviceClassEnergy, haStateClassTotalIncreasing, haUnitKWh},
	{"battery_energy_out", "Battery Energy Out", haDeviceClassEnergy, haStateClassTotalIncreasing, haUnitKWh},
}

type haDevice struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name"`
	Manufacturer string   `json:"manufacturer"`
	Model        string   `json:"model"`
}

// haSensorConfig is the JSON payload of a sensor discovery config message.
type haSensorConfig struct {
	Name                string   `json:"name"`
	UniqueID            string   `json:"unique_id"`
	StateTopic          string   `json:"state_topic"`
	DeviceClass         string   `json:"device_class"`
	StateClass          string   `json:"state_class"`
	UnitOfMeasurement   string   `json:"unit_of_measurement"`
	AvailabilityTopic   string   `json:"availability_topic"`
	PayloadAvailable    string   `json:"payload_available"`
	PayloadNotAvailable string   `json:"payload_not_available"`
	Device              haDevice `json:"device"`
}

// haMessage is one discovery config to publish.
type haMessage struct {
	topic   string
	payload string
}

// haDiscoveryMessages builds the discovery configs for haSensors. unique_id is
// the fixed "lux_<metric>" and the config topic is
// {discoveryPrefix}/sensor/lux/<metric>/config — both independent of --prefix,
// so HA entity identity is stable across restarts and prefix changes while
// state_topic follows the configured prefix.
func haDiscoveryMessages(discoveryPrefix, prefix string) ([]haMessage, error) {
	device := haDevice{
		Identifiers:  []string{haDeviceIdentifier},
		Name:         haDeviceName,
		Manufacturer: haManufacturer,
		Model:        haModel,
	}
	msgs := make([]haMessage, 0, len(haSensors))
	for _, s := range haSensors {
		payload, err := json.Marshal(haSensorConfig{
			Name:                s.name,
			UniqueID:            haDiscoveryNodeID + "_" + s.metric,
			StateTopic:          prefix + "/" + haInverterNode + "/" + s.metric + "/state",
			DeviceClass:         s.deviceClass,
			StateClass:          s.stateClass,
			UnitOfMeasurement:   s.unit,
			AvailabilityTopic:   prefix + "/" + haInverterNode + "/online/state",
			PayloadAvailable:    "true",
			PayloadNotAvailable: "false",
			Device:              device,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal discovery config for %s: %w", s.metric, err)
		}
		msgs = append(msgs, haMessage{
			topic:   discoveryPrefix + "/sensor/" + haDiscoveryNodeID + "/" + s.metric + "/config",
			payload: string(payload),
		})
	}
	return msgs, nil
}

// publishDiscovery publishes Home Assistant discovery configs, retained at
// QoS 1 so HA rediscovers the sensors after a broker or HA restart.
func (p *publisher) publishDiscovery() {
	msgs, err := haDiscoveryMessages(p.haDiscoveryPrefix, p.prefix)
	if err != nil {
		p.log.errorf("build HA discovery configs", err)
		return
	}
	for _, m := range msgs {
		p.await(p.mqtt.Publish(m.topic, 1, true, m.payload), m.topic)
	}
}
