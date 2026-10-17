package service

// These need the test database (and, for the empty-claim case, REDIS_TEST_URL)
// and are not run by ag-patches/test.sh; see its header.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/agclaim"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// agClaimFilterFixture is one workspace with two runtimes, each with one agent.
type agClaimFilterFixture struct {
	ctx         context.Context
	pool        *pgxpool.Pool
	workspaceID string
	userID      string
	filteredRT  string // the runtime the tests put behind the filter
	plainRT     string
	filteredAg  string
	plainAg     string
	issueN      int
}

func newAGClaimFilterFixture(t *testing.T) *agClaimFilterFixture {
	t.Helper()
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	suffix := time.Now().UnixNano()
	f := &agClaimFilterFixture{ctx: ctx, pool: pool, issueN: 700000 + int(suffix%100000)*10}

	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1,$2) RETURNING id`,
		"AG Claim Filter", fmt.Sprintf("ag-claim-filter-%d@multica.ai", suffix)).Scan(&f.userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ($1,$2,'','AGF') RETURNING id`,
		"AG Claim Filter", fmt.Sprintf("ag-claim-filter-%d", suffix)).Scan(&f.workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)`, f.workspaceID)
		pool.Exec(ctx, `DELETE FROM issue WHERE workspace_id = $1`, f.workspaceID)
		pool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1`, f.workspaceID)
		pool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1`, f.workspaceID)
		pool.Exec(ctx, `DELETE FROM member WHERE workspace_id = $1`, f.workspaceID)
		pool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, f.workspaceID)
		pool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, f.userID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1,$2,'owner')`, f.workspaceID, f.userID); err != nil {
		t.Fatalf("create member: %v", err)
	}

	mkRuntime := func(name string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, visibility, owner_id)
			VALUES ($1, 'daemon-ag-claim', $2, 'cloud', $2, 'online', 'test runtime', '{}'::jsonb, now(), 'private', $3)
			RETURNING id`, f.workspaceID, name, f.userID).Scan(&id); err != nil {
			t.Fatalf("create runtime: %v", err)
		}
		return id
	}
	mkAgent := func(name, runtimeID string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id)
			VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 5, $4)
			RETURNING id`, f.workspaceID, name, runtimeID, f.userID).Scan(&id); err != nil {
			t.Fatalf("create agent: %v", err)
		}
		return id
	}
	f.filteredRT = mkRuntime("AG filtered RT")
	f.plainRT = mkRuntime("AG plain RT")
	f.filteredAg = mkAgent("AG filtered agent", f.filteredRT)
	f.plainAg = mkAgent("AG plain agent", f.plainRT)
	return f
}

// queueIssueTask queues a task for agent on a fresh issue of the given priority
// and returns the issue and task IDs. The task's own priority stays 0, so only
// the issue's priority can explain a claim decision.
func (f *agClaimFilterFixture) queueIssueTask(t *testing.T, agentID, runtimeID, priority string) (issueID, taskID string) {
	t.Helper()
	f.issueN++
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, $2, 'in_progress', $3, $4, 'member', $5, 0)
		RETURNING id`, f.workspaceID, "ag claim "+priority, priority, f.userID, f.issueN).Scan(&issueID); err != nil {
		t.Fatalf("create %s issue: %v", priority, err)
	}
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, status, priority, context, runtime_id)
		VALUES ($1, $2, 'queued', 0, '{}'::jsonb, $3) RETURNING id`, agentID, issueID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("queue %s task: %v", priority, err)
	}
	time.Sleep(2 * time.Millisecond)
	return issueID, taskID
}

// queueIssuelessTask queues a task with no issue, chat or autopilot link.
func (f *agClaimFilterFixture) queueIssuelessTask(t *testing.T, agentID, runtimeID string) string {
	t.Helper()
	var taskID string
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO agent_task_queue (agent_id, status, priority, context, runtime_id)
		VALUES ($1, 'queued', 0, '{}'::jsonb, $2) RETURNING id`, agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("queue issue-less task: %v", err)
	}
	return taskID
}

func (f *agClaimFilterFixture) status(t *testing.T, taskID string) string {
	t.Helper()
	var s string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&s); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	return s
}

func (f *agClaimFilterFixture) filterCtx(t *testing.T, min string, runtimeIDs ...string) context.Context {
	t.Helper()
	m := map[string]string{}
	for _, rid := range runtimeIDs {
		m[rid] = min
	}
	filter, err := agclaim.Parse(m)
	if err != nil {
		t.Fatal(err)
	}
	return agclaim.With(f.ctx, filter)
}

func claimedIDs(tasks []db.AgentTaskQueue) map[string]bool {
	out := map[string]bool{}
	for _, task := range tasks {
		out[util.UUIDToString(task.ID)] = true
	}
	return out
}

// claimUntilEmpty keeps claiming (one task per agent per call) until a call
// returns nothing, and returns every claimed task ID.
func claimUntilEmpty(t *testing.T, ctx context.Context, svc *TaskService, ids []pgtype.UUID) map[string]bool {
	t.Helper()
	all := map[string]bool{}
	for i := 0; i < 8; i++ {
		got, err := svc.ClaimTasksForRuntimes(ctx, ids, 5)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if len(got) == 0 {
			return all
		}
		for id := range claimedIDs(got) {
			all[id] = true
		}
	}
	t.Fatal("claim kept returning tasks")
	return nil
}

func TestAGPatchClaimFilterClaimsOnlyUrgentIssueTasks(t *testing.T) {
	f := newAGClaimFilterFixture(t)
	svc := NewTaskService(db.New(f.pool), f.pool, nil, events.New())
	ids := []pgtype.UUID{util.MustParseUUID(f.filteredRT)}

	_, highTask := f.queueIssueTask(t, f.filteredAg, f.filteredRT, "high")
	_, noneTask := f.queueIssueTask(t, f.filteredAg, f.filteredRT, "none")
	issuelessTask := f.queueIssuelessTask(t, f.filteredAg, f.filteredRT)
	_, urgentTask := f.queueIssueTask(t, f.filteredAg, f.filteredRT, "urgent")

	got := claimUntilEmpty(t, f.filterCtx(t, "urgent", f.filteredRT), svc, ids)
	if len(got) != 1 || !got[urgentTask] {
		t.Fatalf("claimed %v, want only the urgent task %s", got, urgentTask)
	}
	for name, id := range map[string]string{"high": highTask, "none": noneTask, "issue-less": issuelessTask} {
		if s := f.status(t, id); s != "queued" {
			t.Errorf("%s task is %q, want it left queued", name, s)
		}
	}
}

func TestAGPatchClaimFilterMinimumIncludesHigherPriorities(t *testing.T) {
	f := newAGClaimFilterFixture(t)
	svc := NewTaskService(db.New(f.pool), f.pool, nil, events.New())
	ids := []pgtype.UUID{util.MustParseUUID(f.filteredRT)}

	_, mediumTask := f.queueIssueTask(t, f.filteredAg, f.filteredRT, "medium")
	_, highTask := f.queueIssueTask(t, f.filteredAg, f.filteredRT, "high")
	_, urgentTask := f.queueIssueTask(t, f.filteredAg, f.filteredRT, "urgent")

	got := claimUntilEmpty(t, f.filterCtx(t, "high", f.filteredRT), svc, ids)
	if len(got) != 2 || !got[highTask] || !got[urgentTask] {
		t.Fatalf("claimed %v, want the high and urgent tasks", got)
	}
	if s := f.status(t, mediumTask); s != "queued" {
		t.Errorf("medium task is %q, want it left queued", s)
	}
}

func TestAGPatchClaimFilterUsesTheIssuesLivePriority(t *testing.T) {
	f := newAGClaimFilterFixture(t)
	svc := NewTaskService(db.New(f.pool), f.pool, nil, events.New())
	ids := []pgtype.UUID{util.MustParseUUID(f.filteredRT)}
	ctx := f.filterCtx(t, "urgent", f.filteredRT)

	raised, raisedTask := f.queueIssueTask(t, f.filteredAg, f.filteredRT, "high")
	if got := claimUntilEmpty(t, ctx, svc, ids); len(got) != 0 {
		t.Fatalf("claimed %v before the issue became urgent", got)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE issue SET priority = 'urgent' WHERE id = $1`, raised); err != nil {
		t.Fatal(err)
	}
	if got := claimUntilEmpty(t, ctx, svc, ids); len(got) != 1 || !got[raisedTask] {
		t.Fatalf("claimed %v after the issue became urgent, want %s", got, raisedTask)
	}

	lowered, loweredTask := f.queueIssueTask(t, f.filteredAg, f.filteredRT, "urgent")
	if _, err := f.pool.Exec(f.ctx, `UPDATE issue SET priority = 'low' WHERE id = $1`, lowered); err != nil {
		t.Fatal(err)
	}
	if got := claimUntilEmpty(t, ctx, svc, ids); len(got) != 0 {
		t.Fatalf("claimed %v for an issue lowered after enqueue", got)
	}
	if s := f.status(t, loweredTask); s != "queued" {
		t.Errorf("lowered task is %q, want it left queued", s)
	}
}

func TestAGPatchClaimFilterLeavesOtherRuntimesAndOldRequestsAlone(t *testing.T) {
	f := newAGClaimFilterFixture(t)
	svc := NewTaskService(db.New(f.pool), f.pool, nil, events.New())
	ids := []pgtype.UUID{util.MustParseUUID(f.filteredRT), util.MustParseUUID(f.plainRT)}

	_, filteredTask := f.queueIssueTask(t, f.filteredAg, f.filteredRT, "none")
	_, plainTask := f.queueIssueTask(t, f.plainAg, f.plainRT, "none")

	got := claimUntilEmpty(t, f.filterCtx(t, "urgent", f.filteredRT), svc, ids)
	if len(got) != 1 || !got[plainTask] {
		t.Fatalf("claimed %v, want only the unfiltered runtime's task %s", got, plainTask)
	}
	if s := f.status(t, filteredTask); s != "queued" {
		t.Errorf("filtered runtime's task is %q, want it left queued", s)
	}

	// A request that names no filter claims it, as before the patch.
	if got := claimUntilEmpty(t, f.ctx, svc, ids); len(got) != 1 || !got[filteredTask] {
		t.Fatalf("unfiltered claim got %v, want %s", got, filteredTask)
	}
}

func TestAGPatchClaimFilterDoesNotFilterStaleDispatchReclaim(t *testing.T) {
	f := newAGClaimFilterFixture(t)
	svc := NewTaskService(db.New(f.pool), f.pool, nil, events.New())
	ids := []pgtype.UUID{util.MustParseUUID(f.filteredRT)}

	_, taskID := f.queueIssueTask(t, f.filteredAg, f.filteredRT, "low")
	if _, err := f.pool.Exec(f.ctx, `
		UPDATE agent_task_queue SET status = 'dispatched', dispatched_at = now() - interval '120 seconds'
		WHERE id = $1`, taskID); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ClaimTasksForRuntimes(f.filterCtx(t, "urgent", f.filteredRT), ids, 5)
	if err != nil {
		t.Fatal(err)
	}
	if ids := claimedIDs(got); len(ids) != 1 || !ids[taskID] {
		t.Fatalf("reclaimed %v, want the stale dispatched task %s", ids, taskID)
	}
}

func TestAGPatchClaimFilterMissDoesNotMarkTheRuntimeEmpty(t *testing.T) {
	f := newAGClaimFilterFixture(t)
	rdb := newRedisTestClient(t)
	svc := NewTaskService(db.New(f.pool), f.pool, nil, events.New())
	svc.EmptyClaim = NewEmptyClaimCache(rdb)
	ids := []pgtype.UUID{util.MustParseUUID(f.filteredRT), util.MustParseUUID(f.plainRT)}

	_, hiddenTask := f.queueIssueTask(t, f.filteredAg, f.filteredRT, "none")

	// The plain runtime has nothing queued, so an ordinary miss marks it empty:
	// that is the control showing this test can see a mark.
	if got := claimUntilEmpty(t, f.filterCtx(t, "urgent", f.filteredRT), svc, ids); len(got) != 0 {
		t.Fatalf("claimed %v, want nothing", got)
	}
	if !svc.EmptyClaim.IsEmpty(f.ctx, f.plainRT) {
		t.Fatal("control: the unfiltered runtime with no tasks was not marked empty")
	}
	if svc.EmptyClaim.IsEmpty(f.ctx, f.filteredRT) {
		t.Fatal("a filtered miss marked the runtime empty; an unfiltered poll would skip its queued task")
	}

	if got := claimUntilEmpty(t, f.ctx, svc, ids[:1]); len(got) != 1 || !got[hiddenTask] {
		t.Fatalf("unfiltered poll claimed %v, want %s", got, hiddenTask)
	}
}
