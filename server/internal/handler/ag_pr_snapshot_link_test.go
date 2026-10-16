package handler

// These need the test database and are not run by ag-patches/test.sh; see its header.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The link and unlink endpoints answer with the issue's pull-request list, so
// `multica issue pull-requests link|unlink` print the same snapshot fields as
// the plain listing.
func TestAGPatchLinkUnlinkResponsesCarrySnapshotFields(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	secret := "ag-snapshot-link"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	const installationID int64 = 30264102
	agEnableSnapshotsForTest(t)
	created := prAutoCompleteTestIssue(t, "snapshot link", installationID)

	// The title names no issue, so the webhook mirrors the PR without linking it.
	firePRWebhook(t, secret, installationID, 81, "Unrelated refactor", "", "refactor/x", "opened")
	agSeedSnapshotForTest(t, 81, `UPDATE github_pull_request
		SET review_decision = 'APPROVED', base_ref_name = 'ag/v0.6.2',
		    latest_reviews = '[{"author":"alan","state":"APPROVED","submitted_at":"2026-10-16T10:00:00Z"}]'::jsonb
		WHERE id = $1::uuid`,
		[]any{"backend", "completed", "success", true},
		[]any{"lint", "completed", "failure", false},
	)
	if got := listIssuePRsForTest(t, created.ID).PullRequests; len(got) != 0 {
		t.Fatalf("PR linked before the test linked it: %+v", got)
	}

	link := func(body map[string]any) *testutil.Response {
		req := withURLParam(newRequest("POST", "/api/issues/"+created.ID+"/pull-requests", body), "id", created.ID)
		return testutil.Call(t, testHandler.LinkIssuePullRequest, req)
	}
	decode := func(rec *testutil.Response) []GitHubPullRequestResponse {
		t.Helper()
		var out issuePullRequestsBodyForTest
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.PullRequests
	}

	t.Run("AG patch: a PR that is not mirrored is a 404 with the server's message", func(t *testing.T) {
		rec := link(map[string]any{"url": "https://github.com/acme/widget/pull/9999"})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
			t.Fatalf("body = %s, want {\"error\": ...}", rec.Body.String())
		}
	})

	t.Run("AG patch: a malformed URL is rejected and links nothing", func(t *testing.T) {
		if rec := link(map[string]any{"url": "not-a-url"}); rec.Code < 400 || rec.Code >= 500 {
			t.Fatalf("status = %d, want a 4xx (%s)", rec.Code, rec.Body.String())
		}
		if got := listIssuePRsForTest(t, created.ID).PullRequests; len(got) != 0 {
			t.Fatalf("malformed link attached %+v", got)
		}
	})

	t.Run("AG patch: the link response carries the snapshot fields of the linked PR", func(t *testing.T) {
		rec := link(map[string]any{"url": "https://github.com/acme/widget/pull/81"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
		}
		prs := decode(rec)
		if len(prs) != 1 {
			t.Fatalf("pull_requests = %+v", prs)
		}
		pr := prs[0]
		if pr.RequiredChecksState == nil || *pr.RequiredChecksState != "success" {
			t.Errorf("required_checks_state = %v, want success (the one required check passed; the failing one is optional)", pr.RequiredChecksState)
		}
		if pr.ReviewDecision == nil || *pr.ReviewDecision != "approved" || pr.BaseRefName == nil || *pr.BaseRefName != "ag/v0.6.2" {
			t.Errorf("review_decision = %v, base_ref_name = %v", pr.ReviewDecision, pr.BaseRefName)
		}
		if len(pr.LatestReviews) != 1 || pr.LatestReviews[0].Author != "alan" || len(pr.Checks) != 2 {
			t.Errorf("latest_reviews = %+v, checks = %+v", pr.LatestReviews, pr.Checks)
		}
	})

	t.Run("AG patch: the plain listing shows the same linked PR and fields", func(t *testing.T) {
		prs := listIssuePRsForTest(t, created.ID).PullRequests
		if len(prs) != 1 || prs[0].Number != 81 || prs[0].BaseRefName == nil || *prs[0].BaseRefName != "ag/v0.6.2" {
			t.Fatalf("pull_requests = %+v", prs)
		}
	})

	t.Run("AG patch: linking by pull_request_id returns the fields too", func(t *testing.T) {
		prID := listIssuePRsForTest(t, created.ID).PullRequests[0].ID
		prs := decode(link(map[string]any{"pull_request_id": prID}))
		if len(prs) != 1 || prs[0].ReviewDecision == nil {
			t.Fatalf("pull_requests = %+v", prs)
		}
	})

	t.Run("AG patch: unlink removes the PR and the response is the remaining list", func(t *testing.T) {
		prID := listIssuePRsForTest(t, created.ID).PullRequests[0].ID
		unlinkPRForTest(t, created.ID, prID)
		if got := listIssuePRsForTest(t, created.ID).PullRequests; len(got) != 0 {
			t.Fatalf("still linked: %+v", got)
		}
		// The fields come back when the PR is linked again.
		prs := decode(link(map[string]any{"url": "https://github.com/acme/widget/pull/81"}))
		if len(prs) != 1 || prs[0].RequiredChecksState == nil || *prs[0].RequiredChecksState != "success" {
			t.Fatalf("relinked pull_requests = %+v", prs)
		}
	})
}

// A required check that has not finished, a failure beside a running one, and
// a StatusContext-style check all roll up through the real list handler.
func TestAGPatchListRollupThroughHandler(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	secret := "ag-snapshot-rollup"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	const installationID int64 = 30264103
	agEnableSnapshotsForTest(t)
	created := prAutoCompleteTestIssue(t, "snapshot rollup", installationID)

	cases := []struct {
		number int32
		want   string
		checks [][]any
	}{
		{91, "success", [][]any{{"a", "completed", "success", true}, {"b", "completed", "skipped", true}, {"c", "in_progress", nil, false}}},
		{92, "failure", [][]any{{"a", "in_progress", nil, true}, {"b", "completed", "cancelled", true}}},
		{93, "pending", [][]any{{"a", "completed", "success", true}, {"b", "queued", nil, true}}},
		{94, "success", [][]any{{"a", "completed", "failure", false}}},
	}
	for _, tc := range cases {
		firePRWebhook(t, secret, installationID, tc.number, created.Identifier+": rollup", "", "feat/rollup", "opened")
		agSeedSnapshotForTest(t, tc.number, "", tc.checks...)
	}
	byNumber := map[int32]GitHubPullRequestResponse{}
	for _, pr := range listIssuePRsForTest(t, created.ID).PullRequests {
		byNumber[pr.Number] = pr
	}
	for _, tc := range cases {
		pr, ok := byNumber[tc.number]
		if !ok {
			t.Fatalf("PR %d not listed", tc.number)
		}
		if pr.RequiredChecksState == nil || *pr.RequiredChecksState != tc.want {
			t.Errorf("PR %d required_checks_state = %v, want %s", tc.number, pr.RequiredChecksState, tc.want)
		}
	}
}
