package daemon

// AG patch: quota hook 4 through its real caller. runTask launches a test-created
// claude that prints the rejected rate-limit fixture; no installed CLI is used.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// agQuotaRunTask runs one claude task through runTask against the rejected
// fixture and returns the daemon, its claim server and the default account dir.
func agQuotaRunTask(t *testing.T, customEnv map[string]string) (*Daemon, *agQuotaClaimServer, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script agent fixture is POSIX-only")
	}
	defaultDir := agQuotaEnv(t)
	t.Setenv("IS_SANDBOX", "1") // the claude backend refuses bypassPermissions as root, e.g. in a container
	fixture, err := filepath.Abs(filepath.Join("..", "..", "pkg", "agent", "testdata", "claude-code-rate-limit-rejected.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "claude")
	writeTestExecutable(t, fake, []byte("#!/bin/sh\ncase \"$*\" in *--version*) echo '2.1.299'; exit 0;; esac\nread -r _\ncat '"+fixture+"'\n"))

	s := newAGQuotaClaimServer(t)
	clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
	d := newAGQuotaDaemon(t, s.URL, filepath.Join(t.TempDir(), "ag_quota.json"), clock)
	d.executionEnvironmentCommand = nil // prepare in-process, as the other focused runTask tests do
	d.workspaces = make(map[string]*workspaceState)
	d.activeEnvRoots = make(map[string]int)
	d.runtimeIndex["test-rt"] = Runtime{ID: "test-rt", Provider: "claude"}
	d.cfg.WorkspacesRoot = t.TempDir()
	d.cfg.AgentTimeout = 10 * time.Second
	agents := map[string]AgentEntry{"claude": {Path: fake}}
	d.cfg.Agents = agents
	d.agentsAvailable.Store(&agents)

	task := Task{
		ID: "rate-limit-task", WorkspaceID: "test-ws", RuntimeID: "test-rt", IssueID: "test-issue",
		AgentID: "test-agent", AuthToken: "mat_test_quota",
		Agent: &AgentData{ID: "test-agent", Name: "test", CustomEnv: customEnv},
	}
	if _, err := d.runTask(context.Background(), task, "claude", 0, discardLogger()); err != nil {
		t.Fatalf("runTask: %v", err)
	}
	return d, s, defaultDir
}

func TestAGPatchQuotaRunTaskHoldsDefaultAccount(t *testing.T) {
	d, s, defaultDir := agQuotaRunTask(t, nil)
	h := d.agQuotaHealth().ClaudeQuota
	if h == nil || !h.Held || h.Hold == nil || h.Hold.Account != defaultDir || !h.Hold.Gating || len(h.Accounts) != 1 {
		t.Fatalf("health = %+v, want a gating hold on %s", h, defaultDir)
	}
	agQuotaClaimAll(t, d)
	got := s.claimed()
	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != "rt-codex" {
		t.Fatalf("claim = %v, want only the Codex runtime while the default account is held", got)
	}
}

func TestAGPatchQuotaRunTaskHoldsAgentOwnAccount(t *testing.T) {
	other := filepath.Join(t.TempDir(), "agent-claude")
	d, s, defaultDir := agQuotaRunTask(t, map[string]string{"CLAUDE_CONFIG_DIR": other})
	h := d.agQuotaHealth().ClaudeQuota
	if h == nil || h.Held || len(h.Accounts) != 1 {
		t.Fatalf("health = %+v, want one tracked account and no gating hold", h)
	}
	acct := h.Accounts[0]
	if acct.Account != other || acct.Account == defaultDir || acct.Default || acct.Hold == nil || acct.Hold.Gating {
		t.Fatalf("account = %+v, want a non-gating hold on %s", acct, other)
	}
	agQuotaClaimAll(t, d)
	if got := s.claimed(); len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("claim = %v, want all three runtimes", got)
	}
	if _, err := os.Stat(d.agQuota.path); err != nil {
		t.Fatalf("state file: %v", err)
	}
}
