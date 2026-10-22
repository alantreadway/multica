package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// AG patch: GC meta on every terminal task path; TestAGPatch* tests as counted
// by ag-patches/test.sh.
//
// handleTask stamps .gc_meta.json on every terminal path, for every task folder
// layout. A failed worktree Finalize fails the task, so the meta cannot be tied
// to the success path: the folder would fall back to the orphan TTL with the
// preserved worktree/ still inside.

const (
	gcMetaTestDaemonID = "d-gc-meta"
	gcMetaNoPoll       = time.Hour
	gcMetaFastPoll     = 5 * time.Millisecond
)

type gcMetaRunner func(ctx context.Context, envRoot string) (TaskResult, error)

// runGCMetaTask drives handleTask against a fake runner and returns the env
// root the runner reported. taskStatus is what the server answers to the
// cancellation checks ("running" means not cancelled). pollInterval is how
// often the in-flight poll asks: an hour keeps it from firing, so only the
// pre-completion check can see a cancel.
func runGCMetaTask(t *testing.T, resources []ProjectResourceData, taskStatus string, pollInterval time.Duration, runner gcMetaRunner) string {
	t.Helper()

	envRoot := filepath.Join(t.TempDir(), "env-root")
	if err := os.MkdirAll(envRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/status") {
			_, _ = fmt.Fprintf(w, `{"status":%q}`, taskStatus)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	d := &Daemon{
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		client:             NewClient(srv.URL),
		workspaces:         make(map[string]*workspaceState),
		runtimeIndex:       map[string]Runtime{"rt-1": {ID: "rt-1", Provider: "claude"}},
		activeEnvRoots:     make(map[string]int),
		localPathLocks:     NewLocalPathLocker(),
		cancelPollInterval: pollInterval,
		cfg:                Config{WorkspacesRoot: t.TempDir(), DaemonID: gcMetaTestDaemonID},
	}
	d.runner = taskRunnerFunc(func(ctx context.Context, _ Task, _ string, _ int, _ *slog.Logger) (TaskResult, error) {
		return runner(ctx, envRoot)
	})

	d.handleTask(context.Background(), Task{
		ID:               "task-gc-meta",
		WorkspaceID:      "ws-gc-meta",
		RuntimeID:        "rt-1",
		IssueID:          "issue-gc-meta",
		Agent:            &AgentData{Name: "test-agent"},
		ProjectResources: resources,
	}, 0)
	return envRoot
}

func localDirectoryResources(t *testing.T, mode string) []ProjectResourceData {
	t.Helper()
	raw, err := json.Marshal(localDirectoryRef{
		LocalPath:     t.TempDir(),
		DaemonID:      gcMetaTestDaemonID,
		ExecutionMode: mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	return []ProjectResourceData{{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: raw}}
}

func mustReadGCMeta(t *testing.T, envRoot string) *execenv.GCMeta {
	t.Helper()
	meta, err := execenv.ReadGCMeta(envRoot)
	if err != nil {
		t.Fatalf("task finished without a readable .gc_meta.json: %v", err)
	}
	if meta.Kind != execenv.GCKindIssue || meta.IssueID != "issue-gc-meta" || meta.TaskID != "task-gc-meta" {
		t.Fatalf("meta = %+v, want the task's issue identity", meta)
	}
	return meta
}

func completedRunner(_ context.Context, envRoot string) (TaskResult, error) {
	return TaskResult{Status: "completed", EnvRoot: envRoot}, nil
}

// A worktree-mode task whose Finalize succeeded gets ordinary TTL
// treatment: meta written, no LocalDirectory exemption.
func TestAGPatchGCMetaWorktreeCompletedWithoutExemption(t *testing.T) {
	t.Parallel()

	envRoot := runGCMetaTask(t, localDirectoryResources(t, localDirectoryModeWorktree), "running", gcMetaNoPoll, completedRunner)

	meta := mustReadGCMeta(t, envRoot)
	if meta.LocalDirectory {
		t.Error("worktree task stamped LocalDirectory: its env root would never be reclaimed")
	}
	if meta.PreservedWorktree {
		t.Error("a worktree whose Finalize succeeded was marked preserved")
	}
}

// In-place local_directory tasks keep the LocalDirectory stamp.
func TestAGPatchGCMetaInPlaceStampsLocalDirectory(t *testing.T) {
	t.Parallel()

	envRoot := runGCMetaTask(t, localDirectoryResources(t, localDirectoryModeInPlace), "running", gcMetaNoPoll, completedRunner)

	meta := mustReadGCMeta(t, envRoot)
	if !meta.LocalDirectory {
		t.Error("in-place local_directory task lost its LocalDirectory stamp")
	}
	if meta.PreservedWorktree {
		t.Error("in-place task marked as a preserved worktree")
	}
}

// A worktree task that fails — including the Finalize failure that fails
// the task — still gets meta. Only the Finalize failure marks the folder as
// holding preserved work.
func TestAGPatchGCMetaWorktreeFailure(t *testing.T) {
	t.Parallel()

	finalizeFailure := &worktreePreservedError{err: errors.New("local_directory worktree: uncommitted changes")}
	cases := []struct {
		name          string
		err           error
		wantPreserved bool
	}{
		{"agent error", errors.New("agent crashed"), false},
		{"finalize failed", finalizeFailure, true},
		{"agent error and finalize failed", errors.Join(errors.New("agent crashed"), finalizeFailure), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			envRoot := runGCMetaTask(t, localDirectoryResources(t, localDirectoryModeWorktree), "running", gcMetaNoPoll,
				func(_ context.Context, envRoot string) (TaskResult, error) {
					return TaskResult{EnvRoot: envRoot}, tc.err
				})

			meta := mustReadGCMeta(t, envRoot)
			if meta.LocalDirectory {
				t.Error("worktree task stamped LocalDirectory")
			}
			if meta.PreservedWorktree != tc.wantPreserved {
				t.Errorf("PreservedWorktree = %v, want %v", meta.PreservedWorktree, tc.wantPreserved)
			}
		})
	}
}

// A folder marked PreservedWorktree must keep worktree/ past the
// TTL on both the clean path (parent issue done) and the orphan path (parent
// unreachable), while a worktree task that finished cleanly is reclaimed.
func TestAGPatchGCKeepsPreservedWorktreePastTTL(t *testing.T) {
	t.Parallel()
	const issueID = "99999999-9999-9999-9999-999999999991"

	cases := []struct {
		name       string
		gcCheck    func(w http.ResponseWriter)
		wantAction gcAction
	}{
		{
			name: "issue done past TTL",
			gcCheck: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "done", "updated_at": time.Now().Add(-10 * 24 * time.Hour)})
			},
			wantAction: gcActionCleanArtifacts,
		},
		{
			name:       "issue gone past orphan TTL",
			gcCheck:    func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) },
			wantAction: gcActionCleanManagedArtifacts,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc(fmt.Sprintf("/api/daemon/issues/%s/gc-check", issueID), func(w http.ResponseWriter, _ *http.Request) {
				tc.gcCheck(w)
			})
			d := newGCTestDaemon(t, mux)
			d.cfg.GCOrphanTTL = time.Hour

			old := time.Now().Add(-10 * 24 * time.Hour)
			newTaskDir := func(name string, preserved bool) string {
				taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", name, &execenv.GCMeta{
					Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1",
					CompletedAt: old, PreservedWorktree: preserved,
				})
				if err := os.MkdirAll(filepath.Join(taskDir, "worktree"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(taskDir, "worktree", "wip.txt"), []byte("uncommitted"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(taskDir, old, old); err != nil {
					t.Fatal(err)
				}
				return taskDir
			}
			preservedDir := newTaskDir("preserved-aaaa", true)
			finalizedDir := newTaskDir("finalized-bbbb", false)

			if got := d.shouldCleanTaskDir(context.Background(), preservedDir); got != tc.wantAction {
				t.Fatalf("preserved worktree action = %d, want %d (artifact cleanup only)", got, tc.wantAction)
			}

			stats := &gcStats{}
			d.gcWorkspace(context.Background(), filepath.Dir(preservedDir), stats)

			if _, err := os.Stat(filepath.Join(preservedDir, "worktree", "wip.txt")); err != nil {
				t.Fatalf("GC removed the preserved worktree's work: %v", err)
			}
			if _, err := os.Stat(finalizedDir); !os.IsNotExist(err) {
				t.Fatalf("a worktree task that finished cleanly was not reclaimed: stat err = %v", err)
			}
		})
	}
}

// Both cancel paths write meta.
func TestAGPatchGCMetaCancelledTask(t *testing.T) {
	t.Parallel()

	t.Run("cancelled before completion is reported", func(t *testing.T) {
		t.Parallel()
		// The poll never fires (hour interval); the pre-completion status
		// check is what observes the cancel.
		envRoot := runGCMetaTask(t, localDirectoryResources(t, localDirectoryModeWorktree), "cancelled", gcMetaNoPoll, completedRunner)

		meta := mustReadGCMeta(t, envRoot)
		if meta.LocalDirectory || meta.PreservedWorktree {
			t.Errorf("meta = %+v, want an ordinary worktree-task meta", meta)
		}
	})

	t.Run("cancelled mid-run by the poll", func(t *testing.T) {
		t.Parallel()
		envRoot := runGCMetaTask(t, localDirectoryResources(t, localDirectoryModeWorktree), "cancelled", gcMetaFastPoll,
			func(ctx context.Context, envRoot string) (TaskResult, error) {
				<-ctx.Done()
				return TaskResult{EnvRoot: envRoot}, context.Cause(ctx)
			})

		meta := mustReadGCMeta(t, envRoot)
		if meta.PreservedWorktree {
			t.Error("a plain cancel marked the worktree as preserved")
		}
	})

	t.Run("cancelled with a failed Finalize keeps the preserved marker", func(t *testing.T) {
		t.Parallel()
		envRoot := runGCMetaTask(t, localDirectoryResources(t, localDirectoryModeWorktree), "cancelled", gcMetaFastPoll,
			func(ctx context.Context, envRoot string) (TaskResult, error) {
				<-ctx.Done()
				return TaskResult{EnvRoot: envRoot}, errors.Join(context.Cause(ctx),
					&worktreePreservedError{err: errors.New("local_directory worktree: uncommitted changes")})
			})

		if meta := mustReadGCMeta(t, envRoot); !meta.PreservedWorktree {
			t.Error("cancelled task with a failed Finalize lost the preserved marker")
		}
	})
}

// The workdir/ layout (no local_directory resource) is unchanged.
func TestAGPatchGCMetaWorkdirLayout(t *testing.T) {
	t.Parallel()

	envRoot := runGCMetaTask(t, nil, "running", gcMetaNoPoll, completedRunner)

	meta := mustReadGCMeta(t, envRoot)
	if meta.LocalDirectory || meta.PreservedWorktree {
		t.Errorf("meta = %+v, want neither exemption for a workdir/ task", meta)
	}

	failedRoot := runGCMetaTask(t, nil, "running", gcMetaNoPoll, func(_ context.Context, envRoot string) (TaskResult, error) {
		return TaskResult{EnvRoot: envRoot}, errors.New("agent crashed")
	})
	mustReadGCMeta(t, failedRoot)
}

// A runner error before any env root exists has nothing to stamp, and must not
// write into an unrelated directory.
func TestAGPatchGCMetaNoEnvRoot(t *testing.T) {
	t.Parallel()

	envRoot := runGCMetaTask(t, nil, "running", gcMetaNoPoll, func(_ context.Context, _ string) (TaskResult, error) {
		return TaskResult{}, errors.New("claim execution environment: boom")
	})
	if _, err := os.Stat(filepath.Join(envRoot, ".gc_meta.json")); !os.IsNotExist(err) {
		t.Fatalf("meta written without an env root: stat err = %v", err)
	}
}

// Lock-only stubs (.task_lock + .task_owner, no meta) are reclaimed once
// past GCOrphanTTL; younger and active ones are kept, and so is a directory
// whose owner marker does not validate.
func TestAGPatchGCLockOnlyStubsFollowOrphanTTL(t *testing.T) {
	t.Parallel()

	d := newGCTestDaemon(t, http.NewServeMux())
	d.cfg.GCOrphanTTL = 72 * time.Hour

	newStub := func(name string, age time.Duration, withOwner bool) string {
		taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", name, nil)
		if !withOwner {
			if err := os.Remove(filepath.Join(taskDir, ".task_owner")); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(taskDir, ".task_lock"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(taskDir, when, when); err != nil {
			t.Fatal(err)
		}
		return taskDir
	}
	oldStub := newStub("stub-old-aaaa", 80*time.Hour, true)
	youngStub := newStub("stub-young-bbbb", 60*time.Hour, true)
	activeStub := newStub("stub-active-cccc", 80*time.Hour, true)
	unownedStub := newStub("stub-unowned-dddd", 80*time.Hour, false)
	d.markActiveEnvRoot(activeStub)
	t.Cleanup(func() { d.unmarkActiveEnvRoot(activeStub) })

	d.gcWorkspace(context.Background(), filepath.Dir(oldStub), &gcStats{})

	if _, err := os.Stat(oldStub); !os.IsNotExist(err) {
		t.Errorf("stub older than GCOrphanTTL was not removed: stat err = %v", err)
	}
	for name, dir := range map[string]string{"younger than TTL": youngStub, "active": activeStub, "unvalidated owner": unownedStub} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("stub %s was removed: %v", name, err)
		}
	}
}

// runTask's error returns are bare TaskResult{}; the env root must still reach
// handleTask so those failures get meta too.
func TestAGPatchGCMetaRunTaskErrorNamesEnvRoot(t *testing.T) {
	t.Parallel()

	d, _, cleanup := newLeaderReuseTestDaemon(t)
	defer cleanup()
	// An agent path that cannot start makes backend creation fail after the
	// environment is prepared.
	d.cfg.Agents["claude"] = AgentEntry{Path: filepath.Join(t.TempDir(), "missing-claude")}

	result, err := d.runTask(context.Background(), leaderReuseTestTask("task-backend-error"), "claude", 0, d.logger)
	if err == nil {
		t.Fatalf("runTask did not fail (result %+v); the failure this test needs is gone", result)
	}
	if result.EnvRoot == "" {
		t.Fatalf("runTask failed with %v but named no env root, so handleTask could not stamp .gc_meta.json", err)
	}
	if info, statErr := os.Stat(result.EnvRoot); statErr != nil || !info.IsDir() {
		t.Fatalf("env root %q is not a directory: %v", result.EnvRoot, statErr)
	}
}
