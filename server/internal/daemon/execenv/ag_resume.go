package execenv

// AG patch: Claude resume across worktree directories. A worktree-mode run
// starts in a new directory each time, so a Claude session can only follow it
// when the prior run's work is in the new tree, or the prior run left none.

// agOwnBranchResumable reports whether the worktree stands on the
// conversation's own branch and that branch was created or reset onto HEAD
// this run, rather than continued. Both cases are safe to resume into:
//
//   - A branch created now means the prior run committed nothing (a run with no
//     commits has its branch deleted at Finalize), so there is no work to lose.
//     A branch someone deleted without merging looks the same; that case is
//     accepted.
//   - A branch reset because it was merged into HEAD has its work through HEAD.
//
// A sibling-forked or task-scoped branch gives no such guarantee, so it is not
// resumable.
func agOwnBranchResumable(plan taskBranchPlan, actualBranch string) bool {
	return plan.conversational && !plan.continues && actualBranch == plan.name
}
