package lux

import "fmt"

// MQTTMapping maps a register to one or more Solar Assistant MQTT topics.
type MQTTMapping struct {
	// Topic path relative to prefix, e.g. "inverter_1/pv_voltage_1".
	// The publisher appends "/state" when publishing.
	Topic string

	// Format converts the raw register value to the published string.
	// If nil, the raw value is published as a decimal string.
	Format func(raw uint16) string
}

func fmtDiv10(raw uint16) string  { return fmt.Sprintf("%.1f", float64(raw)/10) }
func fmtDiv100(raw uint16) string { return fmt.Sprintf("%.1f", float64(raw)/100) }
func fmtRaw(raw uint16) string    { return fmt.Sprintf("%d", raw) }

// fmtCtoF converts a signed integer °C reading to °F (Solar Assistant reports °F).
func fmtCtoF(raw uint16) string { return fmt.Sprintf("%.1f", float64(int16(raw))*1.8+32) }
func fmtTime(raw uint16) string {
	hour := raw & 0xFF
	min := raw >> 8
	return fmt.Sprintf("%02d:%02d", hour, min)
}

func fmtStatus(raw uint16) string {
	if name, ok := StatusCodes[raw]; ok {
		return name
	}
	return fmt.Sprintf("Unknown(0x%02X)", raw)
}

// InputMQTTTopics maps input register numbers to their Solar Assistant topic(s).
var InputMQTTTopics = map[uint16][]MQTTMapping{
	0:  {{"inverter_1/device_mode", fmtStatus}},
	1:  {{"inverter_1/pv_voltage_1", fmtDiv10}},
	2:  {{"inverter_1/pv_voltage_2", fmtDiv10}},
	3:  {{"inverter_1/pv_voltage_3", fmtDiv10}},
	4:  {{"inverter_1/battery_voltage", fmtDiv10}},
	7:  {{"inverter_1/pv_power_1", fmtRaw}},
	8:  {{"inverter_1/pv_power_2", fmtRaw}},
	9:  {{"inverter_1/pv_power_3", fmtRaw}},
	12: {{"inverter_1/grid_voltage", fmtDiv10}},
	15: {{"inverter_1/grid_frequency", fmtDiv100}},    // ac_output_frequency: see ComputedTopics
	20: {{"inverter_1/ac_output_voltage", fmtDiv10}},  // EPS Voltage R
	33: {{"inverter_1/battery_energy_in", fmtDiv10}},  // Echg_day, 0.1 kWh
	34: {{"inverter_1/battery_energy_out", fmtDiv10}}, // Edischg_day, 0.1 kWh
	65: {{"inverter_1/temperature", fmtCtoF}},         // Radiator 1 Temp, °C → °F
	// Register 106 removed: inverter-estimated battery current is unreliable
	// (reports non-zero when both BMS units read 0A). Battery current is now
	// computed in ComputedTopics from charge/discharge power and voltage.
}

// HoldingMQTTTopics maps holding register numbers to their Solar Assistant topic(s).
var HoldingMQTTTopics = map[uint16][]MQTTMapping{
	68: {{"inverter_1/grid_charge_slot_1_start", fmtTime}},
	69: {{"inverter_1/grid_charge_slot_1_end", fmtTime}},
	70: {{"inverter_1/grid_charge_slot_2_start", fmtTime}},
	71: {{"inverter_1/grid_charge_slot_2_end", fmtTime}},
	72: {{"inverter_1/grid_charge_slot_3_start", fmtTime}},
	73: {{"inverter_1/grid_charge_slot_3_end", fmtTime}},

	76: {{"inverter_1/charge_first_slot_1_start", fmtTime}},
	77: {{"inverter_1/charge_first_slot_1_end", fmtTime}},
	78: {{"inverter_1/charge_first_slot_2_start", fmtTime}},
	79: {{"inverter_1/charge_first_slot_2_end", fmtTime}},
	80: {{"inverter_1/charge_first_slot_3_start", fmtTime}},
	81: {{"inverter_1/charge_first_slot_3_end", fmtTime}},

	84: {{"inverter_1/forced_discharge_slot_1_start", fmtTime}},
	85: {{"inverter_1/forced_discharge_slot_1_end", fmtTime}},
	86: {{"inverter_1/forced_discharge_slot_2_start", fmtTime}},
	87: {{"inverter_1/forced_discharge_slot_2_end", fmtTime}},
	88: {{"inverter_1/forced_discharge_slot_3_start", fmtTime}},
	89: {{"inverter_1/forced_discharge_slot_3_end", fmtTime}},

	103: {{"inverter_1/export_power_rate", fmtRaw}},
	105: {{"inverter_1/stop_discharge_capacity", fmtRaw}},
	125: {{"inverter_1/shutdown_battery_capacity", fmtRaw}},
	160: {{"inverter_1/grid_charge_start_capacity", fmtRaw}},
	161: {{"inverter_1/grid_charge_stop_capacity", fmtRaw}},
	158: {
		{"inverter_1/shutdown_battery_voltage", fmtDiv10},
		{"inverter_1/stop_discharge_voltage", fmtDiv10},
	},
	159: {{"inverter_1/grid_charge_stop_voltage", fmtDiv10}},
}

// MasterFlagTopics maps bit positions in register 21 to Solar Assistant topic names.
var MasterFlagTopics = map[int]string{
	7:  "inverter_1/grid_charge",                     // ACCharge
	10: "inverter_1/forced_discharge",                // ForcedDischarge
	11: "inverter_1/charge_priority",                 // ChargePriority
	15: "inverter_1/export_to_grid",                  // FeedInGrid
	0:  "inverter_1/eps_enable",                      // EPS
	6:  "inverter_1/grid_on_power_soft_start_enable", // GridOnPowerSS (was mislabeled grid_peak_shaving)
	4:  "inverter_1/anti_island_enable",              // AntiIsland (was mislabeled generator_peak_shaving)
}

// ComputedTopics generates derived topic→value pairs from a full set of input registers.
// Only computes values when all required source registers are present.
func ComputedTopics(regs map[uint16]uint16) map[string]string {
	result := make(map[string]string)

	// SOC from register 5 (low byte)
	if v, ok := regs[5]; ok {
		soc := v & 0xFF
		result["total/battery_state_of_charge"] = fmt.Sprintf("%d", soc)
	}

	// Total PV power = pv1 + pv2 + pv3
	if p1, ok1 := regs[7]; ok1 {
		p2, _ := regs[8]
		p3, _ := regs[9]
		result["inverter_1/pv_power"] = fmt.Sprintf("%d", int(p1)+int(p2)+int(p3))
	}

	// Battery power = charge - discharge (positive = charging, negative = discharging; SA parity)
	chg, chgOk := regs[10]
	dis, disOk := regs[11]
	if chgOk && disOk {
		bp := int(chg) - int(dis)
		result["total/battery_power"] = fmt.Sprintf("%d", bp)
	}

	// Grid power = import - export (positive = importing)
	exp, expOk := regs[26]
	imp, impOk := regs[27]
	if expOk && impOk {
		gp := int(imp) - int(exp)
		result["inverter_1/grid_power"] = fmt.Sprintf("%d", gp)
		result["inverter_1/grid_power_ct"] = fmt.Sprintf("%d", gp)
	}

	// Load power = inverter power + net grid import (house load; SA parity).
	// Falls back to inverter power alone if grid registers are missing.
	if v, ok := regs[16]; ok {
		lp := int(v)
		if expOk && impOk {
			lp += int(imp) - int(exp)
		}
		// House load can't be negative. A frozen dongle frame once produced
		// i16=1, i26=2520 → -2519 W; clamp rather than publish nonsense.
		if lp < 0 {
			lp = 0
		}
		result["inverter_1/load_power"] = fmt.Sprintf("%d", lp)
	}

	// Battery current = (charge_power - discharge_power) / battery_voltage
	// Derived from registers 10, 11, 4 which match BMS readings.
	// Positive = charging, negative = discharging.
	if voltage, vOk := regs[4]; vOk && voltage > 0 {
		chgP, chgPOk := regs[10]
		disP, disPOk := regs[11]
		if chgPOk && disPOk {
			current := (float64(chgP) - float64(disP)) / (float64(voltage) / 10)
			result["inverter_1/battery_current"] = fmt.Sprintf("%.1f", current)
		}
	}

	// Battery temperature from register 67 (°F×10). SA's total/battery_temperature
	// (constant 242.6) is corrupt on this install; we intentionally report the sensor.
	if v, ok := regs[67]; ok {
		result["total/battery_temperature"] = fmt.Sprintf("%.1f", float64(int16(v))/10)
	}

	// AC output frequency. i15 is the *grid* frequency and is wrong during an
	// outage, when the inverter is the one making the output. i23 (EPS/output
	// frequency) tracks i15 exactly while grid-tied (r=1.000, CORRELATION.md)
	// and keeps reporting the real output when islanded, so prefer it. Fall
	// back to i15 only when i23 is implausible (e.g. 0 with EPS disabled).
	f23, ok23 := regs[23]
	f15, ok15 := regs[15]
	switch {
	case ok23 && plausibleFreq(f23):
		result["inverter_1/ac_output_frequency"] = fmtDiv100(f23)
	case ok15 && plausibleFreq(f15):
		result["inverter_1/ac_output_frequency"] = fmtDiv100(f15)
	case ok23:
		result["inverter_1/ac_output_frequency"] = fmtDiv100(f23)
	case ok15:
		result["inverter_1/ac_output_frequency"] = fmtDiv100(f15)
	}

	return result
}

// plausibleFreq reports whether a ÷100 Hz raw value is a real AC frequency
// (45-65 Hz covers 50 Hz and 60 Hz grids with wide tolerance).
func plausibleFreq(raw uint16) bool {
	return raw >= 4500 && raw <= 6500
}
