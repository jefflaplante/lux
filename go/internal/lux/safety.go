package lux

import (
	"fmt"
	"sync"
	"time"
)

// WriteGuard provides safety checks for register writes.
// It enforces rate limiting and logs all write operations.
type WriteGuard struct {
	mu              sync.Mutex
	writes          []time.Time
	maxWritesPerMin int
	writeLog        []WriteLogEntry
	maxLogEntries   int
}

// WriteLogEntry records a single write operation.
type WriteLogEntry struct {
	Timestamp time.Time
	Register  uint16
	OldValue  uint16
	NewValue  uint16
	Success   bool
}

// NewWriteGuard creates a new WriteGuard with default settings.
// Default: max 10 writes per minute, keep last 100 log entries.
func NewWriteGuard() *WriteGuard {
	return &WriteGuard{
		maxWritesPerMin: 10,
		maxLogEntries:   100,
	}
}

// Check validates that a write to the given register is allowed.
// Returns an error if the register is protected or rate limit exceeded.
func (g *WriteGuard) Check(reg uint16) error {
	// Check protected registers
	if IsProtectedRegister(reg) {
		return fmt.Errorf("register %d is protected (grid protection range 25-53)", reg)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// Clean up old writes (older than 1 minute)
	cutoff := time.Now().Add(-time.Minute)
	var recent []time.Time
	for _, t := range g.writes {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	g.writes = recent

	// Check rate limit
	if len(g.writes) >= g.maxWritesPerMin {
		return fmt.Errorf("rate limit exceeded: %d writes in the last minute (max %d)",
			len(g.writes), g.maxWritesPerMin)
	}

	return nil
}

// Record logs a write operation and updates the rate limit counter.
func (g *WriteGuard) Record(reg uint16, oldVal, newVal uint16, success bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()

	// Add to rate limit tracking
	g.writes = append(g.writes, now)

	// Add to log
	entry := WriteLogEntry{
		Timestamp: now,
		Register:  reg,
		OldValue:  oldVal,
		NewValue:  newVal,
		Success:   success,
	}
	g.writeLog = append(g.writeLog, entry)

	// Trim log if too long
	if len(g.writeLog) > g.maxLogEntries {
		g.writeLog = g.writeLog[len(g.writeLog)-g.maxLogEntries:]
	}
}

// RecentWrites returns the most recent write log entries (up to n).
func (g *WriteGuard) RecentWrites(n int) []WriteLogEntry {
	g.mu.Lock()
	defer g.mu.Unlock()

	if n > len(g.writeLog) {
		n = len(g.writeLog)
	}
	if n <= 0 {
		return nil
	}

	result := make([]WriteLogEntry, n)
	copy(result, g.writeLog[len(g.writeLog)-n:])
	return result
}

// WritesInLastMinute returns the count of writes in the last minute.
func (g *WriteGuard) WritesInLastMinute() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	cutoff := time.Now().Add(-time.Minute)
	count := 0
	for _, t := range g.writes {
		if t.After(cutoff) {
			count++
		}
	}
	return count
}

// writeRule bounds the raw values accepted for a writable holding register.
type writeRule struct {
	min, max uint16
	time     bool // schedule time: low byte hour 0-23, high byte minute 0-59
}

var (
	rulePercent = writeRule{min: 0, max: 100}
	ruleTime    = writeRule{time: true}
)

// writableHolding is the allowlist of holding registers that may be written,
// taken from the "Write (control)" set in REGISTERS.md: mode flags, power
// rates, the three schedules, and SOC limits. Everything else — identity,
// clock, comm address, grid protection, and every battery voltage setpoint
// (99, 100, 144, 158, 159, 162, 166, 169) — is rejected.
var writableHolding = map[uint16]writeRule{
	21: {min: 0, max: 0xFFFF}, // master function bitmask; set-mode gates protection bits

	64: rulePercent, // charge power %
	65: rulePercent, // discharge power %

	66: rulePercent, 67: rulePercent, // AC charge power %, SOC limit
	68: ruleTime, 69: ruleTime, 70: ruleTime, 71: ruleTime, 72: ruleTime, 73: ruleTime,

	74: rulePercent, 75: rulePercent, // charge priority power %, SOC limit
	76: ruleTime, 77: ruleTime, 78: ruleTime, 79: ruleTime, 80: ruleTime, 81: ruleTime,

	82: rulePercent, 83: rulePercent, // forced discharge power %, SOC floor
	84: ruleTime, 85: ruleTime, 86: ruleTime, 87: ruleTime, 88: ruleTime, 89: ruleTime,

	103: rulePercent, // grid feed-in power limit %
	105: rulePercent, // discharge cutoff SOC (on-grid)
	125: rulePercent, // discharge cutoff SOC (EPS)
	160: rulePercent, // AC charge start SOC
	161: rulePercent, // AC charge end SOC
}

// IsWritableRegister reports whether reg is on the holding-register write allowlist.
func IsWritableRegister(reg uint16) bool {
	_, ok := writableHolding[reg]
	return ok
}

// ValidateWrite checks that reg is on the write allowlist and value is within
// its permitted range. Returns nil if the write is allowed.
func ValidateWrite(reg, value uint16) error {
	if IsProtectedRegister(reg) {
		return fmt.Errorf("register %d is protected (grid protection range 25-53)", reg)
	}
	rule, ok := writableHolding[reg]
	if !ok {
		return fmt.Errorf("register %d is not writable (not on the write allowlist)", reg)
	}
	if rule.time {
		hour, min := value&0xFF, value>>8
		if hour > 23 || min > 59 {
			return fmt.Errorf("register %d: invalid schedule time %d (hour %d, minute %d)", reg, value, hour, min)
		}
		return nil
	}
	if value < rule.min || value > rule.max {
		return fmt.Errorf("register %d: value %d out of range %d-%d", reg, value, rule.min, rule.max)
	}
	return nil
}

// MasterProtectionMask covers the register 21 bits that enable grid/safety
// protection functions. Clearing any of them disables a protection.
const MasterProtectionMask = MasterAntiIsland | MasterNeutralDetect | MasterISO | MasterGFCI | MasterDCI

// ClearedProtectionBits returns the names of protection bits that are set in
// old but clear in new, in bit order. Returns nil if no protection is disabled.
func ClearedProtectionBits(old, new uint16) []string {
	cleared := old &^ new & MasterProtectionMask
	var names []string
	for bit := 0; bit < 16; bit++ {
		if cleared&(1<<bit) != 0 {
			names = append(names, MasterFunctionBits[bit])
		}
	}
	return names
}
