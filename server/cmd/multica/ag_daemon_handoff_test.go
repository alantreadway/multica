package main

// AG patch: daemon handoff / overlap CLI tests (ag_daemon_handoff.go).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// agHandoffFake stands in for a daemon's health listener, with the handoff
// fields. goneAfterPolls is the number of /health reads after handoff began
// before it stops answering (-1: never).
type agHandoffFake struct {
	mu             sync.Mutex
	profile        string
	pid            int
	handoff        bool
	active         int
	maxCap         *int     // nil: the field is absent
	taskIDs        []string // nil: the field is absent
	goneAfterPolls int
	polls          int
	gone           bool
	noHandoff      bool // a daemon from before the patch: no /handoff
	calls          []string
}

func intp(v int) *int { return &v }

func (f *agHandoffFake) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *agHandoffFake) called(c string) bool { return slices.Contains(f.callLog(), c) }

func (f *agHandoffFake) record(c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *agHandoffFake) state() map[string]any {
	m := map[string]any{
		"status":             "running",
		"profile":            f.profile,
		"pid":                f.pid,
		"active_task_count":  f.active,
		"draining":           f.handoff,
		"idle":               f.handoff && f.active == 0,
		"handoff":            f.handoff,
		"running_task_count": f.active,
	}
	if f.maxCap != nil {
		m["max_concurrent_tasks"] = *f.maxCap
	}
	if f.taskIDs != nil {
		m["active_task_ids"] = f.taskIDs
	}
	return m
}

func (f *agHandoffFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/health":
		if f.gone {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "stopped"})
			return
		}
		if f.handoff && f.goneAfterPolls >= 0 {
			f.polls++
			if f.polls >= f.goneAfterPolls {
				f.gone = true
				f.active = 0
			}
		}
		_ = json.NewEncoder(w).Encode(f.state())
	case "/handoff":
		if f.noHandoff {
			http.NotFound(w, r)
			return
		}
		f.handoff = true
		_ = json.NewEncoder(w).Encode(f.state())
	case "/drain":
		f.handoff = true
		_ = json.NewEncoder(w).Encode(f.state())
	case "/resume":
		if f.handoff {
			http.Error(w, "daemon is in handoff mode and will exit once idle; handoff cannot be resumed, start a new daemon instead", http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(f.state())
	case "/shutdown":
		f.gone = true
	default:
		http.NotFound(w, r)
	}
}

func startAGHandoffFake(t *testing.T, profile string, f *agHandoffFake) int {
	t.Helper()
	if f.pid == 0 {
		f.pid = os.Getpid()
	}
	if f.profile == "" {
		f.profile = profile
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if err := publishDaemonPort(profile, daemonPortRecord{Port: port, PID: f.pid}); err != nil {
		t.Fatalf("publishDaemonPort: %v", err)
	}
	return port
}

func writeAGHandoffPID(t *testing.T, profile string, pid int) {
	t.Helper()
	path := daemonPIDPathForProfile(profile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func agDeadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run short-lived process: %v", err)
	}
	return cmd.Process.Pid
}

// agAddHandoffFlags adds --handoff and --overlap to a command built by
// agDrainCmdFor and applies their values from flags.
func agAddHandoffFlags(t *testing.T, cmd *cobra.Command, flags map[string]string) *cobra.Command {
	t.Helper()
	cmd.Flags().Bool("handoff", false, "")
	cmd.Flags().Bool("overlap", false, "")
	for _, k := range []string{"handoff", "overlap"} {
		if v, ok := flags[k]; ok {
			if err := cmd.Flags().Set(k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	return cmd
}

// agHandoffCmdFor is agDrainCmdFor plus --handoff and --overlap. agDrainCmdFor
// sets every flag in the map, which fails for those two unless they exist
// first, so they are added and set afterwards.
func agHandoffCmdFor(t *testing.T, name, profile string, flags map[string]string) *cobra.Command {
	t.Helper()
	plain := map[string]string{}
	for k, v := range flags {
		if k != "handoff" && k != "overlap" {
			plain[k] = v
		}
	}
	return agAddHandoffFlags(t, agDrainCmdFor(t, name, profile, plain), flags)
}

func TestAGPatchDaemonHandoffFlags(t *testing.T) {
	t.Run("AG patch: --handoff is on drain, stop and restart; --overlap is on start", func(t *testing.T) {
		for _, c := range []*cobra.Command{daemonDrainCmd, daemonStopCmd, daemonRestartCmd} {
			if c.Flags().Lookup("handoff") == nil {
				t.Errorf("daemon %s is missing --handoff", c.Name())
			}
		}
		if daemonStartCmd.Flags().Lookup("overlap") == nil {
			t.Error("daemon start is missing --overlap")
		}
	})

	t.Run("AG patch: --handoff accepts --timeout without --drain or --wait on stop and restart", func(t *testing.T) {
		for _, name := range []string{"stop", "restart"} {
			cmd := agHandoffCmdFor(t, name, "", map[string]string{"handoff": "true", "timeout": "1s"})
			if err := agValidateDrainFlags(cmd, nil); err != nil {
				t.Errorf("%s --handoff --timeout: %v", name, err)
			}
		}
		cmd := agHandoffCmdFor(t, "stop", "", map[string]string{"timeout": "1s"})
		if err := agValidateDrainFlags(cmd, nil); err == nil || !strings.Contains(err.Error(), "--timeout requires --drain or --handoff") {
			t.Errorf("stop --timeout alone: error = %v, want it to name --drain or --handoff", err)
		}
	})

	t.Run("AG patch: upstream's MULTICA_DAEMON_HANDOFF self-spawn path is not what --handoff uses", func(t *testing.T) {
		t.Setenv("MULTICA_DAEMON_HANDOFF", "")
		cmd := agHandoffCmdFor(t, "drain", "", map[string]string{"handoff": "true"})
		if !agHandoffRequested(cmd) {
			t.Fatal("--handoff not seen")
		}
		if os.Getenv("MULTICA_DAEMON_HANDOFF") != "" {
			t.Fatal("--handoff set upstream's MULTICA_DAEMON_HANDOFF")
		}
		// And the env var alone does not turn this daemon's handoff mode on.
		t.Setenv("MULTICA_DAEMON_HANDOFF", "1")
		if agHandoffRequested(agHandoffCmdFor(t, "drain", "", nil)) || agOverlapRequested(agHandoffCmdFor(t, "start", "", nil)) {
			t.Fatal("MULTICA_DAEMON_HANDOFF=1 switched on the AG handoff/overlap flags")
		}
	})

	t.Run("AG patch: --overlap is forwarded to the background child, which writes its own PID file", func(t *testing.T) {
		overlap := agHandoffCmdFor(t, "start", "", map[string]string{"overlap": "true"})
		if got := agOverlapArgs(overlap, []string{"daemon", "start"}); !slices.Equal(got, []string{"daemon", "start", "--overlap"}) {
			t.Fatalf("args = %v", got)
		}
		normal := agHandoffCmdFor(t, "start", "", nil)
		if got := agOverlapArgs(normal, []string{"daemon", "start"}); !slices.Equal(got, []string{"daemon", "start"}) {
			t.Fatalf("args without overlap = %v", got)
		}

		path := filepath.Join(t.TempDir(), "daemon.pid")
		if err := os.WriteFile(path, []byte("111"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := agWriteLauncherPID(overlap, path, 222); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != "111" {
			t.Fatalf("overlap launcher overwrote the predecessor's PID file: %q", got)
		}
		if err := agWriteLauncherPID(normal, path, 222); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != "222" {
			t.Fatalf("normal launcher PID file = %q, want 222", got)
		}
	})
}

func TestAGPatchDaemonDrainHandoff(t *testing.T) {
	fastDrainPolling(t)
	const profile = "aghandoff-cmd"

	t.Run("AG patch: drain --handoff posts /handoff, not /drain, and returns without --wait", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agHandoffFake{active: 2, goneAfterPolls: -1}
		startAGHandoffFake(t, profile, f)

		if err := runDaemonDrain(agHandoffCmdFor(t, "drain", profile, map[string]string{"handoff": "true"}), nil); err != nil {
			t.Fatalf("drain --handoff: %v", err)
		}
		if !f.called("POST /handoff") {
			t.Fatalf("calls = %v, want POST /handoff", f.callLog())
		}
		if !f.handoff {
			t.Fatal("daemon is not in handoff mode")
		}
	})

	t.Run("AG patch: drain --handoff --wait returns once the daemon has exited", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agHandoffFake{active: 2, goneAfterPolls: 4}
		startAGHandoffFake(t, profile, f)

		err := runDaemonDrain(agHandoffCmdFor(t, "drain", profile, map[string]string{"handoff": "true", "wait": "true", "timeout": "10s"}), nil)
		if err != nil {
			t.Fatalf("drain --handoff --wait: %v", err)
		}
		if !f.gone {
			t.Fatal("returned before the daemon exited")
		}
		if f.called("POST /shutdown") {
			t.Fatal("handoff must let the daemon exit on its own, not shut it down")
		}
	})

	t.Run("AG patch: drain --handoff --wait --timeout reports a daemon that is still handing off", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agHandoffFake{active: 1, goneAfterPolls: -1}
		startAGHandoffFake(t, profile, f)

		err := runDaemonDrain(agHandoffCmdFor(t, "drain", profile, map[string]string{"handoff": "true", "wait": "true", "timeout": "150ms"}), nil)
		if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "handoff mode") {
			t.Fatalf("error = %v, want a timeout that says it is still in handoff mode", err)
		}
		if f.called("POST /shutdown") || f.called("POST /resume") {
			t.Fatalf("a timeout must not stop or resume the daemon: %v", f.callLog())
		}
	})

	t.Run("AG patch: drain --handoff on a daemon that is not running is an error", func(t *testing.T) {
		mkProfiles(t, profile)
		err := runDaemonDrain(agHandoffCmdFor(t, "drain", profile, map[string]string{"handoff": "true"}), nil)
		if err == nil || !strings.Contains(err.Error(), "is not running") {
			t.Fatalf("error = %v, want 'is not running'", err)
		}
	})

	t.Run("AG patch: a daemon without /handoff says it needs the patched build", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agHandoffFake{active: 1, goneAfterPolls: -1, noHandoff: true}
		startAGHandoffFake(t, profile, f)

		err := runDaemonDrain(agHandoffCmdFor(t, "drain", profile, map[string]string{"handoff": "true"}), nil)
		if err == nil || !strings.Contains(err.Error(), "patched build") {
			t.Fatalf("error = %v, want advice to restart from the patched build", err)
		}
	})

	t.Run("AG patch: resume on a handoff daemon is refused with the daemon's message", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agHandoffFake{handoff: true, active: 1, goneAfterPolls: -1}
		startAGHandoffFake(t, profile, f)

		err := runDaemonResume(agHandoffCmdFor(t, "resume", profile, nil), nil)
		if err == nil || !strings.Contains(err.Error(), "handoff cannot be resumed") || !strings.Contains(err.Error(), "start a new daemon") {
			t.Fatalf("error = %v, want the handoff refusal", err)
		}
	})
}

func TestAGPatchDaemonStopRestartHandoff(t *testing.T) {
	fastDrainPolling(t)
	const profile = "aghandoff-stop"

	t.Run("AG patch: stop --handoff hands off and waits for the daemon to exit, without /shutdown", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agHandoffFake{active: 2, goneAfterPolls: 4}
		startAGHandoffFake(t, profile, f)

		if err := runDaemonStop(agHandoffCmdFor(t, "stop", profile, map[string]string{"handoff": "true", "timeout": "10s"}), nil); err != nil {
			t.Fatalf("stop --handoff: %v", err)
		}
		if !f.called("POST /handoff") || f.called("POST /shutdown") || f.called("POST /drain") {
			t.Fatalf("calls = %v, want POST /handoff only", f.callLog())
		}
		if !f.gone {
			t.Fatal("returned before the daemon exited")
		}
	})

	t.Run("AG patch: stop --handoff --timeout leaves a busy daemon handing off", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agHandoffFake{active: 1, goneAfterPolls: -1}
		startAGHandoffFake(t, profile, f)

		err := runDaemonStop(agHandoffCmdFor(t, "stop", profile, map[string]string{"handoff": "true", "timeout": "150ms"}), nil)
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("error = %v, want a timeout", err)
		}
		if f.called("POST /shutdown") {
			t.Fatal("the daemon was shut down although it was busy")
		}
	})

	t.Run("AG patch: stop --handoff on a daemon that is not running is an error, as plain stop is not", func(t *testing.T) {
		mkProfiles(t, profile)
		err := runDaemonStop(agHandoffCmdFor(t, "stop", profile, map[string]string{"handoff": "true"}), nil)
		if err == nil || !strings.Contains(err.Error(), "is not running") {
			t.Fatalf("stop --handoff error = %v, want 'is not running'", err)
		}
		if err := runDaemonStop(agHandoffCmdFor(t, "stop", profile, nil), nil); err != nil {
			t.Fatalf("plain stop with nothing running: %v", err)
		}
	})

	t.Run("AG patch: restart --handoff hands off, then starts the successor in overlap mode", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agHandoffFake{active: 2, goneAfterPolls: -1}
		fd := &agFakeDaemon{}
		agStubRestartPreflight(t, profile, fd)
		startAGHandoffFake(t, profile, f)

		cmd := agHandoffCmdFor(t, "restart", profile, map[string]string{"handoff": "true"})
		// The stubbed spawn fails, which ends the start: it proves the start path
		// was reached without launching a real daemon.
		err := runDaemonRestart(cmd, nil)
		if err == nil || !strings.Contains(err.Error(), "spawn stub") {
			t.Fatalf("error = %v, want the stubbed spawn error", err)
		}
		if !agOverlapRequested(cmd) {
			t.Fatal("the successor was not started with --overlap")
		}
		if !f.called("POST /handoff") || f.called("POST /shutdown") || f.called("POST /drain") {
			t.Fatalf("calls = %v, want POST /handoff and no shutdown or drain", f.callLog())
		}
		if !slices.Contains(fd.callLog(), "spawn") {
			t.Fatalf("the successor was never spawned: %v", fd.callLog())
		}
	})

	t.Run("AG patch: restart --handoff on a daemon that is not running is an error", func(t *testing.T) {
		mkProfiles(t, profile)
		err := runDaemonRestart(agHandoffCmdFor(t, "restart", profile, map[string]string{"handoff": "true"}), nil)
		if err == nil || !strings.Contains(err.Error(), "is not running") {
			t.Fatalf("error = %v, want 'is not running'", err)
		}
	})

	t.Run("AG patch: handoff commands are refused inside a task", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agHandoffFake{active: 1, goneAfterPolls: -1}
		startAGHandoffFake(t, profile, f)
		t.Setenv("MULTICA_AGENT_ID", "agent-test")
		t.Setenv("MULTICA_TASK_ID", "task-test")
		t.Setenv("MULTICA_TASK_CONFIG_ROOT", filepath.Join(t.TempDir(), "task-multica"))

		err := runDaemonDrain(agHandoffCmdFor(t, "drain", profile, map[string]string{"handoff": "true"}), nil)
		if err == nil || !strings.Contains(err.Error(), "not available inside a daemon-managed task") {
			t.Fatalf("error = %v, want the task-context guard", err)
		}
		if f.called("POST /handoff") {
			t.Fatal("a refused command still reached the daemon")
		}
	})
}

func TestAGPatchDaemonStatusHandoffRows(t *testing.T) {
	rows := func(health map[string]any) map[string]string {
		out := map[string]string{}
		for _, r := range agDaemonStatusRows(health) {
			out[r[0]] = r[1]
		}
		return out
	}

	t.Run("AG patch: a handoff daemon shows it exits once idle", func(t *testing.T) {
		got := rows(map[string]any{"draining": true, "handoff": true})
		if !strings.Contains(got["Handoff"], "exits once idle") {
			t.Fatalf("rows = %v", got)
		}
	})

	t.Run("AG patch: an overlap daemon shows its predecessor", func(t *testing.T) {
		got := rows(map[string]any{"draining": false, "predecessor_pid": float64(4242)})
		if !strings.Contains(got["Predecessor"], "pid 4242") {
			t.Fatalf("rows = %v", got)
		}
	})

	t.Run("AG patch: a normal daemon shows neither row", func(t *testing.T) {
		got := rows(map[string]any{"draining": false, "handoff": false, "predecessor_pid": float64(0)})
		if _, ok := got["Handoff"]; ok {
			t.Error("Handoff row on a normal daemon")
		}
		if _, ok := got["Predecessor"]; ok {
			t.Error("Predecessor row on a daemon without one")
		}
	})
}

func TestAGPatchOverlapDiscoversPredecessor(t *testing.T) {
	const profile = "agoverlap-discover"

	setup := func(t *testing.T, f *agHandoffFake) {
		t.Helper()
		mkProfiles(t, profile)
		if f.pid == 0 {
			f.pid = os.Getpid()
		}
		writeAGHandoffPID(t, profile, f.pid)
		startAGHandoffFake(t, profile, f)
	}

	t.Run("AG patch: reads the predecessor's PID, port, cap and active task IDs", func(t *testing.T) {
		f := &agHandoffFake{handoff: true, active: 2, maxCap: intp(5), taskIDs: []string{"t1", "t2"}, goneAfterPolls: -1}
		setup(t, f)

		pred, err := agDiscoverPredecessor(profile, 5)
		if err != nil {
			t.Fatalf("agDiscoverPredecessor: %v", err)
		}
		if pred.PID != os.Getpid() || pred.Port != resolveHealthPort(profile) || pred.MaxConcurrentTasks != 5 || !slices.Equal(pred.TaskIDs, []string{"t1", "t2"}) {
			t.Fatalf("predecessor = %+v", pred)
		}
	})

	t.Run("AG patch: a predecessor with no tasks left gives an empty list", func(t *testing.T) {
		setup(t, &agHandoffFake{handoff: true, maxCap: intp(2), taskIDs: []string{}, goneAfterPolls: -1})
		pred, err := agDiscoverPredecessor(profile, 2)
		if err != nil || len(pred.TaskIDs) != 0 {
			t.Fatalf("predecessor = %+v, err = %v", pred, err)
		}
	})

	cases := []struct {
		name    string
		setup   func(t *testing.T)
		cap     int
		wantErr []string
	}{
		{
			name:    "no PID file: nothing is running",
			setup:   func(t *testing.T) { mkProfiles(t, profile) },
			cap:     2,
			wantErr: []string{"no Daemon [" + profile + "] is running", "normal 'daemon start'"},
		},
		{
			name: "an unreadable PID file",
			setup: func(t *testing.T) {
				mkProfiles(t, profile)
				if err := os.WriteFile(daemonPIDPathForProfile(profile), []byte("not-a-pid"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			cap:     2,
			wantErr: []string{"PID file", "unreadable"},
		},
		{
			name: "a PID that is gone",
			setup: func(t *testing.T) {
				mkProfiles(t, profile)
				writeAGHandoffPID(t, profile, agDeadPID(t))
			},
			cap:     2,
			wantErr: []string{"is gone", "normal 'daemon start'"},
		},
		{
			name: "health unreadable (nothing listening)",
			setup: func(t *testing.T) {
				mkProfiles(t, profile)
				writeAGHandoffPID(t, profile, os.Getpid())
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				port := ln.Addr().(*net.TCPAddr).Port
				ln.Close()
				if err := publishDaemonPort(profile, daemonPortRecord{Port: port, PID: os.Getpid()}); err != nil {
					t.Fatal(err)
				}
			},
			cap:     2,
			wantErr: []string{"cannot read the predecessor's /health", "refusing to start"},
		},
		{
			name: "a daemon that is not in handoff mode",
			setup: func(t *testing.T) {
				setup(t, &agHandoffFake{maxCap: intp(2), taskIDs: []string{"t1"}, goneAfterPolls: -1})
			},
			cap:     2,
			wantErr: []string{"not in handoff mode", "drain --handoff"},
		},
		{
			name: "a predecessor that does not report its cap",
			setup: func(t *testing.T) {
				setup(t, &agHandoffFake{handoff: true, taskIDs: []string{"t1"}, goneAfterPolls: -1})
			},
			cap:     2,
			wantErr: []string{"does not report its task cap"},
		},
		{
			name: "a different cap",
			setup: func(t *testing.T) {
				setup(t, &agHandoffFake{handoff: true, maxCap: intp(3), taskIDs: []string{"t1"}, goneAfterPolls: -1})
			},
			cap:     5,
			wantErr: []string{"cap mismatch", "=5", "has 3"},
		},
		{
			name:    "a predecessor that does not report task IDs",
			setup:   func(t *testing.T) { setup(t, &agHandoffFake{handoff: true, maxCap: intp(2), goneAfterPolls: -1}) },
			cap:     2,
			wantErr: []string{"does not report its active task IDs"},
		},
		{
			name: "a different PID answering on the port",
			setup: func(t *testing.T) {
				mkProfiles(t, profile)
				writeAGHandoffPID(t, profile, os.Getpid()+1)
				startAGHandoffFake(t, profile, &agHandoffFake{handoff: true, maxCap: intp(2), taskIDs: []string{"t1"}, goneAfterPolls: -1})
			},
			cap:     2,
			wantErr: []string{"not the PID file's pid"},
		},
		{
			name: "a daemon of another profile on the port",
			setup: func(t *testing.T) {
				mkProfiles(t, profile)
				writeAGHandoffPID(t, profile, os.Getpid())
				startAGHandoffFake(t, profile, &agHandoffFake{profile: "someone-else", handoff: true, maxCap: intp(2), taskIDs: []string{"t1"}, goneAfterPolls: -1})
			},
			cap:     2,
			wantErr: []string{"cannot read the predecessor's /health"},
		},
	}
	for _, c := range cases {
		t.Run("AG patch: fails closed on "+c.name, func(t *testing.T) {
			c.setup(t)
			pred, err := agDiscoverPredecessor(profile, c.cap)
			if err == nil {
				t.Fatalf("agDiscoverPredecessor succeeded with %+v; an overlap start must fail closed", pred)
			}
			for _, want := range c.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if pred.PID != 0 || pred.TaskIDs != nil {
				t.Errorf("a failed discovery returned a predecessor: %+v", pred)
			}
		})
	}
}

func TestAGPatchHandoffLogSwitchAndLock(t *testing.T) {
	t.Run("AG patch: the log switch moves later output to the new file and closes the old one", func(t *testing.T) {
		dir := t.TempDir()
		first, err := os.Create(filepath.Join(dir, "daemon.log"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := os.Create(filepath.Join(dir, "daemon.handoff.1.log"))
		if err != nil {
			t.Fatal(err)
		}
		sw := &agLogSwitch{w: first}
		fmt.Fprintln(sw, "before")
		sw.SwitchTo(second)
		fmt.Fprintln(sw, "after")
		_ = second.Close()

		if got, _ := os.ReadFile(first.Name()); string(got) != "before\n" {
			t.Errorf("daemon.log = %q, want only the pre-handoff line (two writers must never share it)", got)
		}
		if got, _ := os.ReadFile(second.Name()); string(got) != "after\n" {
			t.Errorf("handoff log = %q, want the post-handoff line", got)
		}
		if _, err := first.Write([]byte("x")); err == nil {
			t.Error("the old writer was left open")
		}
	})

	t.Run("AG patch: the handoff log is one file per process, beside daemon.log", func(t *testing.T) {
		mkProfiles(t, "aghandoff-log")
		a, b := agHandoffLogPath("aghandoff-log", 100), agHandoffLogPath("aghandoff-log", 200)
		if a == b || filepath.Dir(a) != filepath.Dir(daemonLogPathForProfile("aghandoff-log")) || a == daemonLogPathForProfile("aghandoff-log") {
			t.Fatalf("paths %q and %q should be distinct per PID and not daemon.log", a, b)
		}
	})

	t.Run("AG patch: closing the profile lock lets the successor take it at once", func(t *testing.T) {
		mkProfiles(t, "aghandoff-lock")
		old, err := acquireDaemonProfileLock("aghandoff-lock", false)
		if err != nil {
			t.Fatal(err)
		}
		// What the handoff hook does with the lock.
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		next, err := acquireDaemonProfileLock("aghandoff-lock", true)
		if err != nil {
			t.Fatalf("successor could not take the lock: %v", err)
		}
		defer next.Close()
		if time.Since(start) > 2*time.Second {
			t.Fatalf("taking the released lock took %s", time.Since(start))
		}
	})
}

func TestAGPatchWaitDaemonExit(t *testing.T) {
	fastDrainPolling(t)
	const profile = "aghandoff-wait"

	t.Run("AG patch: waiting reports the remaining tasks until the daemon is gone", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agHandoffFake{handoff: true, active: 3, goneAfterPolls: 5}
		port := startAGHandoffFake(t, profile, f)

		var progress bytes.Buffer
		if err := agWaitDaemonExit(port, 10*time.Second, &progress); err != nil {
			t.Fatalf("agWaitDaemonExit: %v", err)
		}
		if !strings.Contains(progress.String(), "Waiting for active tasks") {
			t.Fatalf("progress = %q", progress.String())
		}
	})

	t.Run("AG patch: a daemon that is already gone counts as exited", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agHandoffFake{gone: true, goneAfterPolls: -1}
		port := startAGHandoffFake(t, profile, f)
		if err := agWaitDaemonExit(port, 10*time.Second, &bytes.Buffer{}); err != nil {
			t.Fatalf("agWaitDaemonExit: %v", err)
		}
	})
}
