package main

// AG patch: daemon drain CLI tests.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// agFakeDaemon stands in for the daemon's health listener; idleAfterPolls is
// the number of /health reads while draining before it goes idle (-1: never).
type agFakeDaemon struct {
	mu             sync.Mutex
	profile        string
	draining       bool
	startedAt      string
	active         int
	running        int
	waiting        int
	idleAfterPolls int
	polls          int
	noDrain        bool // behave like a stock daemon: no /drain, no /resume
	stopped        bool
	calls          []string

	failPreflightFrom int // with agStubRestartPreflight: fail preflights from the Nth on (0: never)
}

func (f *agFakeDaemon) state() map[string]any {
	idle := f.draining && f.active == 0
	m := map[string]any{
		"status":                   "running",
		"profile":                  f.profile,
		"pid":                      os.Getpid(),
		"active_task_count":        f.active,
		"running_task_count":       f.running,
		"resource_wait_task_count": f.waiting,
		"draining":                 f.draining,
		"idle":                     idle,
	}
	if f.draining {
		m["drain_started_at"] = f.startedAt
	}
	return m
}

func (f *agFakeDaemon) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *agFakeDaemon) called(call string) bool {
	return slices.Contains(f.callLog(), call)
}

func (f *agFakeDaemon) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *agFakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/health":
		if f.stopped {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "stopped"})
			return
		}
		if f.draining {
			f.polls++
			if f.idleAfterPolls >= 0 && f.polls >= f.idleAfterPolls {
				f.active, f.running, f.waiting = 0, 0, 0
			}
		}
		_ = json.NewEncoder(w).Encode(f.state())
	case "/drain":
		if f.noDrain {
			http.NotFound(w, r)
			return
		}
		if !f.draining {
			f.draining = true
			f.startedAt = time.Now().UTC().Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(f.state())
	case "/resume":
		if f.noDrain {
			http.NotFound(w, r)
			return
		}
		f.draining = false
		f.startedAt = ""
		_ = json.NewEncoder(w).Encode(f.state())
	case "/shutdown":
		f.stopped = true
	default:
		http.NotFound(w, r)
	}
}

// startAGFakeDaemon publishes a fake daemon as the given profile's daemon and
// returns it. The profile must already exist (mkProfiles).
func startAGFakeDaemon(t *testing.T, profile string, f *agFakeDaemon) (*agFakeDaemon, int) {
	t.Helper()
	f.profile = profile
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if err := publishDaemonPort(profile, daemonPortRecord{Port: port, PID: os.Getpid()}); err != nil {
		t.Fatalf("publishDaemonPort: %v", err)
	}
	if got := resolveHealthPort(profile); got != port {
		t.Fatalf("resolveHealthPort = %d, want the fake daemon's %d", got, port)
	}
	return f, port
}

func agDrainCmdFor(t *testing.T, name, profile string, flags map[string]string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: name}
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().Bool("foreground", false, "")
	cmd.Flags().Bool("wait", false, "")
	cmd.Flags().Bool("drain", false, "")
	cmd.Flags().Duration("timeout", 0, "")
	flags["profile"] = profile
	for k, v := range flags {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatalf("set --%s=%s: %v", k, v, err)
		}
	}
	return cmd
}

func fastDrainPolling(t *testing.T) {
	t.Helper()
	orig := agDrainPollInterval
	agDrainPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { agDrainPollInterval = orig })
}

func TestAGPatchDaemonDrainCommand(t *testing.T) {
	fastDrainPolling(t)
	const profile = "agdrain-cmd"

	t.Run("AG patch: drain prints the counts and returns without --wait", func(t *testing.T) {
		mkProfiles(t, profile)
		f, port := startAGFakeDaemon(t, profile, &agFakeDaemon{active: 3, running: 2, waiting: 1, idleAfterPolls: -1})

		var out, progress bytes.Buffer
		if err := agDrainAndReport(port, false, 0, &out, &progress); err != nil {
			t.Fatalf("drain: %v", err)
		}
		for _, want := range []string{
			"Draining since ",
			"Active tasks: 3 (2 running, 1 waiting for resources)",
			"Idle: no",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output missing %q:\n%s", want, out.String())
			}
		}
		if !f.draining {
			t.Error("daemon is not draining")
		}
	})

	t.Run("AG patch: drain --wait exits 0 once the daemon is idle", func(t *testing.T) {
		mkProfiles(t, profile)
		f, _ := startAGFakeDaemon(t, profile, &agFakeDaemon{active: 2, running: 2, idleAfterPolls: 4})

		err := runDaemonDrain(agDrainCmdFor(t, "drain", profile, map[string]string{"wait": "true", "timeout": "5s"}), nil)
		if code := cli.ExitCodeFor(err); err != nil || code != 0 {
			t.Fatalf("drain --wait = %v (exit %d), want exit 0", err, code)
		}
		if !f.draining {
			t.Error("drain --wait must leave the daemon draining: stop or resume is the caller's call")
		}
	})

	t.Run("AG patch: drain --wait on an already idle daemon returns at once", func(t *testing.T) {
		mkProfiles(t, profile)
		_, port := startAGFakeDaemon(t, profile, &agFakeDaemon{idleAfterPolls: -1})

		var out bytes.Buffer
		if err := agDrainAndReport(port, true, time.Second, &out, &out); err != nil {
			t.Fatalf("drain --wait: %v", err)
		}
		if !strings.Contains(out.String(), "Idle: yes") || strings.Contains(out.String(), "Waiting for") {
			t.Errorf("want an immediate idle report, got:\n%s", out.String())
		}
	})

	t.Run("AG patch: drain --wait --timeout exits 1 and leaves drain on", func(t *testing.T) {
		mkProfiles(t, profile)
		f, _ := startAGFakeDaemon(t, profile, &agFakeDaemon{active: 1, running: 1, idleAfterPolls: -1})

		start := time.Now()
		err := runDaemonDrain(agDrainCmdFor(t, "drain", profile, map[string]string{"wait": "true", "timeout": "150ms"}), nil)
		if err == nil {
			t.Fatal("drain --wait --timeout returned nil, want a timeout error")
		}
		if code := cli.ExitCodeFor(err); code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "still draining") {
			t.Errorf("error = %q, want it to say it timed out and drain is still on", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("took %s, the timeout should have fired at 150ms", elapsed)
		}
		if !f.draining {
			t.Error("a timeout must leave drain on")
		}
		if f.called("POST /resume") {
			t.Error("a timeout must not resume the daemon")
		}
	})

	t.Run("AG patch: drain --wait fails if the drain is cancelled underneath it", func(t *testing.T) {
		mkProfiles(t, profile)
		f, port := startAGFakeDaemon(t, profile, &agFakeDaemon{active: 1, running: 1, idleAfterPolls: -1})

		go func() {
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				f.mu.Lock()
				if f.polls > 0 {
					f.draining = false
					f.mu.Unlock()
					return
				}
				f.mu.Unlock()
				time.Sleep(5 * time.Millisecond)
			}
		}()
		var out bytes.Buffer
		err := agDrainAndReport(port, true, 5*time.Second, &out, &out)
		if err == nil || !strings.Contains(err.Error(), "no longer draining") {
			t.Fatalf("error = %v, want a resumed-underneath error", err)
		}
	})

	t.Run("AG patch: drain --wait rides out a failed poll", func(t *testing.T) {
		mkProfiles(t, profile)
		var polls int
		var mu sync.Mutex
		f := &agFakeDaemon{active: 1, running: 1, idleAfterPolls: 4}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			polls++
			n := polls
			mu.Unlock()
			// The second /health read (the first poll of the wait loop) fails.
			if r.URL.Path == "/health" && n == 3 {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			f.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		f.profile = profile
		port := srv.Listener.Addr().(*net.TCPAddr).Port

		var out bytes.Buffer
		if err := agDrainAndReport(port, true, 5*time.Second, &out, &out); err != nil {
			t.Fatalf("a single failed poll ended --wait: %v\n%s", err, out.String())
		}
	})

	t.Run("AG patch: drain --wait gives up when the daemon stops answering and says drain may still be on", func(t *testing.T) {
		mkProfiles(t, profile)
		f, port := startAGFakeDaemon(t, profile, &agFakeDaemon{active: 1, running: 1, idleAfterPolls: -1})
		go func() {
			for !f.called("POST /drain") {
				time.Sleep(time.Millisecond)
			}
			f.mu.Lock()
			f.stopped = true
			f.mu.Unlock()
		}()

		var out bytes.Buffer
		err := agDrainAndReport(port, true, 5*time.Second, &out, &out)
		if err == nil || !strings.Contains(err.Error(), "lost contact") || !strings.Contains(err.Error(), "multica daemon resume") {
			t.Fatalf("error = %v, want a lost-contact error that points at resume", err)
		}
	})

	t.Run("AG patch: a daemon without drain support gets an actionable error", func(t *testing.T) {
		mkProfiles(t, profile)
		startAGFakeDaemon(t, profile, &agFakeDaemon{noDrain: true})

		err := runDaemonDrain(agDrainCmdFor(t, "drain", profile, map[string]string{}), nil)
		if err == nil || !strings.Contains(err.Error(), "does not support /drain") {
			t.Fatalf("error = %v, want an unsupported-daemon error", err)
		}
	})

	t.Run("AG patch: drain with no daemon running fails", func(t *testing.T) {
		mkProfiles(t, profile)
		err := runDaemonDrain(agDrainCmdFor(t, "drain", profile, map[string]string{}), nil)
		if err == nil || !strings.Contains(err.Error(), "not running") {
			t.Fatalf("error = %v, want a not-running error", err)
		}
	})

	t.Run("AG patch: resume clears drain", func(t *testing.T) {
		mkProfiles(t, profile)
		f, _ := startAGFakeDaemon(t, profile, &agFakeDaemon{draining: true, idleAfterPolls: -1, startedAt: time.Now().UTC().Format(time.RFC3339)})

		if err := runDaemonResume(agDrainCmdFor(t, "resume", profile, map[string]string{}), nil); err != nil {
			t.Fatalf("resume: %v", err)
		}
		if f.draining || !f.called("POST /resume") || f.called("POST /drain") || f.called("POST /shutdown") {
			t.Errorf("draining=%v calls=%v, want only a resume", f.draining, f.callLog())
		}
	})
}

func TestAGPatchDaemonDrainFlagValidation(t *testing.T) {
	cases := []struct {
		name    string
		cmd     string
		flags   map[string]string
		wantErr string
	}{
		{"AG patch: drain --timeout needs --wait", "drain", map[string]string{"timeout": "1s"}, "--timeout requires --wait"},
		{"AG patch: stop --timeout needs --drain", "stop", map[string]string{"timeout": "1s"}, "--timeout requires --drain"},
		{"AG patch: restart --timeout needs --drain", "restart", map[string]string{"timeout": "1s"}, "--timeout requires --drain"},
		{"AG patch: a negative timeout is rejected", "drain", map[string]string{"wait": "true", "timeout": "-1s"}, "must not be negative"},
		{"AG patch: stop --drain --timeout is accepted", "stop", map[string]string{"drain": "true", "timeout": "1s"}, ""},
		{"AG patch: drain --wait --timeout is accepted", "drain", map[string]string{"wait": "true", "timeout": "1s"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := agDrainCmdFor(t, c.cmd, "", c.flags)
			err := agValidateDrainFlags(cmd, nil)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("error = %v, want none", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("error = %v, want %q", err, c.wantErr)
			}
		})
	}

	t.Run("AG patch: the real commands run the validator", func(t *testing.T) {
		cases := []struct {
			cmd     *cobra.Command
			timeout string
			want    string
		}{
			{daemonDrainCmd, "1s", "--timeout requires --wait"},
			{daemonDrainCmd, "-1s", "must not be negative"},
			{daemonStopCmd, "1s", "--timeout requires --drain"},
			{daemonRestartCmd, "1s", "--timeout requires --drain"},
		}
		for _, c := range cases {
			f := c.cmd.Flags().Lookup("timeout")
			if f == nil || c.cmd.PreRunE == nil {
				t.Fatalf("daemon %s is missing --timeout or its validator", c.cmd.Name())
			}
			if err := c.cmd.Flags().Set("timeout", c.timeout); err != nil {
				t.Fatal(err)
			}
			err := c.cmd.PreRunE(c.cmd, nil)
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("daemon %s --timeout %s: error = %v, want %q", c.cmd.Name(), c.timeout, err, c.want)
			}
		}
		for _, c := range []*cobra.Command{daemonStopCmd, daemonRestartCmd} {
			if c.Flags().Lookup("drain") == nil {
				t.Errorf("daemon %s is missing --drain", c.Name())
			}
		}
		if daemonDrainCmd.Flags().Lookup("wait") == nil {
			t.Error("daemon drain is missing --wait")
		}
		for _, name := range []string{"drain", "resume"} {
			if found, _, err := daemonCmd.Find([]string{name}); err != nil || found.Name() != name {
				t.Errorf("daemon %s is not registered: %v", name, err)
			}
		}
	})
}

func TestAGPatchDaemonDrainIsHumanOnly(t *testing.T) {
	const profile = "agdrain-guard"
	mkProfiles(t, profile)
	f, _ := startAGFakeDaemon(t, profile, &agFakeDaemon{active: 1, idleAfterPolls: -1})

	t.Setenv("MULTICA_AGENT_ID", "agent-test")
	t.Setenv("MULTICA_TASK_ID", "task-test")
	t.Setenv("MULTICA_TASK_CONFIG_ROOT", filepath.Join(t.TempDir(), "task-multica"))

	cases := map[string]func() error{
		"drain": func() error { return runDaemonDrain(agDrainCmdFor(t, "drain", profile, map[string]string{}), nil) },
		"drain --wait": func() error {
			return runDaemonDrain(agDrainCmdFor(t, "drain", profile, map[string]string{"wait": "true"}), nil)
		},
		"resume": func() error { return runDaemonResume(agDrainCmdFor(t, "resume", profile, map[string]string{}), nil) },
		"stop --drain": func() error {
			return runDaemonStop(agDrainCmdFor(t, "stop", profile, map[string]string{"drain": "true"}), nil)
		},
		"restart --drain": func() error {
			return runDaemonRestart(agDrainCmdFor(t, "restart", profile, map[string]string{"drain": "true"}), nil)
		},
	}
	for name, run := range cases {
		t.Run("AG patch: "+name+" is refused inside a task", func(t *testing.T) {
			err := run()
			if err == nil || !strings.Contains(err.Error(), "not available inside a daemon-managed task") {
				t.Fatalf("error = %v, want the task-context guard", err)
			}
		})
	}

	for _, c := range f.callLog() {
		if strings.HasPrefix(c, "POST") {
			t.Fatalf("a refused command still reached the daemon: %s", c)
		}
	}
}

func TestAGPatchDaemonStopRestartDrain(t *testing.T) {
	fastDrainPolling(t)
	const profile = "agdrain-stop"

	t.Run("AG patch: stop --drain waits for idle, then shuts the daemon down", func(t *testing.T) {
		mkProfiles(t, profile)
		f, _ := startAGFakeDaemon(t, profile, &agFakeDaemon{active: 2, running: 2, idleAfterPolls: 4})

		err := runDaemonStop(agDrainCmdFor(t, "stop", profile, map[string]string{"drain": "true", "timeout": "5s"}), nil)
		if err != nil {
			t.Fatalf("stop --drain: %v", err)
		}
		calls := f.callLog()
		drainAt, shutdownAt := slices.Index(calls, "POST /drain"), slices.Index(calls, "POST /shutdown")
		if drainAt < 0 || shutdownAt < 0 || drainAt > shutdownAt {
			t.Fatalf("calls = %v, want POST /drain before POST /shutdown", calls)
		}
		if f.active != 0 {
			t.Errorf("shutdown went ahead with %d active tasks", f.active)
		}
	})

	t.Run("AG patch: stop --drain --timeout does not stop a busy daemon", func(t *testing.T) {
		mkProfiles(t, profile)
		f, _ := startAGFakeDaemon(t, profile, &agFakeDaemon{active: 1, running: 1, idleAfterPolls: -1})

		err := runDaemonStop(agDrainCmdFor(t, "stop", profile, map[string]string{"drain": "true", "timeout": "150ms"}), nil)
		if err == nil || !strings.Contains(err.Error(), "timed out") || cli.ExitCodeFor(err) != 1 {
			t.Fatalf("error = %v (exit %d), want a timeout with exit 1", err, cli.ExitCodeFor(err))
		}
		if f.called("POST /shutdown") {
			t.Fatal("the daemon was shut down although it never went idle")
		}
		if !f.draining {
			t.Error("a timed-out stop --drain must leave drain on")
		}
	})

	t.Run("AG patch: stop without --drain does not drain", func(t *testing.T) {
		mkProfiles(t, profile)
		f, _ := startAGFakeDaemon(t, profile, &agFakeDaemon{active: 1, idleAfterPolls: -1})

		if err := runDaemonStop(agDrainCmdFor(t, "stop", profile, map[string]string{}), nil); err != nil {
			t.Fatalf("stop: %v", err)
		}
		if f.called("POST /drain") {
			t.Error("plain stop drained the daemon")
		}
		if !f.called("POST /shutdown") {
			t.Error("plain stop did not shut the daemon down")
		}
	})

	t.Run("AG patch: restart --drain --timeout does not restart a busy daemon", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agFakeDaemon{active: 1, running: 1, idleAfterPolls: -1}
		agStubRestartPreflight(t, profile, f)
		startAGFakeDaemon(t, profile, f)

		err := runDaemonRestart(agDrainCmdFor(t, "restart", profile, map[string]string{"drain": "true", "timeout": "150ms"}), nil)
		if err == nil || !strings.Contains(err.Error(), "timed out") || cli.ExitCodeFor(err) != 1 {
			t.Fatalf("error = %v (exit %d), want a timeout with exit 1", err, cli.ExitCodeFor(err))
		}
		if f.called("POST /shutdown") || f.called("spawn") {
			t.Fatalf("the daemon was restarted although it never went idle: %v", f.callLog())
		}
		if !f.draining {
			t.Error("a timed-out restart --drain must leave drain on")
		}
	})

	t.Run("AG patch: restart --drain waits for idle, re-checks, shuts down, then starts", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agFakeDaemon{active: 2, running: 2, idleAfterPolls: 4}
		agStubRestartPreflight(t, profile, f)
		startAGFakeDaemon(t, profile, f)

		// The stubbed spawn fails, which is what ends the restart: it proves
		// the start path was reached without launching a real daemon.
		err := runDaemonRestart(agDrainCmdFor(t, "restart", profile, map[string]string{"drain": "true", "timeout": "5s"}), nil)
		if err == nil || !strings.Contains(err.Error(), "spawn stub") {
			t.Fatalf("error = %v, want the stubbed spawn error", err)
		}
		var steps []string
		for _, c := range f.callLog() {
			if c != "GET /health" {
				steps = append(steps, c)
			}
		}
		want := []string{"preflight", "POST /drain", "preflight", "POST /shutdown", "spawn"}
		if !slices.Equal(steps, want) {
			t.Fatalf("steps = %v, want %v", steps, want)
		}
		if f.active != 0 {
			t.Errorf("shutdown went ahead with %d active tasks", f.active)
		}
	})

	t.Run("AG patch: restart --drain stops short when the post-drain preflight fails", func(t *testing.T) {
		mkProfiles(t, profile)
		// Fail the second preflight, the one after the drain.
		f := &agFakeDaemon{active: 1, running: 1, idleAfterPolls: 3, failPreflightFrom: 2}
		agStubRestartPreflight(t, profile, f)
		startAGFakeDaemon(t, profile, f)

		err := runDaemonRestart(agDrainCmdFor(t, "restart", profile, map[string]string{"drain": "true", "timeout": "5s"}), nil)
		if err == nil || !strings.Contains(err.Error(), "refusing to restart") || !strings.Contains(err.Error(), "multica daemon resume") {
			t.Fatalf("error = %v, want a refusal that points at resume", err)
		}
		if f.called("POST /shutdown") || f.called("spawn") {
			t.Fatalf("the daemon was restarted after a failed preflight: %v", f.callLog())
		}
		if !f.draining {
			t.Error("drain must stay on when the restart is refused")
		}
	})

	t.Run("AG patch: restart --drain fails its preflight before draining", func(t *testing.T) {
		mkProfiles(t, profile)
		f, _ := startAGFakeDaemon(t, profile, &agFakeDaemon{active: 1, idleAfterPolls: -1})

		err := runDaemonRestart(agDrainCmdFor(t, "restart", profile, map[string]string{"drain": "true"}), nil)
		if err == nil || !strings.Contains(err.Error(), "multica login") {
			t.Fatalf("error = %v, want the not-logged-in preflight error", err)
		}
		if f.called("POST /drain") {
			t.Error("restart drained the daemon although it could not have restarted it")
		}
	})
}

// agStubRestartPreflight logs each restart preflight and the daemon spawn into
// f's call log; nothing real is started.
func agStubRestartPreflight(t *testing.T, profile string, f *agFakeDaemon) {
	t.Helper()
	t.Setenv("MULTICA_SERVER_URL", "")
	var preflights int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record("preflight")
		f.mu.Lock()
		preflights++
		fail := f.failPreflightFrom > 0 && preflights >= f.failPreflightFrom
		f.mu.Unlock()
		if fail {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(api.Close)
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{Token: "mul_fake", ServerURL: api.URL}, profile); err != nil {
		t.Fatalf("SaveCLIConfigForProfile: %v", err)
	}

	origExe, origSync := daemonExecutable, syncDaemonAutostart
	daemonExecutable = func() (string, error) {
		f.record("spawn")
		return "", errors.New("spawn stub")
	}
	syncDaemonAutostart = func(string, bool) {}
	t.Cleanup(func() { daemonExecutable, syncDaemonAutostart = origExe, origSync })
}

func TestAGPatchDaemonStatusDrainRows(t *testing.T) {
	render := func(health map[string]any) map[string]string {
		var buf bytes.Buffer
		printDaemonStatusReport(&buf, "Daemon", health)
		return parseStatusRows(buf.String())
	}
	startedAt := time.Date(2026, 10, 15, 9, 41, 0, 0, time.UTC)
	base := func() map[string]any {
		return map[string]any{
			"status": "running", "pid": 1, "uptime": "1h",
			"active_task_count": float64(3), "running_task_count": float64(2), "resource_wait_task_count": float64(1),
		}
	}

	t.Run("AG patch: a stock daemon's status is unchanged", func(t *testing.T) {
		rows := render(base())
		for _, key := range []string{"Claiming", "Active tasks", "Idle"} {
			if _, ok := rows[key]; ok {
				t.Errorf("stock daemon output has a %q row", key)
			}
		}
	})

	t.Run("AG patch: a claiming daemon shows claiming and active tasks but no idle", func(t *testing.T) {
		h := base()
		h["draining"] = false
		h["idle"] = false
		rows := render(h)
		if rows["Claiming"] != "yes" {
			t.Errorf("Claiming = %q, want yes", rows["Claiming"])
		}
		if rows["Active tasks"] != "3 (2 running, 1 waiting for resources)" {
			t.Errorf("Active tasks = %q", rows["Active tasks"])
		}
		if _, ok := rows["Idle"]; ok {
			t.Error("Idle is only shown while draining")
		}
	})

	t.Run("AG patch: a draining daemon shows since and idle", func(t *testing.T) {
		h := base()
		h["draining"] = true
		h["drain_started_at"] = startedAt.Format(time.RFC3339)
		h["idle"] = false
		rows := render(h)
		want := "no (draining since " + startedAt.Local().Format("15:04") + ")"
		if rows["Claiming"] != want {
			t.Errorf("Claiming = %q, want %q", rows["Claiming"], want)
		}
		if rows["Idle"] != "no" {
			t.Errorf("Idle = %q, want no", rows["Idle"])
		}

		h["active_task_count"], h["running_task_count"], h["resource_wait_task_count"] = float64(0), float64(0), float64(0)
		h["idle"] = true
		rows = render(h)
		if rows["Idle"] != "yes" || rows["Active tasks"] != "0 (0 running, 0 waiting for resources)" {
			t.Errorf("idle rows = %v", rows)
		}
	})

	t.Run("AG patch: active tasks without the split prints the total only", func(t *testing.T) {
		h := map[string]any{"status": "running", "pid": 1, "uptime": "1h", "draining": true, "active_task_count": float64(4)}
		if got := render(h)["Active tasks"]; got != "4" {
			t.Errorf("Active tasks = %q, want 4", got)
		}
	})
}
