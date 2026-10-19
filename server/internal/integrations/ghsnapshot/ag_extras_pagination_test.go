package ghsnapshot

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// agCheckRunJSON is a CheckRun node whose isRequired is "true", "false" or "null".
func agCheckRunJSON(name, required string) string {
	return fmt.Sprintf(`{"__typename":"CheckRun","name":%q,"status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"u","isRequired":%s}`, name, required)
}

func TestAGPatchFetchSnapshotPaginatesRequiredChecks(t *testing.T) {
	t.Run("AG patch: 250 checks over three pages keep required-ness and order, and carry the cursor", func(t *testing.T) {
		const total = 250
		// Every 7th check is required, every 11th is unknown (null), the rest are not required.
		requiredFor := func(i int) string {
			switch {
			case i%7 == 0:
				return "true"
			case i%11 == 0:
				return "null"
			}
			return "false"
		}
		var cursors []string
		srv, queries := agQueryServer(t, func(_ string, vars map[string]any) string {
			cursor, _ := vars["cursor"].(string)
			cursors = append(cursors, cursor)
			start, end, next := 0, 100, "P2"
			switch cursor {
			case "P2":
				start, end, next = 100, 200, "P3"
			case "P3":
				start, end, next = 200, total, ""
			}
			var nodes []string
			for i := start; i < end; i++ {
				nodes = append(nodes, agCheckRunJSON(fmt.Sprintf("check-%03d", i), requiredFor(i)))
			}
			// Later pages repeat the PR fields; only the first page's are used.
			return agSnapshotPage(agReviewExtras, next != "", next, strings.Join(nodes, ","))
		})
		snap, err := FetchPRSnapshot(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 5)
		if err != nil {
			t.Fatalf("FetchPRSnapshot: %v", err)
		}
		if got := fmt.Sprint(cursors); got != "[ P2 P3]" {
			t.Fatalf("cursors = %v, want first page, P2, P3", got)
		}
		for i, q := range queries() {
			if !strings.Contains(q, "isRequired(pullRequestNumber:$number)") {
				t.Errorf("page %d query lacks isRequired", i)
			}
		}
		if len(snap.Contexts) != total {
			t.Fatalf("contexts = %d, want %d", len(snap.Contexts), total)
		}
		var required, notRequired, unknown int
		for i, c := range snap.Contexts {
			if want := fmt.Sprintf("check-%03d", i); c.Name != want {
				t.Fatalf("context %d = %s, want %s", i, c.Name, want)
			}
			switch want := requiredFor(i); {
			case c.Required == nil && want != "null", c.Required != nil && fmt.Sprint(*c.Required) != want:
				t.Fatalf("check %d required = %v, want %s", i, c.Required, want)
			case c.Required == nil:
				unknown++
			case *c.Required:
				required++
			default:
				notRequired++
			}
		}
		if required == 0 || notRequired == 0 || unknown == 0 {
			t.Fatalf("fixture did not spread required=%d notRequired=%d unknown=%d", required, notRequired, unknown)
		}
		if !snap.AG.Available || snap.AG.BaseRefName != "latest" || snap.AG.ReviewDecision != "CHANGES_REQUESTED" {
			t.Fatalf("AG = %+v", snap.AG)
		}
	})

	t.Run("AG patch: a required check on the last page decides the rollup", func(t *testing.T) {
		srv, _ := agQueryServer(t, func(_ string, vars map[string]any) string {
			if cursor, _ := vars["cursor"].(string); cursor == "" {
				return agSnapshotPage("", true, "P2", agCheckRunJSON("first", "false"))
			}
			return agSnapshotPage("", false, "",
				`{"__typename":"CheckRun","name":"gate","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"u","isRequired":true}`)
		})
		snap, err := FetchPRSnapshot(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 5)
		if err != nil {
			t.Fatal(err)
		}
		states := make([]AGCheckState, 0, len(snap.Contexts))
		for _, c := range snap.Contexts {
			states = append(states, AGCheckState{Status: c.Status, Conclusion: c.Conclusion, Required: c.Required})
		}
		if got := AGRequiredChecksState(states); got == nil || *got != "failure" {
			t.Fatalf("rollup = %v, want failure from the page-2 required check", got)
		}
	})

	t.Run("AG patch: a response with no isRequired anywhere rolls up to null, not success", func(t *testing.T) {
		srv, _ := agQueryServer(t, func(string, map[string]any) string {
			return agSnapshotPage("", false, "",
				`{"__typename":"CheckRun","name":"a","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"u"},
				 {"__typename":"StatusContext","context":"b","state":"SUCCESS","targetUrl":"u"}`)
		})
		snap, err := FetchPRSnapshot(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 5)
		if err != nil {
			t.Fatal(err)
		}
		states := make([]AGCheckState, 0, len(snap.Contexts))
		for _, c := range snap.Contexts {
			states = append(states, AGCheckState{Status: c.Status, Conclusion: c.Conclusion, Required: c.Required})
		}
		if got := AGRequiredChecksState(states); got != nil {
			t.Fatalf("rollup = %q, want null", *got)
		}
	})
}
