package daemon

// AG patch: Claude quota tracker and auto-hold.
//
// Every headless claude run emits rate_limit_events. The run's observer feeds
// them into per-account state keyed by the run's effective CLAUDE_CONFIG_DIR
// (an agent's own custom_env CLAUDE_CONFIG_DIR is its own account). A
// `rejected` status puts the account on hold until its resetsAt + 2 minutes.
// While the daemon's own (default) account is on hold the batch claim leaves
// out Claude runtimes; Codex, Cursor and the rest keep claiming. Running tasks
// are never touched, and the hold clears itself at reset.
//
// A hold on another account is tracked and shown in `daemon status` but does
// not gate claims: the claim does not know which account a task will use.
//
// Fail policy (every guard fails open unless noted):
//   - no rate_limit_event, no state, no utilization: no hold from them;
//   - garbled event: dropped, counted, warned about at most once per 10 min;
//   - rejected with an unknown or past resetsAt: closed for 1h, reason
//     reset_unknown; the next non-rejected event or `quota clear` ends it;
//   - unreadable or corrupt state file: in-memory state, warned.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

const (
	// agQuotaResetGrace is how long past resetsAt a hold lasts.
	agQuotaResetGrace = 2 * time.Minute
	// agQuotaUnknownResetHold is the hold for a rejection with no usable resetsAt.
	agQuotaUnknownResetHold = time.Hour
	agQuotaWarnInterval     = 10 * time.Minute
	agQuotaWriteInterval    = 2 * time.Second
	// agRetryNotBeforeTTL bounds how long a task's rejected-reset stays in memory.
	agRetryNotBeforeTTL = 24 * time.Hour
)

// agQuotaState is guarded by its own mutex. The zero value is usable.
type agQuotaState struct {
	mu       sync.Mutex
	accounts map[string]*agQuotaAccount

	path      string           // test override of the state file
	now       func() time.Time // test clock
	lastMtime time.Time
	lastSize  int64
	lastWrite time.Time
	dirty     bool

	garbled    int
	warned     map[string]time.Time
	heldLogged bool
}

func (d *Daemon) agQuotaNow() time.Time {
	if d.agQuota.now != nil {
		return d.agQuota.now().UTC()
	}
	return time.Now().UTC()
}

func (d *Daemon) agQuotaLogger() *slog.Logger {
	if d.logger != nil {
		return d.logger
	}
	return slog.Default()
}

// agQuotaWarnLocked logs at most once per agQuotaWarnInterval per key.
func (d *Daemon) agQuotaWarnLocked(key, msg string, args ...any) {
	s := &d.agQuota
	now := d.agQuotaNow()
	if last, ok := s.warned[key]; ok && now.Sub(last) < agQuotaWarnInterval {
		return
	}
	if s.warned == nil {
		s.warned = map[string]time.Time{}
	}
	s.warned[key] = now
	d.agQuotaLogger().Warn(msg, args...)
}

// agQuotaAccountKey is the effective CLAUDE_CONFIG_DIR for a run with env (the
// agent's own env layered over the daemon's), defaulting to ~/.claude.
func agQuotaAccountKey(env map[string]string) string {
	if dir := strings.TrimSpace(env["CLAUDE_CONFIG_DIR"]); dir != "" {
		return filepath.Clean(dir)
	}
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Clean(dir)
	}
	home := strings.TrimSpace(env["HOME"])
	if home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".claude")
}

// agQuotaObserver returns the callback for ExecOptions.AGOnRateLimit, or nil
// for a provider that is not Claude, and done, which ends the run's count for
// pacing (a no-op for a non-Claude run; safe to call twice). It closes over the
// config dir the run actually uses.
func (d *Daemon) agQuotaObserver(task Task, provider string, agentEnv map[string]string) (observe func(json.RawMessage), done func()) {
	if !agQuotaIsClaudeProvider(provider) {
		return nil, func() {}
	}
	account := agQuotaAccountKey(agentEnv)
	done = d.agPaceRunStart(task.ID, account, d.agQuotaNow())
	d.agBackupSlotDone(task.ID) // AG patch: backup account (the run is counted now)
	return func(raw json.RawMessage) {
		ev, err := agent.AGParseRateLimitEvent(raw)
		if err != nil {
			d.agQuotaDropGarbled(err)
			return
		}
		resetsAt, rejected := d.agQuotaObserve(account, ev)
		if rejected {
			d.agBackupNoteRejected(task.ID, account) // AG patch: backup account
		}
		if rejected && task.ID != "" && !resetsAt.IsZero() {
			agRecordRetryNotBefore(task.ID, resetsAt, d.agQuotaNow())
		}
	}, done
}

// agQuotaWire sets the run's rate-limit observer on opts and returns the
// function that ends its pacing count; runTask defers it.
func (d *Daemon) agQuotaWire(opts *agent.ExecOptions, task Task, provider string, agentEnv map[string]string) func() {
	observe, done := d.agQuotaObserver(task, provider, agentEnv)
	opts.AGOnRateLimit = observe
	return done
}

func agQuotaIsClaudeProvider(provider string) bool {
	if provider == "claude" {
		return true
	}
	family, _ := agent.RuntimeProtocolFamily(provider)
	return family == "claude"
}

func (d *Daemon) agQuotaDropGarbled(err error) {
	s := &d.agQuota
	s.mu.Lock()
	defer s.mu.Unlock()
	s.garbled++
	d.agQuotaWarnLocked("garbled", "quota: dropped an unreadable rate_limit_event", "dropped_total", s.garbled, "error", err)
}

// agQuotaObserve folds one event into the account's state. For a rejected
// event it returns the event's own resetsAt (zero when unknown or past) and
// true.
func (d *Daemon) agQuotaObserve(account string, ev agent.AGRateLimitEvent) (rejectedResetsAt time.Time, rejected bool) {
	s := &d.agQuota
	s.mu.Lock()
	defer s.mu.Unlock()
	d.agQuotaRefreshLocked()
	now := d.agQuotaNow()

	if s.accounts == nil {
		s.accounts = map[string]*agQuotaAccount{}
	}
	acct := s.accounts[account]
	if acct == nil {
		acct = &agQuotaAccount{}
		s.accounts[account] = acct
	}
	acct.ObservedAt = now
	acct.Status = ev.Status
	acct.StatusWindow = ev.RateLimitType
	if acct.Windows == nil {
		acct.Windows = map[string]agQuotaWindow{}
	}
	record := func(name string, util *float64, resets time.Time) {
		if name == "" {
			return
		}
		if resets.IsZero() {
			resets = acct.Windows[name].ResetsAt
		}
		acct.Windows[name] = agQuotaWindow{Utilization: util, ResetsAt: resets, ObservedAt: now}
	}
	for name, w := range ev.Windows {
		record(name, w.Utilization, w.ResetsAt)
	}
	if _, listed := ev.Windows[ev.RateLimitType]; !listed {
		record(ev.RateLimitType, ev.Utilization, ev.ResetsAt)
	}
	s.dirty = true

	force := false
	switch ev.Status {
	case "rejected":
		rejected = true
		resets := ev.ResetsAt
		if resets.IsZero() {
			resets = acct.Windows[ev.RateLimitType].ResetsAt
		}
		hold := agQuotaHold{Window: ev.RateLimitType, SetAt: now}
		if resets.After(now) {
			hold.Until = resets.Add(agQuotaResetGrace)
			hold.Reason = agQuotaReasonRejected
			rejectedResetsAt = resets
		} else {
			hold.Until = now.Add(agQuotaUnknownResetHold)
			hold.Reason = agQuotaReasonResetUnknown
		}
		if !hold.SetAt.After(acct.ClearedAt) {
			hold.SetAt = acct.ClearedAt.Add(time.Nanosecond) // a hold is always newer than the clear before it
		}
		cur := acct.activeHold(now)
		if cur == nil || cur.Reason == agQuotaReasonResetUnknown || hold.Until.After(cur.Until) {
			acct.Hold = &hold
			force = true
			d.agQuotaLogger().Info("quota: claude account rejected; holding claims",
				"account", account, "window", hold.Window, "until", hold.Until.Format(time.RFC3339), "reason", hold.Reason)
		}
	case "":
	default:
		// The next event re-evaluates a hold whose reset time was never known.
		if cur := acct.activeHold(now); cur != nil && cur.Reason == agQuotaReasonResetUnknown {
			acct.endHold(now)
			force = true
			d.agQuotaLogger().Info("quota: claude account allowed again; hold with unknown reset released", "account", account)
		}
	}
	d.agPaceObserve(account, acct.Windows, now) // AG patch: pacing
	d.agQuotaFlushLocked(force)
	return rejectedResetsAt, rejected
}

// agQuotaDefaultHold returns the hold on the daemon's own Claude account.
func (d *Daemon) agQuotaDefaultHold() (account string, hold *agQuotaHold) {
	s := &d.agQuota
	s.mu.Lock()
	defer s.mu.Unlock()
	d.agQuotaRefreshLocked()
	d.agQuotaFlushLocked(false)
	account = agQuotaAccountKey(nil)
	now := d.agQuotaNow()
	hold = s.accounts[account].activeHold(now)
	if hold == nil && s.heldLogged {
		s.heldLogged = false
		d.agQuotaLogger().Info("quota: claude hold ended; claiming Claude runtimes again", "account", account)
	} else if hold != nil {
		s.heldLogged = true
	}
	if hold != nil {
		copied := *hold
		hold = &copied
	}
	return account, hold
}

// agQuotaDefaultWindows copies the windows of the daemon's own account.
func (d *Daemon) agQuotaDefaultWindows() map[string]agQuotaWindow {
	s := &d.agQuota
	s.mu.Lock()
	defer s.mu.Unlock()
	d.agQuotaRefreshLocked()
	out := map[string]agQuotaWindow{}
	if acct := s.accounts[agQuotaAccountKey(nil)]; acct != nil {
		for name, w := range acct.Windows {
			out[name] = w
		}
	}
	return out
}

// agQuotaClaim is the pollLoop's batch claim. While Claude is held it leaves
// Claude runtimes out, unless a valid, enabled backup account can take urgent
// issues (ag_quota_backup.go); with none left it returns an empty result without
// calling the server. Otherwise Claude runtimes may claim at most cap - active
// tasks (see ag_quota_pace.go); the rest of the slots go to the other runtimes.
func (d *Daemon) agQuotaClaim(ctx context.Context, daemonID string, runtimeIDs []string, maxTasks int) (claimTasksResult, error) {
	account, hold := d.agQuotaDefaultHold()
	capNow, active := d.agPaceEvaluate(account, d.agQuotaDefaultWindows(), hold != nil, d.cfg.MaxConcurrentTasks, d.agQuotaNow())

	var claude, other []string
	for _, id := range runtimeIDs {
		if d.agQuotaIsClaudeRuntime(id) {
			claude = append(claude, id)
		} else {
			other = append(other, id)
		}
	}
	if res, handled, err := d.agBackupClaim(ctx, daemonID, claude, other, hold != nil, maxTasks); handled { // AG patch: backup account
		return res, err
	}
	room := capNow - active
	switch {
	case len(claude) == 0:
		// nothing to pace
	case hold != nil:
		if len(other) == 0 {
			return claimTasksResult{}, nil
		}
		runtimeIDs = other
	case room >= maxTasks:
		// the cap does not bind this claim
	case room <= 0:
		if len(other) == 0 {
			return claimTasksResult{}, nil
		}
		runtimeIDs = other
	default:
		return d.agQuotaSplitClaim(ctx, daemonID, claude, other, room, maxTasks)
	}
	res, err := d.claimTasksWSFirst(ctx, daemonID, runtimeIDs, maxTasks)
	if err == nil {
		d.agPaceNoteClaimed(res.Tasks)
	}
	return res, err
}

// agQuotaSplitClaim claims up to room tasks from the Claude runtimes, then fills
// the remaining slots from the others, and merges the two results.
func (d *Daemon) agQuotaSplitClaim(ctx context.Context, daemonID string, claude, other []string, room, maxTasks int) (claimTasksResult, error) {
	res, err := d.claimTasksWSFirst(ctx, daemonID, claude, room)
	if err != nil {
		return res, err
	}
	d.agPaceNoteClaimed(res.Tasks)
	rest := maxTasks - len(res.Tasks)
	if len(other) == 0 || rest <= 0 {
		return res, nil
	}
	more, err := d.claimTasksWSFirst(ctx, daemonID, other, rest)
	if err != nil {
		if len(res.Tasks) == 0 {
			return more, err
		}
		// The Claude tasks are already claimed server-side; dispatch them.
		d.agQuotaLogger().Warn("quota: claim for non-Claude runtimes failed after a Claude claim", "error", err)
		return res, nil
	}
	return agQuotaMergeClaims(res, more), nil
}

// agQuotaMergeClaims joins two claim results: tasks concatenated, the transport
// flags ANDed and the earliest deferred-task hint kept.
func agQuotaMergeClaims(a, b claimTasksResult) claimTasksResult {
	a.Tasks = append(a.Tasks, b.Tasks...)
	a.ClaimedOverWS = a.ClaimedOverWS && b.ClaimedOverWS
	a.ClaimPollHintSupported = a.ClaimPollHintSupported && b.ClaimPollHintSupported
	if m := b.NextDeferredTaskAfterMillis; m > 0 && (a.NextDeferredTaskAfterMillis <= 0 || m < a.NextDeferredTaskAfterMillis) {
		a.NextDeferredTaskAfterMillis = m
	}
	return a
}

func (d *Daemon) agQuotaIsClaudeRuntime(id string) bool {
	rt := d.findRuntime(id)
	return rt != nil && agQuotaIsClaudeProvider(rt.Provider)
}

// agQuotaClear overrides every active hold and returns how many it ended.
func (d *Daemon) agQuotaClear() int {
	s := &d.agQuota
	s.mu.Lock()
	d.agQuotaRefreshLocked()
	now := d.agQuotaNow()
	cleared := 0
	for _, acct := range s.accounts {
		if acct.activeHold(now) != nil {
			acct.endHold(now)
			cleared++
		}
	}
	if cleared > 0 {
		s.dirty = true
	}
	d.agQuotaFlushLocked(true)
	s.mu.Unlock()
	if cleared > 0 {
		d.agQuotaLogger().Info("quota: hold cleared by operator", "accounts", cleared)
		d.claimMu.Lock()
		wakeup := d.agDrain.wakeup
		d.claimMu.Unlock()
		if wakeup != nil {
			signalPollerWakeup(wakeup)
		}
	}
	return cleared
}

// agQuotaHealth is embedded in HealthResponse.
type agQuotaHealth struct {
	ClaudeQuota *agQuotaHealthInfo `json:"claude_quota,omitempty"`
	// AGQuota is the pacing block (ag_quota_pace.go); a successor reads
	// active_claude from the predecessor's.
	AGQuota *agQuotaPaceInfo `json:"ag_quota,omitempty"`
}

type agQuotaHealthInfo struct {
	// Held reports the hold that gates claims (the daemon's own account).
	Held     bool                 `json:"held"`
	Hold     *agQuotaHoldInfo     `json:"hold,omitempty"`
	Accounts []agQuotaAccountInfo `json:"accounts"`
	// DroppedEvents counts unreadable rate_limit_events since the daemon started.
	DroppedEvents int `json:"dropped_events,omitempty"`
}

type agQuotaHoldInfo struct {
	Account string `json:"account,omitempty"`
	Window  string `json:"window,omitempty"`
	Until   string `json:"until"`
	Reason  string `json:"reason"`
	// Gating is true only for the daemon's own account; a hold on an agent's
	// own CLAUDE_CONFIG_DIR is tracked but does not stop the batch claim.
	Gating bool `json:"gating"`
}

type agQuotaAccountInfo struct {
	Account      string                       `json:"account"`
	Default      bool                         `json:"default,omitempty"`
	Status       string                       `json:"status,omitempty"`
	StatusWindow string                       `json:"status_window,omitempty"`
	LastSeen     string                       `json:"last_seen,omitempty"`
	Windows      map[string]agQuotaWindowInfo `json:"windows,omitempty"`
	Hold         *agQuotaHoldInfo             `json:"hold,omitempty"`
}

type agQuotaWindowInfo struct {
	Utilization *float64 `json:"utilization,omitempty"`
	ResetsAt    string   `json:"resets_at,omitempty"`
}

func (d *Daemon) agQuotaHealth() agQuotaHealth {
	h := d.agQuotaTrackerHealth()
	h.AGQuota = d.agPaceHealth()
	return h
}

func (d *Daemon) agQuotaTrackerHealth() agQuotaHealth {
	s := &d.agQuota
	s.mu.Lock()
	defer s.mu.Unlock()
	d.agQuotaRefreshLocked()
	if len(s.accounts) == 0 && s.garbled == 0 {
		return agQuotaHealth{}
	}
	now := d.agQuotaNow()
	def := agQuotaAccountKey(nil)
	info := &agQuotaHealthInfo{Accounts: []agQuotaAccountInfo{}, DroppedEvents: s.garbled}
	keys := make([]string, 0, len(s.accounts))
	for key := range s.accounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		acct := s.accounts[key]
		ai := agQuotaAccountInfo{
			Account:      key,
			Default:      key == def,
			Status:       acct.Status,
			StatusWindow: acct.StatusWindow,
			LastSeen:     acct.ObservedAt.Format(time.RFC3339),
		}
		if len(acct.Windows) > 0 {
			ai.Windows = make(map[string]agQuotaWindowInfo, len(acct.Windows))
			for name, w := range acct.Windows {
				wi := agQuotaWindowInfo{Utilization: w.Utilization}
				if !w.ResetsAt.IsZero() {
					wi.ResetsAt = w.ResetsAt.Format(time.RFC3339)
				}
				ai.Windows[name] = wi
			}
		}
		if hold := acct.activeHold(now); hold != nil {
			ai.Hold = &agQuotaHoldInfo{Account: key, Window: hold.Window, Until: hold.Until.Format(time.RFC3339), Reason: hold.Reason, Gating: key == def}
			if key == def {
				info.Held = true
				info.Hold = ai.Hold
			}
		}
		info.Accounts = append(info.Accounts, ai)
	}
	return agQuotaHealth{ClaudeQuota: info}
}

// agQuotaClearHandler serves POST /quota/clear.
func (d *Daemon) agQuotaClearHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		cleared := d.agQuotaClear()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"cleared": cleared})
	}
}

// --- retry hint for the failed-task report -----------------------------------

type agRetryHint struct {
	at     time.Time
	stored time.Time
}

var (
	agRetryHintsMu sync.Mutex
	agRetryHints   = map[string]agRetryHint{}
)

// agRecordRetryNotBefore remembers the resetsAt of the rejected event a task
// saw. The latest one wins. Entries older than a day are pruned.
func agRecordRetryNotBefore(taskID string, at, now time.Time) {
	agRetryHintsMu.Lock()
	defer agRetryHintsMu.Unlock()
	for id, h := range agRetryHints {
		if now.Sub(h.stored) > agRetryNotBeforeTTL {
			delete(agRetryHints, id)
		}
	}
	agRetryHints[taskID] = agRetryHint{at: at, stored: now}
}

// agAddRetryNotBefore adds ag_retry_not_before (RFC 3339) to a fail body when
// the task's run saw a rejection. The entry stays so a replayed report from the
// terminal queue carries it too.
func agAddRetryNotBefore(body map[string]any, taskID string) {
	agRetryHintsMu.Lock()
	h, ok := agRetryHints[taskID]
	agRetryHintsMu.Unlock()
	if ok {
		body["ag_retry_not_before"] = h.at.UTC().Format(time.RFC3339)
	}
}
