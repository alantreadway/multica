package handler

// AG patch: Overview dashboard. Pure parts of the Overview: business time, the
// state history behind ageing and the ETA, the ETA sampler, and the work tree
// summary. ag_overview.go feeds them rows. See
// ag-patches/OVERVIEW-DASHBOARD-DESIGN.md, section 2.

import (
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

const (
	// History the p90s and ETA samples come from.
	agOverviewHistoryWindow = 60 * 24 * time.Hour
	// A state needs this many past samples before it has a p90 or feeds the ETA.
	agOverviewMinSamples = 20
	agOverviewETARuns    = 1000
	// Units created this recently count as scope still arriving.
	agOverviewRecentUnitDays = 3
	// Past waves smaller than this say nothing about growth.
	agOverviewMinGrowthWaveIssues = 3
)

// Statuses that end a unit's work. A cancelled unit leaves the known scope.
func agOverviewClosed(status string) bool { return status == "done" || status == "cancelled" }

// --- Business time ----------------------------------------------------------
// Weekends (Saturday and Sunday, UTC) do not count: almost nothing moves then.

func agOverviewWeekend(t time.Time) bool {
	d := t.UTC().Weekday()
	return d == time.Saturday || d == time.Sunday
}

func agOverviewNextMidnight(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
}

// agOverviewBusinessDuration is the weekday time between a and b.
func agOverviewBusinessDuration(a, b time.Time) time.Duration {
	if !b.After(a) {
		return 0
	}
	var total time.Duration
	for t := a.UTC(); t.Before(b); {
		next := agOverviewNextMidnight(t)
		if next.After(b) {
			next = b
		}
		if !agOverviewWeekend(t) {
			total += next.Sub(t)
		}
		t = next
	}
	return total
}

// agOverviewAddBusiness returns the time d of weekday time after t.
func agOverviewAddBusiness(t time.Time, d time.Duration) time.Time {
	t = t.UTC()
	for {
		for agOverviewWeekend(t) {
			t = agOverviewNextMidnight(t)
		}
		next := agOverviewNextMidnight(t)
		if room := next.Sub(t); d <= room {
			return t.Add(d)
		} else {
			d -= room
		}
		t = next
	}
}

// --- State history ----------------------------------------------------------

type agOverviewTransition struct {
	IssueID        string
	IssueCreatedAt time.Time
	At             time.Time
	From, To       string
}

type agOverviewHistory struct {
	// Wall-clock p90 of one stay in each state, for states with enough samples.
	StateP90 map[string]time.Duration
	// Weekday time from entering a state to done, for units done in the window.
	ToDone map[string][]time.Duration
}

type agOverviewEntry struct {
	state string
	at    time.Time
}

// agOverviewBuildHistory reads transitions sorted by issue, then time. A unit's
// first transition also says which state it was created in.
func agOverviewBuildHistory(transitions []agOverviewTransition, now time.Time) agOverviewHistory {
	since := now.Add(-agOverviewHistoryWindow)
	stays := map[string][]time.Duration{}
	toDone := map[string][]time.Duration{}

	flush := func(entries []agOverviewEntry) {
		for k := 0; k+1 < len(entries); k++ {
			e, next := entries[k], entries[k+1]
			if agOverviewClosed(e.state) || next.at.Before(since) {
				continue
			}
			stays[e.state] = append(stays[e.state], next.at.Sub(e.at))
		}
		last := entries[len(entries)-1]
		if last.state != "done" || last.at.Before(since) {
			return
		}
		for _, e := range entries[:len(entries)-1] {
			if !agOverviewClosed(e.state) {
				toDone[e.state] = append(toDone[e.state], agOverviewBusinessDuration(e.at, last.at))
			}
		}
	}

	var entries []agOverviewEntry
	for i, tr := range transitions {
		if i == 0 || transitions[i-1].IssueID != tr.IssueID {
			if len(entries) > 0 {
				flush(entries)
			}
			entries = entries[:0]
			if tr.From != "" && !tr.IssueCreatedAt.IsZero() && !tr.IssueCreatedAt.After(tr.At) {
				entries = append(entries, agOverviewEntry{tr.From, tr.IssueCreatedAt})
			}
		}
		entries = append(entries, agOverviewEntry{tr.To, tr.At})
	}
	if len(entries) > 0 {
		flush(entries)
	}

	p90 := map[string]time.Duration{}
	for state, ds := range stays {
		if len(ds) >= agOverviewMinSamples {
			p90[state] = agOverviewPercentile(ds, 0.9)
		}
	}
	return agOverviewHistory{StateP90: p90, ToDone: toDone}
}

// agOverviewPercentile is the nearest-rank percentile; it sorts ds.
func agOverviewPercentile(ds []time.Duration, p float64) time.Duration {
	slices.Sort(ds)
	i := int(math.Ceil(p*float64(len(ds)))) - 1
	return ds[max(0, min(i, len(ds)-1))]
}

// --- ETA --------------------------------------------------------------------

type agOverviewOpenUnit struct {
	State string
	// Weekday time already spent in State.
	InState time.Duration
}

type agOverviewETA struct {
	P50, P85 time.Time
	// The fewest samples behind any open unit's draw.
	MinSamples int
}

const (
	agOverviewETANoOpenUnits = "no_open_units"
	agOverviewETAFewSamples  = "few_samples"
)

// agOverviewEstimate runs the per-unit stage method over the known open units:
// each run draws every unit's time to done from units that were in the same
// state and had been there at least as long, and the tree finishes with its
// latest unit. It returns a reason instead when the ETA must stay hidden.
func agOverviewEstimate(units []agOverviewOpenUnit, toDone map[string][]time.Duration, now time.Time, rng *rand.Rand) (*agOverviewETA, string) {
	if len(units) == 0 {
		return nil, agOverviewETANoOpenUnits
	}
	pools := make([][]time.Duration, len(units))
	minSamples := math.MaxInt
	for i, u := range units {
		samples := toDone[u.State]
		if len(samples) < agOverviewMinSamples {
			return nil, agOverviewETAFewSamples
		}
		minSamples = min(minSamples, len(samples))
		var longer []time.Duration
		for _, s := range samples {
			if s > u.InState {
				longer = append(longer, s-u.InState)
			}
		}
		// Past every unit we have seen: the history cannot say, so draw as if the
		// state had only just started.
		if len(longer) == 0 {
			longer = samples
		}
		pools[i] = longer
	}
	remaining := make([]time.Duration, agOverviewETARuns)
	for r := range remaining {
		var latest time.Duration
		for _, pool := range pools {
			latest = max(latest, pool[rng.IntN(len(pool))])
		}
		remaining[r] = latest
	}
	return &agOverviewETA{
		P50:        agOverviewAddBusiness(now, agOverviewPercentile(remaining, 0.5)),
		P85:        agOverviewAddBusiness(now, agOverviewPercentile(remaining, 0.85)),
		MinSamples: minSamples,
	}, ""
}
