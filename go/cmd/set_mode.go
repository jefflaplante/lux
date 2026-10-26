package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jefflaplante/lux/internal/lux"
	"github.com/spf13/cobra"
)

// modeAliases maps user-friendly names to bit positions in register 21.
// Lookup goes through resolveMode, which ignores case and dashes.
var modeAliases = map[string]int{
	"eps":              0,
	"overloadderate":   1,
	"drms":             2,
	"lvrt":             3,
	"antiisland":       4,
	"neutraldetect":    5,
	"gridonpowerss":    6,
	"accharge":         7,
	"ac-charge":        7,
	"seamlesseps":      8,
	"standby":          9,
	"forceddischarge":  10,
	"forced-discharge": 10,
	"chargepriority":   11,
	"charge-priority":  11,
	"iso":              12,
	"gfci":             13,
	"dci":              14,
	"feedingrid":       15,
	"feed-in-grid":     15,
}

var setModeCmd = &cobra.Command{
	Use:   "set-mode <mode> <on|off>",
	Short: "Toggle an inverter mode flag",
	Long: `Toggle a bit in the master function bitmask (register 21).

Uses read-modify-write: reads current value, flips the target bit, then
re-reads register 21 on the write connection and aborts if it changed since
you confirmed, writes, and verifies on that same connection.

Available modes:
  ac-charge, forced-discharge, charge-priority, feed-in-grid,
  eps, standby, seamless-eps, and others (see lux read h21)

Turning off a protection function (antiisland, neutraldetect, iso, gfci, dci)
is refused unless --allow-protection-disable is given.

Examples:
  lux set-mode ac-charge on
  lux set-mode forced-discharge off
  lux set-mode charge-priority on --yes`,
	Args: cobra.ExactArgs(2),
	RunE: runSetMode,
}

func init() {
	setModeCmd.Flags().BoolP("yes", "y", false, "skip confirmation prompt")
	setModeCmd.Flags().Int("wait", 5, "max seconds to wait for read/verify")
	setModeCmd.Flags().Bool("json", false, "output as JSON (prompt goes to stderr; use --yes to skip it)")
	setModeCmd.Flags().Bool("allow-protection-disable", false,
		"permit turning OFF anti-islanding, neutral detect, ISO, GFCI or DCI protection")
	rootCmd.AddCommand(setModeCmd)
}

func runSetMode(cmd *cobra.Command, args []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	wait, _ := cmd.Flags().GetInt("wait")
	jsonFlag, _ := cmd.Flags().GetBool("json")
	allowProtection, _ := cmd.Flags().GetBool("allow-protection-disable")
	waitDur := time.Duration(wait) * time.Second

	modeName := strings.ToLower(args[0])
	action := strings.ToLower(args[1])

	bit, ok := resolveMode(modeName)
	if !ok {
		var names []string
		for k := range modeAliases {
			names = append(names, k)
		}
		sort.Strings(names)
		return fmt.Errorf("unknown mode %q\nAvailable: %s", args[0], strings.Join(names, ", "))
	}

	var setOn bool
	switch action {
	case "on", "enable", "1", "true":
		setOn = true
	case "off", "disable", "0", "false":
		setOn = false
	default:
		return fmt.Errorf("invalid action %q: use on or off", args[1])
	}

	bitName := lux.MasterFunctionBits[bit]

	// Read current register 21; the connection is closed before prompting
	client, err := newClient(cmd)
	if err != nil {
		return err
	}
	vals, err := client.ReadHoldingRange(21, 1, waitDur)
	client.Close()
	if err != nil {
		return fmt.Errorf("read register 21: %w", err)
	}
	current := vals[21]

	newVal := applyModeBit(current, bit, setOn)

	def, _ := lux.GetRegisterDef("holding", 21)

	if current == newVal {
		if jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{
				"mode": modeName, "bit": bit, "action": action,
				"old_raw": current, "new_raw": newVal,
				"written": false, "verified": true, "verify_raw": current,
			})
		}
		fmt.Printf("%s is already %s.\n", bitName, action)
		return nil
	}

	warning, err := checkProtectionChange(current, newVal, allowProtection)
	if err != nil {
		return err
	}
	if warning != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), warning)
	}

	if !jsonFlag {
		fmt.Printf("Mode:    %s → %s\n", bitName, strings.ToUpper(action))
		fmt.Printf("Current: %s (raw: %d)\n", def.FormatValueOnly(current), current)
		fmt.Printf("New:     %s (raw: %d)\n", def.FormatValueOnly(newVal), newVal)
	}

	if err := confirmWrite(cmd, yes, jsonFlag, "Confirm? [y/N] "); err != nil {
		return err
	}

	// Reconnect; re-read, write and verify all on this one connection
	client2, err := newClient(cmd)
	if err != nil {
		return fmt.Errorf("reconnect: %w", err)
	}
	defer client2.Close()

	fresh, err := client2.ReadHoldingRange(21, 1, waitDur)
	if err != nil {
		return fmt.Errorf("re-read register 21 before write: %w (nothing written)", err)
	}
	if fresh[21] != current {
		return fmt.Errorf("register 21 changed since it was read (was %d, now %d); nothing written, re-run to retry",
			current, fresh[21])
	}

	if err := client2.WriteSingle(21, newVal); err != nil {
		return fmt.Errorf("write failed: %w", err)
	}
	if !jsonFlag {
		fmt.Print("Written. ")
	}

	time.Sleep(500 * time.Millisecond)

	got, _ := client2.ReadHoldingRange(21, 1, waitDur)
	verifyVal, readOk := got[21]
	verifyErr := verifyWrites([]regWrite{{reg: 21, val: newVal}}, got)

	if jsonFlag {
		result := map[string]any{
			"mode":     modeName,
			"bit":      bit,
			"action":   action,
			"old_raw":  current,
			"new_raw":  newVal,
			"written":  true,
			"verified": verifyErr == nil,
		}
		if readOk {
			result["verify_raw"] = verifyVal
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return err
		}
		return verifyErr
	}

	if !readOk {
		fmt.Println("Verify: could not read back register")
	} else if verifyVal == newVal {
		fmt.Printf("Verified: %s (raw: %d)\n", def.FormatValueOnly(verifyVal), verifyVal)
	} else {
		fmt.Printf("WARNING: read-back mismatch! Expected %d, got %d\n", newVal, verifyVal)
	}

	return verifyErr
}

// resolveMode maps a user-supplied mode name to its bit in register 21.
// Lookup ignores case and dashes, so "seamless-eps" and "SeamlessEPS" both
// resolve; names are tried against modeAliases, then MasterFunctionBits.
func resolveMode(name string) (int, bool) {
	key := strings.ReplaceAll(strings.ToLower(name), "-", "")
	if bit, ok := modeAliases[key]; ok {
		return bit, true
	}
	for b, n := range lux.MasterFunctionBits {
		if strings.EqualFold(n, key) {
			return b, true
		}
	}
	return 0, false
}

// applyModeBit returns current with bit set (on) or cleared (off).
func applyModeBit(current uint16, bit int, on bool) uint16 {
	if on {
		return current | (1 << bit)
	}
	return current &^ (1 << bit)
}
