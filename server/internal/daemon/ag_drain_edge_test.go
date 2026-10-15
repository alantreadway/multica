package daemon

// AG patch: daemon drain mode, behaviour at the seams: the real health
// listener, wake-up triggered runs, a claim already in flight, and the
// interaction with the claim barrier and the restart-pending reason.
// Complements ag_drain_test.go; same TestAGPatch* / "AG patch: ..." naming so
// ag-patches/test.sh runs and counts them.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func agDrainQuietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAGPatchDrainHealthListener(t *testing.T) {
	t.Run("AG patch: /drain and /resume are routed on the health server and reflected in /health", func(t *testing.T) {
		d := &Daemon{
			cfg:        Config{DaemonID: "daemon-test"},
			workspaces: map[string]*workspaceState{},
			logger:     agDrainQuietLogger(),
		}
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

		if code, body := do(http.MethodGet, "/health"); code != 200 || body["draining"] != false {
			t.Fatalf("initial /health: status=%d draining=%v, want 200/false", code, body["draining"])
		}
		for _, path := range []string{"/drain", "/resume"} {
			if code, _ := do(http.MethodGet, path); code != http.StatusMethodNotAllowed {
				t.Fatalf("GET %s = %d, want 405", path, code)
			}
		}
		if code, body := do(http.MethodGet, "/health"); code != 200 || body["draining"] != false {
			t.Fatalf("a rejected GET changed state: draining=%v", body["draining"])
		}

		code, body := do(http.MethodPost, "/drain")
		if code != 200 || body["draining"] != true || body["idle"] != true {
			t.Fatalf("POST /drain: status=%d body=%v, want draining and idle with no tasks", code, body)
		}
		if _, ok := body["drain_started_at"].(string); !ok {
			t.Fatalf("POST /drain body has no drain_started_at: %v", body)
		}
		if code, body := do(http.MethodGet, "/health"); code != 200 || body["draining"] != true || body["idle"] != true {
			t.Fatalf("/health after drain: status=%d draining=%v idle=%v", code, body["draining"], body["idle"])
		}

		code, body = do(http.MethodPost, "/resume")
		if code != 200 || body["draining"] != false || body["idle"] != false {
			t.Fatalf("POST /resume: status=%d body=%v, want not draining and not idle", code, body)
		}
		if _, present := body["drain_started_at"]; present {
			t.Fatal("drain_started_at must be omitted after resume")
		}
	})

	t.Run("AG patch: /drain reports the running task count and is not idle until it ends", func(t *testing.T) {
		d := &Daemon{logger: agDrainQuietLogger()}
		d.activeTasks.Store(1)
		h := d.agDrainHandler(true)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/drain", nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["idle"] != false || body["active_task_count"] != float64(1) {
			t.Fatalf("body = %v, want idle=false active_task_count=1", body)
		}

		d.activeTasks.Store(0)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/drain", nil))
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["idle"] != true {
			t.Fatalf("idle after the task ended = %v, want true", body["idle"])
		}
	})
}

// agDrainGatedServer serves one task (t1) on the first claim, but holds the
// claim response until gate is closed. It records claims and completions.
type agDrainGatedServer struct {
	*httptest.Server
	gate      chan struct{}
	claims    atomic.Int64
	completed atomic.Int64
	served    atomic.Bool
	arrived   chan struct{}
}

func newAGDrainGatedServer(t *testing.T) *agDrainGatedServer {
	t.Helper()
	s := &agDrainGatedServer{gate: make(chan struct{}), arrived: make(chan struct{}, 8)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/claim"):
			s.claims.Add(1)
			if s.served.CompareAndSwap(false, true) {
				s.arrived <- struct{}{}
				select {
				case <-s.gate:
				case <-r.Context().Done():
					return
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

func newAGDrainGatedDaemon(t *testing.T, s *agDrainGatedServer, pollInterval time.Duration) *Daemon {
	t.Helper()
	d := New(Config{
		ServerBaseURL:      s.URL,
		HeartbeatInterval:  time.Hour,
		PollInterval:       pollInterval,
		MaxConcurrentTasks: 2,
	}, agDrainQuietLogger())
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1"}
	d.cancelPollInterval = time.Hour
	return d
}

func agDrainWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAGPatchDrainWakeups(t *testing.T) {
	t.Run("AG patch: task wake-ups during drain do not claim; resume then claims", func(t *testing.T) {
		s := newAGDrainGatedServer(t)
		close(s.gate)
		d := newAGDrainGatedDaemon(t, s, time.Hour)
		var ran atomic.Int64
		d.runner = taskRunnerFunc(func(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error) {
			ran.Add(1)
			return TaskResult{Status: "completed"}, nil
		})
		d.agStartDrain()

		wakeups := make(chan taskWakeup, 8)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			d.pollLoop(ctx, wakeups)
		}()
		t.Cleanup(func() {
			cancel()
			<-done
		})

		for i := 0; i < 3; i++ {
			wakeups <- taskWakeup{runtimeID: "rt-1"}
		}
		time.Sleep(200 * time.Millisecond)
		if got := s.claims.Load(); got != 0 {
			t.Fatalf("claims after wake-ups while draining = %d, want 0", got)
		}

		d.agResume()
		agDrainWait(t, "the queued task to run after resume", func() bool { return ran.Load() == 1 })
		agDrainWait(t, "completion report", func() bool { return s.completed.Load() == 1 })
	})
}

func TestAGPatchDrainInFlightClaim(t *testing.T) {
	t.Run("AG patch: a claim already in flight when drain starts still runs to completion", func(t *testing.T) {
		s := newAGDrainGatedServer(t)
		d := newAGDrainGatedDaemon(t, s, 10*time.Millisecond)
		var ran atomic.Int64
		d.runner = taskRunnerFunc(func(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error) {
			ran.Add(1)
			return TaskResult{Status: "completed"}, nil
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			d.pollLoop(ctx, nil)
		}()
		t.Cleanup(func() {
			cancel()
			<-done
		})

		select {
		case <-s.arrived:
		case <-time.After(3 * time.Second):
			t.Fatal("claim request never reached the server")
		}
		d.agStartDrain()
		if raw := agDrainHealthJSON(t, d); raw["draining"] != true || raw["idle"] != false {
			t.Fatalf("with a claim in flight: draining=%v idle=%v, want true/false", raw["draining"], raw["idle"])
		}

		close(s.gate) // the claim now returns a task that was already committed to
		agDrainWait(t, "the in-flight task to run", func() bool { return ran.Load() == 1 })
		agDrainWait(t, "completion report", func() bool { return s.completed.Load() == 1 })
		agDrainWait(t, "idle after the task finished", func() bool {
			return agDrainHealthJSON(t, d)["idle"] == true
		})
		// Nothing further may be claimed once that task is done.
		before := s.claims.Load()
		time.Sleep(150 * time.Millisecond)
		if after := s.claims.Load(); after != before {
			t.Fatalf("claims grew from %d to %d while draining", before, after)
		}
	})
}

func TestAGPatchDrainAndBarrierIndependence(t *testing.T) {
	t.Run("AG patch: resume does not clear a claim barrier held by an update", func(t *testing.T) {
		d := &Daemon{logger: agDrainQuietLogger()}
		d.agStartDrain()
		d.claimMu.Lock()
		d.pauseClaims = true
		d.claimMu.Unlock()

		d.agResume()

		if d.agDraining() {
			t.Fatal("resume left drain set")
		}
		if d.tryEnterClaim() {
			t.Fatal("resume cleared pauseClaims and let a claim through the update barrier")
		}
		d.releaseClaimBarrier()
		if !d.tryEnterClaim() {
			t.Fatal("claims still refused after both drain and barrier were released")
		}
		d.exitClaim()
	})

	t.Run("AG patch: resume on a non-draining daemon leaves reload_pending_reason alone", func(t *testing.T) {
		d := &Daemon{logger: agDrainQuietLogger()}
		reason := "version changed (0.3.7 -> 0.3.8)"
		d.reloadPendingReason.Store(&reason)

		d.agResume()

		if got := d.reloadPending(); got != reason {
			t.Fatalf("reload_pending_reason = %q, want %q", got, reason)
		}
	})

	t.Run("AG patch: repeated deferred reloads keep a single drain note", func(t *testing.T) {
		d, restartCalls := newSelfReloadTestDaemon(t, "0.3.7")
		stubSelfVersion(t, "0.3.8", nil)
		d.agStartDrain()

		d.trySelfReload(context.Background())
		d.trySelfReload(context.Background())
		d.trySelfReload(context.Background())

		if restartCalls.Load() != 0 {
			t.Fatal("a draining daemon restarted")
		}
		if got := strings.Count(d.reloadPending(), "draining"); got != 1 {
			t.Fatalf("reload_pending_reason = %q has %d drain notes, want 1", d.reloadPending(), got)
		}
		if got := d.reloadPending(); strings.Count(got, "0.3.8") != 1 {
			t.Fatalf("reload_pending_reason = %q, want the version change exactly once", got)
		}
	})

	t.Run("AG patch: drain started while a restart barrier is held keeps both independent", func(t *testing.T) {
		d := &Daemon{logger: agDrainQuietLogger()}
		if !d.trySetClaimBarrier() {
			t.Fatal("barrier refused on an idle, non-draining daemon")
		}
		d.agStartDrain()
		d.releaseClaimBarrier()
		if !d.agDraining() {
			t.Fatal("releasing the barrier ended the drain")
		}
		if d.tryEnterClaim() {
			t.Fatal("claims allowed after barrier release while still draining")
		}
		d.agResume()
		if !d.tryEnterClaim() {
			t.Fatal("claims refused after barrier release and resume")
		}
		d.exitClaim()
	})
}
