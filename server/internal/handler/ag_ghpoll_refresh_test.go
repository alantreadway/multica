package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/ghpoll"
	"github.com/multica-ai/multica/server/internal/integrations/ghsnapshot"
)

// snapshotCall is one GraphQL snapshot request the refresh worker made.
type snapshotCall struct {
	Repo   string
	Number int32
	Auth   string
}

// recordingGitHubAPI stands in for api.github.com on http.DefaultTransport,
// which the snapshot client uses, and records every snapshot query. Requests to
// other hosts (the httptest GitHub the poller lists PRs from) pass through.
type recordingGitHubAPI struct {
	next http.RoundTripper
	mu   sync.Mutex
	got  []snapshotCall
}

func (r *recordingGitHubAPI) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "api.github.com" {
		return r.next.RoundTrip(req)
	}
	body, _ := io.ReadAll(req.Body)
	var q struct {
		Variables struct {
			Repo   string `json:"repo"`
			Number int32  `json:"number"`
		} `json:"variables"`
	}
	_ = json.Unmarshal(body, &q)
	r.mu.Lock()
	r.got = append(r.got, snapshotCall{q.Variables.Repo, q.Variables.Number, req.Header.Get("Authorization")})
	r.mu.Unlock()
	// A decided snapshot (mergeable, no checks) so the worker neither chases nor retries.
	const reply = `{"data":{"repository":{"pullRequest":{"headRefOid":"abc123","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","commits":{"nodes":[]}}}}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(reply))),
		Request:    req,
	}, nil
}

func (r *recordingGitHubAPI) calls() []snapshotCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]snapshotCall(nil), r.got...)
}

// TestAGPatchPollerEnqueuesSnapshotRefreshes runs the poller with a live,
// token-enabled snapshot Manager and watches the GitHub API calls its workers
// make: a polled installation-0 row gets exactly one snapshot refresh as the
// poll token, and (with the App opted in) a PR whose only row a webhook owns
// gets none.
func TestAGPatchPollerEnqueuesSnapshotRefreshes(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	const repo = "refresh-routing"
	const token = "ghp_refresh_routing"

	// A webhook-owned PR 31 and, for the same repo, a new PR 32 only the poller knows.
	created, installationID := setupPRTestIssue(t, ctx, "ag-poll-refresh-secret")
	setWorkspaceReposForTest(t, testWorkspaceID, "https://github.com/acme/"+repo+".git")
	firePullRequestWebhookWithHead(t, "ag-poll-refresh-secret", created.Identifier, installationID, repo, 31, "opened", "abc123", "clean")

	// The webhook above enqueued on the fixture's inert manager; swap in the live one now.
	for _, k := range []string{"GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY"} {
		t.Setenv(k, "")
	}
	t.Setenv("GITHUB_POLL_TOKEN", token)
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

	gh := &fakeGitHubPulls{path: "/repos/acme/" + repo + "/pulls"}
	srv := httptest.NewServer(gh)
	defer srv.Close()
	// The owned PR is listed first and newer than its webhook, so the poller sees both.
	gh.set("[" + pullListItem(repo, 31, "Fix "+created.Identifier, "open", "", "2026-10-16T10:00:00Z") + "," +
		pullListItem(repo, 32, "Unrelated change", "open", "", "2026-10-16T09:59:00Z") + "]")
	poller := ghpoll.New(ghpoll.Config{Token: token, Interval: time.Minute, APIBase: srv.URL}, ghpoll.PGSource{DB: testHandler.DB}, agPollSink{h: testHandler, appOptIn: true})
	poller.Tick(ctx)

	forRepo := func() (n31, n32 int) {
		for _, c := range rec.calls() {
			if c.Repo != repo {
				continue
			}
			if c.Auth != "Bearer "+token {
				t.Fatalf("snapshot call for PR %d authenticated as %q, want the poll token", c.Number, c.Auth)
			}
			switch c.Number {
			case 31:
				n31++
			case 32:
				n32++
			}
		}
		return n31, n32
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, n32 := forRepo(); n32 > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Workers delay each fetch by up to 250ms of jitter; let a wrongly queued
	// PR 31 refresh arrive before asserting it did not.
	time.Sleep(750 * time.Millisecond)

	n31, n32 := forRepo()
	if n32 != 1 {
		t.Fatalf("polled installation-0 PR 32 got %d snapshot refreshes, want exactly 1", n32)
	}
	if n31 != 0 {
		t.Fatalf("webhook-owned PR 31 got %d snapshot refreshes from the poller, want 0", n31)
	}
	if _, _, inst, ok := storedPollRow(t, testWorkspaceID, repo, 32); !ok || inst != 0 {
		t.Fatalf("PR 32 row: installation %d found %v, want installation 0", inst, ok)
	}
}
