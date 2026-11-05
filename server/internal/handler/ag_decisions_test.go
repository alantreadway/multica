package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestAGPatchDecisionCardValidation(t *testing.T) {
	ok := AGDecisionCardInput{DedupeKey: "k", WaitingOn: "alan", CardType: "bot_pr", Content: json.RawMessage(`{"title":"x"}`)}
	with := func(edit func(*AGDecisionCardInput)) AGDecisionCardInput {
		c := ok
		edit(&c)
		return c
	}
	cases := []struct {
		name  string
		cards []AGDecisionCardInput
		want  string
	}{
		{"valid", []AGDecisionCardInput{ok}, ""},
		{"empty snapshot resolves everything", nil, ""},
		{"blank key", []AGDecisionCardInput{with(func(c *AGDecisionCardInput) { c.DedupeKey = " " })}, "dedupe_key"},
		{"blank waiting_on", []AGDecisionCardInput{with(func(c *AGDecisionCardInput) { c.WaitingOn = "" })}, "waiting_on"},
		{"blank type", []AGDecisionCardInput{with(func(c *AGDecisionCardInput) { c.CardType = "" })}, "card_type"},
		{"content an array", []AGDecisionCardInput{with(func(c *AGDecisionCardInput) { c.Content = json.RawMessage(`[]`) })}, "content"},
		{"content null", []AGDecisionCardInput{with(func(c *AGDecisionCardInput) { c.Content = json.RawMessage(`null`) })}, "content"},
		{"content missing", []AGDecisionCardInput{with(func(c *AGDecisionCardInput) { c.Content = nil })}, "content"},
		{"content too big", []AGDecisionCardInput{with(func(c *AGDecisionCardInput) {
			c.Content = json.RawMessage(`{"a":"` + strings.Repeat("x", agDecisionMaxJSONBytes) + `"}`)
		})}, "content"},
		{"duplicate key", []AGDecisionCardInput{ok, ok}, "duplicate"},
		{"too many", make([]AGDecisionCardInput, agDecisionMaxCards+1), "at most"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := agValidateDecisionCards(tc.cards)
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestAGPatchDecisionRecordValidation(t *testing.T) {
	ok := AGDecisionRecordInput{ID: "r1", At: "2026-11-05T12:00:00Z", Actor: "policy", Action: "shadow", Record: json.RawMessage(`{}`)}
	with := func(edit func(*AGDecisionRecordInput)) []AGDecisionRecordInput {
		r := ok
		edit(&r)
		return []AGDecisionRecordInput{r}
	}
	if got := agValidateDecisionRecords([]AGDecisionRecordInput{ok}); got != "" {
		t.Fatalf("valid record rejected: %s", got)
	}
	for name, records := range map[string][]AGDecisionRecordInput{
		"id":     with(func(r *AGDecisionRecordInput) { r.ID = "" }),
		"at":     with(func(r *AGDecisionRecordInput) { r.At = "yesterday" }),
		"actor":  with(func(r *AGDecisionRecordInput) { r.Actor = "" }),
		"action": with(func(r *AGDecisionRecordInput) { r.Action = "" }),
		"record": with(func(r *AGDecisionRecordInput) { r.Record = json.RawMessage(`"text"`) }),
	} {
		if got := agValidateDecisionRecords(records); !strings.Contains(got, name) {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

type agDecisionList struct {
	Cards []struct {
		DedupeKey string  `json:"dedupe_key"`
		IssueID   *string `json:"issue_id"`
		WaitingOn string  `json:"waiting_on"`
		Revision  int32   `json:"revision"`
		HeadSHA   *string `json:"head_sha"`
		Content   struct {
			Title string `json:"title"`
		} `json:"content"`
	} `json:"cards"`
	LastSyncAt *string `json:"last_sync_at"`
}

func agDecisionsCleanup(t *testing.T) {
	t.Helper()
	clean := func() {
		ctx := context.Background()
		for _, table := range []string{"ag_decision_card", "ag_decision_record", "ag_decision_engine"} {
			if _, err := testPool.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id = $1`, testWorkspaceID); err != nil {
				t.Fatalf("clean %s: %v", table, err)
			}
		}
	}
	clean()
	t.Cleanup(clean)
}

func agPutCards(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequestAs(testUserID, http.MethodPut, "/api/ag/decisions/cards", json.RawMessage(body))
	w := httptest.NewRecorder()
	testHandler.PutAGDecisionCards(w, req)
	return w
}

func agListCards(t *testing.T) agDecisionList {
	t.Helper()
	var out agDecisionList
	req := newRequestAs(testUserID, http.MethodGet, "/api/ag/decisions/", nil)
	testutil.Call(t, testHandler.ListAGDecisions, req).Want(http.StatusOK).JSON(&out)
	return out
}

func TestAGPatchDecisionCardSnapshot(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agDecisionsCleanup(t)

	if got := agListCards(t); len(got.Cards) != 0 || got.LastSyncAt != nil {
		t.Fatalf("before any sync: %+v", got)
	}

	cardA := `{"dedupe_key":"a","waiting_on":"alan","card_type":"bot_pr","head_sha":"abc","content":{"title":"A"}}`
	cardB := `{"dedupe_key":"b","waiting_on":"author","card_type":"colleague_pr","content":{"title":"B"}}`
	if w := agPutCards(t, `{"cards":[`+cardA+`,`+cardB+`]}`); w.Code != http.StatusOK {
		t.Fatalf("first put: %d %s", w.Code, w.Body)
	}
	got := agListCards(t)
	if len(got.Cards) != 2 || got.LastSyncAt == nil {
		t.Fatalf("after first put: %+v", got)
	}

	var count struct {
		Count int64 `json:"count"`
	}
	countReq := newRequestAs(testUserID, http.MethodGet, "/api/ag/decisions/count", nil)
	testutil.Call(t, testHandler.CountAGDecisions, countReq).Want(http.StatusOK).JSON(&count)
	if count.Count != 1 {
		t.Fatalf("count should only include cards waiting on Alan, got %d", count.Count)
	}

	// The same snapshot changes nothing, so it neither bumps a revision nor publishes.
	w := agPutCards(t, `{"cards":[`+cardA+`,`+cardB+`]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changed":false`) {
		t.Fatalf("repeat put: %d %s", w.Code, w.Body)
	}
	if got := agListCards(t); got.Cards[0].Revision != 1 {
		t.Fatalf("unchanged card moved to revision %d", got.Cards[0].Revision)
	}

	// New text bumps the revision, and a card left out is resolved.
	changedA := strings.Replace(cardA, `"title":"A"`, `"title":"A2"`, 1)
	w = agPutCards(t, `{"cards":[`+changedA+`]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changed":true`) || !strings.Contains(w.Body.String(), `"resolved":1`) {
		t.Fatalf("changed put: %d %s", w.Code, w.Body)
	}
	got = agListCards(t)
	if len(got.Cards) != 1 || got.Cards[0].Content.Title != "A2" || got.Cards[0].Revision != 2 {
		t.Fatalf("after change: %+v", got)
	}

	// A resolved card that comes back is open again, on a new revision.
	w = agPutCards(t, `{"cards":[`+changedA+`,`+cardB+`]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("return put: %d %s", w.Code, w.Body)
	}
	got = agListCards(t)
	if len(got.Cards) != 2 {
		t.Fatalf("resolved card did not come back: %+v", got)
	}
	for _, c := range got.Cards {
		if c.DedupeKey == "b" && c.Revision != 2 {
			t.Fatalf("returned card on revision %d", c.Revision)
		}
	}

	// An empty snapshot is a real answer: nothing is waiting any more.
	if w := agPutCards(t, `{"cards":[]}`); w.Code != http.StatusOK {
		t.Fatalf("empty put: %d %s", w.Code, w.Body)
	}
	if got := agListCards(t); len(got.Cards) != 0 {
		t.Fatalf("empty snapshot left %d cards open", len(got.Cards))
	}
}

func TestAGPatchDecisionCardRejectsForeignIssue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agDecisionsCleanup(t)
	keep := `{"dedupe_key":"keep","waiting_on":"alan","card_type":"bot_pr","content":{"title":"K"}}`
	if w := agPutCards(t, `{"cards":[`+keep+`]}`); w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body)
	}
	foreign := `{"dedupe_key":"x","issue_id":"00000000-0000-4000-8000-000000000001","waiting_on":"alan","card_type":"bot_pr","content":{}}`
	if w := agPutCards(t, `{"cards":[`+foreign+`]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("foreign issue: %d %s", w.Code, w.Body)
	}
	// A rejected snapshot leaves the previous cards standing.
	if got := agListCards(t); len(got.Cards) != 1 || got.Cards[0].DedupeKey != "keep" {
		t.Fatalf("rejected snapshot changed the cards: %+v", got)
	}
}

func TestAGPatchDecisionRecordsAppendOnly(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agDecisionsCleanup(t)
	post := func(body string) string {
		req := newRequestAs(testUserID, http.MethodPost, "/api/ag/decisions/records", json.RawMessage(body))
		w := httptest.NewRecorder()
		testHandler.PostAGDecisionRecords(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("post: %d %s", w.Code, w.Body)
		}
		return w.Body.String()
	}
	older := `{"id":"r1","at":"2026-11-05T10:00:00Z","actor":"policy","action":"shadow","record":{"note":"first"}}`
	newer := `{"id":"r2","at":"2026-11-05T11:00:00Z","actor":"alan","action":"approve","record":{"note":"second"}}`
	if got := post(`{"records":[` + older + `,` + newer + `]}`); !strings.Contains(got, `"inserted":2`) {
		t.Fatalf("first post: %s", got)
	}
	// A retry with altered text inserts nothing and rewrites nothing.
	if got := post(`{"records":[` + strings.Replace(older, "first", "rewritten", 1) + `]}`); !strings.Contains(got, `"inserted":0`) {
		t.Fatalf("retry: %s", got)
	}
	var out struct {
		Records []struct {
			ID     string `json:"id"`
			Record struct {
				Note string `json:"note"`
			} `json:"record"`
		} `json:"records"`
	}
	req := newRequestAs(testUserID, http.MethodGet, "/api/ag/decisions/records", nil)
	testutil.Call(t, testHandler.ListAGDecisionRecords, req).Want(http.StatusOK).JSON(&out)
	if len(out.Records) != 2 || out.Records[0].ID != "r2" || out.Records[1].Record.Note != "first" {
		t.Fatalf("records: %+v", out.Records)
	}
}
