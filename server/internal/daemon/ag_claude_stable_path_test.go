package daemon

// AG patch: stable worktree path for resumed Claude sessions; TestAGPatch*
// tests as counted by ag-patches/test.sh.
//
// A worktree-mode Claude run that resumes its prior session works at the
// path the prior run worked at, in a worktree added again there. Anything
// short of every condition leaves the run on its own per-task root, and the
// cross-directory resume of the base patch applies. Real runTask turns, a fake
// claude CLI, a real git repository.

import (
	"bytes"
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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

const agChainIssueID = "55555555-5555-5555-5555-555555555501"

// agChain drives a chain of turns on one issue through handleTask, so each turn
// writes the .gc_meta.json the real daemon writes and marks its env roots
// active the way it does. Turn n carries the session and working directory the
// server would give it from turn n-1.
type agChain struct {
	t         *testing.T
	d         *Daemon
	repo      string
	configDir string
	out       string // the fake claude's records and hooks
	fakeBin   string
	task      Task
	turns     []agTurn

	mu     sync.Mutex
	result TaskResult // the last runTask result, captured by the runner
	runErr error
}

type agTurn struct {
	result TaskResult
	err    error
	log    string
	stdin  string
	args   string
	pwd    string
	top    string
	branch string
}

func (o agTurn) resumed() bool {
	return strings.Contains(o.args, "--resume\n"+o.result.SessionID+"\n")
}
func (o agTurn) notice() bool  { return strings.Contains(o.stdin, "## Session Continuity Notice") }
func (o agTurn) dirLine() bool { return strings.Contains(o.stdin, "## Working directory") }
func (o agTurn) stable() bool  { return strings.Contains(o.log, "claude resume: stable worktree path") }
func (o agTurn) fallback() bool {
	return strings.Contains(o.log, "claude resume: per-task worktree path")
}
func (o agTurn) freshLogged() bool { return strings.Contains(o.log, "starting fresh Claude session") }

const agChainSession = "session-chain"

// newAgChain builds a daemon with a worktree-mode project, GC settings and a
// fake claude. The fake records where it ran, runs the turn's hook (a shell
// snippet the test writes to $OUT/hook) and reports the session
// agChainSession.
func newAgChain(t *testing.T) *agChain {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script agent fixture is POSIX-only")
	}
	agStablePathEnabled = true
	t.Setenv("IS_SANDBOX", "1")
	configDir := filepath.Join(t.TempDir(), "claude-config")
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	prevNow := agClaudeResumeNow
	agClaudeResumeNow = time.Now
	t.Cleanup(func() { agClaudeResumeNow = prevNow })

	c := &agChain{t: t, repo: createDaemonTestRepo(t), configDir: configDir, out: t.TempDir()}
	c.fakeBin = filepath.Join(t.TempDir(), "claude")
	writeTestExecutable(t, c.fakeBin, []byte(`#!/bin/sh
case "$*" in *--version*) echo '2.1.299'; exit 0;; esac
OUT='`+c.out+`'
printf '%s\n' "$@" > "$OUT/args"
pwd > "$OUT/pwd"
git rev-parse --show-toplevel > "$OUT/top" 2>&1
git rev-parse --abbrev-ref HEAD > "$OUT/branch" 2>&1
[ -f "$OUT/hook" ] && . "$OUT/hook"
IFS= read -r line
printf '%s\n' "$line" > "$OUT/stdin"
printf '%s\n' '{"type":"system","session_id":"`+agChainSession+`"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"`+agChainSession+`","result":"done"}'
`))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/daemon/workspaces/ws-chain/issues/gc-check", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": []map[string]any{
			{"id": agChainIssueID, "found": true, "status": "in_review", "updated_at": time.Now()},
		}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/status") {
			_, _ = w.Write([]byte(`{"status":"running"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := New(Config{
		DaemonID: "daemon-chain", WorkspacesRoot: t.TempDir(), AgentTimeout: 10 * time.Second, ServerBaseURL: srv.URL,
		Agents:      map[string]AgentEntry{"claude": {Path: c.fakeBin}},
		GCTTL:       5 * 24 * time.Hour,
		GCOrphanTTL: 30 * 24 * time.Hour, GCCompletedTaskTTL: 24 * time.Hour,
	}, logger)
	d.client = NewClient(srv.URL)
	d.client.SetToken("test-token")
	d.agSlotDir = t.TempDir()
	// Preparation in-process, as the other focused runTask tests do; New wires the helper process.
	d.executionEnvironmentCommand = nil
	d.runtimeIndex = map[string]Runtime{"runtime-chain": {ID: "runtime-chain", Provider: "claude"}}
	d.runner = taskRunnerFunc(func(ctx context.Context, task Task, provider string, slot int, tl *slog.Logger) (TaskResult, error) {
		res, err := d.runTask(ctx, task, provider, slot, tl)
		c.mu.Lock()
		c.result, c.runErr = res, err
		c.mu.Unlock()
		return res, err
	})
	c.d = d

	ref, err := json.Marshal(localDirectoryRef{LocalPath: c.repo, DaemonID: d.cfg.DaemonID, ExecutionMode: localDirectoryModeWorktree})
	if err != nil {
		t.Fatal(err)
	}
	c.task = Task{
		WorkspaceID: "ws-chain", IssueID: agChainIssueID, IssueIdentifier: "MUL-9101",
		RuntimeID: "runtime-chain", AgentID: "agent-chain", AuthToken: "mat_chain_test",
		Agent:            &AgentData{ID: "agent-chain", Name: "chain-agent"},
		ProjectResources: []ProjectResourceData{{ID: "repo-chain", ResourceType: localDirectoryResourceType, ResourceRef: ref}},
	}
	return c
}

// hook sets the shell snippet the fake claude runs before it answers.
func (c *agChain) hook(snippet string) {
	c.t.Helper()
	path := filepath.Join(c.out, "hook")
	if snippet == "" {
		_ = os.Remove(path)
		return
	}
	if err := os.WriteFile(path, []byte(snippet), 0o644); err != nil {
		c.t.Fatal(err)
	}
}

func (c *agChain) commitHook(name string) string {
	return fmt.Sprintf("printf 'work\\n' > %s\ngit add %s\ngit -c user.name=t -c user.email=t@example.com commit -q -m %s\n", name, name, name)
}

type agTurnOpts struct {
	age          time.Duration // the prior transcript's age; zero is 10 minutes
	noTranscript bool
	priorWorkDir string // overrides the directory the server reports
	noSession    bool   // the server offers no prior session
	pinAge       bool   // the resume clock reads exactly age after the transcript's mtime
}

// prepare gives the next turn its task, with the prior session and directory
// the previous turn left, and the prior transcript where Claude Code keeps it.
func (c *agChain) prepare(o agTurnOpts) Task {
	c.t.Helper()
	task := c.task
	task.ID = fmt.Sprintf("task-chain-%d", len(c.turns)+1)
	if len(c.turns) > 0 && !o.noSession {
		prev := c.turns[len(c.turns)-1].result
		task.PriorSessionID, task.PriorWorkDir = prev.SessionID, prev.WorkDir
		if o.priorWorkDir != "" {
			task.PriorWorkDir = o.priorWorkDir
		}
		if !o.noTranscript {
			age := o.age
			if age == 0 {
				age = 10 * time.Minute
			}
			path := agWTWriteTranscript(c.t, c.configDir, task.PriorWorkDir, task.PriorSessionID, age)
			if o.pinAge {
				// Fix "now" against the file's own mtime so a boundary is exact
				// rather than racing the clock.
				info, err := os.Stat(path)
				if err != nil {
					c.t.Fatal(err)
				}
				agClaudeResumeNow = func() time.Time { return info.ModTime().Add(age) }
			}
		}
	}
	for _, f := range []string{"args", "pwd", "top", "branch", "stdin"} {
		_ = os.Remove(filepath.Join(c.out, f))
	}
	return task
}

func (c *agChain) record(task Task, logs *bytes.Buffer) agTurn {
	c.t.Helper()
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(c.out, name))
		return strings.TrimSpace(string(b))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	turn := agTurn{
		result: c.result, err: c.runErr, log: logs.String(),
		stdin: read("stdin"), args: read("args") + "\n", pwd: read("pwd"), top: read("top"), branch: read("branch"),
	}
	c.turns = append(c.turns, turn)
	return turn
}

// run executes one turn through handleTask and returns what happened.
func (c *agChain) run(o agTurnOpts) agTurn {
	c.t.Helper()
	task := c.prepare(o)
	var logs bytes.Buffer
	c.d.logger = slog.New(slog.NewTextHandler(&logs, nil))
	c.d.handleTask(context.Background(), task, 0)
	turn := c.record(task, &logs)
	if turn.err != nil || turn.result.Status != "completed" {
		c.t.Fatalf("turn %s = %+v, %v\n%s", task.ID, turn.result, turn.err, turn.log)
	}
	return turn
}

// start begins a turn on its own goroutine, for a test that acts while the fake
// claude is running. The returned func waits for the turn and returns it.
func (c *agChain) start(o agTurnOpts) (wait func() agTurn) {
	c.t.Helper()
	task := c.prepare(o)
	var logs bytes.Buffer
	c.d.logger = slog.New(slog.NewTextHandler(&logs, nil))
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.d.handleTask(context.Background(), task, 0)
	}()
	return func() agTurn {
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			c.t.Fatal("turn did not finish")
		}
		return c.record(task, &logs)
	}
}

// blockHook parks the fake claude until release is called.
func (c *agChain) blockHook() (started func(), release func()) {
	c.hook(`touch "$OUT/started"
i=0
while [ ! -f "$OUT/release" ] && [ $i -lt 600 ]; do sleep 0.05; i=$((i+1)); done
`)
	started = func() {
		c.t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(c.out, "started")); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		c.t.Fatal("the fake claude never started")
	}
	release = func() {
		if err := os.WriteFile(filepath.Join(c.out, "release"), nil, 0o644); err != nil {
			c.t.Fatal(err)
		}
	}
	return started, release
}

// evalSymlinks resolves the directory part of p, so a worktree that no longer
// exists compares equal to the path the fake claude saw it at.
func (c *agChain) evalSymlinks(p string) string {
	c.t.Helper()
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		c.t.Fatalf("eval %q: %v", p, err)
	}
	return filepath.Join(dir, filepath.Base(p))
}

// worktreeListed reports whether the repository lists dir as one of its
// worktrees.
func (c *agChain) worktreeListed(dir string) bool {
	c.t.Helper()
	want := c.evalSymlinks(dir)
	for _, line := range strings.Split(agWTGit(c.t, c.repo, "worktree", "list", "--porcelain"), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			if got, err := filepath.EvalSymlinks(p); err == nil && got == want {
				return true
			}
		}
	}
	return false
}

// AC1, AC3, AC5: three turns on Claude. Turns 2 and 3 resume the session, take
// over turn 1's env root and run, with git, in a worktree added again at the
// same path, on the issue's branch with the earlier turns' work in it.
func TestAGPatchClaudeStablePathChain(t *testing.T) {
	c := newAgChain(t)

	c.hook(c.commitHook("turn-1.txt"))
	first := c.run(agTurnOpts{})
	if first.stable() || first.fallback() {
		t.Errorf("a first run has no stable-path decision to log:\n%s", first.log)
	}
	if first.resumed() {
		t.Errorf("a first run has nothing to resume:\n%s", first.args)
	}

	c.hook(c.commitHook("turn-2.txt"))
	second := c.run(agTurnOpts{})
	c.hook(c.commitHook("turn-3.txt"))
	third := c.run(agTurnOpts{})

	for n, turn := range []agTurn{second, third} {
		name := fmt.Sprintf("turn %d", n+2)
		if turn.result.EnvRoot != first.result.EnvRoot || turn.result.WorkDir != first.result.WorkDir {
			t.Errorf("%s: env root %q workdir %q, want the first run's %q and %q\n%s",
				name, turn.result.EnvRoot, turn.result.WorkDir, first.result.EnvRoot, first.result.WorkDir, turn.log)
		}
		if !turn.stable() || turn.fallback() {
			t.Errorf("%s: the stable path should be logged, and no fallback:\n%s", name, turn.log)
		}
		if !turn.resumed() {
			t.Errorf("%s: should resume session %s:\n%s", name, turn.result.SessionID, turn.args)
		}
		// AC3: the CLI and git run in the worktree this run added.
		want := c.evalSymlinks(turn.result.WorkDir)
		if got := c.evalSymlinks(turn.pwd); got != want {
			t.Errorf("%s: claude ran in %q, want the re-added worktree %q", name, got, want)
		}
		if got := c.evalSymlinks(turn.top); got != want {
			t.Errorf("%s: git's top level is %q, want %q", name, got, want)
		}
		if turn.branch != first.branch && first.branch != "" {
			t.Errorf("%s: on branch %q, want the first run's %q", name, turn.branch, first.branch)
		}
		if turn.branch != turn.result.BranchName {
			t.Errorf("%s: on branch %q but the result names %q", name, turn.branch, turn.result.BranchName)
		}
		// The CLI's own worktree is gone again once the run finalized.
		if _, err := os.Stat(turn.result.WorkDir); !os.IsNotExist(err) {
			t.Errorf("%s: the worktree should be removed after the run: %v", name, err)
		}
		if c.worktreeListed(turn.result.WorkDir) {
			t.Errorf("%s: git still lists the removed worktree", name)
		}
		// Resumed in place: the directory is not restated and nothing says the
		// session was lost.
		if turn.dirLine() {
			t.Errorf("%s: a stable-path resume should not state its directory:\n%s", name, turn.stdin)
		}
		if turn.notice() {
			t.Errorf("%s: a resumed run should not carry the continuity notice:\n%s", name, turn.stdin)
		}
		if turn.freshLogged() {
			t.Errorf("%s: logged a deliberate fresh start:\n%s", name, turn.log)
		}
		// Claude Code reads the transcript from the path it was written at: the
		// patch must not have migrated a copy anywhere.
		if entries, _ := os.ReadDir(filepath.Join(c.configDir, "projects")); len(entries) != 1 {
			t.Errorf("%s: transcripts under %d project directories, want the one directory of the shared path", name, len(entries))
		}
	}
	// All three runs' work is on the one branch.
	for _, f := range []string{"turn-1.txt", "turn-2.txt", "turn-3.txt"} {
		if got := agWTGit(t, c.repo, "ls-tree", "--name-only", third.result.BranchName, f); got != f {
			t.Errorf("%s is not on branch %s", f, third.result.BranchName)
		}
	}
	// The earlier turn's work was present in the re-added worktree when the
	// resumed CLI started, not only afterwards.
	c.hook("ls > \"$OUT/ls\"\n")
	fourth := c.run(agTurnOpts{})
	if !fourth.stable() {
		t.Fatalf("premise: a fourth turn should still be on the stable path:\n%s", fourth.log)
	}
	listing, _ := os.ReadFile(filepath.Join(c.out, "ls"))
	for _, f := range []string{"turn-1.txt", "turn-2.txt", "turn-3.txt"} {
		if !strings.Contains(string(listing), f) {
			t.Errorf("the resumed worktree lacks %s:\n%s", f, listing)
		}
	}
}

// A stable-path resume of the second turn is told the same things a first
// resume is, and its brief is stable: the same root means the same run-scoped
// directory names, so the prompt of turn 2 and turn 3 do not differ in their
// directory (the cache prefix the patch is for).
func TestAGPatchClaudeStablePathPromptNamesNoDirectoryChange(t *testing.T) {
	c := newAgChain(t)
	c.run(agTurnOpts{})
	second := c.run(agTurnOpts{})
	third := c.run(agTurnOpts{})

	for name, turn := range map[string]agTurn{"second": second, "third": third} {
		if strings.Contains(turn.stdin, "Working directory") || strings.Contains(turn.stdin, "differs") {
			t.Errorf("%s turn: the prompt restates the directory:\n%s", name, turn.stdin)
		}
		if strings.Contains(turn.stdin, turn.result.EnvRoot) && strings.Contains(turn.stdin, "task-chain-1") {
			t.Errorf("%s turn: the prompt names an earlier task's directory:\n%s", name, turn.stdin)
		}
	}
}

// The path is decided with the 60-minute rule, read once, and a session the
// rule clears does not get a path for nothing. Exactly 60 minutes counts as
// over.
func TestAGPatchClaudeStablePathResumeWindow(t *testing.T) {
	for _, tt := range []struct {
		name       string
		age        time.Duration
		wantStable bool
		wantReason string
	}{
		{"59 minutes", 59 * time.Minute, true, ""},
		{"59 minutes 59 seconds", 59*time.Minute + 59*time.Second, true, ""},
		{"exactly 60 minutes", 60 * time.Minute, false, "past the resume window"},
		{"61 minutes", 61 * time.Minute, false, "past the resume window"},
		{"3 hours", 3 * time.Hour, false, "past the resume window"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newAgChain(t)
			c.run(agTurnOpts{})

			got := c.run(agTurnOpts{age: tt.age, pinAge: true})

			sameRoot := got.result.EnvRoot == c.turns[0].result.EnvRoot
			if sameRoot != tt.wantStable || got.stable() != tt.wantStable {
				t.Fatalf("stable root taken = %v (logged %v), want %v\n%s", sameRoot, got.stable(), tt.wantStable, got.log)
			}
			if got.resumed() != tt.wantStable {
				t.Errorf("resumed = %v, want %v\n%s", got.resumed(), tt.wantStable, got.args)
			}
			if tt.wantStable {
				return
			}
			if !got.fallback() || !strings.Contains(got.log, tt.wantReason) {
				t.Errorf("the fallback should be logged with %q:\n%s", tt.wantReason, got.log)
			}
			// A deliberately fresh session says so in the log and nowhere in the
			// prompt: no continuity notice, no directory line.
			if !got.freshLogged() || got.notice() || got.dirLine() {
				t.Errorf("over-window run: freshLogged=%v notice=%v dirLine=%v\n%s", got.freshLogged(), got.notice(), got.dirLine(), got.stdin)
			}
			if got.result.WorkDir == c.turns[0].result.WorkDir {
				t.Errorf("a fresh session inherited the prior path %s", got.result.WorkDir)
			}
		})
	}
}

// A run with no session to resume, or whose age cannot be read, keeps its own
// root. Both are logged where a stable path was a candidate.
func TestAGPatchClaudeStablePathNeedsAReadableSession(t *testing.T) {
	t.Run("no prior session", func(t *testing.T) {
		c := newAgChain(t)
		c.run(agTurnOpts{})
		got := c.run(agTurnOpts{noSession: true})
		if got.result.EnvRoot == c.turns[0].result.EnvRoot || got.stable() || got.fallback() {
			t.Errorf("a run without a session should take a per-task root silently: stable=%v fallback=%v\n%s", got.stable(), got.fallback(), got.log)
		}
	})
	t.Run("no transcript", func(t *testing.T) {
		c := newAgChain(t)
		c.run(agTurnOpts{})
		got := c.run(agTurnOpts{noTranscript: true})
		if got.result.EnvRoot == c.turns[0].result.EnvRoot || !got.fallback() || !strings.Contains(got.log, "age unknown") {
			t.Errorf("an unreadable session age should fall back with its reason:\n%s", got.log)
		}
		if got.resumed() {
			t.Errorf("no transcript, so nothing to resume:\n%s", got.args)
		}
	})
}

// The fallbacks: every doubt about the previous root means a new per-task root
// and the base patch's cross-directory resume, never a failed task.
func TestAGPatchClaudeStablePathFallbacks(t *testing.T) {
	// A fallback run's session resumes across directories when the branch
	// continues, with the directory line the base patch gives it.
	wantCrossDirResume := func(t *testing.T, c *agChain, got agTurn, reason string) {
		t.Helper()
		first := c.turns[0].result
		if got.result.EnvRoot == first.EnvRoot {
			t.Fatalf("took the first run's root, want a new one:\n%s", got.log)
		}
		if !got.fallback() || got.stable() || !strings.Contains(got.log, reason) {
			t.Errorf("want a logged fallback (%q):\n%s", reason, got.log)
		}
		if !got.resumed() || !got.dirLine() {
			t.Errorf("a fallback should still resume across directories: resumed=%v dirLine=%v\n%s", got.resumed(), got.dirLine(), got.log)
		}
		if want := c.evalSymlinks(got.result.WorkDir); c.evalSymlinks(got.pwd) != want {
			t.Errorf("claude ran in %q, want %q", got.pwd, want)
		}
	}

	t.Run("busy root", func(t *testing.T) {
		c := newAgChain(t)
		first := c.run(agTurnOpts{})
		// A live execution holds the root's lock. The next run must not wait.
		held, err := execenv.ClaimEnvRoot(taskRootDirParams(c.d.cfg.WorkspacesRoot, Task{
			ID: "task-chain-1", WorkspaceID: c.task.WorkspaceID, IssueIdentifier: c.task.IssueIdentifier,
		}))
		if err != nil {
			t.Fatalf("premise: take the first run's lock: %v", err)
		}
		if held.RootDir() != first.result.EnvRoot {
			held.Release()
			t.Fatalf("premise: claimed %s, want %s", held.RootDir(), first.result.EnvRoot)
		}
		defer held.Release()

		start := time.Now()
		got := c.run(agTurnOpts{})
		if time.Since(start) > 15*time.Second {
			t.Errorf("a busy root was waited for: %v", time.Since(start))
		}
		wantCrossDirResume(t, c, got, "busy")
		if _, err := os.Stat(held.RootDir()); err != nil {
			t.Errorf("the busy root was touched: %v", err)
		}
	})

	t.Run("preserved worktree", func(t *testing.T) {
		c := newAgChain(t)
		first := c.run(agTurnOpts{})
		// Finalize failed on the first run: worktree/ is the only copy of its
		// work, and the stale-path cleanup would destroy it.
		wip := filepath.Join(first.result.WorkDir, "wip.txt")
		if err := os.MkdirAll(first.result.WorkDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wip, []byte("uncommitted"), 0o644); err != nil {
			t.Fatal(err)
		}
		meta, err := execenv.ReadGCMeta(first.result.EnvRoot)
		if err != nil {
			t.Fatal(err)
		}
		meta.PreservedWorktree = true
		if err := execenv.WriteGCMeta(first.result.EnvRoot, *meta, slog.New(slog.DiscardHandler)); err != nil {
			t.Fatal(err)
		}

		got := c.run(agTurnOpts{})

		if got.result.EnvRoot == first.result.EnvRoot {
			t.Fatalf("took over a root holding a preserved worktree:\n%s", got.log)
		}
		if !got.fallback() || !strings.Contains(got.log, "preserved") {
			t.Errorf("want a logged fallback naming the preserved worktree:\n%s", got.log)
		}
		if b, err := os.ReadFile(wip); err != nil || string(b) != "uncommitted" {
			t.Errorf("the preserved work was disturbed: %q, %v", b, err)
		}
		if _, err := os.Stat(filepath.Join(first.result.EnvRoot, ".task_owner")); err != nil {
			t.Errorf("the preserved root lost its owner marker: %v", err)
		}
		if got.freshLogged() || !got.resumed() {
			t.Errorf("a refused path still resumes across directories when the branch continues:\n%s", got.log)
		}
	})

	// The preserved flag alone refuses, with no worktree/ left to notice.
	t.Run("preserved flag without a worktree directory", func(t *testing.T) {
		c := newAgChain(t)
		first := c.run(agTurnOpts{})
		meta, err := execenv.ReadGCMeta(first.result.EnvRoot)
		if err != nil {
			t.Fatal(err)
		}
		meta.PreservedWorktree = true
		if err := execenv.WriteGCMeta(first.result.EnvRoot, *meta, slog.New(slog.DiscardHandler)); err != nil {
			t.Fatal(err)
		}
		got := c.run(agTurnOpts{})
		wantCrossDirResume(t, c, got, "preserved")
	})

	t.Run("unfinalized worktree", func(t *testing.T) {
		c := newAgChain(t)
		first := c.run(agTurnOpts{})
		// A worktree the prior run did not finalize, with a change nothing
		// committed.
		if err := os.MkdirAll(first.result.WorkDir, 0o755); err != nil {
			t.Fatal(err)
		}
		wip := filepath.Join(first.result.WorkDir, "undelivered.txt")
		if err := os.WriteFile(wip, []byte("not committed"), 0o644); err != nil {
			t.Fatal(err)
		}

		got := c.run(agTurnOpts{})

		wantCrossDirResume(t, c, got, "not finalized")
		if b, err := os.ReadFile(wip); err != nil || string(b) != "not committed" {
			t.Errorf("the unfinalized worktree was removed or changed: %q, %v", b, err)
		}
	})

	t.Run("root already removed", func(t *testing.T) {
		c := newAgChain(t)
		first := c.run(agTurnOpts{})
		if err := os.RemoveAll(first.result.EnvRoot); err != nil {
			t.Fatal(err)
		}
		got := c.run(agTurnOpts{})
		if got.result.EnvRoot == first.result.EnvRoot {
			t.Fatalf("recreated the removed root:\n%s", got.log)
		}
		if _, err := os.Stat(first.result.EnvRoot); !os.IsNotExist(err) {
			t.Errorf("the removed root was brought back: %v", err)
		}
		if !got.fallback() {
			t.Errorf("want a logged fallback:\n%s", got.log)
		}
	})

	t.Run("owner marker removed", func(t *testing.T) {
		c := newAgChain(t)
		first := c.run(agTurnOpts{})
		if err := os.Remove(filepath.Join(first.result.EnvRoot, ".task_owner")); err != nil {
			t.Fatal(err)
		}
		got := c.run(agTurnOpts{})
		wantCrossDirResume(t, c, got, "owner")
	})

	t.Run("owner marker names another workspace", func(t *testing.T) {
		c := newAgChain(t)
		first := c.run(agTurnOpts{})
		data, _ := json.Marshal(execenv.EnvRootOwner{WorkspaceID: "ws-other", TaskID: "task-chain-1"})
		if err := os.WriteFile(filepath.Join(first.result.EnvRoot, ".task_owner"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		got := c.run(agTurnOpts{})
		wantCrossDirResume(t, c, got, "owner")
	})

	t.Run("completion record of another issue", func(t *testing.T) {
		c := newAgChain(t)
		first := c.run(agTurnOpts{})
		meta, err := execenv.ReadGCMeta(first.result.EnvRoot)
		if err != nil {
			t.Fatal(err)
		}
		meta.IssueID = "55555555-5555-5555-5555-555555555599"
		if err := execenv.WriteGCMeta(first.result.EnvRoot, *meta, slog.New(slog.DiscardHandler)); err != nil {
			t.Fatal(err)
		}
		got := c.run(agTurnOpts{})
		wantCrossDirResume(t, c, got, "another issue")
	})

	t.Run("prior directory outside the workspaces root", func(t *testing.T) {
		c := newAgChain(t)
		c.run(agTurnOpts{})
		elsewhere := filepath.Join(t.TempDir(), "task-0", "worktree")
		got := c.run(agTurnOpts{priorWorkDir: elsewhere})
		if got.stable() || !got.fallback() || !strings.Contains(got.log, "workspaces root") {
			t.Errorf("want a logged fallback naming the workspaces root:\n%s", got.log)
		}
	})

	// Not a failure of the task: the run completed on its own root.
	t.Run("a refused claim does not fail the task", func(t *testing.T) {
		c := newAgChain(t)
		first := c.run(agTurnOpts{})
		if err := os.WriteFile(filepath.Join(first.result.EnvRoot, ".task_owner"), []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := c.run(agTurnOpts{}) // run fails the test unless the task completed
		if got.result.Status != "completed" {
			t.Errorf("status = %q", got.result.Status)
		}
	})
}

// A worktree that does not hold the prior run's work refuses the resume even
// on the same path: the stable path must not bypass the record check. The
// refusal starts fresh with the usual notice and no directory line. The base
// patch's matrix runs these too (both paths); here they run in a chain, with the
// third run as the one under test, so the path is repeated twice over.
func TestAGPatchClaudeStablePathStillRefusesAnUnresumableTree(t *testing.T) {
	t.Run("forked off a held branch", func(t *testing.T) {
		c := newAgChain(t)
		c.hook(c.commitHook("turn-1.txt"))
		first := c.run(agTurnOpts{})
		// The user's checkout takes the branch, so the next run forks.
		agWTGit(t, c.repo, "checkout", first.result.BranchName)

		got := c.run(agTurnOpts{})

		if got.result.EnvRoot != first.result.EnvRoot || !got.stable() {
			t.Fatalf("premise: the run should have taken the stable path\n%s", got.log)
		}
		if got.resumed() || !got.notice() || got.dirLine() {
			t.Errorf("forked tree: resumed=%v notice=%v dirLine=%v, want a fresh session with the notice\n%s", got.resumed(), got.notice(), got.dirLine(), got.stdin)
		}
	})
	t.Run("another branch holds the conversation's unmerged record", func(t *testing.T) {
		c := newAgChain(t)
		c.hook(c.commitHook("turn-1.txt"))
		first := c.run(agTurnOpts{})
		// The agent is renamed, so the next run prefers another branch name and
		// the conversation's unmerged work is on the old one: this tree lacks it.
		c.task.Agent = &AgentData{ID: "agent-chain", Name: "renamed-agent"}

		got := c.run(agTurnOpts{})

		if got.result.EnvRoot != first.result.EnvRoot || !got.stable() {
			t.Fatalf("premise: the run should have taken the stable path\n%s", got.log)
		}
		if got.resumed() || !got.notice() || got.dirLine() {
			t.Errorf("unmerged record elsewhere: resumed=%v notice=%v dirLine=%v, want a fresh session with the notice\n%s", got.resumed(), got.notice(), got.dirLine(), got.stdin)
		}
	})
}

// AC5: a provider other than claude keeps per-task roots, and so does a chat
// task, whatever its session.
func TestAGPatchClaudeStablePathOnlyForClaudeIssueRuns(t *testing.T) {
	t.Run("grok", func(t *testing.T) {
		got := agWTRun(t, agWTCase{provider: "grok", firstCommits: true, transcriptAge: agWTMinutes(10), stablePath: true})
		if got.sameRoot() {
			t.Errorf("another provider took over the previous root\n%s", got.log)
		}
		if strings.Contains(got.log, "stable worktree path") || strings.Contains(got.log, "per-task worktree path") {
			t.Errorf("another provider logged a stable-path decision:\n%s", got.log)
		}
		if got.resumed() {
			t.Errorf("another provider's behaviour changed: it resumed across directories")
		}
	})
	t.Run("chat task", func(t *testing.T) {
		got := agWTRun(t, agWTCase{chat: true, firstCommits: true, transcriptAge: agWTMinutes(10), stablePath: true})
		if got.sameRoot() {
			t.Errorf("a chat task took over the previous root\n%s", got.log)
		}
	})

	// The decision function alone, for providers with no cheap fake.
	d := &Daemon{cfg: Config{DaemonID: "daemon-x", WorkspacesRoot: "/ws"}}
	ref, _ := json.Marshal(localDirectoryRef{LocalPath: "/repo", DaemonID: "daemon-x", ExecutionMode: localDirectoryModeWorktree})
	task := Task{
		ID: "t2", IssueID: "i", WorkspaceID: "w", PriorSessionID: "s", PriorWorkDir: "/ws/w/old-key/worktree",
		ProjectResources: []ProjectResourceData{{ID: "r", ResourceType: localDirectoryResourceType, ResourceRef: ref}},
	}
	known := agClaudeSessionAge{age: time.Minute, known: true}
	if root, reason := d.agStableEnvRoot(task, "claude", known); root != "/ws/w/old-key" || reason != "" {
		t.Fatalf("premise: claude = %q, %q", root, reason)
	}
	for _, provider := range []string{"codex", "gemini", "cursor", "opencode", "grok", "copilot", ""} {
		if root, reason := d.agStableEnvRoot(task, provider, known); root != "" || reason != "" {
			t.Errorf("provider %q: root %q reason %q, want neither", provider, root, reason)
		}
	}
	chat := task
	chat.ChatSessionID, chat.IssueID = "chat", ""
	if root, reason := d.agStableEnvRoot(chat, "claude", known); root != "" || reason != "" {
		t.Errorf("chat task: root %q reason %q, want neither", root, reason)
	}
}

// Only worktree-mode assignments: an in-place project or a project without a
// local directory is not about this patch.
func TestAGPatchClaudeStablePathNeedsWorktreeMode(t *testing.T) {
	d := &Daemon{cfg: Config{DaemonID: "daemon-x", WorkspacesRoot: "/ws"}}
	known := agClaudeSessionAge{age: time.Minute, known: true}
	base := Task{ID: "t2", IssueID: "i", WorkspaceID: "w", PriorSessionID: "s", PriorWorkDir: "/ws/w/old-key/worktree"}

	withMode := func(mode, daemonID string) Task {
		ref, _ := json.Marshal(localDirectoryRef{LocalPath: "/repo", DaemonID: daemonID, ExecutionMode: mode})
		task := base
		task.ProjectResources = []ProjectResourceData{{ID: "r", ResourceType: localDirectoryResourceType, ResourceRef: ref}}
		return task
	}
	for name, task := range map[string]Task{
		"in place":                 withMode(localDirectoryModeInPlace, "daemon-x"),
		"pinned to another daemon": withMode(localDirectoryModeWorktree, "daemon-y"),
		"no project resource":      base,
	} {
		if root, reason := d.agStableEnvRoot(task, "claude", known); root != "" || reason != "" {
			t.Errorf("%s: root %q reason %q, want neither", name, root, reason)
		}
	}
	if root, _ := d.agStableEnvRoot(withMode(localDirectoryModeWorktree, "daemon-x"), "claude", known); root != "/ws/w/old-key" {
		t.Errorf("worktree mode: root %q", root)
	}
}

// The prior workdir has to be the shape the daemon makes: a cleaned
// <workspaces root>/<workspace>/<key>/worktree path.
func TestAGPatchClaudeStableEnvRootShapes(t *testing.T) {
	d := &Daemon{cfg: Config{DaemonID: "daemon-x", WorkspacesRoot: "/ws"}}
	ref, _ := json.Marshal(localDirectoryRef{LocalPath: "/repo", DaemonID: "daemon-x", ExecutionMode: localDirectoryModeWorktree})
	decide := func(prior string, age agClaudeSessionAge) (string, string) {
		return d.agStableEnvRoot(Task{
			ID: "t2", IssueID: "i", WorkspaceID: "w", PriorSessionID: "s", PriorWorkDir: prior,
			ProjectResources: []ProjectResourceData{{ID: "r", ResourceType: localDirectoryResourceType, ResourceRef: ref}},
		}, "claude", age)
	}
	known := agClaudeSessionAge{age: time.Minute, known: true}

	for _, tt := range []struct {
		name       string
		prior      string
		age        agClaudeSessionAge
		wantRoot   string
		wantReason string
	}{
		{"a task worktree", "/ws/w/key/worktree", known, "/ws/w/key", ""},
		{"a worktree below the root's worktree directory", "/ws/w/key/worktree/repo", known, "", "not a task worktree"},
		{"a managed workdir", "/ws/w/key/workdir", known, "", "not a task worktree"},
		{"empty", "", known, "", "not a task worktree"},
		{"trailing slash", "/ws/w/key/worktree/", known, "", "not a task worktree"},
		{"dot-dot in the path", "/ws/w/other/../key/worktree", known, "", "not a task worktree"},
		{"relative", "w/key/worktree", known, "", "workspaces root"},
		{"outside the workspaces root", "/elsewhere/w/key/worktree", known, "", "workspaces root"},
		{"one level too shallow", "/ws/key/worktree", known, "", "workspaces root"},
		{"one level too deep", "/ws/w/a/key/worktree", known, "", "workspaces root"},
		{"the workspaces root's sibling", "/ws-other/w/key/worktree", known, "", "workspaces root"},
		{"age unknown", "/ws/w/key/worktree", agClaudeSessionAge{}, "", "age unknown"},
		{"just under the window", "/ws/w/key/worktree", agClaudeSessionAge{age: agClaudeResumeWindow - time.Nanosecond, known: true}, "/ws/w/key", ""},
		{"exactly the window", "/ws/w/key/worktree", agClaudeSessionAge{age: agClaudeResumeWindow, known: true}, "", "resume window"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, reason := decide(tt.prior, tt.age)
			if root != tt.wantRoot || !strings.Contains(reason, tt.wantReason) || (tt.wantReason == "") != (reason == "") {
				t.Errorf("root %q reason %q, want root %q reason containing %q", root, reason, tt.wantRoot, tt.wantReason)
			}
		})
	}
}

// The session's age is read once and applied by the same rule everywhere:
// exactly the window is over, unknown is not.
func TestAGPatchClaudeSessionAgeOverIsInclusive(t *testing.T) {
	for _, tt := range []struct {
		age  agClaudeSessionAge
		want bool
	}{
		{agClaudeSessionAge{age: agClaudeResumeWindow - time.Second, known: true}, false},
		{agClaudeSessionAge{age: agClaudeResumeWindow, known: true}, true},
		{agClaudeSessionAge{age: agClaudeResumeWindow + time.Second, known: true}, true},
		{agClaudeSessionAge{age: 24 * time.Hour}, false},
		{agClaudeSessionAge{}, false},
	} {
		if got := tt.age.over(); got != tt.want {
			t.Errorf("%+v over = %v, want %v", tt.age, got, tt.want)
		}
	}
}

// The gate the stable-path resume uses: refused only for a run that is in the
// directory it resumes from.
func TestAGPatchClaudeStableResumeGate(t *testing.T) {
	wt := filepath.Join("/data", "ws", "key", "worktree")
	resumable := &execenv.Environment{WorkDir: wt, LocalWorktree: &execenv.LocalWorktree{OwnBranchResumable: true}}
	forked := &execenv.Environment{WorkDir: wt, LocalWorktree: &execenv.LocalWorktree{}}
	moved := &execenv.Environment{WorkDir: filepath.Join("/data", "ws", "key2", "worktree"), LocalWorktree: &execenv.LocalWorktree{}}

	for _, tt := range []struct {
		name                 string
		stable               bool
		env                  *execenv.Environment
		prior                string
		wantReady, wantRefus bool
	}{
		{"own branch resumable", true, resumable, wt, true, false},
		{"forked", true, forked, wt, false, true},
		{"continued branch", true, &execenv.Environment{WorkDir: wt, LocalWorktree: &execenv.LocalWorktree{Continued: true, OwnBranchResumable: true}}, wt, true, false},
		{"continued but not resumable", true, &execenv.Environment{WorkDir: wt, LocalWorktree: &execenv.LocalWorktree{Continued: true}}, wt, false, true},
		{"not a stable run", false, forked, wt, false, false},
		{"no environment", true, nil, wt, false, false},
		{"no prior directory", true, forked, "", false, false},
		{"directory differs, so the migration decides", true, moved, wt, false, false},
		{"unclean spelling of the same directory", true, forked, wt + "/", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ready, refused := agClaudeStableResume(tt.stable, tt.env, tt.prior)
			if ready != tt.wantReady || refused != tt.wantRefus {
				t.Errorf("ready=%v refused=%v, want ready=%v refused=%v", ready, refused, tt.wantReady, tt.wantRefus)
			}
		})
	}
}

// GC: a root in use is kept, however old its last record; the chain's latest
// run writes the record GC reads; and when the chain ends the root is reclaimed
// on the normal TTL even though its owner marker names the first task.
func TestAGPatchClaudeStablePathGC(t *testing.T) {
	c := newAgChain(t)
	first := c.run(agTurnOpts{})
	root := first.result.EnvRoot
	wsDir := filepath.Dir(root)

	// Make the first run's record ancient: a GC pass that read it would reclaim
	// the root as soon as nothing marks it active.
	age := func(completedAgo time.Duration) {
		t.Helper()
		meta, err := execenv.ReadGCMeta(root)
		if err != nil {
			t.Fatal(err)
		}
		// WriteGCMeta stamps the time itself, so write the file directly.
		meta.CompletedAt = time.Now().Add(-completedAgo)
		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".gc_meta.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gc := func() {
		t.Helper()
		c.d.gcWorkspace(context.Background(), wsDir, &gcStats{byPattern: map[string]int{}})
	}
	exists := func() bool { _, err := os.Stat(root); return err == nil }
	age(48 * time.Hour)

	// A running chain run keeps the root.
	started, release := c.blockHook()
	wait := c.start(agTurnOpts{})
	started()
	if !c.d.isActiveEnvRoot(root) {
		t.Error("the root of a running chain run is not marked active")
	}
	gc()
	if !exists() {
		t.Fatal("GC reclaimed a root a run was working in")
	}
	release()
	second := wait()
	if second.result.EnvRoot != root || !second.stable() {
		t.Fatalf("premise: the second run should have taken the stable path\n%s", second.log)
	}
	if c.d.isActiveEnvRoot(root) {
		t.Error("the root is still marked active after the run")
	}

	// The record is the successor's: fresh, so the TTL starts again.
	meta, err := execenv.ReadGCMeta(root)
	if err != nil {
		t.Fatal(err)
	}
	if meta.TaskID != "task-chain-2" || time.Since(meta.CompletedAt) > time.Minute || meta.PreservedWorktree {
		t.Fatalf("record after the chain run = task %q completed %v preserved %v, want the successor's fresh record", meta.TaskID, meta.CompletedAt, meta.PreservedWorktree)
	}
	gc()
	if !exists() {
		t.Fatal("GC reclaimed a root inside its TTL")
	}

	// The chain ends. Past the normal TTL the root is reclaimed, though the
	// owner marker still names the task whose key is in the directory's name.
	owner, err := execenv.ReadEnvRootOwner(root)
	if err != nil || owner.TaskID != "task-chain-1" {
		t.Fatalf("owner marker = %+v, %v; want the first task's, which is what GC validates the path against", owner, err)
	}
	age(25 * time.Hour)
	gc()
	if exists() {
		t.Error("the root of an ended chain was not reclaimed after the TTL")
	}
}

// At most one live execution per task id, whichever root it works in. The
// server redelivers a task whose lease lapsed under the same id, and the claim
// on the task's own root is what fails that second execution as busy. A run on
// the previous run's root still holds its own root's claim, and a run that fell
// back is on its own root, so the redelivery fails in both.
func TestAGPatchClaudeStablePathFencesARedeliveredTask(t *testing.T) {
	redeliver := func(t *testing.T, c *agChain) {
		t.Helper()
		// The same row again: same id, prior session and directory. Built here,
		// not by prepare, which would clear the files the live run is writing.
		task := c.task
		task.ID = fmt.Sprintf("task-chain-%d", len(c.turns)+1)
		prev := c.turns[len(c.turns)-1].result
		task.PriorSessionID, task.PriorWorkDir = prev.SessionID, prev.WorkDir
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		res, err := c.d.runTask(context.Background(), task, "claude", 0, logger)
		if err == nil || !strings.Contains(err.Error(), "held by a running execution") {
			t.Fatalf("the redelivered task = %+v, %v; want it refused as busy\n%s", res, err, logs.String())
		}
		if strings.Contains(logs.String(), "claude resume:") {
			t.Errorf("the redelivered task chose a path:\n%s", logs.String())
		}
	}

	t.Run("live stable run", func(t *testing.T) {
		c := newAgChain(t)
		first := c.run(agTurnOpts{})
		started, release := c.blockHook()
		wait := c.start(agTurnOpts{})
		started()
		redeliver(t, c)
		release()
		got := wait()
		if !got.stable() || got.result.EnvRoot != first.result.EnvRoot || !got.resumed() {
			t.Errorf("the live run was disturbed by the redelivery:\n%s", got.log)
		}
	})

	t.Run("live fallback run", func(t *testing.T) {
		c := newAgChain(t)
		first := c.run(agTurnOpts{})
		// The previous run is still exiting: its lock is held, so the next run
		// falls back to its own root.
		held, err := execenv.ClaimEnvRoot(taskRootDirParams(c.d.cfg.WorkspacesRoot, Task{
			ID: "task-chain-1", WorkspaceID: c.task.WorkspaceID, IssueIdentifier: c.task.IssueIdentifier,
		}))
		if err != nil || held.RootDir() != first.result.EnvRoot {
			t.Fatalf("premise: take the first run's lock: %v", err)
		}
		defer held.Release()
		started, release := c.blockHook()
		wait := c.start(agTurnOpts{})
		started()
		redeliver(t, c)
		// The lock freeing up now must not let the redelivery in either: it is
		// the live run's own root it would have to claim first.
		held.Release()
		redeliver(t, c)
		release()
		got := wait()
		if !got.fallback() || got.result.EnvRoot == first.result.EnvRoot {
			t.Errorf("premise: the live run should have fallen back:\n%s", got.log)
		}
	})
}

// The own root a stable run holds only as a fence keeps just its owner marker
// and lock. It is not marked as a task's work: GC reclaims it as an orphan once
// it is past the orphan TTL, and not before.
func TestAGPatchClaudeStablePathFenceRootIsReclaimedByGC(t *testing.T) {
	c := newAgChain(t)
	first := c.run(agTurnOpts{})
	second := c.run(agTurnOpts{})
	if !second.stable() {
		t.Fatalf("premise: no stable path\n%s", second.log)
	}
	fence, err := execenv.ResolveRootDir(taskRootDirParams(c.d.cfg.WorkspacesRoot, Task{
		ID: "task-chain-2", WorkspaceID: c.task.WorkspaceID, IssueIdentifier: c.task.IssueIdentifier,
	}))
	if err != nil || fence == first.result.EnvRoot {
		t.Fatalf("premise: the successor's own root = %q, %v", fence, err)
	}
	entries, err := os.ReadDir(fence)
	if err != nil {
		t.Fatalf("the successor's own root was not claimed: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "worktree" || e.Name() == ".gc_meta.json" {
			t.Errorf("the fence root holds %s", e.Name())
		}
	}
	gc := func() {
		c.d.gcWorkspace(context.Background(), filepath.Dir(fence), &gcStats{byPattern: map[string]int{}})
	}
	gc()
	if _, err := os.Stat(fence); err != nil {
		t.Fatalf("GC reclaimed the fence root inside the orphan TTL: %v", err)
	}
	old := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(fence, old, old); err != nil {
		t.Fatal(err)
	}
	gc()
	if _, err := os.Stat(fence); !os.IsNotExist(err) {
		t.Errorf("the fence root outlived the orphan TTL: %v", err)
	}
	if _, err := os.Stat(first.result.EnvRoot); err != nil {
		t.Errorf("GC reclaimed the chain's root with it: %v", err)
	}
}

// A daemon from before the patch cannot reuse the root a chain leaves: the
// marker names the first task, and the root a later task would get from it is
// another directory.
func TestAGPatchClaudeStablePathRootIsNotReusableByOtherTasks(t *testing.T) {
	c := newAgChain(t)
	first := c.run(agTurnOpts{})
	second := c.run(agTurnOpts{})
	if second.result.EnvRoot != first.result.EnvRoot {
		t.Fatalf("premise: no chain\n%s", second.log)
	}
	// What a daemon without the patch does for the chain's last task, and for a
	// redelivery of it.
	later := Task{ID: "task-chain-2", WorkspaceID: c.task.WorkspaceID, IssueIdentifier: c.task.IssueIdentifier}
	claim, err := execenv.ClaimEnvRoot(taskRootDirParams(c.d.cfg.WorkspacesRoot, later))
	if err != nil {
		t.Fatalf("an unpatched claim for the later task: %v", err)
	}
	defer claim.Release()
	if claim.RootDir() == first.result.EnvRoot {
		t.Errorf("a later task's own root is the chain's root %s", claim.RootDir())
	}
}

// A first run and every fallback run lay out exactly what an unpatched daemon
// does: nothing on disk changes until a resume happens.
func TestAGPatchClaudeStablePathFirstRunLayoutUnchanged(t *testing.T) {
	c := newAgChain(t)
	first := c.run(agTurnOpts{})

	owner, err := execenv.ReadEnvRootOwner(first.result.EnvRoot)
	if err != nil || owner.TaskID != "task-chain-1" || owner.WorkspaceID != "ws-chain" {
		t.Fatalf("owner marker = %+v, %v", owner, err)
	}
	if want := filepath.Join(first.result.EnvRoot, "worktree"); !strings.HasPrefix(first.result.WorkDir, want) {
		t.Errorf("workdir %s is not under %s", first.result.WorkDir, want)
	}
	rel, _ := filepath.Rel(c.d.cfg.WorkspacesRoot, first.result.EnvRoot)
	if parts := strings.Split(rel, string(filepath.Separator)); len(parts) != 2 {
		t.Errorf("env root %s is not <workspace>/<task key> below the workspaces root", rel)
	}
	if _, err := execenv.ReadGCMeta(first.result.EnvRoot); err != nil {
		t.Errorf("no completion record: %v", err)
	}
	if errors.Is(first.err, os.ErrNotExist) {
		t.Error(first.err)
	}
}
