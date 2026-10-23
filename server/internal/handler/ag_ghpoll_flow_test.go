package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/ghpoll"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// setWorkspaceReposForTest points a workspace's `repos` at the given URLs and
// restores the previous value when the test ends.
func setWorkspaceReposForTest(t *testing.T, workspaceID string, urls ...string) {
	t.Helper()
	ctx := context.Background()
	var previous string
	if err := testPool.QueryRow(ctx, `SELECT repos::text FROM workspace WHERE id = $1`, workspaceID).Scan(&previous); err != nil {
		t.Fatalf("read repos: %v", err)
	}
	entries := make([]map[string]string, 0, len(urls))
	for _, u := range urls {
		entries = append(entries, map[string]string{"url": u, "description": ""})
	}
	raw, _ := json.Marshal(entries)
	if _, err := testPool.Exec(ctx, `UPDATE workspace SET repos = $1::jsonb WHERE id = $2`, string(raw), workspaceID); err != nil {
		t.Fatalf("set repos: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE workspace SET repos = $1::jsonb WHERE id = $2`, previous, workspaceID)
	})
}

// fakeGitHubPulls serves a repo's PR list from a swappable body and records the
// Authorization header of every request. Unknown paths answer an empty list so
// other workspaces' repos in the shared test database poll harmlessly.
type fakeGitHubPulls struct {
	mu    sync.Mutex
	path  string
	body  string
	auths []string
	hits  int
}

func (g *fakeGitHubPulls) set(body string) { g.mu.Lock(); g.body = body; g.mu.Unlock() }

func (g *fakeGitHubPulls) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.URL.Path != g.path {
		_, _ = w.Write([]byte("[]"))
		return
	}
	g.hits++
	g.auths = append(g.auths, r.Header.Get("Authorization"))
	_, _ = w.Write([]byte(g.body))
}

func pullListItem(repo string, number int32, title, state, mergedAt, updatedAt string) string {
	raw := polledPR(repo, number, title, state, mergedAt, updatedAt).Raw
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["base"] = map[string]any{"repo": map[string]any{"name": repo, "owner": map[string]any{"login": "acme"}}}
	out, _ := json.Marshal(m)
	return string(out)
}

func storedPollRow(t *testing.T, workspaceID, repo string, number int32) (state, title string, installationID int64, found bool) {
	t.Helper()
	err := testPool.QueryRow(context.Background(),
		`SELECT state, title, installation_id FROM github_pull_request WHERE workspace_id = $1 AND repo_owner = 'acme' AND repo_name = $2 AND pr_number = $3`,
		workspaceID, repo, number).Scan(&state, &title, &installationID)
	return state, title, installationID, err == nil
}

// TestAGPatchPollerEndToEndOpenUpdateMerge runs the real poller against an
// httptest GitHub, the real PGSource and the real sink: no GitHub App, no
// installation row. Each PR transition must land within one tick.
func TestAGPatchPollerEndToEndOpenUpdateMerge(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	const repo = "e2e-polled"
	const token = "ghp_endtoend_token"
	created := prAutoCompleteTestIssue(t, "end to end polled PR", 0)
	setWorkspaceReposForTest(t, testWorkspaceID, "https://github.com/acme/"+repo+".git")
	gh := &fakeGitHubPulls{path: "/repos/acme/" + repo + "/pulls"}
	srv := httptest.NewServer(gh)
	defer srv.Close()
	poller := ghpoll.New(ghpoll.Config{Token: token, Interval: time.Minute, APIBase: srv.URL}, ghpoll.PGSource{DB: testHandler.DB}, agPollSink{h: testHandler})
	title := "Fix " + created.Identifier
	ctx := context.Background()

	t.Run("AG patch: an opened PR is mirrored and linked to the issue within one poll", func(t *testing.T) {
		gh.set("[" + pullListItem(repo, 5, title, "open", "", "2026-10-16T10:00:00Z") + "]")
		poller.Tick(ctx)
		state, _, inst, ok := storedPollRow(t, testWorkspaceID, repo, 5)
		if !ok || state != "open" || inst != 0 {
			t.Fatalf("row = state %q installation %d found %v", state, inst, ok)
		}
		list := listIssuePRsForTest(t, created.ID)
		if len(list.PullRequests) != 1 || list.PullRequests[0].State != "open" || list.PullRequests[0].LinkSource == "manual" {
			t.Fatalf("pull_requests = %+v", list.PullRequests)
		}
		if got := issueStatusForTest(t, created.ID); got != "in_progress" {
			t.Fatalf("issue status = %q", got)
		}
	})

	t.Run("AG patch: an updated PR refreshes the stored row", func(t *testing.T) {
		gh.set("[" + pullListItem(repo, 5, title+" (reworded)", "open", "", "2026-10-16T10:30:00Z") + "]")
		poller.Tick(ctx)
		if _, got, _, _ := storedPollRow(t, testWorkspaceID, repo, 5); got != title+" (reworded)" {
			t.Fatalf("title = %q", got)
		}
	})

	t.Run("AG patch: a merged PR completes the linked issue within one poll", func(t *testing.T) {
		gh.set("[" + pullListItem(repo, 5, title, "closed", "2026-10-16T11:00:00Z", "2026-10-16T11:00:00Z") + "]")
		poller.Tick(ctx)
		if state, _, _, _ := storedPollRow(t, testWorkspaceID, repo, 5); state != "merged" {
			t.Fatalf("row state = %q, want merged", state)
		}
		if got := issueStatusForTest(t, created.ID); got != "done" {
			t.Fatalf("issue status = %q, want done", got)
		}
		list := listIssuePRsForTest(t, created.ID)
		if len(list.PullRequests) != 1 || list.PullRequests[0].State != "merged" {
			t.Fatalf("pull_requests = %+v", list.PullRequests)
		}
	})

	t.Run("AG patch: a closed-unmerged PR is not treated as merged", func(t *testing.T) {
		gh.set("[" + pullListItem(repo, 6, "Abandoned "+created.Identifier, "closed", "", "2026-10-16T12:00:00Z") + "]")
		if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'in_progress' WHERE id = $1`, created.ID); err != nil {
			t.Fatal(err)
		}
		poller.Tick(ctx)
		if state, _, _, ok := storedPollRow(t, testWorkspaceID, repo, 6); !ok || state != "closed" {
			t.Fatalf("row state = %q found %v, want closed", state, ok)
		}
		if got := issueStatusForTest(t, created.ID); got != "in_progress" {
			t.Fatalf("a closed-unmerged PR moved the issue to %q", got)
		}
	})

	gh.mu.Lock()
	defer gh.mu.Unlock()
	if gh.hits < 4 {
		t.Fatalf("GitHub was polled %d times", gh.hits)
	}
	for _, a := range gh.auths {
		if a != "Bearer "+token {
			t.Fatalf("Authorization = %q, want the poll token", a)
		}
	}
}

// ensureOtherWorkspace creates a second workspace with the given issue prefix
// and one issue at number n; everything is removed with the test.
func ensureOtherWorkspace(t *testing.T, slug, prefix string, n int32) (db.Workspace, db.Issue) {
	t.Helper()
	ctx := context.Background()
	testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)
	ws, err := testHandler.Queries.CreateWorkspace(ctx, db.CreateWorkspaceParams{Name: slug, Slug: slug, IssuePrefix: prefix})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	issue, err := testHandler.Queries.CreateIssue(ctx, db.CreateIssueParams{
		WorkspaceID: ws.ID, Title: "other workspace issue", Status: "in_progress", Priority: "none",
		CreatorType: "member", CreatorID: parseUUID(testUserID), Number: n,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		testPool.Exec(bg, `DELETE FROM issue_pull_request WHERE issue_id = $1`, issue.ID)
		testPool.Exec(bg, `DELETE FROM issue_pr_automation WHERE issue_id = $1`, issue.ID)
		testPool.Exec(bg, `DELETE FROM activity_log WHERE issue_id = $1`, issue.ID)
		testPool.Exec(bg, `DELETE FROM github_pull_request WHERE workspace_id = $1`, ws.ID)
		testPool.Exec(bg, `DELETE FROM issue WHERE id = $1`, issue.ID)
		testPool.Exec(bg, `DELETE FROM workspace WHERE id = $1`, ws.ID)
	})
	return ws, issue
}

// TestAGPatchPollerLinkPolicyAcrossWorkspaces: a polled PR is mirrored into
// every workspace that lists the repo and the cross-workspace link policy a
// webhook applies governs the links.
func TestAGPatchPollerLinkPolicyAcrossWorkspaces(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	sink := agPollSink{h: testHandler}

	t.Run("AG patch: an identifier both workspaces answer to links nowhere and completes nothing", func(t *testing.T) {
		const repo = "poll-ambiguous"
		setWorkspaceIssuePrefixForTest(t, "PAM")
		created := prAutoCompleteTestIssue(t, "poll ambiguous", 0)
		wsA, issueA := ensureOtherWorkspace(t, "poll-ambiguous-ws-a", "PAM", created.Number)
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE repo_name = $1`, repo)
		})

		ws := []pgtype.UUID{parseUUID(testWorkspaceID), wsA.ID}
		if err := sink.Mirror(ctx, ws, polledPR(repo, 1, "Fix "+created.Identifier, "closed", "2026-10-16T10:00:00Z", "2026-10-16T10:00:00Z")); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{testWorkspaceID, uuidToString(wsA.ID)} {
			if _, _, _, ok := storedPollRow(t, id, repo, 1); !ok {
				t.Fatalf("PR not mirrored into workspace %s", id)
			}
		}
		for name, issueID := range map[string]string{"test workspace": created.ID, "workspace A": uuidToString(issueA.ID)} {
			if n := linkedPRCountForTest(t, issueID); n != 0 {
				t.Fatalf("%s: ambiguous identifier linked %d PRs", name, n)
			}
			if got := issueStatusForTest(t, issueID); got == "done" {
				t.Fatalf("%s: issue completed on an ambiguous merge", name)
			}
		}
	})

	t.Run("AG patch: with distinct prefixes only the owning workspace links, and both mirror the PR", func(t *testing.T) {
		const repo = "poll-owner"
		setWorkspaceIssuePrefixForTest(t, "POW")
		created := prAutoCompleteTestIssue(t, "poll owner", 0)
		wsA, issueA := ensureOtherWorkspace(t, "poll-owner-ws-a", "POX", created.Number)
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE repo_name = $1`, repo)
		})

		ws := []pgtype.UUID{parseUUID(testWorkspaceID), wsA.ID}
		if err := sink.Mirror(ctx, ws, polledPR(repo, 2, "Fix "+created.Identifier, "closed", "2026-10-16T10:00:00Z", "2026-10-16T10:00:00Z")); err != nil {
			t.Fatal(err)
		}
		if n := linkedPRCountForTest(t, created.ID); n != 1 {
			t.Fatalf("owning workspace linked %d PRs, want 1", n)
		}
		if got := issueStatusForTest(t, created.ID); got != "done" {
			t.Fatalf("owning issue status = %q, want done", got)
		}
		if n := linkedPRCountForTest(t, uuidToString(issueA.ID)); n != 0 {
			t.Fatalf("other workspace linked %d PRs, want 0", n)
		}
		if got := issueStatusForTest(t, uuidToString(issueA.ID)); got != "in_progress" {
			t.Fatalf("other workspace issue status = %q", got)
		}
		if _, _, _, ok := storedPollRow(t, uuidToString(wsA.ID), repo, 2); !ok {
			t.Fatal("the other workspace did not mirror the PR")
		}
	})
}

func TestAGPatchPollerWatchedReposFromWorkspaces(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	wsA, _ := ensureOtherWorkspace(t, "poll-watch-ws-a", "PWA", 1)
	setWorkspaceReposForTest(t, testWorkspaceID,
		"https://github.com/Acme/Watch-One", "https://gitlab.com/acme/not-github", "not a url", "git@github.com:acme/watch-two.git")
	setWorkspaceReposForTest(t, uuidToString(wsA.ID), "https://github.com/acme/watch-one.git")

	watches, err := ghpoll.PGSource{DB: testHandler.DB}.Watched(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, w := range watches {
		if strings.HasPrefix(strings.ToLower(w.Repo.Name), "watch-") {
			got[strings.ToLower(w.Repo.Owner+"/"+w.Repo.Name)] = len(w.Workspaces)
		}
	}
	want := map[string]int{"acme/watch-one": 2, "acme/watch-two": 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("watched = %v, want %v", got, want)
	}
	for _, w := range watches {
		if w.Repo.Name == "not-github" {
			t.Fatalf("a gitlab entry was watched: %+v", w)
		}
	}
}

// startLogRecorder captures slog messages for the duration of a test.
type startLogRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (r *startLogRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *startLogRecorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *startLogRecorder) WithGroup(string) slog.Handler            { return r }
func (r *startLogRecorder) Handle(_ context.Context, rec slog.Record) error {
	line := rec.Message
	rec.Attrs(func(a slog.Attr) bool { line += fmt.Sprintf(" %s=%v", a.Key, a.Value.Any()); return true })
	r.mu.Lock()
	r.msgs = append(r.msgs, line)
	r.mu.Unlock()
	return nil
}
func (r *startLogRecorder) started(mode string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.msgs {
		if strings.Contains(m, "PR polling enabled") && strings.Contains(m, "mode="+mode) {
			return true
		}
	}
	return false
}

// TestAGPatchPollerStartHook pins the one-line hook main.go calls: the poller
// starts only in token mode or the explicit App opt-in.
func TestAGPatchPollerStartHook(t *testing.T) {
	app := map[string]string{"GITHUB_APP_ID": "1", "GITHUB_APP_PRIVATE_KEY": "k"}
	with := func(kv ...string) map[string]string {
		out := map[string]string{}
		for k, v := range app {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return out
	}
	cases := []struct {
		name string
		env  map[string]string
		mode string // "" means nothing starts
	}{
		{"neither App nor token", map[string]string{}, ""},
		{"App alone", with(), ""},
		{"App and token without ENABLED", with("GITHUB_POLL_TOKEN", "t"), ""},
		{"App and ENABLED without token", with("GITHUB_POLL_ENABLED", "true"), ""},
		{"whitespace token and no App", map[string]string{"GITHUB_POLL_TOKEN": "   "}, ""},
		{"token only", map[string]string{"GITHUB_POLL_TOKEN": "t"}, "token"},
		{"App with token and ENABLED=true", with("GITHUB_POLL_TOKEN", "t", "GITHUB_POLL_ENABLED", "true"), "app+poll"},
	}
	for _, c := range cases {
		t.Run("AG patch: "+c.name, func(t *testing.T) {
			for _, k := range []string{"GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY", "GITHUB_POLL_TOKEN", "GITHUB_POLL_ENABLED", "GITHUB_POLL_INTERVAL"} {
				t.Setenv(k, c.env[k])
			}
			rec := &startLogRecorder{}
			prev := slog.Default()
			slog.SetDefault(slog.New(rec))
			t.Cleanup(func() { slog.SetDefault(prev) })

			// A zero Handler has no database: a started loop fails its first
			// tick at once (recovered by the loop) and never reaches GitHub.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			(&Handler{}).AGStartGitHubPoll(ctx)

			for _, mode := range []string{"token", "app+poll"} {
				if got, want := rec.started(mode), mode == c.mode; got != want {
					t.Fatalf("started(%s) = %v, want %v", mode, got, want)
				}
			}
		})
	}
}

// TestAGPatchPollerLeavesWebhookRowsAlone: with the App configured and polling
// opted in, a PR an App webhook already stored keeps its installation id, diff
// stats and refresh routing across a poll that is newer than the webhook.
func TestAGPatchPollerLeavesWebhookRowsAlone(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	const repo = "webhook-owned"
	created, installationID := setupPRTestIssue(t, ctx, "ag-poll-owned-secret")
	setWorkspaceReposForTest(t, testWorkspaceID, "https://github.com/acme/"+repo+".git")
	firePullRequestWebhookWithHead(t, "ag-poll-owned-secret", created.Identifier, installationID, repo, 7, "opened", "abc123", "clean")
	if _, err := testPool.Exec(ctx,
		`UPDATE github_pull_request SET additions = 12, deletions = 3, changed_files = 4 WHERE workspace_id = $1 AND repo_name = $2 AND pr_number = 7`,
		testWorkspaceID, repo); err != nil {
		t.Fatalf("seed diff stats: %v", err)
	}

	gh := &fakeGitHubPulls{path: "/repos/acme/" + repo + "/pulls"}
	srv := httptest.NewServer(gh)
	defer srv.Close()
	// A poll one tick after the webhook: newer updated_at, so the upsert would take it.
	gh.set("[" + pullListItem(repo, 7, "Fix "+created.Identifier+" (reworded)", "open", "", "2026-10-16T10:00:00Z") + "]")
	poller := ghpoll.New(ghpoll.Config{Token: "ghp_owned", Interval: time.Minute, APIBase: srv.URL}, ghpoll.PGSource{DB: testHandler.DB}, agPollSink{h: testHandler, appOptIn: true})
	poller.Tick(ctx)

	var inst int64
	var adds, dels, files int32
	var title string
	if err := testPool.QueryRow(ctx,
		`SELECT installation_id, additions, deletions, changed_files, title FROM github_pull_request WHERE workspace_id = $1 AND repo_name = $2 AND pr_number = 7`,
		testWorkspaceID, repo).Scan(&inst, &adds, &dels, &files, &title); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if inst != installationID || adds != 12 || dels != 3 || files != 4 {
		t.Fatalf("row = installation %d (want %d), stats %d/%d/%d (want 12/3/4)", inst, installationID, adds, dels, files)
	}
	if strings.Contains(title, "reworded") {
		t.Fatalf("poll rewrote a webhook-owned row: title %q", title)
	}
	rows, err := testHandler.Queries.ListGitHubPRRowsByAddress(ctx, db.ListGitHubPRRowsByAddressParams{
		InstallationID: installationID, RepoOwner: "acme", RepoName: repo, PrNumber: 7,
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("check_suite refresh routing: rows for installation %d = %d, err %v; want 1", installationID, len(rows), err)
	}
}

// TestAGPatchTokenModePollTakesOverInstallationRows: a deployment that ran the
// App and then dropped it keeps rows under the old installation id, and no
// webhook will update them again. In token mode the poll rewrites such a row to
// installation 0, so its refreshes and merge handling work.
func TestAGPatchTokenModePollTakesOverInstallationRows(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	const repo = "token-takeover"
	created, installationID := setupPRTestIssue(t, ctx, "ag-poll-takeover-secret")
	if installationID == 0 {
		t.Fatal("fixture installation id is 0; the takeover case needs a real one")
	}
	setWorkspaceReposForTest(t, testWorkspaceID, "https://github.com/acme/"+repo+".git")
	firePullRequestWebhookWithHead(t, "ag-poll-takeover-secret", created.Identifier, installationID, repo, 8, "opened", "abc123", "clean")

	gh := &fakeGitHubPulls{path: "/repos/acme/" + repo + "/pulls"}
	srv := httptest.NewServer(gh)
	defer srv.Close()
	gh.set("[" + pullListItem(repo, 8, "Fix "+created.Identifier+" (reworded)", "closed", "2026-10-16T10:00:00Z", "2026-10-16T10:00:00Z") + "]")
	poller := ghpoll.New(ghpoll.Config{Token: "ghp_takeover", Interval: time.Minute, APIBase: srv.URL}, ghpoll.PGSource{DB: testHandler.DB}, agPollSink{h: testHandler})
	poller.Tick(ctx)

	state, title, inst, ok := storedPollRow(t, testWorkspaceID, repo, 8)
	if !ok || inst != 0 || state != "merged" || !strings.Contains(title, "reworded") {
		t.Fatalf("row = found %v, installation %d, state %q, title %q; want the poll's merged update under installation 0", ok, inst, state, title)
	}
	rows, err := testHandler.Queries.ListGitHubPRRowsByAddress(ctx, db.ListGitHubPRRowsByAddressParams{
		InstallationID: 0, RepoOwner: "acme", RepoName: repo, PrNumber: 8,
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("refresh routing: installation-0 rows = %d, err %v; want 1", len(rows), err)
	}
}
