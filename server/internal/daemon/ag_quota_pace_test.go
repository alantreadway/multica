package daemon

// AG patch: Claude pacing tests; TestAGPatch* with "AG patch:" subtests, as
// counted by ag-patches/test.sh.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
)

func agPaceApprox(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// TestAGPatchPaceWorkedExamples runs the Spec's table A-H through the maths.
func TestAGPatchPaceWorkedExamples(t *testing.T) {
	const max = 5
	cases := []struct {
		name           string
		u5, t5, b5     float64
		u7, t7, b7     float64
		n5, n7         float64
		wantTarget     int
		wantCurveAbove float64 // c, the weekly curve
	}{
		{"A", 0.79, 3, 0.02, 0.40, 100, 0.002, 5, 5, 5, 0.405},
		{"B", 0.85, 2, 0.02, 0.40, 100, 0.002, 2.5, 5, 2, 0.405},
		{"C", 0.90, 2.5, 0.03, 0.40, 100, 0.002, 0.67, 5, 1, 0.405},
		{"D", 0.96, 1, 0.02, 0.40, 100, 0.002, -0.5, 5, 1, 0.405},
		{"E", 0.30, 4, 0.02, 0.50, 72, 0.002, 5, 5, 5, 0.571},
		{"F", 0.30, 4, 0.02, 0.70, 72, 0.002, 5, 1.88, 1, 0.571},
		{"G", 0.88, 1.5, 0.02, 0.70, 72, 0.0005, 2.33, 7.5, 2, 0.571},
		{"H", 0.21, 4, 0.02, 0.80, 14, 0.004, 5, 5, 5, 0.917},
	}
	hours := func(h float64) time.Duration { return time.Duration(h * float64(time.Hour)) }
	for _, c := range cases {
		t.Run("AG patch: example "+c.name, func(t *testing.T) {
			w5 := &agPaceWin{Util: c.u5, Remaining: hours(c.t5), Burn: c.b5, BurnKnown: true}
			w7 := &agPaceWin{Util: c.u7, Remaining: hours(c.t7), Burn: c.b7, BurnKnown: true}
			n5, hold5 := agPaceSessionAllowed(w5, max)
			n7, hold7 := agPaceWeeklyAllowed(w7, max)
			if hold5 || hold7 {
				t.Fatalf("hold = %v/%v with known burn", hold5, hold7)
			}
			if !agPaceApprox(n5, c.n5) || !agPaceApprox(n7, c.n7) {
				t.Fatalf("n_5h = %.3f n_7d = %.3f, want %.2f %.2f", n5, n7, c.n5, c.n7)
			}
			if got := agPaceTarget(n5, n7, max); got != c.wantTarget {
				t.Fatalf("target = %d, want %d", got, c.wantTarget)
			}
		})
	}

	t.Run("AG patch: the session knee is 80% and the weekly curve is inclusive", func(t *testing.T) {
		at := func(u float64) float64 {
			n, _ := agPaceSessionAllowed(&agPaceWin{Util: u, Remaining: hours(2), Burn: 0.02, BurnKnown: true}, max)
			return n
		}
		if at(0.7999) != max || at(0.80) == max {
			t.Fatalf("knee: n(0.7999) = %v, n(0.80) = %v", at(0.7999), at(0.80))
		}
		// 100h left of 168: the curve is 1 - 100/168 = 0.4048.
		wk := func(u float64) float64 {
			n, _ := agPaceWeeklyAllowed(&agPaceWin{Util: u, Remaining: hours(100), Burn: 0.002, BurnKnown: true}, max)
			return n
		}
		if wk(0.4047) != max || wk(0.41) == max {
			t.Fatalf("curve: n(0.4047) = %v, n(0.41) = %v", wk(0.4047), wk(0.41))
		}
		// Late in the week the curve is capped at the 97% target.
		late := &agPaceWin{Util: 0.98, Remaining: hours(1), Burn: 0.002, BurnKnown: true}
		if n, _ := agPaceWeeklyAllowed(late, max); n > 0 {
			t.Fatalf("n_7d at 98%% with 1h left = %v, want <= 0", n)
		}
	})

	t.Run("AG patch: a window that constrains nothing, or whose burn is unknown, is handled", func(t *testing.T) {
		if n, hold := agPaceSessionAllowed(nil, max); n != max || hold {
			t.Fatalf("nil window = %v/%v", n, hold)
		}
		if _, hold := agPaceSessionAllowed(&agPaceWin{Util: 0.9, Remaining: hours(2)}, max); !hold {
			t.Fatal("paced zone with unknown burn is not a hold")
		}
		if _, hold := agPaceSessionAllowed(&agPaceWin{Util: 0.5, Remaining: hours(2)}, max); hold {
			t.Fatal("unknown burn below the knee must not hold")
		}
		if n, _ := agPaceSessionAllowed(&agPaceWin{Util: 0.9, Remaining: hours(2), BurnKnown: true}, max); n != max {
			t.Fatalf("zero burn below target = %v, want max", n)
		}
		if got := agPaceTarget(math.Inf(1), 99, 3); got != 3 {
			t.Fatalf("target clamp = %d, want 3", got)
		}
	})
}

func TestAGPatchPaceLimiter(t *testing.T) {
	t0 := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	t.Run("AG patch: B steps 5, 4, 3, 2 at t0, t0+10m, t0+20m", func(t *testing.T) {
		var l agPaceLimiter
		var got []int
		for _, at := range []time.Duration{0, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 20 * time.Minute, 30 * time.Minute} {
			got = append(got, l.step(2, 5, t0.Add(at), false))
		}
		if want := []int{4, 4, 3, 3, 2, 2}; !slices.Equal(got, want) {
			t.Fatalf("caps = %v, want %v", got, want)
		}
	})
	t.Run("AG patch: a step up is also one at a time, and a quiet period makes the first step immediate", func(t *testing.T) {
		l := agPaceLimiter{cap: 1, set: true, changedAt: t0}
		if got := l.step(5, 5, t0.Add(9*time.Minute), false); got != 1 {
			t.Fatalf("stepped after 9m: %d", got)
		}
		if got := l.step(5, 5, t0.Add(time.Hour), false); got != 2 {
			t.Fatalf("after a quiet hour = %d, want 2", got)
		}
		if got := l.step(5, 5, t0.Add(time.Hour+9*time.Minute), false); got != 2 {
			t.Fatalf("second step too early: %d", got)
		}
	})
	t.Run("AG patch: a jump sets the cap outright", func(t *testing.T) {
		l := agPaceLimiter{cap: 1, set: true, changedAt: t0}
		if got := l.step(5, 5, t0.Add(time.Minute), true); got != 5 {
			t.Fatalf("jump = %d, want 5", got)
		}
		if got := l.step(1, 5, t0.Add(2*time.Minute), false); got != 5 {
			t.Fatalf("stepped within 10m of a jump: %d", got)
		}
	})
	t.Run("AG patch: a lowered Max pulls the cap down", func(t *testing.T) {
		l := agPaceLimiter{cap: 5, set: true, changedAt: t0}
		if got := l.step(2, 2, t0, false); got != 2 {
			t.Fatalf("cap = %d, want 2", got)
		}
	})
}

// agPaceFixture is a daemon with a fake clock, a state file and the default
// Claude account's windows set directly.
type agPaceFixture struct {
	d     *Daemon
	clock *agQuotaClock
	acct  string
}

func newAGPaceFixture(t *testing.T, max int) *agPaceFixture {
	t.Helper()
	return newAGPaceFixtureAt(t, max, newAGQuotaClaimServer(t).URL)
}

func newAGPaceFixtureAt(t *testing.T, max int, serverURL string) *agPaceFixture {
	t.Helper()
	acct := agQuotaEnv(t)
	clock := &agQuotaClock{t: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	d := newAGQuotaDaemon(t, serverURL, filepath.Join(t.TempDir(), "ag_quota.json"), clock)
	d.cfg.MaxConcurrentTasks = max
	return &agPaceFixture{d: d, clock: clock, acct: filepath.Clean(acct)}
}

// event feeds one allowed event carrying both windows to the account's observer.
func (f *agPaceFixture) event(t *testing.T, u5, u7 float64, reset5, reset7 time.Time) {
	t.Helper()
	raw := fmt.Sprintf(`{"status":"allowed","rateLimitType":"five_hour","resetsAt":%d,"unifiedWindows":{"five_hour":{"utilization":%g,"resetsAt":%d},"seven_day":{"utilization":%g,"resetsAt":%d}}}`,
		reset5.Unix(), u5, reset5.Unix(), u7, reset7.Unix())
	agQuotaObs(f.d, Task{ID: "evt"}, "claude", nil)(json.RawMessage(raw))
}

func (f *agPaceFixture) cap(t *testing.T) (int, int) {
	t.Helper()
	held := false
	if _, hold := f.d.agQuotaDefaultHold(); hold != nil {
		held = true
	}
	return f.d.agPaceEvaluate(f.acct, f.d.agQuotaDefaultWindows(), held, f.d.cfg.MaxConcurrentTasks, f.clock.now())
}

func TestAGPatchPaceBurnAndCap(t *testing.T) {
	t.Run("AG patch: with no state the cap is Max", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		if c, a := f.cap(t); c != 5 || a != 0 {
			t.Fatalf("cap/active = %d/%d", c, a)
		}
	})

	t.Run("AG patch: the burn comes from du/dt over the mean active runs and the cap follows it", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		reset5, reset7 := now.Add(3*time.Hour), now.Add(100*time.Hour)

		f.event(t, 0.70, 0.40, reset5, reset7) // first sight: the baseline
		// Two runs for the whole interval.
		done1 := f.d.agPaceRunStart("r1", f.acct, f.clock.now())
		done2 := f.d.agPaceRunStart("r2", f.acct, f.clock.now())
		f.clock.set(now.Add(30 * time.Minute))
		f.event(t, 0.80, 0.40, reset5, reset7) // +0.10 in 0.5h with 2 runs: 0.1 per run-hour
		if a := f.d.agPaceHealth().ActiveClaude; a != 2 {
			t.Fatalf("active = %d, want 2", a)
		}
		f.d.agPace.mu.Lock()
		st := f.d.agPace.accounts[f.acct].wins[agQuotaWindowFiveHour]
		burn, known := st.burn, st.known
		f.d.agPace.mu.Unlock()
		if !known || !agPaceApprox(burn*100, 10) {
			t.Fatalf("burn = %v (known %v), want 0.10", burn, known)
		}

		// Second sample, +0.05 in 0.5h with 2 runs: r/ā = 0.05 -> EWMA 0.3*0.05+0.7*0.1.
		f.clock.set(now.Add(time.Hour))
		f.event(t, 0.85, 0.40, reset5, reset7)
		f.d.agPace.mu.Lock()
		burn = f.d.agPace.accounts[f.acct].wins[agQuotaWindowFiveHour].burn
		f.d.agPace.mu.Unlock()
		if want := 0.3*0.05 + 0.7*0.10; math.Abs(burn-want) > 1e-6 {
			t.Fatalf("EWMA burn = %v, want %v", burn, want)
		}
		// n = (0.95-0.85)/(2h*0.085) = 0.59 -> target 1; one step from 5 is 4.
		c, _ := f.cap(t)
		if c != 4 {
			t.Fatalf("cap = %d, want 4 (one step from Max)", c)
		}
		f.clock.set(now.Add(time.Hour + 5*time.Minute))
		if c, _ := f.cap(t); c != 4 {
			t.Fatalf("cap moved again inside 10m: %d", c)
		}
		f.clock.set(now.Add(time.Hour + 10*time.Minute))
		if c, _ := f.cap(t); c != 3 {
			t.Fatalf("cap = %d after 10m, want 3", c)
		}
		done1()
		done2()
		done2() // safe to call twice
		if _, a := f.cap(t); a != 0 {
			t.Fatalf("active after done = %d", a)
		}
	})

	t.Run("AG patch: burn unknown in the paced zone holds position", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		f.event(t, 0.90, 0.10, now.Add(2*time.Hour), now.Add(100*time.Hour))
		if c, _ := f.cap(t); c != 1 {
			t.Fatalf("cap = %d, want 1 (a fresh process holds at the floor)", c)
		}
		f.d.agPace.mu.Lock()
		f.d.agPace.accounts[f.acct].limiter.cap = 3
		f.d.agPace.mu.Unlock()
		if c, _ := f.cap(t); c != 3 {
			t.Fatalf("cap = %d, want 3 (hold position)", c)
		}
	})

	t.Run("AG patch: negative du is dropped, and an idle sample leaves the burn alone", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		reset5, reset7 := now.Add(4*time.Hour), now.Add(100*time.Hour)
		f.event(t, 0.50, 0.10, reset5, reset7)
		f.clock.set(now.Add(10 * time.Minute))
		f.event(t, 0.40, 0.10, reset5, reset7) // went down: no sample
		f.d.agPace.mu.Lock()
		known := f.d.agPace.accounts[f.acct].wins[agQuotaWindowFiveHour].known
		f.d.agPace.mu.Unlock()
		if known {
			t.Fatal("a negative du produced a burn estimate")
		}
		// 15 minutes with no change and no runs: nothing to learn.
		f.clock.set(now.Add(30 * time.Minute))
		f.cap(t)
		f.d.agPace.mu.Lock()
		known = f.d.agPace.accounts[f.acct].wins[agQuotaWindowFiveHour].known
		f.d.agPace.mu.Unlock()
		if known {
			t.Fatal("an idle interval produced a burn estimate")
		}
	})

	t.Run("AG patch: a window rollover and a cleared hold set the cap outright", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		reset5, reset7 := now.Add(2*time.Hour), now.Add(100*time.Hour)
		f.event(t, 0.50, 0.10, reset5, reset7)
		f.d.agPace.mu.Lock()
		a := f.d.agPace.accounts[f.acct]
		a.limiter = agPaceLimiter{cap: 1, set: true, changedAt: now}
		a.wins[agQuotaWindowFiveHour].burn, a.wins[agQuotaWindowFiveHour].known = 0.001, true
		f.d.agPace.mu.Unlock()

		f.clock.set(now.Add(time.Minute))
		if c, _ := f.cap(t); c != 1 {
			t.Fatalf("stepped inside 10m of the last change: %d", c)
		}
		// The 5h window resets: new resetsAt, low utilisation.
		f.clock.set(now.Add(3 * time.Minute))
		f.event(t, 0.05, 0.10, now.Add(5*time.Hour), reset7)
		if c, _ := f.cap(t); c != 5 {
			t.Fatalf("cap after rollover = %d, want 5", c)
		}

		// A hold clears: the cap jumps to the target rather than stepping.
		f.d.agPace.mu.Lock()
		a = f.d.agPace.accounts[f.acct]
		a.limiter = agPaceLimiter{cap: 1, set: true, changedAt: f.clock.now()}
		f.d.agPace.mu.Unlock()
		f.d.agPaceEvaluate(f.acct, f.d.agQuotaDefaultWindows(), true, 5, f.clock.now())
		f.clock.set(f.clock.now().Add(time.Minute))
		if c, _ := f.d.agPaceEvaluate(f.acct, f.d.agQuotaDefaultWindows(), false, 5, f.clock.now()); c != 5 {
			t.Fatalf("cap after the hold cleared = %d, want 5", c)
		}
	})

	t.Run("AG patch: an expired window constrains nothing", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		f.event(t, 0.99, 0.10, now.Add(time.Hour), now.Add(100*time.Hour))
		f.clock.set(now.Add(2 * time.Hour)) // past the reset, no new event yet
		if c, _ := f.cap(t); c != 5 {
			t.Fatalf("cap = %d with an expired window, want 5", c)
		}
	})

	t.Run("AG patch: the weekly cap bounds the session cap", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		reset5, reset7 := now.Add(4*time.Hour), now.Add(72*time.Hour)
		f.event(t, 0.30, 0.50, reset5, reset7)
		f.d.agPace.mu.Lock()
		a := f.d.agPace.accounts[f.acct]
		a.wins[agQuotaWindowFiveHour].burn, a.wins[agQuotaWindowFiveHour].known = 0.02, true
		a.wins[agQuotaWindowSevenDay].burn, a.wins[agQuotaWindowSevenDay].known = 0.002, true
		f.d.agPace.mu.Unlock()
		if c, _ := f.cap(t); c != 5 { // example E
			t.Fatalf("cap under the curve = %d, want 5", c)
		}
		f.clock.set(now.Add(time.Second))
		f.event(t, 0.30, 0.70, reset5, reset7) // example F: over the curve
		f.clock.set(now.Add(time.Second + 2*time.Minute))
		if c, _ := f.cap(t); c != 4 {
			t.Fatalf("cap over the weekly curve = %d, want one step down (4)", c)
		}
	})
}

func TestAGPatchPaceActiveCount(t *testing.T) {
	f := newAGPaceFixture(t, 5)
	other := filepath.Join(t.TempDir(), "other-claude")

	t.Run("AG patch: only the daemon's own account counts toward the cap", func(t *testing.T) {
		doneOwn := f.d.agPaceRunStart("own", f.acct, f.clock.now())
		doneOther := f.d.agPaceRunStart("other", other, f.clock.now())
		if h := f.d.agPaceHealth(); h.ActiveClaude != 1 {
			t.Fatalf("active_claude = %d, want 1", h.ActiveClaude)
		}
		doneOwn()
		doneOther()
	})

	t.Run("AG patch: the observer counts a Claude run until done and a non-Claude run never", func(t *testing.T) {
		obs, done := f.d.agQuotaObserver(Task{ID: "c1"}, "claude", nil)
		if obs == nil || f.d.agPaceHealth().ActiveClaude != 1 {
			t.Fatalf("claude run not counted: %+v", f.d.agPaceHealth())
		}
		done()
		if f.d.agPaceHealth().ActiveClaude != 0 {
			t.Fatal("claude run still counted after done")
		}
		obs, done = f.d.agQuotaObserver(Task{ID: "x1"}, "codex", nil)
		if obs != nil || done == nil {
			t.Fatalf("codex run: observer %v done %v", obs != nil, done != nil)
		}
		if f.d.agPaceHealth().ActiveClaude != 0 {
			t.Fatal("codex run counted")
		}
		done()
	})

	t.Run("AG patch: a claimed task counts until its dispatch goroutine exits, with no timeout", func(t *testing.T) {
		f.d.agPaceNoteClaimed([]*Task{{ID: "t1", RuntimeID: "rt-claude"}, {ID: "t2", RuntimeID: "rt-codex"}, nil})
		if f.d.agPaceHealth().ActiveClaude != 1 {
			t.Fatalf("claimed count = %d, want 1 (Codex task ignored)", f.d.agPaceHealth().ActiveClaude)
		}
		_, done := f.d.agQuotaObserver(Task{ID: "t1"}, "claude", nil)
		if f.d.agPaceHealth().ActiveClaude != 1 {
			t.Fatalf("run replacing its claim double counted: %d", f.d.agPaceHealth().ActiveClaude)
		}
		done()
		f.d.agUntrackTask("t1")
		f.d.agPaceNoteClaimed([]*Task{{ID: "t3", RuntimeID: "rt-claude"}})
		f.clock.set(f.clock.now().Add(24 * time.Hour))
		if f.d.agPaceHealth().ActiveClaude != 1 {
			t.Fatal("a claim whose run has not started stopped counting before its goroutine exited")
		}
		f.d.agUntrackTask("t3")
		if f.d.agPaceHealth().ActiveClaude != 0 {
			t.Fatal("a claim still counts after its dispatch goroutine exited")
		}
	})

	t.Run("AG patch: health shows active_claude and effective_cap", func(t *testing.T) {
		g := newAGPaceFixture(t, 4)
		done := g.d.agPaceRunStart("h1", g.acct, g.clock.now())
		defer done()
		raw, err := json.Marshal(g.d.agQuotaHealth())
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			AGQuota struct {
				ActiveClaude int `json:"active_claude"`
				EffectiveCap int `json:"effective_cap"`
			} `json:"ag_quota"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.AGQuota.ActiveClaude != 1 || got.AGQuota.EffectiveCap != 4 {
			t.Fatalf("ag_quota = %+v, want active 1 cap 4 (%s)", got.AGQuota, raw)
		}
	})
}

// agPaceClaimServer answers batch claims: each call returns up to max_tasks
// tasks for its first runtime, and records the calls.
type agPaceClaimServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls []agPaceClaimCall
	n     int
}

type agPaceClaimCall struct {
	RuntimeIDs []string `json:"runtime_ids"`
	MaxTasks   int      `json:"max_tasks"`
}

func newAGPaceClaimServer(t *testing.T) *agPaceClaimServer {
	t.Helper()
	s := &agPaceClaimServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/claim") {
			w.Write([]byte(`{}`))
			return
		}
		var call agPaceClaimCall
		_ = json.NewDecoder(r.Body).Decode(&call)
		s.mu.Lock()
		s.calls = append(s.calls, call)
		var tasks []string
		for i := 0; i < call.MaxTasks; i++ {
			s.n++
			tasks = append(tasks, fmt.Sprintf(`{"id":"task-%d","runtime_id":%q}`, s.n, call.RuntimeIDs[0]))
		}
		s.mu.Unlock()
		fmt.Fprintf(w, `{"tasks":[%s]}`, strings.Join(tasks, ","))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *agPaceClaimServer) recorded() []agPaceClaimCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agPaceClaimCall(nil), s.calls...)
}

func TestAGPatchPaceClaimSplit(t *testing.T) {
	setup := func(t *testing.T, cap, max int) (*agPaceFixture, *agPaceClaimServer) {
		t.Helper()
		srv := newAGPaceClaimServer(t)
		f := newAGPaceFixtureAt(t, max, srv.URL)
		f.d.agPace.mu.Lock()
		f.d.agPace.accountLocked(f.acct).limiter = agPaceLimiter{cap: cap, set: true, changedAt: f.clock.now()}
		f.d.agPace.mu.Unlock()
		// A window pair pinned in the paced zone keeps the cap where the test put it.
		now := f.clock.now()
		f.event(t, 0.90, 0.10, now.Add(2*time.Hour), now.Add(100*time.Hour))
		return f, srv
	}
	ids := []string{"rt-claude", "rt-codex", "rt-claude-b"}

	t.Run("AG patch: headroom below the slots claims Claude first with max_tasks = headroom, then the rest", func(t *testing.T) {
		f, srv := setup(t, 3, 4)
		done := f.d.agPaceRunStart("running", f.acct, f.clock.now()) // active 1, room 2
		defer done()
		res, err := f.d.agQuotaClaim(context.Background(), "daemon-test", ids, 4)
		if err != nil {
			t.Fatal(err)
		}
		calls := srv.recorded()
		want := []agPaceClaimCall{
			{RuntimeIDs: []string{"rt-claude", "rt-claude-b"}, MaxTasks: 2},
			{RuntimeIDs: []string{"rt-codex"}, MaxTasks: 2},
		}
		if len(calls) != 2 || !slices.Equal(calls[0].RuntimeIDs, want[0].RuntimeIDs) || calls[0].MaxTasks != 2 ||
			!slices.Equal(calls[1].RuntimeIDs, want[1].RuntimeIDs) || calls[1].MaxTasks != 2 {
			t.Fatalf("calls = %+v, want %+v", calls, want)
		}
		if len(res.Tasks) != 4 {
			t.Fatalf("tasks = %d, want 4", len(res.Tasks))
		}
		if f.d.agPaceHealth().ActiveClaude != 3 { // the running one + the two just claimed
			t.Fatalf("active after the claim = %d, want 3", f.d.agPaceHealth().ActiveClaude)
		}
		// With the claim counted, the next one has no room for Claude.
		if _, err := f.d.agQuotaClaim(context.Background(), "daemon-test", ids, 2); err != nil {
			t.Fatal(err)
		}
		last := srv.recorded()[len(srv.recorded())-1]
		if !slices.Equal(last.RuntimeIDs, []string{"rt-codex"}) || last.MaxTasks != 2 {
			t.Fatalf("claim with no Claude room = %+v", last)
		}
	})

	t.Run("AG patch: headroom at or above the slots is one unchanged claim", func(t *testing.T) {
		f, srv := setup(t, 4, 4)
		if _, err := f.d.agQuotaClaim(context.Background(), "daemon-test", ids, 3); err != nil {
			t.Fatal(err)
		}
		calls := srv.recorded()
		if len(calls) != 1 || !slices.Equal(calls[0].RuntimeIDs, ids) || calls[0].MaxTasks != 3 {
			t.Fatalf("calls = %+v", calls)
		}
	})

	t.Run("AG patch: no headroom drops the Claude runtimes, and only Claude runtimes means no call", func(t *testing.T) {
		f, srv := setup(t, 1, 4)
		done := f.d.agPaceRunStart("running", f.acct, f.clock.now())
		defer done()
		if _, err := f.d.agQuotaClaim(context.Background(), "daemon-test", ids, 3); err != nil {
			t.Fatal(err)
		}
		calls := srv.recorded()
		if len(calls) != 1 || !slices.Equal(calls[0].RuntimeIDs, []string{"rt-codex"}) || calls[0].MaxTasks != 3 {
			t.Fatalf("calls = %+v", calls)
		}
		if res, err := f.d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude"}, 3); err != nil || len(res.Tasks) != 0 || len(srv.recorded()) != 1 {
			t.Fatalf("claude-only claim with no room: %+v, %v, %d calls", res, err, len(srv.recorded()))
		}
	})
}

func TestAGPatchPaceMergeClaims(t *testing.T) {
	a := claimTasksResult{Tasks: []*Task{{ID: "1"}}, ClaimedOverWS: true, ClaimPollHintSupported: true, NextDeferredTaskAfterMillis: 5000}
	b := claimTasksResult{Tasks: []*Task{{ID: "2"}, {ID: "3"}}, ClaimedOverWS: true, ClaimPollHintSupported: false, NextDeferredTaskAfterMillis: 2000}
	got := agQuotaMergeClaims(a, b)
	if len(got.Tasks) != 3 || got.Tasks[0].ID != "1" || got.Tasks[2].ID != "3" {
		t.Fatalf("tasks = %+v", got.Tasks)
	}
	if !got.ClaimedOverWS || got.ClaimPollHintSupported || got.NextDeferredTaskAfterMillis != 2000 {
		t.Fatalf("flags = %+v", got)
	}
	got = agQuotaMergeClaims(claimTasksResult{ClaimedOverWS: true}, claimTasksResult{})
	if got.ClaimedOverWS || got.NextDeferredTaskAfterMillis != 0 {
		t.Fatalf("one side not over WS must clear it: %+v", got)
	}
	got = agQuotaMergeClaims(claimTasksResult{}, claimTasksResult{NextDeferredTaskAfterMillis: 700})
	if got.NextDeferredTaskAfterMillis != 700 {
		t.Fatalf("hint = %d, want 700 (0 means none)", got.NextDeferredTaskAfterMillis)
	}
}

// TestAGPatchPaceOverlap checks the successor adds the predecessor's reported
// Claude runs, using the real /health body of a second daemon.
func TestAGPatchPaceOverlap(t *testing.T) {
	t.Run("AG patch: the successor adds the predecessor's active_claude and falls back to its tasks", func(t *testing.T) {
		pred := newAGPaceFixture(t, 4)
		succ := newAGPaceFixture(t, 4) // both fixtures share the process's CLAUDE_CONFIG_DIR, so one default account
		var mu sync.Mutex
		down, omitQuota := false, false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if down {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			body := map[string]any{"pid": os.Getpid(), "active_task_ids": []string{"a", "b", "c"}}
			if !omitQuota {
				body["ag_quota"] = pred.d.agQuotaHealth().AGQuota
			}
			_ = json.NewEncoder(w).Encode(body)
		}))
		t.Cleanup(srv.Close)
		portNum := srv.Listener.Addr().(*net.TCPAddr).Port

		succ.d.AGEnableOverlap(AGPredecessor{PID: os.Getpid(), Port: portNum, TaskIDs: []string{"a", "b", "c"}, MaxConcurrentTasks: 4})
		step := func() {
			failures := 0
			if succ.d.agOverlapStep(context.Background(), &failures) {
				t.Fatal("overlap ended while the predecessor is alive")
			}
		}
		_, active := succ.cap(t)
		if active != 3 {
			t.Fatalf("before the predecessor reports: active = %d, want its 3 tasks (fail closed)", active)
		}

		d1 := pred.d.agPaceRunStart("p1", succ.acct, pred.clock.now())
		d2 := pred.d.agPaceRunStart("p2", succ.acct, pred.clock.now())
		step()
		if _, active := succ.cap(t); active != 2 {
			t.Fatalf("with the predecessor's 2 Claude runs: active = %d, want 2", active)
		}
		mine := succ.d.agPaceRunStart("s1", succ.acct, succ.clock.now())
		if _, active := succ.cap(t); active != 3 {
			t.Fatalf("own + predecessor = %d, want 3", active)
		}
		if h := succ.d.agPaceHealth(); h.ActiveClaude != 1 {
			t.Fatalf("own health active_claude = %d, want 1", h.ActiveClaude)
		}

		mu.Lock()
		down = true
		mu.Unlock()
		d1()
		d2()
		step() // unreachable: the last known value stands
		if _, active := succ.cap(t); active != 3 {
			t.Fatalf("health unreachable: active = %d, want the last known 3", active)
		}

		mu.Lock()
		down = false
		mu.Unlock()
		step()
		if _, active := succ.cap(t); active != 1 {
			t.Fatalf("predecessor drained: active = %d, want 1", active)
		}

		succ.d.agFinishOverlap(context.Background())
		d3 := succ.d.agPaceRunStart("s2", succ.acct, succ.clock.now())
		defer d3()
		mine()
		if _, active := succ.cap(t); active != 1 {
			t.Fatalf("after overlap ended: active = %d, want only its own", active)
		}
	})

	t.Run("AG patch: a predecessor without the ag_quota block counts all its tasks", func(t *testing.T) {
		succ := newAGPaceFixture(t, 4)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"pid": os.Getpid(), "active_task_ids": []string{"a", "b"}})
		}))
		t.Cleanup(srv.Close)
		portNum := srv.Listener.Addr().(*net.TCPAddr).Port
		succ.d.AGEnableOverlap(AGPredecessor{PID: os.Getpid(), Port: portNum, TaskIDs: []string{"a", "b"}, MaxConcurrentTasks: 4})
		failures := 0
		succ.d.agOverlapStep(context.Background(), &failures)
		if _, active := succ.cap(t); active != 2 {
			t.Fatalf("active = %d, want 2", active)
		}
	})
}

// TestAGPatchPaceRunTaskEndsCount drives runTask: the run's pacing count ends
// when it returns.
func TestAGPatchPaceRunTaskEndsCount(t *testing.T) {
	d, _, _ := agQuotaRunTask(t, nil)
	if h := d.agPaceHealth(); h.ActiveClaude != 0 {
		t.Fatalf("active_claude after runTask = %d, want 0", h.ActiveClaude)
	}
	d.agPace.mu.Lock()
	n := len(d.agPace.running)
	d.agPace.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d runs still registered", n)
	}
}
