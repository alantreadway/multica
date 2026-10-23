package agent

// AG patch: backup Claude account failover. A warm Claude process is bound to
// the CLAUDE_CONFIG_DIR it was spawned with, so the pool must never hand it to
// a run on the other account.

import (
	"log/slog"
	"testing"
)

func TestAGPatchWarmFingerprintSeparatesClaudeAccounts(t *testing.T) {
	fp := func(env map[string]string) string {
		b := &claudeBackend{cfg: Config{Logger: slog.Default(), Env: env}}
		return b.warmFingerprint("claude", ExecOptions{Cwd: "/work"})
	}

	t.Run("AG patch: same session, primary versus backup config dir", func(t *testing.T) {
		primary := fp(map[string]string{"MULTICA_TASK_ID": "t"})
		backup := fp(map[string]string{"MULTICA_TASK_ID": "t", "CLAUDE_CONFIG_DIR": "/home/u/.claude-backup"})
		if primary == backup {
			t.Fatal("a process spawned on the primary account would be reused for a backup run")
		}
	})

	t.Run("AG patch: two different backup dirs differ", func(t *testing.T) {
		a := fp(map[string]string{"CLAUDE_CONFIG_DIR": "/a"})
		b := fp(map[string]string{"CLAUDE_CONFIG_DIR": "/b"})
		if a == b {
			t.Fatal("config dirs must be part of the fingerprint")
		}
	})

	t.Run("AG patch: resume in place swaps the dir on the shared env map", func(t *testing.T) {
		env := map[string]string{}
		b := &claudeBackend{cfg: Config{Logger: slog.Default(), Env: env}}
		before := b.warmFingerprint("claude", ExecOptions{Cwd: "/work"})
		env["CLAUDE_CONFIG_DIR"] = "/backup"
		after := b.warmFingerprint("claude", ExecOptions{Cwd: "/work", ResumeSessionID: "s1"})
		if before == after {
			t.Fatal("the resumed turn must not match the process parked on the primary account")
		}
	})
}
