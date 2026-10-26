# Register Correlation — Solar Assistant MQTT vs Live Modbus

**Date:** 2026-10-26 · **Duration:** ~2.3 h daylight capture · **Method:** paired capture

## Method

1. `python/monitor.py` polled ReadInputAll (registers 0–119) from the dongle every ~5 s,
   logging JSONL with per-register raw values + device-reported names.
2. Simultaneously logged the Solar Assistant MQTT snapshot topic tree (all `grid`/`load`/
   `pv`/`total` values) with timestamps.
3. Lag-aligned every numeric SA topic against every input register: shifted the register
   series −14 s…+14 s, kept the lag maximizing Pearson r, then least-squares fitted the
   scale `k` and computed residual. A match requires |r| ≥ 0.9 and residual ≤ ~3%
   (constants matched by exact value + plausible scale instead).

Raw captures are kept locally under `captures/` (gitignored — they contain the inverter
serial number and this repo is public). Reproduce with the correlator in the Jules
workspace repo (`scripts/mqtt-superset-correlate.py`).

## Dynamically correlated (r ≥ 0.9)

| SA topic | Register | k (fitted) | lag | r | resid |
|---|---|---|---|---|---|
| `pv_voltage_1/2/3` | i1 / i2 / i3 | 0.1 (÷10) | +2…6 s | 0.98–0.99 | ≤0.4% |
| `pv_power_1/2/3` | i7 / i8 / i9 | ≈1.0 | 2–12 s | 0.987–0.998 | ≤0.5% |
| `grid_power` | **i27 "Power From Grid"** | 1.01 | −4 s | 0.995 | 1.2% |
| `grid_power_ct` | **i27** | 1.01 | −4 s | 0.995 | 1.2% |
| `load_power` / `load_power_non-essential` | **i27** | 1.026 | −4 s | 0.995 | 1.1% |
| `grid_power_ld` | **i16 "Inverter Power"** | −1.0 (sign flip) | 0–8 s | −0.98 | ≤3% |
| `total/battery_power` | i10 "Battery Charge Power" | ≈1.0 | 0 s | 0.988 | ≤0.4% |
| `total/battery_state_of_charge` | i5(lo) | 1 | −14 s | 1.000 | 0.9% |
| `grid_voltage` | i12 | 0.1 (÷10) | 0–6 s | 0.885 | 0.1% |
| `grid_frequency` | i15 | 0.01 (÷100) | 0–8 s | 0.987 | 0.0% |
| `ac_output_voltage` | i20 "EPS Voltage R" | 0.1 (÷10) | 0–6 s | 0.934 | 0.1% |
| `ac_output_frequency` | i15 | 0.01 (÷100) | 0–8 s | 0.987 | 0.0% |

Derived (no native register): `pv_current_n = pv_power_n / pv_voltage_n`,
`battery_current = battery_power / battery_voltage` — k-fits for the current topics
collapse to the voltage/scale ratio, confirming derivation, not measurement.

**Correction (2026-10-26 evening):** the `load_power` / `load_power_non-essential` → i27
row above (and finding 2 below, "non-essential ≡ i27") was an artifact of the
passthrough window, when i16 ≈ 0. A live A/B check disproved it: SA `load_power` =
i16 + i27 − i26 (house load) and `load_power_non-essential` = −i16 + i27 (derived, not
register-backed). Likewise `shutdown_battery_voltage` / `stop_discharge_voltage` (40.0 V)
and `grid_charge_stop_voltage` (59.0 V) are holding 158 / 159 ÷10, not "i81/i82 × 0.01".

## Constant-matched

| SA topic | Register |
|---|---|
| `battery_voltage` (51.7 V flat) | i4 × 0.1 |
| `shutdown_battery_voltage` (40.0 V) | i81 × 0.01 (holding reg) |
| `stop_discharge_capacity` (20 %) | i80 × 10 (also i96 × 10) |

## Zero in Solar Assistant itself — no per-phase CTs installed

`load_power_essential_1/2`, per-leg `grid_power_ct_*`, `grid_power_ld_2` are constant 0
in the SA data for the entire capture. This is a Solar-Assistant-side absence (no
per-phase CTs on this install), **not** a locked/undocumented dongle register. There was
never anything to crack.

## Findings

1. **i27 = grid import** (device broadcast name "Power From Grid", r=0.995 vs SA
   `grid_power`, resid 1.2%). The old REGISTERS.md rows (i23/24 grid pair, i25–27 PV
   energy) were wrong for this firmware; PV energy-today is i28–i30, matching
   `go/internal/lux/registers.go`. REGISTERS.md corrected 2026-10-26.
2. **`load_power_non-essential` ≡ i27** (k=1.026): this install feeds the inverter via
   the GRID port with house passthrough — all house load is seen as grid import.
3. **i23 "EPS Frequency" equals grid frequency** when grid-tied (r=1.000 vs i15) —
   EPS output is grid-synchronized; the register is live, not zero.
4. **Dongle frozen-frame quirk:** ~12% of polls (42/348) returned an identical cache
   tuple repeated over 1.5 h (i24=300, i25=2200, i26=2520) inconsistent with live
   neighbors (i16=1 W at the same moments). Discard non-varying repeats before
   correlation or averaging.

## Parity verdict

Every SA topic worth having is addressable directly on the dongle with scale ≈1.0.
A dongle-native replacement for the Solar Assistant feed is feasible with no
register discovery left to do.
