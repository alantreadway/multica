package daemon

// AG patch: overlap mode driven through Run, the real caller of the overlap
// loop and the startup sync. TestAGPatch* with "AG patch:" subtests, as counted
// by ag-patches/test.sh.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// agPathLog records the path of every request that passes through it.
type agPathLog struct {
	mu    sync.Mutex
	paths []string
}

func (l *agPathLog) add(p string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, p)
}

func (l *agPathLog) count(suffix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, p := range l.paths {
		if strings.HasSuffix(p, suffix) {
			n++
		}
	}
	return n
}

// failed returns the task IDs POSTed to /api/daemon/tasks/<id>/fail.
func (l *agPathLog) failed() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var ids []string
	for _, p := range l.paths {
		if rest, ok := strings.CutPrefix(p, "/api/daemon/tasks/"); ok {
			if id, ok := strings.CutSuffix(rest, "/fail"); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func agRecordingProxy(t *testing.T, upstream string) (*httptest.Server, *agPathLog) {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	log := &agPathLog{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.URL.Path)
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

func agWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAGPatchOverlapDaemonRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell and PID probing")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("needs a sleep binary")
	}

	t.Run("AG patch: a running overlap daemon never recovers orphans, ends overlap when the predecessor dies and fails only its leftovers", func(t *testing.T) {
		stageFakeAgent(t)
		redirectTestHome(t, t.TempDir())

		fx := newBatchFixture(t)
		fx.setWorkspaces(WorkspaceInfo{ID: "ws-1", Name: "one"})
		proxy, calls := agRecordingProxy(t, fx.server.URL)
		if err := cli.SaveCLIConfig(cli.CLIConfig{ServerURL: proxy.URL, Token: "mul_test_token"}); err != nil {
			t.Fatalf("save cli config: %v", err)
		}
		cfg, err := LoadConfig(Overrides{ServerURL: proxy.URL, WorkspacesRoot: t.TempDir(), MaxConcurrentTasks: 2})
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		stubAgentProbe(t, cfg.Agents)

		proc := exec.Command(sleep, "60")
		if err := proc.Start(); err != nil {
			t.Fatal(err)
		}
		reaped := make(chan struct{})
		go func() { _ = proc.Wait(); close(reaped) }()
		t.Cleanup(func() { _ = proc.Process.Kill(); <-reaped })

		pred := newAGFakePredecessor(t, proc.Process.Pid, "a", "b")
		d := New(cfg, slog.New(slog.NewTextHandler(testNopWriter{}, &slog.HandlerOptions{Level: slog.LevelWarn})))
		d.AGEnableOverlap(AGPredecessor{PID: proc.Process.Pid, Port: pred.port(), TaskIDs: []string{"a", "b"}, MaxConcurrentTasks: 2})

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runDone := make(chan error, 1)
		go func() { runDone <- d.RunWithHealthListener(ctx, ln) }()

		// The startup sync registers the runtimes; the predecessor's tasks must
		// survive it.
		agWaitFor(t, "the startup sync to register runtimes", func() bool { return calls.count("/api/daemon/register") > 0 })
		if got := calls.count("/recover-orphans"); got != 0 {
			t.Fatalf("recover-orphans called %d times during an overlap startup, which fails the predecessor's tasks", got)
		}

		// The predecessor finishes "b"; the Run-started loop must notice.
		pred.set(proc.Process.Pid, false, "a")
		agWaitFor(t, "the overlap loop to shrink the tracked tasks", func() bool {
			d.agHandoff.mu.Lock()
			defer d.agHandoff.mu.Unlock()
			return slices.Equal(sortedIDs(d.agHandoff.predTasks), []string{"a"})
		})
		if !d.agOverlapActive() || len(calls.failed()) != 0 {
			t.Fatalf("overlap=%v failed=%v while the predecessor is alive", d.agOverlapActive(), calls.failed())
		}

		_ = proc.Process.Kill()
		agWaitFor(t, "overlap to end after the predecessor died", func() bool { return !d.agOverlapActive() })
		if got := calls.failed(); !slices.Equal(got, []string{"a"}) {
			t.Fatalf("failed tasks = %v, want only the leftover [a]", got)
		}
		if got := calls.count("/recover-orphans"); got != 0 {
			t.Fatalf("recover-orphans called %d times by an overlap daemon", got)
		}

		cancel()
		select {
		case err := <-runDone:
			if err != nil && err != context.Canceled {
				t.Fatalf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	})
}

// agStartRunDaemon starts a real daemon through RunWithHealthListener against a
// fake multica server, once its startup sync has registered the runtimes.
func agStartRunDaemon(t *testing.T) (d *Daemon, calls *agPathLog, healthURL string, runDone <-chan error, cancel context.CancelFunc) {
	t.Helper()
	stageFakeAgent(t)
	redirectTestHome(t, t.TempDir())

	fx := newBatchFixture(t)
	fx.setWorkspaces(WorkspaceInfo{ID: "ws-1", Name: "one"})
	proxy, calls := agRecordingProxy(t, fx.server.URL)
	if err := cli.SaveCLIConfig(cli.CLIConfig{ServerURL: proxy.URL, Token: "mul_test_token"}); err != nil {
		t.Fatalf("save cli config: %v", err)
	}
	cfg, err := LoadConfig(Overrides{ServerURL: proxy.URL, WorkspacesRoot: t.TempDir(), MaxConcurrentTasks: 2})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	stubAgentProbe(t, cfg.Agents)
	d = New(cfg, slog.New(slog.NewTextHandler(testNopWriter{}, &slog.HandlerOptions{Level: slog.LevelWarn})))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- d.RunWithHealthListener(ctx, ln) }()
	agWaitFor(t, "the startup sync to register runtimes", func() bool { return calls.count("/api/daemon/register") > 0 })
	return d, calls, "http://" + ln.Addr().String(), done, cancel
}

func TestAGPatchHandoffDaemonRunExitsWhenIdle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	// The CLI treats a nil or context.Canceled return from the daemon as a clean
	// exit (exit status 0), which the README's launchd KeepAlive setup relies on.
	cleanExit := func(err error) bool { return err == nil || errors.Is(err, context.Canceled) }
	waitReturn := func(t *testing.T, runDone <-chan error, within time.Duration) error {
		t.Helper()
		select {
		case err := <-runDone:
			return err
		case <-time.After(within):
			t.Fatal("Run did not return")
			return nil
		}
	}

	t.Run("AG patch: a normal daemon deregisters its runtimes when stopped (control)", func(t *testing.T) {
		_, calls, _, runDone, cancel := agStartRunDaemon(t)
		cancel()
		if err := waitReturn(t, runDone, 10*time.Second); !cleanExit(err) {
			t.Fatalf("Run: %v", err)
		}
		if got := calls.count("/api/daemon/deregister"); got != 1 {
			t.Fatalf("deregister calls = %d, want 1; the handoff case below proves nothing otherwise", got)
		}
	})

	t.Run("AG patch: Run returns cleanly once a handoff daemon is idle, without deregistering", func(t *testing.T) {
		d, calls, healthURL, runDone, _ := agStartRunDaemon(t)

		resp, err := http.Post(healthURL+"/handoff", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /handoff: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /handoff = %d, want 200", resp.StatusCode)
		}
		if !d.agHandoffActive() {
			t.Fatal("daemon is not in handoff mode")
		}

		// Nothing cancels the daemon from outside: it must leave on its own once
		// the hold has passed.
		if err := waitReturn(t, runDone, 10*time.Second); !cleanExit(err) {
			t.Fatalf("Run returned %v after a handoff drained, want a clean exit", err)
		}
		if got := calls.count("/api/daemon/deregister"); got != 0 {
			t.Fatalf("a handoff daemon deregistered the runtimes its successor owns (%d calls)", got)
		}
	})
}
