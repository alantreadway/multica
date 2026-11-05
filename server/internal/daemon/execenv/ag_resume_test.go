package execenv

// AG patch: Claude resume across worktree directories. A new worktree is
// resumable (OwnBranchResumable) when it stands on the conversation's own
// branch and that branch was created or reset onto HEAD this run. Continued is
// a separate flag that the Codex rule reads, so it must not move.

import (
	"os"
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

// Prior work sits on the branch and is not in HEAD. The worktree stands on that
// branch, so the work is in the tree and the run resumes.
func TestAGPatchOwnBranchResumableIsSetWhenContinued(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	writeFile(t, filepath.Join(first.WorkDir, "turn-one.txt"), "work from turn one\n")
	finalizeOK(t, first)

	second := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
	if !second.Continued {
		t.Fatal("premise: the second turn should continue the branch")
	}
	if !second.OwnBranchResumable {
		t.Error("a continued run on its own branch has its work in the tree and should be resumable")
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

// ---- The record check: another branch holds this conversation's work ----

// prepareNamed is prepareTurnAs with the agent's display name chosen, which is
// what names the conversation's preferred branch.
func prepareNamed(t *testing.T, repo, agentName, conversationKey, taskID string, owner branchOwner) *LocalWorktree {
	t.Helper()
	wt, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:       repo,
		EnvRoot:         t.TempDir(),
		AgentName:       agentName,
		TaskID:          taskID,
		ConversationKey: conversationKey,
		WorkspaceID:     owner.WorkspaceID,
		AgentID:         owner.AgentID,
		ConversationID:  owner.ConversationID,
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("PrepareLocalWorktree(%s as %s): %v", taskID, agentName, err)
	}
	return wt
}

// The agent was renamed after a run committed. The preferred branch name moves
// with the name, so the new one is free, but the earlier work sits on the old
// branch and this tree lacks it.
func TestAGPatchOwnBranchResumableIsNotSetWhenAgentRenamedAfterCommitting(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareNamed(t, repo, "J", "MUL-6881", turnOneTask, testBranchOwner)
	writeFile(t, filepath.Join(first.WorkDir, "turn-one.txt"), "work from turn one\n")
	finalizeOK(t, first)

	second := prepareNamed(t, repo, "K", "MUL-6881", turnTwoTask, testBranchOwner)
	if second.Branch != "agent/k/mul-6881" {
		t.Fatalf("premise: the renamed agent should get a branch of its own, got %q", second.Branch)
	}
	if second.OwnBranchResumable || second.Continued {
		t.Errorf("renamed after committing: OwnBranchResumable=%v Continued=%v, want false/false", second.OwnBranchResumable, second.Continued)
	}
}

// The preferred name was refused on the first run (a branch of the user's holds
// it), so the work went to <preferred>-<fingerprint>. The user then deleted the
// preferred branch, which makes it free again while the work is still on the
// fingerprint branch.
func TestAGPatchOwnBranchResumableIsNotSetWhenWorkIsOnFingerprintBranchAndPreferredDeleted(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	gitRun(t, repo, "branch", "agent/j/mul-6881")

	first := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	if first.Branch == "agent/j/mul-6881" {
		t.Fatal("premise: the first run should have been refused the preferred name")
	}
	writeFile(t, filepath.Join(first.WorkDir, "turn-one.txt"), "work from turn one\n")
	finalizeOK(t, first)
	gitRun(t, repo, "branch", "-D", "agent/j/mul-6881")

	second := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
	if second.Branch != "agent/j/mul-6881" {
		t.Fatalf("premise: the preferred name should be free again, got %q", second.Branch)
	}
	if second.OwnBranchResumable || second.Continued {
		t.Errorf("work on the fingerprint branch: OwnBranchResumable=%v Continued=%v, want false/false", second.OwnBranchResumable, second.Continued)
	}
}

// A renamed agent whose earlier run committed nothing has no record anywhere:
// Finalize deleted the branch and its record. That is the case the wider rule
// exists for.
func TestAGPatchOwnBranchResumableWhenAgentRenamedAfterCommittingNothing(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareNamed(t, repo, "J", "MUL-6881", turnOneTask, testBranchOwner)
	finalizeOK(t, first)
	if out := gitRun(t, repo, "for-each-ref", "refs/multica/local-state/"); out != "" {
		t.Fatalf("premise: a run with no commits should leave no record, got %q", out)
	}

	second := prepareNamed(t, repo, "K", "MUL-6881", turnTwoTask, testBranchOwner)
	if second.Branch != "agent/k/mul-6881" {
		t.Fatalf("premise: the renamed agent should get a branch of its own, got %q", second.Branch)
	}
	if !second.OwnBranchResumable {
		t.Error("a renamed agent whose earlier run committed nothing should resume")
	}
	if second.Continued {
		t.Error("Continued must stay false")
	}
}

// The old-name branch holds work, but the user merged it, so the work is in
// HEAD and in this tree.
func TestAGPatchOwnBranchResumableWhenOtherNamedBranchWasMerged(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareNamed(t, repo, "J", "MUL-6881", turnOneTask, testBranchOwner)
	writeFile(t, filepath.Join(first.WorkDir, "turn-one.txt"), "work from turn one\n")
	finalizeOK(t, first)
	gitRun(t, repo, "merge", "--no-edit", "agent/j/mul-6881")

	second := prepareNamed(t, repo, "K", "MUL-6881", turnTwoTask, testBranchOwner)
	if second.Branch != "agent/k/mul-6881" {
		t.Fatalf("premise: the renamed agent should get a branch of its own, got %q", second.Branch)
	}
	if !second.OwnBranchResumable {
		t.Error("a merged old-name branch has its work in HEAD, so the run should resume")
	}
	if got := readFile(t, filepath.Join(second.WorkDir, "turn-one.txt")); got != "work from turn one\n" {
		t.Errorf("the merged work should be in the new tree: %q", got)
	}
}

// A record that names a different workspace, agent or conversation is not this
// conversation's, whatever work its branch holds.
func TestAGPatchOwnBranchResumableIgnoresRecordsOfOtherOwners(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		other branchOwner
	}{
		{"another conversation", branchOwner{WorkspaceID: testBranchOwner.WorkspaceID, AgentID: testBranchOwner.AgentID, ConversationID: "11112222-3333-4444-5555-0000000000c1"}},
		{"another agent", branchOwner{WorkspaceID: testBranchOwner.WorkspaceID, AgentID: "11112222-3333-4444-5555-0000000000a1", ConversationID: testBranchOwner.ConversationID}},
		{"another workspace", branchOwner{WorkspaceID: "11112222-3333-4444-5555-0000000000b1", AgentID: testBranchOwner.AgentID, ConversationID: testBranchOwner.ConversationID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)

			elsewhere := prepareNamed(t, repo, "J", "MUL-7000", turnOneTask, tt.other)
			writeFile(t, filepath.Join(elsewhere.WorkDir, "other.txt"), "someone else's work\n")
			finalizeOK(t, elsewhere)
			if out := gitRun(t, repo, "for-each-ref", "refs/multica/local-state/"); out == "" {
				t.Fatal("premise: the other conversation should have left a record")
			}

			wt := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
			if wt.Branch != "agent/j/mul-6881" {
				t.Fatalf("premise: branch = %q, want the conversation's own", wt.Branch)
			}
			if !wt.OwnBranchResumable {
				t.Error("another owner's unmerged record should not block the resume")
			}
		})
	}
}

// A record that cannot be read leaves the prior work's location unknown, so the
// run is not resumable.
func TestAGPatchOwnBranchResumableIsNotSetWhenARecordCannotBeRead(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	other := prepareNamed(t, repo, "J", "MUL-7000", turnOneTask, branchOwner{
		WorkspaceID: testBranchOwner.WorkspaceID, AgentID: testBranchOwner.AgentID,
		ConversationID: "11112222-3333-4444-5555-0000000000c1",
	})
	writeFile(t, filepath.Join(other.WorkDir, "other.txt"), "other work\n")
	finalizeOK(t, other)
	record := gitRun(t, repo, "rev-parse", "refs/multica/local-state/agent/j/mul-7000")
	object := filepath.Join(repo, ".git", "objects", record[:2], record[2:])
	if err := os.Remove(object); err != nil {
		t.Fatalf("premise: the record should be a loose object: %v", err)
	}
	if _, err := gitTry(t, repo, "log", "-1", "--format=%B", record); err == nil {
		t.Fatal("premise: the record should no longer be readable")
	}

	wt := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
	if wt.Branch != "agent/j/mul-6881" {
		t.Fatalf("premise: branch = %q, want the conversation's own", wt.Branch)
	}
	if wt.OwnBranchResumable {
		t.Error("an unreadable record should fail closed")
	}
	if wt.Continued {
		t.Error("Continued must stay false")
	}
}
