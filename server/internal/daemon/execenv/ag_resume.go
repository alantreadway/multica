package execenv

import "strings"

// AG patch: Claude resume across worktree directories. A worktree-mode run
// starts in a new directory each time, so a Claude session can only follow it
// when the prior run's work is in the new tree, or the prior run left none.

// agOwnBranchResumable reports whether the worktree stands on the
// conversation's own branch, reached without the preferred name having been
// refused, and that branch was created or reset onto HEAD this run:
//
//   - A branch created now means the prior run committed nothing (a run with no
//     commits has its branch deleted at Finalize). A branch someone deleted
//     without merging looks the same; that case is accepted.
//   - A branch reset because it was merged into HEAD has its work through HEAD.
//
// The fingerprint fallback (<preferred>-<fingerprint>) is reached exactly when
// the preferred branch exists but cannot be proved ours, so the prior work may
// be on it and not here. A forked or task-scoped branch gives no guarantee
// either.
func agOwnBranchResumable(plan taskBranchPlan, actualBranch string) bool {
	if !plan.conversational || plan.continues || actualBranch != plan.name {
		return false
	}
	return !strings.HasSuffix(plan.name, "-"+plan.owner.fingerprint())
}
