package daemon

// AG patch: backup_urgent_until sends urgent Claude issues to the backup before
// the primary is held, at backup_max_concurrent with no pacing.

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// readyUrgent enables the backup for rt-claude with backup_urgent_until at until.
func (f *agBackupFixture) readyUrgent(t *testing.T, until time.Time) {
	t.Helper()
	f.ready(t)
	f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{
		BackupEnabled: true, BackupMaxConcurrent: 2, BackupUrgentUntil: until.UTC().Format(time.RFC3339),
	}, []string{"claim_filter"})
}

func TestAGPatchBackupUrgentUntil(t *testing.T) {
	backup := func(f *agBackupFixture) string { return filepath.Clean(f.backupDir) }
	swap := func(f *agBackupFixture, task Task) string {
		env := map[string]string{}
		f.d.agApplyClaudeAccount("claude", task, env)
		return env["CLAUDE_CONFIG_DIR"]
	}

	t.Run("AG patch: urgent issues are claimed onto the backup first, then the rest of the claim is the usual one", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.readyUrgent(t, f.clock.now().Add(12*time.Hour))
		claimWith(t, f, 5)
		want := []agClaimShape{
			{Runtimes: []string{"rt-claude"}, Max: 2, Filter: map[string]string{"rt-claude": "urgent"}},
			{Runtimes: []string{"rt-claude", "rt-codex", "rt-claude-b"}, Max: 5},
		}
		if got := claimShapes(t, f.srv); !reflect.DeepEqual(got, want) {
			t.Fatalf("claims = %+v, want %+v", got, want)
		}
	})

	t.Run("AG patch: urgent tasks claimed onto the backup leave fewer slots for the usual claim", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.readyUrgent(t, f.clock.now().Add(12*time.Hour))
		f.srv.reply = func(body map[string]any) string {
			if _, ok := body["ag_min_issue_priority"]; ok {
				return `{"tasks":[{"id":"u1","runtime_id":"rt-claude"}]}`
			}
			return `{"tasks":[]}`
		}
		res, err := f.d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude", "rt-codex", "rt-claude-b"}, 5)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if len(res.Tasks) != 1 || res.Tasks[0].ID != "u1" {
			t.Fatalf("tasks = %+v, want u1", res.Tasks)
		}
		if got := claimShapes(t, f.srv); len(got) != 2 || got[1].Max != 4 || got[1].Filter != nil {
			t.Fatalf("claims = %+v, want the usual claim for the 4 slots left", got)
		}
	})

	t.Run("AG patch: the backup's room counts its runs, and a full backup claims nothing early", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.readyUrgent(t, f.clock.now().Add(12*time.Hour))
		done := f.d.agPaceRunStart("b1", backup(f), f.clock.now())
		claimWith(t, f, 5)
		if got := claimShapes(t, f.srv); len(got) != 2 || got[0].Max != 1 || got[0].Filter["rt-claude"] != "urgent" {
			t.Fatalf("claims = %+v, want one urgent backup slot", got)
		}
		f.d.agPaceRunStart("b2", backup(f), f.clock.now())
		before := len(f.srv.bodies())
		claimWith(t, f, 5)
		if got := claimShapes(t, f.srv)[before:]; len(got) != 1 || got[0].Filter != nil {
			t.Fatalf("claims with the backup full = %+v, want only the usual claim", got)
		}
		done()
	})

	t.Run("AG patch: nothing changes once backup_urgent_until has passed", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.readyUrgent(t, f.clock.now().Add(time.Hour))
		f.clock.set(f.clock.now().Add(time.Hour + time.Second))
		claimWith(t, f, 5)
		if got := claimShapes(t, f.srv); len(got) != 1 || got[0].Filter != nil {
			t.Fatalf("claims = %+v, want only the usual claim", got)
		}
		if got := swap(f, urgentTask()); got != "" {
			t.Fatalf("CLAUDE_CONFIG_DIR = %q after backup_urgent_until, want it untouched", got)
		}
	})

	t.Run("AG patch: no early claim while the backup is on hold or disabled", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.readyUrgent(t, f.clock.now().Add(12*time.Hour))
		f.hold(t, map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir})
		claimWith(t, f, 5)
		if got := claimShapes(t, f.srv); len(got) != 1 || got[0].Filter != nil {
			t.Fatalf("claims with the backup held = %+v, want only the usual claim", got)
		}

		g := newAGBackupFixture(t)
		g.readyUrgent(t, g.clock.now().Add(12*time.Hour))
		g.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{
			BackupEnabled: false, BackupMaxConcurrent: 2, BackupUrgentUntil: g.clock.now().Add(12 * time.Hour).UTC().Format(time.RFC3339),
		}, []string{"claim_filter"})
		claimWith(t, g, 5)
		if got := claimShapes(t, g.srv); len(got) != 1 || got[0].Filter != nil {
			t.Fatalf("claims with the backup disabled = %+v, want only the usual claim", got)
		}
	})

	t.Run("AG patch: an urgent task runs on the backup without a hold; other tasks stay on the primary", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.readyUrgent(t, f.clock.now().Add(12*time.Hour))
		if got := swap(f, urgentTask()); got != backup(f) {
			t.Fatalf("urgent task CLAUDE_CONFIG_DIR = %q, want the backup", got)
		}
		if got := swap(f, Task{ID: "n1", RuntimeID: "rt-claude", AGIssuePriority: "high"}); got != "" {
			t.Fatalf("high task CLAUDE_CONFIG_DIR = %q, want it untouched", got)
		}
	})

	t.Run("AG patch: while the primary is held the backup claims at backup_max_concurrent, not its paced cap", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		now := f.clock.now()
		reset5 := now.Add(3 * time.Hour)
		// As TestAGPatchBackupPacing: this curve paces the backup to one run.
		backupEvent(f, 0.70, reset5)
		d1 := f.d.agPaceRunStart("b1", backup(f), f.clock.now())
		d2 := f.d.agPaceRunStart("b2", backup(f), f.clock.now())
		f.clock.set(now.Add(30 * time.Minute))
		backupEvent(f, 0.80, reset5)
		f.clock.set(now.Add(time.Hour))
		backupEvent(f, 0.85, reset5)
		d1()
		d2()

		f.holdPrimary(t)
		f.readyUrgent(t, f.clock.now().Add(12*time.Hour))
		claimWith(t, f, 5)
		got := claimShapes(t, f.srv)
		if len(got) == 0 || got[0].Max != 2 || got[0].Filter["rt-claude"] != "urgent" {
			t.Fatalf("claims = %+v, want two urgent backup slots", got)
		}
	})

	t.Run("AG patch: an unparseable backup_urgent_until counts as unset", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 2, BackupUrgentUntil: "soon"}, []string{"claim_filter"})
		if f.d.agBackupUrgentNow("rt-claude") {
			t.Fatal("agBackupUrgentNow = true for an unparseable value")
		}
	})
}
