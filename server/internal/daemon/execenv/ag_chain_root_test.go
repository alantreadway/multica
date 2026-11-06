package execenv

// AG patch: stable worktree path for resumed Claude sessions; TestAGPatch*
// tests as counted by ag-patches/test.sh.

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	chainWS    = "ws-chain-test"
	chainIssue = "issue-chain-test"
)

// chainFixture is an env root a first run left behind: made by the real claim,
// its worktree/ removed, a completion record written.
type chainFixture struct {
	wsRoot string
	root   string
}

func newChainFixture(t *testing.T) chainFixture {
	t.Helper()
	wsRoot := t.TempDir()
	claim, err := ClaimEnvRoot(RootDirParams{WorkspacesRoot: wsRoot, WorkspaceID: chainWS, TaskID: "task-first", IssueIdentifier: "MUL-9201"})
	if err != nil {
		t.Fatalf("claim the first run's root: %v", err)
	}
	root := claim.RootDir()
	claim.Release()
	for _, sub := range []string{"output", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "output", "result.txt"), []byte("first run"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := chainFixture{wsRoot: wsRoot, root: root}
	f.writeMeta(t, GCMeta{Kind: GCKindIssue, IssueID: chainIssue, WorkspaceID: chainWS, TaskID: "task-first", CompletedAt: time.Now()})
	return f
}

func (f chainFixture) writeMeta(t *testing.T, meta GCMeta) {
	t.Helper()
	if err := WriteGCMeta(f.root, meta, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
}

func (f chainFixture) claim(t *testing.T) (*EnvRootClaim, error) {
	t.Helper()
	return ClaimChainedEnvRoot(f.wsRoot, chainWS, "task-second", chainIssue, f.root)
}

func wantRefusal(t *testing.T, err error, reason string) {
	t.Helper()
	var refused *ChainRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want a ChainRefusedError containing %q", err, reason)
	}
	if !strings.Contains(refused.Reason, reason) {
		t.Fatalf("reason = %q, want it to contain %q", refused.Reason, reason)
	}
}

// The run after a finished one takes the root over: reset to bookkeeping, the
// owner marker untouched, the lock held until released.
func TestAGPatchClaimChainedEnvRootTakesOverAFinishedRoot(t *testing.T) {
	t.Parallel()
	f := newChainFixture(t)

	claim, err := f.claim(t)
	if err != nil {
		t.Fatal(err)
	}
	if claim.RootDir() != f.root {
		t.Fatalf("RootDir = %q, want %q", claim.RootDir(), f.root)
	}

	if _, err := os.Stat(filepath.Join(f.root, "output")); !os.IsNotExist(err) {
		t.Errorf("the first run's output survived the reset: %v", err)
	}
	owner, err := ReadEnvRootOwner(f.root)
	if err != nil || owner.TaskID != "task-first" || owner.WorkspaceID != chainWS {
		t.Errorf("owner marker = %+v, %v; it must keep naming the task whose key is in the directory name", owner, err)
	}
	if err := ValidateEnvRootOwnerPath(f.wsRoot, f.root, *owner); err != nil {
		t.Errorf("GC could not validate the root: %v", err)
	}

	// Held: a second claimant, chained or fresh, does not wait.
	if _, err := f.claim(t); !errors.Is(err, ErrEnvRootBusy) {
		t.Errorf("a second chained claim = %v, want ErrEnvRootBusy", err)
	}
	var refused *ChainRefusedError
	if _, err := f.claim(t); errors.As(err, &refused) {
		t.Errorf("a busy root is its own error, not a refusal: %v", err)
	}

	claim.Release()
	next, err := ClaimChainedEnvRoot(f.wsRoot, chainWS, "task-third", chainIssue, f.root)
	if err != nil {
		t.Fatalf("a claim after the release: %v", err)
	}
	next.Release()
}

// A run whose predecessor has not written its completion record yet (the server
// wakes the next run before the handler writes it) still takes the root.
func TestAGPatchClaimChainedEnvRootAllowsAMissingCompletionRecord(t *testing.T) {
	t.Parallel()
	f := newChainFixture(t)
	if err := os.Remove(filepath.Join(f.root, ".gc_meta.json")); err != nil {
		t.Fatal(err)
	}
	claim, err := f.claim(t)
	if err != nil {
		t.Fatalf("a missing completion record refused the root: %v", err)
	}
	claim.Release()
}

// Every refusal leaves the root exactly as it was, and says why.
func TestAGPatchClaimChainedEnvRootRefusals(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		setup  func(t *testing.T, f chainFixture) (workspacesRoot, root string)
		reason string
	}{
		{"preserved worktree", func(t *testing.T, f chainFixture) (string, string) {
			f.writeMeta(t, GCMeta{Kind: GCKindIssue, IssueID: chainIssue, WorkspaceID: chainWS, TaskID: "task-first", CompletedAt: time.Now(), PreservedWorktree: true})
			return f.wsRoot, f.root
		}, "preserved"},
		{"worktree directory still there", func(t *testing.T, f chainFixture) (string, string) {
			if err := os.MkdirAll(filepath.Join(f.root, "worktree", "repo"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.root, "worktree", "repo", "undelivered.txt"), []byte("work"), 0o644); err != nil {
				t.Fatal(err)
			}
			return f.wsRoot, f.root
		}, "not finalized"},
		{"empty worktree directory", func(t *testing.T, f chainFixture) (string, string) {
			if err := os.Mkdir(filepath.Join(f.root, "worktree"), 0o755); err != nil {
				t.Fatal(err)
			}
			return f.wsRoot, f.root
		}, "not finalized"},
		{"worktree is a dangling symlink", func(t *testing.T, f chainFixture) (string, string) {
			if err := os.Symlink(filepath.Join(f.root, "nowhere"), filepath.Join(f.root, "worktree")); err != nil {
				t.Skip("symlinks unavailable")
			}
			return f.wsRoot, f.root
		}, "not finalized"},
		{"another issue's record", func(t *testing.T, f chainFixture) (string, string) {
			f.writeMeta(t, GCMeta{Kind: GCKindIssue, IssueID: "issue-other", WorkspaceID: chainWS, TaskID: "task-first", CompletedAt: time.Now()})
			return f.wsRoot, f.root
		}, "another issue"},
		{"a chat record", func(t *testing.T, f chainFixture) (string, string) {
			f.writeMeta(t, GCMeta{Kind: GCKindChat, ChatSessionID: "chat-1", WorkspaceID: chainWS, TaskID: "task-first", CompletedAt: time.Now()})
			return f.wsRoot, f.root
		}, "another issue"},
		{"a record of another workspace", func(t *testing.T, f chainFixture) (string, string) {
			f.writeMeta(t, GCMeta{Kind: GCKindIssue, IssueID: chainIssue, WorkspaceID: "ws-other", TaskID: "task-first", CompletedAt: time.Now()})
			return f.wsRoot, f.root
		}, "another issue"},
		{"an unreadable record", func(t *testing.T, f chainFixture) (string, string) {
			if err := os.WriteFile(filepath.Join(f.root, ".gc_meta.json"), []byte("{not json"), 0o644); err != nil {
				t.Fatal(err)
			}
			return f.wsRoot, f.root
		}, "completion record"},
		{"no owner marker", func(t *testing.T, f chainFixture) (string, string) {
			if err := os.Remove(filepath.Join(f.root, ".task_owner")); err != nil {
				t.Fatal(err)
			}
			return f.wsRoot, f.root
		}, "owner"},
		{"owner marker of another workspace", func(t *testing.T, f chainFixture) (string, string) {
			if err := os.WriteFile(filepath.Join(f.root, ".task_owner"), []byte(`{"workspace_id":"ws-other","task_id":"task-first"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			return f.wsRoot, f.root
		}, "owner"},
		{"owner marker naming a task whose key is not in the directory name", func(t *testing.T, f chainFixture) (string, string) {
			if err := os.WriteFile(filepath.Join(f.root, ".task_owner"), []byte(`{"workspace_id":"`+chainWS+`","task_id":"task-unrelated"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			return f.wsRoot, f.root
		}, "does not match the directory"},
		{"the root is this task's own", func(t *testing.T, f chainFixture) (string, string) {
			if err := os.WriteFile(filepath.Join(f.root, ".task_owner"), []byte(`{"workspace_id":"`+chainWS+`","task_id":"task-second"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			return f.wsRoot, f.root
		}, "own"},
		{"the root is gone", func(t *testing.T, f chainFixture) (string, string) {
			gone := filepath.Join(filepath.Dir(f.root), "gone-key")
			return f.wsRoot, gone
		}, "gone"},
		{"the root is a file", func(t *testing.T, f chainFixture) (string, string) {
			file := filepath.Join(filepath.Dir(f.root), "a-file")
			if err := os.WriteFile(file, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			return f.wsRoot, file
		}, "not a directory"},
		{"the root is a symlink to a real root", func(t *testing.T, f chainFixture) (string, string) {
			link := filepath.Join(filepath.Dir(f.root), "link-key")
			if err := os.Symlink(f.root, link); err != nil {
				t.Skip("symlinks unavailable")
			}
			return f.wsRoot, link
		}, "not a directory"},
		{"one level too shallow", func(t *testing.T, f chainFixture) (string, string) {
			return f.wsRoot, filepath.Dir(f.root)
		}, "not a task root"},
		{"the workspaces root itself", func(t *testing.T, f chainFixture) (string, string) {
			return f.wsRoot, f.wsRoot
		}, "not a task root"},
		{"one level too deep", func(t *testing.T, f chainFixture) (string, string) {
			deep := filepath.Join(f.root, "worktree")
			if err := os.MkdirAll(deep, 0o755); err != nil {
				t.Fatal(err)
			}
			return f.wsRoot, deep
		}, "not a task root"},
		{"outside the workspaces root", func(t *testing.T, f chainFixture) (string, string) {
			return t.TempDir(), f.root
		}, "not a task root"},
		{"path climbing out of the workspaces root", func(t *testing.T, f chainFixture) (string, string) {
			return f.wsRoot, filepath.Join(f.wsRoot, "..", filepath.Base(f.wsRoot), filepath.Base(filepath.Dir(f.root)), filepath.Base(f.root)+"-x")
		}, "gone"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newChainFixture(t)
			wsRoot, root := tt.setup(t, f)
			before := snapshotTree(t, f.root)

			claim, err := ClaimChainedEnvRoot(wsRoot, chainWS, "task-second", chainIssue, root)
			if claim != nil {
				claim.Release()
				t.Fatalf("claimed %s, want a refusal", claim.RootDir())
			}
			wantRefusal(t, err, tt.reason)

			if after := snapshotTree(t, f.root); after != before {
				t.Errorf("a refusal changed the root:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			// No lock was left behind on the root the refusal looked at.
			if lf, lockErr := openLockFile(filepath.Join(f.root, ".task_lock")); lockErr == nil {
				if locked, err := lockFileExclusiveNonBlocking(lf); err != nil || !locked {
					t.Errorf("the refusal left the root locked: locked=%v err=%v", locked, err)
				}
				releaseLockFile(lf)
			}
		})
	}
}

func TestAGPatchClaimChainedEnvRootMissingIdentity(t *testing.T) {
	t.Parallel()
	f := newChainFixture(t)
	for name, args := range map[string][5]string{
		"no workspaces root": {"", chainWS, "task-second", chainIssue, f.root},
		"no workspace":       {f.wsRoot, "", "task-second", chainIssue, f.root},
		"no task":            {f.wsRoot, chainWS, "", chainIssue, f.root},
		"no issue":           {f.wsRoot, chainWS, "task-second", "", f.root},
		"no prior root":      {f.wsRoot, chainWS, "task-second", chainIssue, ""},
	} {
		claim, err := ClaimChainedEnvRoot(args[0], args[1], args[2], args[3], args[4])
		if claim != nil {
			claim.Release()
			t.Errorf("%s: claimed", name)
			continue
		}
		wantRefusal(t, err, "missing identity")
	}
}

// A busy root is neither waited for nor reset.
func TestAGPatchClaimChainedEnvRootBusyRootIsLeftAlone(t *testing.T) {
	t.Parallel()
	f := newChainFixture(t)
	held, err := ClaimEnvRoot(RootDirParams{WorkspacesRoot: f.wsRoot, WorkspaceID: chainWS, TaskID: "task-first", IssueIdentifier: "MUL-9201"})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if held.RootDir() != f.root {
		t.Fatalf("premise: claimed %s, want %s", held.RootDir(), f.root)
	}
	// ClaimEnvRoot reset the root; give it content a run could lose.
	if err := os.WriteFile(filepath.Join(f.root, "live.txt"), []byte("in use"), 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		claim, err := f.claim(t)
		if claim != nil {
			claim.Release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrEnvRootBusy) {
			t.Fatalf("err = %v, want ErrEnvRootBusy", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the claim waited for a busy root")
	}
	if b, err := os.ReadFile(filepath.Join(f.root, "live.txt")); err != nil || string(b) != "in use" {
		t.Errorf("a busy root was reset: %q, %v", b, err)
	}
}

// Prepare only works in a chained root that was claimed first, and only one of
// this workspace's.
func TestAGPatchEnvRootOverride(t *testing.T) {
	t.Parallel()
	f := newChainFixture(t)
	const proposed = "/proposed/ws/task"

	params := func(mod func(*PrepareParams)) PrepareParams {
		p := PrepareParams{
			WorkspacesRoot: f.wsRoot, WorkspaceID: chainWS, TaskID: "task-second",
			EnvRootOverride: f.root, EnvRootPreclaimed: true,
		}
		if mod != nil {
			mod(&p)
		}
		return p
	}

	if got, err := agEnvRootOverride(params(nil), proposed); err != nil || got != f.root {
		t.Errorf("override = %q, %v; want %q", got, err, f.root)
	}
	if got, err := agEnvRootOverride(params(func(p *PrepareParams) { p.EnvRootOverride, p.EnvRootPreclaimed = "", false }), proposed); err != nil || got != proposed {
		t.Errorf("no override = %q, %v; want the proposal %q", got, err, proposed)
	}
	if got, err := agEnvRootOverride(params(func(p *PrepareParams) { p.EnvRootOverride = "" }), proposed); err != nil || got != proposed {
		t.Errorf("preclaimed without an override = %q, %v; want the proposal", got, err)
	}

	for name, p := range map[string]PrepareParams{
		"not preclaimed":      params(func(p *PrepareParams) { p.EnvRootPreclaimed = false }),
		"another workspace":   params(func(p *PrepareParams) { p.WorkspaceID = "ws-other" }),
		"outside the root":    params(func(p *PrepareParams) { p.WorkspacesRoot = t.TempDir() }),
		"no owner marker":     params(func(p *PrepareParams) { p.EnvRootOverride = filepath.Join(f.wsRoot, chainWS) }),
		"a root that is gone": params(func(p *PrepareParams) { p.EnvRootOverride = filepath.Join(f.wsRoot, chainWS, "gone") }),
	} {
		if got, err := agEnvRootOverride(p, proposed); err == nil {
			t.Errorf("%s: override %q accepted, want an error", name, got)
		}
	}
}

// snapshotTree lists a directory's files with their sizes and contents, for an
// exact before/after comparison.
func snapshotTree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == ".task_lock" {
			return nil
		}
		b.WriteString(rel)
		if !d.IsDir() && d.Type()&os.ModeSymlink == 0 {
			content, _ := os.ReadFile(path)
			b.WriteString(" = " + string(content))
		}
		b.WriteString("\n")
		return nil
	})
	return b.String()
}
