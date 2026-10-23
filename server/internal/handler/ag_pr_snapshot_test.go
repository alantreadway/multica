package handler

// These need the test database and are not run by ag-patches/test.sh; see its header.
// The hermetic rollup tests live in internal/integrations/ghsnapshot.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/ghsnapshot"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// agEnableSnapshotsForTest gives testHandler a configured (but never started)
// snapshot manager, so list responses treat a stored snapshot as available.
func agEnableSnapshotsForTest(t *testing.T) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_APP_ID", "1")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", string(pem.EncodeToMemory(
		&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})))
	client, err := ghsnapshot.NewClientFromEnv()
	if err != nil || client == nil {
		t.Fatalf("snapshot client: %v", err)
	}
	previous := testHandler.PRRefresh
	testHandler.PRRefresh = ghsnapshot.NewManager(client, testHandler.Queries, testPool, nil)
	t.Cleanup(func() { testHandler.PRRefresh = previous })
}

// agSeedSnapshotForTest stamps a current-head snapshot onto the mirrored PR.
func agSeedSnapshotForTest(t *testing.T, prNumber int32, extras string, checks ...[]any) string {
	t.Helper()
	ctx := context.Background()
	var prID string
	if err := testPool.QueryRow(ctx, `
		UPDATE github_pull_request
		SET head_sha = 'abc', snapshot_head_sha = 'abc', snapshot_fetched_at = now(),
		    api_mergeable = 'MERGEABLE', api_merge_state_status = 'BLOCKED', checks_rollup_state = 'PENDING'
		WHERE workspace_id = $1 AND pr_number = $2 RETURNING id::text`,
		testWorkspaceID, prNumber).Scan(&prID); err != nil {
		t.Fatalf("stamp snapshot: %v", err)
	}
	if extras != "" {
		if _, err := testPool.Exec(ctx, extras, prID); err != nil {
			t.Fatalf("store extras: %v", err)
		}
	}
	for i, c := range checks {
		args := append([]any{prID, i}, c...)
		if _, err := testPool.Exec(ctx, `
			INSERT INTO github_pull_request_check_run (pr_id, head_sha, ordinal, name, status, conclusion, is_required)
			VALUES ($1, 'abc', $2, $3, $4, $5, $6)`, args...); err != nil {
			t.Fatalf("insert check: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM github_pull_request_check_run WHERE pr_id = $1`, prID)
	})
	return prID
}

func agListRawForTest(t *testing.T, issueID string) []map[string]json.RawMessage {
	t.Helper()
	req := withURLParam(newRequest("GET", "/api/issues/"+issueID+"/pull-requests", nil), "id", issueID)
	w := testutil.Call(t, testHandler.ListPullRequestsForIssue, req).Want(200)
	var out struct {
		PullRequests []map[string]json.RawMessage `json:"pull_requests"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.PullRequests
}

func TestAGPatchListPullRequestsSnapshotFields(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	secret := "ag-snapshot-fields"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	const installationID int64 = 30264101
	agEnableSnapshotsForTest(t)
	created := prAutoCompleteTestIssue(t, "snapshot fields", installationID)
	firePRWebhook(t, secret, installationID, 71, created.Identifier+": fields", "", "feat/fields", "opened")
	firePRWebhook(t, secret, installationID, 72, created.Identifier+": no data", "", "feat/nodata", "opened")
	firePRWebhook(t, secret, installationID, 73, created.Identifier+": old snapshot", "", "feat/old", "opened")

	fieldsPR := agSeedSnapshotForTest(t, 71, `UPDATE github_pull_request
		SET review_decision = 'CHANGES_REQUESTED', base_ref_name = 'latest',
		    latest_reviews = '[{"author":"alan","state":"APPROVED","submitted_at":"2026-10-16T10:00:00Z"}]'::jsonb
		WHERE id = $1::uuid`,
		[]any{"backend", "completed", "success", true},
		[]any{"lint", "completed", "failure", false},
		[]any{"e2e", "in_progress", nil, true},
	)
	if _, err := testPool.Exec(context.Background(), `UPDATE github_pull_request_check_run
		SET workflow_name = 'CI', run_id = 40309530097, started_at = '2026-10-26T10:00:00+01:00',
		    suite_status = 'COMPLETED', suite_conclusion = 'SUCCESS'
		WHERE pr_id = $1::uuid AND name = 'backend'`, fieldsPR); err != nil {
		t.Fatal(err)
	}
	// A snapshot from before the patch: no extras stored, required-ness NULL.
	agSeedSnapshotForTest(t, 72, "", []any{"backend", "completed", "success", nil})
	// A snapshot that is not for the current head is not exposed at all.
	agSeedSnapshotForTest(t, 73, `UPDATE github_pull_request SET review_decision = 'APPROVED', base_ref_name = 'x' WHERE id = $1::uuid`)
	if _, err := testPool.Exec(context.Background(), `UPDATE github_pull_request SET head_sha = 'newer' WHERE workspace_id = $1 AND pr_number = 73`, testWorkspaceID); err != nil {
		t.Fatal(err)
	}

	byNumber := map[int32]GitHubPullRequestResponse{}
	for _, pr := range listIssuePRsForTest(t, created.ID).PullRequests {
		byNumber[pr.Number] = pr
	}

	t.Run("AG patch: a current snapshot exposes checks, rollup, review decision, reviews and base ref", func(t *testing.T) {
		pr := byNumber[71]
		if pr.RequiredChecksState == nil || *pr.RequiredChecksState != "pending" {
			t.Fatalf("required_checks_state = %v, want pending (the required e2e check is running)", pr.RequiredChecksState)
		}
		if pr.ReviewDecision == nil || *pr.ReviewDecision != "changes_requested" {
			t.Fatalf("review_decision = %v", pr.ReviewDecision)
		}
		if pr.BaseRefName == nil || *pr.BaseRefName != "latest" {
			t.Fatalf("base_ref_name = %v", pr.BaseRefName)
		}
		if len(pr.LatestReviews) != 1 || pr.LatestReviews[0] != (AGPRReview{Author: "alan", State: "approved", SubmittedAt: "2026-10-16T10:00:00Z"}) {
			t.Fatalf("latest_reviews = %+v", pr.LatestReviews)
		}
		if len(pr.Checks) != 3 {
			t.Fatalf("checks = %+v", pr.Checks)
		}
		for i, want := range []struct {
			name     string
			required bool
		}{{"backend", true}, {"lint", false}, {"e2e", true}} {
			got := pr.Checks[i]
			if got.Name != want.name || got.Required == nil || *got.Required != want.required {
				t.Errorf("check %d = %+v, want %s required=%v", i, got, want.name, want.required)
			}
		}
		// Existing fields are untouched.
		if pr.ChecksTotal != 3 || pr.ChecksFailed != 1 || pr.MergeStateStatus == nil || *pr.MergeStateStatus != "blocked" {
			t.Fatalf("existing snapshot fields changed: %+v", pr)
		}
	})

	t.Run("AG patch: checks carry their own run fields, null where the snapshot has none", func(t *testing.T) {
		checks := byNumber[71].Checks
		backend := checks[0]
		if backend.WorkflowName == nil || *backend.WorkflowName != "CI" || backend.RunID == nil || *backend.RunID != 40309530097 ||
			backend.StartedAt == nil || *backend.StartedAt != "2026-10-26T09:00:00Z" ||
			backend.SuiteStatus == nil || *backend.SuiteStatus != "completed" ||
			backend.SuiteConclusion == nil || *backend.SuiteConclusion != "success" {
			t.Fatalf("backend run fields = %+v", backend)
		}
		lint := checks[1]
		if lint.WorkflowName != nil || lint.RunID != nil || lint.StartedAt != nil || lint.SuiteStatus != nil || lint.SuiteConclusion != nil {
			t.Fatalf("lint run fields = %+v, want all null", lint)
		}
	})

	t.Run("AG patch: head_sha is the mirrored head, with or without a current snapshot", func(t *testing.T) {
		for number, want := range map[int32]string{71: "abc", 72: "abc", 73: "newer"} {
			if got := byNumber[number].HeadSHA; got == nil || *got != want {
				t.Errorf("PR %d head_sha = %v, want %s", number, got, want)
			}
		}
	})

	t.Run("AG patch: a snapshot without the new data reports null, never success", func(t *testing.T) {
		pr := byNumber[72]
		if pr.RequiredChecksState != nil || pr.ReviewDecision != nil || pr.BaseRefName != nil || pr.LatestReviews != nil {
			t.Fatalf("expected nulls, got %+v", pr.AGPRSnapshotFields)
		}
		if len(pr.Checks) != 1 || pr.Checks[0].Required != nil {
			t.Fatalf("checks = %+v, want one with required=null", pr.Checks)
		}
	})

	t.Run("AG patch: a snapshot for an older head is not exposed", func(t *testing.T) {
		pr := byNumber[73]
		if pr.ReviewDecision != nil || pr.BaseRefName != nil || pr.RequiredChecksState != nil || pr.Checks != nil {
			t.Fatalf("stale snapshot leaked: %+v", pr.AGPRSnapshotFields)
		}
	})

	t.Run("AG patch: the JSON carries the new keys on every PR, null when absent", func(t *testing.T) {
		for _, pr := range agListRawForTest(t, created.ID) {
			var checks []map[string]json.RawMessage
			_ = json.Unmarshal(pr["checks"], &checks)
			for _, check := range checks {
				for _, key := range []string{"workflow_name", "run_id", "started_at", "suite_status", "suite_conclusion"} {
					if _, ok := check[key]; !ok {
						t.Errorf("check key %q missing from %s", key, pr["number"])
					}
				}
			}
			for _, key := range []string{"checks", "required_checks_state", "review_decision", "latest_reviews", "base_ref_name", "head_sha"} {
				if _, ok := pr[key]; !ok {
					t.Errorf("key %q missing from %s", key, pr["number"])
				}
			}
		}
	})
}

func TestAGPatchSkipViewRefreshForFinishedPRs(t *testing.T) {
	fetched := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	row := func(state, head, snapshotHead string, fetchedAt pgtype.Timestamptz) db.ListPullRequestsByIssueRow {
		return db.ListPullRequestsByIssueRow{State: state, HeadSha: head, SnapshotHeadSha: snapshotHead, SnapshotFetchedAt: fetchedAt}
	}
	for _, tc := range []struct {
		name string
		row  db.ListPullRequestsByIssueRow
		want bool
	}{
		{"merged with a snapshot", row("merged", "abc", "abc", fetched), true},
		{"closed with a snapshot", row("closed", "abc", "abc", fetched), true},
		{"open with a stale snapshot", row("open", "abc", "abc", fetched), false},
		{"draft with a stale snapshot", row("draft", "abc", "abc", fetched), false},
		{"merged without a snapshot", row("merged", "abc", "", pgtype.Timestamptz{}), false},
		{"merged with a snapshot of an older head", row("merged", "new", "abc", fetched), false},
		{"closed, never fetched", row("closed", "abc", "abc", pgtype.Timestamptz{}), false},
	} {
		t.Run("AG patch: no refresh on view for "+tc.name, func(t *testing.T) {
			if got := agSkipViewRefresh(tc.row); got != tc.want {
				t.Fatalf("agSkipViewRefresh = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAGPatchPullRequestHeadSHA(t *testing.T) {
	t.Run("AG patch: head_sha is the mirrored row's head, null when it has none", func(t *testing.T) {
		var resp GitHubPullRequestResponse
		agSetPRHeadSHA(&resp, "abc123")
		if resp.HeadSHA == nil || *resp.HeadSHA != "abc123" {
			t.Fatalf("head_sha = %v", resp.HeadSHA)
		}
		var empty GitHubPullRequestResponse
		agSetPRHeadSHA(&empty, "")
		raw, _ := json.Marshal(empty.AGPRSnapshotFields)
		var keys map[string]json.RawMessage
		_ = json.Unmarshal(raw, &keys)
		if string(keys["head_sha"]) != "null" {
			t.Fatalf("head_sha JSON = %s, want null", keys["head_sha"])
		}
	})
}
