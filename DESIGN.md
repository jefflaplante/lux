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
| `safety.go` | Write allowlist + per-register ranges (`ValidateWrite`), protection-bit mask for reg 21, `WriteGuard` — per-process rate limiting (10/min), protected register blocking (25-53), in-memory write log |
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
```

The dongle and inverter serials are **not** configurable: requests use fixed placeholder
serials (`AAAAAAAAAA` / `1234567890`, see `cmd/root.go`). There is no `LUX_DATALOG` or
`LUX_SERIAL`.

For MQTT publishing:
```bash
LUX_BROKER=tcp://192.168.1.50:1883
LUX_PREFIX=lux                 # default; "solar_assistant" would overwrite SA's own topics
LUX_MQTT_CLIENT_ID=            # default lux-publish-<hostname>-<pid>
LUX_STALE_THRESHOLD=3          # identical input frames before a block is treated as frozen
LUX_RETAIN=true
LUX_QUIET=false
```

---

## Safety Features

| Feature | Implementation |
|---------|----------------|
| Write allowlist | `Client.WriteSingle` rejects any holding register not on the allowlist (mode flags, power rates, schedules, SOC limits) and out-of-range values; comm address and battery voltage setpoints are not writable |
| Protected registers | Writes to 25-53 (grid protection) blocked at library level |
| Protection bits | `set-mode` refuses to clear AntiIsland / NeutralDetect / ISO / GFCI / DCI without `--allow-protection-disable` |
| Confirmation | Every write prompts; only `--yes` skips it (`--json` does not) |
| Verification | Writes are read back on the write connection; a missing or mismatched read-back exits nonzero |
| Rate limiting | Max 10 writes/minute, **per process** — `WriteGuard` is in memory and starts empty on every CLI invocation, so this caps a single command, not writes across invocations |
| Write log | **In memory only** — `WriteGuard` keeps the last 100 attempts (timestamp, old/new value, success) for the life of the process; nothing is persisted, so there is no audit trail across runs |
| Graceful shutdown | Signal handler closes the inverter connection; the publisher then publishes offline, drains, and disconnects; second Ctrl-C force exits |

---

## MQTT Publishing

The `publish` command streams inverter data to an MQTT broker using Solar Assistant-compatible topics:

```
{prefix}/total/battery_state_of_charge/state   → "85"
{prefix}/inverter_1/pv_power/state             → "4523"
{prefix}/inverter_1/grid_power/state           → "-1200"
{prefix}/inverter_1/grid_charge/state          → "true"
{prefix}/inverter_1/online/state               → "true" (LWT: "false")
{prefix}/inverter_1/last_seen/state            → "2024-03-26T12:34:56Z"
```

Features:
- Auto-reconnect with exponential backoff
- Last Will and Testament for online status, republished on every broker reconnect
- Computed topics (total PV power, battery power direction)
- Bitmask expansion for mode flags
- Bad-CRC frames dropped; frozen (identical repeated) input frames skipped and excluded from `last_seen`

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
| [lxp-bridge](https://github.com/celsworth/lxp-bridge) | Original Rust implementation (protocol reference) |
| [eg4-bridge](https://github.com/jaredmauch/eg4-bridge) | Maintained fork |
