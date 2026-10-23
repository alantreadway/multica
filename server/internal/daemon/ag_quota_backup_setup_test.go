package daemon

// AG patch: backup account setup script, driven on a temp HOME.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runBackupSetup(t *testing.T, home string, extraEnv ...string) (string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "ag-patches", "claude-backup-setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAGPatchBackupSetupScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash script")
	}
	newHome := func(t *testing.T) (home, primary string) {
		t.Helper()
		home = t.TempDir()
		primary = filepath.Join(home, ".claude")
		for _, d := range []string{"skills", "projects", "plugins"} {
			if err := os.MkdirAll(filepath.Join(primary, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, f := range []string{"settings.json", "CLAUDE.md", "statusline.sh"} {
			if err := os.WriteFile(filepath.Join(primary, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"primary"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return home, primary
	}

	t.Run("AG patch: setup links the shared items, copies .claude.json and skips what the primary lacks", func(t *testing.T) {
		home, primary := newHome(t)
		out, err := runBackupSetup(t, home)
		if err != nil {
			t.Fatalf("setup: %v\n%s", err, out)
		}
		backup := filepath.Join(home, ".claude-backup")
		for _, name := range []string{"settings.json", "CLAUDE.md", "statusline.sh", "skills", "projects", "plugins"} {
			dest, err := os.Readlink(filepath.Join(backup, name))
			if err != nil || dest != filepath.Join(primary, name) {
				t.Fatalf("%s -> %q (%v), want a link to the primary's", name, dest, err)
			}
		}
		for _, name := range []string{"agents", "commands", "hooks", "keybindings.json", "plans"} {
			if _, err := os.Lstat(filepath.Join(backup, name)); err == nil {
				t.Fatalf("%s linked although the primary has none", name)
			}
		}
		fi, err := os.Lstat(filepath.Join(backup, ".claude.json"))
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf(".claude.json must be a copy, got %v (%v)", fi, err)
		}
		if !strings.Contains(out, "claude auth login") || !strings.Contains(out, backup) {
			t.Fatalf("output does not tell Alan how to log in:\n%s", out)
		}
		if fi, _ := os.Stat(backup); fi.Mode().Perm() != 0o700 {
			t.Fatalf("backup dir mode = %v", fi.Mode().Perm())
		}
		if _, err := os.Stat(filepath.Join(backup, "credentials")); err == nil {
			t.Fatal("setup created credentials")
		}
	})

	t.Run("AG patch: running setup again changes nothing", func(t *testing.T) {
		home, _ := newHome(t)
		if out, err := runBackupSetup(t, home); err != nil {
			t.Fatalf("first run: %v\n%s", err, out)
		}
		backup := filepath.Join(home, ".claude-backup")
		if err := os.WriteFile(filepath.Join(backup, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"backup"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := runBackupSetup(t, home)
		if err != nil {
			t.Fatalf("second run: %v\n%s", err, out)
		}
		if strings.Contains(out, "linked ") || strings.Contains(out, "copied") {
			t.Fatalf("second run changed something:\n%s", out)
		}
		raw, _ := os.ReadFile(filepath.Join(backup, ".claude.json"))
		if !strings.Contains(string(raw), "backup") {
			t.Fatalf("the backup's own .claude.json was overwritten: %s", raw)
		}
	})

	t.Run("AG patch: setup never overwrites a real file and exits 1 after finishing the rest", func(t *testing.T) {
		home, _ := newHome(t)
		backup := filepath.Join(home, ".claude-backup")
		if err := os.MkdirAll(backup, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backup, "settings.json"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/elsewhere", filepath.Join(backup, "skills")); err != nil {
			t.Fatal(err)
		}
		out, err := runBackupSetup(t, home)
		if err == nil {
			t.Fatalf("setup succeeded over a real file:\n%s", out)
		}
		if raw, _ := os.ReadFile(filepath.Join(backup, "settings.json")); string(raw) != "mine" {
			t.Fatalf("settings.json = %q", raw)
		}
		if dest, _ := os.Readlink(filepath.Join(backup, "skills")); dest != "/elsewhere" {
			t.Fatalf("skills link changed to %q", dest)
		}
		if _, err := os.Readlink(filepath.Join(backup, "CLAUDE.md")); err != nil {
			t.Fatalf("the rest was not finished: %v", err)
		}
		if !strings.Contains(out, "conflict") {
			t.Fatalf("no conflict reported:\n%s", out)
		}
	})

	t.Run("AG patch: setup honours MULTICA_AG_CLAUDE_BACKUP_DIR and refuses the primary dir", func(t *testing.T) {
		home, primary := newHome(t)
		custom := filepath.Join(t.TempDir(), "custom-backup")
		if out, err := runBackupSetup(t, home, "MULTICA_AG_CLAUDE_BACKUP_DIR="+custom); err != nil {
			t.Fatalf("setup: %v\n%s", err, out)
		}
		if _, err := os.Readlink(filepath.Join(custom, "CLAUDE.md")); err != nil {
			t.Fatalf("custom dir not set up: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude-backup")); err == nil {
			t.Fatal("the default dir was created as well")
		}
		if out, err := runBackupSetup(t, home, "MULTICA_AG_CLAUDE_BACKUP_DIR="+primary); err == nil {
			t.Fatalf("setup accepted the primary dir as the backup:\n%s", out)
		}
	})

	t.Run("AG patch: setup expands a leading ~ in the backup dir and reports a symlinked .claude.json as a conflict", func(t *testing.T) {
		home, _ := newHome(t)
		if out, err := runBackupSetup(t, home, "MULTICA_AG_CLAUDE_BACKUP_DIR=~/second-backup"); err != nil {
			t.Fatalf("setup: %v\n%s", err, out)
		}
		if _, err := os.Readlink(filepath.Join(home, "second-backup", "CLAUDE.md")); err != nil {
			t.Fatalf("~ was not expanded: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, "~")); err == nil {
			t.Fatal("a literal ~ directory was created")
		}

		linked := filepath.Join(home, "linked-backup")
		if err := os.MkdirAll(linked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(home, ".claude.json"), filepath.Join(linked, ".claude.json")); err != nil {
			t.Fatal(err)
		}
		out, err := runBackupSetup(t, home, "MULTICA_AG_CLAUDE_BACKUP_DIR="+linked)
		if err == nil || !strings.Contains(out, "conflict") || !strings.Contains(out, ".claude.json") {
			t.Fatalf("a symlinked .claude.json was accepted: err %v\n%s", err, out)
		}
	})

	t.Run("AG patch: setup fails clearly when the primary dir is missing", func(t *testing.T) {
		out, err := runBackupSetup(t, t.TempDir())
		if err == nil || !strings.Contains(out, "does not exist") {
			t.Fatalf("err %v output %s", err, out)
		}
	})
}
