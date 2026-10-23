package ghsnapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// agFakeGitHub stands in for GitHub for PR o/r#5 at head sha1: the REST snapshot
// endpoints serve runs and statuses (100 per page), and every GraphQL request
// goes to gql, which gets the request's variables. It records the GraphQL
// queries and cursors.
type agFakeGitHub struct {
	runs     []map[string]any
	statuses []map[string]any
	gql      func(vars map[string]any) string

	mu      sync.Mutex
	queries []string
	cursors []string
}

func (f *agFakeGitHub) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		switch r.URL.Path {
		case "/app/installations/1/access_tokens":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"ghs_secret","expires_at":"` +
				time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
		case "/repos/o/r/pulls/5":
			writeTestPR(w, "sha1", true, "blocked")
		case "/repos/o/r/commits/sha1/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(f.runs), "check_runs": agPage(f.runs, page)})
		case "/repos/o/r/commits/sha1/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(f.statuses), "statuses": agPage(f.statuses, page)})
		case "/graphql":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			_ = json.Unmarshal(body, &req)
			cursor, _ := req.Variables["cursor"].(string)
			f.mu.Lock()
			f.queries = append(f.queries, req.Query)
			f.cursors = append(f.cursors, cursor)
			f.mu.Unlock()
			_, _ = w.Write([]byte(f.gql(req.Variables)))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *agFakeGitHub) fetch(t *testing.T) (*PRSnapshot, error) {
	t.Helper()
	return FetchPRSnapshot(context.Background(), newTestClient(t, f.serve(t).URL), 1, "o", "r", 5)
}

func (f *agFakeGitHub) recorded() (queries, cursors []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...), append([]string(nil), f.cursors...)
}

// agPage is 1-based page p of items, 100 per page; never nil, as GitHub sends [].
func agPage(items []map[string]any, p int) []map[string]any {
	start, end := (p-1)*100, p*100
	if start < 0 || start > len(items) {
		start = len(items)
	}
	if end > len(items) {
		end = len(items)
	}
	return append([]map[string]any{}, items[start:end]...)
}

func agRun(id int, name, status, conclusion string) map[string]any {
	run := map[string]any{"id": id, "head_sha": "sha1", "name": name, "status": status, "details_url": "u"}
	if conclusion != "" {
		run["conclusion"] = conclusion
	}
	return run
}

func agStatus(context, state string) map[string]any {
	return map[string]any{"context": context, "state": state, "target_url": "u"}
}

// agExtrasPage builds one extras-query page for head sha1. prFields is the
// PR-level JSON (baseRefName, reviewDecision, latestReviews) spliced in before
// commits; nodes are the check contexts.
func agExtrasPage(prFields string, hasNext bool, endCursor string, nodes string) string {
	return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{
		"headRefOid":"sha1",%s
		"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
			"pageInfo":{"hasNextPage":%t,"endCursor":%q},"nodes":[%s]}}}}]}}}}}`,
		prFields, hasNext, endCursor, nodes)
}

const agReviewExtras = `
	"baseRefName":"latest","reviewDecision":"CHANGES_REQUESTED",
	"latestReviews":{"nodes":[
		{"author":{"login":"alan"},"state":"APPROVED","submittedAt":"2026-10-16T10:00:00Z"},
		{"author":null,"state":"CHANGES_REQUESTED","submittedAt":"2026-10-16T11:00:00Z"}
	]},`

func agRequiredOf(snap *PRSnapshot) string {
	var out []string
	for _, c := range snap.Contexts {
		v := "null"
		if c.Required != nil {
			v = strconv.FormatBool(*c.Required)
		}
		out = append(out, c.Name+"="+v)
	}
	return strings.Join(out, " ")
}

// agServeExtrasForbidden answers the AG extras query in upstream's REST-only
// snapshot tests (snapshotServer) with the GraphQL error an App without the
// permission gets, so those tests check the REST snapshot with the extras
// unavailable.
func agServeExtrasForbidden(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
		return false
	}
	_, _ = w.Write([]byte(`{"data":null,"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`))
	return true
}

func TestAGPatchSnapshotQueryAsksForTheExtraFields(t *testing.T) {
	t.Run("AG patch: the extras query asks for isRequired on both check kinds and the PR fields", func(t *testing.T) {
		if got := strings.Count(agExtrasQuery, "isRequired(pullRequestNumber:$number)"); got != 2 {
			t.Fatalf("isRequired appears %d times, want 2 (CheckRun and StatusContext)", got)
		}
		for _, want := range []string{"headRefOid", "reviewDecision", "latestReviews(first:20)", "baseRefName", "after:$cursor"} {
			if !strings.Contains(agExtrasQuery, want) {
				t.Errorf("query lacks %s", want)
			}
		}
	})
}

func TestAGPatchFetchSnapshotRequiredAcrossPages(t *testing.T) {
	t.Run("AG patch: required true, false and null are matched to the REST checks across GraphQL pages", func(t *testing.T) {
		f := &agFakeGitHub{
			runs: []map[string]any{
				agRun(1, "backend", "completed", "success"),
				agRun(2, "lint", "completed", "failure"),
				agRun(3, "e2e", "in_progress", ""),
				agRun(4, "not-in-graphql", "completed", "success"),
			},
			statuses: []map[string]any{agStatus("vercel", "success"), agStatus("legacy", "pending")},
			gql: func(vars map[string]any) string {
				if cursor, _ := vars["cursor"].(string); cursor == "" {
					return agExtrasPage(agReviewExtras, true, "CUR2", `
						{"__typename":"CheckRun","name":"backend","isRequired":true},
						{"__typename":"CheckRun","name":"lint","isRequired":false}`)
				}
				return agExtrasPage(agReviewExtras, false, "", `
					{"__typename":"CheckRun","name":"e2e","isRequired":null},
					{"__typename":"StatusContext","context":"vercel","isRequired":true},
					{"__typename":"StatusContext","context":"legacy"}`)
			},
		}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatalf("FetchPRSnapshot: %v", err)
		}
		want := "backend=true lint=false e2e=null not-in-graphql=null vercel=true legacy=null"
		if got := agRequiredOf(snap); got != want {
			t.Fatalf("required = %s\nwant       %s", got, want)
		}
		if _, cursors := f.recorded(); fmt.Sprint(cursors) != "[ CUR2]" {
			t.Fatalf("cursors = %v", cursors)
		}
	})

	t.Run("AG patch: a check run and a status with the same name are matched by kind", func(t *testing.T) {
		f := &agFakeGitHub{
			runs:     []map[string]any{agRun(1, "ci", "completed", "success")},
			statuses: []map[string]any{agStatus("ci", "success")},
			gql: func(map[string]any) string {
				return agExtrasPage("", false, "", `
					{"__typename":"CheckRun","name":"ci","isRequired":false},
					{"__typename":"StatusContext","context":"ci","isRequired":true}`)
			},
		}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		if got := agRequiredOf(snap); got != "ci=false ci=true" {
			t.Fatalf("required = %s", got)
		}
	})

	t.Run("AG patch: duplicate GraphQL names merge to required, then unknown", func(t *testing.T) {
		f := &agFakeGitHub{
			runs: []map[string]any{agRun(1, "a", "completed", "success"), agRun(2, "b", "completed", "success"), agRun(3, "c", "completed", "success")},
			gql: func(map[string]any) string {
				return agExtrasPage("", false, "", `
					{"__typename":"CheckRun","name":"a","isRequired":false},
					{"__typename":"CheckRun","name":"a","isRequired":true},
					{"__typename":"CheckRun","name":"b","isRequired":false},
					{"__typename":"CheckRun","name":"b","isRequired":null},
					{"__typename":"CheckRun","name":"c","isRequired":false},
					{"__typename":"CheckRun","name":"c","isRequired":false}`)
			},
		}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		if got := agRequiredOf(snap); got != "a=true b=null c=false" {
			t.Fatalf("required = %s", got)
		}
	})
}

func TestAGPatchFetchSnapshotReviewFields(t *testing.T) {
	t.Run("AG patch: review decision, latest reviews and base ref are parsed", func(t *testing.T) {
		f := &agFakeGitHub{gql: func(map[string]any) string { return agExtrasPage(agReviewExtras, false, "", "") }}
		snap, err := f.fetch(t)
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
			prFields := `"baseRefName":"b","reviewDecision":` + value + `,"latestReviews":{"nodes":[]},`
			f := &agFakeGitHub{gql: func(map[string]any) string { return agExtrasPage(prFields, false, "", "") }}
			snap, err := f.fetch(t)
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
		f := &agFakeGitHub{gql: func(map[string]any) string {
			return agExtrasPage(`"baseRefName":null,"reviewDecision":null,"latestReviews":null,`, false, "", "")
		}}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		if !snap.AG.Available || snap.AG.ReviewDecision != "" || snap.AG.BaseRefName != "" || snap.AG.LatestReviews != nil {
			t.Fatalf("AG = %+v", snap.AG)
		}
	})

	t.Run("AG patch: a PR with no status check rollup still gets its review fields", func(t *testing.T) {
		f := &agFakeGitHub{gql: func(map[string]any) string {
			return `{"data":{"repository":{"pullRequest":{"headRefOid":"sha1","baseRefName":"main","reviewDecision":"APPROVED",
				"latestReviews":{"nodes":[]},"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}}}}`
		}}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		if !snap.AG.Available || snap.AG.ReviewDecision != "APPROVED" || snap.AG.BaseRefName != "main" || snap.HasChecks {
			t.Fatalf("snapshot = %+v", snap)
		}
	})
}

func TestAGPatchFetchSnapshotKeepsTheSnapshotWhenTheExtrasFail(t *testing.T) {
	runs := []map[string]any{agRun(1, "backend", "completed", "success")}
	for name, reply := range map[string]string{
		"a GraphQL error":   `{"data":null,"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`,
		"a malformed reply": `{"data":{"repository":null}}`,
		"not JSON":          `{`,
	} {
		t.Run("AG patch: "+name+" leaves the extras unavailable and keeps the REST snapshot", func(t *testing.T) {
			f := &agFakeGitHub{runs: runs, gql: func(map[string]any) string { return reply }}
			snap, err := f.fetch(t)
			if err != nil {
				t.Fatalf("snapshot failed because of the extras: %v", err)
			}
			if snap.AG.Available || snap.HeadSHA != "sha1" || snap.RollupState != "SUCCESS" || len(snap.Contexts) != 1 || snap.Contexts[0].Required != nil {
				t.Fatalf("snapshot = %+v", snap)
			}
			if queries, _ := f.recorded(); len(queries) != 1 {
				t.Fatalf("GraphQL requests = %d, want 1 (no retry)", len(queries))
			}
		})
	}

	t.Run("AG patch: a permission failure (HTTP 403) keeps the REST snapshot", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/app/installations/1/access_tokens":
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"token":"ghs_secret","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
			case "/repos/o/r/pulls/5":
				writeTestPR(w, "sha1", true, "clean")
			case "/repos/o/r/commits/sha1/check-runs":
				_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
			case "/repos/o/r/commits/sha1/status":
				_, _ = w.Write([]byte(`{"total_count":0,"statuses":[]}`))
			default:
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
			}
		}))
		defer srv.Close()
		snap, err := FetchPRSnapshot(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 5)
		if err != nil || snap.AG.Available || snap.MergeStateStatus != "CLEAN" {
			t.Fatalf("snapshot = %+v, err = %v", snap, err)
		}
	})

	t.Run("AG patch: a failure on a later page drops the first page's answers too", func(t *testing.T) {
		f := &agFakeGitHub{runs: runs, gql: func(vars map[string]any) string {
			if cursor, _ := vars["cursor"].(string); cursor == "" {
				return agExtrasPage(agReviewExtras, true, "P2", `{"__typename":"CheckRun","name":"backend","isRequired":true}`)
			}
			return `{"data":null,"errors":[{"type":"INTERNAL","message":"boom"}]}`
		}}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		if snap.AG.Available || snap.AG.ReviewDecision != "" || snap.Contexts[0].Required != nil {
			t.Fatalf("snapshot = %+v", snap)
		}
	})

	t.Run("AG patch: a cursor that does not advance leaves the extras unavailable", func(t *testing.T) {
		f := &agFakeGitHub{runs: runs, gql: func(map[string]any) string {
			return agExtrasPage(agReviewExtras, true, "SAME", `{"__typename":"CheckRun","name":"backend","isRequired":true}`)
		}}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		if queries, _ := f.recorded(); snap.AG.Available || len(queries) != 2 {
			t.Fatalf("available = %v after %d requests", snap.AG.Available, len(queries))
		}
	})
}

func TestAGPatchFetchSnapshotExtrasFailuresThatFailTheSnapshot(t *testing.T) {
	t.Run("AG patch: a GraphQL rate limit fails the snapshot with a RateLimitError", func(t *testing.T) {
		f := &agFakeGitHub{gql: func(map[string]any) string {
			return `{"data":null,"errors":[{"type":"RATE_LIMITED","message":"slow down"}]}`
		}}
		_, err := f.fetch(t)
		var rl *RateLimitError
		if !asRateLimit(err, &rl) {
			t.Fatalf("err = %v, want a RateLimitError", err)
		}
	})

	t.Run("AG patch: a head that moved before the extras query fails the snapshot", func(t *testing.T) {
		f := &agFakeGitHub{gql: func(map[string]any) string {
			return strings.Replace(agExtrasPage("", false, "", ""), `"headRefOid":"sha1"`, `"headRefOid":"sha2"`, 1)
		}}
		if _, err := f.fetch(t); err == nil || !strings.Contains(err.Error(), "head changed") {
			t.Fatalf("err = %v, want a head-changed error", err)
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

// agRunOf renders a check run's AGRun for comparison: "workflow#run@start
// suite/conclusion", or "null" when the extras said nothing about it.
func agRunOf(c CheckContext) string {
	if c.AGRun == nil {
		return "null"
	}
	r := c.AGRun
	started := ""
	if !r.StartedAt.IsZero() {
		started = r.StartedAt.Format(time.RFC3339)
	}
	return fmt.Sprintf("%s#%d@%s %s/%s", r.WorkflowName, r.RunID, started, r.SuiteStatus, r.SuiteConclusion)
}

func agRunNode(databaseID int, name, startedAt, suite string) string {
	return fmt.Sprintf(`{"__typename":"CheckRun","databaseId":%d,"name":%q,"startedAt":%q,"isRequired":true,"checkSuite":%s}`,
		databaseID, name, startedAt, suite)
}

func TestAGPatchSnapshotQueryAsksForTheRunFields(t *testing.T) {
	t.Run("AG patch: the extras query asks for the check run's own id, start and suite", func(t *testing.T) {
		for _, want := range []string{"databaseId name startedAt", "checkSuite{status conclusion workflowRun{databaseId workflow{name}}}"} {
			if !strings.Contains(agExtrasQuery, want) {
				t.Errorf("query lacks %s", want)
			}
		}
	})
}

func TestAGPatchFetchSnapshotRunFieldsPerRow(t *testing.T) {
	suiteA := `{"status":"COMPLETED","conclusion":"CANCELLED","workflowRun":{"databaseId":40309530097,"workflow":{"name":"CI"}}}`
	suiteB := `{"status":"IN_PROGRESS","conclusion":null,"workflowRun":{"databaseId":40313137478,"workflow":{"name":"CI"}}}`

	t.Run("AG patch: two same-named checks from different runs each get their own run fields", func(t *testing.T) {
		f := &agFakeGitHub{
			runs: []map[string]any{
				agRun(101, "report", "completed", "cancelled"),
				agRun(102, "report", "in_progress", ""),
			},
			gql: func(map[string]any) string {
				// GraphQL lists them in the opposite order to REST.
				return agExtrasPage("", false, "", agRunNode(102, "report", "2026-10-26T11:00:00Z", suiteB)+","+
					agRunNode(101, "report", "2026-10-26T10:00:00+01:00", suiteA))
			},
		}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		if got := agRunOf(snap.Contexts[0]); got != "CI#40309530097@2026-10-26T09:00:00Z completed/cancelled" {
			t.Errorf("first report = %s", got)
		}
		if got := agRunOf(snap.Contexts[1]); got != "CI#40313137478@2026-10-26T11:00:00Z in_progress/" {
			t.Errorf("second report = %s", got)
		}
		// Required still merges by name, as before.
		if got := agRequiredOf(snap); got != "report=true report=true" {
			t.Errorf("required = %s", got)
		}
	})

	t.Run("AG patch: status contexts, rows GitHub did not return and mismatched names get null", func(t *testing.T) {
		f := &agFakeGitHub{
			runs: []map[string]any{
				agRun(1, "build", "completed", "success"),
				agRun(2, "missing", "completed", "success"),
				agRun(3, "renamed", "completed", "success"),
				agRun(4, "dup", "completed", "success"),
			},
			statuses: []map[string]any{agStatus("vercel", "success")},
			gql: func(map[string]any) string {
				return agExtrasPage("", false, "", strings.Join([]string{
					agRunNode(1, "build", "2026-10-26T10:00:00Z", suiteA),
					agRunNode(3, "other-name", "2026-10-26T10:00:00Z", suiteA),
					agRunNode(4, "dup", "2026-10-26T10:00:00Z", suiteA),
					agRunNode(4, "dup", "2026-10-26T10:00:00Z", suiteB),
					`{"__typename":"StatusContext","context":"vercel","isRequired":true}`,
				}, ","))
			},
		}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range snap.Contexts {
			got = append(got, c.Name+"="+agRunOf(c))
		}
		want := "build=CI#40309530097@2026-10-26T10:00:00Z completed/cancelled missing=null renamed=null dup=null vercel=null"
		if strings.Join(got, " ") != want {
			t.Fatalf("run fields =\n%s\nwant\n%s", strings.Join(got, " "), want)
		}
	})

	t.Run("AG patch: a check run outside Actions keeps the suite and leaves the workflow null", func(t *testing.T) {
		f := &agFakeGitHub{
			runs: []map[string]any{agRun(1, "external", "completed", "success")},
			gql: func(map[string]any) string {
				return agExtrasPage("", false, "", agRunNode(1, "external", "not a time",
					`{"status":"COMPLETED","conclusion":"SUCCESS","workflowRun":null}`))
			},
		}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		if got := agRunOf(snap.Contexts[0]); got != "#0@ completed/success" {
			t.Fatalf("run fields = %s", got)
		}
	})

	t.Run("AG patch: run fields are matched across GraphQL pages", func(t *testing.T) {
		f := &agFakeGitHub{
			runs: []map[string]any{agRun(1, "a", "completed", "success"), agRun(2, "b", "completed", "success")},
			gql: func(vars map[string]any) string {
				if cursor, _ := vars["cursor"].(string); cursor == "" {
					return agExtrasPage("", true, "P2", agRunNode(1, "a", "2026-10-26T10:00:00Z", suiteA))
				}
				return agExtrasPage("", false, "", agRunNode(2, "b", "2026-10-26T10:05:00Z", suiteB))
			},
		}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		if agRunOf(snap.Contexts[0]) == "null" || agRunOf(snap.Contexts[1]) == "null" {
			t.Fatalf("run fields = %s / %s", agRunOf(snap.Contexts[0]), agRunOf(snap.Contexts[1]))
		}
	})

	t.Run("AG patch: when the extras fail every row's run fields are null", func(t *testing.T) {
		for name, gql := range map[string]func(map[string]any) string{
			"a GraphQL error": func(map[string]any) string {
				return `{"data":null,"errors":[{"type":"FORBIDDEN","message":"nope"}]}`
			},
			"a later page failing": func(vars map[string]any) string {
				if cursor, _ := vars["cursor"].(string); cursor == "" {
					return agExtrasPage("", true, "P2", agRunNode(1, "a", "2026-10-26T10:00:00Z", suiteA))
				}
				return `{"data":null,"errors":[{"type":"INTERNAL","message":"boom"}]}`
			},
		} {
			f := &agFakeGitHub{runs: []map[string]any{agRun(1, "a", "completed", "success")}, gql: gql}
			snap, err := f.fetch(t)
			if err != nil {
				t.Fatalf("%s failed the snapshot: %v", name, err)
			}
			if got := agRunOf(snap.Contexts[0]); got != "null" {
				t.Errorf("%s: run fields = %s, want null", name, got)
			}
		}
	})
}
