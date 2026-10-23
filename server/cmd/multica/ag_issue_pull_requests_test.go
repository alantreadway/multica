package main

// AG patch: `issue pull-requests link|unlink` CLI tests.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

const (
	agTestPRID  = "11111111-1111-4111-8111-111111111111"
	agTestPRURL = "https://github.com/acme/widget/pull/41"
	// The fake server answers DELETE of this id with 403.
	agForbiddenPRID = "22222222-2222-4222-8222-222222222222"
)

// agFakePRServer serves one issue (MUL-9 / issue-uuid) with a mutable list of
// linked PRs, and records "METHOD path" for every request. mirrored is the set
// of PR URLs the link endpoint finds.
type agFakePRServer struct {
	*httptest.Server
	mu       sync.Mutex
	calls    []string
	bodies   []string
	linked   []map[string]any
	mirrored map[string]map[string]any
}

func newAGFakePRServer(t *testing.T) *agFakePRServer {
	t.Helper()
	f := &agFakePRServer{mirrored: map[string]map[string]any{
		agTestPRURL: {"id": agTestPRID, "number": 41, "state": "open", "title": "MUL-9 widget", "html_url": agTestPRURL, "base_ref_name": "latest"},
	}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.bodies = append(f.bodies, string(body))
		list := func() { _ = json.NewEncoder(w).Encode(map[string]any{"pull_requests": f.linked}) }
		fail := func(code int, msg string) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
		}
		switch {
		case r.URL.Path == "/api/issues/MUL-9":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "issue-uuid", "identifier": "MUL-9"})
		case r.URL.Path == "/api/issues/issue-uuid/pull-requests" && r.Method == http.MethodGet:
			list()
		case r.URL.Path == "/api/issues/issue-uuid/pull-requests" && r.Method == http.MethodPost:
			var req struct {
				URL string `json:"url"`
			}
			_ = json.Unmarshal(body, &req)
			if !strings.HasPrefix(req.URL, "https://") {
				fail(http.StatusNotFound, "pull request not found; it appears here once its repository is connected and the PR has been synced")
				return
			}
			pr, ok := f.mirrored[req.URL]
			if !ok {
				fail(http.StatusNotFound, "pull request not found; it appears here once its repository is connected and the PR has been synced")
				return
			}
			f.linked = append(f.linked, pr)
			list()
		case strings.HasPrefix(r.URL.Path, "/api/issues/issue-uuid/pull-requests/") && r.Method == http.MethodDelete:
			id := strings.TrimPrefix(r.URL.Path, "/api/issues/issue-uuid/pull-requests/")
			if id == agForbiddenPRID {
				fail(http.StatusForbidden, "not allowed")
				return
			}
			kept := f.linked[:0:0]
			for _, pr := range f.linked {
				if pr["id"] != id {
					kept = append(kept, pr)
				}
			}
			f.linked = kept
			list()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	t.Setenv("MULTICA_SERVER_URL", f.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	return f
}

func (f *agFakePRServer) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// agRunPR runs `multica issue pull-requests <args...>` through the real command
// tree and returns what it printed on stdout.
func agRunPR(t *testing.T, args ...string) (string, error) {
	t.Helper()
	target, rest, err := rootCmd.Find(append([]string{"issue", "pull-requests"}, args...))
	if err != nil {
		t.Fatalf("find command: %v", err)
	}
	// Flags are parsed per run; reset the ones these tests set.
	t.Cleanup(func() { _ = target.Flags().Set("output", "table") })
	if err := target.ParseFlags(rest); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if err := target.ValidateArgs(target.Flags().Args()); err != nil {
		t.Fatalf("validate args: %v", err)
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string, 1)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	err = target.RunE(target, target.Flags().Args())
	_ = w.Close()
	os.Stdout = old
	return <-done, err
}

func agPRRoute(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd, _, err := rootCmd.Find(append([]string{"issue", "pull-requests"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestAGPatchPullRequestsRouting(t *testing.T) {
	t.Run("AG patch: pull-requests <id> still lists, with no subcommand", func(t *testing.T) {
		if got := agPRRoute(t, "MUL-9"); got != issuePullRequestsCmd {
			t.Fatalf("routed to %q, want the listing command", got.CommandPath())
		}
	})
	t.Run("AG patch: link and unlink are subcommands", func(t *testing.T) {
		if got := agPRRoute(t, "link", "MUL-9", agTestPRURL); got != issuePullRequestsLinkCmd {
			t.Fatalf("link routed to %q", got.CommandPath())
		}
		if got := agPRRoute(t, "unlink", "MUL-9", agTestPRURL); got != issuePullRequestsUnlinkCmd {
			t.Fatalf("unlink routed to %q", got.CommandPath())
		}
	})
	t.Run("AG patch: the prs alias reaches the subcommands", func(t *testing.T) {
		cmd, _, err := rootCmd.Find([]string{"issue", "prs", "link", "MUL-9", agTestPRURL})
		if err != nil || cmd != issuePullRequestsLinkCmd {
			t.Fatalf("prs link routed to %v (%v)", cmd, err)
		}
	})
}

func TestAGPatchPullRequestsLink(t *testing.T) {
	t.Run("AG patch: link POSTs the URL and the PR then appears in the listing", func(t *testing.T) {
		f := newAGFakePRServer(t)
		out, err := agRunPR(t, "link", "MUL-9", agTestPRURL, "--output", "json")
		if err != nil {
			t.Fatalf("link: %v", err)
		}
		if got, want := f.callLog(), []string{"GET /api/issues/MUL-9", "POST /api/issues/issue-uuid/pull-requests"}; strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("calls = %v, want %v", got, want)
		}
		if f.bodies[1] != `{"url":"`+agTestPRURL+`"}` {
			t.Fatalf("POST body = %s", f.bodies[1])
		}
		var linked struct {
			PullRequests []map[string]any `json:"pull_requests"`
		}
		if err := json.Unmarshal([]byte(out), &linked); err != nil || len(linked.PullRequests) != 1 || linked.PullRequests[0]["id"] != agTestPRID {
			t.Fatalf("link output = %s (%v)", out, err)
		}

		// The same PR is now in the plain listing.
		listOut, err := agRunPR(t, "MUL-9", "--output", "json")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if !strings.Contains(listOut, agTestPRID) {
			t.Fatalf("listing lacks the linked PR: %s", listOut)
		}
	})

	t.Run("AG patch: link prints the table by default", func(t *testing.T) {
		newAGFakePRServer(t)
		out, err := agRunPR(t, "link", "MUL-9", agTestPRURL)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "NUMBER") || !strings.Contains(out, agTestPRURL) {
			t.Fatalf("table output = %q", out)
		}
	})

	t.Run("AG patch: a PR that is not mirrored fails with a clear message", func(t *testing.T) {
		newAGFakePRServer(t)
		_, err := agRunPR(t, "link", "MUL-9", "https://github.com/acme/widget/pull/999")
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, want := range []string{"has not been mirrored yet", "pull/999", "pull request not found"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q lacks %q", err, want)
			}
		}
	})

	t.Run("AG patch: a malformed URL fails with the server's message", func(t *testing.T) {
		newAGFakePRServer(t)
		_, err := agRunPR(t, "link", "MUL-9", "not-a-url")
		if err == nil || !strings.Contains(err.Error(), "pull request not found") {
			t.Fatalf("error = %v, want the server's message", err)
		}
	})

	t.Run("AG patch: a blank URL is rejected before any request", func(t *testing.T) {
		f := newAGFakePRServer(t)
		if _, err := agRunPR(t, "link", "MUL-9", "  "); err == nil {
			t.Fatal("expected an error")
		}
		if len(f.callLog()) != 0 {
			t.Fatalf("calls = %v, want none", f.callLog())
		}
	})
}

func TestAGPatchPullRequestsUnlink(t *testing.T) {
	linkFirst := func(t *testing.T) *agFakePRServer {
		f := newAGFakePRServer(t)
		f.linked = []map[string]any{f.mirrored[agTestPRURL]}
		return f
	}

	t.Run("AG patch: unlink by URL resolves the id through the listing, then DELETEs", func(t *testing.T) {
		f := linkFirst(t)
		// Case, a sub-page and a query do not matter, as on the server.
		out, err := agRunPR(t, "unlink", "MUL-9", "https://GitHub.com/acme/widget/pull/41/files?w=1", "--output", "json")
		if err != nil {
			t.Fatalf("unlink: %v", err)
		}
		want := []string{"GET /api/issues/MUL-9", "GET /api/issues/issue-uuid/pull-requests", "DELETE /api/issues/issue-uuid/pull-requests/" + agTestPRID}
		if strings.Join(f.callLog(), "|") != strings.Join(want, "|") {
			t.Fatalf("calls = %v, want %v", f.callLog(), want)
		}
		if strings.Contains(out, agTestPRID) {
			t.Fatalf("unlinked PR still listed: %s", out)
		}
	})

	t.Run("AG patch: unlink by id skips the listing", func(t *testing.T) {
		f := linkFirst(t)
		if _, err := agRunPR(t, "unlink", "MUL-9", agTestPRID); err != nil {
			t.Fatalf("unlink: %v", err)
		}
		want := []string{"GET /api/issues/MUL-9", "DELETE /api/issues/issue-uuid/pull-requests/" + agTestPRID}
		if strings.Join(f.callLog(), "|") != strings.Join(want, "|") {
			t.Fatalf("calls = %v, want %v", f.callLog(), want)
		}
	})

	t.Run("AG patch: unlink of a PR that is not linked fails and deletes nothing", func(t *testing.T) {
		f := linkFirst(t)
		_, err := agRunPR(t, "unlink", "MUL-9", "https://github.com/acme/widget/pull/7")
		if err == nil || !strings.Contains(err.Error(), "no pull request matching") {
			t.Fatalf("error = %v", err)
		}
		for _, call := range f.callLog() {
			if strings.HasPrefix(call, "DELETE") {
				t.Fatalf("unexpected %s", call)
			}
		}
	})

	t.Run("AG patch: a server error on unlink is printed as the server's message", func(t *testing.T) {
		newAGFakePRServer(t)
		_, err := agRunPR(t, "unlink", "MUL-9", agForbiddenPRID)
		if err == nil || err.Error() != "unlink pull request: not allowed" {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestAGPatchPullRequestsServerMessage(t *testing.T) {
	t.Run("AG patch: the error field of a JSON body is the message", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/issues/MUL-9":
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "issue-uuid", "identifier": "MUL-9"})
			default:
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"agents cannot link pull requests"}`))
			}
		}))
		defer srv.Close()
		t.Setenv("MULTICA_SERVER_URL", srv.URL)
		t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
		t.Setenv("MULTICA_TOKEN", "test-token")

		_, err := agRunPR(t, "link", "MUL-9", agTestPRURL)
		if err == nil || err.Error() != "link pull request: agents cannot link pull requests" {
			t.Fatalf("error = %v", err)
		}
	})
}
