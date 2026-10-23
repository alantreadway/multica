package daemon

// AG patch: Claude pacing, round-1 rules: a claimed task counts until its dispatch
// goroutine exits, and a fresh process never starts above the predecessor's pace.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestAGPatchPaceClaimLifetime(t *testing.T) {
	claimOne := func(t *testing.T) (*agPaceFixture, string) {
		t.Helper()
		srv := newAGPaceClaimServer(t)
		f := newAGPaceFixtureAt(t, 5, srv.URL)
		res, err := f.d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude"}, 1)
		if err != nil || len(res.Tasks) != 1 {
			t.Fatalf("claim: %v, %d tasks", err, len(res.Tasks))
		}
		return f, res.Tasks[0].ID
	}

	t.Run("AG patch: a task that waits longer than ten minutes before its run still counts", func(t *testing.T) {
		f, id := claimOne(t)
		f.d.agTrackTask(id)
		f.clock.set(f.clock.now().Add(45 * time.Minute)) // e.g. queued behind the local-directory lock
		if _, active := f.cap(t); active != 1 {
			t.Fatalf("active after 45 min waiting = %d, want 1", active)
		}
		_, done := f.d.agQuotaObserver(Task{ID: id}, "claude", nil) // the run starts
		if a := f.d.agPaceHealth().ActiveClaude; a != 1 {
			t.Fatalf("active once running = %d, want 1 (the run replaces its claim)", a)
		}
		done()
		f.d.agUntrackTask(id)
		if a := f.d.agPaceHealth().ActiveClaude; a != 0 {
			t.Fatalf("active after the goroutine exited = %d, want 0", a)
		}
	})

	t.Run("AG patch: a task that returns before its run starts stops counting at once", func(t *testing.T) {
		f, id := claimOne(t)
		f.d.agTrackTask(id)
		delete(f.d.runtimeIndex, "rt-claude") // the runtime went offline between claim and dispatch
		if f.d.agPaceHealth().ActiveClaude != 1 {
			t.Fatal("claimed task not counted")
		}
		func() { // the pollLoop's dispatch goroutine body
			defer f.d.agUntrackTask(id)
			f.d.handleTask(context.Background(), Task{ID: id, RuntimeID: "rt-claude", WorkspaceID: "ws"}, 0)
		}()
		if a := f.d.agPaceHealth().ActiveClaude; a != 0 {
			t.Fatalf("active after handleTask returned early = %d, want 0", a)
		}
	})

	t.Run("AG patch: a failed claim counts nothing", func(t *testing.T) {
		srv := newAGPaceSpecServer(t)
		srv.tasks = func(agPaceClaimCall) int { return 1 }
		srv.status = func(int, agPaceClaimCall) int { return http.StatusInternalServerError }
		f := newAGPaceFixtureAt(t, 5, srv.URL)
		if _, err := f.d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude"}, 1); err == nil {
			t.Fatal("claim error expected")
		}
		if a := f.d.agPaceHealth().ActiveClaude; a != 0 {
			t.Fatalf("active after a failed claim = %d, want 0", a)
		}
	})
}

func TestAGPatchPaceStartingCap(t *testing.T) {
	setup := func(t *testing.T, u5 float64) (*agPaceFixture, time.Time) {
		t.Helper()
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		f.event(t, u5, 0.10, now.Add(2*time.Hour), now.Add(100*time.Hour)) // first sight: no burn yet
		return f, now
	}

	t.Run("AG patch: a cold start in the paced zone with the burn unknown starts at the floor", func(t *testing.T) {
		f, _ := setup(t, 0.90)
		if c, _ := f.cap(t); c != 1 {
			t.Fatalf("cap = %d, want 1", c)
		}
		if h := f.d.agPaceHealth(); h.EffectiveCap != 1 {
			t.Fatalf("health effective_cap = %d, want 1", h.EffectiveCap)
		}
	})

	t.Run("AG patch: a cold start outside the paced zone starts at Max", func(t *testing.T) {
		f, _ := setup(t, 0.50)
		if c, _ := f.cap(t); c != 5 {
			t.Fatalf("cap = %d, want 5", c)
		}
	})

	t.Run("AG patch: from the floor the cap follows the limiter once a burn forms", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		t0 := f.clock.now()
		reset5, reset7 := t0.Add(2*time.Hour), t0.Add(100*time.Hour)
		done := f.d.agPaceRunStart("r1", f.acct, t0)
		defer done()
		f.event(t, 0.860, 0.10, reset5, reset7)
		if c, _ := f.cap(t); c != 1 {
			t.Fatalf("cap = %d, want 1", c)
		}
		f.clock.set(t0.Add(6 * time.Minute))
		f.event(t, 0.861, 0.10, reset5, reset7) // burn 0.01/h per run: target 4
		for _, step := range []struct {
			at   time.Duration
			want int
		}{{6 * time.Minute, 2}, {11 * time.Minute, 2}, {17 * time.Minute, 3}, {27 * time.Minute, 4}} {
			f.clock.set(t0.Add(step.at))
			if c, _ := f.cap(t); c != step.want {
				t.Fatalf("at +%v cap = %d, want %d", step.at, c, step.want)
			}
		}
	})
}

// agPaceHealthServer serves a predecessor's /health with a settable effective_cap.
type agPaceHealthServer struct {
	mu  sync.Mutex
	cap *int
}

func (h *agPaceHealthServer) set(c *int) {
	h.mu.Lock()
	h.cap = c
	h.mu.Unlock()
}

func (h *agPaceHealthServer) start(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		quota := map[string]any{"active_claude": 0}
		if h.cap != nil {
			quota["effective_cap"] = *h.cap
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pid": os.Getpid(), "active_task_ids": []string{}, "ag_quota": quota})
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

func TestAGPatchPaceSuccessorSeed(t *testing.T) {
	ptr := func(n int) *int { return &n }
	setup := func(t *testing.T, max int) (*agPaceFixture, *agPaceHealthServer, func()) {
		t.Helper()
		f := newAGPaceFixture(t, max)
		now := f.clock.now()
		f.event(t, 0.90, 0.10, now.Add(2*time.Hour), now.Add(100*time.Hour)) // paced zone, burn unknown
		pred := &agPaceHealthServer{}
		port := pred.start(t)
		f.d.AGEnableOverlap(AGPredecessor{PID: os.Getpid(), Port: port, MaxConcurrentTasks: max})
		return f, pred, func() {
			failures := 0
			if f.d.agOverlapStep(context.Background(), &failures) {
				t.Fatal("overlap ended while the predecessor is alive")
			}
		}
	}

	t.Run("AG patch: the successor takes the predecessor's cap on the first poll that reports it", func(t *testing.T) {
		f, pred, step := setup(t, 5)
		pred.set(nil) // an older build reporting no cap
		step()
		if c, _ := f.cap(t); c != 1 {
			t.Fatalf("before a cap is reported: cap = %d, want the cold-start 1", c)
		}
		pred.set(ptr(3))
		step()
		if c, _ := f.cap(t); c != 3 {
			t.Fatalf("after the predecessor reported 3: cap = %d, want 3", c)
		}
		pred.set(ptr(5))
		step()
		if c, _ := f.cap(t); c != 3 {
			t.Fatalf("a later report changed the cap to %d, want it left to the limiter at 3", c)
		}
	})

	t.Run("AG patch: the seed is clamped to 1..Max", func(t *testing.T) {
		for _, tc := range []struct{ reported, want int }{{9, 4}, {0, 1}, {-2, 1}} {
			f, pred, step := setup(t, 4)
			pred.set(ptr(tc.reported))
			step()
			if c, _ := f.cap(t); c != tc.want {
				t.Fatalf("reported %d: cap = %d, want %d", tc.reported, c, tc.want)
			}
		}
	})

	t.Run("AG patch: a seed above the target steps down at once", func(t *testing.T) {
		f, pred, step := setup(t, 5)
		pred.set(ptr(4))
		step()
		now := f.clock.now()
		done := f.d.agPaceRunStart("r1", f.acct, now)
		defer done()
		f.clock.set(now.Add(6 * time.Minute))
		f.event(t, 0.91, 0.10, now.Add(2*time.Hour), now.Add(100*time.Hour)) // burn 0.01/0.1h = 0.1/h: target 1
		if c, _ := f.cap(t); c != 3 {
			t.Fatalf("cap = %d, want one step down from the seed (3)", c)
		}
	})
}
