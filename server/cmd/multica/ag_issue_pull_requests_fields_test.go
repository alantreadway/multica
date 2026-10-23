package main

// AG patch: the pull-requests listing carries the snapshot fields (required
// checks, review decision, base ref) unchanged.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const agFieldsListBody = `{"pull_requests":[{
	"id":"11111111-1111-4111-8111-111111111111","number":41,"state":"open","title":"MUL-9 widget",
	"html_url":"https://github.com/acme/widget/pull/41",
	"existing_field":"kept",
	"required_checks_state":"pending",
	"review_decision":"changes_requested",
	"base_ref_name":"latest",
	"latest_reviews":[{"author":"alan","state":"approved","submitted_at":"2026-10-16T10:00:00Z"}],
	"checks":[
		{"name":"backend","status":"completed","conclusion":"success","details_url":"u","required":true},
		{"name":"lint","status":"completed","conclusion":"failure","details_url":"u","required":false},
		{"name":"e2e","status":"in_progress","conclusion":null,"details_url":null,"required":null}]
},{
	"id":"22222222-2222-4222-8222-222222222222","number":42,"state":"open","title":"no snapshot",
	"html_url":"https://github.com/acme/widget/pull/42",
	"required_checks_state":null,"review_decision":null,"base_ref_name":null,"latest_reviews":null,"checks":null
}],"auto_complete":{"state":"none"}}`

func agListServer(t *testing.T, listBody string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/issues/MUL-9":
			_, _ = w.Write([]byte(`{"id":"issue-uuid","identifier":"MUL-9"}`))
		case r.URL.Path == "/api/issues/issue-uuid/pull-requests":
			_, _ = w.Write([]byte(listBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
}

func TestAGPatchPullRequestsListingJSONCarriesSnapshotFields(t *testing.T) {
	t.Run("AG patch: --output json passes every new field and the existing ones through", func(t *testing.T) {
		agListServer(t, agFieldsListBody)
		out, err := agRunPR(t, "MUL-9", "--output", "json")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			PullRequests []map[string]any `json:"pull_requests"`
			AutoComplete map[string]any   `json:"auto_complete"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.PullRequests) != 2 {
			t.Fatalf("output = %s (%v)", out, err)
		}
		pr := got.PullRequests[0]
		if pr["required_checks_state"] != "pending" || pr["review_decision"] != "changes_requested" || pr["base_ref_name"] != "latest" || pr["existing_field"] != "kept" {
			t.Fatalf("pr = %v", pr)
		}
		reviews, _ := pr["latest_reviews"].([]any)
		if len(reviews) != 1 || reviews[0].(map[string]any)["author"] != "alan" {
			t.Fatalf("latest_reviews = %v", pr["latest_reviews"])
		}
		checks, _ := pr["checks"].([]any)
		if len(checks) != 3 {
			t.Fatalf("checks = %v", pr["checks"])
		}
		for i, want := range []any{true, false, nil} {
			if got := checks[i].(map[string]any)["required"]; got != want {
				t.Errorf("check %d required = %v, want %v", i, got, want)
			}
		}
		if got.AutoComplete["state"] != "none" {
			t.Errorf("auto_complete dropped: %v", got.AutoComplete)
		}
	})

	t.Run("AG patch: a PR with no snapshot keeps explicit nulls in the JSON", func(t *testing.T) {
		agListServer(t, agFieldsListBody)
		out, err := agRunPR(t, "MUL-9", "--output", "json")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			PullRequests []map[string]any `json:"pull_requests"`
		}
		_ = json.Unmarshal([]byte(out), &got)
		pr := got.PullRequests[1]
		for _, key := range []string{"required_checks_state", "review_decision", "base_ref_name", "latest_reviews", "checks"} {
			if v, ok := pr[key]; !ok || v != nil {
				t.Errorf("%s = %v (present %v), want null", key, v, ok)
			}
		}
	})

	t.Run("AG patch: the default table listing still renders with the new fields present", func(t *testing.T) {
		agListServer(t, agFieldsListBody)
		out, err := agRunPR(t, "MUL-9")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "NUMBER") || !strings.Contains(out, "41") || !strings.Contains(out, "42") {
			t.Fatalf("table = %q", out)
		}
	})

	t.Run("AG patch: a listing error is non-zero and names the failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/issues/MUL-9" {
				_, _ = w.Write([]byte(`{"id":"issue-uuid","identifier":"MUL-9"}`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"failed to list pull requests"}`))
		}))
		defer srv.Close()
		t.Setenv("MULTICA_SERVER_URL", srv.URL)
		t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
		t.Setenv("MULTICA_TOKEN", "test-token")
		if _, err := agRunPR(t, "MUL-9", "--output", "json"); err == nil || !strings.Contains(err.Error(), "failed to list pull requests") {
			t.Fatalf("error = %v", err)
		}
	})
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
