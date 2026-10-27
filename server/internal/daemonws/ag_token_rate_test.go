package daemonws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/agquota"
	"github.com/multica-ai/multica/server/internal/agtokenrate"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestAGPatchHubHandsTokenRateToHeartbeatHandler(t *testing.T) {
	M.Reset()
	defer M.Reset()

	type seen struct {
		rate  *protocol.DaemonTokenRateReport
		quota *protocol.DaemonQuotaReport
	}
	for _, tc := range []struct {
		name    string
		payload protocol.DaemonHeartbeatRequestPayload
		rate    bool
	}{
		{
			name: "AG patch: a heartbeat frame's ag_token_rate reaches the handler context beside ag_quota",
			payload: protocol.DaemonHeartbeatRequestPayload{
				RuntimeID:   "runtime-1",
				AGQuota:     &protocol.DaemonQuotaReport{Account: "primary"},
				AGTokenRate: &protocol.DaemonTokenRateReport{OutputPerSec: 42, WindowSeconds: 60, ActiveRuns: 1},
			},
			rate: true,
		},
		{
			name:    "AG patch: a heartbeat frame without ag_token_rate leaves the report nil",
			payload: protocol.DaemonHeartbeatRequestPayload{RuntimeID: "runtime-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan seen, 1)
			hub := NewHub()
			hub.SetHeartbeatHandler(func(ctx context.Context, _ ClientIdentity, runtimeID string, _ bool) (*protocol.DaemonHeartbeatAckPayload, error) {
				got <- seen{rate: agtokenrate.ReportFrom(ctx), quota: agquota.ReportFrom(ctx)}
				return &protocol.DaemonHeartbeatAckPayload{RuntimeID: runtimeID, Status: "ok"}, nil
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hub.HandleWebSocket(w, r, ClientIdentity{RuntimeIDs: []string{"runtime-1"}})
			}))
			defer server.Close()

			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			defer conn.Close()
			frame, err := json.Marshal(protocol.Message{Type: protocol.EventDaemonHeartbeat, Payload: mustMarshalRaw(tc.payload)})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
				t.Fatalf("WriteMessage: %v", err)
			}

			select {
			case s := <-got:
				if (s.rate != nil) != tc.rate || (s.rate != nil && s.rate.OutputPerSec != 42) {
					t.Fatalf("token rate = %+v, want present=%v", s.rate, tc.rate)
				}
				if (s.quota != nil) != (tc.payload.AGQuota != nil) {
					t.Fatalf("quota report = %+v lost beside the token rate", s.quota)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("heartbeat handler was not invoked")
			}
		})
	}
}
