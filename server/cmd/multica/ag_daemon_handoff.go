package main

// AG patch: zero-downtime daemon handoff CLI. Upstream files carry one-line
// hooks into this file. The daemon side is internal/daemon/ag_handoff.go.
//
//	daemon drain --handoff     old daemon: drain, hand over liveness, exit when idle
//	daemon stop --handoff      the same, then wait for it to exit
//	daemon restart --handoff   hand off, then start a successor with --overlap
//	daemon start --overlap     new daemon: start beside a handing-off predecessor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/daemon"
)

func init() {
	for _, c := range []*cobra.Command{daemonDrainCmd, daemonStopCmd, daemonRestartCmd} {
		c.Flags().Bool("handoff", false, "Hand the runtimes to a successor: implies drain, releases the profile lock and exits once idle; the successor is started with 'daemon start --overlap'")
	}
	daemonStartCmd.Flags().Bool("overlap", false, "Start beside a daemon that is handing off ('daemon drain --handoff'): do not recover its running tasks, and share the task cap with it")
	// restart --handoff starts its successor in overlap mode itself.
	daemonRestartCmd.Flags().Bool("overlap", false, "")
	_ = daemonRestartCmd.Flags().MarkHidden("overlap")
}

func agHandoffRequested(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("handoff")
	return v
}

func agOverlapRequested(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("overlap")
	return v
}

// agOverlapArgs forwards --overlap to the background child.
func agOverlapArgs(cmd *cobra.Command, args []string) []string {
	if agOverlapRequested(cmd) {
		return append(args, "--overlap")
	}
	return args
}

// agWriteLauncherPID writes the background child's PID file, except in overlap
// mode: the child reads the predecessor's PID from that file and writes its own
// once it has done so.
func agWriteLauncherPID(cmd *cobra.Command, path string, pid int) error {
	if agOverlapRequested(cmd) {
		return nil
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644)
}

// agRequireRunningForHandoff makes --handoff on a daemon that isn't running an
// error, as drain does.
func agRequireRunningForHandoff(cmd *cobra.Command, profile string, health map[string]any) error {
	if agHandoffRequested(cmd) && !daemonAlive(health) {
		return fmt.Errorf("%s is not running", agDaemonLabel(profile))
	}
	return nil
}

// agHandoffBegin puts the daemon on port into handoff mode.
func agHandoffBegin(port int, out io.Writer) error {
	resp, err := agPostDrainEndpoint(port, "/handoff")
	if err != nil {
		return fmt.Errorf("%w\n(a daemon from before the handoff patch has no /handoff; restart it once from the patched build)", err)
	}
	fmt.Fprintf(out, "Handoff started: no new tasks will be claimed; the daemon exits once its %s active task(s) finish.\n", agActiveTasksSummary(resp))
	return nil
}

// agHandoffStop is `daemon stop --handoff`: hand off, then wait for the daemon
// to exit on its own. handled is true when the stop is complete.
func agHandoffStop(cmd *cobra.Command, port int) (handled bool, err error) {
	if !agHandoffRequested(cmd) {
		return false, nil
	}
	if err := agHandoffBegin(port, os.Stderr); err != nil {
		return true, err
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")
	if err := agWaitDaemonExit(port, timeout, os.Stderr); err != nil {
		return true, err
	}
	fmt.Fprintln(os.Stderr, "Daemon exited.")
	return true, nil
}

// agHandoffRestart is `daemon restart --handoff`: hand off, then start the
// successor in overlap mode without waiting for the old daemon.
func agHandoffRestart(cmd *cobra.Command, port int, args []string) (handled bool, err error) {
	if !agHandoffRequested(cmd) {
		return false, nil
	}
	if err := agHandoffBegin(port, os.Stderr); err != nil {
		return true, err
	}
	if err := cmd.Flags().Set("overlap", "true"); err != nil {
		return true, err
	}
	return true, runDaemonStart(cmd, args)
}

// agWaitDaemonExit polls the daemon on port until it stops answering. A daemon
// that exits between two polls is the expected outcome, so unlike agWaitIdle a
// lost contact is success.
func agWaitDaemonExit(port int, timeout time.Duration, progress io.Writer) error {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	lastActive := int64(-1)
	failures := 0
	for {
		health := agHealth(port)
		if daemonAlive(health) {
			failures = 0
			if active, ok := agIntField(health, "active_task_count"); ok && active != lastActive {
				lastActive = active
				fmt.Fprintf(progress, "Waiting for active tasks: %s\n", agActiveTasksSummary(health))
			}
		} else if failures++; failures >= agDrainMaxPollFailures {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("timed out waiting for the daemon to exit (active tasks: %s); it is still in handoff mode and exits once idle",
				agActiveTasksSummary(health))
		}
		select {
		case <-ctx.Done():
		case <-time.After(agDrainPollInterval):
		}
	}
}

// agLogSwitch lets a running daemon change the file its logger writes to. The
// handoff daemon moves aside so the successor's rotating writer is the only one
// on daemon.log.
type agLogSwitch struct {
	mu sync.Mutex
	w  io.WriteCloser
}

func (s *agLogSwitch) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// SwitchTo closes the current writer and sends later output to next.
func (s *agLogSwitch) SwitchTo(next io.WriteCloser) {
	s.mu.Lock()
	old := s.w
	s.w = next
	s.mu.Unlock()
	_ = old.Close()
}

// agHandoffLogPath is where a handoff daemon logs from then on, one file per
// process so two draining generations never share a rotator.
func agHandoffLogPath(profile string, pid int) string {
	return filepath.Join(daemonDirForProfile(profile), fmt.Sprintf("daemon.handoff.%d.log", pid))
}

// agHandoffRecord names a daemon that has handed off and is still draining.
type agHandoffRecord struct {
	PID  int `json:"pid"`
	Port int `json:"port"`
}

// agHandoffRecordPath is read by every start, independent of daemon.port, which
// the successor overwrites with its own port.
func agHandoffRecordPath(profile string) string {
	return filepath.Join(daemonDirForProfile(profile), "daemon.handoff.json")
}

func agWriteHandoffRecord(profile string, rec agHandoffRecord) {
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = os.WriteFile(agHandoffRecordPath(profile), data, 0o644)
}

// agRemoveHandoffRecordIfOwned removes the record only while it names pid.
func agRemoveHandoffRecordIfOwned(profile string, pid int) {
	if rec, ok := agReadHandoffRecord(profile); ok && rec.PID == pid {
		_ = os.Remove(agHandoffRecordPath(profile))
	}
}

func agReadHandoffRecord(profile string) (agHandoffRecord, bool) {
	var rec agHandoffRecord
	data, err := os.ReadFile(agHandoffRecordPath(profile))
	if err != nil || json.Unmarshal(data, &rec) != nil || rec.PID < 1 {
		return rec, false
	}
	return rec, true
}

// agRefuseWhileHandoffDraining stops a normal start from running the startup
// RecoverOrphans, which would fail the tasks of a handoff daemon that is still
// draining. Only an overlap start may run beside it. A record whose process is
// gone, or whose port is answered by some other process, is stale and removed.
func agRefuseWhileHandoffDraining(profile string) error {
	rec, ok := agReadHandoffRecord(profile)
	if !ok || rec.PID == os.Getpid() {
		return nil
	}
	if alive, known := daemon.AGPIDAlive(rec.PID); known && !alive {
		_ = os.Remove(agHandoffRecordPath(profile))
		return nil
	}
	if health := agHealth(rec.Port); daemonAlive(health) {
		if got, _ := agIntField(health, "pid"); got != int64(rec.PID) {
			_ = os.Remove(agHandoffRecordPath(profile))
			return nil
		}
	}
	return fmt.Errorf("%s (pid %d) has handed off and is still draining its tasks; a normal start would fail them. "+
		"Wait for it to exit, or start beside it with 'daemon start --overlap'. "+
		"If pid %d is not a daemon, delete %s",
		agDaemonLabel(profile), rec.PID, rec.PID, agHandoffRecordPath(profile))
}

// agInstallHandoffHook tells the daemon what handoff means for this process:
// leave a record of it for later starts, move logging off daemon.log (when it
// logs there), then release the profile lock so the successor can take it. sw
// and lock may be nil.
func agInstallHandoffHook(d *daemon.Daemon, profile string, port int, sw *agLogSwitch, lock *os.File) {
	d.AGSetHandoffHook(func() {
		agWriteHandoffRecord(profile, agHandoffRecord{PID: os.Getpid(), Port: port})
		if sw != nil {
			sw.SwitchTo(newDaemonLogRotator(agHandoffLogPath(profile, os.Getpid())))
		}
		if lock != nil {
			_ = lock.Close()
		}
	})
}

// agDiscoverPredecessor is the overlap start's first act, before it publishes
// its own port or PID file or registers anything. It fails closed: every
// problem reading the predecessor is an error, never a fallback to a normal
// start, which would fail the predecessor's running tasks.
func agDiscoverPredecessor(profile string, myCap int) (daemon.AGPredecessor, error) {
	var zero daemon.AGPredecessor
	label := agDaemonLabel(profile)

	data, err := os.ReadFile(daemonPIDPathForProfile(profile))
	if err != nil {
		return zero, fmt.Errorf("--overlap: no %s is running (no PID file); use a normal 'daemon start'", label)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid < 1 {
		return zero, fmt.Errorf("--overlap: the PID file of %s is unreadable; use a normal 'daemon start'", label)
	}
	if alive, known := daemon.AGPIDAlive(pid); known && !alive {
		return zero, fmt.Errorf("--overlap: no %s is running (pid %d is gone); use a normal 'daemon start'", label, pid)
	}

	port := resolveHealthPort(profile)
	health := agHealth(port)
	if !daemonAlive(health) {
		return zero, fmt.Errorf("--overlap: cannot read the predecessor's /health on port %d (pid %d); refusing to start, which could fail its running tasks", port, pid)
	}
	if err := daemonIdentityMismatch(health, profile, port); err != nil {
		return zero, fmt.Errorf("--overlap: %w", err)
	}
	if got, _ := agIntField(health, "pid"); got != int64(pid) {
		return zero, fmt.Errorf("--overlap: the daemon answering on port %d is pid %d, not the PID file's pid %d; refusing to start", port, got, pid)
	}
	if handoff, _ := health["handoff"].(bool); !handoff {
		return zero, fmt.Errorf("--overlap: the daemon (pid %d) is not in handoff mode; run 'multica daemon drain --handoff' first, or use 'daemon restart --handoff'", pid)
	}
	theirCap, ok := agIntField(health, "max_concurrent_tasks")
	if !ok {
		return zero, fmt.Errorf("--overlap: the predecessor (pid %d) does not report its task cap; refusing to start with an unknown cap", pid)
	}
	if theirCap != int64(myCap) {
		return zero, fmt.Errorf("--overlap: task cap mismatch: this daemon has MULTICA_DAEMON_MAX_CONCURRENT_TASKS=%d, the predecessor has %d; start with the same cap", myCap, theirCap)
	}
	rawIDs, ok := health["active_task_ids"].([]any)
	if !ok {
		return zero, fmt.Errorf("--overlap: the predecessor (pid %d) does not report its active task IDs; refusing to start", pid)
	}
	ids := make([]string, 0, len(rawIDs))
	for _, v := range rawIDs {
		if id, ok := v.(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return daemon.AGPredecessor{PID: pid, Port: port, TaskIDs: ids, MaxConcurrentTasks: int(theirCap)}, nil
}
