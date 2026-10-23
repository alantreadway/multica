package protocol

import (
	"encoding/json"
	"testing"
)

// A stock server and a stock daemon decode the same JSON into structs that lack
// the AG fields. They must ignore ag_quota and ag_settings, not fail on them.
func TestAGPatchQuotaFieldsAreIgnoredByStockDecoders(t *testing.T) {
	t.Run("AG patch: a stock heartbeat decoder ignores ag_quota", func(t *testing.T) {
		b, err := json.Marshal(DaemonHeartbeatRequestPayload{RuntimeID: "rt-1", SupportsBatchImport: true, AGQuota: agQuotaFixture()})
		if err != nil {
			t.Fatal(err)
		}
		var stock struct {
			RuntimeID           string `json:"runtime_id"`
			SupportsBatchImport bool   `json:"supports_batch_import"`
		}
		if err := json.Unmarshal(b, &stock); err != nil || stock.RuntimeID != "rt-1" || !stock.SupportsBatchImport {
			t.Fatalf("stock decode = %+v (%v)", stock, err)
		}
	})

	t.Run("AG patch: a stock ack decoder ignores ag_settings", func(t *testing.T) {
		b, err := json.Marshal(DaemonHeartbeatAckPayload{RuntimeID: "rt-1", Status: "ok", AGSettings: &AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 3}})
		if err != nil {
			t.Fatal(err)
		}
		var stock struct {
			RuntimeID string `json:"runtime_id"`
			Status    string `json:"status"`
		}
		if err := json.Unmarshal(b, &stock); err != nil || stock.RuntimeID != "rt-1" || stock.Status != "ok" {
			t.Fatalf("stock decode = %+v (%v)", stock, err)
		}
	})

	t.Run("AG patch: a report with keys from a later daemon still decodes", func(t *testing.T) {
		in := `{"runtime_id":"rt-1","ag_quota":{"account":"backup","effective_cap":1,"max":2,"windows":{},"backup":{"detected":true,"valid":true,"reason":"","enabled":true,"max_concurrent":2,"future":1},"backup_windows":{"five_hour":{"utilization":0.1,"future":2}},"future":3}}`
		var p DaemonHeartbeatRequestPayload
		if err := json.Unmarshal([]byte(in), &p); err != nil || p.AGQuota == nil || p.AGQuota.Account != "backup" || !p.AGQuota.Backup.Valid {
			t.Fatalf("decode = %+v (%v)", p.AGQuota, err)
		}
		if w := p.AGQuota.BackupWindows["five_hour"]; w.Utilization == nil || *w.Utilization != 0.1 {
			t.Fatalf("backup window = %+v", w)
		}
	})
}
