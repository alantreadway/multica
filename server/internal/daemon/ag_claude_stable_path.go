package daemon

// AG patch: stable worktree path for resumed Claude sessions. A worktree-mode
// run that will resume its prior Claude session takes over the env root of the
// run before it, so its worktree is added at the path the session was written
// from. Claude Code keys the transcript on that path, and a resume from the same
// path reads the history as a cache hit where a resume from another path (see
// migrateClaudeWorktreeTranscript) rebuilds it. Anything short of every
// condition below leaves the run on its own per-task root, which is the
// behaviour without this patch.

import (
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// agStablePathEnabled is cleared by tests of the cross-directory resume, which
// is what a run falls back to when the stable path is unavailable.
var agStablePathEnabled = true

// agStableEnvRoot returns the env root a run could take over, or the reason it
// cannot. Both are empty for a run this patch is not about (not Claude, not a
// worktree, no prior session), which is not logged.
//
// The age is the reading agClaudeApplyWindow acts on later: a session that
// window will clear never gets a path, so a fresh session does not inherit one.
func (d *Daemon) agStableEnvRoot(task Task, provider string, age agClaudeSessionAge) (root, reason string) {
	if !agStablePathEnabled || !agClaudeSessionApplies(&task, provider) {
		return "", ""
	}
	if assignment, _ := localDirectoryAssignmentForTask(task, d.cfg.DaemonID); !assignment.UsesWorktree() {
		return "", ""
	}
	switch {
	case !age.known:
		return "", "prior session age unknown"
	case age.over():
		return "", "prior session is past the resume window"
	}
	// Cleaned, so the root derived here is the one runTask marks active from the
	// same field.
	workDir := filepath.Clean(task.PriorWorkDir)
	if task.PriorWorkDir == "" || workDir != task.PriorWorkDir || filepath.Base(workDir) != "worktree" {
		return "", "prior workdir is not a task worktree"
	}
	root = filepath.Dir(workDir)
	rel, err := filepath.Rel(filepath.Clean(d.cfg.WorkspacesRoot), root)
	if err != nil || !filepath.IsLocal(rel) || len(strings.Split(rel, string(filepath.Separator))) != 2 {
		return "", "prior workdir is not under the workspaces root"
	}
	return root, ""
}

// agClaimEnvRoot claims the env root for a run: the previous run's when
// agStableEnvRoot allows it and the claim succeeds, otherwise the task's own.
// It never fails a run for the sake of the stable path, and never waits for a
// busy root. stable is the root taken over, or empty.
//
// The task's own root is claimed first on every path. That claim is what fails a
// second execution of the same task id as busy, so it is held for the run even
// when the run works in the previous run's root: fence is that claim then, and
// the caller releases both. The own root it leaves behind holds only the owner
// marker and lock, which GC reclaims as an orphan once it is past the orphan TTL.
func (d *Daemon) agClaimEnvRoot(task Task, provider string, age agClaudeSessionAge, taskLog *slog.Logger) (claim, fence *execenv.EnvRootClaim, stable string, err error) {
	own, err := execenv.ClaimEnvRoot(taskRootDirParams(d.cfg.WorkspacesRoot, task))
	if err != nil {
		return nil, nil, "", err
	}
	root, reason := d.agStableEnvRoot(task, provider, age)
	if root != "" {
		chained, claimErr := execenv.ClaimChainedEnvRoot(d.cfg.WorkspacesRoot, task.WorkspaceID, task.ID, task.IssueID, root)
		if claimErr == nil {
			taskLog.Info("claude resume: stable worktree path", "path", "stable", "env_root", root)
			return chained, own, root, nil
		}
		reason = strings.TrimPrefix(claimErr.Error(), "execenv: ")
	}
	if reason != "" {
		taskLog.Info("claude resume: per-task worktree path", "path", "fallback", "reason", reason)
	}
	return own, nil, "", nil
}

// agClaudeStableResume decides the resume of a run that took over the previous
// run's env root and so has the previous run's working directory. Such a run
// has no changed directory to migrate a transcript across, so without this the
// gate would pass it on the directory comparison alone, without asking whether
// the tree holds the prior run's work. ready is the same answer a run in
// another directory gets (agClaudeWorktreeResumable); refused is its negation,
// and tells the caller to drop the session with the usual notice. Neither is
// set for a run that did not take the stable path or whose directory differs
// (it migrates instead).
func agClaudeStableResume(stable bool, env *execenv.Environment, priorWorkDir string) (ready, refused bool) {
	if !stable || env == nil || priorWorkDir == "" || filepath.Clean(env.WorkDir) != filepath.Clean(priorWorkDir) {
		return false, false
	}
	ready = agClaudeWorktreeResumable(env, priorWorkDir)
	return ready, !ready
}
