package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// ag_capabilities is how a daemon learns the server applies the claim filter.
func TestAGPatchHeartbeatAckCapabilitiesWireFormat(t *testing.T) {
	t.Run("AG patch: the ack carries ag_capabilities", func(t *testing.T) {
		b, err := json.Marshal(DaemonHeartbeatAckPayload{RuntimeID: "rt-1", Status: "ok", AGCapabilities: []string{"claim_filter"}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"ag_capabilities":["claim_filter"]`) {
			t.Fatalf("ack = %s", b)
		}
	})

	t.Run("AG patch: an ack without capabilities omits the key", func(t *testing.T) {
		b, _ := json.Marshal(DaemonHeartbeatAckPayload{RuntimeID: "rt-1", Status: "ok"})
		if strings.Contains(string(b), "ag_capabilities") {
			t.Fatalf("ack = %s", b)
		}
	})

	t.Run("AG patch: a stock daemon decoding the ack ignores the key", func(t *testing.T) {
		// A daemon built before the patch has no AGCapabilities field.
		type stockAck struct {
			RuntimeID string `json:"runtime_id"`
			Status    string `json:"status"`
		}
		var ack stockAck
		if err := json.Unmarshal([]byte(`{"runtime_id":"rt-1","status":"ok","ag_capabilities":["claim_filter"]}`), &ack); err != nil {
			t.Fatal(err)
		}
		if ack.RuntimeID != "rt-1" || ack.Status != "ok" {
			t.Fatalf("decoded = %+v", ack)
		}
	})
}
