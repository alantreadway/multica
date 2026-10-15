package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// AG patch: drain badge. The drain report is optional on the heartbeat wire
// format: a stock daemon sends none and the server must read that as "leave
// metadata alone", and a patched daemon must not send the field when it has
// nothing to report.
func TestAGPatchDrainReportWireFormat(t *testing.T) {
	t.Run("AG patch: a heartbeat without ag_drain decodes to a nil report", func(t *testing.T) {
		var p DaemonHeartbeatRequestPayload
		if err := json.Unmarshal([]byte(`{"runtime_id":"rt-1","supports_batch_import":true}`), &p); err != nil {
			t.Fatal(err)
		}
		if p.AGDrain != nil {
			t.Fatalf("AGDrain = %+v, want nil", p.AGDrain)
		}
		if p.RuntimeID != "rt-1" || !p.SupportsBatchImport {
			t.Fatalf("existing fields changed: %+v", p)
		}
	})

	t.Run("AG patch: a nil report is omitted from the encoded heartbeat", func(t *testing.T) {
		b, err := json.Marshal(DaemonHeartbeatRequestPayload{RuntimeID: "rt-1", SupportsBatchImport: true})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "ag_drain") {
			t.Fatalf("encoded heartbeat carries ag_drain: %s", b)
		}
	})

	t.Run("AG patch: draining, idle and active_tasks round-trip under ag_drain", func(t *testing.T) {
		in := DaemonHeartbeatRequestPayload{
			RuntimeID: "rt-1",
			AGDrain:   &DaemonDrainReport{Draining: true, Idle: false, ActiveTasks: 3},
		}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		got, _ := raw["ag_drain"].(map[string]any)
		if got["draining"] != true || got["idle"] != false || got["active_tasks"] != float64(3) {
			t.Fatalf("ag_drain = %v", got)
		}
		var out DaemonHeartbeatRequestPayload
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if out.AGDrain == nil || *out.AGDrain != *in.AGDrain {
			t.Fatalf("round-trip = %+v, want %+v", out.AGDrain, in.AGDrain)
		}
	})
}
