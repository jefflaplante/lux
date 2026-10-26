package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jefflaplante/lux/internal/lux"
	"github.com/spf13/cobra"
)

var writeCmd = &cobra.Command{
	Use:   "write <register> <value>",
	Short: "Write a holding register value",
	Long: `Write a value to a single holding register on the inverter.

Includes safety checks (protected register rejection, rate limiting),
confirmation prompt, and read-back verification.

Examples:
  lux write h67 80                   set AC Charge SOC Limit to 80
  lux write 67 80                    same (bare number = holding)
  lux write "AC Charge SOC Limit" 80 by name
  lux write h67 80 --yes             skip confirmation`,
	Args: cobra.ExactArgs(2),
	RunE: runWrite,
}

func init() {
	writeCmd.Flags().BoolP("yes", "y", false, "skip confirmation prompt")
	writeCmd.Flags().Int("wait", 5, "max seconds to wait for read/verify")
	writeCmd.Flags().Bool("json", false, "output as JSON (prompt goes to stderr; use --yes to skip it)")
	rootCmd.AddCommand(writeCmd)
}

// validateWriteTarget rejects writes that must not go through "lux write":
// anything off the allowlist or out of range, and raw writes to register 21,
// which must use set-mode so protection bits are gated.
func validateWriteTarget(reg, value uint16) error {
	if reg == 21 {
		return fmt.Errorf("register 21 (master function flags) cannot be written raw; use \"lux set-mode\"")
	}
	return lux.ValidateWrite(reg, value)
}

func runWrite(cmd *cobra.Command, args []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	wait, _ := cmd.Flags().GetInt("wait")
	jsonFlag, _ := cmd.Flags().GetBool("json")
	waitDur := time.Duration(wait) * time.Second

	// Parse register
	targets, err := parseRegArg(args[0])
	if err != nil {
		return err
	}

	// Must resolve to exactly one holding register
	var holdingTargets []regTarget
	for _, t := range targets {
		if t.regType == "holding" {
			holdingTargets = append(holdingTargets, t)
		}
	}
	if len(holdingTargets) == 0 {
		return fmt.Errorf("only holding registers can be written (input registers are read-only)")
	}
	if len(holdingTargets) > 1 {
		names := make([]string, len(holdingTargets))
		for i, t := range holdingTargets {
			name := lux.RegisterName("holding", t.regNum)
			if name == "" {
				name = fmt.Sprintf("h%d", t.regNum)
			}
			names[i] = fmt.Sprintf("h%d (%s)", t.regNum, name)
		}
		return fmt.Errorf("ambiguous register match, be more specific: %s", strings.Join(names, ", "))
	}
	target := holdingTargets[0]

	// Parse and validate value before touching the network
	val, err := strconv.ParseUint(args[1], 10, 16)
	if err != nil {
		return fmt.Errorf("invalid value %q: must be 0-65535", args[1])
	}
	newVal := uint16(val)
	if err := validateWriteTarget(target.regNum, newVal); err != nil {
		return err
	}

	// Read current value; the connection is closed before prompting
	client, err := newClient(cmd)
	if err != nil {
		return err
	}
	cur, readErr := client.ReadHoldingRange(target.regNum, 1, waitDur)
	client.Close()

	// Show current and proposed values
	name := lux.RegisterName("holding", target.regNum)
	if name == "" {
		name = fmt.Sprintf("r%d", target.regNum)
	}

	oldRaw, hasOld := cur[target.regNum]
	hasOld = hasOld && readErr == nil

	if hasOld && oldRaw == newVal {
		if jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{
				"register": target.regNum, "name": name,
				"old_value": formatRegValue("holding", target.regNum, oldRaw), "old_raw": oldRaw,
				"new_value": formatRegValue("holding", target.regNum, newVal), "new_raw": newVal,
				"written": false, "verified": true, "verify_raw": oldRaw,
			})
		}
		fmt.Printf("Register: h%d %s\n", target.regNum, name)
		fmt.Printf("Current:  %s (raw: %d)\n", formatRegValue("holding", target.regNum, oldRaw), oldRaw)
		fmt.Println("Value unchanged, skipping write.")
		return nil
	}

	if !jsonFlag {
		fmt.Printf("Register: h%d %s\n", target.regNum, name)
		if hasOld {
			fmt.Printf("Current:  %s (raw: %d)\n", formatRegValue("holding", target.regNum, oldRaw), oldRaw)
		} else {
			fmt.Printf("Current:  (could not read)\n")
		}
		fmt.Printf("New:      %s (raw: %d)\n", formatRegValue("holding", target.regNum, newVal), newVal)
	}

	if !confirm(cmd, yes, jsonFlag, "Confirm write? [y/N] ") {
		return nil
	}

	// Reconnect; write and verify on the same connection
	client2, err := newClient(cmd)
	if err != nil {
		return fmt.Errorf("reconnect for write: %w", err)
	}
	defer client2.Close()

	if err := client2.WriteSingle(target.regNum, newVal); err != nil {
		return fmt.Errorf("write failed: %w", err)
	}
	if !jsonFlag {
		fmt.Print("Written. ")
	}

	// Wait for the dongle to process the write before reading back
	time.Sleep(500 * time.Millisecond)

	got, _ := client2.ReadHoldingRange(target.regNum, 1, waitDur)
	verifyRaw, ok := got[target.regNum]
	verifyErr := verifyWrites([]regWrite{{reg: target.regNum, val: newVal}}, got)

	if jsonFlag {
		result := map[string]any{
			"register":  target.regNum,
			"name":      name,
			"old_value": formatRegValue("holding", target.regNum, oldRaw),
			"old_raw":   oldRaw,
			"new_value": formatRegValue("holding", target.regNum, newVal),
			"new_raw":   newVal,
			"written":   true,
			"verified":  verifyErr == nil,
		}
		if ok {
			result["verify_raw"] = verifyRaw
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return err
		}
		return verifyErr
	}

	if !ok {
		fmt.Println("Verify: could not read back register")
	} else if verifyRaw == newVal {
		fmt.Printf("Verified: %s (raw: %d)\n", formatRegValue("holding", target.regNum, verifyRaw), verifyRaw)
	} else {
		fmt.Printf("WARNING: read-back mismatch! Expected %d, got %d\n", newVal, verifyRaw)
	}

	return verifyErr
}
