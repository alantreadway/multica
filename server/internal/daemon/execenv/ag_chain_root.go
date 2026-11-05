package execenv

// AG patch: stable worktree path for resumed Claude sessions. Claude Code keys
// a session's transcript on the working directory, so a worktree run that
// resumes a session reads its history cheaply only when it runs at the path the
// session was written from. A run in a chain therefore takes over the env root
// of the run before it, whose worktree/ was finalized and removed, and adds its
// own worktree at the same path.
//
// The owner marker is not rewritten: it keeps naming the task whose key is in
// the directory's name. ValidateEnvRootOwnerPath, which every GC mutation
// passes through, reads exactly that, and so does a daemon without this patch,
// which sees a root it does not own and leaves it alone.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ChainRefusedError says why a root could not be taken over. The caller treats
// every one of them as "use a new root", never as a failed task.
type ChainRefusedError struct{ Reason string }

func (e *ChainRefusedError) Error() string { return "stable env root unavailable: " + e.Reason }

func chainRefused(format string, args ...any) error {
	return &ChainRefusedError{Reason: fmt.Sprintf(format, args...)}
}

// ClaimChainedEnvRoot takes over priorRoot, the env root of the run before
// taskID's, and resets it for taskID. It refuses, without touching anything,
// unless all of these hold while the root's execution lock is held:
//
//   - priorRoot is a real directory exactly two levels below the workspaces
//     root, under workspaceID's directory;
//   - its owner marker names a task of that workspace whose key is in the
//     directory's name (the root is a task root the daemon made);
//   - its completion record, when present, belongs to issueID and does not mark
//     a preserved worktree (a record is absent while the run before has not
//     written one yet, which is no reason to refuse);
//   - worktree/ is gone. A worktree still there was not finalized, and removing
//     it would destroy the only copy of that work, so it is never inspected or
//     cleaned, only declined.
//
// A root held by a running execution is not waited for: ErrEnvRootBusy.
func ClaimChainedEnvRoot(workspacesRoot, workspaceID, taskID, issueID, priorRoot string) (*EnvRootClaim, error) {
	if workspacesRoot == "" || workspaceID == "" || taskID == "" || issueID == "" || priorRoot == "" {
		return nil, chainRefused("missing identity")
	}
	envRoot := filepath.Clean(priorRoot)
	rel, err := filepath.Rel(filepath.Clean(workspacesRoot), envRoot)
	if err != nil || !filepath.IsLocal(rel) || len(strings.Split(rel, string(filepath.Separator))) != 2 {
		return nil, chainRefused("%s is not a task root below the workspaces root", envRoot)
	}
	// Lstat, not Stat: a symlink standing in for the root is not the directory
	// a run made.
	if info, err := os.Lstat(envRoot); err != nil || !info.IsDir() {
		return nil, chainRefused("%s is gone or not a directory", envRoot)
	}

	lockFile, err := openLockFile(filepath.Join(envRoot, envRootLockFile))
	if err != nil {
		return nil, chainRefused("open lock: %v", err)
	}
	locked, err := lockFileExclusiveNonBlocking(lockFile)
	if err != nil {
		lockFile.Close()
		return nil, chainRefused("lock: %v", err)
	}
	if !locked {
		lockFile.Close()
		return nil, fmt.Errorf("execenv: chain %s: %w", envRoot, ErrEnvRootBusy)
	}
	claimed := false
	defer func() {
		if !claimed {
			releaseLockFile(lockFile)
		}
	}()

	if err := removeStaleEnvRootOwnerTemps(envRoot); err != nil {
		return nil, chainRefused("clear owner temp files: %v", err)
	}
	owner, err := ReadEnvRootOwner(envRoot)
	if err != nil {
		return nil, chainRefused("read owner marker: %v", err)
	}
	if owner.TaskID == "" || owner.WorkspaceID != workspaceID {
		return nil, chainRefused("owner marker names no task of this workspace")
	}
	if owner.TaskID == taskID {
		return nil, chainRefused("root is already this task's own")
	}
	if err := ValidateEnvRootOwnerPath(workspacesRoot, envRoot, *owner); err != nil {
		return nil, chainRefused("owner marker does not match the directory: %v", err)
	}

	meta, err := ReadGCMeta(envRoot)
	switch {
	case err == nil:
		if meta.PreservedWorktree {
			return nil, chainRefused("the worktree was preserved")
		}
		if meta.Kind != GCKindIssue || strings.TrimSpace(meta.IssueID) != issueID ||
			(meta.WorkspaceID != "" && strings.TrimSpace(meta.WorkspaceID) != workspaceID) {
			return nil, chainRefused("completion record belongs to another issue")
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, chainRefused("read completion record: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(envRoot, localWorktreeDirName)); err == nil {
		return nil, chainRefused("the worktree was not finalized")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, chainRefused("inspect worktree: %v", err)
	}

	// Same reset a rerun of a task's own root gets: a clean tree, the marker and
	// the lock kept.
	if err := resetEnvRootContents(envRoot); err != nil {
		return nil, chainRefused("reset: %v", err)
	}
	claimed = true
	return &EnvRootClaim{rootDir: envRoot, lock: lockFile}, nil
}

// agEnvRootOverride returns the env root Prepare works in: proposed, or the
// chained root the caller claimed with ClaimChainedEnvRoot. The caller holds
// the claim, so the override is only taken together with EnvRootPreclaimed, and
// only for a root that still names a task of this workspace.
func agEnvRootOverride(params PrepareParams, proposed string) (string, error) {
	if params.EnvRootOverride == "" {
		return proposed, nil
	}
	if !params.EnvRootPreclaimed {
		return "", fmt.Errorf("execenv: env root override needs a preclaimed root")
	}
	owner, err := ReadEnvRootOwner(params.EnvRootOverride)
	if err != nil {
		return "", fmt.Errorf("execenv: read owner of env root override: %w", err)
	}
	if owner.WorkspaceID != params.WorkspaceID {
		return "", fmt.Errorf("execenv: env root override %s is not this workspace's", params.EnvRootOverride)
	}
	if err := ValidateEnvRootOwnerPath(params.WorkspacesRoot, params.EnvRootOverride, *owner); err != nil {
		return "", err
	}
	return params.EnvRootOverride, nil
}
