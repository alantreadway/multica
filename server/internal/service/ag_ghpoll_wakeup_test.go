package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/ghsnapshot"
	"github.com/multica-ai/multica/server/internal/util"
)

// redirectGitHubAPI sends every request made through http.DefaultTransport to
// srv, which is how a client built by NewClientFromEnv (hard-wired to
// api.github.com) reaches an httptest GitHub.
type redirectGitHubAPI struct {
	target *url.URL
	next   http.RoundTripper
}

func (r redirectGitHubAPI) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	return r.next.RoundTrip(req)
}

// TestAGPatchTokenPolledPRFiresChecksAndMergedWakeups drives the pipeline that
// token-mode polling relies on for `--until-pr checks` and `--until-pr merged`,
// with no GitHub App: a PR row the poller wrote (installation 0), a token-only
// snapshot client, a real refresh Manager worker, and the real wakeup rules.
func TestAGPatchTokenPolledPRFiresChecksAndMergedWakeups(t *testing.T) {
	f, s, issue, agent := conditionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const token = "ghp_wakeup_flow_token"
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	t.Setenv("GITHUB_POLL_TOKEN", token)
	client, err := ghsnapshot.NewClientFromEnv()
	if err != nil || client == nil {
		t.Fatalf("token-only client = %v, %v", client, err)
	}

	var mu sync.Mutex
	var auths, paths []string
	checks := `"state":"PENDING"`
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auths = append(auths, r.Header.Get("Authorization"))
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"headRefOid":"polledhead","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN",` +
			`"commits":{"nodes":[{"commit":{"statusCheckRollup":{` + checks + `,"contexts":{"pageInfo":{"hasNextPage":false,"endCursor":""},` +
			`"nodes":[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://example.test/ci"}]}}}}]}}}}}`))
	}))
	t.Cleanup(github.Close)
	target, _ := url.Parse(github.URL)
	original := http.DefaultTransport
	http.DefaultTransport = redirectGitHubAPI{target: target, next: original}
	t.Cleanup(func() { http.DefaultTransport = original })

	applied := make(chan pgtype.UUID, 4)
	manager := ghsnapshot.NewManager(client, f.q, f.Pool, func(_ context.Context, id pgtype.UUID) { applied <- id })
	if !manager.Enabled() {
		t.Fatal("a token-only client must enable the snapshot pipeline")
	}
	// Mirrors handler.New: checks_finished rules need the pipeline to be enabled.
	s.Tasks.PRSnapshotsEnabled = manager.Enabled()
	manager.Start(ctx)
	var checksRun string

	// The row a poll writes: installation 0, open, head from the list payload,
	// no snapshot yet.
	now := time.Now()
	ts := pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}
	number := int32(now.UnixNano() % 1_000_000)
	var pr string
	if err := f.Pool.QueryRow(ctx, `INSERT INTO github_pull_request(workspace_id,installation_id,repo_owner,repo_name,pr_number,title,state,html_url,head_sha,pr_created_at,pr_updated_at)
		VALUES($1,0,'acme','polled-wakeups',$2,'Polled PR','open','https://example.test/pr','polledhead',$3,$3) RETURNING id`,
		f.WorkspaceID, number, ts.Time).Scan(&pr); err != nil {
		t.Fatal(err)
	}
	f.Cleanup(t, "DELETE FROM github_pull_request_check_run WHERE pr_id=$1", pr)
	f.Cleanup(t, "DELETE FROM github_pull_request WHERE id=$1", pr)
	f.Cleanup(t, "DELETE FROM issue_pull_request WHERE pull_request_id=$1", pr)
	f.Exec(t, "INSERT INTO issue_pull_request(issue_id,pull_request_id) VALUES($1,$2)", issue, pr)

	checksRule := wakeCreate(t, f, s, issue, WakeupInput{AgentID: agent, Kind: "event", Instruction: "Look at CI",
		Condition: condition(t, map[string]any{"type": "pull_request", "event": "checks_finished"})})
	mergedRule := wakeCreate(t, f, s, issue, WakeupInput{AgentID: agent, Kind: "event", Instruction: "Wrap up",
		Condition: condition(t, map[string]any{"type": "pull_request", "event": "merged"})})

	t.Run("AG patch: neither wakeup fires before the snapshot lands or the PR merges", func(t *testing.T) {
		wakeTick(t, f, s, checksRule.ID)
		wakeTick(t, f, s, mergedRule.ID)
		if n := wakeRuns(t, f, checksRule.ID) + wakeRuns(t, f, mergedRule.ID); n != 0 {
			t.Fatalf("unmet conditions started %d runs", n)
		}
	})

	t.Run("AG patch: the token-only Manager applies a snapshot to the polled row", func(t *testing.T) {
		manager.Enqueue(0, "acme", "polled-wakeups", number)
		select {
		case id := <-applied:
			if got := util.UUIDToString(id); got != pr {
				t.Fatalf("snapshot applied to %s, want the polled row %s", got, pr)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("the refresh worker never applied a snapshot for installation 0")
		}
		var head, rollup string
		if err := f.Pool.QueryRow(ctx, `SELECT COALESCE(snapshot_head_sha,''),COALESCE(checks_rollup_state,'') FROM github_pull_request WHERE id=$1`, pr).Scan(&head, &rollup); err != nil {
			t.Fatal(err)
		}
		if head != "polledhead" || rollup != "PENDING" {
			t.Fatalf("snapshot_head_sha=%q checks_rollup_state=%q", head, rollup)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(auths) == 0 || paths[0] != "/graphql" {
			t.Fatalf("GitHub calls = %v", paths)
		}
		for i, a := range auths {
			if a != "Bearer "+token || strings.Contains(paths[i], "/app/") {
				t.Fatalf("call %d: %s with Authorization %q", i, paths[i], a)
			}
		}
	})

	t.Run("AG patch: checks_finished fires once the polled PR's checks decide", func(t *testing.T) {
		wakeTick(t, f, s, checksRule.ID)
		if n := wakeRuns(t, f, checksRule.ID); n != 0 {
			t.Fatalf("pending checks started %d runs", n)
		}
		mu.Lock()
		checks = `"state":"SUCCESS"`
		mu.Unlock()
		manager.Enqueue(0, "acme", "polled-wakeups", number)
		select {
		case <-applied:
		case <-time.After(15 * time.Second):
			t.Fatal("the refresh worker never applied the decided snapshot")
		}
		got := wakeTick(t, f, s, checksRule.ID)
		if n := wakeRuns(t, f, checksRule.ID); n != 1 {
			t.Fatalf("decided checks started %d runs, want 1", n)
		}
		checksRun = util.UUIDToString(got.LastTaskID)
		task, err := f.q.GetAgentTask(ctx, got.LastTaskID)
		if err != nil {
			t.Fatal(err)
		}
		if note := task.HandoffNote.String; !strings.Contains(note, `"checks":"success"`) || !strings.Contains(note, `"head_sha":"polledhead"`) {
			t.Fatalf("PR facts missing: %s", note)
		}
	})

	t.Run("AG patch: merged fires once a poll records the merge", func(t *testing.T) {
		wakeTick(t, f, s, mergedRule.ID)
		if n := wakeRuns(t, f, mergedRule.ID); n != 0 {
			t.Fatalf("an open PR started %d merged runs", n)
		}
		// What the poller's mirror writes for a merged PR.
		f.Exec(t, "UPDATE github_pull_request SET state='merged', merged_at=now(), pr_updated_at=now() WHERE id=$1", pr)
		wakeTick(t, f, s, mergedRule.ID)
		// Both rules wake the same agent on the same issue, so the merged rule
		// joins the checks run that is already waiting instead of queuing a second.
		if notes := wakeClaim(t, f, s, checksRun); !strings.Contains(notes, "Wrap up") || !strings.Contains(notes, `"state":"merged"`) {
			t.Fatalf("the merged rule did not reach the run: %q", notes)
		}
	})
}
