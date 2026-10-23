package main

// AG patch: Claude quota hold CLI. Upstream files carry one-line hooks into this file.

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

var daemonQuotaCmd = &cobra.Command{
	Use:   "quota",
	Short: "Inspect and override the Claude quota hold",
}

var daemonQuotaClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "End the Claude quota hold and claim Claude tasks again",
	Long: "The daemon stops claiming for Claude runtimes after Claude Code reports a rejected rate limit,\n" +
		"until the window resets. This overrides that hold. Running tasks are not affected.",
	Args: cobra.NoArgs,
	RunE: runDaemonQuotaClear,
}

func init() {
	daemonQuotaCmd.AddCommand(daemonQuotaClearCmd)
	daemonCmd.AddCommand(daemonQuotaCmd)
}

func runDaemonQuotaClear(cmd *cobra.Command, _ []string) error {
	port, err := agDrainTarget(cmd, "daemon quota clear")
	if err != nil {
		return err
	}
	resp, err := agPostDrainEndpoint(port, "/quota/clear")
	if err != nil {
		return err
	}
	if cleared, _ := agIntField(resp, "cleared"); cleared > 0 {
		fmt.Fprintln(os.Stdout, "Quota hold cleared: claiming Claude tasks again.")
	} else {
		fmt.Fprintln(os.Stdout, "No quota hold was active.")
	}
	return nil
}

// agQuotaStatusRows returns one `daemon status` row per Claude account on
// hold; a daemon that reports no quota state gets none.
func agQuotaStatusRows(health map[string]any) [][2]string {
	quota, _ := health["claude_quota"].(map[string]any)
	accounts, _ := quota["accounts"].([]any)
	var rows [][2]string
	for _, raw := range accounts {
		acct, _ := raw.(map[string]any)
		hold, _ := acct["hold"].(map[string]any)
		if hold == nil {
			continue
		}
		until, err := time.Parse(time.RFC3339, fmt.Sprint(hold["until"]))
		if err != nil {
			continue
		}
		label := "Claude"
		if isDefault, _ := acct["default"].(bool); !isDefault {
			label = fmt.Sprintf("Claude (%v)", acct["account"])
		}
		value := "quota hold"
		if window, _ := hold["window"].(string); window != "" {
			value += " (" + window + ")"
		}
		value += " until " + until.Local().Format("15:04")
		if reason, _ := hold["reason"].(string); reason == "reset_unknown" {
			value += ", reset time unknown"
		}
		if gating, _ := hold["gating"].(bool); !gating {
			value += ", not gating"
		}
		rows = append(rows, [2]string{label, value})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	return rows
}
