package daemon

// AG patch: Claude resume window. A claude issue run whose prior transcript was
// last written an hour or more ago starts a fresh session on purpose: no
// "prior session unavailable" notice, and the cold prompt path with full
// context. Anything we can't age, any other provider and chat tasks stay as
// they were.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

var agResumeNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

const agResumeSession = "11111111-2222-3333-4444-555555555555"

// agResumeEnv points the default Claude account at a temp dir, freezes the
// clock and returns the config dir.
func agResumeEnv(t *testing.T) string {
	t.Helper()
	dir := agQuotaEnv(t)
	prev := agClaudeResumeNow
	agClaudeResumeNow = func() time.Time { return agResumeNow }
	t.Cleanup(func() { agClaudeResumeNow = prev })
	return dir
}

// agResumeTranscript writes the session transcript, last written age ago.
func agResumeTranscript(t *testing.T, configDir string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(configDir, "projects", "-prior-workdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, agResumeSession+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := agResumeNow.Add(-age)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func agResumeTask() (Task, execenv.TaskContextForEnv) {
	task := Task{
		ID: "task-1", IssueID: "issue-1", PriorSessionID: agResumeSession,
		PriorWorkDir:          "/prior/workdir",
		TriggerCommentID:      "comment-1",
		TriggerCommentContent: "please continue",
		IssueStatus:           "in_progress", IssueStateDeltaKnown: true,
		NewCommentsDeltaKnown: true,
	}
	return task, execenv.TaskContextForEnv{PriorSessionResumed: true}
}

func agResumeLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestAGPatchClaudeResumeWindowByAge(t *testing.T) {
	cases := []struct {
		name      string
		age       time.Duration
		wantFresh bool
	}{
		{"just written", 0, false},
		{"under the window", 59 * time.Minute, false},
		{"one second under", 60*time.Minute - time.Second, false},
		{"exactly the window", 60 * time.Minute, true},
		{"one second over", 60*time.Minute + time.Second, true},
		{"a day old", 24 * time.Hour, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := agResumeEnv(t)
			agResumeTranscript(t, dir, tc.age)
			task, taskCtx := agResumeTask()
			log, buf := agResumeLogger()

			agClaudeFreshAfterWindow(&task, &taskCtx, "claude", t.TempDir(), log)

			if got := task.PriorSessionID == ""; got != tc.wantFresh {
				t.Fatalf("PriorSessionID cleared = %v, want %v (age %v)", got, tc.wantFresh, tc.age)
			}
			if got := !taskCtx.PriorSessionResumed; got != tc.wantFresh {
				t.Fatalf("PriorSessionResumed cleared = %v, want %v", got, tc.wantFresh)
			}
			// A deliberate fresh start is never reported as a lost session.
			if task.PriorSessionResumeUnavailable || taskCtx.PriorSessionResumeUnavailable {
				t.Fatal("PriorSessionResumeUnavailable set: the notice would show for a deliberate fresh start")
			}
			logged := strings.Contains(buf.String(), "starting fresh Claude session")
			if logged != tc.wantFresh {
				t.Fatalf("fresh-start log line present = %v, want %v\n%s", logged, tc.wantFresh, buf.String())
			}
			if strings.Contains(buf.String(), "dropping prior session") {
				t.Fatalf("log must be distinct from the unreachable-session drop:\n%s", buf.String())
			}
		})
	}
}

func TestAGPatchClaudeResumeWindowLogLine(t *testing.T) {
	dir := agResumeEnv(t)
	agResumeTranscript(t, dir, 90*time.Minute)
	task, taskCtx := agResumeTask()
	log, buf := agResumeLogger()

	agClaudeFreshAfterWindow(&task, &taskCtx, "claude", t.TempDir(), log)

	out := buf.String()
	for _, want := range []string{"provider=claude", "session_id=" + agResumeSession, "age=1h30m0s"} {
		if !strings.Contains(out, want) {
			t.Errorf("log line missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "\n"); n != 1 {
		t.Errorf("want exactly one log line, got %d:\n%s", n, out)
	}
}

func TestAGPatchClaudeResumeWindowMissingTranscriptKeepsSession(t *testing.T) {
	dir := agResumeEnv(t)
	// A projects dir with an unrelated session, and a config dir with no
	// projects dir at all: neither can age the session.
	for _, setup := range []string{"other session only", "no projects dir"} {
		t.Run(setup, func(t *testing.T) {
			if setup == "other session only" {
				p := filepath.Join(dir, "projects", "-p")
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(p, "someone-else.jsonl"), []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "empty"))
			}
			task, taskCtx := agResumeTask()
			log, buf := agResumeLogger()

			agClaudeFreshAfterWindow(&task, &taskCtx, "claude", t.TempDir(), log)

			if task.PriorSessionID != agResumeSession || !taskCtx.PriorSessionResumed {
				t.Fatalf("session dropped on unknown age: id=%q resumed=%v", task.PriorSessionID, taskCtx.PriorSessionResumed)
			}
			if task.PriorSessionResumeUnavailable || taskCtx.PriorSessionResumeUnavailable {
				t.Fatal("the hook must leave notice handling to the existing gate")
			}
			if buf.Len() != 0 {
				t.Fatalf("unexpected log output:\n%s", buf.String())
			}
		})
	}
}

func TestAGPatchClaudeResumeWindowUnreadableTranscriptKeepsSession(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the current user can't traverse")
	}
	dir := agResumeEnv(t)
	agResumeTranscript(t, dir, 3*time.Hour)
	project := filepath.Join(dir, "projects", "-prior-workdir")
	if err := os.Chmod(project, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(project, 0o755) })
	task, taskCtx := agResumeTask()

	agClaudeFreshAfterWindow(&task, &taskCtx, "claude", t.TempDir(), slog.New(slog.DiscardHandler))

	if task.PriorSessionID != agResumeSession || !taskCtx.PriorSessionResumed {
		t.Fatalf("session dropped when the transcript could not be statted: id=%q", task.PriorSessionID)
	}
}

func TestAGPatchClaudeResumeWindowOnlyAppliesToClaudeIssueRuns(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		mutate   func(*Task)
	}{
		{"codex", "codex", nil},
		{"opencode", "opencode", nil},
		{"hermes", "hermes", nil},
		{"chat task on claude", "claude", func(task *Task) { task.ChatSessionID = "chat-1" }},
		{"no prior session", "claude", func(task *Task) { task.PriorSessionID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := agResumeEnv(t)
			agResumeTranscript(t, dir, 48*time.Hour)
			task, taskCtx := agResumeTask()
			if tc.mutate != nil {
				tc.mutate(&task)
			}
			before := task
			log, buf := agResumeLogger()

			agClaudeFreshAfterWindow(&task, &taskCtx, tc.provider, t.TempDir(), log)

			if !reflect.DeepEqual(task, before) {
				t.Fatalf("task changed:\n got %+v\nwant %+v", task, before)
			}
			if !taskCtx.PriorSessionResumed || taskCtx.PriorSessionResumeUnavailable {
				t.Fatalf("task context changed: %+v", taskCtx)
			}
			if buf.Len() != 0 {
				t.Fatalf("unexpected log output:\n%s", buf.String())
			}
		})
	}
}

// The agent's own CLAUDE_CONFIG_DIR (a second account) decides where the
// transcript is looked up, as it does for the run itself.
func TestAGPatchClaudeResumeWindowUsesTheAgentsOwnConfigDir(t *testing.T) {
	defaultDir := agResumeEnv(t)
	agentDir := filepath.Join(t.TempDir(), "agent-claude")

	t.Run("stale there, fresh in the default account", func(t *testing.T) {
		agResumeTranscript(t, defaultDir, time.Minute)
		agResumeTranscript(t, agentDir, 5*time.Hour)
		task, taskCtx := agResumeTask()
		task.Agent = &AgentData{CustomEnv: map[string]string{"CLAUDE_CONFIG_DIR": agentDir}}

		agClaudeFreshAfterWindow(&task, &taskCtx, "claude", t.TempDir(), slog.New(slog.DiscardHandler))

		if task.PriorSessionID != "" {
			t.Fatal("want a fresh start: the agent's account holds the stale transcript")
		}
	})
	t.Run("fresh there, stale in the default account", func(t *testing.T) {
		agResumeTranscript(t, defaultDir, 5*time.Hour)
		agResumeTranscript(t, agentDir, time.Minute)
		task, taskCtx := agResumeTask()
		task.Agent = &AgentData{CustomEnv: map[string]string{"CLAUDE_CONFIG_DIR": agentDir}}

		agClaudeFreshAfterWindow(&task, &taskCtx, "claude", t.TempDir(), slog.New(slog.DiscardHandler))

		if task.PriorSessionID != agResumeSession {
			t.Fatal("want the session kept: the agent's account holds a fresh transcript")
		}
	})
}

// The fresh start has to reach the agent as a cold run: the rendered prompt
// must equal what a run with no prior session gets, not a delta from a
// conversation the agent no longer has.
func TestAGPatchClaudeResumeWindowRendersColdPromptWithoutNotice(t *testing.T) {
	dir := agResumeEnv(t)
	agResumeTranscript(t, dir, 2*time.Hour)

	task, taskCtx := agResumeTask()
	// The server's view of a resumable session: what it would hand a resumed run.
	task.NewCommentCount = 2
	task.NewCommentsSince = "2026-11-05T10:00:00Z"
	task.IssueChangedFields = []string{"status"}
	agClaudeFreshAfterWindow(&task, &taskCtx, "claude", t.TempDir(), slog.New(slog.DiscardHandler))
	got := BuildPrompt(task, "claude")

	// Control: the same claim with the session under the window renders the
	// warm path, so the assertions below can tell the two apart.
	agResumeTranscript(t, dir, time.Minute)
	warm, warmCtx := agResumeTask()
	warm.NewCommentCount = 2
	warm.NewCommentsSince = "2026-11-05T10:00:00Z"
	warm.IssueChangedFields = []string{"status"}
	agClaudeFreshAfterWindow(&warm, &warmCtx, "claude", t.TempDir(), slog.New(slog.DiscardHandler))
	warmPrompt := BuildPrompt(warm, "claude")
	if !strings.Contains(warmPrompt, "since your last run") {
		t.Fatalf("control prompt is not the warm resume prompt:\n%s", warmPrompt)
	}

	cold := task
	cold.PriorSessionID = ""
	cold.NewCommentCount, cold.NewCommentsSince, cold.IssueChangedFields = 0, "", nil
	cold.IssueStateDeltaKnown, cold.NewCommentsDeltaKnown = false, false
	want := BuildPrompt(cold, "claude")
	if got != want {
		t.Fatalf("fresh-start prompt differs from the cold prompt\n got: %s\nwant: %s", got, want)
	}

	if strings.Contains(got, execenv.SessionContinuityNoticeIssue) || strings.Contains(got, "could not be restored") {
		t.Fatalf("prompt carries the prior-session-unavailable notice:\n%s", got)
	}
	for _, warmOnly := range []string{"since your last run", "You're resuming the prior session", "resuming"} {
		if strings.Contains(got, warmOnly) {
			t.Errorf("prompt claims a resume (%q):\n%s", warmOnly, got)
		}
	}
	if hint := execenv.BuildColdCommentsHint("issue-1", "comment-1", ""); hint == "" || !strings.Contains(got, hint) {
		t.Errorf("prompt lacks the cold comment-reading hint %q:\n%s", hint, got)
	}
	if hint := execenv.BuildIssueStateHint("issue-1", "", "", "", nil, false, false); !strings.Contains(got, hint) {
		t.Errorf("prompt lacks the full issue read %q:\n%s", hint, got)
	}
}

// Contrast with the existing path: a session we lost keeps its notice, and the
// hook must not suppress that for the missing-transcript case.
func TestAGPatchClaudeResumeWindowMissingTranscriptStillGetsGateNotice(t *testing.T) {
	agResumeEnv(t) // empty config dir: the transcript is missing
	task, taskCtx := agResumeTask()
	log, _ := agResumeLogger()

	agClaudeFreshAfterWindow(&task, &taskCtx, "claude", t.TempDir(), log)
	// The workdir differs from the prior one, so the existing gate drops the session.
	gateResumeToReachableSession(&task, &taskCtx, "claude", t.TempDir(), true, false, false, log)

	if !task.PriorSessionResumeUnavailable {
		t.Fatal("the gate should still flag the lost session")
	}
	if got := BuildPrompt(task, "claude"); !strings.Contains(got, execenv.SessionContinuityNoticeIssue) {
		t.Fatalf("prompt lost the notice for a missing transcript:\n%s", got)
	}
}

// A relative CLAUDE_CONFIG_DIR resolves against the run's work dir, so the
// hook has to be handed the work dir the run uses: the same transcript is
// found from one directory and absent from another.
func TestAGPatchClaudeResumeWindowResolvesRelativeConfigDirAgainstWorkDir(t *testing.T) {
	agResumeEnv(t)
	workDir := t.TempDir()
	agResumeTranscript(t, filepath.Join(workDir, ".claude-account"), 3*time.Hour)
	t.Setenv("CLAUDE_CONFIG_DIR", ".claude-account")

	t.Run("work dir holding the config dir", func(t *testing.T) {
		task, taskCtx := agResumeTask()
		agClaudeFreshAfterWindow(&task, &taskCtx, "claude", workDir, slog.New(slog.DiscardHandler))
		if task.PriorSessionID != "" || taskCtx.PriorSessionResumed {
			t.Fatalf("want a fresh start: the stale transcript is under the work dir: id=%q", task.PriorSessionID)
		}
	})
	t.Run("another work dir", func(t *testing.T) {
		task, taskCtx := agResumeTask()
		agClaudeFreshAfterWindow(&task, &taskCtx, "claude", t.TempDir(), slog.New(slog.DiscardHandler))
		if task.PriorSessionID != agResumeSession || !taskCtx.PriorSessionResumed {
			t.Fatalf("want the session kept: no transcript is reachable from this work dir: id=%q", task.PriorSessionID)
		}
	})
}

// agResumeMode is the kind of working directory a run gets.
type agResumeMode string

const (
	// agResumeManaged is a daemon-managed work dir, reused by the follow-up.
	agResumeManaged agResumeMode = "non-worktree (managed workdir)"
	// agResumeInPlace is a local_directory resource run in the user's own tree.
	agResumeInPlace agResumeMode = "non-worktree (local directory, in place)"
	// agResumeWorktree is a local_directory resource in worktree mode: every run
	// gets a new worktree, so the prior one is gone.
	agResumeWorktree agResumeMode = "worktree (local directory)"
)

type agResumeOutcome struct {
	invocations []agResumeInvocation // claude runs of the run under test, in order
	log         string
}

type agResumeInvocation struct {
	args  string // one argument per line
	stdin string // the prompt line
}

func (o agResumeOutcome) resumed(i int) bool {
	return strings.Contains(o.invocations[i].args, "\n--resume\n"+agResumeSession+"\n") ||
		strings.HasPrefix(o.invocations[i].args, "--resume\n"+agResumeSession+"\n")
}

func (o agResumeOutcome) notice(i int) bool {
	return strings.Contains(o.invocations[i].stdin, "could not be restored")
}

func (o agResumeOutcome) freshStartLogged() bool {
	return strings.Contains(o.log, "starting fresh Claude session")
}

// agResumeRunTask drives real runTask calls on the claude provider: a first run
// to create the work dir (or, in the local-directory modes, the resource), then
// the follow-up under test carrying the prior session. The prior transcript is
// written transcriptAge before the frozen clock; nil leaves it missing. When
// rejectMissingResume is set the fake claude refuses a --resume the way the real
// one does for a session it can't find.
func agResumeRunTask(t *testing.T, mode agResumeMode, transcriptAge *time.Duration, rejectMissingResume bool) agResumeOutcome {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script agent fixture is POSIX-only")
	}
	configDir := agResumeEnv(t)
	t.Setenv("IS_SANDBOX", "1")
	if transcriptAge != nil {
		agResumeTranscript(t, configDir, *transcriptAge)
	}

	d, _, cleanup := newLeaderReuseTestDaemon(t)
	t.Cleanup(cleanup)
	const daemonID = "d-resume"
	d.cfg.DaemonID = daemonID
	d.localPathLocks = NewLocalPathLocker()
	out := t.TempDir()
	argsFile, stdinFile := filepath.Join(out, "args"), filepath.Join(out, "stdin")
	resumeReply := `printf '%s\n' '{"type":"system","session_id":"session-leader-reuse"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"session-leader-reuse","result":"done"}'`
	if rejectMissingResume {
		resumeReply = `case "$*" in *--resume*)
printf '%s\n' '{"type":"system","session_id":"fresh-new"}'
printf '%s\n' '{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"fresh-new","result":"No conversation found with session ID: ` + agResumeSession + `"}'
exit 1;;
esac
` + resumeReply
	}
	writeTestExecutable(t, d.cfg.Agents["claude"].Path, []byte(`#!/bin/sh
case "$*" in *--version*) echo '2.1.299'; exit 0;; esac
printf '%s\n' "$@" >> '`+argsFile+`'
printf '%s\n' '--invocation-end--' >> '`+argsFile+`'
IFS= read -r line
printf '%s\n--prompt-end--\n' "$line" >> '`+stdinFile+`'
`+resumeReply+"\n"))

	task := leaderReuseTestTask("resume-window-task")
	task.IsLeaderTask = false
	task.PriorSessionID = agResumeSession
	task.TriggerCommentID, task.TriggerCommentContent = "comment-1", "please continue"
	log, buf := agResumeLogger()

	switch mode {
	case agResumeManaged:
		first, err := d.runTask(context.Background(), leaderReuseTestTask("resume-window-first"), "claude", 0, d.logger)
		if err != nil {
			t.Fatalf("first runTask: %v", err)
		}
		task.PriorWorkDir = first.WorkDir
	case agResumeInPlace, agResumeWorktree:
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		ref := localDirectoryRef{LocalPath: dir, DaemonID: daemonID, ExecutionMode: localDirectoryModeInPlace}
		task.PriorWorkDir = dir
		if mode == agResumeWorktree {
			agResumeGitRepo(t, dir)
			ref.ExecutionMode = localDirectoryModeWorktree
			// A worktree is disposable: the prior run's no longer exists.
			task.PriorWorkDir = filepath.Join(t.TempDir(), "gone", "worktree")
		}
		raw, err := json.Marshal(ref)
		if err != nil {
			t.Fatal(err)
		}
		task.ProjectResources = []ProjectResourceData{{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: raw}}
	}

	before := agResumeInvocations(t, argsFile, stdinFile)
	if _, err := d.runTask(context.Background(), task, "claude", 0, log); err != nil {
		t.Fatalf("runTask: %v", err)
	}
	all := agResumeInvocations(t, argsFile, stdinFile)
	if len(all) <= len(before) {
		t.Fatalf("fake claude was not run for the follow-up\n%s", buf.String())
	}
	return agResumeOutcome{invocations: all[len(before):], log: buf.String()}
}

func agResumeInvocations(t *testing.T, argsFile, stdinFile string) []agResumeInvocation {
	t.Helper()
	args, _ := os.ReadFile(argsFile)
	stdin, _ := os.ReadFile(stdinFile)
	argRuns := strings.Split(strings.TrimSuffix(string(args), "--invocation-end--\n"), "--invocation-end--\n")
	stdinRuns := strings.Split(strings.TrimSuffix(string(stdin), "\n--prompt-end--\n"), "\n--prompt-end--\n")
	if len(args) == 0 {
		return nil
	}
	if len(argRuns) != len(stdinRuns) {
		t.Fatalf("fake claude recorded %d arg sets and %d prompts", len(argRuns), len(stdinRuns))
	}
	runs := make([]agResumeInvocation, len(argRuns))
	for i := range argRuns {
		runs[i] = agResumeInvocation{args: argRuns[i], stdin: stdinRuns[i]}
	}
	return runs
}

func agResumeGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// Through the real caller, in every mode the daemon runs a task in: the hook
// runs before the gate and the brief, so a stale session starts fresh with no
// "prior session unavailable" notice, and claude is started without --resume.
// A session under the window is left to the existing gate: in a reused work dir
// it is resumed; in worktree mode the prior worktree is gone, so the gate drops
// it with its notice, as it did before this patch.
func TestAGPatchClaudeResumeWindowRunTask(t *testing.T) {
	minutes := func(m int) *time.Duration { d := time.Duration(m) * time.Minute; return &d }
	cases := []struct {
		name       string
		age        *time.Duration
		wantFresh  bool // a deliberate fresh start by the hook
		wantResume bool
		wantNotice bool
	}{
		{"3 hours", minutes(180), true, false, false},
		{"exactly 60 minutes", minutes(60), true, false, false},
		{"under the window", minutes(59), false, true, false},
	}
	for _, mode := range []agResumeMode{agResumeManaged, agResumeInPlace, agResumeWorktree} {
		for _, tc := range cases {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				wantResume, wantNotice := tc.wantResume, tc.wantNotice
				if mode == agResumeWorktree && !tc.wantFresh {
					wantResume, wantNotice = false, true
				}

				got := agResumeRunTask(t, mode, tc.age, false)

				if len(got.invocations) != 1 {
					t.Fatalf("want one claude run, got %d\n%s", len(got.invocations), got.log)
				}
				if got.resumed(0) != wantResume {
					t.Errorf("claude --resume passed = %v, want %v\nargs:\n%s", got.resumed(0), wantResume, got.invocations[0].args)
				}
				if got.notice(0) != wantNotice {
					t.Errorf("continuity notice in prompt = %v, want %v\n%s", got.notice(0), wantNotice, got.invocations[0].stdin)
				}
				if got.freshStartLogged() != tc.wantFresh {
					t.Errorf("fresh-start log = %v, want %v\n%s", got.freshStartLogged(), tc.wantFresh, got.log)
				}
			})
		}
	}
}

// A transcript that can't be found leaves the age unknown, so the hook keeps
// the session: where the work dir is reused claude is asked to resume it, the
// resume is refused, and the existing retry starts fresh with its notice. In
// worktree mode the gate drops it first. Neither logs a deliberate fresh start.
func TestAGPatchClaudeResumeWindowRunTaskMissingTranscript(t *testing.T) {
	for _, mode := range []agResumeMode{agResumeManaged, agResumeInPlace, agResumeWorktree} {
		t.Run(string(mode), func(t *testing.T) {
			got := agResumeRunTask(t, mode, nil, true)

			if got.freshStartLogged() {
				t.Errorf("the hook started fresh without knowing the age\n%s", got.log)
			}
			last := len(got.invocations) - 1
			if got.resumed(last) {
				t.Errorf("the final run still passes --resume\n%s", got.invocations[last].args)
			}
			if !got.notice(last) {
				t.Errorf("a lost session should carry the notice\n%s", got.invocations[last].stdin)
			}
			if mode == agResumeWorktree {
				if len(got.invocations) != 1 {
					t.Fatalf("worktree mode: the gate drops the session before launch, want 1 run, got %d", len(got.invocations))
				}
				return
			}
			if len(got.invocations) != 2 || !got.resumed(0) {
				t.Fatalf("want a refused --resume then one fresh retry, got %d runs, first resumed = %v\n%s",
					len(got.invocations), got.resumed(0), got.log)
			}
		})
	}
}
