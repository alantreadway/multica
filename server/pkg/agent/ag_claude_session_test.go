package agent

// AG patch: Claude resume window. ClaudeSessionModTime is the age source for
// the daemon's decision, so it has to follow findClaudeSessionFile's rule
// (newest <id>.jsonl under any project) and fail, not guess, when it can't tell.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func agWriteClaudeTranscript(t *testing.T, configDir, project, sessionID string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(configDir, "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAGPatchClaudeSessionModTimeReturnsTranscriptMtime(t *testing.T) {
	configDir := t.TempDir()
	want := time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC)
	agWriteClaudeTranscript(t, configDir, "-work-tree", "sess-1", want)

	got, err := ClaudeSessionModTime([]string{"CLAUDE_CONFIG_DIR=" + configDir}, t.TempDir(), "sess-1")
	if err != nil {
		t.Fatalf("ClaudeSessionModTime: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("mtime = %v, want %v", got, want)
	}
}

func TestAGPatchClaudeSessionModTimeUsesNewestCopyAcrossProjects(t *testing.T) {
	configDir := t.TempDir()
	old := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	newest := old.Add(2 * time.Hour)
	agWriteClaudeTranscript(t, configDir, "-old-worktree", "sess-1", old)
	agWriteClaudeTranscript(t, configDir, "-new-worktree", "sess-1", newest)
	agWriteClaudeTranscript(t, configDir, "-other", "sess-2", newest.Add(time.Hour)) // different session, ignored

	got, err := ClaudeSessionModTime([]string{"CLAUDE_CONFIG_DIR=" + configDir}, "", "sess-1")
	if err != nil {
		t.Fatalf("ClaudeSessionModTime: %v", err)
	}
	if !got.Equal(newest) {
		t.Fatalf("mtime = %v, want the newest copy %v", got, newest)
	}
}

func TestAGPatchClaudeSessionModTimeResolvesConfigDirFromHome(t *testing.T) {
	home := t.TempDir()
	want := time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC)
	agWriteClaudeTranscript(t, filepath.Join(home, ".claude"), "-p", "sess-1", want)

	got, err := ClaudeSessionModTime([]string{"HOME=" + home}, "", "sess-1")
	if err != nil {
		t.Fatalf("ClaudeSessionModTime: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("mtime = %v, want %v", got, want)
	}
}

func TestAGPatchClaudeSessionModTimeCLAUDEConfigDirWinsOverHome(t *testing.T) {
	home := t.TempDir()
	configDir := t.TempDir()
	agWriteClaudeTranscript(t, filepath.Join(home, ".claude"), "-p", "sess-1", time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC))
	want := time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC)
	agWriteClaudeTranscript(t, configDir, "-p", "sess-1", want)

	got, err := ClaudeSessionModTime([]string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + configDir}, "", "sess-1")
	if err != nil {
		t.Fatalf("ClaudeSessionModTime: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("mtime = %v, want the CLAUDE_CONFIG_DIR copy %v", got, want)
	}
}

func TestAGPatchClaudeSessionModTimeMissingTranscriptIsAnError(t *testing.T) {
	configDir := t.TempDir()
	agWriteClaudeTranscript(t, configDir, "-p", "other-session", time.Now())

	_, err := ClaudeSessionModTime([]string{"CLAUDE_CONFIG_DIR=" + configDir}, "", "sess-1")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestAGPatchClaudeSessionModTimeMissingProjectsDirIsAnError(t *testing.T) {
	_, err := ClaudeSessionModTime([]string{"CLAUDE_CONFIG_DIR=" + filepath.Join(t.TempDir(), "absent")}, "", "sess-1")
	if err == nil {
		t.Fatal("want an error when the config dir has no projects directory")
	}
}

func TestAGPatchClaudeSessionModTimeRejectsUnsafeSessionIDs(t *testing.T) {
	configDir := t.TempDir()
	agWriteClaudeTranscript(t, configDir, "-p", "sess-1", time.Now())
	for _, id := range []string{"", ".", "..", "../sess-1", "a/b", `a\b`} {
		if _, err := ClaudeSessionModTime([]string{"CLAUDE_CONFIG_DIR=" + configDir}, "", id); err == nil {
			t.Errorf("id %q: want an error", id)
		}
	}
}
