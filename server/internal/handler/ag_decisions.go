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

	"github.com/go-chi/chi/v5"
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
	agDecisionMaxNoteBytes  = 8 * 1024
	agDecisionMaxReasonLen  = 200
	agDecisionMaxMessageLen = 2000
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
	// The card's newest intent, whatever its status. Absent until the first action.
	LatestIntent *agDecisionIntentResponse `json:"latest_intent,omitempty"`
}

type agDecisionIntentResponse struct {
	ID             string  `json:"id"`
	CardID         string  `json:"card_id"`
	CardRevision   int32   `json:"card_revision"`
	ActionID       string  `json:"action_id"`
	Note           string  `json:"note"`
	OverrideReason string  `json:"override_reason"`
	Status         string  `json:"status"`
	Message        string  `json:"message"`
	CreatedAt      string  `json:"created_at"`
	ExpiresAt      string  `json:"expires_at"`
	FinishedAt     *string `json:"finished_at"`
}

// AGDecisionIntentRequest asks for one of the actions a card offers. Revision is
// the one the page was showing.
type AGDecisionIntentRequest struct {
	Revision       int32  `json:"revision"`
	ActionID       string `json:"action_id"`
	Note           string `json:"note"`
	OverrideReason string `json:"override_reason"`
}

// AGDecisionIntentResult is the worker's report on a claimed intent. The record
// goes in with it, so a finished action cannot be missing from the log.
type AGDecisionIntentResult struct {
	Status  string                 `json:"status"`
	Message string                 `json:"message"`
	Record  *AGDecisionRecordInput `json:"record"`
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
	engine, err := h.Queries.GetAGDecisionEngine(r.Context(), wsUUID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("ListAGDecisions last sync failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list decisions")
		return
	}
	intents, err := h.Queries.ListLatestAGDecisionIntents(r.Context(), wsUUID)
	if err != nil {
		slog.Warn("ListAGDecisions intents failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list decisions")
		return
	}
	latest := make(map[string]*agDecisionIntentResponse, len(intents))
	for _, intent := range intents {
		response := agDecisionIntentToResponse(intent)
		latest[response.CardID] = &response
	}
	cards := make([]agDecisionCardResponse, 0, len(rows))
	for _, row := range rows {
		card := agDecisionCardToResponse(row)
		card.LatestIntent = latest[card.ID]
		cards = append(cards, card)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cards":          cards,
		"last_sync_at":   timestampToPtr(engine.LastSyncAt),
		"worker_seen_at": timestampToPtr(engine.WorkerSeenAt),
	})
}

func agDecisionCardToResponse(row db.AgDecisionCard) agDecisionCardResponse {
	return agDecisionCardResponse{
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
	}
}

func agDecisionIntentToResponse(row db.AgDecisionIntent) agDecisionIntentResponse {
	return agDecisionIntentResponse{
		ID:             uuidToString(row.ID),
		CardID:         uuidToString(row.CardID),
		CardRevision:   row.CardRevision,
		ActionID:       row.ActionID,
		Note:           row.Note,
		OverrideReason: row.OverrideReason,
		Status:         row.Status,
		Message:        row.Message,
		CreatedAt:      timestampToString(row.CreatedAt),
		ExpiresAt:      timestampToString(row.ExpiresAt),
		FinishedAt:     timestampToPtr(row.FinishedAt),
	}
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

// agDecisionOffersAction reports whether a card's content lists the action. The
// server knows nothing else about actions: the engine decides what a card offers
// and the worker decides what each one does.
func agDecisionOffersAction(content []byte, actionID string) bool {
	var card struct {
		Actions []struct {
			ID string `json:"id"`
		} `json:"actions"`
	}
	if json.Unmarshal(content, &card) != nil {
		return false
	}
	for _, action := range card.Actions {
		if action.ID == actionID {
			return true
		}
	}
	return false
}

// agValidateDecisionIntent returns what is wrong with a request, or "".
func agValidateDecisionIntent(req AGDecisionIntentRequest) string {
	switch {
	case strings.TrimSpace(req.ActionID) == "" || len(req.ActionID) > agDecisionMaxKeyLen:
		return "action_id is invalid"
	case len(req.Note) > agDecisionMaxNoteBytes:
		return fmt.Sprintf("note must be at most %d bytes", agDecisionMaxNoteBytes)
	case len(req.OverrideReason) > agDecisionMaxReasonLen:
		return fmt.Sprintf("override_reason must be at most %d bytes", agDecisionMaxReasonLen)
	}
	return ""
}

// agValidateDecisionResult returns what is wrong with a worker's report, or "".
func agValidateDecisionResult(req AGDecisionIntentResult) string {
	switch req.Status {
	case "succeeded", "failed", "refused":
	default:
		return "status must be succeeded, failed or refused"
	}
	if len(req.Message) > agDecisionMaxMessageLen {
		return fmt.Sprintf("message must be at most %d bytes", agDecisionMaxMessageLen)
	}
	if req.Record != nil {
		return agValidateDecisionRecords([]AGDecisionRecordInput{*req.Record})
	}
	return ""
}

// CreateAGDecisionIntent queues an action on the revision Alan was looking at.
func (h *Handler) CreateAGDecisionIntent(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	cardID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	actorID, ok := parseUUIDOrBadRequest(w, requestUserID(r), "user_id")
	if !ok {
		return
	}
	var req AGDecisionIntentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := agValidateDecisionIntent(req); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	card, err := h.Queries.GetAGDecisionCard(r.Context(), db.GetAGDecisionCardParams{ID: cardID, WorkspaceID: wsUUID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "card not found")
		return
	}
	if err != nil {
		slog.Warn("CreateAGDecisionIntent card read failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to queue the action")
		return
	}
	switch {
	case card.ResolvedAt.Valid:
		writeError(w, http.StatusConflict, "this card is no longer waiting")
		return
	case card.Revision != req.Revision:
		writeError(w, http.StatusConflict, "this card has changed; check it again")
		return
	case !agDecisionOffersAction(card.Content, req.ActionID):
		writeError(w, http.StatusBadRequest, "this card does not offer that action")
		return
	}
	intent, err := h.Queries.CreateAGDecisionIntent(r.Context(), db.CreateAGDecisionIntentParams{
		WorkspaceID:    wsUUID,
		CardID:         cardID,
		CardRevision:   req.Revision,
		ActionID:       req.ActionID,
		Note:           req.Note,
		OverrideReason: req.OverrideReason,
		ActorUserID:    actorID,
	})
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "an action is already queued on this card")
		return
	}
	if err != nil {
		slog.Warn("CreateAGDecisionIntent failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to queue the action")
		return
	}
	h.publish(protocol.EventAGDecisionChanged, workspaceID, "system", "", map[string]any{})
	writeJSON(w, http.StatusCreated, agDecisionIntentToResponse(intent))
}

// CancelAGDecisionIntent withdraws an action the worker has not started.
func (h *Handler) CancelAGDecisionIntent(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	n, err := h.Queries.CancelAGDecisionIntent(r.Context(), db.CancelAGDecisionIntentParams{ID: id, WorkspaceID: wsUUID})
	if err != nil {
		slog.Warn("CancelAGDecisionIntent failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to cancel the action")
		return
	}
	if n == 0 {
		writeError(w, http.StatusConflict, "this action has already started or finished")
		return
	}
	h.publish(protocol.EventAGDecisionChanged, workspaceID, "system", "", map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}

// ClaimAGDecisionIntent hands the worker the oldest live intent and its card, or
// a null intent. Every call also counts as the worker's heartbeat, and is what
// retires intents nobody picked up.
func (h *Handler) ClaimAGDecisionIntent(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	fail := func(step string, err error) {
		slog.Warn("ClaimAGDecisionIntent "+step+" failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to claim an action")
	}
	if err := h.Queries.TouchAGDecisionWorker(r.Context(), wsUUID); err != nil {
		fail("heartbeat", err)
		return
	}
	expired, err := h.Queries.ExpireAGDecisionIntents(r.Context(), wsUUID)
	if err != nil {
		fail("expiry", err)
		return
	}
	lost, err := h.Queries.FailLostAGDecisionIntents(r.Context(), wsUUID)
	if err != nil {
		fail("lost check", err)
		return
	}
	intent, err := h.Queries.ClaimAGDecisionIntent(r.Context(), wsUUID)
	claimed := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		fail("claim", err)
		return
	}
	if claimed || expired > 0 || lost > 0 {
		h.publish(protocol.EventAGDecisionChanged, workspaceID, "system", "", map[string]any{})
	}
	if !claimed {
		writeJSON(w, http.StatusOK, map[string]any{"intent": nil, "card": nil})
		return
	}
	card, err := h.Queries.GetAGDecisionCard(r.Context(), db.GetAGDecisionCardParams{ID: intent.CardID, WorkspaceID: wsUUID})
	if err != nil {
		// The intent stays running and the lost check retires it: nothing has acted on it.
		fail("card read", err)
		return
	}
	response := agDecisionCardToResponse(card)
	writeJSON(w, http.StatusOK, map[string]any{
		"intent":    agDecisionIntentToResponse(intent),
		"card":      response,
		"card_open": !card.ResolvedAt.Valid,
	})
}

// FinishAGDecisionIntent takes the worker's report and its log record in one
// transaction. Only a running intent can finish, so a late report changes nothing.
func (h *Handler) FinishAGDecisionIntent(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	var req AGDecisionIntentResult
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := agValidateDecisionResult(req); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	n, err := qtx.FinishAGDecisionIntent(r.Context(), db.FinishAGDecisionIntentParams{
		ID: id, WorkspaceID: wsUUID, Status: req.Status, Message: req.Message,
	})
	if err != nil {
		slog.Warn("FinishAGDecisionIntent failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to save the result")
		return
	}
	if n == 0 {
		writeError(w, http.StatusConflict, "this action is not running")
		return
	}
	// A pass can be minutes away, and a card that stays after its action worked
	// reads as if the click did nothing. A refusal or failure keeps the card and
	// its buttons.
	if req.Status == "succeeded" {
		if _, err := qtx.ResolveAGDecisionCardOfIntent(r.Context(), db.ResolveAGDecisionCardOfIntentParams{
			WorkspaceID: wsUUID, IntentID: id,
		}); err != nil {
			slog.Warn("FinishAGDecisionIntent resolve failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to save the result")
			return
		}
	}
	if rec := req.Record; rec != nil {
		at, _ := time.Parse(time.RFC3339, rec.At) // validated above
		if _, err := qtx.InsertAGDecisionRecord(r.Context(), db.InsertAGDecisionRecordParams{
			ID:          rec.ID,
			WorkspaceID: wsUUID,
			At:          pgtype.Timestamptz{Time: at, Valid: true},
			Actor:       rec.Actor,
			Action:      rec.Action,
			Record:      rec.Record,
		}); err != nil {
			slog.Warn("FinishAGDecisionIntent record failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to save the result")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save the result")
		return
	}
	h.publish(protocol.EventAGDecisionChanged, workspaceID, "system", "", map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}
