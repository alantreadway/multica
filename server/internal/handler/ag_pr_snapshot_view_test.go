package handler

// AG patch tests: reading a PR list must not re-fetch the snapshot of a finished
// PR, and head_sha reaches the response as the mirrored row's head. See
// ag-patches/PATCHES.md.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/ghsnapshot"
)

// TestAGPatchListPullRequestsRefreshOnView drives GET issue pull-requests with a
// live snapshot manager and counts the GitHub snapshot fetches the read causes.
// Every PR's snapshot is an hour old, past the 60s view TTL.
func TestAGPatchListPullRequestsRefreshOnView(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	secret := "ag-view-refresh"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	const installationID int64 = 30264102
	created := prAutoCompleteTestIssue(t, "view refresh", installationID)

	// Webhooks first: they enqueue on the fixture's inert manager.
	firePRWebhook(t, secret, installationID, 81, created.Identifier+": open", "", "feat/open", "opened")
	firePRWebhook(t, secret, installationID, 82, created.Identifier+": merged", "", "feat/merged", "merged")
	firePRWebhook(t, secret, installationID, 83, created.Identifier+": closed", "", "feat/closed", "closed")
	firePRWebhook(t, secret, installationID, 84, created.Identifier+": merged, no snapshot", "", "feat/nosnap", "merged")
	firePRWebhook(t, secret, installationID, 85, created.Identifier+": draft", "", "feat/draft", "opened")
	if _, err := testPool.Exec(ctx, `UPDATE github_pull_request SET state = 'draft' WHERE workspace_id = $1 AND pr_number = 85`, testWorkspaceID); err != nil {
		t.Fatal(err)
	}
	// A snapshot for the current head, fetched an hour ago, on all but PR 84.
	if _, err := testPool.Exec(ctx, `UPDATE github_pull_request
		SET head_sha = 'abc', snapshot_head_sha = 'abc', snapshot_fetched_at = now() - interval '1 hour'
		WHERE workspace_id = $1 AND pr_number IN (81, 82, 83, 85)`, testWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE github_pull_request SET head_sha = 'abc'
		WHERE workspace_id = $1 AND pr_number = 84`, testWorkspaceID); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY"} {
		t.Setenv(k, "")
	}
	t.Setenv("GITHUB_POLL_TOKEN", "ghp_view_refresh")
	client, err := ghsnapshot.NewClientFromEnv()
	if err != nil || !client.Enabled() {
		t.Fatalf("token client = %v, err %v", client, err)
	}
	rec := &recordingGitHubAPI{next: http.DefaultTransport}
	prevTransport := http.DefaultTransport
	http.DefaultTransport = rec
	prevManager := testHandler.PRRefresh
	testHandler.PRRefresh = ghsnapshot.NewManager(client, testHandler.Queries, testHandler.TxStarter, nil)
	workers, stop := context.WithCancel(ctx)
	testHandler.PRRefresh.Start(workers)
	t.Cleanup(func() {
		stop()
		testHandler.PRRefresh = prevManager
		http.DefaultTransport = prevTransport
	})

	agListRawForTest(t, created.ID)

	fetches := func() map[int32]int {
		n := map[int32]int{}
		for _, c := range rec.calls() {
			if c.Repo == "widget" && c.Extras {
				n[c.Number]++
			}
		}
		return n
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := fetches(); got[81] > 0 && got[84] > 0 && got[85] > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Workers delay each fetch by up to 250ms of jitter; let a wrongly queued
	// refresh of a finished PR arrive before asserting it did not.
	time.Sleep(750 * time.Millisecond)

	got := fetches()
	for number, want := range map[int32]int{
		81: 1, // open, stale snapshot: refreshed as before
		85: 1, // draft, stale snapshot: refreshed as before
		84: 1, // merged but no snapshot yet: still fetched once
		82: 0, // merged with a snapshot: final
		83: 0, // closed with a snapshot: final
	} {
		if got[number] != want {
			t.Errorf("PR %d got %d snapshot fetches on view, want %d (all: %v)", number, got[number], want, got)
		}
	}
}

func TestAGPatchListPullRequestsHeadSHAIsNullWithoutAHead(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	secret := "ag-head-sha-null"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	const installationID int64 = 30264103
	created := prAutoCompleteTestIssue(t, "head sha", installationID)
	firePRWebhook(t, secret, installationID, 91, created.Identifier+": no head", "", "feat/nohead", "opened")
	firePRWebhook(t, secret, installationID, 92, created.Identifier+": head", "", "feat/head", "opened")
	if _, err := testPool.Exec(context.Background(), `UPDATE github_pull_request SET head_sha = ''
		WHERE workspace_id = $1 AND pr_number = 91`, testWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE github_pull_request SET head_sha = 'f00d'
		WHERE workspace_id = $1 AND pr_number = 92`, testWorkspaceID); err != nil {
		t.Fatal(err)
	}

	t.Run("AG patch: head_sha is null on the wire when the row has no head and the head otherwise", func(t *testing.T) {
		seen := map[string]string{}
		for _, pr := range agListRawForTest(t, created.ID) {
			var number int
			_ = json.Unmarshal(pr["number"], &number)
			raw, ok := pr["head_sha"]
			if !ok {
				t.Fatalf("PR %d has no head_sha key", number)
			}
			seen[string(pr["number"])] = string(raw)
		}
		if seen["91"] != "null" || seen["92"] != `"f00d"` {
			t.Fatalf("head_sha by PR = %v, want 91 null and 92 \"f00d\"", seen)
		}
	})
}
