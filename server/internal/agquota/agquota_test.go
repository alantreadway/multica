package agquota

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeStore models the guarded query: a stored report, rows changed 0 when the
// write would not change it, and the runtime's stored ag_settings alongside.
type fakeStore struct {
	stored   []byte
	settings []byte
	sets     int
	fail     error
}

func (f *fakeStore) SetAGQuota(_ context.Context, _ pgtype.UUID, report []byte) (bool, []byte, error) {
	f.sets++
	if f.fail != nil {
		return false, nil, f.fail
	}
	if string(f.stored) == string(report) {
		return false, f.settings, nil
	}
	f.stored = report
	return true, f.settings, nil
}

var (
	t0      = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	runtime = pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
)

func report(util float64) *protocol.DaemonQuotaReport {
	return &protocol.DaemonQuotaReport{
		Account:      "primary",
		EffectiveCap: 3,
		Max:          5,
		Windows: map[string]protocol.DaemonQuotaWindow{
			"five_hour": {Utilization: &util, ResetsAt: "2026-10-18T14:00:00Z", ObservedAt: "2026-10-18T12:00:00Z"},
		},
		Backup:        protocol.DaemonQuotaBackup{MaxConcurrent: 2},
		BackupWindows: map[string]protocol.DaemonQuotaWindow{},
	}
}

func TestAGPatchQuotaStoreOnChange(t *testing.T) {
	ctx := context.Background()

	t.Run("AG patch: a changed report writes, the same report again does not", func(t *testing.T) {
		store := &fakeStore{}
		memo, changed, err := Apply(ctx, store, runtime, Memo{}, report(0.5), t0)
		if err != nil || !changed || store.sets != 1 {
			t.Fatalf("first report: changed=%v sets=%d err=%v", changed, store.sets, err)
		}
		for i := 1; i <= 5; i++ {
			memo, changed, err = Apply(ctx, store, runtime, memo, report(0.5), t0.Add(time.Duration(i)*time.Second))
			if err != nil || changed {
				t.Fatalf("repeat %d: changed=%v err=%v", i, changed, err)
			}
		}
		if store.sets != 1 {
			t.Fatalf("sets = %d after repeats, want 1", store.sets)
		}
		_, changed, _ = Apply(ctx, store, runtime, memo, report(0.51), t0.Add(10*time.Second))
		if !changed || store.sets != 2 {
			t.Fatalf("a changed utilisation: changed=%v sets=%d, want a second write", changed, store.sets)
		}
	})

	t.Run("AG patch: the guarded query is re-run every refresh interval and reports no change", func(t *testing.T) {
		store := &fakeStore{}
		memo, _, _ := Apply(ctx, store, runtime, Memo{}, report(0.5), t0)
		memo, changed, err := Apply(ctx, store, runtime, memo, report(0.5), t0.Add(RefreshInterval-time.Second))
		if err != nil || changed || store.sets != 1 {
			t.Fatalf("inside the interval: changed=%v sets=%d err=%v", changed, store.sets, err)
		}
		_, changed, err = Apply(ctx, store, runtime, memo, report(0.5), t0.Add(RefreshInterval))
		if err != nil || changed || store.sets != 2 {
			t.Fatalf("at the interval: changed=%v sets=%d err=%v, want a no-op second query", changed, store.sets, err)
		}
	})

	t.Run("AG patch: a nil report touches nothing and keeps the memo", func(t *testing.T) {
		store := &fakeStore{}
		memo := Memo{Known: true, Settings: &protocol.AGRuntimeSettings{BackupMaxConcurrent: 3}}
		got, changed, err := Apply(ctx, store, runtime, memo, nil, t0)
		if err != nil || changed || store.sets != 0 || got.Settings == nil || got.Settings.BackupMaxConcurrent != 3 {
			t.Fatalf("nil report: memo=%+v changed=%v sets=%d err=%v", got, changed, store.sets, err)
		}
	})

	t.Run("AG patch: a store failure keeps the old memo so the next heartbeat retries", func(t *testing.T) {
		store := &fakeStore{fail: errors.New("db down")}
		memo, changed, err := Apply(ctx, store, runtime, Memo{}, report(0.5), t0)
		if err == nil || changed || memo.Known {
			t.Fatalf("memo=%+v changed=%v err=%v, want unknown memo and the error", memo, changed, err)
		}
		store.fail = nil
		_, changed, err = Apply(ctx, store, runtime, memo, report(0.5), t0.Add(time.Second))
		if err != nil || !changed {
			t.Fatalf("retry: changed=%v err=%v", changed, err)
		}
	})

	t.Run("AG patch: an HTTP memo rebuilt from the row's jsonb sees an identical report as unchanged", func(t *testing.T) {
		store := &fakeStore{}
		_, _, _ = Apply(ctx, store, runtime, Memo{}, report(0.5), t0)
		// jsonb reorders keys and drops whitespace, so rebuild through a re-encode.
		var doc any
		_ = json.Unmarshal(store.stored, &doc)
		stored, _ := json.MarshalIndent(map[string]any{"cli_version": "v0.6.2", MetadataKey: doc}, "", "  ")

		memo := MemoFromMetadata(stored, t0)
		_, changed, err := Apply(ctx, store, runtime, memo, report(0.5), t0)
		if err != nil || changed || store.sets != 1 {
			t.Fatalf("changed=%v sets=%d err=%v, want no query for an unchanged report", changed, store.sets, err)
		}
		_, changed, _ = Apply(ctx, store, runtime, memo, report(0.6), t0)
		if !changed || store.sets != 2 {
			t.Fatalf("changed report: changed=%v sets=%d", changed, store.sets)
		}
	})
}

func TestAGPatchQuotaSettings(t *testing.T) {
	t.Run("AG patch: the setting read back by the guarded query rides the memo", func(t *testing.T) {
		store := &fakeStore{settings: []byte(`{"backup_enabled":true,"backup_max_concurrent":3}`)}
		memo, _, err := Apply(context.Background(), store, runtime, Memo{}, report(0.5), t0)
		if err != nil || memo.Settings == nil || !memo.Settings.BackupEnabled || memo.Settings.BackupMaxConcurrent != 3 {
			t.Fatalf("memo.Settings = %+v (%v)", memo.Settings, err)
		}

		store.settings = []byte(`{"backup_enabled":false,"backup_max_concurrent":3}`)
		memo, _, _ = Apply(context.Background(), store, runtime, memo, report(0.5), t0.Add(RefreshInterval))
		if memo.Settings == nil || memo.Settings.BackupEnabled {
			t.Fatalf("after refresh memo.Settings = %+v, want the toggle picked up", memo.Settings)
		}
	})

	t.Run("AG patch: a missing, null, malformed or out-of-range setting reads as none", func(t *testing.T) {
		for _, raw := range []string{``, `null`, `{`, `[]`, `{"backup_enabled":true}`, `{"backup_enabled":true,"backup_max_concurrent":0}`, `{"backup_enabled":true,"backup_max_concurrent":101}`} {
			if s := ParseSettings([]byte(raw)); s != nil {
				t.Errorf("ParseSettings(%q) = %+v, want nil", raw, s)
			}
		}
		if s := ParseSettings([]byte(`{"backup_enabled":true,"backup_max_concurrent":100}`)); s == nil || s.BackupMaxConcurrent != 100 {
			t.Errorf("a value at the bound = %+v, want accepted", s)
		}
	})

	t.Run("AG patch: settings are read from a runtime row's metadata", func(t *testing.T) {
		md := []byte(`{"cli_version":"v0.6.2","ag_settings":{"backup_enabled":true,"backup_max_concurrent":2},"ag_drain":{"draining":true}}`)
		if s := SettingsFromMetadata(md); s == nil || !s.BackupEnabled || s.BackupMaxConcurrent != 2 {
			t.Fatalf("SettingsFromMetadata = %+v", s)
		}
		if s := MemoFromMetadata(md, t0).Settings; s == nil || !s.BackupEnabled {
			t.Fatalf("MemoFromMetadata settings = %+v", s)
		}
		for _, md := range []string{``, `not json`, `{}`, `{"ag_settings":"x"}`} {
			if s := SettingsFromMetadata([]byte(md)); s != nil {
				t.Errorf("SettingsFromMetadata(%q) = %+v, want nil", md, s)
			}
		}
	})

	t.Run("AG patch: a stored report this patch did not write is replaced rather than trusted", func(t *testing.T) {
		store := &fakeStore{}
		memo := MemoFromMetadata([]byte(`{"ag_quota":"garbage"}`), t0)
		if memo.Known {
			t.Fatalf("memo = %+v, want unknown", memo)
		}
		if _, changed, err := Apply(context.Background(), store, runtime, memo, report(0.5), t0); err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
	})
}

func TestAGPatchQuotaValid(t *testing.T) {
	t.Run("AG patch: only a report with a known account and non-negative counts is stored", func(t *testing.T) {
		good := report(0.5)
		if !Valid(good) {
			t.Fatal("good report rejected")
		}
		backup := report(0.5)
		backup.Account = "backup"
		bad := []*protocol.DaemonQuotaReport{nil, {Account: ""}, {Account: "other"}, {Account: "primary", EffectiveCap: -1}, {Account: "primary", Max: -1}}
		if !Valid(backup) {
			t.Error("backup account rejected")
		}
		for i, r := range bad {
			if Valid(r) {
				t.Errorf("bad[%d] = %+v accepted", i, r)
			}
		}
	})
}

func TestAGPatchQuotaContextCarriesReport(t *testing.T) {
	t.Run("AG patch: the report rides the context, and a nil one leaves the context bare", func(t *testing.T) {
		r := report(0.5)
		if got := ReportFrom(WithReport(context.Background(), r)); got != r {
			t.Fatalf("ReportFrom = %p, want %p", got, r)
		}
		if got := ReportFrom(WithReport(context.Background(), nil)); got != nil {
			t.Fatalf("ReportFrom on a nil report = %+v", got)
		}
	})
}

func TestAGPatchQuotaMaterial(t *testing.T) {
	ctx := context.Background()
	at := func(r *protocol.DaemonQuotaReport, observed string) *protocol.DaemonQuotaReport {
		w := r.Windows["five_hour"]
		w.ObservedAt = observed
		r.Windows["five_hour"] = w
		return r
	}

	t.Run("AG patch: a write that only moved observed_at is not material, a utilisation change is", func(t *testing.T) {
		store := &fakeStore{}
		memo, changed, _ := Apply(ctx, store, runtime, Memo{}, report(0.5), t0)
		if !changed || !Material(Memo{}, memo.Report) {
			t.Fatalf("first report: changed=%v, want a material write", changed)
		}
		next, changed, err := Apply(ctx, store, runtime, memo, at(report(0.5), "2026-10-18T12:01:00Z"), t0.Add(time.Second))
		if err != nil || !changed || store.sets != 2 {
			t.Fatalf("observed_at move: changed=%v sets=%d err=%v, want a write", changed, store.sets, err)
		}
		if Material(memo, next.Report) {
			t.Error("an observed_at-only change is material")
		}
		after, changed, _ := Apply(ctx, store, runtime, next, report(0.6), t0.Add(2*time.Second))
		if !changed || !Material(next, after.Report) {
			t.Errorf("a utilisation change: changed=%v, want a material write", changed)
		}
	})

	t.Run("AG patch: a hold, cap or reset-time change is material and an unreadable prior report is too", func(t *testing.T) {
		base := report(0.5)
		prev, _ := Normalize(base)
		memo := Memo{Known: true, Report: prev}
		held := report(0.5)
		held.Hold = &protocol.DaemonQuotaHold{Until: "2026-10-18T14:00:00Z"}
		capped := report(0.5)
		capped.EffectiveCap = 2
		reset := report(0.5)
		w := reset.Windows["five_hour"]
		w.ResetsAt = "2026-10-18T19:00:00Z"
		reset.Windows["five_hour"] = w
		for name, r := range map[string]*protocol.DaemonQuotaReport{"hold": held, "cap": capped, "reset": reset} {
			next, _ := Normalize(r)
			if !Material(memo, next) {
				t.Errorf("%s change is not material", name)
			}
		}
		next, _ := Normalize(base)
		if !Material(memo, next) {
			t.Error("a write that restored an identical report (row was reset) is not material")
		}
		if !Material(Memo{Known: true, Report: []byte(`{`)}, prev) {
			t.Error("an unreadable prior report is not material")
		}
		if !Material(memo, []byte(`{`)) {
			t.Error("an unreadable next report is not material")
		}
	})
}
