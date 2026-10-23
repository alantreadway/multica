package ghsnapshot

import (
	"fmt"
	"strings"
	"testing"
)

// agCheckRunJSON is an extras-query CheckRun node whose isRequired is "true",
// "false" or "null".
func agCheckRunJSON(name, required string) string {
	return fmt.Sprintf(`{"__typename":"CheckRun","name":%q,"isRequired":%s}`, name, required)
}

func agStatesOf(snap *PRSnapshot) []AGCheckState {
	states := make([]AGCheckState, 0, len(snap.Contexts))
	for _, c := range snap.Contexts {
		states = append(states, AGCheckState{Status: c.Status, Conclusion: c.Conclusion, Required: c.Required})
	}
	return states
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
		f := &agFakeGitHub{}
		for i := 0; i < total; i++ {
			f.runs = append(f.runs, agRun(i+1, fmt.Sprintf("check-%03d", i), "completed", "success"))
		}
		f.gql = func(vars map[string]any) string {
			cursor, _ := vars["cursor"].(string)
			start, end, next := 0, 100, "P2"
			switch cursor {
			case "P2":
				start, end, next = 100, 200, "P3"
			case "P3":
				start, end, next = 200, total, ""
			}
			var nodes []string
			// GraphQL lists the contexts in another order than REST; matching is by name.
			for i := end - 1; i >= start; i-- {
				nodes = append(nodes, agCheckRunJSON(fmt.Sprintf("check-%03d", i), requiredFor(i)))
			}
			// Later pages repeat the PR fields; only the first page's are used.
			return agExtrasPage(agReviewExtras, next != "", next, strings.Join(nodes, ","))
		}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatalf("FetchPRSnapshot: %v", err)
		}
		queries, cursors := f.recorded()
		if got := fmt.Sprint(cursors); got != "[ P2 P3]" {
			t.Fatalf("cursors = %v, want first page, P2, P3", got)
		}
		for i, q := range queries {
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
		f := &agFakeGitHub{
			runs: []map[string]any{agRun(1, "first", "completed", "success"), agRun(2, "gate", "completed", "failure")},
			gql: func(vars map[string]any) string {
				if cursor, _ := vars["cursor"].(string); cursor == "" {
					return agExtrasPage("", true, "P2", agCheckRunJSON("first", "false"))
				}
				return agExtrasPage("", false, "", agCheckRunJSON("gate", "true"))
			},
		}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		if got := AGRequiredChecksState(agStatesOf(snap)); got == nil || *got != "failure" {
			t.Fatalf("rollup = %v, want failure from the page-2 required check", got)
		}
	})

	t.Run("AG patch: a response with no isRequired anywhere rolls up to null, not success", func(t *testing.T) {
		f := &agFakeGitHub{
			runs:     []map[string]any{agRun(1, "a", "completed", "success")},
			statuses: []map[string]any{agStatus("b", "success")},
			gql: func(map[string]any) string {
				return agExtrasPage("", false, "",
					`{"__typename":"CheckRun","name":"a"},{"__typename":"StatusContext","context":"b"}`)
			},
		}
		snap, err := f.fetch(t)
		if err != nil {
			t.Fatal(err)
		}
		if got := AGRequiredChecksState(agStatesOf(snap)); got != nil {
			t.Fatalf("rollup = %q, want null", *got)
		}
	})
}
