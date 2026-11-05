package execenv

// AG patch: Claude resume across worktree directories. A new worktree is
// resumable (OwnBranchResumable) when it stands on the conversation's own
// branch and that branch was created or reset onto HEAD this run. Continued is
// a separate flag that the Codex rule reads, so it must not move.

import (
	"path/filepath"
	"testing"
)

// Prior run made no commits: its created branch was deleted at Finalize, so
// the next turn creates it again. There is no work to lose.
func TestAGPatchOwnBranchResumableWhenPriorRunCommittedNothing(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	finalizeOK(t, first)
	if _, err := gitTry(t, repo, "rev-parse", "--verify", "--quiet", "refs/heads/agent/j/mul-6881"); err == nil {
		t.Fatal("premise: a run with no commits should leave no branch behind")
	}

	second := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
	if second.Branch != "agent/j/mul-6881" {
		t.Fatalf("second turn branch = %q, want the conversation's own branch", second.Branch)
	}
	if !second.OwnBranchResumable {
		t.Error("a newly created own branch should be resumable")
	}
	if second.Continued {
		t.Error("Continued must stay false: the branch was created, not continued")
	}
}

// The very first turn creates the branch too. It has no session to resume, so
// the flag is moot there, but the table in the Spec says "newly created".
func TestAGPatchOwnBranchResumableOnFirstTurn(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	if !first.OwnBranchResumable || first.Continued {
		t.Errorf("first turn: OwnBranchResumable=%v Continued=%v, want true/false", first.OwnBranchResumable, first.Continued)
	}
}

// The user merged the conversation's branch: its work is in HEAD, so a
// resumed session finds it there.
func TestAGPatchOwnBranchResumableWhenBranchWasMerged(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	writeFile(t, filepath.Join(first.WorkDir, "turn-one.txt"), "work from turn one\n")
	finalizeOK(t, first)
	gitRun(t, repo, "merge", "--no-edit", "agent/j/mul-6881")

	second := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
	if second.Branch != "agent/j/mul-6881" {
		t.Fatalf("second turn branch = %q, want the conversation's own branch", second.Branch)
	}
	if !second.OwnBranchResumable {
		t.Error("a branch reset because it was merged should be resumable")
	}
	if second.Continued {
		t.Error("Continued must stay false: the merged branch was reset, not continued")
	}
	if got := readFile(t, filepath.Join(second.WorkDir, "turn-one.txt")); got != "work from turn one\n" {
		t.Errorf("the merged work should be in the new tree through HEAD: %q", got)
	}
}

// Prior work sits on the branch and is not in HEAD. Continued carries it, and
// that case stays resumable through Continued, not the new flag.
func TestAGPatchOwnBranchResumableIsNotSetWhenContinued(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	writeFile(t, filepath.Join(first.WorkDir, "turn-one.txt"), "work from turn one\n")
	finalizeOK(t, first)

	second := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
	if !second.Continued {
		t.Fatal("premise: the second turn should continue the branch")
	}
	if second.OwnBranchResumable {
		t.Error("a continued branch is resumable through Continued; the new flag must not also be set")
	}
}

// A sibling holds the branch of a merged conversation, so this run is forked
// onto a branch of its own. It is neither continued nor the conversation's own
// branch, and nothing says the prior session's work is here.
func TestAGPatchOwnBranchResumableIsNotSetWhenForked(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	writeFile(t, filepath.Join(first.WorkDir, "turn-one.txt"), "work from turn one\n")
	finalizeOK(t, first)
	gitRun(t, repo, "merge", "--no-edit", "agent/j/mul-6881")
	// The user's own checkout now holds the conversation's branch.
	gitRun(t, repo, "checkout", "agent/j/mul-6881")

	forked := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
	if forked.Branch == "agent/j/mul-6881" {
		t.Fatal("premise: the run should have been forked off the busy branch")
	}
	if forked.OwnBranchResumable || forked.Continued {
		t.Errorf("forked: OwnBranchResumable=%v Continued=%v, want false/false", forked.OwnBranchResumable, forked.Continued)
	}
}

// A sibling holds a branch that still carries unmerged work: the fork starts
// from that branch's tip and so is continued. Resuming into it is the
// Continued rule's, and the new flag stays out of it.
func TestAGPatchOwnBranchResumableIsNotSetWhenForkedFromContinuedBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	writeFile(t, filepath.Join(first.WorkDir, "turn-one.txt"), "work from turn one\n")
	gitRun(t, first.Path, "add", "-A")
	gitRun(t, first.Path, "commit", "-m", "turn one")

	sibling := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
	if sibling.Branch == first.Branch {
		t.Fatal("premise: the sibling should have been forked off the busy branch")
	}
	if sibling.OwnBranchResumable {
		t.Error("a fork is never the conversation's own branch")
	}
	if got := readFile(t, filepath.Join(sibling.WorkDir, "turn-one.txt")); got != "work from turn one\n" {
		t.Errorf("the fork should carry the branch's work: %q", got)
	}
	finalizeOK(t, sibling)
	finalizeOK(t, first)
}

// A run with no conversation key gets a task-scoped branch that continues
// nothing.
func TestAGPatchOwnBranchResumableIsNotSetForTaskScopedBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	wt := prepareTurn(t, repo, "", turnOneTask)
	if wt.OwnBranchResumable || wt.Continued {
		t.Errorf("task-scoped: OwnBranchResumable=%v Continued=%v, want false/false", wt.OwnBranchResumable, wt.Continued)
	}
}

// The preferred branch exists but is no longer the one the conversation
// recorded (rewritten, so the recorded checkpoint is not an ancestor). The run
// moves to the fingerprint branch, newly created from HEAD, and the prior work
// is on the refused branch, not here.
func TestAGPatchOwnBranchResumableIsNotSetOnFingerprintFallback(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	writeFile(t, filepath.Join(first.WorkDir, "turn-one.txt"), "work from turn one\n")
	finalizeOK(t, first)
	orphan := gitRun(t, repo, "commit-tree", "-m", "rewritten", "HEAD^{tree}")
	gitRun(t, repo, "update-ref", "refs/heads/agent/j/mul-6881", orphan)

	second := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
	if second.Branch == "agent/j/mul-6881" {
		t.Fatal("premise: the rewritten branch should have been refused")
	}
	if second.OwnBranchResumable || second.Continued {
		t.Errorf("fingerprint fallback: OwnBranchResumable=%v Continued=%v, want false/false", second.OwnBranchResumable, second.Continued)
	}
}
