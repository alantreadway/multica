package handler

// AG patch: Decision Desk. ag-jira-sync sends the cards and the decision log;
// this file stores and serves them and never touches GitHub or JIRA. See
// ag-patches/DECISION-DESK-DESIGN.md.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	agDecisionMaxCards      = 500
	agDecisionMaxRecords    = 200
	agDecisionMaxKeyLen     = 200
	agDecisionMaxJSONBytes  = 64 * 1024
	agDecisionDefaultRecLim = 100
	agDecisionMaxRecLim     = 500
	agDecisionWaitingOnAlan = "alan"
)

// AGDecisionCardInput is one card in the engine's complete snapshot.
type AGDecisionCardInput struct {
	DedupeKey string          `json:"dedupe_key"`
	IssueID   string          `json:"issue_id"`
	WaitingOn string          `json:"waiting_on"`
	CardType  string          `json:"card_type"`
	HeadSHA   string          `json:"head_sha"`
	Content   json.RawMessage `json:"content"`
}

// AGDecisionCardsRequest replaces the open set: a card left out is resolved.
type AGDecisionCardsRequest struct {
	Cards []AGDecisionCardInput `json:"cards"`
}

// AGDecisionRecordInput is one decision to append to the log.
type AGDecisionRecordInput struct {
	ID     string          `json:"id"`
	At     string          `json:"at"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Record json.RawMessage `json:"record"`
}

// AGDecisionRecordsRequest appends to the log. Retrying one is safe.
type AGDecisionRecordsRequest struct {
	Records []AGDecisionRecordInput `json:"records"`
}

type agDecisionCardResponse struct {
	ID          string          `json:"id"`
	DedupeKey   string          `json:"dedupe_key"`
	IssueID     *string         `json:"issue_id"`
	WaitingOn   string          `json:"waiting_on"`
	CardType    string          `json:"card_type"`
	Revision    int32           `json:"revision"`
	HeadSHA     *string         `json:"head_sha"`
	Content     json.RawMessage `json:"content"`
	FirstSeenAt string          `json:"first_seen_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type agDecisionRecordResponse struct {
	ID     string          `json:"id"`
	At     string          `json:"at"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Record json.RawMessage `json:"record"`
}

// agJSONObject reports whether raw is a JSON object within the size limit.
func agJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > agDecisionMaxJSONBytes {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

// agValidateDecisionCards returns what is wrong with a snapshot, or "". Issue
// ids are checked separately, against the workspace.
func agValidateDecisionCards(cards []AGDecisionCardInput) string {
	if len(cards) > agDecisionMaxCards {
		return fmt.Sprintf("at most %d cards", agDecisionMaxCards)
	}
	seen := make(map[string]struct{}, len(cards))
	for i, c := range cards {
		switch {
		case strings.TrimSpace(c.DedupeKey) == "" || len(c.DedupeKey) > agDecisionMaxKeyLen:
			return fmt.Sprintf("cards[%d].dedupe_key is invalid", i)
		case strings.TrimSpace(c.WaitingOn) == "" || len(c.WaitingOn) > agDecisionMaxKeyLen:
			return fmt.Sprintf("cards[%d].waiting_on is invalid", i)
		case strings.TrimSpace(c.CardType) == "" || len(c.CardType) > agDecisionMaxKeyLen:
			return fmt.Sprintf("cards[%d].card_type is invalid", i)
		case !agJSONObject(c.Content):
			return fmt.Sprintf("cards[%d].content must be a JSON object of at most %d bytes", i, agDecisionMaxJSONBytes)
		}
		if _, dup := seen[c.DedupeKey]; dup {
			return fmt.Sprintf("cards[%d].dedupe_key is a duplicate", i)
		}
		seen[c.DedupeKey] = struct{}{}
	}
	return ""
}

// agValidateDecisionRecords returns what is wrong with a batch, or "".
func agValidateDecisionRecords(records []AGDecisionRecordInput) string {
	if len(records) > agDecisionMaxRecords {
		return fmt.Sprintf("at most %d records", agDecisionMaxRecords)
	}
	for i, rec := range records {
		_, err := time.Parse(time.RFC3339, rec.At)
		switch {
		case strings.TrimSpace(rec.ID) == "" || len(rec.ID) > agDecisionMaxKeyLen:
			return fmt.Sprintf("records[%d].id is invalid", i)
		case err != nil:
			return fmt.Sprintf("records[%d].at must be RFC 3339", i)
		case strings.TrimSpace(rec.Actor) == "" || len(rec.Actor) > agDecisionMaxKeyLen:
			return fmt.Sprintf("records[%d].actor is invalid", i)
		case strings.TrimSpace(rec.Action) == "" || len(rec.Action) > agDecisionMaxKeyLen:
			return fmt.Sprintf("records[%d].action is invalid", i)
		case !agJSONObject(rec.Record):
			return fmt.Sprintf("records[%d].record must be a JSON object of at most %d bytes", i, agDecisionMaxJSONBytes)
		}
	}
	return ""
}

// ListAGDecisions serves the open cards and when the engine last reported.
func (h *Handler) ListAGDecisions(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListOpenAGDecisionCards(r.Context(), wsUUID)
	if err != nil {
		slog.Warn("ListAGDecisions failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list decisions")
		return
	}
	lastSync, err := h.Queries.GetAGDecisionEngineLastSync(r.Context(), wsUUID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("ListAGDecisions last sync failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list decisions")
		return
	}
	cards := make([]agDecisionCardResponse, 0, len(rows))
	for _, row := range rows {
		cards = append(cards, agDecisionCardResponse{
			ID:          uuidToString(row.ID),
			DedupeKey:   row.DedupeKey,
			IssueID:     uuidToPtr(row.IssueID),
			WaitingOn:   row.WaitingOn,
			CardType:    row.CardType,
			Revision:    row.Revision,
			HeadSHA:     textToPtr(row.HeadSha),
			Content:     row.Content,
			FirstSeenAt: timestampToString(row.FirstSeenAt),
			UpdatedAt:   timestampToString(row.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"cards": cards, "last_sync_at": timestampToPtr(lastSync)})
}

// CountAGDecisions serves the sidebar badge: cards waiting on Alan.
func (h *Handler) CountAGDecisions(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	count, err := h.Queries.CountAGDecisionCardsWaitingOn(r.Context(), db.CountAGDecisionCardsWaitingOnParams{
		WorkspaceID: wsUUID, WaitingOn: agDecisionWaitingOnAlan,
	})
	if err != nil {
		slog.Warn("CountAGDecisions failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to count decisions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"count": count})
}

// PutAGDecisionCards takes the engine's complete snapshot in one transaction:
// a failed write leaves the previous cards standing.
func (h *Handler) PutAGDecisionCards(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var req AGDecisionCardsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := agValidateDecisionCards(req.Cards); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	keys := make([]string, 0, len(req.Cards))
	issueIDs := make([]pgtype.UUID, len(req.Cards))
	wanted := make([]pgtype.UUID, 0, len(req.Cards))
	for i, c := range req.Cards {
		keys = append(keys, c.DedupeKey)
		if c.IssueID == "" {
			continue
		}
		id, ok := parseUUIDOrBadRequest(w, c.IssueID, fmt.Sprintf("cards[%d].issue_id", i))
		if !ok {
			return
		}
		issueIDs[i] = id
		wanted = append(wanted, id)
	}
	known, err := h.Queries.ListIssueIDsInWorkspace(r.Context(), db.ListIssueIDsInWorkspaceParams{WorkspaceID: wsUUID, IssueIds: wanted})
	if err != nil {
		slog.Warn("PutAGDecisionCards issue check failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to save decisions")
		return
	}
	inWorkspace := make(map[string]struct{}, len(known))
	for _, id := range known {
		inWorkspace[uuidToString(id)] = struct{}{}
	}
	for _, id := range wanted {
		if _, ok := inWorkspace[uuidToString(id)]; !ok {
			writeError(w, http.StatusBadRequest, "issue_id is not in this workspace")
			return
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	changed := false
	for i, c := range req.Cards {
		row, err := qtx.UpsertAGDecisionCard(r.Context(), db.UpsertAGDecisionCardParams{
			WorkspaceID: wsUUID,
			DedupeKey:   c.DedupeKey,
			IssueID:     issueIDs[i],
			WaitingOn:   c.WaitingOn,
			CardType:    c.CardType,
			HeadSha:     strToText(c.HeadSHA),
			Content:     c.Content,
		})
		if err != nil {
			slog.Warn("PutAGDecisionCards upsert failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to save decisions")
			return
		}
		changed = changed || row.Changed
	}
	resolved, err := qtx.ResolveAGDecisionCardsNotIn(r.Context(), db.ResolveAGDecisionCardsNotInParams{WorkspaceID: wsUUID, DedupeKeys: keys})
	if err != nil {
		slog.Warn("PutAGDecisionCards resolve failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to save decisions")
		return
	}
	if err := qtx.TouchAGDecisionEngine(r.Context(), wsUUID); err != nil {
		slog.Warn("PutAGDecisionCards heartbeat failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to save decisions")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save decisions")
		return
	}
	// A heartbeat alone publishes nothing: the page's staleness check reads the
	// timestamp from its periodic refetch.
	changed = changed || resolved > 0
	if changed {
		h.publish(protocol.EventAGDecisionChanged, workspaceID, "system", "", map[string]any{})
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": changed, "resolved": resolved})
}

// PostAGDecisionRecords appends to the decision log. Nothing here updates or
// deletes a record.
func (h *Handler) PostAGDecisionRecords(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var req AGDecisionRecordsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := agValidateDecisionRecords(req.Records); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	var inserted int64
	for _, rec := range req.Records {
		at, _ := time.Parse(time.RFC3339, rec.At) // validated above
		n, err := h.Queries.InsertAGDecisionRecord(r.Context(), db.InsertAGDecisionRecordParams{
			ID:          rec.ID,
			WorkspaceID: wsUUID,
			At:          pgtype.Timestamptz{Time: at, Valid: true},
			Actor:       rec.Actor,
			Action:      rec.Action,
			Record:      rec.Record,
		})
		if err != nil {
			slog.Warn("PostAGDecisionRecords failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to save records")
			return
		}
		inserted += n
	}
	writeJSON(w, http.StatusOK, map[string]int64{"inserted": inserted})
}

// ListAGDecisionRecords serves the newest decisions first.
func (h *Handler) ListAGDecisionRecords(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	limit := agDecisionDefaultRecLim
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > agDecisionMaxRecLim {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", agDecisionMaxRecLim))
			return
		}
		limit = n
	}
	rows, err := h.Queries.ListAGDecisionRecords(r.Context(), db.ListAGDecisionRecordsParams{WorkspaceID: wsUUID, Limit: int32(limit)})
	if err != nil {
		slog.Warn("ListAGDecisionRecords failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list records")
		return
	}
	records := make([]agDecisionRecordResponse, 0, len(rows))
	for _, row := range rows {
		records = append(records, agDecisionRecordResponse{
			ID: row.ID, At: timestampToString(row.At), Actor: row.Actor, Action: row.Action, Record: row.Record,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records})
}
