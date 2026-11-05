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
		for _, table := range []string{"ag_decision_card", "ag_decision_record", "ag_decision_engine", "ag_decision_intent"} {
			if _, err := testPool.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id = $1`, testWorkspaceID); err != nil {
				t.Fatalf("clean %s: %v", table, err)
			}
		}
	}
	clean()
	t.Cleanup(clean)
}

func TestAGPatchDeleteWorkspaceClearsDecisionRows(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agDecisionsCleanup(t)
	ctx := context.Background()

	const slug = "handler-tests-ag-decision-delete"
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)
	var wsID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO workspace (name, slug, description) VALUES ($1, $2, $3) RETURNING id
`, "AG Decision Delete", slug, "DeleteWorkspace clears decision rows").Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, table := range []string{"ag_decision_card", "ag_decision_record", "ag_decision_engine", "ag_decision_intent"} {
			_, _ = testPool.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id = $1`, wsID)
		}
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, wsID)
	})

	for _, id := range []string{wsID, testWorkspaceID} {
		if _, err := testPool.Exec(ctx, `
INSERT INTO ag_decision_card (workspace_id, dedupe_key, waiting_on, card_type) VALUES ($1, 'k', 'alan', 'pr')
`, id); err != nil {
			t.Fatalf("insert card: %v", err)
		}
		if _, err := testPool.Exec(ctx, `
INSERT INTO ag_decision_record (id, workspace_id, at, actor, action, record) VALUES ($1, $2, now(), 'alan', 'approve', '{}')
`, "delete-test-"+id, id); err != nil {
			t.Fatalf("insert record: %v", err)
		}
		if _, err := testPool.Exec(ctx, `
INSERT INTO ag_decision_engine (workspace_id, last_sync_at) VALUES ($1, now())
`, id); err != nil {
			t.Fatalf("insert engine: %v", err)
		}
		if _, err := testPool.Exec(ctx, `
INSERT INTO ag_decision_intent (workspace_id, card_id, card_revision, action_id, actor_user_id, expires_at)
VALUES ($1, gen_random_uuid(), 1, 'park', $2, now())
`, id, testUserID); err != nil {
			t.Fatalf("insert intent: %v", err)
		}
	}

	if err := testHandler.Queries.DeleteWorkspace(ctx, parseUUID(wsID)); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}

	for _, table := range []string{"ag_decision_card", "ag_decision_record", "ag_decision_engine", "ag_decision_intent"} {
		var deleted, kept int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE workspace_id = $1`, wsID).Scan(&deleted); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE workspace_id = $1`, testWorkspaceID).Scan(&kept); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if deleted != 0 || kept != 1 {
			t.Errorf("%s: deleted workspace has %d rows, other workspace has %d; want 0 and 1", table, deleted, kept)
		}
	}
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

func TestAGPatchDecisionIntentValidation(t *testing.T) {
	long := strings.Repeat("x", agDecisionMaxNoteBytes+1)
	for name, tc := range map[string]struct {
		req  AGDecisionIntentRequest
		want string
	}{
		"valid":       {AGDecisionIntentRequest{ActionID: "park", Note: "ok"}, ""},
		"no action":   {AGDecisionIntentRequest{ActionID: " "}, "action_id"},
		"long note":   {AGDecisionIntentRequest{ActionID: "park", Note: long}, "note"},
		"long reason": {AGDecisionIntentRequest{ActionID: "park", OverrideReason: long}, "override_reason"},
	} {
		got := agValidateDecisionIntent(tc.req)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want it to contain %q", name, got, tc.want)
		}
	}
	for name, content := range map[string]string{"no actions": `{}`, "other action": `{"actions":[{"id":"merge"}]}`, "malformed": `{"actions":3}`} {
		if agDecisionOffersAction([]byte(content), "park") {
			t.Errorf("%s should not offer park", name)
		}
	}
	if !agDecisionOffersAction([]byte(`{"actions":[{"id":"merge"},{"id":"park"}]}`), "park") {
		t.Error("the listed action should be offered")
	}
	for status, ok := range map[string]bool{"succeeded": true, "failed": true, "refused": true, "running": false, "": false} {
		if got := agValidateDecisionResult(AGDecisionIntentResult{Status: status}); (got == "") != ok {
			t.Errorf("status %q: got %q", status, got)
		}
	}
}

type agDecisionIntentOut struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Message        string `json:"message"`
	ActionID       string `json:"action_id"`
	CardRevision   int32  `json:"card_revision"`
	Note           string `json:"note"`
	OverrideReason string `json:"override_reason"`
}

type agDecisionClaimOut struct {
	Intent *agDecisionIntentOut `json:"intent"`
	Card   *struct {
		ID       string `json:"id"`
		Revision int32  `json:"revision"`
	} `json:"card"`
	CardOpen bool `json:"card_open"`
}

// agSeedActionCard stores one card offering `park`, returning its id.
func agSeedActionCard(t *testing.T, title string) string {
	t.Helper()
	body := `{"cards":[{"dedupe_key":"k","waiting_on":"alan","card_type":"bot_pr","content":{"title":"` + title + `","actions":[{"id":"park"}]}}]}`
	if w := agPutCards(t, body); w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body)
	}
	var id string
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM ag_decision_card WHERE workspace_id = $1 AND dedupe_key = 'k'`, testWorkspaceID).Scan(&id); err != nil {
		t.Fatalf("read card: %v", err)
	}
	return id
}

func agPostIntent(t *testing.T, cardID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequestAs(testUserID, http.MethodPost, "/api/ag/decisions/"+cardID+"/intents", json.RawMessage(body))
	req = withURLParam(req, "id", cardID)
	w := httptest.NewRecorder()
	testHandler.CreateAGDecisionIntent(w, req)
	return w
}

func agClaim(t *testing.T) agDecisionClaimOut {
	t.Helper()
	var out agDecisionClaimOut
	req := newRequestAs(testUserID, http.MethodPost, "/api/ag/decisions/intents/claim", nil)
	testutil.Call(t, testHandler.ClaimAGDecisionIntent, req).Want(http.StatusOK).JSON(&out)
	return out
}

func agFinish(t *testing.T, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequestAs(testUserID, http.MethodPost, "/api/ag/decisions/intents/"+id+"/result", json.RawMessage(body))
	req = withURLParam(req, "id", id)
	w := httptest.NewRecorder()
	testHandler.FinishAGDecisionIntent(w, req)
	return w
}

func TestAGPatchDecisionIntentGuards(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agDecisionsCleanup(t)
	cardID := agSeedActionCard(t, "A")

	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"stale revision":     {`{"revision":99,"action_id":"park"}`, http.StatusConflict},
		"action not offered": {`{"revision":1,"action_id":"merge"}`, http.StatusBadRequest},
		"bad json":           {`[]`, http.StatusBadRequest},
	} {
		if w := agPostIntent(t, cardID, tc.body); w.Code != tc.want {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := agPostIntent(t, "00000000-0000-4000-8000-000000000001", `{"revision":1,"action_id":"park"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown card: %d %s", w.Code, w.Body)
	}

	if w := agPostIntent(t, cardID, `{"revision":1,"action_id":"park","note":"n","override_reason":"Too big"}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	// One active intent per card, so a double click cannot act twice.
	if w := agPostIntent(t, cardID, `{"revision":1,"action_id":"park"}`); w.Code != http.StatusConflict {
		t.Fatalf("second intent: %d %s", w.Code, w.Body)
	}

	// A card the engine stopped sending takes no new intent.
	if w := agPutCards(t, `{"cards":[]}`); w.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", w.Code, w.Body)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE ag_decision_intent SET status = 'cancelled' WHERE card_id = $1`, cardID); err != nil {
		t.Fatal(err)
	}
	if w := agPostIntent(t, cardID, `{"revision":2,"action_id":"park"}`); w.Code != http.StatusConflict {
		t.Fatalf("resolved card: %d %s", w.Code, w.Body)
	}
}

func TestAGPatchDecisionIntentLifecycle(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agDecisionsCleanup(t)
	ctx := context.Background()
	cardID := agSeedActionCard(t, "A")

	if got := agClaim(t); got.Intent != nil || got.Card != nil {
		t.Fatalf("nothing queued, but claimed %+v", got)
	}
	var seen *string
	if err := testPool.QueryRow(ctx, `SELECT worker_seen_at::text FROM ag_decision_engine WHERE workspace_id = $1`, testWorkspaceID).Scan(&seen); err != nil || seen == nil {
		t.Fatalf("a claim should count as the worker's heartbeat: %v %v", seen, err)
	}

	if w := agPostIntent(t, cardID, `{"revision":1,"action_id":"park","note":"later","override_reason":"Too big"}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	claim := agClaim(t)
	if claim.Intent == nil || claim.Card == nil || claim.Intent.Status != "running" || claim.Intent.Note != "later" || claim.Intent.OverrideReason != "Too big" || claim.Card.ID != cardID || !claim.CardOpen {
		t.Fatalf("claim: %+v", claim)
	}
	if again := agClaim(t); again.Intent != nil {
		t.Fatalf("claimed twice: %+v", again)
	}
	// A running intent cannot be withdrawn.
	cancel := newRequestAs(testUserID, http.MethodDelete, "/api/ag/decisions/intents/"+claim.Intent.ID, nil)
	cancelled := httptest.NewRecorder()
	testHandler.CancelAGDecisionIntent(cancelled, withURLParam(cancel, "id", claim.Intent.ID))
	if cancelled.Code != http.StatusConflict {
		t.Fatalf("cancel running: %d %s", cancelled.Code, cancelled.Body)
	}

	// A failed record leaves the intent running, so the report can be repeated.
	if w := agFinish(t, claim.Intent.ID, `{"status":"succeeded","record":{"id":"","at":"2026-11-05T12:00:00Z","actor":"alan","action":"park","record":{}}}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad record: %d %s", w.Code, w.Body)
	}
	good := `{"status":"succeeded","message":"Parked","record":{"id":"intent:1","at":"2026-11-05T12:00:00Z","actor":"alan","action":"park","record":{"outcome":"parked"}}}`
	if w := agFinish(t, claim.Intent.ID, good); w.Code != http.StatusNoContent {
		t.Fatalf("finish: %d %s", w.Code, w.Body)
	}
	if w := agFinish(t, claim.Intent.ID, good); w.Code != http.StatusConflict {
		t.Fatalf("second report: %d %s", w.Code, w.Body)
	}
	var records int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM ag_decision_record WHERE workspace_id = $1 AND id = 'intent:1'`, testWorkspaceID).Scan(&records); err != nil || records != 1 {
		t.Fatalf("records = %d, %v", records, err)
	}

	// A success clears the card without waiting for the next push.
	listed := agListDecisions(t)
	if len(listed.Cards) != 0 || listed.WorkerSeenAt == nil {
		t.Fatalf("listed: %+v", listed)
	}
}

type agListedDecisions struct {
	Cards []struct {
		ID           string               `json:"id"`
		Revision     int64                `json:"revision"`
		LatestIntent *agDecisionIntentOut `json:"latest_intent"`
	} `json:"cards"`
	WorkerSeenAt *string `json:"worker_seen_at"`
}

func agListDecisions(t *testing.T) agListedDecisions {
	t.Helper()
	var listed agListedDecisions
	req := newRequestAs(testUserID, http.MethodGet, "/api/ag/decisions/", nil)
	testutil.Call(t, testHandler.ListAGDecisions, req).Want(http.StatusOK).JSON(&listed)
	return listed
}

func TestAGPatchDecisionResultClearsOnlyOnSuccess(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	finish := func(t *testing.T, cardID, status string) {
		t.Helper()
		if w := agPostIntent(t, cardID, `{"revision":1,"action_id":"park"}`); w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body)
		}
		claim := agClaim(t)
		if claim.Intent == nil {
			t.Fatal("nothing to claim")
		}
		if w := agFinish(t, claim.Intent.ID, `{"status":"`+status+`","message":"Why"}`); w.Code != http.StatusNoContent {
			t.Fatalf("finish: %d %s", w.Code, w.Body)
		}
	}
	for _, status := range []string{"refused", "failed"} {
		t.Run("AG patch: "+status+" keeps the card and shows why", func(t *testing.T) {
			agDecisionsCleanup(t)
			finish(t, agSeedActionCard(t, "A"), status)
			listed := agListDecisions(t)
			if len(listed.Cards) != 1 || listed.Cards[0].LatestIntent == nil || listed.Cards[0].LatestIntent.Status != status || listed.Cards[0].LatestIntent.Message != "Why" {
				t.Fatalf("listed: %+v", listed)
			}
		})
	}
	t.Run("AG patch: a card cleared by success comes back if the engine still sends it", func(t *testing.T) {
		agDecisionsCleanup(t)
		cardID := agSeedActionCard(t, "A")
		finish(t, cardID, "succeeded")
		if listed := agListDecisions(t); len(listed.Cards) != 0 {
			t.Fatalf("listed after success: %+v", listed)
		}
		if again := agSeedActionCard(t, "A"); again != cardID {
			t.Fatalf("card %s came back as %s", cardID, again)
		}
		listed := agListDecisions(t)
		if len(listed.Cards) != 1 || listed.Cards[0].Revision != 2 {
			t.Fatalf("listed after the next push: %+v", listed)
		}
	})
}

func TestAGPatchDecisionIntentCancelAndTimeouts(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agDecisionsCleanup(t)
	ctx := context.Background()
	cardID := agSeedActionCard(t, "A")
	create := func() string {
		w := agPostIntent(t, cardID, `{"revision":1,"action_id":"park"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body)
		}
		var out agDecisionIntentOut
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.ID
	}
	status := func(id string) (string, string) {
		var s, m string
		if err := testPool.QueryRow(ctx, `SELECT status, message FROM ag_decision_intent WHERE id = $1`, id).Scan(&s, &m); err != nil {
			t.Fatal(err)
		}
		return s, m
	}

	// A withdrawn intent is never claimed, and frees the card.
	first := create()
	req := newRequestAs(testUserID, http.MethodDelete, "/api/ag/decisions/intents/"+first, nil)
	w := httptest.NewRecorder()
	testHandler.CancelAGDecisionIntent(w, withURLParam(req, "id", first))
	if w.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	if got := agClaim(t); got.Intent != nil {
		t.Fatalf("claimed a cancelled intent: %+v", got)
	}

	// One nobody picked up in time never runs.
	second := create()
	if _, err := testPool.Exec(ctx, `UPDATE ag_decision_intent SET expires_at = now() - interval '1 second' WHERE id = $1`, second); err != nil {
		t.Fatal(err)
	}
	if got := agClaim(t); got.Intent != nil {
		t.Fatalf("claimed an expired intent: %+v", got)
	}
	if s, _ := status(second); s != "expired" {
		t.Fatalf("expired intent is %s", s)
	}

	// A worker that died part way fails the intent, and it is not handed out again.
	third := create()
	if got := agClaim(t); got.Intent == nil || got.Intent.ID != third {
		t.Fatalf("claim: %+v", got)
	}
	if _, err := testPool.Exec(ctx, `UPDATE ag_decision_intent SET claimed_at = now() - interval '11 minutes' WHERE id = $1`, third); err != nil {
		t.Fatal(err)
	}
	if got := agClaim(t); got.Intent != nil {
		t.Fatalf("handed out a lost intent: %+v", got)
	}
	if s, m := status(third); s != "failed" || !strings.Contains(m, "check before retrying") {
		t.Fatalf("lost intent: %s %q", s, m)
	}
}
