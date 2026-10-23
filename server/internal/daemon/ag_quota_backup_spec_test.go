package daemon

// AG patch: backup account failover, cases written from the Spec's wording
// rather than from the implementation: the resume's session and prompt, tasks
// without an issue priority, and the full list of items the setup script links.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agBackupFakeClaudeLogged logs the config dir, args and first stdin line of every
// run. A run resuming sess-first (the session of the rejected run) succeeds; any
// other run is rejected on its quota and reports the session sess-first.
const agBackupFakeClaudeLogged = `#!/bin/sh
case "$*" in
  *--version*) echo '2.1.299'; exit 0;;
  "auth status --json") cat "$CLAUDE_CONFIG_DIR/auth.json" 2>/dev/null || exit 3; exit 0;;
esac
IFS= read -r line
echo "$CLAUDE_CONFIG_DIR $*" >> "$AG_TEST_LOG"
echo "STDIN $line" >> "$AG_TEST_LOG"
case "$*" in
  *"--resume sess-first"*) cat "$AG_TEST_RESUME_OUT";;
  *) cat "$AG_TEST_FIRST_OUT";;
esac
`

func runLoggedBackupTask(t *testing.T, task Task) (*agBackupRun, []string) {
	t.Helper()
	f := newAGBackupFixture(t)
	t.Setenv("IS_SANDBOX", "1")
	fake := filepath.Join(t.TempDir(), "claude")
	writeTestExecutable(t, fake, []byte(agBackupFakeClaudeLogged))
	agents := map[string]AgentEntry{"claude": {Path: fake}}
	f.d.cfg.Agents = agents
	f.d.agentsAvailable.Store(&agents)

	dir := t.TempDir()
	logPath := filepath.Join(dir, "runs.log")
	first := filepath.Join(dir, "first.out")
	resume := filepath.Join(dir, "resume.out")
	resets := f.clock.now().Add(30 * time.Minute)
	if err := os.WriteFile(first, []byte(strings.ReplaceAll(agBackupRejectedFirst, "RESETS", itoa(resets.Unix()))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resume, []byte(agBackupResumeOK), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AG_TEST_LOG", logPath)
	t.Setenv("AG_TEST_FIRST_OUT", first)
	t.Setenv("AG_TEST_RESUME_OUT", resume)

	f.ready(t)
	f.d.executionEnvironmentCommand = nil
	f.d.workspaces = make(map[string]*workspaceState)
	f.d.activeEnvRoots = make(map[string]int)
	f.d.runtimeIndex["rt-claude"] = Runtime{ID: "rt-claude", Provider: "claude"}
	f.d.cfg.WorkspacesRoot = t.TempDir()
	f.d.cfg.AgentTimeout = 10 * time.Second

	task.WorkspaceID, task.IssueID, task.AgentID, task.AuthToken = "test-ws", "test-issue", "test-agent", "mat_test_backup"
	task.Agent = &AgentData{ID: "test-agent", Name: "test"}
	res, err := f.d.runTask(context.Background(), task, "claude", 0, discardLogger())
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	raw, _ := os.ReadFile(logPath)
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return &agBackupRun{d: f.d, f: f, log: string(raw), result: res}, lines
}

func TestAGPatchBackupResumeSessionAndPrompt(t *testing.T) {
	t.Run("AG patch: the resume carries the Spec's prompt, not the original one, and nothing else is run", func(t *testing.T) {
		r, _ := runLoggedBackupTask(t, urgentTask())
		// Each run logs "<config dir> <args>" and then its first stdin line; the
		// original prompt spans several lines, so count runs by their argument list.
		if got := strings.Count(r.log, " -p --output-format stream-json"); got != 2 {
			t.Fatalf("runs = %d, want the first run and one resume:\n%s", got, r.log)
		}
		i := strings.Index(r.log, filepath.Clean(r.f.backupDir)+" ")
		if i < 0 {
			t.Fatalf("no run under the backup dir:\n%s", r.log)
		}
		first, resume := r.log[:i], r.log[i:]
		if !strings.Contains(resume, "--resume sess-first") {
			t.Fatalf("resume = %q, want --resume sess-first", resume)
		}
		const want = "You hit a usage limit and have been moved to another account; continue where you left off"
		if !strings.Contains(resume, want) {
			t.Fatalf("resume prompt = %q, want it to contain %q", resume, want)
		}
		if strings.Contains(first, want) {
			t.Fatalf("the first run was given the resume prompt: %q", first)
		}
		if r.result.Status != "completed" {
			t.Fatalf("result = %+v", r.result)
		}
	})

	t.Run("AG patch: a task without an issue priority is not resumed on the backup", func(t *testing.T) {
		task := urgentTask()
		task.AGIssuePriority = "" // chat, or an autopilot run with no issue
		r, _ := runLoggedBackupTask(t, task)
		if got := strings.Count(r.log, " -p --output-format stream-json"); got != 1 || r.result.Status == "completed" {
			t.Fatalf("runs = %d status %s, want only the failed first run:\n%s", got, r.result.Status, r.log)
		}
	})
}

func TestAGPatchBackupNoIssueTaskStaysOnPrimary(t *testing.T) {
	t.Run("AG patch: a task without an issue priority keeps the primary config dir while the primary is held", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		env := map[string]string{}
		f.d.agApplyClaudeAccount("claude", Task{ID: "chat-1", RuntimeID: "rt-claude"}, env)
		if got := env["CLAUDE_CONFIG_DIR"]; got != "" {
			t.Fatalf("CLAUDE_CONFIG_DIR = %q, want it untouched", got)
		}
	})
}

func TestAGPatchBackupSetupLinksEverySpecItem(t *testing.T) {
	t.Run("AG patch: setup links each shared item the Spec lists and never links .claude.json", func(t *testing.T) {
		home := t.TempDir()
		primary := filepath.Join(home, ".claude")
		dirs := []string{"plugins", "skills", "agents", "commands", "hooks", "projects", "file-history", "todos", "shell-snapshots", "session-env", "plans"}
		files := []string{"settings.json", "CLAUDE.md", "keybindings.json", "statusline.sh", "statusline.py"}
		for _, d := range dirs {
			if err := os.MkdirAll(filepath.Join(primary, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(primary, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// A primary that keeps .claude.json inside its dir.
		if err := os.WriteFile(filepath.Join(primary, ".claude.json"), []byte(`{"oauthAccount":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		// Something not on the Spec's list must not be shared.
		if err := os.WriteFile(filepath.Join(primary, "history.jsonl"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runBackupSetup(t, home); err != nil {
			t.Fatalf("setup: %v\n%s", err, out)
		}
		backup := filepath.Join(home, ".claude-backup")
		for _, name := range append(append([]string{}, dirs...), files...) {
			dest, err := os.Readlink(filepath.Join(backup, name))
			if err != nil || dest != filepath.Join(primary, name) {
				t.Fatalf("%s -> %q (%v), want a link to the primary's", name, dest, err)
			}
		}
		if _, err := os.Lstat(filepath.Join(backup, "history.jsonl")); err == nil {
			t.Fatal("history.jsonl is not on the Spec's list but was linked")
		}
		fi, err := os.Lstat(filepath.Join(backup, ".claude.json"))
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf(".claude.json must be a copy, got %v (%v)", fi, err)
		}
	})
}
