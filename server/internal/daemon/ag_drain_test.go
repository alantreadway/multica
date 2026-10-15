package daemon

// AG patch: daemon drain mode tests. Every top-level test is TestAGPatch* with
// "AG patch: ..." subtests so ag-patches/test.sh can run and count them. They
// exercise daemon internals directly: an upstream rename should break the
// build here, which is the signal that the patch needs rebasing by hand.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// agDrainClaimServer counts claim requests and serves one task (t1) on the
// first claim after serveTask is set, empty batches otherwise.
type agDrainClaimServer struct {
	*httptest.Server
	claims    atomic.Int64
	completed atomic.Int64
	serveTask atomic.Bool
	served    atomic.Bool
}

func newAGDrainClaimServer(t *testing.T) *agDrainClaimServer {
	t.Helper()
	s := &agDrainClaimServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/claim"):
			s.claims.Add(1)
			if s.serveTask.Load() && s.served.CompareAndSwap(false, true) {
				w.Write([]byte(`{"tasks":[{"id":"t1","runtime_id":"rt-1","issue_id":"i1","agent":{"name":"a"}}]}`))
				return
			}
			w.Write([]byte(`{"tasks":[]}`))
		case strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/t1/complete"):
			s.completed.Add(1)
			w.Write([]byte(`{}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func newAGDrainPollDaemon(t *testing.T, s *agDrainClaimServer, pollInterval time.Duration) *Daemon {
	t.Helper()
	d := New(Config{
		ServerBaseURL:      s.URL,
		HeartbeatInterval:  time.Hour,
		PollInterval:       pollInterval,
		MaxConcurrentTasks: 2,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1"}
	d.cancelPollInterval = time.Hour
	return d
}

func startAGDrainPollLoop(t *testing.T, d *Daemon) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.pollLoop(ctx, nil)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("pollLoop did not stop")
		}
	})
	return cancel
}

func agDrainWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func agDrainHealthJSON(t *testing.T, d *Daemon) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return raw
}

func TestAGPatchDrainBlocksClaims(t *testing.T) {
	t.Run("AG patch: no claim is issued while draining", func(t *testing.T) {
		s := newAGDrainClaimServer(t)
		d := newAGDrainPollDaemon(t, s, 10*time.Millisecond)
		d.agStartDrain()
		startAGDrainPollLoop(t, d)

		time.Sleep(300 * time.Millisecond)
		if got := s.claims.Load(); got != 0 {
			t.Fatalf("claim calls while draining = %d, want 0", got)
		}
	})

	t.Run("AG patch: tryEnterClaim refuses and leaves claimsInFlight untouched", func(t *testing.T) {
		d := &Daemon{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		d.agStartDrain()
		if d.tryEnterClaim() {
			t.Fatal("tryEnterClaim allowed a claim while draining")
		}
		d.claimMu.Lock()
		inFlight := d.claimsInFlight
		d.claimMu.Unlock()
		if inFlight != 0 {
			t.Fatalf("claimsInFlight = %d, want 0", inFlight)
		}
	})
}

func TestAGPatchDrainLetsRunningTasksFinish(t *testing.T) {
	t.Run("AG patch: a running task is not cancelled and completes after drain starts", func(t *testing.T) {
		s := newAGDrainClaimServer(t)
		s.serveTask.Store(true)
		d := newAGDrainPollDaemon(t, s, 10*time.Millisecond)

		started := make(chan struct{})
		release := make(chan struct{})
		var cancelled atomic.Bool
		d.runner = taskRunnerFunc(func(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error) {
			close(started)
			select {
			case <-release:
				return TaskResult{Status: "completed"}, nil
			case <-ctx.Done():
				cancelled.Store(true)
				return TaskResult{Status: "cancelled"}, ctx.Err()
			}
		})
		startAGDrainPollLoop(t, d)

		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("task did not start")
		}
		d.agStartDrain()
		time.Sleep(150 * time.Millisecond)
		if cancelled.Load() {
			t.Fatal("drain cancelled the running task")
		}
		if got := d.activeTasks.Load(); got != 1 {
			t.Fatalf("activeTasks = %d, want 1 while the task runs", got)
		}

		close(release)
		agDrainWaitFor(t, "task completion report", func() bool { return s.completed.Load() == 1 })
		agDrainWaitFor(t, "activeTasks to reach 0", func() bool { return d.activeTasks.Load() == 0 })
	})
}

func TestAGPatchDrainIdleReporting(t *testing.T) {
	t.Run("AG patch: idle flips when active tasks reach zero and no claim is in flight", func(t *testing.T) {
		d := &Daemon{
			cfg:        Config{DaemonID: "daemon-test"},
			workspaces: map[string]*workspaceState{},
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		raw := agDrainHealthJSON(t, d)
		if raw["draining"] != false || raw["idle"] != false {
			t.Fatalf("not draining: draining=%v idle=%v, want false/false", raw["draining"], raw["idle"])
		}
		if _, present := raw["drain_started_at"]; present {
			t.Fatal("drain_started_at must be omitted when not draining")
		}

		d.activeTasks.Store(2)
		d.resourceWaitTasks.Store(1)
		d.agStartDrain()
		raw = agDrainHealthJSON(t, d)
		if raw["draining"] != true || raw["idle"] != false {
			t.Fatalf("draining with 2 active: draining=%v idle=%v, want true/false", raw["draining"], raw["idle"])
		}
		startedAt, _ := raw["drain_started_at"].(string)
		if _, err := time.Parse(time.RFC3339, startedAt); err != nil {
			t.Fatalf("drain_started_at %q is not RFC3339: %v", startedAt, err)
		}

		// A task parked on a local-directory lock still counts as active.
		d.activeTasks.Store(1)
		if raw = agDrainHealthJSON(t, d); raw["idle"] != false {
			t.Fatalf("idle with a resource-waiting task = %v, want false", raw["idle"])
		}

		d.claimMu.Lock()
		d.claimsInFlight = 1
		d.claimMu.Unlock()
		d.activeTasks.Store(0)
		if raw = agDrainHealthJSON(t, d); raw["idle"] != false {
			t.Fatalf("idle with a claim in flight = %v, want false", raw["idle"])
		}

		d.exitClaim()
		if raw = agDrainHealthJSON(t, d); raw["idle"] != true {
			t.Fatalf("idle with nothing left = %v, want true", raw["idle"])
		}
	})

	t.Run("AG patch: POST /drain and /resume toggle state; other methods get 405", func(t *testing.T) {
		d := &Daemon{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		drain, resume := d.agDrainHandler(true), d.agDrainHandler(false)

		for _, h := range []http.HandlerFunc{drain, resume} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("GET status = %d, want 405", rec.Code)
			}
		}
		if d.agDraining() {
			t.Fatal("a GET must not start a drain")
		}

		rec := httptest.NewRecorder()
		drain.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/drain", nil))
		if rec.Code != http.StatusOK || !d.agDraining() {
			t.Fatalf("POST /drain: status=%d draining=%v", rec.Code, d.agDraining())
		}
		first := d.agDrain.startedAt
		drain.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/drain", nil))
		if !d.agDrain.startedAt.Equal(first) {
			t.Fatal("a repeated drain must keep the original start time")
		}

		rec = httptest.NewRecorder()
		resume.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resume", nil))
		if rec.Code != http.StatusOK || d.agDraining() {
			t.Fatalf("POST /resume: status=%d draining=%v", rec.Code, d.agDraining())
		}
	})
}

func TestAGPatchDrainResume(t *testing.T) {
	t.Run("AG patch: resume claims at once, not after PollInterval", func(t *testing.T) {
		s := newAGDrainClaimServer(t)
		d := newAGDrainPollDaemon(t, s, time.Hour)
		d.agStartDrain()
		startAGDrainPollLoop(t, d)

		time.Sleep(200 * time.Millisecond) // poller is now asleep for PollInterval
		if got := s.claims.Load(); got != 0 {
			t.Fatalf("claims while draining = %d, want 0", got)
		}

		d.agResume()
		agDrainWaitFor(t, "a claim right after resume", func() bool { return s.claims.Load() >= 1 })
	})

	t.Run("AG patch: a task queued during drain is claimed after resume", func(t *testing.T) {
		s := newAGDrainClaimServer(t)
		s.serveTask.Store(true)
		d := newAGDrainPollDaemon(t, s, time.Hour)
		var ran atomic.Int64
		d.runner = taskRunnerFunc(func(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error) {
			ran.Add(1)
			return TaskResult{Status: "completed"}, nil
		})
		d.agStartDrain()
		startAGDrainPollLoop(t, d)
		time.Sleep(150 * time.Millisecond)
		if ran.Load() != 0 || s.claims.Load() != 0 {
			t.Fatalf("work started while draining: ran=%d claims=%d", ran.Load(), s.claims.Load())
		}

		d.agResume()
		agDrainWaitFor(t, "queued task to run", func() bool { return ran.Load() == 1 })
		agDrainWaitFor(t, "completion report", func() bool { return s.completed.Load() == 1 })
	})
}

func TestAGPatchDrainDefersRestarts(t *testing.T) {
	t.Run("AG patch: self-reload does not restart while draining and says why", func(t *testing.T) {
		d, restartCalls := newSelfReloadTestDaemon(t, "0.3.7")
		stubSelfVersion(t, "0.3.8", nil)
		d.agStartDrain()

		d.trySelfReload(context.Background())

		if restartCalls.Load() != 0 || d.RestartBinary() != "" {
			t.Fatal("self-reload restarted a draining daemon")
		}
		if claimsPaused(t, d) {
			t.Fatal("deferral must not leave pauseClaims held")
		}
		if d.updating.Load() {
			t.Fatal("deferral must release the updating flag")
		}
		reason := d.reloadPending()
		if !strings.Contains(reason, "0.3.8") || !strings.Contains(reason, "draining") {
			t.Fatalf("reload_pending_reason = %q, want the version change and the drain note", reason)
		}

		// Resume drops the drain note and the next tick restarts as normal.
		d.agResume()
		if got := d.reloadPending(); strings.Contains(got, "draining") || !strings.Contains(got, "0.3.8") {
			t.Fatalf("reason after resume = %q, want the version change only", got)
		}
		d.trySelfReload(context.Background())
		if restartCalls.Load() != 1 {
			t.Fatalf("restart after resume fired %d times, want 1", restartCalls.Load())
		}
	})

	t.Run("AG patch: auto-update does not upgrade or restart while draining", func(t *testing.T) {
		d, restartCalls := newAutoUpdateTestDaemon(t, "v0.1.13")
		d.runUpdateFn = func(string) (string, error) {
			t.Fatal("auto-update ran an upgrade while draining")
			return "", nil
		}
		withStubRelease(t, &cli.GitHubRelease{TagName: "v0.1.14"}, nil)
		d.agStartDrain()

		d.tryAutoUpdate(context.Background())

		if restartCalls.Load() != 0 {
			t.Fatal("auto-update restarted a draining daemon")
		}
		if claimsPaused(t, d) || d.updating.Load() {
			t.Fatal("deferral left the claim barrier or updating flag held")
		}
		if got := d.reloadPending(); !strings.Contains(got, "draining") {
			t.Fatalf("reload_pending_reason = %q, want a drain note", got)
		}
		d.agResume()
		if got := d.reloadPending(); got != "" {
			t.Fatalf("reload_pending_reason after resume = %q, want empty", got)
		}
	})

	t.Run("AG patch: a server-triggered update is refused while draining", func(t *testing.T) {
		var payload map[string]any
		d, reportCalls := updateReportDaemon(t, func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&payload)
			w.WriteHeader(http.StatusOK)
		})
		d.runUpdateFn = func(string) (string, error) {
			t.Fatal("server update ran while draining")
			return "", nil
		}
		d.agStartDrain()

		d.handleUpdate(context.Background(), "runtime-1", &PendingUpdate{ID: "update-1", TargetVersion: "v0.4.14"})

		if got := atomic.LoadInt32(reportCalls); got != 1 {
			t.Fatalf("update reports = %d, want 1", got)
		}
		if payload["status"] != "failed" || !strings.Contains(payload["error"].(string), "draining") {
			t.Fatalf("report = %v, want a failed status naming the drain", payload)
		}
		if claimsPaused(t, d) || d.updating.Load() {
			t.Fatal("refused update left the claim barrier or updating flag held")
		}
	})

	t.Run("AG patch: a barrier acquired in the server-update race is released, drain kept", func(t *testing.T) {
		d := &Daemon{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		// Drain starts after the entry check but before the final idle check:
		// simulate by holding a claim in flight so the loop runs, then draining.
		d.claimsInFlight = 1
		result := make(chan serverUpdateAcquireResult, 1)
		go func() { result <- d.tryBeginServerUpdate(context.Background()) }()
		agDrainWaitFor(t, "barrier to be taken", func() bool { return claimsPaused(t, d) })
		d.agStartDrain()
		d.exitClaim()

		select {
		case got := <-result:
			if got == serverUpdateAcquired {
				t.Fatal("server update acquired the restart barrier on a draining daemon")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("tryBeginServerUpdate did not return")
		}
		if !d.agDraining() {
			t.Fatal("update path cleared drain")
		}
		if claimsPaused(t, d) {
			t.Fatal("refused update left pauseClaims held")
		}
	})
}

func TestAGPatchDrainSurvivesBarrierRelease(t *testing.T) {
	t.Run("AG patch: releaseClaimBarrier does not clear drain", func(t *testing.T) {
		d := &Daemon{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		d.agStartDrain()
		d.claimMu.Lock()
		d.pauseClaims = true
		d.claimMu.Unlock()

		d.releaseClaimBarrier()

		if !d.agDraining() {
			t.Fatal("releaseClaimBarrier cleared drain")
		}
		if d.tryEnterClaim() {
			t.Fatal("claims allowed after the barrier was released during a drain")
		}
	})

	t.Run("AG patch: drain is in-memory only; a fresh daemon claims", func(t *testing.T) {
		old := &Daemon{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		old.agStartDrain()
		fresh := New(Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if fresh.agDraining() {
			t.Fatal("a new daemon started in drain mode")
		}
		if !fresh.tryEnterClaim() {
			t.Fatal("a new daemon refused to claim")
		}
		fresh.exitClaim()
	})
}
