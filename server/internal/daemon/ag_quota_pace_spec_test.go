package daemon

// AG patch: Claude pacing tests written from the Spec's rules (burn estimate,
// limiter timeline through the claim path, split-claim accounting, account
// scope), alongside ag_quota_pace_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// agPaceSpecServer answers batch claims with a configurable number of tasks per
// call (none by default, so claims do not change the active count) and records
// each call.
type agPaceSpecServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls []agPaceClaimCall
	n     int
	// tasks is how many tasks to return for a call; nil returns none.
	tasks func(call agPaceClaimCall) int
	// status, when it returns non-200 for a call, fails that call.
	status func(callIndex int, call agPaceClaimCall) int
}

func newAGPaceSpecServer(t *testing.T) *agPaceSpecServer {
	t.Helper()
	s := &agPaceSpecServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/claim") {
			w.Write([]byte(`{}`))
			return
		}
		var call agPaceClaimCall
		_ = json.NewDecoder(r.Body).Decode(&call)
		s.mu.Lock()
		idx := len(s.calls)
		s.calls = append(s.calls, call)
		status, count := http.StatusOK, 0
		if s.status != nil {
			status = s.status(idx, call)
		}
		if s.tasks != nil {
			count = s.tasks(call)
		}
		var tasks []string
		for i := 0; i < count; i++ {
			s.n++
			tasks = append(tasks, fmt.Sprintf(`{"id":"spec-task-%d","runtime_id":%q}`, s.n, call.RuntimeIDs[0]))
		}
		s.mu.Unlock()
		if status != http.StatusOK {
			http.Error(w, `{"error":"boom"}`, status)
			return
		}
		fmt.Fprintf(w, `{"tasks":[%s]}`, strings.Join(tasks, ","))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *agPaceSpecServer) recorded() []agPaceClaimCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agPaceClaimCall(nil), s.calls...)
}

func (s *agPaceSpecServer) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

func agPaceHasClaude(ids []string) bool {
	return slices.ContainsFunc(ids, func(id string) bool { return strings.HasPrefix(id, "rt-claude") })
}

// claudeMax returns the max_tasks of the Claude call in calls, or -1 when none
// of them listed a Claude runtime.
func agPaceClaudeMax(calls []agPaceClaimCall) int {
	for _, c := range calls {
		if agPaceHasClaude(c.RuntimeIDs) {
			return c.MaxTasks
		}
	}
	return -1
}

func (f *agPaceFixture) burn(name string) (float64, bool) {
	f.d.agPace.mu.Lock()
	defer f.d.agPace.mu.Unlock()
	st := f.d.agPace.accounts[f.acct].wins[name]
	if st == nil {
		return 0, false
	}
	return st.burn, st.known
}

// TestAGPatchPaceSpecBurnEstimate checks the Spec's burn rules: r = du/dt, a
// time-weighted mean of active runs floored at 1, EWMA alpha 0.3, a sample when
// u changes or 15 minutes after the last one, nothing across windows.
func TestAGPatchPaceSpecBurnEstimate(t *testing.T) {
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

	t.Run("AG patch: the active-run mean is weighted by time", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		t0 := f.clock.now()
		reset5, reset7 := t0.Add(4*time.Hour), t0.Add(100*time.Hour)
		f.event(t, 0.50, 0.10, reset5, reset7)
		// Two runs for the second half hour of a one hour interval: mean 1.
		f.clock.set(t0.Add(30 * time.Minute))
		d1 := f.d.agPaceRunStart("w1", f.acct, f.clock.now())
		d2 := f.d.agPaceRunStart("w2", f.acct, f.clock.now())
		f.clock.set(t0.Add(time.Hour))
		f.event(t, 0.60, 0.10, reset5, reset7) // +0.10 in 1h
		if b, ok := f.burn(agQuotaWindowFiveHour); !ok || !near(b, 0.10) {
			t.Fatalf("burn = %v (known %v), want 0.10 (r 0.10/h over a mean of 1 run)", b, ok)
		}
		d1()
		d2()
	})

	t.Run("AG patch: a mean below one run is floored at one", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		t0 := f.clock.now()
		reset5, reset7 := t0.Add(4*time.Hour), t0.Add(100*time.Hour)
		f.event(t, 0.50, 0.10, reset5, reset7)
		// One run for the last 15 minutes of the hour: mean 0.25.
		f.clock.set(t0.Add(45 * time.Minute))
		done := f.d.agPaceRunStart("w1", f.acct, f.clock.now())
		f.clock.set(t0.Add(time.Hour))
		f.event(t, 0.54, 0.10, reset5, reset7)
		done()
		if b, ok := f.burn(agQuotaWindowFiveHour); !ok || !near(b, 0.04) {
			t.Fatalf("burn = %v (known %v), want 0.04 (r / max(0.25, 1))", b, ok)
		}
	})

	t.Run("AG patch: later samples blend in at alpha 0.3", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		t0 := f.clock.now()
		reset5, reset7 := t0.Add(4*time.Hour), t0.Add(100*time.Hour)
		done := f.d.agPaceRunStart("w1", f.acct, t0)
		defer done()
		f.event(t, 0.50, 0.10, reset5, reset7)
		f.clock.set(t0.Add(time.Hour))
		f.event(t, 0.60, 0.10, reset5, reset7) // 0.10 per run-hour
		f.clock.set(t0.Add(2 * time.Hour))
		f.event(t, 0.65, 0.10, reset5, reset7) // 0.05 per run-hour
		if b, _ := f.burn(agQuotaWindowFiveHour); !near(b, 0.3*0.05+0.7*0.10) {
			t.Fatalf("burn = %v, want %v", b, 0.3*0.05+0.7*0.10)
		}
	})

	t.Run("AG patch: with runs active, 15 quiet minutes is a zero sample and 10 is not", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		t0 := f.clock.now()
		reset5, reset7 := t0.Add(4*time.Hour), t0.Add(100*time.Hour)
		done := f.d.agPaceRunStart("w1", f.acct, t0)
		defer done()
		f.event(t, 0.50, 0.10, reset5, reset7)
		f.clock.set(t0.Add(time.Hour))
		f.event(t, 0.60, 0.10, reset5, reset7) // burn 0.10
		f.clock.set(t0.Add(time.Hour + 10*time.Minute))
		f.cap(t)
		if b, _ := f.burn(agQuotaWindowFiveHour); !near(b, 0.10) {
			t.Fatalf("burn after 10 quiet minutes = %v, want 0.10 unchanged", b)
		}
		f.clock.set(t0.Add(time.Hour + 15*time.Minute))
		f.cap(t)
		if b, _ := f.burn(agQuotaWindowFiveHour); !near(b, 0.7*0.10) {
			t.Fatalf("burn after 15 quiet minutes = %v, want %v (a zero sample)", b, 0.7*0.10)
		}
	})

	t.Run("AG patch: a new resetsAt starts afresh and never differences across windows", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		t0 := f.clock.now()
		f.event(t, 0.50, 0.10, t0.Add(time.Hour), t0.Add(100*time.Hour))
		f.clock.set(t0.Add(30 * time.Minute))
		done := f.d.agPaceRunStart("w1", f.acct, f.clock.now())
		defer done()
		// Same window so far: no burn yet. Then the window rolls over to a new
		// resetsAt with a higher utilisation than before; that gap is not a rate.
		f.event(t, 0.60, 0.10, t0.Add(6*time.Hour), t0.Add(100*time.Hour))
		if b, ok := f.burn(agQuotaWindowFiveHour); ok {
			t.Fatalf("burn = %v: a sample straddled two windows", b)
		}
		f.clock.set(t0.Add(90 * time.Minute))
		f.event(t, 0.66, 0.10, t0.Add(6*time.Hour), t0.Add(100*time.Hour)) // +0.06 in 1h, 1 run
		if b, ok := f.burn(agQuotaWindowFiveHour); !ok || !near(b, 0.06) {
			t.Fatalf("burn = %v (known %v), want 0.06 from the new window", b, ok)
		}
	})

	t.Run("AG patch: the two windows keep separate estimates", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		t0 := f.clock.now()
		reset5, reset7 := t0.Add(4*time.Hour), t0.Add(100*time.Hour)
		done := f.d.agPaceRunStart("w1", f.acct, t0)
		defer done()
		f.event(t, 0.50, 0.20, reset5, reset7)
		f.clock.set(t0.Add(time.Hour))
		f.event(t, 0.60, 0.21, reset5, reset7)
		b5, _ := f.burn(agQuotaWindowFiveHour)
		b7, _ := f.burn(agQuotaWindowSevenDay)
		if !near(b5, 0.10) || !near(b7, 0.01) {
			t.Fatalf("burn 5h %v, 7d %v; want 0.10 and 0.01", b5, b7)
		}
	})
}

// TestAGPatchPaceSpecClaimTimeline drives worked example B's cap timeline through
// the claim path with real rate-limit events: the cap steps down one run per 10
// minutes, is held while the burn is unknown, and jumps back when the window
// rolls over; a hold takes the Claude runtimes out at once.
func TestAGPatchPaceSpecClaimTimeline(t *testing.T) {
	srv := newAGPaceSpecServer(t)
	f := newAGPaceFixtureAt(t, 5, srv.URL)
	t0 := f.clock.now()
	reset5, reset7 := t0.Add(4*time.Hour), t0.Add(100*time.Hour)
	ids := []string{"rt-claude", "rt-codex"}
	at := func(m int) { f.clock.set(t0.Add(time.Duration(m) * time.Minute)) }

	// One Claude run throughout, burning 0.02/h: the estimate settles at 0.02.
	done := f.d.agPaceRunStart("running", f.acct, t0)
	defer done()

	claim := func() (maxClaude, capNow int) {
		t.Helper()
		srv.reset()
		if _, err := f.d.agQuotaClaim(context.Background(), "daemon-test", ids, 5); err != nil {
			t.Fatal(err)
		}
		return agPaceClaudeMax(srv.recorded()), f.d.agPaceHealth().EffectiveCap
	}

	u := 0.90
	f.event(t, u, 0.10, reset5, reset7)
	// Burn unknown in the paced zone: hold position at Max. One run is active,
	// so four more may start.
	if m, c := claim(); c != 5 || m != 4 {
		t.Fatalf("t+0: cap %d, Claude max_tasks %d; want 5 and 4", c, m)
	}

	step := []struct {
		minute int
		wantC  int
	}{{10, 4}, {20, 3}, {30, 2}, {40, 1}}
	for _, s := range step {
		at(s.minute)
		u += 0.02 / 6 // 0.02 per hour for ten minutes
		f.event(t, u, 0.10, reset5, reset7)
		m, c := claim()
		if c != s.wantC {
			t.Fatalf("t+%dm: cap %d, want %d", s.minute, c, s.wantC)
		}
		if s.wantC == 1 {
			// Cap 1 with one run active: no room, so Claude drops out.
			if m != -1 {
				t.Fatalf("t+%dm: cap 1 with 1 active must not list Claude runtimes (max_tasks %d)", s.minute, m)
			}
		} else if m != s.wantC-1 {
			t.Fatalf("t+%dm: Claude max_tasks %d, want cap-active = %d", s.minute, m, s.wantC-1)
		}
		// Five minutes later nothing has moved.
		at(s.minute + 5)
		if _, c := claim(); c != s.wantC {
			t.Fatalf("t+%dm: cap moved to %d inside the 10 minute step", s.minute+5, c)
		}
	}
	at(50)
	if _, c := claim(); c != 1 {
		t.Fatalf("t+50m: cap %d, want the floor of 1", c)
	}

	t.Run("AG patch: a window rollover returns the cap straight to the target", func(t *testing.T) {
		at(52)
		f.event(t, 0.05, 0.10, t0.Add(9*time.Hour), reset7) // new 5h window, low utilisation
		m, c := claim()
		if c != 5 || m != 4 {
			t.Fatalf("after rollover: cap %d, Claude max_tasks %d; want 5 and 4", c, m)
		}
	})

	t.Run("AG patch: a rejection takes the Claude runtimes out of the very next claim", func(t *testing.T) {
		at(53)
		obs := agQuotaObs(f.d, Task{ID: "rej"}, "claude", nil)
		obs(json.RawMessage(fmt.Sprintf(`{"status":"rejected","rateLimitType":"five_hour","resetsAt":%d}`, t0.Add(9*time.Hour).Unix())))
		srv.reset()
		if _, err := f.d.agQuotaClaim(context.Background(), "daemon-test", ids, 5); err != nil {
			t.Fatal(err)
		}
		calls := srv.recorded()
		if len(calls) != 1 || !slices.Equal(calls[0].RuntimeIDs, []string{"rt-codex"}) || calls[0].MaxTasks != 5 {
			t.Fatalf("calls during a hold = %+v, want one claim for rt-codex with all 5 slots", calls)
		}
	})
}

// TestAGPatchPaceSpecWeekly covers the weekly rules through the cap: it is the
// upper limit on the session cap, and an unknown weekly burn holds position.
func TestAGPatchPaceSpecWeekly(t *testing.T) {
	seed := func(t *testing.T, f *agPaceFixture, cur int, b5, b7 *float64) {
		t.Helper()
		f.d.agPace.mu.Lock()
		defer f.d.agPace.mu.Unlock()
		a := f.d.agPace.accounts[f.acct]
		a.limiter = agPaceLimiter{cap: cur, set: true, changedAt: f.clock.now().Add(-time.Hour)}
		for name, b := range map[string]*float64{agQuotaWindowFiveHour: b5, agQuotaWindowSevenDay: b7} {
			if b != nil {
				a.wins[name].burn, a.wins[name].known = *b, true
			}
		}
	}
	ptr := func(v float64) *float64 { return &v }

	t.Run("AG patch: over the curve with an unknown weekly burn holds the current cap", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		f.event(t, 0.30, 0.70, now.Add(4*time.Hour), now.Add(72*time.Hour)) // example F inputs
		seed(t, f, 3, ptr(0.02), nil)
		if c, _ := f.cap(t); c != 3 {
			t.Fatalf("cap = %d, want the held 3", c)
		}
	})

	t.Run("AG patch: a held weekly window does not mask a lower session target", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		f.event(t, 0.85, 0.70, now.Add(2*time.Hour), now.Add(72*time.Hour)) // 5h: n = 2.5
		seed(t, f, 4, ptr(0.02), nil)
		if c, _ := f.cap(t); c != 3 { // target 2, one step down from 4
			t.Fatalf("cap = %d, want 3", c)
		}
	})

	t.Run("AG patch: the weekly cap bounds a generous session cap (example F)", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		f.event(t, 0.30, 0.70, now.Add(4*time.Hour), now.Add(72*time.Hour))
		seed(t, f, 2, ptr(0.02), ptr(0.002)) // n_5h = 5, n_7d = 1.88, target 1
		if c, _ := f.cap(t); c != 1 {
			t.Fatalf("cap = %d, want 1", c)
		}
	})

	t.Run("AG patch: under the curve the weekly window lets the session cap rise to Max (example E)", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		f.event(t, 0.30, 0.50, now.Add(4*time.Hour), now.Add(72*time.Hour))
		seed(t, f, 4, ptr(0.02), ptr(0.002))
		if c, _ := f.cap(t); c != 5 {
			t.Fatalf("cap = %d, want 5", c)
		}
	})
}

// TestAGPatchPaceSpecAccountScope checks that only the daemon's own account
// drives the cap: another account's windows are tracked but never limit claims.
func TestAGPatchPaceSpecAccountScope(t *testing.T) {
	srv := newAGPaceSpecServer(t)
	f := newAGPaceFixtureAt(t, 4, srv.URL)
	now := f.clock.now()
	other := map[string]string{"CLAUDE_CONFIG_DIR": t.TempDir() + "/backup"}
	obs := agQuotaObs(f.d, Task{ID: "o"}, "claude", other)
	raw := fmt.Sprintf(`{"status":"allowed","rateLimitType":"five_hour","resetsAt":%d,"unifiedWindows":{"five_hour":{"utilization":0.99,"resetsAt":%d},"seven_day":{"utilization":0.99,"resetsAt":%d}}}`,
		now.Add(time.Hour).Unix(), now.Add(time.Hour).Unix(), now.Add(100*time.Hour).Unix())
	obs(json.RawMessage(raw))
	f.clock.set(now.Add(time.Hour / 2))
	obs(json.RawMessage(raw))

	if _, err := f.d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude", "rt-codex"}, 4); err != nil {
		t.Fatal(err)
	}
	calls := srv.recorded()
	if len(calls) != 1 || !slices.Equal(calls[0].RuntimeIDs, []string{"rt-claude", "rt-codex"}) || calls[0].MaxTasks != 4 {
		t.Fatalf("claims = %+v, want one unchanged claim: the other account must not limit the daemon's own", calls)
	}
	if h := f.d.agPaceHealth(); h.EffectiveCap != 4 {
		t.Fatalf("effective_cap = %d, want 4", h.EffectiveCap)
	}
}

// TestAGPatchPaceSpecClaimResults covers the split claim's accounting and error
// handling: the other runtimes get the slots the Claude claim left, a short
// Claude claim frees slots, and failures keep already-claimed tasks.
func TestAGPatchPaceSpecClaimResults(t *testing.T) {
	setup := func(t *testing.T, capNow, max int) (*agPaceFixture, *agPaceSpecServer) {
		t.Helper()
		srv := newAGPaceSpecServer(t)
		f := newAGPaceFixtureAt(t, max, srv.URL)
		now := f.clock.now()
		// Paced zone with the cap pinned by the test: the burn is unknown, so it holds.
		f.event(t, 0.90, 0.10, now.Add(2*time.Hour), now.Add(100*time.Hour))
		f.d.agPace.mu.Lock()
		f.d.agPace.accountLocked(f.acct).limiter = agPaceLimiter{cap: capNow, set: true, changedAt: now}
		f.d.agPace.mu.Unlock()
		return f, srv
	}
	ids := []string{"rt-claude", "rt-codex"}
	claim := func(f *agPaceFixture, ids []string, n int) (claimTasksResult, error) {
		return f.d.agQuotaClaim(context.Background(), "daemon-test", ids, n)
	}

	t.Run("AG patch: slots a short Claude claim leaves go to the other runtimes", func(t *testing.T) {
		f, srv := setup(t, 2, 5)
		srv.tasks = func(c agPaceClaimCall) int {
			if agPaceHasClaude(c.RuntimeIDs) {
				return 1 // asked for 2, only one waiting
			}
			return c.MaxTasks
		}
		res, err := claim(f, ids, 5)
		if err != nil {
			t.Fatal(err)
		}
		calls := srv.recorded()
		if len(calls) != 2 || calls[0].MaxTasks != 2 || calls[1].MaxTasks != 4 {
			t.Fatalf("calls = %+v, want max_tasks 2 then 4 (5 slots less the 1 claimed)", calls)
		}
		if len(res.Tasks) != 5 {
			t.Fatalf("tasks = %d, want 5", len(res.Tasks))
		}
	})

	t.Run("AG patch: claimed Claude tasks count against the headroom of the next claim", func(t *testing.T) {
		f, srv := setup(t, 2, 2)
		srv.tasks = func(c agPaceClaimCall) int { return c.MaxTasks }
		if _, err := claim(f, ids, 3); err != nil {
			t.Fatal(err)
		}
		// cap 2, active 0: room 2 < 3 slots, so Claude takes 2 and the rest go elsewhere.
		calls := srv.recorded()
		if len(calls) != 2 || calls[0].MaxTasks != 2 || calls[1].MaxTasks != 1 {
			t.Fatalf("calls = %+v", calls)
		}
		srv.reset()
		if _, err := claim(f, ids, 2); err != nil { // room 0 now (2 claimed, 2 active)
			t.Fatal(err)
		}
		calls = srv.recorded()
		if len(calls) != 1 || agPaceHasClaude(calls[0].RuntimeIDs) || calls[0].MaxTasks != 2 {
			t.Fatalf("calls with no Claude room = %+v, want only the non-Claude runtimes", calls)
		}
	})

	t.Run("AG patch: only Claude runtimes get a single call limited to the headroom", func(t *testing.T) {
		f, srv := setup(t, 3, 5)
		srv.tasks = func(c agPaceClaimCall) int { return c.MaxTasks }
		res, err := claim(f, []string{"rt-claude", "rt-claude-b"}, 5)
		if err != nil {
			t.Fatal(err)
		}
		calls := srv.recorded()
		if len(calls) != 1 || calls[0].MaxTasks != 3 || len(res.Tasks) != 3 {
			t.Fatalf("calls %+v, tasks %d; want one call for 3", calls, len(res.Tasks))
		}
	})

	t.Run("AG patch: non-Claude runtimes are never paced", func(t *testing.T) {
		f, srv := setup(t, 1, 5)
		done := f.d.agPaceRunStart("running", f.acct, f.clock.now())
		defer done()
		if _, err := claim(f, []string{"rt-codex"}, 5); err != nil {
			t.Fatal(err)
		}
		calls := srv.recorded()
		if len(calls) != 1 || calls[0].MaxTasks != 5 || !slices.Equal(calls[0].RuntimeIDs, []string{"rt-codex"}) {
			t.Fatalf("calls = %+v", calls)
		}
	})

	t.Run("AG patch: an error on the Claude claim is returned and the others are not tried", func(t *testing.T) {
		f, srv := setup(t, 2, 5)
		srv.status = func(int, agPaceClaimCall) int { return http.StatusInternalServerError }
		if _, err := claim(f, ids, 5); err == nil {
			t.Fatal("expected the claim error")
		}
		if calls := srv.recorded(); len(calls) != 1 {
			t.Fatalf("calls = %+v, want only the failed Claude call", calls)
		}
	})

	t.Run("AG patch: an error on the second call keeps the Claude tasks already claimed", func(t *testing.T) {
		f, srv := setup(t, 2, 5)
		srv.tasks = func(c agPaceClaimCall) int {
			if agPaceHasClaude(c.RuntimeIDs) {
				return c.MaxTasks
			}
			return 0
		}
		srv.status = func(idx int, _ agPaceClaimCall) int {
			if idx == 1 {
				return http.StatusInternalServerError
			}
			return http.StatusOK
		}
		res, err := claim(f, ids, 5)
		if err != nil || len(res.Tasks) != 2 {
			t.Fatalf("tasks %d, err %v; want the 2 Claude tasks and no error", len(res.Tasks), err)
		}
	})
}

// TestAGPatchPaceSpecConcurrentUse runs claims, runs starting and ending,
// events, health reads and predecessor reports at once; the race detector is the
// assertion.
func TestAGPatchPaceSpecConcurrentUse(t *testing.T) {
	srv := newAGPaceSpecServer(t)
	srv.tasks = func(c agPaceClaimCall) int { return min(c.MaxTasks, 1) }
	f := newAGPaceFixtureAt(t, 4, srv.URL)
	now := f.clock.now()
	reset5, reset7 := now.Add(2*time.Hour), now.Add(100*time.Hour)
	f.d.AGEnableOverlap(AGPredecessor{PID: 1, Port: 1, TaskIDs: []string{"a"}, MaxConcurrentTasks: 4})

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				_, _ = f.d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude", "rt-codex"}, 4)
			}
		}()
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				f.d.agPaceRunStart(fmt.Sprintf("run-%d-%d", i, j), f.acct, f.clock.now())()
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				f.event(t, 0.85+float64(j)/1000, 0.10, reset5, reset7)
				_ = f.d.agQuotaHealth()
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				n := j % 3
				f.d.agPaceSetPredecessor(&n)
			}
		}(i)
	}
	wg.Wait()
}
