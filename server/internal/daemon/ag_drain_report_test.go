package daemon

// AG patch: drain state on the heartbeat; TestAGPatch* with "AG patch:"
// subtests, as counted by ag-patches/test.sh.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// agDrainHeartbeatBodies records the JSON body of each POST /api/daemon/heartbeat.
type agDrainHeartbeatBodies struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (b *agDrainHeartbeatBodies) last(t *testing.T) map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.bodies) == 0 {
		t.Fatal("no heartbeat reached the server")
	}
	return b.bodies[len(b.bodies)-1]
}

func newAGDrainHeartbeatServer(t *testing.T) (*httptest.Server, *agDrainHeartbeatBodies) {
	t.Helper()
	rec := &agDrainHeartbeatBodies{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/api/daemon/heartbeat") {
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err == nil {
				rec.mu.Lock()
				rec.bodies = append(rec.bodies, body)
				rec.mu.Unlock()
			}
			w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(s.Close)
	return s, rec
}

func TestAGPatchDrainHeartbeatReport(t *testing.T) {
	t.Run("AG patch: a daemon that is not draining reports draining=false so a stored report clears", func(t *testing.T) {
		s, rec := newAGDrainHeartbeatServer(t)
		d := New(Config{ServerBaseURL: s.URL, HeartbeatInterval: time.Hour}, discardLogger())

		if _, err := d.client.SendHeartbeatWithDrain(context.Background(), "rt-1", d.agDrainReport()); err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
		got, ok := rec.last(t)["ag_drain"].(map[string]any)
		if !ok {
			t.Fatalf("ag_drain missing from heartbeat body: %v", rec.last(t))
		}
		if got["draining"] != false || got["idle"] != false || got["active_tasks"] != float64(0) {
			t.Fatalf("ag_drain = %v, want draining=false idle=false active_tasks=0", got)
		}
	})

	t.Run("AG patch: a draining daemon reports the running task count, then idle at zero", func(t *testing.T) {
		s, rec := newAGDrainHeartbeatServer(t)
		d := New(Config{ServerBaseURL: s.URL, HeartbeatInterval: time.Hour}, discardLogger())
		d.agStartDrain()
		d.activeTasks.Store(2)

		if _, err := d.client.SendHeartbeatWithDrain(context.Background(), "rt-1", d.agDrainReport()); err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
		got := rec.last(t)["ag_drain"].(map[string]any)
		if got["draining"] != true || got["idle"] != false || got["active_tasks"] != float64(2) {
			t.Fatalf("ag_drain = %v, want draining=true idle=false active_tasks=2", got)
		}

		d.activeTasks.Store(0)
		if _, err := d.client.SendHeartbeatWithDrain(context.Background(), "rt-1", d.agDrainReport()); err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
		got = rec.last(t)["ag_drain"].(map[string]any)
		if got["draining"] != true || got["idle"] != true || got["active_tasks"] != float64(0) {
			t.Fatalf("ag_drain = %v, want draining=true idle=true active_tasks=0", got)
		}
	})

	t.Run("AG patch: the plain heartbeat call omits ag_drain", func(t *testing.T) {
		s, rec := newAGDrainHeartbeatServer(t)
		d := New(Config{ServerBaseURL: s.URL, HeartbeatInterval: time.Hour}, discardLogger())

		if _, err := d.client.SendHeartbeat(context.Background(), "rt-1"); err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
		if _, present := rec.last(t)["ag_drain"]; present {
			t.Fatalf("ag_drain present on a plain heartbeat: %v", rec.last(t))
		}
	})

	t.Run("AG patch: the WebSocket heartbeat frame carries the report", func(t *testing.T) {
		d := New(Config{HeartbeatInterval: time.Hour}, discardLogger())
		d.agStartDrain()
		d.activeTasks.Store(3)
		writes := make(chan *wsOutbound, 2)

		d.sendWSHeartbeats(context.Background(), []string{"rt-1", "rt-2"}, writes)

		for _, want := range []string{"rt-1", "rt-2"} {
			var out *wsOutbound
			select {
			case out = <-writes:
			default:
				t.Fatalf("no frame for %s", want)
			}
			var msg protocol.Message
			if err := json.Unmarshal(out.data, &msg); err != nil {
				t.Fatalf("decode frame: %v", err)
			}
			var payload protocol.DaemonHeartbeatRequestPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if payload.RuntimeID != want {
				t.Fatalf("runtime_id = %q, want %q", payload.RuntimeID, want)
			}
			if payload.AGDrain == nil || !payload.AGDrain.Draining || payload.AGDrain.ActiveTasks != 3 {
				t.Fatalf("ag_drain = %+v, want draining with 3 active", payload.AGDrain)
			}
		}
	})
}
