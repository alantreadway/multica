package main

// AG patch: daemon drain CLI. Upstream files carry one-line hooks into this file.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// agDrainPollInterval is how often --wait re-reads /health. Tests shorten it.
var agDrainPollInterval = time.Second

// agDrainMaxPollFailures is how many consecutive failed /health polls --wait
// tolerates before it gives up.
const agDrainMaxPollFailures = 3

var daemonDrainCmd = &cobra.Command{
	Use:   "drain",
	Short: "Stop claiming new tasks and let running tasks finish",
	Long: "Put the running daemon into drain mode: it stops claiming new tasks, running tasks finish, and it\n" +
		"reports idle once nothing is active, which is when a restart or upgrade is safe.\n" +
		"Drain is held in memory only, so a daemon that restarts comes back claiming.\n\n" +
		"--wait blocks until the daemon is idle and exits 0. If --timeout elapses first it exits 1 and leaves\n" +
		"drain on; run 'multica daemon resume' to claim again.",
	Args:    cobra.NoArgs,
	PreRunE: agValidateDrainFlags,
	RunE:    runDaemonDrain,
}

var daemonResumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume claiming new tasks after a drain",
	Args:  cobra.NoArgs,
	RunE:  runDaemonResume,
}

func init() {
	df := daemonDrainCmd.Flags()
	df.Bool("wait", false, "Block until the daemon is idle")
	df.Duration("timeout", 0, "With --wait, give up after this long and exit 1, leaving drain on (0 = wait indefinitely)")

	for c, verb := range map[*cobra.Command]string{daemonStopCmd: "stopping", daemonRestartCmd: "restarting"} {
		f := c.Flags()
		f.Bool("drain", false, "Drain first: stop claiming, wait for running tasks to finish, then "+c.Name())
		f.Duration("timeout", 0, "With --drain, give up after this long without "+verb+", leaving drain on (0 = wait indefinitely)")
		c.PreRunE = agChainPreRunE(c.PreRunE, agValidateDrainFlags)
	}

	daemonCmd.AddCommand(daemonDrainCmd)
	daemonCmd.AddCommand(daemonResumeCmd)
}

// agValidateDrainFlags rejects flag combinations that would otherwise be
// silently ignored.
func agValidateDrainFlags(cmd *cobra.Command, _ []string) error {
	timeout, _ := cmd.Flags().GetDuration("timeout")
	if timeout < 0 {
		return fmt.Errorf("--timeout must not be negative")
	}
	if !cmd.Flags().Changed("timeout") {
		return nil
	}
	if cmd.Name() == "drain" {
		if wait, _ := cmd.Flags().GetBool("wait"); !wait {
			return fmt.Errorf("--timeout requires --wait")
		}
		return nil
	}
	if drain, _ := cmd.Flags().GetBool("drain"); !drain && !agHandoffRequested(cmd) {
		return fmt.Errorf("--timeout requires --drain or --handoff")
	}
	return nil
}

// agChainPreRunE runs prev, if any, before next, so a PreRunE set in the
// command literal is kept. A later init() that assigns PreRunE would replace it.
func agChainPreRunE(prev, next func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	if prev == nil {
		return next
	}
	return func(cmd *cobra.Command, args []string) error {
		if err := prev(cmd, args); err != nil {
			return err
		}
		return next(cmd, args)
	}
}

// agDrainTarget runs the guards every drain command shares and returns the
// health port of the daemon the command should act on.
func agDrainTarget(cmd *cobra.Command, command string) (int, error) {
	if err := requireHumanLocalCommand(command); err != nil {
		return 0, err
	}
	profile := resolveProfile(cmd)
	if err := requireKnownProfile(profile); err != nil {
		return 0, err
	}
	port := resolveHealthPort(profile)

	health := agHealth(port)
	if !daemonAlive(health) {
		return 0, fmt.Errorf("%s is not running", agDaemonLabel(profile))
	}
	if err := daemonIdentityMismatch(health, profile, port); err != nil {
		return 0, err
	}
	return port, nil
}

func agDaemonLabel(profile string) string {
	if profile != "" {
		return fmt.Sprintf("Daemon [%s]", profile)
	}
	return "Daemon"
}

func runDaemonDrain(cmd *cobra.Command, _ []string) error {
	port, err := agDrainTarget(cmd, "daemon drain")
	if err != nil {
		return err
	}
	if agHandoffRequested(cmd) {
		if err := agHandoffBegin(port, os.Stdout); err != nil {
			return err
		}
	}
	wait, _ := cmd.Flags().GetBool("wait")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	if agHandoffRequested(cmd) && wait {
		// A handoff daemon exits once idle, so waiting for it means waiting for that.
		return agWaitDaemonExit(port, timeout, os.Stderr)
	}
	return agDrainAndReport(port, wait, timeout, os.Stdout, os.Stderr)
}

func runDaemonResume(cmd *cobra.Command, _ []string) error {
	port, err := agDrainTarget(cmd, "daemon resume")
	if err != nil {
		return err
	}
	resp, err := agPostDrainEndpoint(port, "/resume")
	if err != nil {
		return err
	}
	if draining, _ := resp["draining"].(bool); draining {
		return fmt.Errorf("daemon is still draining after resume")
	}
	fmt.Fprintln(os.Stdout, "Claiming resumed.")
	return nil
}

// agDrainBeforeStop drains and waits for idle when --drain is set. An error,
// including the timeout, means the caller must not stop the daemon; drain stays on.
func agDrainBeforeStop(cmd *cobra.Command, port int) error {
	drain, _ := cmd.Flags().GetBool("drain")
	if !drain {
		return nil
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")
	if err := agDrainAndReport(port, true, timeout, os.Stderr, os.Stderr); err != nil {
		return fmt.Errorf("daemon %s aborted: %w", cmd.Name(), err)
	}
	return nil
}

// agDrainBeforeRestart is agDrainBeforeStop for restart, plus a second
// preflight: the one before the drain can be hours old by the time it ends.
func agDrainBeforeRestart(cmd *cobra.Command, port int, profile string) error {
	if err := agDrainBeforeStop(cmd, port); err != nil {
		return err
	}
	if drain, _ := cmd.Flags().GetBool("drain"); !drain {
		return nil
	}
	if err := requireDaemonRestartPreflight(cmd, profile); err != nil {
		return fmt.Errorf("%w\nthe daemon is idle but still draining; run 'multica daemon resume' to claim again", err)
	}
	return nil
}

// agDrainAndReport POSTs /drain, prints the state to out and, with wait,
// blocks until idle (progress goes to progress).
func agDrainAndReport(port int, wait bool, timeout time.Duration, out, progress io.Writer) error {
	resp, err := agPostDrainEndpoint(port, "/drain")
	if err != nil {
		return err
	}
	// /drain returns only the total; /health carries the running/waiting split,
	// so report one /health snapshot when it answers.
	if health := agHealth(port); daemonAlive(health) {
		resp = health
	}
	idle, _ := resp["idle"].(bool)
	fmt.Fprintf(out, "Draining%s: no new tasks will be claimed.\n", agDrainSinceSuffix(resp))
	fmt.Fprintf(out, "Active tasks: %s\n", agActiveTasksSummary(resp))
	if idle {
		fmt.Fprintln(out, "Idle: yes (safe to restart or upgrade)")
		return nil
	}
	fmt.Fprintln(out, "Idle: no")
	if !wait {
		return nil
	}

	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := agWaitIdle(ctx, port, progress); err != nil {
		return err
	}
	fmt.Fprintln(out, "Idle: yes (safe to restart or upgrade)")
	return nil
}

func agWaitIdle(ctx context.Context, port int, progress io.Writer) error {
	lastActive := int64(-1)
	failures := 0
	for {
		health := agHealth(port)
		if daemonAlive(health) {
			failures = 0
			if draining, _ := health["draining"].(bool); !draining {
				return fmt.Errorf("daemon is no longer draining (resumed?); not waiting")
			}
			if idle, _ := health["idle"].(bool); idle {
				return nil
			}
			if active, ok := agIntField(health, "active_task_count"); ok && active != lastActive {
				lastActive = active
				fmt.Fprintf(progress, "Waiting for active tasks: %s\n", agActiveTasksSummary(health))
			}
		} else {
			failures++
			if failures >= agDrainMaxPollFailures {
				return fmt.Errorf("lost contact with the daemon while waiting for it to go idle; if it is still running, drain is still on: run 'multica daemon resume' to claim again")
			}
		}
		if ctx.Err() != nil {
			return fmt.Errorf("timed out waiting for the daemon to go idle (active tasks: %s); the daemon is still draining, run 'multica daemon resume' to claim again",
				agActiveTasksSummary(health))
		}
		select {
		case <-ctx.Done():
		case <-time.After(agDrainPollInterval):
		}
	}
}

func agHealth(port int) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return checkDaemonHealthOnPort(ctx, port)
}

func agPostDrainEndpoint(port int, path string) (map[string]any, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, fmt.Errorf("the running daemon does not support %s: it was started from a multica binary without the AG drain patch; restart it from the patched build (see ag-patches/README.md)", path)
	}
	if resp.StatusCode == http.StatusConflict { // AG patch: handoff refuses /resume
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(msg)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("daemon %s returned status %d", path, resp.StatusCode)
	}
	var state map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", path, err)
	}
	return state, nil
}

func agIntField(m map[string]any, key string) (int64, bool) {
	v, ok := m[key].(float64)
	return int64(v), ok
}

// agActiveTasksSummary renders "N (R running, W waiting for resources)". The
// split is left out when the daemon does not report it.
func agActiveTasksSummary(m map[string]any) string {
	active, ok := agIntField(m, "active_task_count")
	if !ok {
		return "unknown"
	}
	running, hasRunning := agIntField(m, "running_task_count")
	waiting, hasWaiting := agIntField(m, "resource_wait_task_count")
	if !hasRunning || !hasWaiting {
		return fmt.Sprintf("%d", active)
	}
	return fmt.Sprintf("%d (%d running, %d waiting for resources)", active, running, waiting)
}

// agDrainSinceSuffix renders " since HH:MM" in local time, or "" when the
// daemon gave no usable start time.
func agDrainSinceSuffix(m map[string]any) string {
	raw, _ := m["drain_started_at"].(string)
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return ""
	}
	return " since " + t.Local().Format("15:04")
}

// agDaemonStatusRows returns the extra `daemon status` rows; a daemon that
// reports no drain state gets none.
func agDaemonStatusRows(health map[string]any) [][2]string {
	draining, ok := health["draining"].(bool)
	if !ok {
		return nil
	}
	var rows [][2]string
	claiming := "yes"
	if draining {
		claiming = "no (draining" + agDrainSinceSuffix(health) + ")"
	}
	rows = append(rows, [2]string{"Claiming", claiming})
	if _, ok := agIntField(health, "active_task_count"); ok {
		rows = append(rows, [2]string{"Active tasks", agActiveTasksSummary(health)})
	}
	if draining {
		idle := "no"
		if v, _ := health["idle"].(bool); v {
			idle = "yes"
		}
		rows = append(rows, [2]string{"Idle", idle})
	}
	if handoff, _ := health["handoff"].(bool); handoff {
		rows = append(rows, [2]string{"Handoff", "yes (exits once idle)"})
	}
	if pred, ok := agIntField(health, "predecessor_pid"); ok && pred > 0 {
		rows = append(rows, [2]string{"Predecessor", fmt.Sprintf("pid %d still running (overlap)", pred)})
	}
	return rows
}
