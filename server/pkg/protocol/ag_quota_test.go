package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func agQuotaFixture() *DaemonQuotaReport {
	u := 0.84
	return &DaemonQuotaReport{
		Account:      "primary",
		Hold:         &DaemonQuotaHold{Until: "2026-10-18T14:02:00Z", Window: "five_hour"},
		EffectiveCap: 3,
		Max:          5,
		Windows: map[string]DaemonQuotaWindow{
			"five_hour": {Utilization: &u, ResetsAt: "2026-10-18T14:00:00Z", ObservedAt: "2026-10-18T12:31:00Z"},
		},
		Backup:        DaemonQuotaBackup{Detected: false, Valid: false, Reason: "", Enabled: true, MaxConcurrent: 2},
		BackupWindows: map[string]DaemonQuotaWindow{},
	}
}

func TestAGPatchQuotaReportWireFormat(t *testing.T) {
	t.Run("AG patch: a heartbeat without ag_quota decodes to a nil report", func(t *testing.T) {
		var p DaemonHeartbeatRequestPayload
		if err := json.Unmarshal([]byte(`{"runtime_id":"rt-1","supports_batch_import":true}`), &p); err != nil {
			t.Fatal(err)
		}
		if p.AGQuota != nil {
			t.Fatalf("AGQuota = %+v, want nil", p.AGQuota)
		}
	})

	t.Run("AG patch: a nil quota report is omitted from the encoded heartbeat", func(t *testing.T) {
		b, err := json.Marshal(DaemonHeartbeatRequestPayload{RuntimeID: "rt-1"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "ag_quota") {
			t.Fatalf("encoded heartbeat carries ag_quota: %s", b)
		}
	})

	t.Run("AG patch: the quota report round-trips under ag_quota with the documented keys", func(t *testing.T) {
		in := DaemonHeartbeatRequestPayload{RuntimeID: "rt-1", AGQuota: agQuotaFixture()}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		report, _ := raw["ag_quota"].(map[string]any)
		for _, key := range []string{"account", "hold", "effective_cap", "max", "windows", "backup", "backup_windows"} {
			if _, ok := report[key]; !ok {
				t.Fatalf("ag_quota lacks %q: %s", key, b)
			}
		}
		backup, _ := report["backup"].(map[string]any)
		for _, key := range []string{"detected", "valid", "reason", "enabled", "max_concurrent"} {
			if _, ok := backup[key]; !ok {
				t.Fatalf("ag_quota.backup lacks %q: %s", key, b)
			}
		}
		var out DaemonHeartbeatRequestPayload
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(out, in) {
			t.Fatalf("round trip = %+v, want %+v", out.AGQuota, in.AGQuota)
		}
	})

	t.Run("AG patch: a report with no hold and no utilisation omits both keys", func(t *testing.T) {
		b, err := json.Marshal(DaemonQuotaReport{
			Account: "primary",
			Windows: map[string]DaemonQuotaWindow{"seven_day": {ResetsAt: "2026-10-20T00:00:00Z"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"hold"`) || strings.Contains(string(b), "utilization") {
			t.Fatalf("encoded report = %s", b)
		}
	})
}

func TestAGPatchRuntimeSettingsAckWireFormat(t *testing.T) {
	t.Run("AG patch: an ack without ag_settings decodes to nil and encodes without the key", func(t *testing.T) {
		var ack DaemonHeartbeatAckPayload
		if err := json.Unmarshal([]byte(`{"runtime_id":"rt-1","status":"ok"}`), &ack); err != nil {
			t.Fatal(err)
		}
		if ack.AGSettings != nil {
			t.Fatalf("AGSettings = %+v, want nil", ack.AGSettings)
		}
		b, _ := json.Marshal(ack)
		if strings.Contains(string(b), "ag_settings") {
			t.Fatalf("encoded ack carries ag_settings: %s", b)
		}
	})

	t.Run("AG patch: ag_settings round-trips on the ack, false and all", func(t *testing.T) {
		in := DaemonHeartbeatAckPayload{RuntimeID: "rt-1", Status: "ok", AGSettings: &AGRuntimeSettings{BackupEnabled: false, BackupMaxConcurrent: 4}}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"ag_settings":{"backup_enabled":false,"backup_max_concurrent":4}`) {
			t.Fatalf("encoded ack = %s", b)
		}
		var out DaemonHeartbeatAckPayload
		if err := json.Unmarshal(b, &out); err != nil || *out.AGSettings != *in.AGSettings {
			t.Fatalf("decoded = %+v (%v)", out.AGSettings, err)
		}
	})
}
