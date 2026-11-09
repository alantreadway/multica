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
	"github.com/multica-ai/multica/server/internal/testutil"
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
