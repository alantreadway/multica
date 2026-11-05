package daemon

// AG patch: Claude resume window. A claude issue run whose prior transcript was
// last written an hour or more ago starts a fresh session on purpose: no
// "prior session unavailable" notice, and the cold prompt path with full
// context. Anything we can't age, any other provider and chat tasks stay as
// they were.

import (
	"bytes"
	"context"
	"log/slog"
	"os"
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
	// The hook only sees the work dir to resolve a relative CLAUDE_CONFIG_DIR, so
	// the two modes differ in path alone; both must give the same answer.
	for _, mode := range []string{"worktree", "non-worktree"} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				dir := agResumeEnv(t)
				agResumeTranscript(t, dir, tc.age)
				workDir := filepath.Join(t.TempDir(), "task", "workdir")
				if mode == "worktree" {
					workDir = filepath.Join(t.TempDir(), "task", "worktree")
				}
				task, taskCtx := agResumeTask()
				log, buf := agResumeLogger()

				agClaudeFreshAfterWindow(&task, &taskCtx, "claude", workDir, log)

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

// Through the real caller: the hook runs before the gate and the brief, so a
// stale session is dropped without the notice even though the gate would have
// dropped it with one (the prior workdir differs here), and claude is started
// without --resume.
func TestAGPatchClaudeResumeWindowRunTask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script agent fixture is POSIX-only")
	}
	cases := []struct {
		name       string
		age        time.Duration
		wantFresh  bool
		wantNotice bool // the existing gate drops an unreachable prior workdir with a notice
	}{
		{"over the window", 3 * time.Hour, true, false},
		{"under the window", 5 * time.Minute, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configDir := agQuotaEnv(t)
			t.Setenv("IS_SANDBOX", "1")
			// Real clock here: runTask is driven end to end, so age the transcript
			// against time.Now.
			path := filepath.Join(configDir, "projects", "-prior-workdir")
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(path, agResumeSession+".jsonl")
			if err := os.WriteFile(file, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			mtime := time.Now().Add(-tc.age)
			if err := os.Chtimes(file, mtime, mtime); err != nil {
				t.Fatal(err)
			}

			fixture, err := filepath.Abs(filepath.Join("..", "..", "pkg", "agent", "testdata", "claude-code-rate-limit-rejected.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			argsFile, stdinFile := filepath.Join(out, "args"), filepath.Join(out, "stdin")
			fake := filepath.Join(t.TempDir(), "claude")
			writeTestExecutable(t, fake, []byte("#!/bin/sh\ncase \"$*\" in *--version*) echo '2.1.299'; exit 0;; esac\n"+
				"echo \"$*\" > '"+argsFile+"'\nread -r line\nprintf '%s' \"$line\" > '"+stdinFile+"'\ncat '"+fixture+"'\n"))

			s := newAGQuotaClaimServer(t)
			d := newAGQuotaDaemon(t, s.URL, filepath.Join(t.TempDir(), "ag_quota.json"), &agQuotaClock{t: agQuotaResets.Add(-time.Hour)})
			d.executionEnvironmentCommand = nil
			d.workspaces = make(map[string]*workspaceState)
			d.activeEnvRoots = make(map[string]int)
			d.runtimeIndex["test-rt"] = Runtime{ID: "test-rt", Provider: "claude"}
			d.cfg.WorkspacesRoot = t.TempDir()
			d.cfg.AgentTimeout = 10 * time.Second
			agents := map[string]AgentEntry{"claude": {Path: fake}}
			d.cfg.Agents = agents
			d.agentsAvailable.Store(&agents)

			task := Task{
				ID: "resume-window-task", WorkspaceID: "test-ws", RuntimeID: "test-rt", IssueID: "test-issue",
				AgentID: "test-agent", AuthToken: "mat_test_resume",
				Agent:          &AgentData{ID: "test-agent", Name: "test"},
				PriorSessionID: agResumeSession, PriorWorkDir: filepath.Join(t.TempDir(), "elsewhere"),
				TriggerCommentID: "comment-1", TriggerCommentContent: "please continue",
			}
			log, buf := agResumeLogger()
			if _, err := d.runTask(context.Background(), task, "claude", 0, log); err != nil {
				t.Fatalf("runTask: %v", err)
			}

			args, _ := os.ReadFile(argsFile)
			stdin, _ := os.ReadFile(stdinFile)
			if len(args) == 0 || len(stdin) == 0 {
				t.Fatalf("fake claude was not run: args=%q stdin=%q", args, stdin)
			}
			if got := strings.Contains(buf.String(), "starting fresh Claude session"); got != tc.wantFresh {
				t.Errorf("fresh-start log = %v, want %v\n%s", got, tc.wantFresh, buf.String())
			}
			if strings.Contains(string(args), "--resume") {
				t.Errorf("claude was started with --resume: %s", args)
			}
			if got := strings.Contains(string(stdin), "could not be restored"); got != tc.wantNotice {
				t.Errorf("continuity notice in prompt = %v, want %v\n%s", got, tc.wantNotice, stdin)
			}
		})
	}
}
