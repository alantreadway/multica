package handler

// These need the test database and are not run by ag-patches/test.sh; see its header.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/testutil"
)

type agClaimResponse struct {
	Tasks []struct {
		ID              string `json:"id"`
		RuntimeID       string `json:"runtime_id"`
		AGIssuePriority string `json:"ag_issue_priority"`
	} `json:"tasks"`
}

// agPostBatchClaim posts a batch claim as the daemon the claim tests run as,
// adding ag_min_issue_priority when filter is non-nil.
func agPostBatchClaim(t *testing.T, runtimeIDs []string, filter map[string]string) (int, agClaimResponse, string) {
	t.Helper()
	body := map[string]any{"daemon_id": batchClaimTestDaemonID, "runtime_ids": runtimeIDs, "max_tasks": 5}
	if filter != nil {
		body["ag_min_issue_priority"] = filter
	}
	req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/tasks/claim", body)
	w := testutil.Call(t, testHandler.ClaimTasksByRuntime, req)
	var resp agClaimResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp, w.Body.String()
}

func agSetIssuePriority(t *testing.T, issueID, priority string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET priority = $2 WHERE id = $1`, issueID, priority); err != nil {
		t.Fatalf("set issue priority: %v", err)
	}
}

func agTaskStatus(t *testing.T, taskID string) string {
	t.Helper()
	var s string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&s); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	return s
}

func TestAGPatchClaimFilterBatchClaim(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	t.Run("AG patch: a filtered runtime claims only its urgent issue's task and the response carries the priority", func(t *testing.T) {
		rt := createClaimReclaimRuntime(t, ctx, "AG filter urgent rt")
		a, urgentIssue := createClaimReclaimAgentAndIssue(t, ctx, rt, "AG filter urgent agent")
		agSetIssuePriority(t, urgentIssue, "urgent")
		urgentTask := seedQueuedIssueTask(t, ctx, a, rt, urgentIssue)

		rt2 := createClaimReclaimRuntime(t, ctx, "AG filter high rt")
		a2, highIssue := createClaimReclaimAgentAndIssue(t, ctx, rt2, "AG filter high agent")
		agSetIssuePriority(t, highIssue, "high")
		highTask := seedQueuedIssueTask(t, ctx, a2, rt2, highIssue)

		code, resp, raw := agPostBatchClaim(t, []string{rt, rt2}, map[string]string{rt: "urgent", rt2: "urgent"})
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, raw)
		}
		if len(resp.Tasks) != 1 || resp.Tasks[0].ID != urgentTask {
			t.Fatalf("claimed %s, want only the urgent task %s", raw, urgentTask)
		}
		if resp.Tasks[0].AGIssuePriority != "urgent" {
			t.Fatalf("ag_issue_priority = %q, want urgent", resp.Tasks[0].AGIssuePriority)
		}
		if s := agTaskStatus(t, highTask); s != "queued" {
			t.Fatalf("high task is %q, want it left queued", s)
		}
	})

	t.Run("AG patch: an unfiltered runtime in the same request and a body without the field are unaffected", func(t *testing.T) {
		rt := createClaimReclaimRuntime(t, ctx, "AG filter hidden rt")
		a, issue := createClaimReclaimAgentAndIssue(t, ctx, rt, "AG filter hidden agent")
		hiddenTask := seedQueuedIssueTask(t, ctx, a, rt, issue)
		rt2 := createClaimReclaimRuntime(t, ctx, "AG filter plain rt")
		a2, issue2 := createClaimReclaimAgentAndIssue(t, ctx, rt2, "AG filter plain agent")
		plainTask := seedQueuedIssueTask(t, ctx, a2, rt2, issue2)

		code, resp, raw := agPostBatchClaim(t, []string{rt, rt2}, map[string]string{rt: "urgent"})
		if code != http.StatusOK || len(resp.Tasks) != 1 || resp.Tasks[0].ID != plainTask {
			t.Fatalf("filtered request: %d %s, want only %s", code, raw, plainTask)
		}
		if resp.Tasks[0].AGIssuePriority != "none" {
			t.Fatalf("ag_issue_priority = %q, want none", resp.Tasks[0].AGIssuePriority)
		}

		code, resp, raw = agPostBatchClaim(t, []string{rt, rt2}, nil)
		if code != http.StatusOK || len(resp.Tasks) != 1 || resp.Tasks[0].ID != hiddenTask {
			t.Fatalf("old-shaped request: %d %s, want the previously hidden %s", code, raw, hiddenTask)
		}
	})

	t.Run("AG patch: an issue-less task is not claimed on a filtered runtime", func(t *testing.T) {
		rt := createClaimReclaimRuntime(t, ctx, "AG filter issueless rt")
		a, _ := createClaimReclaimAgentAndIssue(t, ctx, rt, "AG filter issueless agent")
		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'queued', 0) RETURNING id
		`, a, rt).Scan(&taskID); err != nil {
			t.Fatalf("seed issue-less task: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

		code, resp, raw := agPostBatchClaim(t, []string{rt}, map[string]string{rt: "urgent"})
		if code != http.StatusOK || len(resp.Tasks) != 0 {
			t.Fatalf("filtered request: %d %s, want nothing claimed", code, raw)
		}
		if s := agTaskStatus(t, taskID); s != "queued" {
			t.Fatalf("issue-less task is %q, want it left queued", s)
		}
	})

	t.Run("AG patch: an unknown priority or runtime id is a 400 that claims nothing", func(t *testing.T) {
		rt := createClaimReclaimRuntime(t, ctx, "AG filter bad rt")
		a, issue := createClaimReclaimAgentAndIssue(t, ctx, rt, "AG filter bad agent")
		task := seedQueuedIssueTask(t, ctx, a, rt, issue)

		for name, filter := range map[string]map[string]string{
			"unknown priority": {rt: "critical"},
			"empty priority":   {rt: ""},
			"bad runtime id":   {"not-a-uuid": "urgent"},
		} {
			if code, _, raw := agPostBatchClaim(t, []string{rt}, filter); code != http.StatusBadRequest {
				t.Errorf("%s: status %d (%s), want 400", name, code, raw)
			}
		}
		if s := agTaskStatus(t, task); s != "queued" {
			t.Fatalf("task is %q after rejected requests, want it left queued", s)
		}
	})
}

func TestAGPatchClaimFilterWireEdgeCases(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	t.Run("AG patch: an empty or null ag_min_issue_priority claims as before", func(t *testing.T) {
		for name, raw := range map[string]string{"empty object": `{}`, "null": `null`} {
			rt := createClaimReclaimRuntime(t, ctx, "AG filter "+name+" rt")
			a, issue := createClaimReclaimAgentAndIssue(t, ctx, rt, "AG filter "+name+" agent")
			task := seedQueuedIssueTask(t, ctx, a, rt, issue)

			body := map[string]any{
				"daemon_id": batchClaimTestDaemonID, "runtime_ids": []string{rt}, "max_tasks": 5,
				"ag_min_issue_priority": json.RawMessage(raw),
			}
			req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/tasks/claim", body)
			w := testutil.Call(t, testHandler.ClaimTasksByRuntime, req)
			var resp agClaimResponse
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			if w.Code != http.StatusOK || len(resp.Tasks) != 1 || resp.Tasks[0].ID != task {
				t.Errorf("%s: %d %s, want the queued task %s claimed", name, w.Code, w.Body.String(), task)
			}
		}
	})

	t.Run("AG patch: an upper-case runtime id still filters that runtime", func(t *testing.T) {
		rt := createClaimReclaimRuntime(t, ctx, "AG filter upper-case rt")
		a, issue := createClaimReclaimAgentAndIssue(t, ctx, rt, "AG filter upper-case agent")
		task := seedQueuedIssueTask(t, ctx, a, rt, issue)

		code, resp, raw := agPostBatchClaim(t, []string{rt}, map[string]string{strings.ToUpper(rt): "urgent"})
		if code != http.StatusOK || len(resp.Tasks) != 0 {
			t.Fatalf("%d %s, want nothing claimed", code, raw)
		}
		if s := agTaskStatus(t, task); s != "queued" {
			t.Fatalf("task is %q, want it left queued", s)
		}
	})

	t.Run("AG patch: a filtered runtime keeps serving urgent issues after a non-urgent miss", func(t *testing.T) {
		rt := createClaimReclaimRuntime(t, ctx, "AG filter miss rt")
		a, lowIssue := createClaimReclaimAgentAndIssue(t, ctx, rt, "AG filter miss agent")
		lowTask := seedQueuedIssueTask(t, ctx, a, rt, lowIssue)
		filter := map[string]string{rt: "urgent"}

		if code, resp, raw := agPostBatchClaim(t, []string{rt}, filter); code != http.StatusOK || len(resp.Tasks) != 0 {
			t.Fatalf("first poll: %d %s, want nothing claimed", code, raw)
		}
		agSetIssuePriority(t, lowIssue, "urgent")
		code, resp, raw := agPostBatchClaim(t, []string{rt}, filter)
		if code != http.StatusOK || len(resp.Tasks) != 1 || resp.Tasks[0].ID != lowTask {
			t.Fatalf("second poll: %d %s, want %s claimed", code, raw, lowTask)
		}
	})
}

func TestAGPatchClaimFilterCapabilityOnHeartbeatAck(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	t.Run("AG patch: the HTTP heartbeat ack advertises ag_capabilities", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/heartbeat",
			map[string]any{"runtime_id": runtimeID})
		w := testutil.Call(t, testHandler.DaemonHeartbeat, req).Want(http.StatusOK)
		var ack struct {
			AGCapabilities []string `json:"ag_capabilities"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil || len(ack.AGCapabilities) != 1 || ack.AGCapabilities[0] != "claim_filter" {
			t.Fatalf("ack = %s (%v), want ag_capabilities [claim_filter]", w.Body.String(), err)
		}
	})

	t.Run("AG patch: the WebSocket heartbeat ack advertises ag_capabilities", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		identity := daemonws.ClientIdentity{
			WorkspaceID:   testWorkspaceID,
			WorkspaceIDs:  []string{testWorkspaceID},
			RuntimeIDs:    []string{runtimeID},
			RuntimeLeases: map[string]*daemonws.RuntimeLease{runtimeID: daemonws.NewRuntimeLease(testWorkspaceID, "online", time.Now(), true)},
		}
		ack, err := testHandler.HandleDaemonWSHeartbeat(context.Background(), identity, runtimeID, true)
		if err != nil || ack == nil {
			t.Fatalf("ws heartbeat: %v %v", ack, err)
		}
		if len(ack.AGCapabilities) != 1 || ack.AGCapabilities[0] != "claim_filter" {
			t.Fatalf("AGCapabilities = %v, want [claim_filter]", ack.AGCapabilities)
		}
	})
}

func TestAGPatchClaimFilterAppliesOverTheWebSocketRPC(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	rt := createClaimReclaimRuntime(t, ctx, "AG filter ws rt")
	a, issue := createClaimReclaimAgentAndIssue(t, ctx, rt, "AG filter ws agent")
	task := seedQueuedIssueTask(t, ctx, a, rt, issue)
	identity := daemonws.ClientIdentity{
		// User-credential connection (the runtime owner): no daemon id.
		UserID:       testUserID,
		WorkspaceID:  testWorkspaceID,
		WorkspaceIDs: []string{testWorkspaceID},
		RuntimeIDs:   []string{rt},
	}
	claim := func(filter map[string]string) (int, agClaimResponse) {
		body, _ := json.Marshal(map[string]any{
			"daemon_id": "ws-daemon", "runtime_ids": []string{rt}, "max_tasks": 5, "ag_min_issue_priority": filter,
		})
		status, respBody, err := testHandler.DaemonRPCHandler(ctx, identity, "tasks.claim", body)
		if err != nil {
			t.Fatalf("DaemonRPCHandler: %v", err)
		}
		var resp agClaimResponse
		_ = json.Unmarshal(respBody, &resp)
		return status, resp
	}

	if status, resp := claim(map[string]string{rt: "urgent"}); status != http.StatusOK || len(resp.Tasks) != 0 {
		t.Fatalf("filtered rpc claim = %d %+v, want nothing", status, resp)
	}
	if status, _ := claim(map[string]string{rt: "critical"}); status != http.StatusBadRequest {
		t.Fatalf("unknown priority over rpc = %d, want 400", status)
	}
	agSetIssuePriority(t, issue, "urgent")
	if status, resp := claim(map[string]string{rt: "urgent"}); status != http.StatusOK || len(resp.Tasks) != 1 || resp.Tasks[0].ID != task {
		t.Fatalf("filtered rpc claim after the issue became urgent = %d %+v, want %s", status, resp, task)
	}
}
