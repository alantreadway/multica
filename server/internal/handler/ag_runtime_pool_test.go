package handler

// These need the test database and are not run by ag-patches/test.sh; see its
// header. ag-patches/test-go-full.sh runs them.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/agpool"
	"github.com/multica-ai/multica/server/internal/agquota"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type agPoolClaimResponse struct {
	Tasks []struct {
		ID             string `json:"id"`
		RuntimeID      string `json:"runtime_id"`
		PriorSessionID string `json:"prior_session_id"`
		PriorWorkDir   string `json:"prior_work_dir"`
	} `json:"tasks"`
}

func agPoolBatchClaim(t *testing.T, runtimeID string) (int, agPoolClaimResponse, string) {
	t.Helper()
	body := map[string]any{"daemon_id": batchClaimTestDaemonID, "runtime_ids": []string{runtimeID}, "max_tasks": 5}
	w := testutil.Call(t, testHandler.ClaimTasksByRuntime, newOwnerDaemonRequest(http.MethodPost, "/api/daemon/tasks/claim", body))
	var resp agPoolClaimResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp, w.Body.String()
}

// agPoolRuntimes creates two runtimes in one fresh pool: home (the agent's) and
// a peer.
func agPoolRuntimes(t *testing.T) (home, peer string) {
	t.Helper()
	meta := testutil.Raw(fmt.Sprintf(`'{"ag_pool": "ag-pool-h-%d"}'::jsonb`, time.Now().UnixNano()))
	home = dbfx.Runtime(t, "AG pool home", testutil.Cols{"metadata": meta})
	peer = dbfx.Runtime(t, "AG pool peer", testutil.Cols{"metadata": meta})
	return home, peer
}

// agPoolPriorRun records a finished run of agentID on issueID on runtimeID,
// with a resumable session, finished two hours ago (past the affinity window,
// so either runtime may claim the follow-up).
func agPoolPriorRun(t *testing.T, agentID, runtimeID, issueID string) {
	t.Helper()
	dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id":    runtimeID,
		"issue_id":      issueID,
		"status":        "completed",
		"session_id":    "ag-pool-session",
		"work_dir":      "/home-machine/workspaces/ws/task/workdir",
		"dispatched_at": testutil.Raw("now() - interval '3 hours'"),
		"started_at":    testutil.Raw("now() - interval '3 hours'"),
		"completed_at":  testutil.Raw("now() - interval '2 hours'"),
	})
}

func TestAGPatchRuntimePoolClaimDelivery(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	t.Run("AG patch: a peer's claim is delivered bound to the peer, without the other machine's session", func(t *testing.T) {
		home, peer := agPoolRuntimes(t)
		agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, home, "AG pool delivery agent")
		agPoolPriorRun(t, agentID, home, issueID)
		task := seedQueuedIssueTask(t, ctx, agentID, home, issueID)

		code, resp, raw := agPoolBatchClaim(t, peer)
		if code != http.StatusOK || len(resp.Tasks) != 1 || resp.Tasks[0].ID != task {
			t.Fatalf("peer claim: %d %s, want %s", code, raw, task)
		}
		got := resp.Tasks[0]
		if got.RuntimeID != peer {
			t.Fatalf("runtime_id = %s, want the peer %s", got.RuntimeID, peer)
		}
		if got.PriorSessionID != "" {
			t.Fatalf("prior_session_id = %q, want none across machines", got.PriorSessionID)
		}
		// Sent as upstream does; the daemon only reuses a directory it can
		// prove is its own (TestAGPatchRuntimePoolForeignPriorWorkDirIgnored).
		if got.PriorWorkDir != "/home-machine/workspaces/ws/task/workdir" {
			t.Fatalf("prior_work_dir = %q", got.PriorWorkDir)
		}
		if s := agTaskStatus(t, task); s != "dispatched" {
			t.Fatalf("task is %q, want dispatched", s)
		}
	})

	t.Run("AG patch: the same follow-up on the home machine still resumes", func(t *testing.T) {
		home, _ := agPoolRuntimes(t)
		agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, home, "AG pool resume agent")
		agPoolPriorRun(t, agentID, home, issueID)
		task := seedQueuedIssueTask(t, ctx, agentID, home, issueID)

		code, resp, raw := agPoolBatchClaim(t, home)
		if code != http.StatusOK || len(resp.Tasks) != 1 || resp.Tasks[0].ID != task {
			t.Fatalf("home claim: %d %s, want %s", code, raw, task)
		}
		if resp.Tasks[0].PriorSessionID != "ag-pool-session" {
			t.Fatalf("prior_session_id = %q, want the home session", resp.Tasks[0].PriorSessionID)
		}
	})

	t.Run("AG patch: a runtime outside the pool still claims nothing", func(t *testing.T) {
		home, _ := agPoolRuntimes(t)
		other := createClaimReclaimRuntime(t, ctx, "AG pool outsider")
		agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, home, "AG pool outsider agent")
		task := seedQueuedIssueTask(t, ctx, agentID, home, issueID)

		if code, resp, raw := agPoolBatchClaim(t, other); code != http.StatusOK || len(resp.Tasks) != 0 {
			t.Fatalf("outsider claim: %d %s, want nothing", code, raw)
		}
		if s := agTaskStatus(t, task); s != "queued" {
			t.Fatalf("task is %q, want queued", s)
		}
	})
}

func TestAGPatchRuntimePoolRegisterStoresPool(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	daemonID := fmt.Sprintf("ag-pool-register-%d", time.Now().UnixNano())
	register := func(pool string) map[string]any {
		t.Helper()
		body := map[string]any{
			"workspace_id": testWorkspaceID,
			"daemon_id":    daemonID,
			"device_name":  "ag-pool-device",
			"runtimes":     []map[string]any{{"name": "ag-pool-runtime", "type": "claude", "version": "1.0.0", "status": "online"}},
		}
		if pool != "" {
			body["ag_pool"] = pool
		}
		w := testutil.Call(t, testHandler.DaemonRegister, newDaemonTokenRequest("POST", "/api/daemon/register", body, testWorkspaceID, daemonID)).Want(http.StatusOK)
		var resp struct {
			Runtimes []struct {
				ID string `json:"id"`
			} `json:"runtimes"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil || len(resp.Runtimes) != 1 {
			t.Fatalf("register response: %v %+v", err, resp)
		}
		id := resp.Runtimes[0].ID
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, id) })
		var raw []byte
		if err := testPool.QueryRow(context.Background(), `SELECT metadata FROM agent_runtime WHERE id = $1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	if m := register(" alan "); m[agpool.MetadataKey] != "alan" || m["version"] != "1.0.0" {
		t.Fatalf("metadata with a pool = %v, want ag_pool alan beside the upstream keys", m)
	}
	if m := register(""); m[agpool.MetadataKey] != nil {
		t.Fatalf("metadata after re-register without a pool = %v, want no ag_pool", m)
	}
	if m := register("not a pool!"); m[agpool.MetadataKey] != nil {
		t.Fatalf("metadata with an invalid pool = %v, want no ag_pool", m)
	}
}

// agPoolPaceRuntime creates a local Claude runtime with its own daemon id;
// pool "" leaves it out of any pool.
func agPoolPaceRuntime(t *testing.T, name, pool string, over testutil.Cols) string {
	t.Helper()
	meta := `{}`
	if pool != "" {
		meta = fmt.Sprintf(`{"ag_pool": %q}`, pool)
	}
	cols := testutil.Cols{
		"daemon_id":    fmt.Sprintf("ag-pool-pace-%s-%d", name, time.Now().UnixNano()),
		"runtime_mode": "local",
		"provider":     "claude",
		"metadata":     testutil.Raw(fmt.Sprintf("'%s'::jsonb", meta)),
	}
	for k, v := range over {
		cols[k] = v
	}
	return dbfx.Runtime(t, name, cols)
}

// agPoolPaceTasks puts n tasks in status on runtimeID, for an agent bound there.
func agPoolPaceTasks(t *testing.T, runtimeID, status string, n int) {
	t.Helper()
	agentID := dbfx.Agent(t, fmt.Sprintf("AG pool pace agent %d", time.Now().UnixNano()), runtimeID)
	for range n {
		cols := testutil.Cols{"runtime_id": runtimeID, "status": status}
		if status != "queued" {
			cols["dispatched_at"] = testutil.Raw("now()")
		}
		if status == "running" {
			cols["started_at"] = testutil.Raw("now()")
		}
		dbfx.Task(t, agentID, cols)
	}
}

// agPoolPaceReport is a quota report with a weekly reset and max.
func agPoolPaceReport(weeklyReset string, max int) map[string]any {
	r := agQuotaReport(0.5)
	r["max"] = max
	r["windows"] = map[string]any{"seven_day": map[string]any{"utilization": 0.21, "resets_at": weeklyReset, "observed_at": "2026-11-09T15:00:00Z"}}
	return r
}

func agPoolPaceAck(t *testing.T, runtimeID string, body map[string]any) *protocol.AGPoolPace {
	t.Helper()
	ack := agQuotaHeartbeat(t, runtimeID, body)
	raw, ok := ack["ag_pool_pace"]
	if !ok {
		return nil
	}
	b, _ := json.Marshal(raw)
	var pace protocol.AGPoolPace
	if err := json.Unmarshal(b, &pace); err != nil {
		t.Fatalf("ag_pool_pace = %s: %v", b, err)
	}
	return &pace
}

func TestAGPatchRuntimePoolPace(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	const reset = "2026-11-14T06:00:00Z"
	setPeer := func(t *testing.T, id string, metadataSQL string, args ...any) {
		t.Helper()
		if _, err := testPool.Exec(context.Background(), `UPDATE agent_runtime SET `+metadataSQL+` WHERE id = $1`, append([]any{id}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	storeQuota := func(t *testing.T, id string, max int, weeklyReset string) {
		t.Helper()
		q := fmt.Sprintf(`{"account":"primary","max":%d,"effective_cap":1,"windows":{"seven_day":{"utilization":0.21,"resets_at":%q}}}`, max, weeklyReset)
		setPeer(t, id, `metadata = metadata || jsonb_build_object('ag_quota', $2::jsonb)`, q)
	}
	pool := func() string { return fmt.Sprintf("ag-pool-pace-%d", time.Now().UnixNano()) }

	t.Run("AG patch: the ack counts an online peer's dispatched and running tasks and both sides' slots", func(t *testing.T) {
		p := pool()
		self := agPoolPaceRuntime(t, "self", p, nil)
		peer := agPoolPaceRuntime(t, "peer", p, nil)
		storeQuota(t, peer, 3, reset)
		agPoolPaceTasks(t, peer, "running", 2)
		agPoolPaceTasks(t, peer, "dispatched", 1)
		agPoolPaceTasks(t, peer, "queued", 2)
		agPoolPaceTasks(t, self, "running", 1) // the daemon counts its own

		got := agPoolPaceAck(t, self, map[string]any{"ag_quota": agPoolPaceReport(reset, 2)})
		if got == nil || *got != (protocol.AGPoolPace{PeerActive: 3, PeerSlots: 3, Slots: 2}) {
			t.Fatalf("ag_pool_pace = %+v, want peer_active 3, peer_slots 3, slots 2", got)
		}
	})

	t.Run("AG patch: a stale, offline or other-account peer is left out", func(t *testing.T) {
		p := pool()
		self := agPoolPaceRuntime(t, "self", p, nil)
		stale := agPoolPaceRuntime(t, "stale", p, testutil.Cols{"last_seen_at": testutil.Raw("now() - interval '10 minutes'"), "updated_at": testutil.Raw("now() - interval '10 minutes'")})
		offline := agPoolPaceRuntime(t, "offline", p, testutil.Cols{"status": "offline"})
		other := agPoolPaceRuntime(t, "other-account", p, nil)
		storeQuota(t, other, 3, "2026-11-12T18:00:00Z")
		for _, id := range []string{stale, offline, other} {
			agPoolPaceTasks(t, id, "running", 1)
		}

		got := agPoolPaceAck(t, self, map[string]any{"ag_quota": agPoolPaceReport(reset, 2)})
		if got == nil || *got != (protocol.AGPoolPace{Slots: 2, Lead: true}) {
			t.Fatalf("ag_pool_pace = %+v, want only slots 2, lead", got)
		}
	})

	t.Run("AG patch: a draining peer's runs count and its slots do not; a runtime cap lowers slots", func(t *testing.T) {
		p := pool()
		self := agPoolPaceRuntime(t, "self", p, nil)
		draining := agPoolPaceRuntime(t, "draining", p, nil)
		capped := agPoolPaceRuntime(t, "capped", p, testutil.Cols{"max_concurrent_tasks": 1})
		storeQuota(t, draining, 3, reset)
		setPeer(t, draining, `metadata = metadata || '{"ag_drain":{"draining":true}}'::jsonb`)
		storeQuota(t, capped, 3, reset)
		agPoolPaceTasks(t, draining, "running", 2)

		got := agPoolPaceAck(t, self, map[string]any{"ag_quota": agPoolPaceReport(reset, 2)})
		if got == nil || *got != (protocol.AGPoolPace{PeerActive: 2, PeerSlots: 1, Slots: 2, Lead: true}) {
			t.Fatalf("ag_pool_pace = %+v, want peer_active 2, peer_slots 1, slots 2, lead", got)
		}
	})

	t.Run("AG patch: no ag_pool_pace outside a pool or without a quota report", func(t *testing.T) {
		lone := agPoolPaceRuntime(t, "lone", "", nil)
		if got := agPoolPaceAck(t, lone, map[string]any{"ag_quota": agPoolPaceReport(reset, 2)}); got != nil {
			t.Fatalf("unpooled ack carries %+v", got)
		}
		p := pool()
		self := agPoolPaceRuntime(t, "self", p, nil)
		agPoolPaceRuntime(t, "peer", p, nil)
		if got := agPoolPaceAck(t, self, map[string]any{}); got != nil {
			t.Fatalf("ack without a quota report carries %+v", got)
		}
	})

	t.Run("AG patch: the WebSocket ack carries it too", func(t *testing.T) {
		p := pool()
		self := agPoolPaceRuntime(t, "self", p, nil)
		peer := agPoolPaceRuntime(t, "peer", p, nil)
		storeQuota(t, peer, 3, reset)
		agPoolPaceTasks(t, peer, "running", 1)

		lease := daemonws.NewRuntimeLease(testWorkspaceID, "online", time.Now(), true)
		identity := daemonws.ClientIdentity{
			WorkspaceID: testWorkspaceID, WorkspaceIDs: []string{testWorkspaceID}, RuntimeIDs: []string{self},
			RuntimeLeases: map[string]*daemonws.RuntimeLease{self: lease},
		}
		weekly := protocol.DaemonQuotaWindow{ResetsAt: reset}
		ctx := agquota.WithReport(context.Background(), &protocol.DaemonQuotaReport{Account: "primary", Max: 2, Windows: map[string]protocol.DaemonQuotaWindow{"seven_day": weekly}})
		ack, err := testHandler.HandleDaemonWSHeartbeat(ctx, identity, self, true)
		if err != nil || ack == nil || ack.AGPoolPace == nil || *ack.AGPoolPace != (protocol.AGPoolPace{PeerActive: 1, PeerSlots: 3, Slots: 2}) {
			t.Fatalf("ack = %+v (%v)", ack, err)
		}
		ack, err = testHandler.HandleDaemonWSHeartbeat(context.Background(), identity, self, true)
		if err != nil || ack == nil || ack.AGPoolPace != nil {
			t.Fatalf("ack without a report = %+v (%v)", ack, err)
		}
	})
}
