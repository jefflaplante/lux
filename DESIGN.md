# EG4 Modbus — Implementation Design

## Architecture

```
┌─────────────────────────────────────────────────────┐
│ lux CLI                                             │
│  ┌─────────────────────┐                            │
│  │ cmd/                 │ ← Cobra commands          │
│  │  ├─ read, write      │    (register ops)         │
│  │  ├─ status, monitor  │    (live data)            │
│  │  ├─ publish          │    (MQTT streaming)       │
│  │  └─ schedules, modes │    (configuration)        │
│  └──────────┬──────────┘                            │
│             │                                       │
│  ┌──────────▼──────────┐                            │
│  │ internal/lux         │ ← Protocol library        │
│  │  ├─ protocol.go      │    (TCP framing, CRC-16)  │
│  │  ├─ registers.go     │    (register definitions) │
│  │  ├─ client.go        │    (TCP client, Listen)   │
│  │  ├─ safety.go        │    (write guards)         │
│  │  └─ mqtt_topics.go   │    (SA topic mappings)    │
│  └──────────┬──────────┘                            │
└─────────────┼───────────────────────────────────────┘
              │ TCP :8000
      ┌───────▼───────┐
      │ WiFi Dongle    │ LuxPower TCP v5
      └───────┬───────┘
              │ RS485/Modbus RTU
      ┌───────▼───────┐
      │ EG4 18kPV      │
      └───────────────┘
```

## Current Implementation

### Library (`internal/lux`)

| File | Purpose |
|------|---------|
| `protocol.go` | TCP frame encoding/decoding, CRC-16/MODBUS, packet builders (ReadHold, ReadInput, WriteSingle) |
| `registers.go` | Register metadata, human-readable names, formatting, schedule register offsets |
| `client.go` | TCP connection management, `Listen()` for passive monitoring, active read/write methods |
| `safety.go` | `WriteGuard` — rate limiting (10/min), protected register blocking (25-53), audit logging |
| `mqtt_topics.go` | Solar Assistant-compatible topic mappings for input/holding registers, computed topics |

### CLI Commands (`cmd/`)

| Command | Description |
|---------|-------------|
| `lux read <reg>` | Read holding registers (prefix with `i` for input, e.g., `i0-39`) |
| `lux write <reg> <val>` | Write single holding register (with safety checks) |
| `lux status` | Display current inverter state (SOC, PV power, battery, grid) |
| `lux monitor` | Live updating display of key metrics |
| `lux dump` | Raw hex dump of all packets from dongle |
| `lux publish` | Stream data to MQTT broker (Solar Assistant compatible) |
| `lux registers` | List all known registers with current values |
| `lux schedules` | Display all charge/discharge schedule periods |
| `lux read-schedule <type> <n>` | Read specific schedule (ac-charge, forced-discharge, etc.) |
| `lux set-schedule <type> <n> <time>` | Configure schedule period with optional --power, --soc |
| `lux set-mode <mode> <on\|off>` | Toggle mode flags in register 21 (ac-charge, forced-discharge, etc.) |

### Configuration

All flags can be set via environment variables with `LUX_` prefix:

```bash
LUX_HOST=192.168.1.100      # WiFi dongle IP
LUX_PORT=8000             # TCP port
LUX_DATALOG=AAAAAAAAAA    # Dongle serial (10 char)
LUX_SERIAL=1234567890     # Inverter serial (10 char)
```

For MQTT publishing:
```bash
LUX_BROKER=tcp://192.168.1.50:1883
LUX_PREFIX=solar_assistant
LUX_RETAIN=true
LUX_QUIET=false
```

---

## Safety Features

| Feature | Implementation |
|---------|----------------|
| Protected registers | Writes to 25-53 (grid protection) blocked at library level |
| Rate limiting | Max 10 writes/minute enforced by WriteGuard |
| Audit logging | All write attempts logged with timestamp, old/new values, success |
| Graceful shutdown | Signal handler closes connections cleanly, second Ctrl-C force exits |

---

## MQTT Publishing

The `publish` command streams inverter data to an MQTT broker using Solar Assistant-compatible topics:

```
{prefix}/inverter_1/battery_soc/state          → "85"
{prefix}/inverter_1/pv_power/state             → "4523"
{prefix}/inverter_1/grid_power/state           → "-1200"
{prefix}/inverter_1/battery_energy_in/state    → "23.2"  (i33 ÷10 kWh)
{prefix}/inverter_1/battery_energy_out/state   → "19.9"  (i34 ÷10 kWh)
{prefix}/inverter_1/ac_charge_enabled/state    → "true"
{prefix}/inverter_1/online/state               → "true" (LWT: "false")
{prefix}/inverter_1/last_seen/state            → "2024-03-26T12:34:56Z"
```

Features:
- Auto-reconnect with exponential backoff
- Last Will and Testament for online status
- Computed topics (total PV power, battery power direction)
- Bitmask expansion for mode flags

---

## Protocol Notes

The LuxPower TCP v5 protocol wraps Modbus RTU frames:

```
┌────────┬────────┬─────────┬────────┬───────────┬───────┐
│ Header │ Datalog│ Serial  │ Control│ Modbus RTU│ CRC16 │
│ (6B)   │ (10B)  │ (10B)   │ (2B)   │ (var)     │ (2B)  │
└────────┴────────┴─────────┴────────┴───────────┴───────┘
```

Key functions:
- `0x03` ReadHold — Read holding registers
- `0x04` ReadInput — Read input registers
- `0x06` WriteSingle — Write single holding register

The dongle broadcasts input register packets every ~8 seconds. Active requests (ReadHold/ReadInput) get immediate responses.

---

## Future Considerations

- **Home Assistant integration** — MQTT discovery for auto-configuration
- **Prometheus metrics** — `/metrics` endpoint for Grafana dashboards
- **Schedule optimization** — Time-of-use rate awareness
- **Multi-inverter support** — Different serials on same network

---

## Reference

| Resource | Notes |
|----------|-------|
| `PROTOCOL.md` | Detailed packet format documentation |
| `REGISTERS.md` | Complete register map with addresses and meanings |
| `HANDOFF.md` | Implementation notes and decisions |
| [lxp-bridge](https://github.com/celsworth/lxp-bridge) | Original Rust implementation (protocol reference) |
| [eg4-bridge](https://github.com/jaredmauch/eg4-bridge) | Maintained fork |
