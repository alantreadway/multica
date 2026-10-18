package daemon

// AG patch: backup Claude account failover (urgent issues only).
//
// Claude Code picks its account from CLAUDE_CONFIG_DIR. ag-patches/claude-backup-setup.sh
// builds a second config dir (default ~/.claude-backup, MULTICA_AG_CLAUDE_BACKUP_DIR
// overrides it) with its own login that shares settings, skills and projects/ with
// the primary. While the daemon's own account is on hold (ag_quota.go) and the
// backup is valid and enabled from the runtime page:
//
//   - the batch claim still includes the Claude runtimes, but asks the server for
//     urgent issues only (ag_min_issue_priority) and at most
//     min(pacing room, backup_max_concurrent - active on backup) tasks;
//   - an urgent task starting up gets CLAUDE_CONFIG_DIR = backup (agApplyClaudeAccount);
//   - an urgent run that is rejected on the primary mid-task resumes in place on
//     the backup with --resume <its session> (agQuotaResumeOnBackup).
//
// The hold ends on its own at the primary's reset and everything goes back.
//
// Detection spends no quota: `claude auth status --json` against each dir, at
// start and every 30 minutes. Valid = logged in with claude.ai, a subscription,
// and a different email from the primary.
//
// Fail policy (every guard fails closed for failover):
//   - the backup check fails: valid=false, reason check_failed, retried next interval;
//   - an ack without ag_settings (or none for 10 minutes): backup disabled;
//   - an ack without ag_capabilities claim_filter: no Claude claims on the backup;
//   - the legacy per-runtime claim cannot filter, so it is never used for Claude
//     runtimes on the backup;
//   - a resume the backup rejects is a normal quota failure: no fresh-session
//     retry, because tools already ran.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	agBackupDirEnv        = "MULTICA_AG_CLAUDE_BACKUP_DIR"
	agBackupDirName       = ".claude-backup"
	agBackupCheckInterval = 30 * time.Minute
	// agBackupStaleAfter is how long a check result is trusted if the loop stops.
	agBackupStaleAfter = 2*agBackupCheckInterval + 10*time.Second

	agBackupCapClaimFilter = "claim_filter"
	agBackupUrgent         = "urgent"
	agBackupResumePrompt   = "You hit a usage limit and have been moved to another account; continue where you left off"

	agBackupReasonNotDetected  = "not_detected"
	agBackupReasonNotLoggedIn  = "not_logged_in"
	agBackupReasonSameAccount  = "same_account"
	agBackupReasonCheckFailed  = "check_failed"
	agQuotaAccountBackup       = "backup"
	agBackupRejectedTTL        = 24 * time.Hour
	agBackupAuthMethodClaudeAI = "claude.ai"
)

// agBackupCheckTimeout bounds one `claude auth status` call; a variable for tests.
var agBackupCheckTimeout = 10 * time.Second

// agBackupStatus is the result of one detection pass.
type agBackupStatus struct {
	Dir       string
	Detected  bool
	Valid     bool
	Reason    string
	CheckedAt time.Time
}

type agBackupRejection struct {
	account string
	at      time.Time
}

// agBackupState is guarded by its own mutex. Lock order: agBackup.mu, then agPace.mu.
type agBackupState struct {
	mu       sync.Mutex
	status   agBackupStatus
	checked  bool
	capOK    bool
	capSeen  time.Time
	rejected map[string]agBackupRejection // task id -> primary account it was rejected on
	resuming int                          // in-place resumes started and not yet finished
}

// agBackupDir is the backup config dir: MULTICA_AG_CLAUDE_BACKUP_DIR, else ~/.claude-backup.
func agBackupDir() string {
	home, _ := os.UserHomeDir()
	if v := strings.TrimSpace(os.Getenv(agBackupDirEnv)); v != "" {
		if v == "~" || strings.HasPrefix(v, "~/") {
			v = filepath.Join(home, strings.TrimPrefix(v, "~"))
		}
		return filepath.Clean(v)
	}
	return filepath.Join(home, agBackupDirName)
}

// --- detection ----------------------------------------------------------------------

// agAuthStatus is the part of `claude auth status --json` detection reads.
type agAuthStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	Email            string `json:"email"`
	SubscriptionType string `json:"subscriptionType"`
}

// agBackupAuthStatus runs `claude auth status --json` against dir. The command
// exits non-zero when logged out but still prints the JSON, so the output is
// parsed whatever the exit code.
func (d *Daemon) agBackupAuthStatus(ctx context.Context, dir string) (agAuthStatus, error) {
	entry, ok := d.agents()["claude"]
	if !ok || entry.Path == "" {
		return agAuthStatus{}, errors.New("claude CLI not available")
	}
	ctx, cancel := context.WithTimeout(ctx, agBackupCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, entry.Path, "auth", "status", "--json")
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+dir)
	out, runErr := cmd.Output()
	var st agAuthStatus
	if err := json.Unmarshal(out, &st); err != nil {
		if runErr != nil {
			return agAuthStatus{}, runErr
		}
		return agAuthStatus{}, err
	}
	if ctx.Err() != nil {
		return agAuthStatus{}, ctx.Err()
	}
	return st, nil
}

// agBackupCheck runs one detection pass and stores the result.
func (d *Daemon) agBackupCheck(ctx context.Context) {
	dir := agBackupDir()
	st := agBackupStatus{Dir: dir}
	st.Detected, st.Valid, st.Reason = d.agBackupEvaluate(ctx, dir)
	st.CheckedAt = d.agQuotaNow()

	b := &d.agBackup
	b.mu.Lock()
	prev, hadPrev := b.status, b.checked
	b.status, b.checked = st, true
	b.mu.Unlock()
	if !hadPrev || prev.Valid != st.Valid || prev.Reason != st.Reason || prev.Detected != st.Detected {
		d.agQuotaLogger().Info("quota: backup claude account checked",
			"dir", dir, "detected", st.Detected, "valid", st.Valid, "reason", st.Reason)
	}
}

func (d *Daemon) agBackupEvaluate(ctx context.Context, dir string) (detected, valid bool, reason string) {
	fi, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, false, agBackupReasonNotDetected
	case err != nil:
		return false, false, agBackupReasonCheckFailed
	case !fi.IsDir():
		return false, false, agBackupReasonNotDetected
	}
	primary := agQuotaAccountKey(nil)
	if filepath.Clean(dir) == filepath.Clean(primary) {
		return true, false, agBackupReasonSameAccount
	}
	backup, err := d.agBackupAuthStatus(ctx, dir)
	if err != nil {
		return true, false, agBackupReasonCheckFailed
	}
	if !backup.LoggedIn || backup.AuthMethod != agBackupAuthMethodClaudeAI || backup.SubscriptionType == "" || backup.Email == "" {
		return true, false, agBackupReasonNotLoggedIn
	}
	main, err := d.agBackupAuthStatus(ctx, primary)
	if err != nil || main.Email == "" {
		return true, false, agBackupReasonCheckFailed
	}
	if strings.EqualFold(strings.TrimSpace(main.Email), strings.TrimSpace(backup.Email)) {
		return true, false, agBackupReasonSameAccount
	}
	return true, true, ""
}

// agBackupLoop checks the backup at start and every agBackupCheckInterval. It does
// nothing at all while the backup dir does not exist, so a daemon that never set
// up a backup never runs the CLI for it.
func (d *Daemon) agBackupLoop(ctx context.Context) {
	d.agBackupCheck(ctx)
	t := time.NewTicker(agBackupCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.agBackupCheck(ctx)
		}
	}
}

// agBackupStatusNow is the last check result; a result older than agBackupStaleAfter
// (the loop stopped) or none at all is an invalid backup.
func (d *Daemon) agBackupStatusNow() agBackupStatus {
	b := &d.agBackup
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.status
	if !b.checked {
		return agBackupStatus{Dir: agBackupDir()}
	}
	if d.agQuotaNow().Sub(st.CheckedAt) > agBackupStaleAfter {
		st.Valid, st.Reason = false, agBackupReasonCheckFailed
	}
	return st
}

// --- ack: setting and capability ---------------------------------------------------------

// agApplyAck takes what a received heartbeat ack carries: the runtime's backup
// setting and the server's capabilities. Both fail closed when absent.
func (d *Daemon) agApplyAck(runtimeID string, s *protocol.AGRuntimeSettings, capabilities []string) {
	d.agApplySettings(runtimeID, s)
	ok := false
	for _, c := range capabilities {
		if c == agBackupCapClaimFilter {
			ok = true
		}
	}
	b := &d.agBackup
	b.mu.Lock()
	b.capOK, b.capSeen = ok, d.agQuotaNow()
	b.mu.Unlock()
}

func (d *Daemon) agBackupCapability() bool {
	b := &d.agBackup
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.capOK && d.agQuotaNow().Sub(b.capSeen) < agSettingsTTL
}

// agBackupUsable reports whether the backup may be used for runtimeID: enabled
// from the runtime page, valid, and the server filters claims.
func (d *Daemon) agBackupUsable(runtimeID string) bool {
	if !d.agBackupSetting(runtimeID).BackupEnabled {
		return false
	}
	return d.agBackupStatusNow().Valid && d.agBackupCapability()
}

// agBackupOn reports whether new urgent Claude runs for runtimeID go to the backup:
// it is usable and the daemon's own account is on hold.
func (d *Daemon) agBackupOn(runtimeID string) bool {
	if !d.agBackupUsable(runtimeID) {
		return false
	}
	_, hold := d.agQuotaDefaultHold()
	return hold != nil
}

// --- tracker helpers for the backup account ---------------------------------------------

func (d *Daemon) agQuotaAccountHold(account string) *agQuotaHold {
	s := &d.agQuota
	s.mu.Lock()
	defer s.mu.Unlock()
	d.agQuotaRefreshLocked()
	hold := s.accounts[account].activeHold(d.agQuotaNow())
	if hold == nil {
		return nil
	}
	copied := *hold
	return &copied
}

func (d *Daemon) agQuotaAccountWindows(account string) map[string]agQuotaWindow {
	s := &d.agQuota
	s.mu.Lock()
	defer s.mu.Unlock()
	d.agQuotaRefreshLocked()
	out := map[string]agQuotaWindow{}
	if acct := s.accounts[account]; acct != nil {
		for name, w := range acct.Windows {
			out[name] = w
		}
	}
	return out
}

// agBackupPendingClaims counts Claude tasks claimed and not yet running; while on
// the backup they are assumed to land there.
func (d *Daemon) agBackupPendingClaims() int {
	p := &d.agPace
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.claimed)
}

func (d *Daemon) agBackupRunning(account string) int {
	p := &d.agPace
	p.mu.Lock()
	defer p.mu.Unlock()
	return d.agPaceActiveLocked(account, agQuotaAccountKey(nil))
}

// --- report -----------------------------------------------------------------------------

// agBackupFillReport adds the backup block and windows to a heartbeat report.
// While Claude runs are failing over, account is "backup" and the cap and max are
// the backup's; windows and hold stay the primary's, and backup_windows are the
// backup's own.
func (d *Daemon) agBackupFillReport(r *protocol.DaemonQuotaReport, runtimeID string, primaryHeld bool) {
	st := d.agBackupStatusNow()
	r.Backup.Detected, r.Backup.Valid, r.Backup.Reason = st.Detected, st.Valid, st.Reason
	if !st.Detected {
		return
	}
	for name, w := range d.agQuotaAccountWindows(st.Dir) {
		if name == agQuotaWindowFiveHour || name == agQuotaWindowSevenDay {
			r.BackupWindows[name] = agQuotaReportWindow(w)
		}
	}
	if !primaryHeld || !d.agBackupUsable(runtimeID) {
		return
	}
	r.Account = agQuotaAccountBackup
	r.Max = r.Backup.MaxConcurrent
	r.EffectiveCap = r.Max
	if d.agQuotaAccountHold(st.Dir) != nil {
		r.EffectiveCap = 0
		return
	}
	p := &d.agPace
	p.mu.Lock()
	if a := p.accounts[st.Dir]; a != nil && a.limiter.set && a.limiter.cap < r.EffectiveCap {
		r.EffectiveCap = a.limiter.cap
	}
	p.mu.Unlock()
}

// --- claim -------------------------------------------------------------------------------

type agClaimFilterKey struct{}

// agWithMinPriority marks ctx so the batch claim asks the server for issues of at
// least the given priority on those runtimes.
func agWithMinPriority(ctx context.Context, byRuntime map[string]string) context.Context {
	return context.WithValue(ctx, agClaimFilterKey{}, byRuntime)
}

func agClaimFilter(ctx context.Context) map[string]string {
	m, _ := ctx.Value(agClaimFilterKey{}).(map[string]string)
	return m
}

// agClaimBody adds ag_min_issue_priority to a batch claim body when ctx carries a
// filter, for the runtimes the request names.
func agClaimBody(ctx context.Context, body map[string]any) map[string]any {
	filter := agClaimFilter(ctx)
	if len(filter) == 0 {
		return body
	}
	ids, _ := body["runtime_ids"].([]string)
	out := map[string]string{}
	for _, id := range ids {
		if p, ok := filter[id]; ok {
			out[id] = p
		}
	}
	if len(out) > 0 {
		body["ag_min_issue_priority"] = out
	}
	return body
}

// agLegacyClaimIDs drops the runtimes a filter applies to: the per-runtime claim
// endpoint cannot filter, so it must not serve them.
func agLegacyClaimIDs(ctx context.Context, runtimeIDs []string) []string {
	filter := agClaimFilter(ctx)
	if len(filter) == 0 {
		return runtimeIDs
	}
	out := make([]string, 0, len(runtimeIDs))
	for _, id := range runtimeIDs {
		if _, filtered := filter[id]; !filtered {
			out = append(out, id)
		}
	}
	return out
}

// agBackupClaim handles the batch claim while the primary is held. handled is false
// when the backup cannot serve (the caller then leaves Claude runtimes out, as it
// does for any hold). Otherwise the eligible Claude runtimes claim urgent issues
// only, up to the backup's room, and the other runtimes fill the remaining slots.
func (d *Daemon) agBackupClaim(ctx context.Context, daemonID string, claude, other []string, primaryHeld bool, maxTasks int) (claimTasksResult, bool, error) {
	if !primaryHeld || len(claude) == 0 || d.batchClaimUnsupported.Load() {
		return claimTasksResult{}, false, nil
	}
	var eligible []string
	limit := 0
	for _, id := range claude {
		if !d.agBackupUsable(id) {
			continue
		}
		eligible = append(eligible, id)
		if m := d.agBackupSetting(id).BackupMaxConcurrent; limit == 0 || m < limit {
			limit = m
		}
	}
	st := d.agBackupStatusNow()
	if len(eligible) == 0 || d.agQuotaAccountHold(st.Dir) != nil {
		return claimTasksResult{}, false, nil
	}
	capNow, active := d.agPaceEvaluate(st.Dir, d.agQuotaAccountWindows(st.Dir), false, limit, d.agQuotaNow())
	room := capNow - active - d.agBackupPendingClaims()
	if room > maxTasks {
		room = maxTasks
	}
	if room <= 0 {
		if len(other) == 0 {
			return claimTasksResult{}, true, nil
		}
		res, err := d.claimTasksWSFirst(ctx, daemonID, other, maxTasks)
		return res, true, err
	}
	filter := make(map[string]string, len(eligible))
	for _, id := range eligible {
		filter[id] = agBackupUrgent
	}
	res, err := d.agQuotaSplitClaim(agWithMinPriority(ctx, filter), daemonID, eligible, other, room, maxTasks)
	return res, true, err
}

// --- environment swap --------------------------------------------------------------------

// agApplyClaudeAccount points an urgent Claude task at the backup config dir while
// the primary is on hold. An agent's own CLAUDE_CONFIG_DIR is left alone, and so
// is any task that is not urgent (one claimed just before the hold started).
func (d *Daemon) agApplyClaudeAccount(provider string, task Task, agentEnv map[string]string) {
	if !agQuotaIsClaudeProvider(provider) || task.AGIssuePriority != agBackupUrgent {
		return
	}
	if agQuotaAccountKey(agentEnv) != agQuotaAccountKey(nil) {
		return
	}
	if !d.agBackupOn(task.RuntimeID) {
		return
	}
	dir := d.agBackupStatusNow().Dir
	agentEnv["CLAUDE_CONFIG_DIR"] = dir
	d.agQuotaLogger().Info("quota: urgent claude task runs on the backup account", "task_id", task.ID, "dir", dir)
}

// --- in-place resume ---------------------------------------------------------------------

// agBackupNoteRejected remembers that a run saw `rejected` on the daemon's own
// account, so the resume hook can tell it apart from a failure for other reasons.
func (d *Daemon) agBackupNoteRejected(taskID, account string) {
	if taskID == "" || account != agQuotaAccountKey(nil) {
		return
	}
	now := d.agQuotaNow()
	b := &d.agBackup
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rejected == nil {
		b.rejected = map[string]agBackupRejection{}
	}
	for id, r := range b.rejected {
		if now.Sub(r.at) > agBackupRejectedTTL {
			delete(b.rejected, id)
		}
	}
	b.rejected[taskID] = agBackupRejection{account: account, at: now}
}

func (d *Daemon) agBackupTakeRejected(taskID string) bool {
	b := &d.agBackup
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.rejected[taskID]
	delete(b.rejected, taskID)
	return ok
}

// agBackupReserveResume takes one of the backup's concurrency slots for a resume.
func (d *Daemon) agBackupReserveResume(dir string, limit int) (release func(), ok bool) {
	b := &d.agBackup
	b.mu.Lock()
	defer b.mu.Unlock()
	if d.agBackupRunning(dir)+b.resuming >= limit {
		return nil, false
	}
	b.resuming++
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.resuming--
			b.mu.Unlock()
		})
	}, true
}

// agQuotaResumeOnBackup continues an urgent run that was rejected on the primary
// account: same session, backup config dir, within backup_max_concurrent. It
// returns the first result untouched when any condition fails or the backup
// rejects the resume (that is then a normal quota failure; no fresh-session retry,
// because tools already ran).
func (d *Daemon) agQuotaResumeOnBackup(ctx context.Context, backend agent.Backend, task Task, provider string, agentEnv map[string]string,
	opts agent.ExecOptions, taskLog *slog.Logger, codexHome string, msgSeq *atomic.Int32, result agent.Result, tools int32) (agent.Result, int32) {
	sawRejected := d.agBackupTakeRejected(task.ID)
	if !sawRejected || !agQuotaIsClaudeProvider(provider) || task.AGIssuePriority != agBackupUrgent ||
		result.Status == "completed" || result.SessionID == "" || ctx.Err() != nil {
		return result, tools
	}
	if !d.agBackupUsable(task.RuntimeID) {
		return result, tools
	}
	st := d.agBackupStatusNow()
	if agQuotaAccountKey(agentEnv) != agQuotaAccountKey(nil) || d.agQuotaAccountHold(st.Dir) != nil {
		return result, tools
	}
	release, ok := d.agBackupReserveResume(st.Dir, d.agBackupSetting(task.RuntimeID).BackupMaxConcurrent)
	if !ok {
		taskLog.Info("quota: backup account is at its concurrency limit; not resuming this run there", "task_id", task.ID)
		return result, tools
	}
	defer release()

	taskLog.Info("quota: urgent run hit the usage limit; resuming in place on the backup account", "session_id", result.SessionID, "dir", st.Dir)
	agentEnv["CLAUDE_CONFIG_DIR"] = st.Dir
	opts.ResumeSessionID = result.SessionID
	opts.ResumeExpected = false
	opts.ResumeContinuityNotice = ""
	defer d.agQuotaWire(&opts, task, provider, agentEnv)()

	resumed, resumedTools, err := d.executeAndDrain(ctx, backend, agBackupResumePrompt, opts, taskLog, task.ID, codexHome, msgSeq)
	if err != nil {
		taskLog.Warn("quota: resume on the backup account did not start; keeping the original result", "error", err)
		return result, tools
	}
	if resumed.ResumeRejected || resumed.ResumeRejectedTransient {
		taskLog.Warn("quota: backup account rejected the resume; failing as quota", "error", resumed.Error)
		result.Usage = mergeUsage(result.Usage, resumed.Usage)
		return result, tools
	}
	resumed.Usage = mergeUsage(result.Usage, resumed.Usage)
	if resumed.SessionID == "" {
		resumed.SessionID = result.SessionID
	}
	return resumed, tools + resumedTools
}
