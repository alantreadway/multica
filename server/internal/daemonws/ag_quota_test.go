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
	"github.com/multica-ai/multica/server/internal/agquota"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestAGPatchHubHandsQuotaReportToHeartbeatHandler(t *testing.T) {
	M.Reset()
	defer M.Reset()

	type seen struct {
		quota *protocol.DaemonQuotaReport
		drain *protocol.DaemonDrainReport
	}
	cases := []struct {
		name    string
		payload protocol.DaemonHeartbeatRequestPayload
		want    seen
	}{
		{
			name: "AG patch: a heartbeat frame's ag_quota reaches the handler context beside ag_drain",
			payload: protocol.DaemonHeartbeatRequestPayload{
				RuntimeID: "runtime-1",
				AGDrain:   &protocol.DaemonDrainReport{Draining: true},
				AGQuota:   &protocol.DaemonQuotaReport{Account: "primary", EffectiveCap: 2, Max: 5},
			},
			want: seen{
				quota: &protocol.DaemonQuotaReport{Account: "primary", EffectiveCap: 2, Max: 5},
				drain: &protocol.DaemonDrainReport{Draining: true},
			},
		},
		{
			name:    "AG patch: a heartbeat frame without ag_quota leaves the report nil",
			payload: protocol.DaemonHeartbeatRequestPayload{RuntimeID: "runtime-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan seen, 1)
			hub := NewHub()
			hub.SetHeartbeatHandler(func(ctx context.Context, _ ClientIdentity, runtimeID string, _ bool) (*protocol.DaemonHeartbeatAckPayload, error) {
				got <- seen{quota: agquota.ReportFrom(ctx), drain: agdrain.ReportFrom(ctx)}
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
				if (s.quota == nil) != (tc.want.quota == nil) || (s.quota != nil && (s.quota.Account != tc.want.quota.Account || s.quota.EffectiveCap != tc.want.quota.EffectiveCap || s.quota.Max != tc.want.quota.Max)) {
					t.Fatalf("quota report = %+v, want %+v", s.quota, tc.want.quota)
				}
				if (s.drain == nil) != (tc.want.drain == nil) {
					t.Fatalf("drain report = %+v, want %+v", s.drain, tc.want.drain)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("heartbeat handler was not invoked")
			}
		})
	}
}

func TestAGPatchLeaseKeepsQuotaMemo(t *testing.T) {
	t.Run("AG patch: a lease remembers the quota memo, and a nil lease is inert", func(t *testing.T) {
		lease := NewRuntimeLease("ws-1", "online", time.Now(), true)
		if m := lease.AGQuota(); m.Known || m.Settings != nil {
			t.Fatalf("fresh lease memo = %+v, want zero", m)
		}
		lease.SetAGQuota(agquota.Memo{Known: true, Settings: &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 2}})
		if m := lease.AGQuota(); !m.Known || m.Settings == nil || !m.Settings.BackupEnabled {
			t.Fatalf("memo = %+v", m)
		}
		var none *RuntimeLease
		none.SetAGQuota(agquota.Memo{Known: true})
		if none.AGQuota().Known {
			t.Fatal("nil lease returned a memo")
		}
	})
}
