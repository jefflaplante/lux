# EG4 Modbus — Direct Inverter Control for Conduit

A Go library and Conduit skill for direct Modbus communication with the EG4 18kPV inverter via the WiFi dongle's TCP port 8000.

## Goal

Read and write inverter registers — battery charge/discharge schedules, modes, power limits — without depending on SolarAssistant, lxp-bridge, or the EG4 cloud.

## Architecture

```
CLI / Library
  └── eg4-modbus Go library
        └── TCP:8000 → WiFi Dongle → RS485/Modbus RTU → EG4 18kPV Inverter
```

The library handles the LuxPower wire protocol (TCP framing around Modbus RTU). The Conduit skill exposes high-level actions like `set_charge_schedule`, `get_battery_status`, `set_mode`.

## Status

**Phase: Research & Design** — protocol fully reverse-engineered from lxp-bridge source.

### Breaking changes

- **`lux write` / `set-mode` / `set-schedule`: a declined or non-interactive
  confirmation now exits nonzero** (`cancelled at confirmation prompt`).
  Previously `--json` with a closed stdin printed `Cancelled.` and exited 0,
  so automation could not distinguish a skipped write from a successful one.
  Scripts that rely on exit 0 must pass `--yes` explicitly.
- **The frozen-frame detector only observes input registers that feed a
  published topic.** Blocks with no topic mapping are exempt by definition.
  (`--stale-threshold 0` still disables the detector entirely.)

## Key Documents

- `PROTOCOL.md` — Full wire protocol specification
- `REGISTERS.md` — Register map with read/write capabilities
- `DESIGN.md` — Implementation plan and phasing
