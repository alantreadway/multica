package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// AG patch: a wakeup waiting on its own issue's done or closed status can never
// fire, because closing the issue disables its wakeups. Creation rejects it.
func TestAGPatchWakeupConditionRejectsClosingStatus(t *testing.T) {
	f, s, issue, agent := conditionFixture(t)
	f.Insert(t, "issue_status", testutil.Cols{"workspace_id": f.WorkspaceID, "key": "ag_finished", "name": "AG Finished", "category": "done", "color": "#000000"})
	f.Insert(t, "issue_status", testutil.Cols{"workspace_id": f.WorkspaceID, "key": "ag_dropped", "name": "AG Dropped", "category": "closed", "color": "#000000"})
	issueID := util.UUIDToString(issue)

	for _, tc := range []struct {
		status, category, states string
	}{
		{"done", "done", "done|ended"},
		{"cancelled", "closed", "ended"},
		{"ag_finished", "done", "done|ended"},
		{"ag_dropped", "closed", "ended"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			_, err := s.Create(context.Background(), issue, parseTestUUID(t, f.UserID), pgtype.UUID{}, WakeupInput{AgentID: agent, Kind: "event", Instruction: "x",
				Condition: condition(t, map[string]any{"type": "issue_field", "field": "status", "value": tc.status})})
			if !errors.Is(err, ErrWakeupInput) {
				t.Fatalf("error = %v, want ErrWakeupInput", err)
			}
			for _, want := range []string{
				"--until-status " + tc.status + " (" + tc.category + " category) can never fire",
				"--until-issue " + issueID + " --until-issue-state " + tc.states,
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q lacks %q", err, want)
				}
			}
			if n := f.Count(t, "SELECT count(*) FROM issue_wakeup WHERE issue_id=$1", issue); n != 0 {
				t.Fatalf("rejected rule left %d rows", n)
			}
		})
	}
}

// AG patch: open-category statuses and unknown keys keep their old handling.
func TestAGPatchWakeupConditionKeepsOpenAndUnknownStatuses(t *testing.T) {
	f, s, issue, agent := conditionFixture(t)
	f.Insert(t, "issue_status", testutil.Cols{"workspace_id": f.WorkspaceID, "key": "ag_checking", "name": "AG Checking", "category": "started", "color": "#000000"})
	for _, status := range []string{"in_review", "blocked", "todo", "ag_checking"} {
		w := wakeCreate(t, f, s, issue, WakeupInput{AgentID: agent, Kind: "event", Instruction: "x",
			Condition: condition(t, map[string]any{"type": "issue_field", "field": "status", "value": status})})
		if !w.Enabled {
			t.Fatalf("%s: rule not enabled", status)
		}
	}
	_, err := s.Create(context.Background(), issue, parseTestUUID(t, f.UserID), pgtype.UUID{}, WakeupInput{AgentID: agent, Kind: "event", Instruction: "x",
		Condition: condition(t, map[string]any{"type": "issue_field", "field": "status", "value": "no_such_status"})})
	if !errors.Is(err, ErrWakeupInput) || !strings.Contains(err.Error(), "unknown status") {
		t.Fatalf("unknown status error = %v", err)
	}
}
