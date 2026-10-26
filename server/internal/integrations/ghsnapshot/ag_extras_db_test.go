package ghsnapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// These need Postgres and are not run by ag-patches/test.sh; see its header.

type agStoredPR struct {
	ReviewDecision *string
	LatestReviews  []byte
	BaseRefName    *string
}

func agReadPR(t *testing.T, pool *pgxpool.Pool, prID pgtype.UUID) agStoredPR {
	t.Helper()
	var s agStoredPR
	if err := pool.QueryRow(context.Background(),
		`SELECT review_decision, latest_reviews, base_ref_name FROM github_pull_request WHERE id=$1`, prID,
	).Scan(&s.ReviewDecision, &s.LatestReviews, &s.BaseRefName); err != nil {
		t.Fatal(err)
	}
	return s
}

func agReadRequired(t *testing.T, pool *pgxpool.Pool, prID pgtype.UUID) []*bool {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT is_required FROM github_pull_request_check_run WHERE pr_id=$1 ORDER BY ordinal`, prID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []*bool
	for rows.Next() {
		var r *bool
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestAGPatchApplySnapshotStoresExtras(t *testing.T) {
	pool := testDBPool(t)
	q := db.New(pool)
	m := NewManager(nil, q, pool, nil)
	ctx := context.Background()
	pr := seedPR(t, pool, q, "H1")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM github_pull_request_check_run WHERE pr_id=$1`, pr.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE id=$1`, pr.ID)
	})
	tr, fa := true, false

	t.Run("AG patch: extras and per-check required-ness are stored with the snapshot", func(t *testing.T) {
		applied, err := m.applySnapshot(ctx, pr.ID, &PRSnapshot{
			HeadSHA: "H1", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", HasChecks: true, RollupState: "PENDING",
			Contexts: []CheckContext{
				{Name: "backend", Status: "completed", Conclusion: "success", Required: &tr},
				{Name: "lint", Status: "completed", Conclusion: "failure", Required: &fa},
				{Name: "e2e", Status: "in_progress"},
			},
			AG: AGSnapshotExtras{
				Available: true, ReviewDecision: "APPROVED", BaseRefName: "latest",
				LatestReviews: []AGReview{{Author: "alan", State: "APPROVED", SubmittedAt: "2026-10-16T10:00:00Z"}},
			},
		})
		if err != nil || !applied {
			t.Fatalf("applySnapshot = %v, %v", applied, err)
		}
		got := agReadPR(t, pool, pr.ID)
		if got.ReviewDecision == nil || *got.ReviewDecision != "APPROVED" || got.BaseRefName == nil || *got.BaseRefName != "latest" {
			t.Fatalf("stored = %+v", got)
		}
		var reviews []AGReview
		if err := json.Unmarshal(got.LatestReviews, &reviews); err != nil || len(reviews) != 1 || reviews[0].Author != "alan" {
			t.Fatalf("latest_reviews = %s (%v)", got.LatestReviews, err)
		}
		req := agReadRequired(t, pool, pr.ID)
		if len(req) != 3 || req[0] == nil || !*req[0] || req[1] == nil || *req[1] || req[2] != nil {
			t.Fatalf("is_required = %v, want [true false null]", req)
		}
	})

	t.Run("AG patch: a snapshot for an older head writes none of it", func(t *testing.T) {
		applied, err := m.applySnapshot(ctx, pr.ID, &PRSnapshot{
			HeadSHA: "OLD", Contexts: []CheckContext{{Name: "x", Status: "completed", Conclusion: "success", Required: &fa}},
			AG: AGSnapshotExtras{Available: true, ReviewDecision: "REVIEW_REQUIRED", BaseRefName: "other"},
		})
		if err != nil || applied {
			t.Fatalf("applySnapshot = %v, %v; want not applied", applied, err)
		}
		if got := agReadPR(t, pool, pr.ID); got.ReviewDecision == nil || *got.ReviewDecision != "APPROVED" || *got.BaseRefName != "latest" {
			t.Fatalf("stale snapshot overwrote the extras: %+v", got)
		}
		if req := agReadRequired(t, pool, pr.ID); len(req) != 3 {
			t.Fatalf("stale snapshot replaced the checks: %v", req)
		}
	})

	t.Run("AG patch: a snapshot without the extras clears them rather than keeping old values", func(t *testing.T) {
		applied, err := m.applySnapshot(ctx, pr.ID, &PRSnapshot{
			HeadSHA: "H1", Mergeable: "MERGEABLE", HasChecks: true, RollupState: "SUCCESS",
			Contexts: []CheckContext{{Name: "backend", Status: "completed", Conclusion: "success"}},
		})
		if err != nil || !applied {
			t.Fatalf("applySnapshot = %v, %v", applied, err)
		}
		got := agReadPR(t, pool, pr.ID)
		if got.ReviewDecision != nil || got.BaseRefName != nil || got.LatestReviews != nil {
			t.Fatalf("old extras survived: %+v", got)
		}
		if req := agReadRequired(t, pool, pr.ID); len(req) != 1 || req[0] != nil {
			t.Fatalf("is_required = %v, want [null]", req)
		}
	})

	t.Run("AG patch: an empty review list is stored as [], distinct from not carried", func(t *testing.T) {
		applied, err := m.applySnapshot(ctx, pr.ID, &PRSnapshot{
			HeadSHA: "H1", AG: AGSnapshotExtras{Available: true, BaseRefName: "latest"},
		})
		if err != nil || !applied {
			t.Fatalf("applySnapshot = %v, %v", applied, err)
		}
		got := agReadPR(t, pool, pr.ID)
		if string(got.LatestReviews) != "[]" || got.ReviewDecision != nil {
			t.Fatalf("stored = %s / %v", got.LatestReviews, got.ReviewDecision)
		}
	})
}

func TestAGPatchApplySnapshotCapsLatestReviews(t *testing.T) {
	pool := testDBPool(t)
	q := db.New(pool)
	m := NewManager(nil, q, pool, nil)
	pr := seedPR(t, pool, q, "H1")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM github_pull_request_check_run WHERE pr_id=$1`, pr.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE id=$1`, pr.ID)
	})

	t.Run("AG patch: more than 20 latest reviews store the first 20, in order", func(t *testing.T) {
		var reviews []AGReview
		for i := 0; i < 25; i++ {
			reviews = append(reviews, AGReview{Author: fmt.Sprintf("reviewer-%02d", i), State: "COMMENTED", SubmittedAt: "2026-10-16T10:00:00Z"})
		}
		applied, err := m.applySnapshot(context.Background(), pr.ID, &PRSnapshot{
			HeadSHA: "H1", AG: AGSnapshotExtras{Available: true, LatestReviews: reviews},
		})
		if err != nil || !applied {
			t.Fatalf("applySnapshot = %v, %v", applied, err)
		}
		var stored []AGReview
		if err := json.Unmarshal(agReadPR(t, pool, pr.ID).LatestReviews, &stored); err != nil {
			t.Fatal(err)
		}
		if len(stored) != 20 || stored[0].Author != "reviewer-00" || stored[19].Author != "reviewer-19" {
			t.Fatalf("stored %d reviews, first %v last %v", len(stored), stored[0], stored[len(stored)-1])
		}
	})

	t.Run("AG patch: required-ness of a re-snapshot replaces the old values, including back to null", func(t *testing.T) {
		tr := true
		apply := func(required *bool) []*bool {
			applied, err := m.applySnapshot(context.Background(), pr.ID, &PRSnapshot{
				HeadSHA: "H1", HasChecks: true,
				Contexts: []CheckContext{{Name: "backend", Status: "completed", Conclusion: "success", Required: required}},
				AG:       AGSnapshotExtras{Available: true},
			})
			if err != nil || !applied {
				t.Fatalf("applySnapshot = %v, %v", applied, err)
			}
			return agReadRequired(t, pool, pr.ID)
		}
		if got := apply(&tr); len(got) != 1 || got[0] == nil || !*got[0] {
			t.Fatalf("is_required = %v, want [true]", got)
		}
		if got := apply(nil); len(got) != 1 || got[0] != nil {
			t.Fatalf("is_required = %v, want [null] after a snapshot that could not fetch it", got)
		}
	})
}

func TestAGPatchApplySnapshotStoresRunFields(t *testing.T) {
	pool := testDBPool(t)
	q := db.New(pool)
	m := NewManager(nil, q, pool, nil)
	ctx := context.Background()
	pr := seedPR(t, pool, q, "H2")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM github_pull_request_check_run WHERE pr_id=$1`, pr.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE id=$1`, pr.ID)
	})
	type stored struct {
		Workflow          *string
		RunID             *int64
		Started           *time.Time
		Suite, Conclusion *string
	}
	read := func() []stored {
		rows, err := pool.Query(ctx, `SELECT workflow_name, run_id, started_at, suite_status, suite_conclusion
			FROM github_pull_request_check_run WHERE pr_id=$1 ORDER BY ordinal`, pr.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []stored
		for rows.Next() {
			var s stored
			if err := rows.Scan(&s.Workflow, &s.RunID, &s.Started, &s.Suite, &s.Conclusion); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	started := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)

	t.Run("AG patch: run fields are stored on their own row and the rest stay null", func(t *testing.T) {
		applied, err := m.applySnapshot(ctx, pr.ID, &PRSnapshot{
			HeadSHA: "H2", Mergeable: "MERGEABLE", HasChecks: true, RollupState: "SUCCESS",
			Contexts: []CheckContext{
				{Name: "report", Status: "completed", Conclusion: "cancelled", AGRun: &AGCheckRun{WorkflowName: "CI", RunID: 40309530097, StartedAt: started, SuiteStatus: "completed", SuiteConclusion: "cancelled"}},
				{Name: "report", Status: "completed", Conclusion: "success", AGRun: &AGCheckRun{WorkflowName: "CI", RunID: 40313137478, StartedAt: started.Add(time.Hour), SuiteStatus: "completed", SuiteConclusion: "success"}},
				{Name: "external", Status: "completed", Conclusion: "success", AGRun: &AGCheckRun{SuiteStatus: "completed"}},
				{Name: "vercel", Status: "completed", Conclusion: "success", IsStatusContext: true},
			},
		})
		if err != nil || !applied {
			t.Fatalf("applySnapshot = %v, %v", applied, err)
		}
		got := read()
		if len(got) != 4 {
			t.Fatalf("rows = %d", len(got))
		}
		if got[0].RunID == nil || *got[0].RunID != 40309530097 || got[1].RunID == nil || *got[1].RunID != 40313137478 ||
			got[0].Workflow == nil || *got[0].Workflow != "CI" || got[0].Started == nil || !got[0].Started.Equal(started) ||
			got[1].Conclusion == nil || *got[1].Conclusion != "success" {
			t.Fatalf("stored = %+v / %+v", got[0], got[1])
		}
		if got[2].Workflow != nil || got[2].RunID != nil || got[2].Started != nil || got[2].Suite == nil || got[2].Conclusion != nil {
			t.Fatalf("non-Actions run stored %+v", got[2])
		}
		if got[3] != (stored{}) {
			t.Fatalf("status context stored %+v, want all null", got[3])
		}
	})

	t.Run("AG patch: a later snapshot without run fields leaves them null rather than keeping old ones", func(t *testing.T) {
		applied, err := m.applySnapshot(ctx, pr.ID, &PRSnapshot{
			HeadSHA: "H2", Contexts: []CheckContext{{Name: "report", Status: "completed", Conclusion: "success"}},
		})
		if err != nil || !applied {
			t.Fatalf("applySnapshot = %v, %v", applied, err)
		}
		if got := read(); len(got) != 1 || got[0] != (stored{}) {
			t.Fatalf("stored = %+v", got)
		}
	})
}
