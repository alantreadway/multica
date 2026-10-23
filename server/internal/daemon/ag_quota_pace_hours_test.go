package daemon

// AG patch: Claude pacing over working hours; TestAGPatch* with "AG patch:"
// subtests, as counted by ag-patches/test.sh.

import (
	"testing"
	"time"
)

func agMustPaceHours(t *testing.T, s string, loc *time.Location) *agPaceHours {
	t.Helper()
	h, err := agParsePaceHours(s, loc)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return h
}

func TestAGPatchPaceWorkHours(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skipf("no tz data: %v", err)
	}
	hours := func(h float64) time.Duration { return time.Duration(h * float64(time.Hour)) }

	t.Run("AG patch: the schedule parses day ranges, wrapped ranges and lists", func(t *testing.T) {
		for s, want := range map[string][7]bool{
			"Mon-Fri 08:00-19:00": {false, true, true, true, true, true, false},
			"fri-mon 09:00-17:30": {true, true, false, false, false, true, true},
			"Mon,Wed 09:00-17:00": {false, true, false, true, false, false, false},
			"Sat 10:00-12:00":     {false, false, false, false, false, false, true},
		} {
			if got := agMustPaceHours(t, s, time.UTC).days; got != want {
				t.Errorf("%q days = %v, want %v", s, got, want)
			}
		}
		for _, bad := range []string{"", "Mon-Fri", "Mon-Fri 8-19", "Mon-Fry 08:00-19:00", "Mon-Fri 19:00-08:00", "Mon-Fri 08:00", "Mon-Fri 08:00-19:00 extra"} {
			if _, err := agParsePaceHours(bad, time.UTC); err == nil {
				t.Errorf("%q parsed, want an error", bad)
			}
		}
	})

	t.Run("AG patch: working time counts only the listed days and hours, across a DST change", func(t *testing.T) {
		h := agMustPaceHours(t, "Mon-Fri 08:00-19:00", london)
		start := time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC) // Sat
		if got := h.active(start, start.Add(agPaceWeeklyLen)); got != hours(55) {
			t.Fatalf("one week = %v, want 55h", got)
		}
		// Sunday 25 October 2026 is 25 hours long in London; every day keeps 08:00-19:00.
		all := agMustPaceHours(t, "Mon-Sun 08:00-19:00", london)
		day := time.Date(2026, 10, 25, 0, 0, 0, 0, london)
		if got := all.active(day, day.AddDate(0, 0, 1)); got != hours(11) {
			t.Fatalf("DST day = %v, want 11h", got)
		}
		mid := time.Date(2026, 11, 6, 14, 15, 0, 0, time.UTC) // Fri 14:15 GMT
		if got := h.active(mid, mid.Add(10*time.Hour)); got != hours(4.75) {
			t.Fatalf("Fri afternoon = %v, want 4h45m", got)
		}
		if got := h.active(mid, mid); got != 0 {
			t.Fatalf("empty range = %v", got)
		}
	})

	// Friday 6 November 2026, 14:15 GMT; the weekly window resets Saturday 06:00.
	now := time.Date(2026, 11, 6, 14, 15, 0, 0, time.UTC)
	reset := time.Date(2026, 11, 7, 6, 0, 0, 0, time.UTC)
	work := agMustPaceHours(t, "Mon-Fri 08:00-19:00", london)
	weekly := func(u, burn float64, h *agPaceHours, at time.Time) float64 {
		w := &agPaceWin{Util: u, Remaining: reset.Sub(at), Burn: burn, BurnKnown: true}
		if h != nil {
			w.Work = h.work(reset, at)
		}
		n, _ := agPaceWeeklyAllowed(w, 5)
		return n
	}

	t.Run("AG patch: 91% used with a working afternoon left runs at Max, where clock time paced it to 1", func(t *testing.T) {
		if got := agPaceTarget(5, weekly(0.91, 0.005, nil, now), 5); got != 1 {
			t.Fatalf("clock time target = %d, want 1 (the behaviour this replaces)", got)
		}
		if got := weekly(0.91, 0.005, work, now); got != 5 {
			t.Fatalf("working-hours n_7d = %v, want Max", got)
		}
	})

	t.Run("AG patch: over the working-time curve the cap still follows the burn over the hours left", func(t *testing.T) {
		// 0.95 is over the curve (50.25/55 = 0.914): n = (0.97 - 0.95) / (4.75h * b).
		if got := weekly(0.95, 0.002, work, now); !agPaceApprox(got, 0.02/(4.75*0.002)) {
			t.Fatalf("n_7d = %v, want %v", got, 0.02/(4.75*0.002))
		}
		if got := agPaceTarget(5, weekly(0.95, 0.01, work, now), 5); got != 1 {
			t.Fatalf("a burn that would breach 97%% in the hours left: target = %d, want 1", got)
		}
	})

	t.Run("AG patch: after the last working hour the window runs at Max up to 97%, then drops to 1", func(t *testing.T) {
		evening := time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC)
		if w := work.work(reset, evening); w.Left != 0 || w.Elapsed != 1 {
			t.Fatalf("Friday evening = %+v, want no time left and the curve at its end", w)
		}
		if got := weekly(0.96, 0.01, work, evening); got != 5 {
			t.Fatalf("96%% after hours = %v, want Max", got)
		}
		if got := agPaceTarget(5, weekly(0.98, 0.01, work, evening), 5); got != 1 {
			t.Fatalf("98%% after hours target = %d, want 1", got)
		}
	})

	t.Run("AG patch: a schedule with no working time in the window leaves it on clock time", func(t *testing.T) {
		none := &agPaceHours{loc: london} // no days
		if w := none.work(reset, now); w != nil {
			t.Fatalf("no working days = %+v, want nil", w)
		}
	})

	t.Run("AG patch: the daemon reads the schedule from the environment and paces with it", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		f.clock.t = now
		f.d.agPace.hoursOnce.Do(func() { f.d.agPace.hours = agMustPaceHours(t, "Mon-Fri 08:00-19:00", time.UTC) })
		f.event(t, 0.30, 0.91, now.Add(3*time.Hour), reset)
		if c, _ := f.cap(t); c != 5 {
			t.Fatalf("cap with working hours = %d, want 5", c)
		}

		g := newAGPaceFixture(t, 5)
		g.clock.t = now
		t.Setenv(agPaceWorkHoursEnv, "")
		g.event(t, 0.30, 0.91, now.Add(3*time.Hour), reset)
		if c, _ := g.cap(t); c != 1 {
			t.Fatalf("cap on clock time, burn unknown = %d, want the floor 1", c)
		}

		t.Setenv(agPaceWorkHoursEnv, "Mon-Fri 08:00-19:00")
		if h := newAGPaceFixture(t, 5).d.agPaceWorkHours(); h == nil || !h.days[time.Friday] || h.days[time.Saturday] {
			t.Fatalf("parsed from env = %+v", h)
		}
		t.Setenv(agPaceWorkHoursEnv, "weekdays")
		if h := newAGPaceFixture(t, 5).d.agPaceWorkHours(); h != nil {
			t.Fatalf("invalid value parsed = %+v, want nil (clock time)", h)
		}
	})
}
