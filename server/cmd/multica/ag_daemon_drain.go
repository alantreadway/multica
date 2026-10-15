package main

// AG patch: daemon drain CLI.
//
// `daemon drain` and `daemon resume` drive the daemon's POST /drain and
// POST /resume (server/internal/daemon/ag_drain.go); `daemon stop --drain` and
// `daemon restart --drain` drain, wait for idle, then stop. Everything here is
// human-only like `daemon stop`, so an agent run cannot drain its own daemon.
// The upstream files carry one-line hooks into this file.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// agDrainPollInterval is how often --wait re-reads /health. Tests shorten it.
var agDrainPollInterval = time.Second

var daemonDrainCmd = &cobra.Command{
	Use:   "drain",
	Short: "Stop claiming new tasks and let running tasks finish",
	Long: "Put the running daemon into drain mode: it stops claiming new tasks, running tasks finish, and it\n" +
		"reports idle once nothing is active, which is when a restart or upgrade is safe.\n" +
		"Drain is held in memory only, so a daemon that restarts comes back claiming.\n\n" +
		"--wait blocks until the daemon is idle and exits 0. If --timeout elapses first it exits 1 and leaves\n" +
		"drain on; run 'multica daemon resume' to claim again.",
	Args: cobra.NoArgs,
	RunE: runDaemonDrain,
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

	for _, c := range []*cobra.Command{daemonStopCmd, daemonRestartCmd} {
		f := c.Flags()
		f.Bool("drain", false, "Drain first: stop claiming, wait for running tasks to finish, then "+c.Name())
		f.Duration("timeout", 0, "With --drain, give up after this long without "+c.Name()+"ing, leaving drain on (0 = wait indefinitely)")
		c.PreRunE = agValidateDrainFlags
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
	if drain, _ := cmd.Flags().GetBool("drain"); !drain {
		return fmt.Errorf("--timeout requires --drain")
	}
	return nil
}

// agDrainTarget runs the guards every drain command shares and returns the
// health port of the daemon the command should act on.
func agDrainTarget(cmd *cobra.Command, command string) (profile string, port int, err error) {
	if err := requireHumanLocalCommand(command); err != nil {
		return "", 0, err
	}
	profile = resolveProfile(cmd)
	if err := requireKnownProfile(profile); err != nil {
		return "", 0, err
	}
	port = resolveHealthPort(profile)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	health := checkDaemonHealthOnPort(ctx, port)
	if !daemonAlive(health) {
		return "", 0, fmt.Errorf("%s is not running", agDaemonLabel(profile))
	}
	if err := daemonIdentityMismatch(health, profile, port); err != nil {
		return "", 0, err
	}
	return profile, port, nil
}

func agDaemonLabel(profile string) string {
	if profile != "" {
		return fmt.Sprintf("Daemon [%s]", profile)
	}
	return "Daemon"
}

func runDaemonDrain(cmd *cobra.Command, _ []string) error {
	_, port, err := agDrainTarget(cmd, "daemon drain")
	if err != nil {
		return err
	}
	wait, _ := cmd.Flags().GetBool("wait")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	return agDrainAndReport(port, wait, timeout, os.Stdout, os.Stderr)
}

func runDaemonResume(cmd *cobra.Command, _ []string) error {
	_, port, err := agDrainTarget(cmd, "daemon resume")
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

// agDrainBeforeStop is the hook for `daemon stop --drain` and
// `daemon restart --drain`: drain, wait for idle, and only then let the caller
// go on. Any error, including the timeout, means the caller must not stop the
// daemon; the drain is left on.
func agDrainBeforeStop(cmd *cobra.Command, port int) error {
	drain, _ := cmd.Flags().GetBool("drain")
	if !drain {
		return nil
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")
	// Progress and results go to stderr: stop and restart report on stderr too.
	if err := agDrainAndReport(port, true, timeout, os.Stderr, os.Stderr); err != nil {
		return fmt.Errorf("daemon %s aborted: %w", cmd.Name(), err)
	}
	return nil
}

// agDrainAndReport POSTs /drain, prints the resulting state to out and, with
// wait, blocks until the daemon is idle (progress goes to progress).
func agDrainAndReport(port int, wait bool, timeout time.Duration, out, progress io.Writer) error {
	resp, err := agPostDrainEndpoint(port, "/drain")
	if err != nil {
		return err
	}
	// The /drain response has only the total; /health has the running vs
	// waiting-for-resources split.
	health := agHealth(port)
	for _, k := range []string{"running_task_count", "resource_wait_task_count"} {
		if v, ok := health[k]; ok {
			resp[k] = v
		}
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

// agWaitIdle polls /health until the daemon reports idle. It returns an error
// when ctx ends first, when the daemon stops answering, or when the drain was
// cancelled underneath it (someone ran `daemon resume`).
func agWaitIdle(ctx context.Context, port int, progress io.Writer) error {
	lastActive := int64(-1)
	for {
		health := agHealth(port)
		if err := ctx.Err(); err != nil {
			return agDrainTimeoutError(health, "timed out waiting for the daemon to go idle")
		}
		if !daemonAlive(health) {
			return fmt.Errorf("daemon stopped while waiting for it to go idle")
		}
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
		select {
		case <-ctx.Done():
		case <-time.After(agDrainPollInterval):
		}
	}
}

func agDrainTimeoutError(health map[string]any, msg string) error {
	return fmt.Errorf("%s (active tasks: %s); the daemon is still draining, run 'multica daemon resume' to claim again",
		msg, agActiveTasksSummary(health))
}

func agHealth(port int) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return checkDaemonHealthOnPort(ctx, port)
}

// agPostDrainEndpoint POSTs to /drain or /resume and decodes the state the
// daemon returns.
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
	if since := agDrainSinceClock(m); since != "" {
		return " since " + since
	}
	return ""
}

func agDrainSinceClock(m map[string]any) string {
	raw, _ := m["drain_started_at"].(string)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return ""
	}
	return t.Local().Format("15:04")
}

// agDaemonStatusRows returns the extra `daemon status` rows for a /health
// response. A daemon that does not report drain state (stock upstream) gets
// none, so its output is unchanged.
func agDaemonStatusRows(health map[string]any) [][2]string {
	draining, ok := health["draining"].(bool)
	if !ok {
		return nil
	}
	var rows [][2]string
	claiming := "yes"
	if draining {
		claiming = "no (draining"
		if since := agDrainSinceClock(health); since != "" {
			claiming += " since " + since
		}
		claiming += ")"
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
	return rows
}
