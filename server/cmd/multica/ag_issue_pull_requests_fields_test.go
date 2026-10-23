package main

// AG patch: the pull-requests listing and link/unlink output carry the
// snapshot fields (required checks, review decision, base ref) unchanged.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestAGPatchPullRequestsLinkVariants(t *testing.T) {
	t.Run("AG patch: a 400 from the server is printed as its message and exits non-zero", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/issues/MUL-9" {
				_, _ = w.Write([]byte(`{"id":"issue-uuid","identifier":"MUL-9"}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"url is required"}`))
		}))
		defer srv.Close()
		t.Setenv("MULTICA_SERVER_URL", srv.URL)
		t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
		t.Setenv("MULTICA_TOKEN", "test-token")
		_, err := agRunPR(t, "link", "MUL-9", agTestPRURL)
		if err == nil || err.Error() != "link pull request: url is required" {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("AG patch: a 404 with a plain-text body still says not mirrored and keeps the body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/issues/MUL-9" {
				_, _ = w.Write([]byte(`{"id":"issue-uuid","identifier":"MUL-9"}`))
				return
			}
			http.Error(w, "nothing here", http.StatusNotFound)
		}))
		defer srv.Close()
		t.Setenv("MULTICA_SERVER_URL", srv.URL)
		t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
		t.Setenv("MULTICA_TOKEN", "test-token")
		_, err := agRunPR(t, "link", "MUL-9", agTestPRURL)
		if err == nil || !strings.Contains(err.Error(), "has not been mirrored yet") || !strings.Contains(err.Error(), "nothing here") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("AG patch: link --output json prints the returned list with the snapshot fields", func(t *testing.T) {
		f := newAGFakePRServer(t)
		f.mirrored[agTestPRURL]["review_decision"] = "approved"
		f.mirrored[agTestPRURL]["required_checks_state"] = "success"
		out, err := agRunPR(t, "link", "MUL-9", agTestPRURL, "--output", "json")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"review_decision": "approved"`, `"required_checks_state": "success"`, `"base_ref_name": "latest"`} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %s: %s", want, out)
			}
		}
	})
}

func TestAGPatchPullRequestsUnlinkURLForms(t *testing.T) {
	for _, ref := range []string{
		"https://github.com/acme/widget/pull/41",
		"https://github.com/acme/widget/pull/41/",
		"https://GitHub.com/Acme/Widget/pull/41#issuecomment-1",
		"github.com/acme/widget/pull/41/files",
	} {
		t.Run("AG patch: unlink matches "+ref, func(t *testing.T) {
			f := newAGFakePRServer(t)
			f.linked = []map[string]any{f.mirrored[agTestPRURL]}
			if _, err := agRunPR(t, "unlink", "MUL-9", ref); err != nil {
				t.Fatalf("unlink %s: %v", ref, err)
			}
			if len(f.linked) != 0 {
				t.Fatalf("PR still linked after unlink %s", ref)
			}
		})
	}

	t.Run("AG patch: a similar PR number does not match", func(t *testing.T) {
		f := newAGFakePRServer(t)
		f.linked = []map[string]any{f.mirrored[agTestPRURL]}
		if _, err := agRunPR(t, "unlink", "MUL-9", "https://github.com/acme/widget/pull/4"); err == nil {
			t.Fatal("pull/4 must not unlink pull/41")
		}
		if len(f.linked) != 1 {
			t.Fatal("PR 41 was unlinked")
		}
	})

	t.Run("AG patch: a blank reference is rejected before any request", func(t *testing.T) {
		f := newAGFakePRServer(t)
		if _, err := agRunPR(t, "unlink", "MUL-9", "  "); err == nil || len(f.callLog()) != 0 {
			t.Fatalf("err = %v, calls = %v", err, f.callLog())
		}
	})
}
