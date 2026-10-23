package daemon

// AG patch: Claude pacing over working hours.
//
// MULTICA_AG_PACE_WORK_HOURS (for example "Mon-Fri 08:00-19:00", in the daemon's
// local time zone) makes the weekly window count working time instead of clock
// time: the spending curve is the fraction of the window's working time already
// past, and T is the working time left before the reset. Runs outside working
// hours still spend quota, and the next sample raises the burn and lowers C, so
// the 97% target holds either way. Unset or invalid leaves the weekly window on
// clock time. The 5-hour window always uses clock time.

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const agPaceWorkHoursEnv = "MULTICA_AG_PACE_WORK_HOURS"

// agPaceHours is a weekly schedule: the same start and end on each listed day.
type agPaceHours struct {
	days       [7]bool // indexed by time.Weekday
	start, end time.Duration
	loc        *time.Location
}

var agPaceDayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// agParsePaceHours parses "<days> <HH:MM>-<HH:MM>". Days are a name ("Mon"), a
// range ("Mon-Fri", which may wrap) or a comma list of either.
func agParsePaceHours(s string, loc *time.Location) (*agPaceHours, error) {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return nil, fmt.Errorf("want \"<days> <HH:MM>-<HH:MM>\", got %q", s)
	}
	h := &agPaceHours{loc: loc}
	for _, part := range strings.Split(fields[0], ",") {
		from, to, isRange := strings.Cut(part, "-")
		a, ok := agPaceDayNames[strings.ToLower(from)]
		if !ok {
			return nil, fmt.Errorf("unknown day %q", from)
		}
		b := a
		if isRange {
			if b, ok = agPaceDayNames[strings.ToLower(to)]; !ok {
				return nil, fmt.Errorf("unknown day %q", to)
			}
		}
		for d := a; ; d = (d + 1) % 7 {
			h.days[d] = true
			if d == b {
				break
			}
		}
	}
	from, to, ok := strings.Cut(fields[1], "-")
	if !ok {
		return nil, fmt.Errorf("want a time range HH:MM-HH:MM, got %q", fields[1])
	}
	var err error
	if h.start, err = agParseClock(from); err != nil {
		return nil, err
	}
	if h.end, err = agParseClock(to); err != nil {
		return nil, err
	}
	if h.end <= h.start {
		return nil, fmt.Errorf("end %s is not after start %s", to, from)
	}
	return h, nil
}

func agParseClock(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("bad time %q", s)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// active is the working time inside [from, to).
func (h *agPaceHours) active(from, to time.Time) time.Duration {
	if !to.After(from) {
		return 0
	}
	from, to = from.In(h.loc), to.In(h.loc)
	var total time.Duration
	for day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, h.loc); day.Before(to); day = day.AddDate(0, 0, 1) {
		if !h.days[day.Weekday()] {
			continue
		}
		// Built from the wall clock, so a DST change day keeps 08:00-19:00.
		s := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, h.loc).Add(h.start)
		e := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, h.loc).Add(h.end)
		if s.Before(from) {
			s = from
		}
		if e.After(to) {
			e = to
		}
		if e.After(s) {
			total += e.Sub(s)
		}
	}
	return total
}

// agPaceWork is the weekly window measured in working time.
type agPaceWork struct {
	Elapsed float64       // fraction of the window's working time already past
	Left    time.Duration // working time left before the reset
}

// work measures the weekly window ending at resets; nil when the window holds
// no working time at all.
func (h *agPaceHours) work(resets, now time.Time) *agPaceWork {
	start := resets.Add(-agPaceWeeklyLen)
	total := h.active(start, resets)
	if total <= 0 {
		return nil
	}
	return &agPaceWork{
		Elapsed: float64(h.active(start, now)) / float64(total),
		Left:    h.active(now, resets),
	}
}

// agPaceWorkHours reads MULTICA_AG_PACE_WORK_HOURS once per process.
func (d *Daemon) agPaceWorkHours() *agPaceHours {
	p := &d.agPace
	p.hoursOnce.Do(func() {
		s := strings.TrimSpace(os.Getenv(agPaceWorkHoursEnv))
		if s == "" {
			return
		}
		h, err := agParsePaceHours(s, time.Local)
		if err != nil {
			d.agQuotaLogger().Warn("quota: ignoring "+agPaceWorkHoursEnv+"; weekly pacing stays on clock time", "value", s, "error", err)
			return
		}
		p.hours = h
		d.agQuotaLogger().Info("quota: weekly pacing counts working hours", "hours", s, "zone", time.Local.String())
	})
	return p.hours
}
