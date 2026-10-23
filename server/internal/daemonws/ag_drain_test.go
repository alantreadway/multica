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
	"github.com/multica-ai/multica/server/internal/agdrain"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestAGPatchHubHandsDrainReportToHeartbeatHandler(t *testing.T) {
	M.Reset()
	defer M.Reset()

	cases := []struct {
		name    string
		payload protocol.DaemonHeartbeatRequestPayload
		want    *protocol.DaemonDrainReport
	}{
		{
			name:    "AG patch: a heartbeat frame's ag_drain reaches the handler context",
			payload: protocol.DaemonHeartbeatRequestPayload{RuntimeID: "runtime-1", AGDrain: &protocol.DaemonDrainReport{Draining: true, ActiveTasks: 2}},
			want:    &protocol.DaemonDrainReport{Draining: true, ActiveTasks: 2},
		},
		{
			name:    "AG patch: a stock heartbeat frame leaves the report nil",
			payload: protocol.DaemonHeartbeatRequestPayload{RuntimeID: "runtime-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan *protocol.DaemonDrainReport, 1)
			hub := NewHub()
			hub.SetHeartbeatHandler(func(ctx context.Context, _ ClientIdentity, runtimeID string, _ bool) (*protocol.DaemonHeartbeatAckPayload, error) {
				got <- agdrain.ReportFrom(ctx)
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
			frame, err := json.Marshal(protocol.Message{
				Type:    protocol.EventDaemonHeartbeat,
				Payload: mustMarshalRaw(tc.payload),
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
				t.Fatalf("WriteMessage: %v", err)
			}

			select {
			case report := <-got:
				switch {
				case tc.want == nil && report != nil:
					t.Fatalf("report = %+v, want nil", report)
				case tc.want != nil && (report == nil || *report != *tc.want):
					t.Fatalf("report = %+v, want %+v", report, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("heartbeat handler was not invoked")
			}
		})
	}
}
