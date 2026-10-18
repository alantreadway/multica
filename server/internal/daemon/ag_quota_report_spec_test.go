package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Spec-derived cases for the heartbeat report and the backup setting. They sit
// beside ag_quota_report_test.go and reuse its fixtures.

func TestAGPatchQuotaReportSpecCases(t *testing.T) {
	t.Run("AG patch: max is the configured concurrency limit", func(t *testing.T) {
		for _, max := range []int{1, 3, 7} {
			f := newAGPaceFixture(t, max)
			if r := f.d.agQuotaReport("rt-claude"); r == nil || r.Max != max {
				t.Fatalf("max = %v, want %d", r, max)
			}
		}
	})

	t.Run("AG patch: with no hold the cap is reported as the limiter's, never above max", func(t *testing.T) {
		f := newAGPaceFixture(t, 4)
		r := f.d.agQuotaReport("rt-claude")
		if r.Hold != nil || r.EffectiveCap != 4 || r.EffectiveCap > r.Max {
			t.Fatalf("cap/max/hold = %d/%d/%v, want an unheld full cap of 4", r.EffectiveCap, r.Max, r.Hold)
		}
	})

	t.Run("AG patch: a seven_day rejection reports a seven_day hold and cap 0", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		reset7 := now.Add(48 * time.Hour)
		raw := fmt.Sprintf(`{"status":"rejected","rateLimitType":"seven_day","resetsAt":%d,"unifiedWindows":{"seven_day":{"utilization":1,"resetsAt":%d}}}`, reset7.Unix(), reset7.Unix())
		agQuotaObs(f.d, Task{ID: "t7"}, "claude", nil)(json.RawMessage(raw))

		r := f.d.agQuotaReport("rt-claude")
		if r.EffectiveCap != 0 || r.Hold == nil || r.Hold.Window != "seven_day" {
			t.Fatalf("report = cap %d hold %+v, want cap 0 and a seven_day hold", r.EffectiveCap, r.Hold)
		}
		if _, err := time.Parse(time.RFC3339, r.Hold.Until); err != nil {
			t.Fatalf("hold.until %q is not RFC 3339: %v", r.Hold.Until, err)
		}
		if u := r.Windows["seven_day"].Utilization; u == nil || *u != 1 {
			t.Fatalf("seven_day utilisation = %v, want 1", u)
		}
	})

	t.Run("AG patch: the report always describes the primary account in this unit", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		f.d.agApplySettings("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 4})
		r := f.d.agQuotaReport("rt-claude")
		if r.Account != "primary" || r.Backup.Detected || r.Backup.Valid || len(r.BackupWindows) != 0 {
			t.Fatalf("enabling the setting changed detection state: %+v / %v", r.Backup, r.BackupWindows)
		}
	})

	t.Run("AG patch: an expired setting shows as disabled in the next report", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		f.d.agApplySettings("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 4})
		if b := f.d.agQuotaReport("rt-claude").Backup; !b.Enabled || b.MaxConcurrent != 4 {
			t.Fatalf("fresh setting not reported: %+v", b)
		}
		f.clock.set(f.clock.now().Add(agSettingsTTL + time.Second))
		if b := f.d.agQuotaReport("rt-claude").Backup; b.Enabled || b.MaxConcurrent != 2 {
			t.Fatalf("expired setting reported as %+v, want disabled with 2", b)
		}
	})
}

func TestAGPatchBackupSettingSpecCases(t *testing.T) {
	t.Run("AG patch: each runtime keeps its own setting and its own expiry", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		start := f.clock.now()
		f.d.agApplySettings("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 3})
		f.clock.set(start.Add(agSettingsTTL - time.Minute))
		f.d.agApplySettings("rt-claude-b", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 5})
		f.clock.set(start.Add(agSettingsTTL + time.Second))

		if s := f.d.agBackupSetting("rt-claude"); s.BackupEnabled {
			t.Fatalf("rt-claude = %+v, want expired", s)
		}
		if s := f.d.agBackupSetting("rt-claude-b"); !s.BackupEnabled || s.BackupMaxConcurrent != 5 {
			t.Fatalf("rt-claude-b = %+v, want its own live setting", s)
		}
	})

	t.Run("AG patch: an ack with a null or unknown-field setting is handled", func(t *testing.T) {
		srv := newAGReportServer(t, `{"status":"ok","ag_settings":{"backup_enabled":true,"backup_max_concurrent":3,"future_key":"x"}}`)
		f := newAGPaceFixtureAt(t, 5, srv.URL)
		f.d.runHeartbeatTick(context.Background(), "rt-claude")
		if s := f.d.agBackupSetting("rt-claude"); !s.BackupEnabled || s.BackupMaxConcurrent != 3 {
			t.Fatalf("a setting with an extra key was not applied: %+v", s)
		}
		srv.setAck(`{"status":"ok","ag_settings":null}`)
		f.d.runHeartbeatTick(context.Background(), "rt-claude")
		if s := f.d.agBackupSetting("rt-claude"); !s.BackupEnabled {
			t.Fatalf("a null setting cleared the last one inside the TTL: %+v", s)
		}
	})

	t.Run("AG patch: settings and reports are safe to use from heartbeat and claim goroutines at once", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				for j := 0; j < 200; j++ {
					f.d.agApplySettings("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: j%2 == 0, BackupMaxConcurrent: 1 + i})
					if s := f.d.agBackupSetting("rt-claude"); s.BackupMaxConcurrent < 1 || s.BackupMaxConcurrent > 8 {
						t.Errorf("read a setting nobody wrote: %+v", s)
					}
					if r := f.d.agQuotaReport("rt-claude"); r == nil || r.Max != 5 {
						t.Errorf("read an incomplete report: %+v", r)
					}
				}
			}(i)
		}
		wg.Wait()
	})
}
