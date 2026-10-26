package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jefflaplante/lux/internal/lux"
	"github.com/spf13/cobra"
)

// --- Confirmation prompt ---

func TestConfirm(t *testing.T) {
	tests := []struct {
		name       string
		yes, json  bool
		input      string
		want       bool
		wantPrompt string // "stdout", "stderr", or "" (no prompt)
	}{
		{"--yes skips prompt", true, false, "", true, ""},
		{"--yes with --json skips prompt", true, true, "", true, ""},
		{"--json alone still prompts, declines on EOF", false, true, "", false, "stderr"},
		{"--json alone accepts explicit y", false, true, "y\n", true, "stderr"},
		{"interactive yes", false, false, "yes\n", true, "stdout"},
		{"interactive no", false, false, "n\n", false, "stdout"},
		{"interactive empty declines", false, false, "\n", false, "stdout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			c := &cobra.Command{}
			c.SetIn(strings.NewReader(tt.input))
			c.SetOut(&stdout)
			c.SetErr(&stderr)

			if got := confirm(c, tt.yes, tt.json, "Confirm? [y/N] "); got != tt.want {
				t.Errorf("confirm() = %v, want %v", got, tt.want)
			}
			prompted := map[string]bool{
				"stdout": strings.Contains(stdout.String(), "Confirm?"),
				"stderr": strings.Contains(stderr.String(), "Confirm?"),
			}
			for stream, saw := range prompted {
				if saw != (stream == tt.wantPrompt) {
					t.Errorf("prompt on %s = %v, want prompt on %q", stream, saw, tt.wantPrompt)
				}
			}
		})
	}
}

func TestJSONFlagDoesNotImplyYes(t *testing.T) {
	for _, c := range []*cobra.Command{writeCmd, setModeCmd, setScheduleCmd} {
		f := c.Flags().Lookup("json")
		if f == nil {
			t.Fatalf("%s has no --json flag", c.Name())
		}
		if strings.Contains(f.Usage, "implies --yes") {
			t.Errorf("%s --json usage still claims it implies --yes: %q", c.Name(), f.Usage)
		}
	}
}

// --- write target validation (before any connection is made) ---

func TestRunWriteRejectsBeforeConnecting(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{"comm address", []string{"h15", "2"}, "not writable"},
		{"charge voltage", []string{"h99", "560"}, "not writable"},
		{"float voltage", []string{"h144", "540"}, "not writable"},
		{"soc out of range", []string{"h67", "150"}, "out of range"},
		{"bad schedule time", []string{"h68", "24"}, "invalid schedule time"},
		{"master flags via raw write", []string{"h21", "0"}, "set-mode"},
		{"grid protection", []string{"h30", "100"}, "protected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runWrite(writeCmd, tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("runWrite(%v) err = %v, want containing %q", tt.args, err, tt.wantMsg)
			}
		})
	}
}

// --- set-mode protection gate ---

func TestCheckProtectionChange(t *testing.T) {
	tests := []struct {
		name        string
		current     uint16
		newVal      uint16
		allow       bool
		wantErr     bool
		wantWarning bool
	}{
		{"ac-charge off is not gated", lux.MasterACChargeEnable, 0, false, false, false},
		{"enabling gfci is not gated", 0, lux.MasterGFCI, false, false, false},
		{"anti-island off blocked", lux.MasterAntiIsland, 0, false, true, false},
		{"gfci off blocked", lux.MasterGFCI | lux.MasterEPSEnable, lux.MasterEPSEnable, false, true, false},
		{"iso off blocked", lux.MasterISO, 0, false, true, false},
		{"gfci off allowed with flag warns", lux.MasterGFCI, 0, true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warning, err := checkProtectionChange(tt.current, tt.newVal, tt.allow)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "--allow-protection-disable") {
				t.Errorf("err = %v, want it to name --allow-protection-disable", err)
			}
			if (warning != "") != tt.wantWarning {
				t.Errorf("warning = %q, wantWarning %v", warning, tt.wantWarning)
			}
		})
	}
}

func TestSetModeHasProtectionFlag(t *testing.T) {
	f := setModeCmd.Flags().Lookup("allow-protection-disable")
	if f == nil {
		t.Fatal("set-mode has no --allow-protection-disable flag")
	}
	if f.DefValue != "false" {
		t.Errorf("--allow-protection-disable default = %s, want false", f.DefValue)
	}
}

// --- set-schedule apply + verify ---

type fakeWriter struct {
	failAt  int // 0-based index of the write that fails; -1 = never
	written []uint16
}

func (f *fakeWriter) WriteSingle(reg, val uint16) error {
	if len(f.written) == f.failAt {
		return errors.New("dongle hung up")
	}
	f.written = append(f.written, reg)
	return nil
}

func TestApplyWrites(t *testing.T) {
	writes := []regWrite{
		{reg: 68, val: encodeTime(22, 0), desc: "start"},
		{reg: 69, val: encodeTime(6, 0), desc: "end"},
		{reg: 67, val: 80, desc: "soc"},
	}
	tests := []struct {
		name        string
		failAt      int
		wantApplied int
		wantErr     bool
	}{
		{"all succeed", -1, 3, false},
		{"first fails, nothing applied", 0, 0, true},
		{"second fails, half-applied window", 1, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &fakeWriter{failAt: tt.failAt}
			applied, err := applyWrites(w, writes)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if applied != tt.wantApplied || len(w.written) != tt.wantApplied {
				t.Errorf("applied = %d (writer saw %d), want %d", applied, len(w.written), tt.wantApplied)
			}
			if tt.wantApplied > 0 && err != nil && !strings.Contains(err.Error(), "PARTIALLY APPLIED") {
				t.Errorf("partial failure err = %v, want it flagged PARTIALLY APPLIED", err)
			}
		})
	}
}

func TestVerifyWrites(t *testing.T) {
	writes := []regWrite{
		{reg: 68, val: encodeTime(22, 0)},
		{reg: 69, val: encodeTime(6, 0)},
	}
	tests := []struct {
		name    string
		got     map[uint16]uint16
		wantErr string
	}{
		{"all match", map[uint16]uint16{68: encodeTime(22, 0), 69: encodeTime(6, 0)}, ""},
		{"nothing read back", map[uint16]uint16{}, "not read back"},
		{"nil readback", nil, "not read back"},
		{"one missing", map[uint16]uint16{68: encodeTime(22, 0)}, "h69 not read back"},
		{"mismatch", map[uint16]uint16{68: encodeTime(22, 0), 69: 0}, "h69 expected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyWrites(writes, tt.got)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
			if !errors.Is(err, errVerifyFailed) {
				t.Errorf("err = %v, want errors.Is(errVerifyFailed)", err)
			}
		})
	}
}

func TestConfirmWriteDeclinedReturnsErrDeclined(t *testing.T) {
	cases := []struct {
		name  string
		yes   bool
		input string
	}{
		{"interactive decline", false, "n\n"},
		{"EOF under --json declines", false, ""},
		{"empty line declines", false, "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			c := &cobra.Command{}
			c.SetIn(strings.NewReader(tc.input))
			c.SetOut(&stdout)
			c.SetErr(&stderr)
			err := confirmWrite(c, tc.yes, true, "Confirm? [y/N] ")
			if !errors.Is(err, errDeclined) {
				t.Fatalf("confirmWrite() error = %v, want errDeclined", err)
			}
			if tc.yes && err == nil {
				t.Error("--yes must not decline")
			}
		})
	}
	c := &cobra.Command{}
	c.SetIn(strings.NewReader("y\n"))
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	if err := confirmWrite(c, false, false, "Confirm? [y/N] "); err != nil {
		t.Errorf("accepted confirmation returned %v, want nil", err)
	}
}
