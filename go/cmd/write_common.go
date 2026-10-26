package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jefflaplante/lux/internal/lux"
	"github.com/spf13/cobra"
)

// errVerifyFailed marks a write whose read-back did not confirm the new value.
// Commands return it (wrapped) so the process exits nonzero.
var errVerifyFailed = errors.New("read-back verification failed")

// regWrite is a single holding register write with a display description.
type regWrite struct {
	reg  uint16
	val  uint16
	desc string
}

// holdingWriter is the subset of *lux.Client used to apply writes.
type holdingWriter interface {
	WriteSingle(reg, value uint16) error
}

// confirm asks the user to approve a write and reports whether they did.
// Only --yes skips the prompt. With --json the prompt goes to stderr so stdout
// stays machine-readable; a non-interactive stdin (EOF) declines.
func confirm(cmd *cobra.Command, yes, jsonOut bool, prompt string) bool {
	if yes {
		return true
	}
	var out io.Writer = cmd.OutOrStdout()
	if jsonOut {
		out = cmd.ErrOrStderr()
	}
	fmt.Fprint(out, prompt)
	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line != "y" && line != "yes" {
		fmt.Fprintln(out, "Cancelled.")
		return false
	}
	return true
}

// applyWrites performs writes in order and stops at the first failure. It
// returns how many writes were applied; an error after at least one applied
// write is flagged as a partial application listing what did and did not land.
func applyWrites(w holdingWriter, writes []regWrite) (int, error) {
	for i, wr := range writes {
		if err := w.WriteSingle(wr.reg, wr.val); err != nil {
			if i == 0 {
				return 0, fmt.Errorf("write h%d: %w (nothing applied)", wr.reg, err)
			}
			var applied, pending []string
			for _, a := range writes[:i] {
				applied = append(applied, fmt.Sprintf("h%d", a.reg))
			}
			for _, p := range writes[i:] {
				pending = append(pending, fmt.Sprintf("h%d", p.reg))
			}
			return i, fmt.Errorf("write h%d: %w — PARTIALLY APPLIED: wrote %s, not written %s",
				wr.reg, err, strings.Join(applied, ", "), strings.Join(pending, ", "))
		}
	}
	return len(writes), nil
}

// verifyWrites checks every write against values read back from the inverter.
// A register missing from got counts as a failure, never as a pass.
func verifyWrites(writes []regWrite, got map[uint16]uint16) error {
	var problems []string
	for _, w := range writes {
		v, ok := got[w.reg]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("h%d not read back", w.reg))
		case v != w.val:
			problems = append(problems, fmt.Sprintf("h%d expected %d, got %d", w.reg, w.val, v))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", errVerifyFailed, strings.Join(problems, "; "))
	}
	return nil
}

// checkProtectionChange gates register 21 changes that clear protection bits
// (anti-islanding, neutral detect, ISO, GFCI, DCI). Without allow it returns
// an error; with allow it returns a warning the caller must display.
func checkProtectionChange(current, newVal uint16, allow bool) (string, error) {
	cleared := lux.ClearedProtectionBits(current, newVal)
	if len(cleared) == 0 {
		return "", nil
	}
	names := strings.Join(cleared, ", ")
	if !allow {
		return "", fmt.Errorf("refusing to disable protection function(s) %s; "+
			"re-run with --allow-protection-disable if you really mean it", names)
	}
	return fmt.Sprintf("!!! WARNING: DISABLING INVERTER PROTECTION: %s !!!\n"+
		"!!! This can violate grid interconnection requirements and create a safety hazard. !!!", names), nil
}
