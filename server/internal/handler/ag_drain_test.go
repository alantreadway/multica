package handler

// AG patch: drain badge, backend half. These hit the real queries and need the
// test database (they skip without one, like the rest of this package), so
// ag-patches/test.sh counts the hermetic agdrain tests and a developer with a
// database runs these via `go test ./internal/handler -run '^TestAGPatch'`.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/agdrain"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func agDrainMetadata(t *testing.T, runtimeID string) map[string]json.RawMessage {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(),
		`SELECT metadata FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&raw); err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var md map[string]json.RawMessage
	if err := json.Unmarshal(raw, &md); err != nil {
		t.Fatalf("decode metadata %s: %v", raw, err)
	}
	return md
}

func agDrainHeartbeat(t *testing.T, runtimeID string, body map[string]any) {
	t.Helper()
	body["runtime_id"] = runtimeID
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/heartbeat", body, testWorkspaceID, "runtime-local-skills-daemon")
	testutil.Call(t, testHandler.DaemonHeartbeat, req).Want(http.StatusOK)
}

func TestAGPatchDrainHeartbeatMetadata(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	t.Run("AG patch: HTTP heartbeat stores, updates and clears the drain state beside other metadata", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		if _, err := testPool.Exec(context.Background(),
			`UPDATE agent_runtime SET metadata = '{"cli_version":"v0.6.2"}'::jsonb WHERE id = $1`, runtimeID); err != nil {
			t.Fatal(err)
		}

		agDrainHeartbeat(t, runtimeID, map[string]any{"ag_drain": map[string]any{"draining": true, "idle": false, "active_tasks": 2}})
		md := agDrainMetadata(t, runtimeID)
		var got agdrain.State
		if err := json.Unmarshal(md[agdrain.MetadataKey], &got); err != nil || got != (agdrain.State{Draining: true, ActiveTasks: 2}) {
			t.Fatalf("ag_drain = %s (%v)", md[agdrain.MetadataKey], err)
		}
		if string(md["cli_version"]) != `"v0.6.2"` {
			t.Fatalf("other metadata lost: %v", md)
		}

		agDrainHeartbeat(t, runtimeID, map[string]any{"ag_drain": map[string]any{"draining": true, "idle": true, "active_tasks": 0}})
		_ = json.Unmarshal(agDrainMetadata(t, runtimeID)[agdrain.MetadataKey], &got)
		if !got.Idle || got.ActiveTasks != 0 {
			t.Fatalf("after idle report: %+v", got)
		}

		agDrainHeartbeat(t, runtimeID, map[string]any{"ag_drain": map[string]any{"draining": false}})
		md = agDrainMetadata(t, runtimeID)
		if _, present := md[agdrain.MetadataKey]; present {
			t.Fatalf("ag_drain still stored after resume: %v", md)
		}
		if string(md["cli_version"]) != `"v0.6.2"` {
			t.Fatalf("other metadata lost on clear: %v", md)
		}
	})

	t.Run("AG patch: a heartbeat without the field leaves a stored drain state alone", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		agDrainHeartbeat(t, runtimeID, map[string]any{"ag_drain": map[string]any{"draining": true, "idle": false, "active_tasks": 1}})

		agDrainHeartbeat(t, runtimeID, map[string]any{"supports_batch_import": true})

		if _, present := agDrainMetadata(t, runtimeID)[agdrain.MetadataKey]; !present {
			t.Fatal("a stock heartbeat cleared the drain state")
		}
	})

	t.Run("AG patch: an unchanged report does not rewrite the row", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		report := map[string]any{"ag_drain": map[string]any{"draining": true, "idle": false, "active_tasks": 1}}
		agDrainHeartbeat(t, runtimeID, report)

		var before time.Time
		if err := testPool.QueryRow(context.Background(),
			`SELECT updated_at FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&before); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		agDrainHeartbeat(t, runtimeID, report)
		var after time.Time
		if err := testPool.QueryRow(context.Background(),
			`SELECT updated_at FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if !after.Equal(before) {
			t.Fatalf("updated_at moved %s -> %s on an unchanged report", before, after)
		}
	})

	t.Run("AG patch: the WebSocket heartbeat stores the report and keeps it on the lease", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		lease := daemonws.NewRuntimeLease(testWorkspaceID, "online", time.Now(), true)
		identity := daemonws.ClientIdentity{
			WorkspaceID:   testWorkspaceID,
			WorkspaceIDs:  []string{testWorkspaceID},
			RuntimeIDs:    []string{runtimeID},
			RuntimeLeases: map[string]*daemonws.RuntimeLease{runtimeID: lease},
		}
		report := &protocol.DaemonDrainReport{Draining: true, Idle: false, ActiveTasks: 4}
		ctx := agdrain.WithReport(context.Background(), report)

		if _, err := testHandler.HandleDaemonWSHeartbeat(ctx, identity, runtimeID, true); err != nil {
			t.Fatalf("ws heartbeat: %v", err)
		}
		var got agdrain.State
		if err := json.Unmarshal(agDrainMetadata(t, runtimeID)[agdrain.MetadataKey], &got); err != nil || got.ActiveTasks != 4 {
			t.Fatalf("ag_drain = %+v (%v)", got, err)
		}
		if memo := lease.AGDrain(); !memo.Known || memo.State == nil || memo.State.ActiveTasks != 4 {
			t.Fatalf("lease memo = %+v", memo)
		}

		if _, err := testHandler.HandleDaemonWSHeartbeat(agdrain.WithReport(context.Background(), &protocol.DaemonDrainReport{}), identity, runtimeID, true); err != nil {
			t.Fatalf("ws heartbeat: %v", err)
		}
		if _, present := agDrainMetadata(t, runtimeID)[agdrain.MetadataKey]; present {
			t.Fatal("ag_drain still stored after the WebSocket resume report")
		}
	})

	t.Run("AG patch: a resume report after the metadata was reset is a no-op", func(t *testing.T) {
		// Registration overwrites metadata wholesale, which is what clears a
		// stale key; a restarted daemon's first report must then not fail.
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		agDrainHeartbeat(t, runtimeID, map[string]any{"ag_drain": map[string]any{"draining": true, "idle": false, "active_tasks": 1}})

		if _, err := testPool.Exec(context.Background(),
			`UPDATE agent_runtime SET metadata = '{}'::jsonb WHERE id = $1`, runtimeID); err != nil {
			t.Fatal(err)
		}
		agDrainHeartbeat(t, runtimeID, map[string]any{"ag_drain": map[string]any{"draining": false}})
		if _, present := agDrainMetadata(t, runtimeID)[agdrain.MetadataKey]; present {
			t.Fatal("ag_drain present after re-register and a resume report")
		}
	})
}
