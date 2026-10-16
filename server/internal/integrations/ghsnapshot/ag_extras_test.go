package ghsnapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// agQueryServer stands in for GitHub's GraphQL endpoint and records the query
// of every request. reply gets the request's query and variables.
func agQueryServer(t *testing.T, reply func(query string, vars map[string]any) string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"ghs_secret","expires_at":"` +
				time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		queries = append(queries, req.Query)
		mu.Unlock()
		_, _ = w.Write([]byte(reply(req.Query, req.Variables)))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), queries...)
	}
}

// agSnapshotPage builds one recorded GraphQL page. extras is the PR-level JSON
// (baseRefName, reviewDecision, latestReviews) spliced in before commits.
func agSnapshotPage(extras string, hasNext bool, endCursor string, nodes string) string {
	return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{
		"headRefOid":"sha1","mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED",%s
		"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"PENDING","contexts":{
			"pageInfo":{"hasNextPage":%t,"endCursor":%q},"nodes":[%s]}}}}]}}}}}`,
		extras, hasNext, endCursor, nodes)
}

const agReviewExtras = `
	"baseRefName":"latest","reviewDecision":"CHANGES_REQUESTED",
	"latestReviews":{"nodes":[
		{"author":{"login":"alan"},"state":"APPROVED","submittedAt":"2026-10-16T10:00:00Z"},
		{"author":null,"state":"CHANGES_REQUESTED","submittedAt":"2026-10-16T11:00:00Z"}
	]},`

func TestAGPatchSnapshotQueryAsksForTheExtraFields(t *testing.T) {
	t.Run("AG patch: extended query adds isRequired to both check kinds and the PR fields", func(t *testing.T) {
		q := prSnapshotQuery(true)
		if got := strings.Count(q, "isRequired(pullRequestNumber:$number)"); got != 2 {
			t.Fatalf("isRequired appears %d times, want 2 (CheckRun and StatusContext)", got)
		}
		for _, want := range []string{"reviewDecision", "latestReviews(first:20)", "baseRefName"} {
			if !strings.Contains(q, want) {
				t.Errorf("query lacks %s", want)
			}
		}
	})
	t.Run("AG patch: base query is the pre-patch query", func(t *testing.T) {
		q := prSnapshotQuery(false)
		for _, banned := range []string{"isRequired", "reviewDecision", "latestReviews", "baseRefName", "%s"} {
			if strings.Contains(q, banned) {
				t.Errorf("base query contains %s", banned)
			}
		}
	})
}

func TestAGPatchFetchSnapshotRequiredAcrossPages(t *testing.T) {
	t.Run("AG patch: required true, false and null are kept per check across pages", func(t *testing.T) {
		srv, _ := agQueryServer(t, func(_ string, vars map[string]any) string {
			if cursor, _ := vars["cursor"].(string); cursor == "" {
				return agSnapshotPage(agReviewExtras, true, "CUR2", `
					{"__typename":"CheckRun","name":"backend","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"u1","isRequired":true},
					{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"u2","isRequired":false}`)
			}
			return agSnapshotPage(agReviewExtras, false, "", `
				{"__typename":"CheckRun","name":"e2e","status":"IN_PROGRESS","conclusion":null,"detailsUrl":"u3","isRequired":null},
				{"__typename":"StatusContext","context":"vercel","state":"SUCCESS","targetUrl":"u4","isRequired":true},
				{"__typename":"StatusContext","context":"legacy","state":"PENDING","targetUrl":"u5"}`)
		})
		snap, err := FetchPRSnapshot(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 5)
		if err != nil {
			t.Fatalf("FetchPRSnapshot: %v", err)
		}
		tr, fa := true, false
		want := []struct {
			name     string
			required *bool
		}{{"backend", &tr}, {"lint", &fa}, {"e2e", nil}, {"vercel", &tr}, {"legacy", nil}}
		if len(snap.Contexts) != len(want) {
			t.Fatalf("contexts = %d, want %d", len(snap.Contexts), len(want))
		}
		for i, w := range want {
			got := snap.Contexts[i]
			if got.Name != w.name || (got.Required == nil) != (w.required == nil) ||
				(w.required != nil && *got.Required != *w.required) {
				t.Errorf("context %d = %s required=%v, want %s required=%v", i, got.Name, got.Required, w.name, w.required)
			}
		}
	})
}

func TestAGPatchFetchSnapshotReviewFields(t *testing.T) {
	t.Run("AG patch: review decision, latest reviews and base ref are parsed", func(t *testing.T) {
		srv, _ := agQueryServer(t, func(string, map[string]any) string { return agSnapshotPage(agReviewExtras, false, "", "") })
		snap, err := FetchPRSnapshot(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 5)
		if err != nil {
			t.Fatal(err)
		}
		ag := snap.AG
		if !ag.Available || ag.ReviewDecision != "CHANGES_REQUESTED" || ag.BaseRefName != "latest" {
			t.Fatalf("AG = %+v", ag)
		}
		want := []AGReview{
			{Author: "alan", State: "APPROVED", SubmittedAt: "2026-10-16T10:00:00Z"},
			{Author: "", State: "CHANGES_REQUESTED", SubmittedAt: "2026-10-16T11:00:00Z"},
		}
		if fmt.Sprint(ag.LatestReviews) != fmt.Sprint(want) {
			t.Fatalf("latest reviews = %+v, want %+v", ag.LatestReviews, want)
		}
	})

	for _, decision := range []string{"APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED", "null"} {
		t.Run("AG patch: reviewDecision "+decision, func(t *testing.T) {
			value := decision
			if decision != "null" {
				value = `"` + decision + `"`
			}
			extras := `"baseRefName":"b","reviewDecision":` + value + `,"latestReviews":{"nodes":[]},`
			srv, _ := agQueryServer(t, func(string, map[string]any) string { return agSnapshotPage(extras, false, "", "") })
			snap, err := FetchPRSnapshot(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 5)
			if err != nil {
				t.Fatal(err)
			}
			wantDecision := decision
			if decision == "null" {
				wantDecision = ""
			}
			if !snap.AG.Available || snap.AG.ReviewDecision != wantDecision || len(snap.AG.LatestReviews) != 0 {
				t.Fatalf("AG = %+v", snap.AG)
			}
		})
	}

	t.Run("AG patch: a null latestReviews and missing fields leave the extras empty, not failed", func(t *testing.T) {
		srv, _ := agQueryServer(t, func(string, map[string]any) string {
			return agSnapshotPage(`"baseRefName":null,"reviewDecision":null,"latestReviews":null,`, false, "", "")
		})
		snap, err := FetchPRSnapshot(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 5)
		if err != nil {
			t.Fatal(err)
		}
		if snap.AG.ReviewDecision != "" || snap.AG.BaseRefName != "" || snap.AG.LatestReviews != nil {
			t.Fatalf("AG = %+v", snap.AG)
		}
	})
}

func TestAGPatchFetchSnapshotFallsBackWhenGitHubRejectsTheExtraFields(t *testing.T) {
	t.Run("AG patch: a GraphQL error on the extended query retries the base query and keeps the snapshot", func(t *testing.T) {
		srv, queries := agQueryServer(t, func(query string, _ map[string]any) string {
			if strings.Contains(query, "isRequired") {
				return `{"data":null,"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`
			}
			return agSnapshotPage("", false, "", `{"__typename":"CheckRun","name":"backend","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"u1"}`)
		})
		snap, err := FetchPRSnapshot(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 5)
		if err != nil {
			t.Fatalf("snapshot failed because of the new fields: %v", err)
		}
		if got := queries(); len(got) != 2 || !strings.Contains(got[0], "isRequired") || strings.Contains(got[1], "isRequired") {
			t.Fatalf("queries = %d, want the extended query then the base query", len(got))
		}
		if snap.AG.Available || snap.HeadSHA != "sha1" || len(snap.Contexts) != 1 || snap.Contexts[0].Required != nil {
			t.Fatalf("snapshot = %+v", snap)
		}
	})

	t.Run("AG patch: a failing base query is still an error", func(t *testing.T) {
		srv, _ := agQueryServer(t, func(string, map[string]any) string {
			return `{"data":null,"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a Repository"}]}`
		})
		if _, err := FetchPRSnapshot(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 5); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("AG patch: a rate limit on the extended query is not retried with the base query", func(t *testing.T) {
		srv, queries := agQueryServer(t, func(string, map[string]any) string {
			return `{"data":null,"errors":[{"type":"RATE_LIMITED","message":"slow down"}]}`
		})
		_, err := FetchPRSnapshot(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 5)
		var rl *RateLimitError
		if !asRateLimit(err, &rl) {
			t.Fatalf("err = %v, want a RateLimitError", err)
		}
		if n := len(queries()); n != 1 {
			t.Fatalf("queries = %d, want 1", n)
		}
	})
}

func TestAGPatchRequiredChecksState(t *testing.T) {
	tr, fa := true, false
	check := func(status, conclusion string, required *bool) AGCheckState {
		return AGCheckState{Status: status, Conclusion: conclusion, Required: required}
	}
	cases := []struct {
		name   string
		checks []AGCheckState
		want   string // "" is nil
	}{
		{"no checks", nil, ""},
		{"all required passed", []AGCheckState{check("completed", "success", &tr), check("completed", "skipped", &tr), check("completed", "neutral", &tr)}, "success"},
		{"a required check failed", []AGCheckState{check("completed", "success", &tr), check("completed", "failure", &tr)}, "failure"},
		{"each failing conclusion counts", []AGCheckState{check("completed", "cancelled", &tr)}, "failure"},
		{"failure beats pending, in either order", []AGCheckState{check("in_progress", "", &tr), check("completed", "timed_out", &tr)}, "failure"},
		{"a failure then a pending stays failure", []AGCheckState{check("completed", "error", &tr), check("queued", "", &tr)}, "failure"},
		{"a running required check is pending", []AGCheckState{check("completed", "success", &tr), check("in_progress", "", &tr)}, "pending"},
		{"a completed check with no conclusion is pending", []AGCheckState{check("completed", "", &tr)}, "pending"},
		{"an unknown conclusion is pending", []AGCheckState{check("completed", "mystery", &tr)}, "pending"},
		{"failing and running optional checks are ignored", []AGCheckState{check("completed", "success", &tr), check("completed", "failure", &fa), check("in_progress", "", &fa)}, "success"},
		{"nothing required is success", []AGCheckState{check("completed", "failure", &fa)}, "success"},
		{"no required-ness data is null", []AGCheckState{check("completed", "success", nil), check("completed", "failure", nil)}, ""},
		{"one check without required-ness makes it null", []AGCheckState{check("completed", "success", &tr), check("completed", "success", nil)}, ""},
	}
	for _, tc := range cases {
		t.Run("AG patch: "+tc.name, func(t *testing.T) {
			got := AGRequiredChecksState(tc.checks)
			switch {
			case tc.want == "" && got != nil:
				t.Fatalf("got %q, want null", *got)
			case tc.want != "" && (got == nil || *got != tc.want):
				t.Fatalf("got %v, want %q", got, tc.want)
			}
		})
	}
}
