package handler

import (
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The workspace catalog lists by position, then LOWER(name) as the tie-break.
func TestListLabelsOrdersByPositionThenName(t *testing.T) {
	ws := createOtherTestWorkspace(t)
	for _, row := range []struct {
		name string
		pos  int
	}{{"delta", 2}, {"Bravo", 1}, {"alpha", 1}, {"zulu", 0}, {"Charlie", 1}} {
		dbfx.Exec(t,
			`INSERT INTO issue_label (workspace_id, resource_type, name, color, position) VALUES ($1, 'issue', $2, '#3b82f6', $3)`,
			ws, row.name, row.pos)
	}

	got := labelNames(listLabelsIn(t, ws, "issue"))
	want := []string{"zulu", "alpha", "Bravo", "Charlie", "delta"}
	if !equalStrings(got, want) {
		t.Fatalf("workspace label order = %v, want %v", got, want)
	}
}

// With nothing to order, an empty list is the exact current set.
func TestReorderLabelsEmptyNamespace(t *testing.T) {
	ws := createOtherTestWorkspace(t)
	createLabelIn(t, ws, "issue", "only-issue")

	var out labelList
	testutil.Call(t, testHandler.ReorderLabels, reorderLabelsIn(ws, "skill", []string{})).
		Want(http.StatusOK).JSON(&out)
	if len(out.Labels) != 0 || out.Total != 0 {
		t.Fatalf("reorder of an empty namespace = %+v, want no labels", out)
	}
	if got := labelNames(listLabelsIn(t, ws, "issue")); !equalStrings(got, []string{"only-issue"}) {
		t.Fatalf("issue labels touched by a skill reorder: %v", got)
	}
}

// A reorder tells other clients to refetch: one label:updated for the workspace,
// carrying the resource type and the new order.
func TestReorderLabelsPublishesLabelUpdated(t *testing.T) {
	ws := createOtherTestWorkspace(t)
	a := createLabelIn(t, ws, "issue", "a")
	b := createLabelIn(t, ws, "issue", "b")

	var mu sync.Mutex
	var seen []events.Event
	testHandler.Bus.Subscribe(protocol.EventLabelUpdated, func(e events.Event) {
		if e.WorkspaceID != ws {
			return
		}
		mu.Lock()
		seen = append(seen, e)
		mu.Unlock()
	})

	// A rejected reorder publishes nothing.
	testutil.Call(t, testHandler.ReorderLabels, reorderLabelsIn(ws, "issue", []string{a.ID})).
		Want(http.StatusConflict)
	mu.Lock()
	rejected := len(seen)
	mu.Unlock()
	if rejected != 0 {
		t.Fatalf("a rejected reorder published %d label:updated events", rejected)
	}

	testutil.Call(t, testHandler.ReorderLabels, reorderLabelsIn(ws, "issue", []string{b.ID, a.ID})).
		Want(http.StatusOK)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("label:updated events = %d, want 1", len(seen))
	}
	payload, ok := seen[0].Payload.(map[string]any)
	if !ok || payload["resource_type"] != "issue" {
		t.Fatalf("payload = %#v, want resource_type issue", seen[0].Payload)
	}
	labels, ok := payload["labels"].([]LabelResponse)
	if !ok || !equalStrings(labelIDs(labels), []string{b.ID, a.ID}) {
		t.Fatalf("payload labels = %#v, want [b a]", payload["labels"])
	}
}

// Issue labels follow the workspace order after a reorder, whatever order they
// were attached in.
func TestIssueLabelsFollowReorder(t *testing.T) {
	issueID := dbfx.Issue(t, "label reorder follow", nil)
	suffix := uuid.NewString()[:6]
	a := createLabelIn(t, testWorkspaceID, "issue", "follow-a-"+suffix)
	b := createLabelIn(t, testWorkspaceID, "issue", "follow-b-"+suffix)
	c := createLabelIn(t, testWorkspaceID, "issue", "follow-c-"+suffix)
	t.Cleanup(func() {
		for _, id := range []string{a.ID, b.ID, c.ID} {
			testPool.Exec(t.Context(), `DELETE FROM issue_label WHERE id = $1`, id)
		}
	})
	for _, l := range []LabelResponse{b, c, a} {
		testutil.Call(t, testHandler.AttachLabel,
			withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/labels", map[string]any{"label_id": l.ID}), "id", issueID),
		).Want(http.StatusOK)
	}
	issueLabelIDs := func() []string {
		var out struct {
			Labels []LabelResponse `json:"labels"`
		}
		testutil.Call(t, testHandler.ListLabelsForIssue,
			withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID+"/labels", nil), "id", issueID),
		).Want(http.StatusOK).JSON(&out)
		return labelIDs(out.Labels)
	}
	if got := issueLabelIDs(); !equalStrings(got, []string{a.ID, b.ID, c.ID}) {
		t.Fatalf("issue labels before reorder = %v, want creation order a b c", got)
	}

	// Reorder the whole namespace: c, a, b first, then any labels other suites
	// left behind, in their current order.
	ids := []string{c.ID, a.ID, b.ID}
	mine := map[string]bool{a.ID: true, b.ID: true, c.ID: true}
	for _, l := range listLabelsIn(t, testWorkspaceID, "issue") {
		if !mine[l.ID] {
			ids = append(ids, l.ID)
		}
	}
	testutil.Call(t, testHandler.ReorderLabels, reorderLabelsIn(testWorkspaceID, "issue", ids)).
		Want(http.StatusOK)

	if got := issueLabelIDs(); !equalStrings(got, []string{c.ID, a.ID, b.ID}) {
		t.Fatalf("issue labels after reorder = %v, want c a b", got)
	}
}
