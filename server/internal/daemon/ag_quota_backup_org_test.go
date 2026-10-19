package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// agBackupEnterpriseEvent is the rate_limit_event an Enterprise login emits
// (captured from `claude -p` with stream-json output, ids removed): one overage
// window and neither five_hour nor seven_day.
func agBackupEnterpriseEvent(status string, resets time.Time) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"type":"rate_limit_event","rate_limit_info":{"status":%q,"resetsAt":%d,"rateLimitType":"overage","overageStatus":"allowed","overageResetsAt":%d,"isUsingOverage":false,"overageInUse":true}}`,
		status, resets.Unix(), resets.Unix()))
}

func TestAGPatchBackupAccountState(t *testing.T) {
	t.Run("AG patch: two config dirs with the same email keep separate holds and pacing", func(t *testing.T) {
		f := newAGBackupFixture(t)
		same := `{"loggedIn":true,"authMethod":"claude.ai","email":"alan@both.test","orgId":"%s","subscriptionType":"max"}`
		f.setAuth(f.acct, fmt.Sprintf(same, "org-personal"))
		f.setAuth(f.backupDir, fmt.Sprintf(same, "org-company"))
		f.ready(t)

		now := f.clock.now()
		f.event(t, 0.9, 0.1, now.Add(4*time.Hour), now.Add(6*24*time.Hour)) // the primary is deep into its session
		backupEnv := map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir}
		fresh := fmt.Sprintf(`{"status":"allowed","rateLimitType":"five_hour","resetsAt":%d,"unifiedWindows":{"five_hour":{"utilization":0.05,"resetsAt":%d},"seven_day":{"utilization":0.05,"resetsAt":%d}}}`,
			now.Add(4*time.Hour).Unix(), now.Add(4*time.Hour).Unix(), now.Add(6*24*time.Hour).Unix())
		agQuotaObs(f.d, Task{ID: "evt-backup"}, "claude", backupEnv)(json.RawMessage(fresh))
		primaryWindows, backupWindows := f.d.agQuotaDefaultWindows(), f.d.agQuotaAccountWindows(f.backupDir)
		if len(primaryWindows) == 0 || len(backupWindows) == 0 {
			t.Fatalf("each dir should have its own windows: primary %v backup %v", primaryWindows, backupWindows)
		}
		if *primaryWindows[agQuotaWindowFiveHour].Utilization == *backupWindows[agQuotaWindowFiveHour].Utilization {
			t.Fatalf("windows leaked between the dirs: primary %v backup %v", primaryWindows, backupWindows)
		}

		done := f.d.agPaceRunStart("run-backup", f.backupDir, now)
		defer done()
		pc, pa := f.d.agPaceEvaluate(f.acct, primaryWindows, false, 5, now)
		bc, ba := f.d.agPaceEvaluate(f.backupDir, backupWindows, false, 5, now)
		if pc >= 5 || bc != 5 {
			t.Fatalf("pacing is shared: primary cap %d, backup cap %d", pc, bc)
		}
		if pa != 0 || ba != 1 {
			t.Fatalf("active runs are shared: primary %d, backup %d", pa, ba)
		}

		f.holdPrimary(t)
		if f.d.agQuotaAccountHold(f.backupDir) != nil {
			t.Fatal("the primary's hold leaked onto the backup")
		}
		f.hold(t, map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir})
		if f.d.agQuotaAccountHold(f.backupDir) == nil || f.d.agQuotaAccountHold(f.acct) == nil {
			t.Fatal("each dir should hold on its own")
		}
	})

	t.Run("AG patch: an Enterprise backup that reports only an overage window is not paced", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		env := map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir}
		now := f.clock.now()
		agQuotaObs(f.d, Task{ID: "ent-1"}, "claude", env)(agBackupEnterpriseEvent("allowed", now.Add(24*time.Hour)))

		if c, a := f.d.agPaceEvaluate(f.backupDir, f.d.agQuotaAccountWindows(f.backupDir), false, 2, now); c != 2 || a != 0 {
			t.Fatalf("cap/active = %d/%d, want the backup max with no pacing", c, a)
		}
		r := f.d.agQuotaReport("rt-claude")
		if len(r.BackupWindows) != 0 || r.Account != "backup" || r.Max != 2 || r.EffectiveCap != 2 {
			t.Fatalf("report = account %s max %d cap %d windows %+v", r.Account, r.Max, r.EffectiveCap, r.BackupWindows)
		}
		f.d.agBackupCheck(context.Background())
		if !f.d.agBackupStatusNow().Valid {
			t.Fatal("the overage event invalidated the backup")
		}
	})

	t.Run("AG patch: a rejected overage window still holds the Enterprise backup", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		env := map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir}
		agQuotaObs(f.d, Task{ID: "ent-2"}, "claude", env)(agBackupEnterpriseEvent("rejected", f.clock.now().Add(time.Hour)))
		if hold := f.d.agQuotaAccountHold(f.backupDir); hold == nil || hold.Window != "overage" {
			t.Fatalf("hold = %+v", hold)
		}
		if r := f.d.agQuotaReport("rt-claude"); r.EffectiveCap != 0 {
			t.Fatalf("cap = %d with the backup held", r.EffectiveCap)
		}
	})
}
