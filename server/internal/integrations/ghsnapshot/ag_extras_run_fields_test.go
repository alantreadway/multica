package ghsnapshot

import (
	"context"
	"os"
	"testing"
)

// AG patch tests for the per-run check fields: the fail-closed paths and the
// 9003 migration. See ag-patches/PATCHES.md.

func TestAGPatchRunFieldsAreNullWhenGitHubOmitsThem(t *testing.T) {
	cases := map[string]string{
		"a malformed reply": `{"data":{"repository":null}}`,
		"not JSON":          `{`,
		"checkSuite null":   agExtrasPage("", false, "", `{"__typename":"CheckRun","databaseId":1,"name":"build","startedAt":"2026-10-26T10:00:00Z","isRequired":true,"checkSuite":null}`),
		"startedAt null":    agExtrasPage("", false, "", `{"__typename":"CheckRun","databaseId":1,"name":"build","startedAt":null,"isRequired":true,"checkSuite":{"status":"COMPLETED","conclusion":null,"workflowRun":null}}`),
	}
	for name, reply := range cases {
		t.Run("AG patch: "+name+" leaves no value to guess", func(t *testing.T) {
			f := &agFakeGitHub{
				runs: []map[string]any{agRun(1, "build", "completed", "success")},
				gql:  func(map[string]any) string { return reply },
			}
			snap, err := f.fetch(t)
			if err != nil {
				t.Fatalf("the extras failed the snapshot: %v", err)
			}
			if len(snap.Contexts) != 1 || snap.Contexts[0].Status != "completed" {
				t.Fatalf("REST data lost: %+v", snap.Contexts)
			}
			r := snap.Contexts[0].AGRun
			// What GitHub did not send stays zero (null on the wire); unknown is nil.
			sentStart, sentSuite := name == "checkSuite null", name == "startedAt null"
			if r == nil {
				if sentStart || sentSuite {
					t.Fatalf("the run fields GitHub did send were dropped")
				}
				return
			}
			if r.WorkflowName != "" || r.RunID != 0 || r.SuiteConclusion != "" {
				t.Fatalf("run fields invented from nothing: %+v", r)
			}
			if r.StartedAt.IsZero() == sentStart {
				t.Fatalf("StartedAt = %v, GitHub sent one: %v", r.StartedAt, sentStart)
			}
			if (r.SuiteStatus != "") != sentSuite {
				t.Fatalf("SuiteStatus = %q, GitHub sent one: %v", r.SuiteStatus, sentSuite)
			}
		})
	}
}

func TestAGPatchRunFieldsMigrationIsAdditiveAndReversible(t *testing.T) {
	pool := testDBPool(t)
	ctx := context.Background()
	up, err := os.ReadFile("../../../migrations/9003_ag_github_pr_check_run_fields.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/9003_ag_github_pr_check_run_fields.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]string{
		"workflow_name":    "text",
		"run_id":           "bigint",
		"started_at":       "timestamp with time zone",
		"suite_status":     "text",
		"suite_conclusion": "text",
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The migration is exercised inside a transaction that is always rolled back.
	defer func() { _ = tx.Rollback(ctx) }()
	found := func() map[string]string {
		rows, err := tx.Query(ctx, `SELECT column_name, data_type || CASE WHEN is_nullable = 'YES' THEN '' ELSE ' NOT NULL' END
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'github_pull_request_check_run'
			  AND column_name = ANY($1)`, []string{"workflow_name", "run_id", "started_at", "suite_status", "suite_conclusion"})
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var name, typ string
			if err := rows.Scan(&name, &typ); err != nil {
				t.Fatal(err)
			}
			out[name] = typ
		}
		return out
	}

	t.Run("AG patch: 9003 down removes the columns and up adds them back nullable with no index", func(t *testing.T) {
		if _, err := tx.Exec(ctx, string(down)); err != nil {
			t.Fatalf("down: %v", err)
		}
		if got := found(); len(got) != 0 {
			t.Fatalf("columns after down = %v", got)
		}
		var indexesBefore int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE tablename = 'github_pull_request_check_run'`).Scan(&indexesBefore); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(up)); err != nil {
			t.Fatalf("up: %v", err)
		}
		got := found()
		for name, typ := range columns {
			if got[name] != typ {
				t.Errorf("column %s = %q, want nullable %q", name, got[name], typ)
			}
		}
		var indexesAfter int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE tablename = 'github_pull_request_check_run'`).Scan(&indexesAfter); err != nil {
			t.Fatal(err)
		}
		if indexesAfter != indexesBefore {
			t.Errorf("indexes %d -> %d, want none added", indexesBefore, indexesAfter)
		}
	})
}
