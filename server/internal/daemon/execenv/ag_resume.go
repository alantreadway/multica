package execenv

import "strings"

// AG patch: Claude resume across worktree directories. A worktree-mode run
// starts in a new directory each time, so a Claude session can only follow it
// when the prior run's work is in the new tree, or the prior run left none.

// agOwnBranchResumable reports whether the worktree stands on the
// conversation's own branch (actualBranch is plan.name, not a fork off a held
// branch or a task-scoped fallback) and the prior run's work is in the tree or
// the prior run left none:
//
//   - A continued branch carries the earlier turns' commits. It resumes on the
//     fingerprint branch too: its work is in the tree, so the record check below
//     does not apply.
//   - A branch created now means the prior run committed nothing (a run with no
//     commits has its branch and record deleted at Finalize).
//   - A branch reset because it was merged into HEAD has its work through HEAD.
//
// A created or reset branch also needs the preferred name not to have been
// refused: the fingerprint fallback (<preferred>-<fingerprint>) is reached
// exactly when the preferred branch exists but cannot be proved ours, so the
// prior work may be on it and not here. The branch name comes from the agent's
// display name, so a free name is not proof on its own: the record check
// catches a prior run that committed under another name (an agent renamed
// since, or the fingerprint branch after the preferred one was deleted). Every
// record left after pruneOrphanedStateRefs has a live branch.
//
// A fork off a held branch starts fresh even when it continues: the holder is a
// preserved worktree, a crash or the user's own checkout, and any of them may
// hold uncommitted work this tree lacks.
//
// Not covered, and accepted: a branch deleted without merging leaves no record;
// a fork records nothing, so once the branch is released the next run resumes
// without the fork's commits (merged or unmerged own branch alike); and an
// issue moved to a project on another repo is not seen from here.
func agOwnBranchResumable(gitRoot string, plan taskBranchPlan, actualBranch string) bool {
	if !plan.conversational || actualBranch != plan.name {
		return false
	}
	if plan.continues {
		return true
	}
	if strings.HasSuffix(plan.name, "-"+plan.owner.fingerprint()) {
		return false
	}
	return !agHasOtherUnmergedBranch(gitRoot, plan)
}

// agHasOtherUnmergedBranch reports whether a branch other than plan.name is
// recorded as this conversation's and its tip is not in HEAD. Any read or
// ancestor-test error counts as yes, so the run starts fresh.
func agHasOtherUnmergedBranch(gitRoot string, plan taskBranchPlan) bool {
	refs, err := runGitTrimmed(gitRoot, "for-each-ref", "--format=%(refname)", localStateRefPrefix)
	if err != nil {
		return true
	}
	for _, ref := range strings.Split(refs, "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == userStateRef(plan.name) {
			continue
		}
		record, err := readBranchRecord(gitRoot, ref)
		if err != nil {
			return true
		}
		if record.owner != plan.owner {
			continue
		}
		branch := strings.TrimPrefix(ref, localStateRefPrefix)
		if _, err := runGit(gitRoot, "merge-base", "--is-ancestor", "refs/heads/"+branch, "HEAD"); err != nil {
			return true
		}
	}
	return false
}
