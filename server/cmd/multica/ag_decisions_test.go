package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

type agDecisionsCall struct {
	method string
	path   string
	body   string
}

func agDecisionsServer(t *testing.T, failPath string) (*cli.APIClient, *[]agDecisionsCall) {
	t.Helper()
	calls := &[]agDecisionsCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, agDecisionsCall{r.Method, r.URL.Path, string(body)})
		if r.URL.Path == failPath {
			http.Error(w, `{"error":"nope"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	t.Cleanup(srv.Close)
	return cli.NewAPIClient(srv.URL, "ws-1", "tok"), calls
}

func TestAGPatchDecisionsPushSendsRecordsThenCards(t *testing.T) {
	client, calls := agDecisionsServer(t, "")
	raw := `{"cards":[{"dedupe_key":"a"}],"records":[{"id":"r1"}]}`
	if err := pushAGDecisions(context.Background(), client, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %+v", *calls)
	}
	first, second := (*calls)[0], (*calls)[1]
	if first.method != http.MethodPost || first.path != "/api/ag/decisions/records" || !strings.Contains(first.body, `"r1"`) {
		t.Errorf("first call = %+v", first)
	}
	if second.method != http.MethodPut || second.path != "/api/ag/decisions/cards" || !strings.Contains(second.body, `"dedupe_key":"a"`) {
		t.Errorf("second call = %+v", second)
	}
}

func TestAGPatchDecisionsPushSendsAnEmptySnapshotWithoutRecords(t *testing.T) {
	client, calls := agDecisionsServer(t, "")
	if err := pushAGDecisions(context.Background(), client, []byte(`{"cards":[]}`)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].path != "/api/ag/decisions/cards" || !strings.Contains((*calls)[0].body, `"cards":[]`) {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestAGPatchDecisionsPushRefusesInputThatWouldResolveEveryCard(t *testing.T) {
	for _, raw := range []string{`{}`, `{"cards":null}`, `not json`, ``} {
		client, calls := agDecisionsServer(t, "")
		if err := pushAGDecisions(context.Background(), client, []byte(raw)); err == nil {
			t.Errorf("%q: expected an error", raw)
		}
		if len(*calls) != 0 {
			t.Errorf("%q: sent %+v", raw, *calls)
		}
	}
}

func TestAGPatchDecisionsPushStopsWhenRecordsFail(t *testing.T) {
	client, calls := agDecisionsServer(t, "/api/ag/decisions/records")
	raw := `{"cards":[],"records":[{"id":"r1"}]}`
	if err := pushAGDecisions(context.Background(), client, []byte(raw)); err == nil {
		t.Fatal("expected an error")
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestAGPatchDecisionsClaimPrintsTheServersAnswer(t *testing.T) {
	client, calls := agDecisionsServer(t, "")
	var out strings.Builder
	if err := claimAGDecision(context.Background(), client, &out); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].method != http.MethodPost || (*calls)[0].path != "/api/ag/decisions/intents/claim" {
		t.Fatalf("calls = %+v", *calls)
	}
	if !strings.HasPrefix(out.String(), "{") {
		t.Fatalf("printed %q", out.String())
	}
}

func TestAGPatchDecisionsResultPostsTheReport(t *testing.T) {
	client, calls := agDecisionsServer(t, "")
	raw := `{"status":"failed","message":"no"}`
	if err := reportAGDecision(context.Background(), client, "i-1", []byte(raw)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].path != "/api/ag/decisions/intents/i-1/result" || !strings.Contains((*calls)[0].body, `"failed"`) {
		t.Fatalf("calls = %+v", *calls)
	}
	if err := reportAGDecision(context.Background(), client, "i-1", []byte("not json")); err == nil {
		t.Fatal("bad stdin should fail before any request")
	}
}

func TestAGPatchDecisionsResultSurfacesAConflict(t *testing.T) {
	client, _ := agDecisionsServer(t, "/api/ag/decisions/intents/i-1/result")
	if err := reportAGDecision(context.Background(), client, "i-1", []byte(`{"status":"failed"}`)); err == nil {
		t.Fatal("a refused report should be an error")
	}
}
