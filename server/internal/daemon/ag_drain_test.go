package daemon

// AG patch: daemon drain mode tests; TestAGPatch* with "AG patch:" subtests,
// as counted by ag-patches/test.sh.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// agDrainClaimServer counts claims and serves one task (t1) on the first claim.
// A non-nil gate holds that claim's response until it is closed; arrived
// signals that the claim reached the server.
type agDrainClaimServer struct {
	*httptest.Server
	claims    atomic.Int64
	completed atomic.Int64
	served    atomic.Bool
	gate      chan struct{}
	arrived   chan struct{}
}

func newAGDrainClaimServer(t *testing.T, gated bool) *agDrainClaimServer {
	t.Helper()
	s := &agDrainClaimServer{arrived: make(chan struct{}, 8)}
	if gated {
		s.gate = make(chan struct{})
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/claim"):
			s.claims.Add(1)
			if s.served.CompareAndSwap(false, true) {
				s.arrived <- struct{}{}
				if s.gate != nil {
					select {
					case <-s.gate:
					case <-r.Context().Done():
						return
					}
				}
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
	}, discardLogger())
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1"}
	d.cancelPollInterval = time.Hour
	return d
}

func startAGDrainPollLoop(t *testing.T, d *Daemon, wakeups <-chan taskWakeup) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.pollLoop(ctx, wakeups)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("pollLoop did not stop")
		}
	})
}

// agDrainCompletingRunner makes tasks complete at once and counts them.
func agDrainCompletingRunner(d *Daemon) *atomic.Int64 {
	var ran atomic.Int64
	d.runner = taskRunnerFunc(func(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error) {
		ran.Add(1)
		return TaskResult{Status: "completed"}, nil
	})
	return &ran
}

func newAGDrainHealthDaemon() *Daemon {
	return &Daemon{
		cfg:        Config{DaemonID: "daemon-test"},
		workspaces: map[string]*workspaceState{},
		logger:     discardLogger(),
	}
}

// agDrainBlockingUpdate makes d's runUpdateFn block until finish is closed and
// runs fn in a goroutine. upgrading closes once the update is running; done
// closes when fn returns.
func agDrainBlockingUpdate(d *Daemon, fn func()) (upgrading, finish, done chan struct{}) {
	upgrading, finish, done = make(chan struct{}), make(chan struct{}), make(chan struct{})
	d.runUpdateFn = func(string) (string, error) {
		close(upgrading)
		<-finish
		return "ok", nil
	}
	go func() {
		defer close(done)
		fn()
	}()
	return upgrading, finish, done
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
	t.Run("AG patch: tryEnterClaim refuses and leaves claimsInFlight untouched", func(t *testing.T) {
		d := &Daemon{logger: discardLogger()}
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

	t.Run("AG patch: wake-ups during drain claim nothing; resume claims at once, not after PollInterval", func(t *testing.T) {
		s := newAGDrainClaimServer(t, false)
		d := newAGDrainPollDaemon(t, s, time.Hour)
		ran := agDrainCompletingRunner(d)
		d.agStartDrain()
		wakeups := make(chan taskWakeup, 8)
		startAGDrainPollLoop(t, d, wakeups)

		for i := 0; i < 3; i++ {
			wakeups <- taskWakeup{runtimeID: "rt-1"}
		}
		waitFor(t, func() bool { return len(wakeups) == 0 }, "poller to consume the wake-ups")
		time.Sleep(100 * time.Millisecond)
		if s.claims.Load() != 0 || ran.Load() != 0 {
			t.Fatalf("work started while draining: claims=%d ran=%d", s.claims.Load(), ran.Load())
		}

		d.agResume()
		waitFor(t, func() bool { return ran.Load() == 1 && s.completed.Load() == 1 }, "queued task to run and complete after resume")
	})
}

func TestAGPatchDrainLetsRunningTasksFinish(t *testing.T) {
	t.Run("AG patch: a running task is not cancelled and completes after drain starts", func(t *testing.T) {
		s := newAGDrainClaimServer(t, false)
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
		startAGDrainPollLoop(t, d, nil)

		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("task did not start")
		}
		d.agStartDrain()
		time.Sleep(100 * time.Millisecond)
		if cancelled.Load() {
			t.Fatal("drain cancelled the running task")
		}
		if got := d.activeTasks.Load(); got != 1 {
			t.Fatalf("activeTasks = %d, want 1 while the task runs", got)
		}

		close(release)
		waitFor(t, func() bool { return s.completed.Load() == 1 && d.activeTasks.Load() == 0 }, "task completion report")
	})

	t.Run("AG patch: a claim already in flight when drain starts still runs; nothing is claimed after", func(t *testing.T) {
		s := newAGDrainClaimServer(t, true)
		d := newAGDrainPollDaemon(t, s, 10*time.Millisecond)
		ran := agDrainCompletingRunner(d)
		startAGDrainPollLoop(t, d, nil)

		select {
		case <-s.arrived:
		case <-time.After(3 * time.Second):
			t.Fatal("claim request never reached the server")
		}
		d.agStartDrain()
		if raw := agDrainHealthJSON(t, d); raw["draining"] != true || raw["idle"] != false {
			t.Fatalf("with a claim in flight: draining=%v idle=%v, want true/false", raw["draining"], raw["idle"])
		}

		close(s.gate) // the claim returns the task it had already committed to
		waitFor(t, func() bool { return ran.Load() == 1 && s.completed.Load() == 1 }, "in-flight task to run and complete")
		waitFor(t, func() bool { return agDrainHealthJSON(t, d)["idle"] == true }, "idle after the task finished")
		before := s.claims.Load()
		time.Sleep(100 * time.Millisecond)
		if after := s.claims.Load(); after != before {
			t.Fatalf("claims grew from %d to %d while draining", before, after)
		}
	})
}

func TestAGPatchDrainReportsIdle(t *testing.T) {
	t.Run("AG patch: /health idle tracks active tasks and in-flight claims", func(t *testing.T) {
		d := newAGDrainHealthDaemon()
		raw := agDrainHealthJSON(t, d)
		if raw["draining"] != false || raw["idle"] != false {
			t.Fatalf("not draining: draining=%v idle=%v, want false/false", raw["draining"], raw["idle"])
		}
		if _, present := raw["drain_started_at"]; present {
			t.Fatal("drain_started_at must be omitted when not draining")
		}

		d.activeTasks.Store(2)
		d.agStartDrain()
		raw = agDrainHealthJSON(t, d)
		if raw["draining"] != true || raw["idle"] != false {
			t.Fatalf("draining with 2 active: draining=%v idle=%v, want true/false", raw["draining"], raw["idle"])
		}
		startedAt, _ := raw["drain_started_at"].(string)
		if _, err := time.Parse(time.RFC3339, startedAt); err != nil {
			t.Fatalf("drain_started_at %q is not RFC3339: %v", startedAt, err)
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

	t.Run("AG patch: /drain and /resume are routed on the health server and answer with the state", func(t *testing.T) {
		d := newAGDrainHealthDaemon()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			d.serveHealth(ctx, ln, time.Now())
		}()
		t.Cleanup(func() {
			cancel()
			<-done
		})
		base := "http://" + ln.Addr().String()

		do := func(method, path string) (int, map[string]any) {
			t.Helper()
			req, err := http.NewRequest(method, base+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			defer resp.Body.Close()
			var body map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&body)
			return resp.StatusCode, body
		}

		for _, path := range []string{"/drain", "/resume"} {
			if code, _ := do(http.MethodGet, path); code != http.StatusMethodNotAllowed {
				t.Fatalf("GET %s = %d, want 405", path, code)
			}
		}
		if _, body := do(http.MethodGet, "/health"); body["draining"] != false {
			t.Fatalf("a rejected GET changed state: draining=%v", body["draining"])
		}

		d.activeTasks.Store(1)
		code, body := do(http.MethodPost, "/drain")
		if code != 200 || body["draining"] != true || body["idle"] != false || body["active_task_count"] != float64(1) {
			t.Fatalf("POST /drain with a running task: status=%d body=%v, want draining, not idle, 1 active", code, body)
		}
		if _, ok := body["drain_started_at"].(string); !ok {
			t.Fatalf("POST /drain body has no drain_started_at: %v", body)
		}

		d.activeTasks.Store(0)
		if code, body := do(http.MethodGet, "/health"); code != 200 || body["draining"] != true || body["idle"] != true {
			t.Fatalf("/health after the task ended: status=%d draining=%v idle=%v", code, body["draining"], body["idle"])
		}

		code, body = do(http.MethodPost, "/resume")
		if code != 200 || body["draining"] != false || body["idle"] != false {
			t.Fatalf("POST /resume: status=%d body=%v, want not draining and not idle", code, body)
		}
		if _, present := body["drain_started_at"]; present {
			t.Fatal("drain_started_at must be omitted after resume")
		}
	})

	t.Run("AG patch: a repeated drain keeps the original start time", func(t *testing.T) {
		d := &Daemon{logger: discardLogger()}
		d.agStartDrain()
		first := d.agDrain.startedAt
		time.Sleep(2 * time.Millisecond)
		d.agStartDrain()
		if !d.agDrain.startedAt.Equal(first) {
			t.Fatal("a repeated drain reset the start time")
		}
	})
}

func TestAGPatchDrainSurvivesBarrierRelease(t *testing.T) {
	t.Run("AG patch: releaseClaimBarrier does not clear drain", func(t *testing.T) {
		d := &Daemon{logger: discardLogger()}
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

	t.Run("AG patch: resume does not clear a claim barrier held by an update", func(t *testing.T) {
		d := &Daemon{logger: discardLogger()}
		d.agStartDrain()
		d.claimMu.Lock()
		d.pauseClaims = true
		d.claimMu.Unlock()

		d.agResume()

		if d.tryEnterClaim() {
			t.Fatal("resume cleared pauseClaims and let a claim through the update barrier")
		}
		d.releaseClaimBarrier()
		if !d.tryEnterClaim() {
			t.Fatal("claims still refused after both drain and barrier were released")
		}
		d.exitClaim()
	})
}

func TestAGPatchDrainDefersRestarts(t *testing.T) {
	t.Run("AG patch: self-reload does not restart while draining, keeps one note, and restarts after resume", func(t *testing.T) {
		d, restartCalls := newSelfReloadTestDaemon(t, "0.3.7")
		stubSelfVersion(t, "0.3.8", nil)
		d.agStartDrain()

		for i := 0; i < 3; i++ {
			d.trySelfReload(context.Background())
		}

		if restartCalls.Load() != 0 || d.RestartBinary() != "" {
			t.Fatal("self-reload restarted a draining daemon")
		}
		if claimsPaused(t, d) || d.updating.Load() {
			t.Fatal("deferral left the claim barrier or updating flag held")
		}
		reason := d.reloadPending()
		if strings.Count(reason, "0.3.8") != 1 || strings.Count(reason, "draining") != 1 {
			t.Fatalf("reload_pending_reason = %q, want the version change and one drain note", reason)
		}

		d.agResume()
		if got := d.reloadPending(); strings.Contains(got, "draining") || !strings.Contains(got, "0.3.8") {
			t.Fatalf("reason after resume = %q, want the version change only", got)
		}
		d.trySelfReload(context.Background())
		if restartCalls.Load() != 1 {
			t.Fatalf("restart after resume fired %d times, want 1", restartCalls.Load())
		}
	})

	t.Run("AG patch: auto-update does not upgrade while draining", func(t *testing.T) {
		d, restartCalls := newAutoUpdateTestDaemon(t, "v0.1.13")
		d.runUpdateFn = func(string) (string, error) {
			t.Fatal("auto-update ran an upgrade while draining")
			return "", nil
		}
		withStubRelease(t, &cli.GitHubRelease{TagName: "v0.1.14"}, nil)
		d.agStartDrain()

		d.tryAutoUpdate(context.Background())

		if restartCalls.Load() != 0 || claimsPaused(t, d) || d.updating.Load() {
			t.Fatalf("auto-update acted on a draining daemon: restarts=%d", restartCalls.Load())
		}
	})

	t.Run("AG patch: a drain that starts during an auto-update upgrade stops the restart", func(t *testing.T) {
		d, restartCalls := newAutoUpdateTestDaemon(t, "v0.1.13")
		withStubRelease(t, &cli.GitHubRelease{TagName: "v0.1.14"}, nil)
		upgrading, finish, done := agDrainBlockingUpdate(d, func() { d.tryAutoUpdate(context.Background()) })
		<-upgrading

		d.agStartDrain()
		if raw := agDrainHealthJSON(t, d); raw["draining"] != true || raw["idle"] != false {
			t.Fatalf("draining mid-update: draining=%v idle=%v, want true/false", raw["draining"], raw["idle"])
		}
		close(finish)
		<-done

		if restartCalls.Load() != 0 || d.RestartBinary() != "" {
			t.Fatal("auto-update restarted a daemon that began draining mid-upgrade")
		}
		if claimsPaused(t, d) || d.updating.Load() {
			t.Fatal("deferred restart left the claim barrier or updating flag held")
		}
		if !strings.Contains(d.reloadPending(), "draining") {
			t.Fatalf("reload_pending_reason = %q, want a drain note", d.reloadPending())
		}
		if !d.agDraining() {
			t.Fatal("the update path cleared drain")
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

	t.Run("AG patch: a drain that starts during a server update stops the restart and says so", func(t *testing.T) {
		var lastReport atomic.Value
		d, _ := updateReportDaemon(t, func(w http.ResponseWriter, r *http.Request) {
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			lastReport.Store(p)
			w.WriteHeader(http.StatusOK)
		})
		var restartCalls atomic.Int32
		d.cancelFunc = func() { restartCalls.Add(1) }
		upgrading, finish, done := agDrainBlockingUpdate(d, func() {
			d.handleUpdate(context.Background(), "runtime-1", &PendingUpdate{ID: "update-1", TargetVersion: "v0.4.14"})
		})
		<-upgrading

		d.agStartDrain()
		if raw := agDrainHealthJSON(t, d); raw["draining"] != true || raw["idle"] != false {
			t.Fatalf("draining mid-update: draining=%v idle=%v, want true/false", raw["draining"], raw["idle"])
		}
		close(finish)
		<-done

		if restartCalls.Load() != 0 || d.RestartBinary() != "" {
			t.Fatal("server update restarted a daemon that began draining mid-update")
		}
		report, _ := lastReport.Load().(map[string]any)
		if report["status"] != "completed" || !strings.Contains(report["output"].(string), "draining") {
			t.Fatalf("last report = %v, want completed with the drain note", report)
		}
		if claimsPaused(t, d) || d.updating.Load() {
			t.Fatal("deferred restart left the claim barrier or updating flag held")
		}
	})

	t.Run("AG patch: runtime demotion still runs while draining and leaves no drain note", func(t *testing.T) {
		fx := newBatchFixture(t)
		d := fx.daemon
		d.cfg.Agents = map[string]AgentEntry{
			"claude": {Path: "/fake/claude"},
			"codex":  {Path: "/fake/codex"},
		}
		fx.setWorkspaces(WorkspaceInfo{ID: "ws-1", Name: "one"})
		stubAgentProbe(t, d.cfg.Agents)
		if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
			t.Fatalf("syncWorkspacesFromAPI: %v", err)
		}
		d.agStartDrain()

		d.demoteUnusableRuntimes(context.Background(), map[string]runtimeVerdict{"codex": {reason: "below minimum supported version: 0.0.1"}})

		if got := registeredProviders(t, d, "ws-1"); len(got) != 1 || got[0] != "claude" {
			t.Fatalf("providers after demotion = %v, want [claude]", got)
		}
		if got := d.reloadPending(); got != "" {
			t.Fatalf("reload_pending_reason = %q, want empty: demotion is not a restart", got)
		}
		if !d.agDraining() {
			t.Fatal("demotion cleared drain")
		}
	})
}
