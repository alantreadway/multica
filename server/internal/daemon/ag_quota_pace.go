package daemon

// AG patch: Claude pacing.
//
// The quota tracker (ag_quota.go) knows each Claude account's utilisation and
// reset time per window. This file turns that into a cap C on how many Claude
// runs the daemon's own account may have in flight; the batch claim never starts
// more than C - active. The cap only limits new claims and never pre-empts a
// running task. It is read every claim cycle, so no restart is needed.
//
// Per window w (five_hour, seven_day): u utilisation, T hours to reset, L the
// window length, b the burn per run in fraction/hour (an EWMA of du/dt divided
// by the mean number of active runs).
//
//	session: n = Max while u < 0.80, else (0.95 - u) / (T * b)
//	weekly:  c = min(1 - T/L, 0.97); n = Max while u <= c, else (0.97 - u) / (T * b)
//	target = clamp(floor(min(n_5h, n_7d)), 1, Max)
//
// The limiter moves C one step toward the target at most every 10 minutes (the
// first step after a quiet period is immediate). After a window rolls over or a
// hold clears, C jumps to the target. Other windows are tracked by the quota
// state but not paced.
//
// Fail policy: no windows or no state is Max; where the formula applies but the
// burn is unknown the cap holds its position; an expired window constrains
// nothing; the cap never drops below 1 (a lone run is what refreshes
// utilisation). During a handoff overlap the successor adds the predecessor's
// reported Claude runs (its last known value, or all its tasks when it never
// reported one) to its own count.
//
// Deviation from a literal EWMA of du/dt / max(a, 1): a sample with no change in
// utilisation and fewer than 0.05 runs on average is skipped, so idle time does
// not decay the burn estimate toward zero.

import (
	"math"
	"sync"
	"time"
)

const (
	agPaceSessionKnee    = 0.80
	agPaceSessionTarget  = 0.95
	agPaceWeeklyTarget   = 0.97
	agPaceSessionLen     = 5 * time.Hour
	agPaceWeeklyLen      = 168 * time.Hour
	agPaceEWMAAlpha      = 0.3
	agPaceStepInterval   = 10 * time.Minute
	agPaceSampleInterval = 15 * time.Minute
	agPaceMinSample      = time.Second
	agPaceIdleRuns       = 0.05
	// agPaceResetTolerance is how far resetsAt may move and still be the same window.
	agPaceResetTolerance = time.Minute
	// agPaceClaimedTTL is how long a claimed Claude task counts as active before its
	// run starts.
	agPaceClaimedTTL = 10 * time.Minute

	agQuotaWindowFiveHour = "five_hour"
	agQuotaWindowSevenDay = "seven_day"
)

// --- pure maths ---------------------------------------------------------------

// agPaceWin is one window as the maths sees it.
type agPaceWin struct {
	Util      float64
	Remaining time.Duration // time to resetsAt, > 0
	Burn      float64       // fraction/hour per run
	BurnKnown bool
}

// agPaceSessionAllowed is n_5h: how many runs fit. A nil window constrains nothing.
// hold is true when the formula applies but the burn is unknown.
func agPaceSessionAllowed(w *agPaceWin, max int) (n float64, hold bool) {
	if w == nil || w.Util < agPaceSessionKnee {
		return float64(max), false
	}
	return agPaceRuns(agPaceSessionTarget, w, max)
}

// agPaceWeeklyAllowed is n_7d: the linear spending curve is the elapsed fraction
// of the window, capped at the target.
func agPaceWeeklyAllowed(w *agPaceWin, max int) (n float64, hold bool) {
	if w == nil {
		return float64(max), false
	}
	elapsed := 1 - float64(w.Remaining)/float64(agPaceWeeklyLen)
	elapsed = math.Max(0, math.Min(1, elapsed))
	if w.Util <= math.Min(elapsed, agPaceWeeklyTarget) {
		return float64(max), false
	}
	return agPaceRuns(agPaceWeeklyTarget, w, max)
}

func agPaceRuns(target float64, w *agPaceWin, max int) (float64, bool) {
	if !w.BurnKnown {
		return 0, true
	}
	denom := w.Remaining.Hours() * w.Burn
	if denom <= 0 {
		// No burn at all: the target is not reachable by running.
		if w.Util >= target {
			return 0, false
		}
		return float64(max), false
	}
	n := (target - w.Util) / denom
	if math.IsNaN(n) {
		return float64(max), false
	}
	return n, false // the target clamps it
}

// agPaceTarget is clamp(floor(min(n_5h, n_7d)), 1, max).
func agPaceTarget(n5, n7 float64, max int) int {
	t := int(math.Floor(math.Min(n5, n7)))
	if t > max {
		t = max
	}
	if t < 1 {
		t = 1
	}
	return t
}

// agPaceLimiter moves the cap toward the target.
type agPaceLimiter struct {
	cap       int
	set       bool
	changedAt time.Time
}

// step returns the cap for target. jump sets it outright; otherwise it moves one
// step when at least agPaceStepInterval has passed since the last change.
func (l *agPaceLimiter) step(target, max int, now time.Time, jump bool) int {
	if !l.set {
		l.set, l.cap = true, max // no state: Max, with the quiet-period step still due
		l.changedAt = time.Time{}
	}
	if l.cap > max {
		l.cap = max
	}
	if jump {
		if l.cap != target {
			l.cap, l.changedAt = target, now
		}
		return l.cap
	}
	if target == l.cap || (!l.changedAt.IsZero() && now.Sub(l.changedAt) < agPaceStepInterval) {
		return l.cap
	}
	if target > l.cap {
		l.cap++
	} else {
		l.cap--
	}
	l.changedAt = now
	return l.cap
}

// --- state ------------------------------------------------------------------------

type agPaceWindowState struct {
	resets time.Time
	lastU  float64
	lastAt time.Time
	cumAt  float64 // account run-hours at the last sample
	burn   float64
	known  bool
}

type agPaceAccount struct {
	wins    map[string]*agPaceWindowState
	limiter agPaceLimiter
	wasHeld bool
	jump    bool // a window rolled over: the next evaluation sets the cap outright

	// time-weighted active runs: cum is the integral of the count in run-hours.
	cum      float64
	count    int
	countAt  time.Time
	counting bool
}

// agPaceState is guarded by its own mutex; lock order is agQuota.mu then mu.
type agPaceState struct {
	mu       sync.Mutex
	accounts map[string]*agPaceAccount

	nextRun uint64
	running map[uint64]string    // run token -> account
	claimed map[string]time.Time // task id -> claim time, default account assumed

	predActive int  // predecessor's reported Claude runs
	predKnown  bool // false until the predecessor reports
}

func (p *agPaceState) accountLocked(key string) *agPaceAccount {
	if p.accounts == nil {
		p.accounts = map[string]*agPaceAccount{}
	}
	a := p.accounts[key]
	if a == nil {
		a = &agPaceAccount{wins: map[string]*agPaceWindowState{}}
		p.accounts[key] = a
	}
	return a
}

// agPaceActiveLocked is the Claude runs counted against account: running ones on it,
// plus (default account only) claimed tasks whose run has not started and, in an
// overlap, the predecessor's. def is the daemon's own account.
func (d *Daemon) agPaceActiveLocked(account, def string, now time.Time) int {
	p := &d.agPace
	n := 0
	for _, acct := range p.running {
		if acct == account {
			n++
		}
	}
	if account != def {
		return n
	}
	for id, at := range p.claimed {
		if now.Sub(at) > agPaceClaimedTTL {
			delete(p.claimed, id)
			continue
		}
		n++
	}
	if d.agOverlapActive() {
		if p.predKnown {
			n += p.predActive
		} else {
			d.agHandoff.mu.Lock()
			n += len(d.agHandoff.predTasks)
			d.agHandoff.mu.Unlock()
		}
	}
	return n
}

// agPaceAccrueLocked folds the time since the last count change into cum and records
// the current count.
func (d *Daemon) agPaceAccrueLocked(a *agPaceAccount, account string, now time.Time) {
	count := d.agPaceActiveLocked(account, agQuotaAccountKey(nil), now)
	if a.counting && now.After(a.countAt) {
		a.cum += float64(a.count) * now.Sub(a.countAt).Hours()
	}
	a.count, a.countAt, a.counting = count, now, true
}

// sample folds one window observation into its burn estimate.
func (a *agPaceAccount) sample(name string, w agQuotaWindow, now time.Time) {
	st := a.wins[name]
	if w.Utilization == nil || w.ResetsAt.IsZero() {
		return
	}
	if !w.ResetsAt.After(now) {
		return // expired: waiting for the next event to open the new window
	}
	u := *w.Utilization
	if st != nil && w.ResetsAt.Sub(st.resets).Abs() > agPaceResetTolerance {
		a.jump = true // rolled over; the burn estimate carries on
		st.resets, st.lastU, st.lastAt, st.cumAt = w.ResetsAt, u, now, a.cum
		return
	}
	if st == nil {
		a.wins[name] = &agPaceWindowState{resets: w.ResetsAt, lastU: u, lastAt: now, cumAt: a.cum}
		return
	}
	dt := now.Sub(st.lastAt)
	if dt < agPaceMinSample || (u == st.lastU && dt < agPaceSampleInterval) {
		return
	}
	du := u - st.lastU
	abar := (a.cum - st.cumAt) / dt.Hours()
	if du >= 0 && !(du == 0 && abar < agPaceIdleRuns) {
		b := (du / dt.Hours()) / math.Max(abar, 1)
		if st.known {
			b = agPaceEWMAAlpha*b + (1-agPaceEWMAAlpha)*st.burn
		}
		st.burn, st.known = b, true
	}
	st.lastU, st.lastAt, st.cumAt = u, now, a.cum
}

// agPaceObserve samples account's windows (called as events arrive and every claim
// cycle).
func (d *Daemon) agPaceObserve(account string, windows map[string]agQuotaWindow, now time.Time) {
	p := &d.agPace
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accountLocked(account)
	d.agPaceAccrueLocked(a, account, now)
	for _, name := range []string{agQuotaWindowFiveHour, agQuotaWindowSevenDay} {
		if w, ok := windows[name]; ok {
			a.sample(name, w, now)
		}
	}
}

// window turns a window into the maths' input; nil when it constrains
// nothing (no utilisation, no reset, expired).
func (a *agPaceAccount) window(name string, w agQuotaWindow, ok bool, now time.Time) *agPaceWin {
	if !ok || w.Utilization == nil || w.ResetsAt.IsZero() {
		return nil
	}
	if !w.ResetsAt.After(now) {
		if _, had := a.wins[name]; had {
			delete(a.wins, name)
			a.jump = true
		}
		return nil
	}
	out := &agPaceWin{Util: *w.Utilization, Remaining: w.ResetsAt.Sub(now)}
	if st := a.wins[name]; st != nil && st.known {
		out.Burn, out.BurnKnown = st.burn, true
	}
	return out
}

// agPaceEvaluate computes account's cap and active count for this claim cycle.
func (d *Daemon) agPaceEvaluate(account string, windows map[string]agQuotaWindow, held bool, max int, now time.Time) (capNow, active int) {
	if max < 1 {
		max = 1
	}
	p := &d.agPace
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accountLocked(account)
	d.agPaceAccrueLocked(a, account, now)
	active = a.count

	jump := a.wasHeld && !held
	a.wasHeld = held
	if held {
		// Claude claims are at zero; the cap resumes at the target when it clears.
		if a.limiter.set {
			return a.limiter.cap, active
		}
		return max, active
	}
	for _, name := range []string{agQuotaWindowFiveHour, agQuotaWindowSevenDay} {
		if w, ok := windows[name]; ok {
			a.sample(name, w, now)
		}
	}
	w5, ok5 := windows[agQuotaWindowFiveHour]
	w7, ok7 := windows[agQuotaWindowSevenDay]
	in5, in7 := a.window(agQuotaWindowFiveHour, w5, ok5, now), a.window(agQuotaWindowSevenDay, w7, ok7, now)

	cur := max
	if a.limiter.set {
		cur = a.limiter.cap
	}
	n5, hold5 := agPaceSessionAllowed(in5, max)
	n7, hold7 := agPaceWeeklyAllowed(in7, max)
	if hold5 {
		n5 = float64(cur)
	}
	if hold7 {
		n7 = float64(cur)
	}
	target := agPaceTarget(n5, n7, max)
	if in5 == nil && in7 == nil {
		target, jump = max, true // no state: Max
	}
	jump = jump || a.jump
	a.jump = false

	before := cur
	capNow = a.limiter.step(target, max, now, jump)
	if capNow != before {
		d.agQuotaLogger().Info("quota: claude concurrency cap changed",
			"account", account, "from", before, "to", capNow, "target", target, "active", active, "max", max)
	}
	return capNow, active
}

// agPaceRunStart counts a Claude run on account and returns the function that
// ends it. The task's claim entry is replaced by the run.
func (d *Daemon) agPaceRunStart(taskID, account string, now time.Time) func() {
	p := &d.agPace
	p.mu.Lock()
	a := p.accountLocked(account)
	d.agPaceAccrueLocked(a, account, now)
	delete(p.claimed, taskID)
	if p.running == nil {
		p.running = map[uint64]string{}
	}
	p.nextRun++
	token := p.nextRun
	p.running[token] = account
	d.agPaceAccrueLocked(a, account, now)
	p.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			now := d.agQuotaNow()
			a := p.accountLocked(account)
			d.agPaceAccrueLocked(a, account, now)
			delete(p.running, token)
			d.agPaceAccrueLocked(a, account, now)
		})
	}
}

// agPaceNoteClaimed counts freshly claimed Claude tasks on the daemon's own
// account until their runs start.
func (d *Daemon) agPaceNoteClaimed(tasks []*Task) {
	var ids []string
	for _, t := range tasks {
		if t != nil && t.ID != "" && d.agQuotaIsClaudeRuntime(t.RuntimeID) {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	now := d.agQuotaNow()
	p := &d.agPace
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.claimed == nil {
		p.claimed = map[string]time.Time{}
	}
	def := agQuotaAccountKey(nil)
	d.agPaceAccrueLocked(p.accountLocked(def), def, now)
	for _, id := range ids {
		p.claimed[id] = now
	}
	d.agPaceAccrueLocked(p.accountLocked(def), def, now)
}

// agPaceSetPredecessor records the Claude runs the predecessor reported.
func (d *Daemon) agPaceSetPredecessor(activeClaude *int) {
	if activeClaude == nil {
		return
	}
	p := &d.agPace
	p.mu.Lock()
	p.predActive, p.predKnown = *activeClaude, true
	p.mu.Unlock()
}

// --- health ---------------------------------------------------------------------

// agQuotaPaceInfo is /health's ag_quota block.
type agQuotaPaceInfo struct {
	// ActiveClaude counts this process's Claude runs on the daemon's own account
	// (the predecessor's, during an overlap, are its own to report).
	ActiveClaude int `json:"active_claude"`
	// EffectiveCap is C: the most Claude runs the next claim may leave in flight.
	EffectiveCap int `json:"effective_cap"`
}

func (d *Daemon) agPaceHealth() *agQuotaPaceInfo {
	p := &d.agPace
	p.mu.Lock()
	defer p.mu.Unlock()
	def := agQuotaAccountKey(nil)
	now := d.agQuotaNow()
	n := 0
	for _, acct := range p.running {
		if acct == def {
			n++
		}
	}
	for _, at := range p.claimed {
		if now.Sub(at) <= agPaceClaimedTTL {
			n++
		}
	}
	info := &agQuotaPaceInfo{ActiveClaude: n, EffectiveCap: d.cfg.MaxConcurrentTasks}
	if a := p.accounts[def]; a != nil && a.limiter.set {
		info.EffectiveCap = a.limiter.cap
	}
	return info
}
