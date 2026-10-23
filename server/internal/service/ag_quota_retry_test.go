package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func TestAGPatchQuotaFireAt(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	epoch := func(d time.Duration) string {
		return fmt.Sprintf("usage limit reached|%d", now.Add(d).Unix())
	}
	cases := []struct {
		name     string
		ctx      context.Context
		reason   string
		text     string
		unchaged bool
		want     time.Time
	}{
		{"text reset plus two minutes", context.Background(), agQuotaReason, epoch(3 * time.Hour), false, now.Add(3*time.Hour + 2*time.Minute)},
		{"daemon reset wins over text", AGWithRetryNotBefore(context.Background(), now.Add(time.Hour).Format(time.RFC3339)), agQuotaReason, epoch(5 * time.Hour), false, now.Add(time.Hour + 2*time.Minute)},
		{"past reset fires in two minutes", context.Background(), agQuotaReason, epoch(-time.Hour), false, now.Add(2 * time.Minute)},
		{"reset beyond eight days is unknown", context.Background(), agQuotaReason, epoch(9 * 24 * time.Hour), false, now.Add(time.Hour)},
		{"reset inside eight days is kept", context.Background(), agQuotaReason, epoch(7 * 24 * time.Hour), false, now.Add(7*24*time.Hour + 2*time.Minute)},
		{"no reset defers an hour", context.Background(), agQuotaReason, "you've hit your limit", false, now.Add(time.Hour)},
		{"malformed daemon time falls back to the text", AGWithRetryNotBefore(context.Background(), "tomorrow"), agQuotaReason, epoch(3 * time.Hour), false, now.Add(3*time.Hour + 2*time.Minute)},
		{"other reasons are untouched", context.Background(), "timeout", epoch(3 * time.Hour), true, time.Time{}},
	}
	for _, tc := range cases {
		got := agQuotaFireAt(tc.ctx, tc.reason, tc.text, now, time.Time{})
		if !got.Equal(tc.want) {
			t.Errorf("%s: fire_at = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A daemon-reported reset time makes the failure a quota limit even when the
// text would classify as context_overflow ("token" + "limit").
func TestAGPatchQuotaNotBeforeOverridesClassifier(t *testing.T) {
	text := "You have reached your token limit for this window"
	if got := taskfailure.Classify(text); got != taskfailure.ReasonAgentContextOverflow {
		t.Fatalf("precondition: classifier gave %q, want context_overflow", got)
	}
	withField := AGWithRetryNotBefore(context.Background(), time.Now().Add(time.Hour).Format(time.RFC3339))
	if got := agQuotaFailureReason(withField, taskfailure.Classify(text).String()); got != agQuotaReason {
		t.Errorf("with ag_retry_not_before: reason = %q, want %q", got, agQuotaReason)
	}
	for name, ctx := range map[string]context.Context{
		"absent":    context.Background(),
		"empty":     AGWithRetryNotBefore(context.Background(), ""),
		"malformed": AGWithRetryNotBefore(context.Background(), "later"),
	} {
		if got := agQuotaFailureReason(ctx, "agent_error.context_overflow"); got != "agent_error.context_overflow" {
			t.Errorf("%s: reason = %q, want it unchanged", name, got)
		}
	}
}

func TestAGPatchQuotaRetryEligibility(t *testing.T) {
	mk := func(attempt, max int32, errText string) db.AgentTaskQueue {
		return db.AgentTaskQueue{
			Attempt:     attempt,
			MaxAttempts: max,
			IssueID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
			Error:       pgtype.Text{String: errText, Valid: errText != ""},
		}
	}
	autopilot := mk(1, 2, "")
	autopilot.AutopilotRunID = pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	cases := []struct {
		name string
		task db.AgentTaskQueue
		want bool
	}{
		{"first failure retries", mk(1, 2, "usage limit reached|1760000000"), true},
		{"second failure retries", mk(2, 2, "usage limit reached|1760000000"), true},
		{"third attempt is the ceiling", mk(3, 2, "usage limit reached|1760000000"), false},
		{"retry disabled stays disabled", mk(1, 1, "usage limit reached|1760000000"), false},
		{"402 is billing", mk(1, 2, "API Error: 402"), false},
		{"insufficient_balance is billing", mk(1, 2, "insufficient_balance"), false},
		{"credits is billing", mk(1, 2, "out of credits"), false},
		{"balance is too low is billing", mk(1, 2, "credit balance is too low"), false},
		{"autopilot is excluded", autopilot, false},
	}
	for _, tc := range cases {
		if got := retryEligible(agQuotaReason, tc.task); got != tc.want {
			t.Errorf("%s: retryEligible = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := retryAttemptCeiling(agQuotaReason, 2); got != 3 {
		t.Errorf("ceiling for max_attempts=2 = %d, want 3", got)
	}
	if got := retryDelayForAttempt(agQuotaReason, 1); got != time.Hour {
		t.Errorf("default delay = %s, want 1h", got)
	}
}

func agQuotaFixture(t *testing.T) (svc *TaskService, parentID pgtype.UUID, runtimeID string, cleanup func()) {
	t.Helper()
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	_, _, agentID, issueID := seedAttributionFixture(t, pool)
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("read agent runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, attempt, max_attempts, session_id, work_dir)
		VALUES ($1, $2, $3, 'running', 0, 1, 2, 'src-session', '/tmp/src-workdir')
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&parentID); err != nil {
		t.Fatalf("insert parent task: %v", err)
	}
	cleanup = func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE parent_task_id = $1 OR id = $1`, parentID)
	}
	t.Cleanup(cleanup)
	return NewTaskService(db.New(pool), pool, nil, events.New()), parentID, runtimeID, cleanup
}

// DB-backed: run with DATABASE_URL set (ag-patches/test.sh does not).
func TestAGPatchQuotaFailDefersRetryToReset(t *testing.T) {
	svc, parentID, _, _ := agQuotaFixture(t)
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()

	reset := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	text := fmt.Sprintf("Claude AI usage limit reached|%d", reset.Unix())
	if _, err := svc.FailTask(ctx, parentID, text, "src-session", "/tmp/src-workdir", "", "", false, "", ""); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	var status, session, workDir, reason string
	var attempt, maxAttempts int32
	var fireAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `
		SELECT status, session_id, work_dir, attempt, max_attempts, fire_at
		FROM agent_task_queue WHERE parent_task_id = $1`, parentID).Scan(&status, &session, &workDir, &attempt, &maxAttempts, &fireAt); err != nil {
		t.Fatalf("read retry child: %v", err)
	}
	if status != "deferred" || !fireAt.Valid {
		t.Fatalf("child = status:%q fire_at_valid:%v, want deferred with fire_at", status, fireAt.Valid)
	}
	if want := reset.Add(2 * time.Minute); fireAt.Time.Sub(want).Abs() > time.Second {
		t.Errorf("fire_at = %v, want %v", fireAt.Time, want)
	}
	if session != "src-session" || workDir != "/tmp/src-workdir" {
		t.Errorf("child session/work_dir = %q/%q, want the parent's", session, workDir)
	}
	if attempt != 2 || maxAttempts != 3 {
		t.Errorf("child attempt/max = %d/%d, want 2/3", attempt, maxAttempts)
	}
	if err := pool.QueryRow(ctx, `SELECT failure_reason FROM agent_task_queue WHERE id = $1`, parentID).Scan(&reason); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if reason != agQuotaReason {
		t.Errorf("parent failure_reason = %q, want %q", reason, agQuotaReason)
	}
}

// DB-backed: run with DATABASE_URL set (ag-patches/test.sh does not).
func TestAGPatchQuotaFailBillingAndOverride(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()

	t.Run("billing text is not retried", func(t *testing.T) {
		svc, parentID, _, _ := agQuotaFixture(t)
		if _, err := svc.FailTask(ctx, parentID, "API Error: 402 insufficient_balance", "src-session", "/tmp/src-workdir", "", "", false, "", ""); err != nil {
			t.Fatalf("FailTask: %v", err)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1`, parentID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("retry children = %d, want 0", n)
		}
	})

	t.Run("ag_retry_not_before beats a context_overflow text", func(t *testing.T) {
		svc, parentID, _, _ := agQuotaFixture(t)
		reset := time.Now().Add(90 * time.Minute).Truncate(time.Second)
		fctx := AGWithRetryNotBefore(ctx, reset.Format(time.RFC3339))
		if _, err := svc.FailTask(fctx, parentID, "token limit for this window", "src-session", "/tmp/src-workdir", "", "", false, "", ""); err != nil {
			t.Fatalf("FailTask: %v", err)
		}
		var status string
		var fireAt pgtype.Timestamptz
		if err := pool.QueryRow(ctx, `SELECT status, fire_at FROM agent_task_queue WHERE parent_task_id = $1`, parentID).Scan(&status, &fireAt); err != nil {
			t.Fatalf("read retry child: %v", err)
		}
		if status != "deferred" || fireAt.Time.Sub(reset.Add(2*time.Minute)).Abs() > time.Second {
			t.Errorf("child = %q fire_at %v, want deferred at %v", status, fireAt.Time, reset.Add(2*time.Minute))
		}
	})
}

// The daemon's time is held to the same rules as a parsed one: slack added, a
// past time clamped to now, anything beyond eight days treated as unknown (it
// does not fall back to the text, which the daemon already had a chance to
// read).
func TestAGPatchQuotaFireAtDaemonTime(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) context.Context {
		return AGWithRetryNotBefore(context.Background(), now.Add(d).Format(time.RFC3339))
	}
	plusOne := time.FixedZone("BST", 3600)
	cases := []struct {
		name string
		ctx  context.Context
		want time.Time
	}{
		{"past daemon time fires in two minutes", at(-6 * time.Hour), now.Add(2 * time.Minute)},
		{"exactly now fires in two minutes", at(0), now.Add(2 * time.Minute)},
		{"daemon time beyond eight days is unknown", at(8*24*time.Hour + time.Second), now.Add(time.Hour)},
		{"daemon time at the eight day edge is kept", at(8 * 24 * time.Hour), now.Add(8*24*time.Hour + 2*time.Minute)},
		{"offset timestamps are the same instant", AGWithRetryNotBefore(context.Background(), now.Add(time.Hour).In(plusOne).Format(time.RFC3339)), now.Add(time.Hour + 2*time.Minute)},
	}
	for _, tc := range cases {
		// The text names a different reset, so a fallback to it would show.
		text := fmt.Sprintf("usage limit reached|%d", now.Add(5*time.Hour).Unix())
		if got := agQuotaFireAt(tc.ctx, agQuotaReason, text, now, time.Time{}); !got.Equal(tc.want) {
			t.Errorf("%s: fire_at = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Only provider_quota_limit is touched: other retryable reasons keep their
// delay and ceiling, and the billing exclusion does not leak onto them.
func TestAGPatchQuotaLeavesOtherReasonsAlone(t *testing.T) {
	if got := retryDelayForAttempt(string(taskfailure.ReasonRuntimeOffline), 1); got != runtimeOfflineRetryDeferral {
		t.Errorf("runtime_offline delay = %s, want %s", got, runtimeOfflineRetryDeferral)
	}
	if got := retryAttemptCeiling(string(taskfailure.ReasonRuntimeOffline), 2); got != 2 {
		t.Errorf("runtime_offline ceiling = %d, want 2", got)
	}
	if got := retryAttemptCeiling(agQuotaReason, 5); got != 5 {
		t.Errorf("quota ceiling with max_attempts=5 = %d, want it left at 5", got)
	}
	task := db.AgentTaskQueue{
		Attempt:     1,
		MaxAttempts: 2,
		IssueID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		Error:       pgtype.Text{String: "out of credits", Valid: true},
	}
	if !retryEligible(string(taskfailure.ReasonRuntimeOffline), task) {
		t.Error("runtime_offline with billing-like text must stay retryable")
	}
	if retryEligible("agent_error", task) {
		t.Error("an unlisted reason must stay non-retryable")
	}
}

// Autopilot is covered above; Triage and the link requirement stay as they are.
func TestAGPatchQuotaRetryEligibilityLinks(t *testing.T) {
	base := db.AgentTaskQueue{Attempt: 1, MaxAttempts: 2, Error: pgtype.Text{String: "usage limit reached|1760000000", Valid: true}}
	withIssue := base
	withIssue.IssueID = pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	withChat := base
	withChat.ChatSessionID = pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	triage := withIssue
	triage.Context = []byte(`{"type":"` + TriageContextType + `"}`)
	cases := []struct {
		name string
		task db.AgentTaskQueue
		want bool
	}{
		{"issue task retries", withIssue, true},
		{"chat task retries", withChat, true},
		{"no issue or chat link", base, false},
		{"triage task is excluded", triage, false},
	}
	for _, tc := range cases {
		if got := retryEligible(agQuotaReason, tc.task); got != tc.want {
			t.Errorf("%s: retryEligible = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// DB-backed: run with DATABASE_URL set (ag-patches/test.sh does not).
func TestAGPatchQuotaFailAtCeilingDoesNotRetry(t *testing.T) {
	svc, parentID, _, _ := agQuotaFixture(t)
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET attempt = 3, max_attempts = 3 WHERE id = $1`, parentID); err != nil {
		t.Fatalf("raise attempt: %v", err)
	}
	text := fmt.Sprintf("Claude AI usage limit reached|%d", time.Now().Add(time.Hour).Unix())
	if _, err := svc.FailTask(ctx, parentID, text, "src-session", "/tmp/src-workdir", "", "", false, "", ""); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1`, parentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("retry children at attempt 3 = %d, want 0", n)
	}
}

// DB-backed: run with DATABASE_URL set (ag-patches/test.sh does not). The
// sweeper path has no daemon time in hand and reads the stored error text.
func TestAGPatchQuotaSweeperRetryUsesStoredResetText(t *testing.T) {
	svc, parentID, _, _ := agQuotaFixture(t)
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	reset := time.Now().Add(4 * time.Hour).Truncate(time.Second)
	text := fmt.Sprintf("Claude AI usage limit reached|%d", reset.Unix())
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'failed', failure_reason = $2, error = $3 WHERE id = $1`,
		parentID, agQuotaReason, text); err != nil {
		t.Fatalf("mark parent failed: %v", err)
	}
	parent, err := svc.Queries.GetAgentTask(ctx, parentID)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	child, err := svc.MaybeRetryFailedTask(ctx, parent)
	if err != nil || child == nil {
		t.Fatalf("MaybeRetryFailedTask = %v, %v; want a deferred child", child, err)
	}
	if child.Status != "deferred" || !child.FireAt.Valid {
		t.Fatalf("child = status:%q fire_at_valid:%v, want deferred with fire_at", child.Status, child.FireAt.Valid)
	}
	if want := reset.Add(2 * time.Minute); child.FireAt.Time.Sub(want).Abs() > time.Second {
		t.Errorf("fire_at = %v, want %v", child.FireAt.Time, want)
	}
	if child.SessionID.String != "src-session" {
		t.Errorf("child session = %q, want the parent's", child.SessionID.String)
	}
}
