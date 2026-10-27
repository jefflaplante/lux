package lux

import "testing"

func TestWriteGuardRecentWrites(t *testing.T) {
	guard := NewWriteGuard()

	// No writes yet
	writes := guard.RecentWrites(5)
	if len(writes) != 0 {
		t.Errorf("RecentWrites(5) with no writes = %d entries, want 0", len(writes))
	}

	// Add some writes
	guard.Record(60, 0, 100, true)
	guard.Record(64, 50, 80, true)
	guard.Record(67, 0, 50, false)

	// Request more than available
	writes = guard.RecentWrites(10)
	if len(writes) != 3 {
		t.Errorf("RecentWrites(10) = %d entries, want 3", len(writes))
	}

	// Request fewer
	writes = guard.RecentWrites(2)
	if len(writes) != 2 {
		t.Errorf("RecentWrites(2) = %d entries, want 2", len(writes))
	}
	// Should be the last 2 entries
	if writes[0].Register != 64 {
		t.Errorf("RecentWrites[0].Register = %d, want 64", writes[0].Register)
	}
	if writes[1].Register != 67 {
		t.Errorf("RecentWrites[1].Register = %d, want 67", writes[1].Register)
	}

	// Request 0
	writes = guard.RecentWrites(0)
	if writes != nil {
		t.Errorf("RecentWrites(0) = %v, want nil", writes)
	}

	// Negative
	writes = guard.RecentWrites(-1)
	if writes != nil {
		t.Errorf("RecentWrites(-1) = %v, want nil", writes)
	}
}

func TestWritesInLastMinute(t *testing.T) {
	guard := NewWriteGuard()

	if got := guard.WritesInLastMinute(); got != 0 {
		t.Errorf("WritesInLastMinute() = %d, want 0", got)
	}

	guard.Record(60, 0, 100, true)
	guard.Record(64, 50, 80, true)

	if got := guard.WritesInLastMinute(); got != 2 {
		t.Errorf("WritesInLastMinute() = %d, want 2", got)
	}
}

func TestWriteGuardLogTrim(t *testing.T) {
	guard := &WriteGuard{
		maxWritesPerMin: 200, // high limit so we don't hit rate limit
		maxLogEntries:   5,
	}

	// Write more than maxLogEntries
	for i := 0; i < 10; i++ {
		guard.Record(60, 0, uint16(i), true)
	}

	writes := guard.RecentWrites(100)
	if len(writes) != 5 {
		t.Errorf("After 10 records with maxLog=5, got %d entries, want 5", len(writes))
	}
	// Should have the last 5 entries (values 5-9)
	if writes[0].NewValue != 5 {
		t.Errorf("First entry NewValue = %d, want 5", writes[0].NewValue)
	}
}

func TestWriteLogEntry(t *testing.T) {
	guard := NewWriteGuard()
	guard.Record(60, 100, 200, true)

	writes := guard.RecentWrites(1)
	if len(writes) != 1 {
		t.Fatal("Expected 1 write log entry")
	}
	entry := writes[0]
	if entry.Register != 60 || entry.OldValue != 100 || entry.NewValue != 200 || !entry.Success {
		t.Errorf("Entry = {Reg:%d Old:%d New:%d Success:%v}, want {60 100 200 true}",
			entry.Register, entry.OldValue, entry.NewValue, entry.Success)
	}
	if entry.Timestamp.IsZero() {
		t.Error("Entry timestamp should not be zero")
	}
}

func TestValidateWrite(t *testing.T) {
	tests := []struct {
		name    string
		reg     uint16
		value   uint16
		wantErr bool
	}{
		{"master flags any bitmask", 21, 0xFFFF, false},
		{"charge power 100%", 64, 100, false},
		{"charge power 101%", 64, 101, true},
		{"charge priority power 4000W", 74, 4000, false}, // watts, not % — inverter's own value
		{"charge priority power 18001", 74, 18001, true},
		{"forced discharge 120W", 82, 120, false},     // morning value
		{"forced discharge 4000W", 82, 4000, false},   // inverter's own current value
		{"forced discharge 18000W", 82, 18000, false}, // 18kPV max
		{"forced discharge 18001W", 82, 18001, true},
		{"discharge power 0%", 65, 0, false},
		{"ac charge soc 80", 67, 80, false},
		{"ac charge soc 150", 67, 150, true},
		{"schedule time 22:30", 68, (30 << 8) | 22, false},
		{"schedule time 00:00", 89, 0, false},
		{"schedule hour 24", 68, 24, true},
		{"schedule minute 60", 76, 60 << 8, true},
		{"feed-in limit 100", 103, 100, false},
		{"discharge cutoff soc 10", 105, 10, false},
		{"eps cutoff soc 101", 125, 101, true},
		{"ac charge start soc 20", 160, 20, false},
		{"ac charge end soc 101", 161, 101, true},
		// Not on the allowlist, regardless of value.
		{"comm address", 15, 1, true},
		{"lead-acid charge voltage", 99, 560, true},
		{"lead-acid cutoff voltage", 100, 480, true},
		{"floating voltage", 144, 540, true},
		{"shutdown voltage", 158, 400, true},
		{"grid charge stop voltage", 159, 590, true},
		{"warning voltage", 162, 480, true},
		{"switch-to-grid voltage", 166, 480, true},
		{"on-grid EOD voltage", 169, 480, true},
		{"clock", 12, 0x031A, true},
		{"grid protection", 30, 100, true},
		{"model", 0, 1, true},
		{"undefined register", 200, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWrite(tt.reg, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateWrite(%d, %d) err = %v, wantErr %v", tt.reg, tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestClearedProtectionBits(t *testing.T) {
	tests := []struct {
		name     string
		old, new uint16
		want     []string
	}{
		{"no change", 0x8FC3, 0x8FC3, nil},
		{"clear ac charge only", MasterACChargeEnable | MasterAntiIsland, MasterAntiIsland, nil},
		{"enable gfci", 0, MasterGFCI, nil},
		{"clear anti-island", MasterAntiIsland, 0, []string{"AntiIsland"}},
		{"clear gfci", MasterGFCI | MasterEPSEnable, MasterEPSEnable, []string{"GFCI"}},
		{"clear iso", MasterISO, 0, []string{"ISO"}},
		{"clear several", MasterNeutralDetect | MasterDCI | MasterISO, 0, []string{"NeutralDetect", "ISO", "DCI"}},
		{"protection bit already clear", 0, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClearedProtectionBits(tt.old, tt.new)
			if len(got) != len(tt.want) {
				t.Fatalf("ClearedProtectionBits(%#04x, %#04x) = %v, want %v", tt.old, tt.new, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ClearedProtectionBits(%#04x, %#04x) = %v, want %v", tt.old, tt.new, got, tt.want)
				}
			}
		})
	}
}
