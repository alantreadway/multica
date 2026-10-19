package handler

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Label order tests run in a throwaway workspace so the exact-set contract of
// the reorder endpoint is not at the mercy of labels other suites leave behind
// in the shared test workspace.

type labelList struct {
	Labels []LabelResponse `json:"labels"`
	Total  int             `json:"total"`
}

func labelReq(ws, method, path string, body any) *http.Request {
	return testutil.WithHeaders(newRequest(method, path, body), "X-Workspace-ID", ws)
}

func createLabelIn(t *testing.T, ws, resourceType, name string) LabelResponse {
	t.Helper()
	var out LabelResponse
	testutil.Call(t, testHandler.CreateLabel, labelReq(ws, http.MethodPost, "/api/labels", map[string]any{
		"resource_type": resourceType, "name": name, "color": "#3b82f6",
	})).Want(http.StatusCreated).JSON(&out)
	return out
}

func listLabelsIn(t *testing.T, ws, resourceType string) []LabelResponse {
	t.Helper()
	var out labelList
	testutil.Call(t, testHandler.ListLabels, labelReq(ws, http.MethodGet, "/api/labels?resource_type="+resourceType, nil)).
		Want(http.StatusOK).JSON(&out)
	return out.Labels
}

func labelNames(labels []LabelResponse) []string {
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return names
}

func labelIDs(labels []LabelResponse) []string {
	ids := make([]string, len(labels))
	for i, l := range labels {
		ids[i] = l.ID
	}
	return ids
}

func reorderLabelsIn(ws, resourceType string, ids []string) *http.Request {
	return labelReq(ws, http.MethodPut, "/api/labels/order", map[string]any{
		"resource_type": resourceType, "label_ids": ids,
	})
}

// New labels are appended per (workspace, resource type) namespace.
func TestCreateLabelAppendsToItsNamespace(t *testing.T) {
	ws := createOtherTestWorkspace(t)
	// Names are deliberately not alphabetical: position, not name, decides.
	for i, name := range []string{"zeta", "alpha", "mid"} {
		if got := createLabelIn(t, ws, "issue", name); int(got.Position) != i {
			t.Fatalf("issue label %q position = %d, want %d", name, got.Position, i)
		}
	}
	if got := createLabelIn(t, ws, "skill", "first-skill"); got.Position != 0 {
		t.Fatalf("first skill label position = %d, want 0 (own namespace)", got.Position)
	}
	if got := labelNames(listLabelsIn(t, ws, "issue")); !equalStrings(got, []string{"zeta", "alpha", "mid"}) {
		t.Fatalf("issue label order = %v, want creation order", got)
	}
}

// The migration's backfill numbers existing labels alphabetically per
// (workspace, resource type). Run its UPDATE against rows that all start at 0.
func TestLabelPositionBackfillIsAlphabeticalPerNamespace(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/574_label_position.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	i := strings.Index(sql, "UPDATE issue_label")
	if i < 0 {
		t.Fatal("backfill UPDATE not found in migration")
	}
	ws := createOtherTestWorkspace(t)
	for _, row := range [][2]string{
		{"issue", "Charlie"}, {"issue", "alpha"}, {"issue", "Bravo"},
		{"skill", "zulu"}, {"skill", "Yankee"},
	} {
		dbfx.Exec(t,
			`INSERT INTO issue_label (workspace_id, resource_type, name, color, position) VALUES ($1, $2, $3, '#3b82f6', 0)`,
			ws, row[0], row[1])
	}
	// Scope the migration's statement to this test's workspace: unscoped it
	// would renumber labels other tests in the shared database depend on. The
	// ranking text (PARTITION BY ... ORDER BY LOWER(name), id) is untouched.
	stmt := sql[i:]
	for _, r := range [][2]string{
		{"FROM issue_label\n) ranked", "FROM issue_label WHERE workspace_id = $1\n) ranked"},
		{"WHERE l.id = ranked.id", "WHERE l.id = ranked.id AND l.workspace_id = $1"},
	} {
		if strings.Count(stmt, r[0]) != 1 {
			t.Fatalf("backfill statement no longer contains %q; update this test", r[0])
		}
		stmt = strings.Replace(stmt, r[0], r[1], 1)
	}
	dbfx.Exec(t, stmt, ws)

	for rt, want := range map[string][]string{
		"issue": {"alpha", "Bravo", "Charlie"},
		"skill": {"Yankee", "zulu"},
	} {
		labels := listLabelsIn(t, ws, rt)
		if got := labelNames(labels); !equalStrings(got, want) {
			t.Errorf("%s order = %v, want %v", rt, got, want)
		}
		for pos, l := range labels {
			if int(l.Position) != pos {
				t.Errorf("%s %q position = %d, want %d", rt, l.Name, l.Position, pos)
			}
		}
	}
}

func TestReorderLabelsRewritesOneNamespace(t *testing.T) {
	ws := createOtherTestWorkspace(t)
	a := createLabelIn(t, ws, "issue", "a")
	b := createLabelIn(t, ws, "issue", "b")
	c := createLabelIn(t, ws, "issue", "c")
	s1 := createLabelIn(t, ws, "skill", "s1")
	s2 := createLabelIn(t, ws, "skill", "s2")

	var out labelList
	testutil.Call(t, testHandler.ReorderLabels, reorderLabelsIn(ws, "issue", []string{c.ID, a.ID, b.ID})).
		Want(http.StatusOK).JSON(&out)
	if got := labelNames(out.Labels); !equalStrings(got, []string{"c", "a", "b"}) || out.Total != 3 {
		t.Fatalf("reorder response = %v (total %d), want [c a b]", got, out.Total)
	}
	for pos, l := range out.Labels {
		if int(l.Position) != pos {
			t.Errorf("%q position = %d, want %d", l.Name, l.Position, pos)
		}
	}
	if got := labelNames(listLabelsIn(t, ws, "issue")); !equalStrings(got, []string{"c", "a", "b"}) {
		t.Fatalf("persisted issue order = %v", got)
	}
	if got := labelIDs(listLabelsIn(t, ws, "skill")); !equalStrings(got, []string{s1.ID, s2.ID}) {
		t.Fatalf("skill labels moved when issue labels were reordered: %v", got)
	}

	// Labels created after a reorder go last.
	if d := createLabelIn(t, ws, "issue", "d"); d.Position != 3 {
		t.Fatalf("label created after reorder has position %d, want 3", d.Position)
	}
}

func TestReorderLabelsFailsClosed(t *testing.T) {
	ws := createOtherTestWorkspace(t)
	a := createLabelIn(t, ws, "issue", "a")
	b := createLabelIn(t, ws, "issue", "b")
	skill := createLabelIn(t, ws, "skill", "s")
	// A label in the shared test workspace, foreign to ws.
	foreign := createLabelIn(t, testWorkspaceID, "issue", "foreign-"+uuid.NewString()[:8])
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue_label WHERE id = $1`, foreign.ID)
	})

	cases := []struct {
		name         string
		resourceType string
		ids          []string
		want         int
	}{
		{"malformed id", "issue", []string{a.ID, "not-a-uuid"}, http.StatusBadRequest},
		{"duplicate id", "issue", []string{a.ID, a.ID}, http.StatusBadRequest},
		{"unknown resource type", "project", []string{a.ID, b.ID}, http.StatusBadRequest},
		{"unknown label", "issue", []string{a.ID, uuid.NewString()}, http.StatusConflict},
		{"missing label", "issue", []string{a.ID}, http.StatusConflict},
		{"extra label", "issue", []string{a.ID, b.ID, uuid.NewString()}, http.StatusConflict},
		{"label from another resource type", "issue", []string{a.ID, skill.ID}, http.StatusConflict},
		{"label from another workspace", "issue", []string{a.ID, foreign.ID}, http.StatusConflict},
		{"empty list", "issue", []string{}, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testutil.Call(t, testHandler.ReorderLabels, reorderLabelsIn(ws, tc.resourceType, tc.ids)).Want(tc.want)
			labels := listLabelsIn(t, ws, "issue")
			if got := labelIDs(labels); !equalStrings(got, []string{a.ID, b.ID}) {
				t.Fatalf("order changed by a rejected request: %v", got)
			}
			for pos, l := range labels {
				if int(l.Position) != pos {
					t.Fatalf("%q position = %d after rejected request, want %d", l.Name, l.Position, pos)
				}
			}
		})
	}

	testutil.Call(t, testHandler.ReorderLabels, labelReq(ws, http.MethodPut, "/api/labels/order", "nope")).
		Want(http.StatusBadRequest)
}

// Every list that returns labels honours position, then LOWER(name).
func TestLabelListsOrderByPositionThenName(t *testing.T) {
	issueID := dbfx.Issue(t, "label order issue", nil)
	agentID := handlerTestAgentID(t)
	skill := dbfx.Insert(t, "skill", testutil.Cols{"workspace_id": testWorkspaceID, "name": "label-order-skill-" + uuid.NewString()[:8], "description": ""})

	type spec struct {
		resourceType string
		junction     string
		ownerCol     string
		owner        string
	}
	for _, sp := range []spec{
		{"issue", "issue_to_label", "issue_id", issueID},
		{"agent", "agent_to_label", "agent_id", agentID},
		{"skill", "skill_to_label", "skill_id", skill},
	} {
		t.Run(sp.resourceType, func(t *testing.T) {
			// (name, position): "b" and "A" tie at 1; "c" is first by position.
			var ids []string
			for _, row := range []struct {
				name string
				pos  int
			}{{"b", 1}, {"A", 1}, {"c", 0}, {"d", 2}} {
				id := dbfx.Insert(t, "issue_label", testutil.Cols{
					"workspace_id": testWorkspaceID, "resource_type": sp.resourceType,
					"name": row.name + "-" + uuid.NewString()[:6], "color": "#3b82f6", "position": row.pos,
				})
				ids = append(ids, id)
				dbfx.Exec(t, `INSERT INTO `+sp.junction+` (`+sp.ownerCol+`, label_id) VALUES ($1, $2)`, sp.owner, id)
			}
			// ids[0]=b(1) ids[1]=A(1) ids[2]=c(0) ids[3]=d(2): want c, A, b, d.
			want := []string{ids[2], ids[1], ids[0], ids[3]}

			ctx := t.Context()
			ws, owner := parseUUID(testWorkspaceID), parseUUID(sp.owner)
			var got []string
			switch sp.resourceType {
			case "issue":
				rows, err := testHandler.Queries.ListLabelsByIssue(ctx, db.ListLabelsByIssueParams{IssueID: owner, WorkspaceID: ws})
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range rows {
					got = append(got, uuidToString(r.ID))
				}
				batch, err := testHandler.Queries.ListLabelsForIssues(ctx, db.ListLabelsForIssuesParams{IssueIds: []pgtype.UUID{owner}, WorkspaceID: ws})
				if err != nil {
					t.Fatal(err)
				}
				var bgot []string
				for _, r := range batch {
					bgot = append(bgot, uuidToString(r.ID))
				}
				if !equalStrings(bgot, want) {
					t.Errorf("ListLabelsForIssues order = %v, want %v", bgot, want)
				}
			case "agent":
				rows, err := testHandler.Queries.ListLabelsByAgent(ctx, db.ListLabelsByAgentParams{AgentID: owner, WorkspaceID: ws})
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range rows {
					got = append(got, uuidToString(r.ID))
				}
				batch, err := testHandler.Queries.ListLabelsForAgents(ctx, db.ListLabelsForAgentsParams{AgentIds: []pgtype.UUID{owner}, WorkspaceID: ws})
				if err != nil {
					t.Fatal(err)
				}
				var bgot []string
				for _, r := range batch {
					bgot = append(bgot, uuidToString(r.ID))
				}
				if !equalStrings(bgot, want) {
					t.Errorf("ListLabelsForAgents order = %v, want %v", bgot, want)
				}
			case "skill":
				rows, err := testHandler.Queries.ListLabelsBySkill(ctx, db.ListLabelsBySkillParams{SkillID: owner, WorkspaceID: ws})
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range rows {
					got = append(got, uuidToString(r.ID))
				}
				batch, err := testHandler.Queries.ListLabelsForSkills(ctx, db.ListLabelsForSkillsParams{SkillIds: []pgtype.UUID{owner}, WorkspaceID: ws})
				if err != nil {
					t.Fatal(err)
				}
				var bgot []string
				for _, r := range batch {
					bgot = append(bgot, uuidToString(r.ID))
				}
				if !equalStrings(bgot, want) {
					t.Errorf("ListLabelsForSkills order = %v, want %v", bgot, want)
				}
			}
			if !equalStrings(got, want) {
				t.Errorf("order = %v, want %v", got, want)
			}
		})
	}
}
