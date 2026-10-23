package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestAGPatchBackupIdentityEdges(t *testing.T) {
	check := func(t *testing.T, f *agBackupFixture) agBackupStatus {
		t.Helper()
		f.d.agBackupCheck(context.Background())
		return f.d.agBackupStatusNow()
	}
	login := func(email, org, plan string) string {
		return fmt.Sprintf(`{"loggedIn":true,"authMethod":"claude.ai","email":%q,"orgId":%q,"subscriptionType":%q}`, email, org, plan)
	}

	t.Run("AG patch: a blank orgId counts as missing, so the email decides", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.setAuth(f.acct, login("alan@both.test", "  ", "max"))
		f.setAuth(f.backupDir, login("alan@both.test", "org-company", "enterprise"))
		if st := check(t, f); st.Valid || st.Reason != "same_account" {
			t.Fatalf("blank primary orgId, same email: %+v", st)
		}
		f.setAuth(f.backupDir, login("alan@backup.test", "", "enterprise"))
		if st := check(t, f); !st.Valid || st.Reason != "" {
			t.Fatalf("blank orgIds, different email: %+v", st)
		}
	})

	t.Run("AG patch: orgIds are compared without surrounding whitespace", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.setAuth(f.acct, login("alan@primary.test", " org-1 ", "max"))
		f.setAuth(f.backupDir, login("alan@backup.test", "org-1", "enterprise"))
		if st := check(t, f); st.Valid || st.Reason != "same_account" {
			t.Fatalf("status = %+v", st)
		}
	})

	t.Run("AG patch: different orgIds are valid whatever the emails say", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.setAuth(f.acct, login("alan@primary.test", "org-personal", "max"))
		f.setAuth(f.backupDir, login("alan@backup.test", "org-company", "enterprise"))
		if st := check(t, f); !st.Valid {
			t.Fatalf("different emails and orgs: %+v", st)
		}
		f.setAuth(f.backupDir, login("", "org-company", "enterprise"))
		if st := check(t, f); !st.Valid {
			t.Fatalf("backup email missing: %+v", st)
		}
	})

	t.Run("AG patch: an Enterprise backup reporting an unknown window is not paced or held", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		env := map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir}
		now := f.clock.now()
		raw := fmt.Sprintf(`{"status":"allowed","rateLimitType":"weekly_credits","utilization":0.99,"resetsAt":%d}`, now.Add(2*time.Hour).Unix())
		agQuotaObs(f.d, Task{ID: "ent-unknown"}, "claude", env)(json.RawMessage(raw))

		if c, _ := f.d.agPaceEvaluate(f.backupDir, f.d.agQuotaAccountWindows(f.backupDir), false, 2, now); c != 2 {
			t.Fatalf("cap = %d, want the backup max with no pacing", c)
		}
		if f.d.agQuotaAccountHold(f.backupDir) != nil {
			t.Fatal("an allowed unknown window set a hold")
		}
	})
}
