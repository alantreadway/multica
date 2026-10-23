package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/ghpoll"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// polledPR builds what ghpoll hands the sink for one list item.
func polledPR(repo string, number int32, title, state, mergedAt, updatedAt string) ghpoll.PR {
	item := map[string]any{
		"number":     number,
		"html_url":   fmt.Sprintf("https://github.com/acme/%s/pull/%d", repo, number),
		"title":      title,
		"state":      state,
		"draft":      false,
		"created_at": "2026-10-16T09:00:00Z",
		"updated_at": updatedAt,
		"head":       map[string]any{"ref": "feature/x", "sha": "abc123"},
		"user":       map[string]any{"login": "octocat"},
	}
	if mergedAt != "" {
		item["merged_at"] = mergedAt
		item["closed_at"] = mergedAt
	}
	raw, _ := json.Marshal(item)
	ts, _ := time.Parse(time.RFC3339, updatedAt)
	return ghpoll.PR{Repo: ghpoll.RepoRef{Owner: "acme", Name: repo}, Number: number, UpdatedAt: ts, Raw: raw}
}

func TestAGPatchPolledPayload(t *testing.T) {
	t.Run("AG patch: a merged_at marks the PR merged and the repo comes from the poller", func(t *testing.T) {
		p, err := agPolledPayload(polledPR("widget", 7, "Fix it", "closed", "2026-10-16T10:00:00Z", "2026-10-16T10:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		if !p.PullRequest.Merged || p.PullRequest.Number != 7 || p.PullRequest.Head.Ref != "feature/x" ||
			p.Repository.Name != "widget" || p.Repository.Owner.Login != "acme" || p.Action != "" {
			t.Fatalf("payload = %+v", p)
		}
		open, _ := agPolledPayload(polledPR("widget", 8, "Fix it", "open", "", "2026-10-16T10:00:00Z"))
		if open.PullRequest.Merged {
			t.Fatal("an open PR is not merged")
		}
	})
	t.Run("AG patch: a PR object that is not JSON is an error", func(t *testing.T) {
		if _, err := agPolledPayload(ghpoll.PR{Raw: []byte("nope")}); err == nil {
			t.Fatal("want an error")
		}
	})
}

// TestAGPatchPollerMirrorsWithoutAnInstallation covers the first AC at the
// handler layer: no github_installation row, installation_id 0, and the PR still
// mirrors, links, shows on the issue and completes it on merge.
func TestAGPatchPollerMirrorsWithoutAnInstallation(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	created := prAutoCompleteTestIssue(t, "polled PR", 0)
	sink := agPollSink{h: testHandler}
	ws := []pgtype.UUID{parseUUID(testWorkspaceID)}
	title := "Fix " + created.Identifier

	if err := sink.Mirror(ctx, ws, polledPR("polled", 11, title, "open", "", "2026-10-16T10:00:00Z")); err != nil {
		t.Fatalf("open: %v", err)
	}
	list := listIssuePRsForTest(t, created.ID)
	if len(list.PullRequests) != 1 || list.PullRequests[0].State != "open" {
		t.Fatalf("pull_requests = %+v, want the open PR", list.PullRequests)
	}
	var installationID int64
	if err := testPool.QueryRow(ctx, `SELECT installation_id FROM github_pull_request WHERE workspace_id = $1 AND pr_number = 11`, testWorkspaceID).Scan(&installationID); err != nil || installationID != 0 {
		t.Fatalf("installation_id = %d, %v; want 0", installationID, err)
	}
	if got := issueStatusForTest(t, created.ID); got != "in_progress" {
		t.Fatalf("status after open = %q", got)
	}

	if err := sink.Mirror(ctx, ws, polledPR("polled", 11, title, "closed", "2026-10-16T11:00:00Z", "2026-10-16T11:00:00Z")); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := issueStatusForTest(t, created.ID); got != "done" {
		t.Fatalf("status after merge = %q, want done", got)
	}
	if got := prAutoCompleteStateForTest(t, created.ID); got.State == prAutoCompleteNotMerged {
		t.Fatalf("auto_complete = %+v, want merged", got)
	}

	// Polling the same merged PR again must not re-complete a reopened issue.
	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'in_progress' WHERE id = $1`, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := sink.Mirror(ctx, ws, polledPR("polled", 11, title, "closed", "2026-10-16T11:00:00Z", "2026-10-16T11:00:00Z")); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if got := issueStatusForTest(t, created.ID); got != "in_progress" {
		t.Fatalf("a repeated poll of a merged PR moved the issue to %q", got)
	}
}

func TestAGPatchPollerManualLinkWithoutAnInstallation(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	created := prAutoCompleteTestIssue(t, "manual link of a polled PR", 0)
	sink := agPollSink{h: testHandler}
	if err := sink.Mirror(context.Background(), []pgtype.UUID{parseUUID(testWorkspaceID)}, polledPR("manual", 21, "Refactor helper", "open", "", "2026-10-16T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(newRequest("POST", "/api/issues/"+created.ID+"/pull-requests", map[string]any{"url": "https://github.com/acme/manual/pull/21"}), "id", created.ID)
	testutil.Call(t, testHandler.LinkIssuePullRequest, req).Want(http.StatusOK)
	list := listIssuePRsForTest(t, created.ID)
	if len(list.PullRequests) != 1 || list.PullRequests[0].LinkSource != "manual" {
		t.Fatalf("pull_requests = %+v, want one manual link", list.PullRequests)
	}
}

func TestAGPatchPollerMirrorFailureIsReported(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE repo_name = 'gone'`) })
	sink := agPollSink{h: testHandler}

	t.Run("AG patch: a workspace that cannot store the PR makes Mirror fail, and the others still mirror", func(t *testing.T) {
		missing := parseUUID("00000000-0000-4000-8000-0000000000aa")
		err := sink.Mirror(ctx, []pgtype.UUID{missing, parseUUID(testWorkspaceID)}, polledPR("gone", 31, "x", "open", "", "2026-10-16T10:00:00Z"))
		if err == nil || !strings.Contains(err.Error(), "not stored") {
			t.Fatalf("err = %v, want a not-stored error", err)
		}
		var n int
		testPool.QueryRow(ctx, `SELECT count(*) FROM github_pull_request WHERE workspace_id = $1 AND repo_name = 'gone'`, testWorkspaceID).Scan(&n)
		if n != 1 {
			t.Fatalf("the healthy workspace stored %d rows, want 1", n)
		}
	})

	t.Run("AG patch: a PR older than the stored row is a success, not a failure", func(t *testing.T) {
		ws := []pgtype.UUID{parseUUID(testWorkspaceID)}
		if err := sink.Mirror(ctx, ws, polledPR("gone", 32, "x", "open", "", "2026-10-16T12:00:00Z")); err != nil {
			t.Fatal(err)
		}
		if err := sink.Mirror(ctx, ws, polledPR("gone", 32, "x", "open", "", "2026-10-16T10:00:00Z")); err != nil {
			t.Fatalf("stale poll: %v", err)
		}
	})
}
