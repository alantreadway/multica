package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// DB-backed: run with DATABASE_URL set (ag-patches/test.sh does not). Drives the
// daemon's /fail endpoint with the JSON body the patched daemon sends.
func TestAGPatchQuotaFailEndpointReadsRetryNotBefore(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var agentID, runtimeID string
	if err := testPool.QueryRow(ctx,
		`SELECT id, runtime_id FROM agent WHERE workspace_id = $1 AND runtime_id IS NOT NULL LIMIT 1`,
		testWorkspaceID).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("setup: get agent: %v", err)
	}
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position, assignee_type, assignee_id)
		VALUES ($1, 'quota retry fixture', 'in_progress', 'none', $2, 'member', 999098, 0, 'agent', $3)
		RETURNING id
	`, testWorkspaceID, testUserID, agentID).Scan(&issueID); err != nil {
		t.Fatalf("setup: create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	fail := func(t *testing.T, body map[string]any) (taskID string, w int) {
		t.Helper()
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, attempt, max_attempts, session_id, work_dir, created_at, started_at)
			VALUES ($1, $2, $3, 'running', 0, 1, 2, 'src-session', '/tmp/src-workdir', now() - interval '2 minutes', now() - interval '1 minute')
			RETURNING id
		`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
			t.Fatalf("setup: running task: %v", err)
		}
		t.Cleanup(func() {
			testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE parent_task_id = $1 OR id = $1`, taskID)
		})
		rec := newOwnerDaemonRequest("POST", "/api/daemon/tasks/"+taskID+"/fail", body)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("taskId", taskID)
		rec = rec.WithContext(context.WithValue(rec.Context(), chi.RouteCtxKey, rctx))
		rw := httptest.NewRecorder()
		testHandler.FailTask(rw, rec)
		return taskID, rw.Code
	}
	child := func(t *testing.T, parentID string) (status string, fireAt pgtype.Timestamptz, session string, found bool) {
		t.Helper()
		rows, err := testPool.Query(ctx, `SELECT status, fire_at, COALESCE(session_id, '') FROM agent_task_queue WHERE parent_task_id = $1`, parentID)
		if err != nil {
			t.Fatalf("read child: %v", err)
		}
		defer rows.Close()
		if rows.Next() {
			if err := rows.Scan(&status, &fireAt, &session); err != nil {
				t.Fatalf("scan child: %v", err)
			}
			found = true
		}
		return
	}

	t.Run("field makes a token-limit text a deferred quota retry", func(t *testing.T) {
		reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
		parentID, code := fail(t, map[string]any{
			"error":               "You have reached your token limit for this window",
			"session_id":          "src-session",
			"work_dir":            "/tmp/src-workdir",
			"failure_reason":      "agent_error",
			"ag_retry_not_before": reset.Format(time.RFC3339),
		})
		if code != http.StatusOK {
			t.Fatalf("FailTask: status %d", code)
		}
		var reason string
		if err := testPool.QueryRow(ctx, `SELECT failure_reason FROM agent_task_queue WHERE id = $1`, parentID).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		if reason != string(taskfailure.ReasonAgentProviderQuotaLimit) {
			t.Errorf("parent failure_reason = %q, want %q", reason, taskfailure.ReasonAgentProviderQuotaLimit)
		}
		status, fireAt, session, found := child(t, parentID)
		if !found || status != "deferred" {
			t.Fatalf("child found=%v status=%q, want a deferred retry", found, status)
		}
		if want := reset.Add(2 * time.Minute); fireAt.Time.Sub(want).Abs() > time.Second {
			t.Errorf("fire_at = %v, want %v", fireAt.Time, want)
		}
		if session != "src-session" {
			t.Errorf("child session_id = %q, want the parent's", session)
		}
	})

	t.Run("old daemon without the field still defers on the text", func(t *testing.T) {
		reset := time.Now().Add(3 * time.Hour).Truncate(time.Second)
		parentID, code := fail(t, map[string]any{
			"error":      fmt.Sprintf("Claude AI usage limit reached|%d", reset.Unix()),
			"session_id": "src-session",
			"work_dir":   "/tmp/src-workdir",
		})
		if code != http.StatusOK {
			t.Fatalf("FailTask: status %d", code)
		}
		status, fireAt, _, found := child(t, parentID)
		if !found || status != "deferred" {
			t.Fatalf("child found=%v status=%q, want a deferred retry", found, status)
		}
		if want := reset.Add(2 * time.Minute); fireAt.Time.Sub(want).Abs() > time.Second {
			t.Errorf("fire_at = %v, want %v", fireAt.Time, want)
		}
	})

	t.Run("field with billing text is not retried", func(t *testing.T) {
		parentID, code := fail(t, map[string]any{
			"error":               "API Error: 402 insufficient_balance",
			"ag_retry_not_before": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
		if code != http.StatusOK {
			t.Fatalf("FailTask: status %d", code)
		}
		if _, _, _, found := child(t, parentID); found {
			t.Error("billing failure produced a retry child")
		}
	})
}
