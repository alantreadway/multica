package handler

// AG patch: label ordering. PUT /api/labels/order rewrites the order of one
// (workspace, resource type) label namespace; the position column and the
// position-aware list order live in label.go and issue_label.sql. See
// ag-patches/PATCHES.md.

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type ReorderLabelsRequest struct {
	ResourceType string   `json:"resource_type"`
	LabelIDs     []string `json:"label_ids"`
}

// ReorderLabels rewrites the order of one (workspace, resource type) label
// namespace. The request must list exactly the namespace's current labels:
// anything else (unknown or foreign ids, duplicates, a label created or deleted
// since the client last fetched) fails with nothing written, so a stale client
// rolls back and refetches instead of silently dropping someone else's change.
func (h *Handler) ReorderLabels(w http.ResponseWriter, r *http.Request) {
	var req ReorderLabelsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	resourceType, err := parseLabelResourceType(req.ResourceType)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	ids := make([]pgtype.UUID, len(req.LabelIDs))
	seen := make(map[string]struct{}, len(req.LabelIDs))
	for i, raw := range req.LabelIDs {
		id, ok := parseUUIDOrBadRequest(w, raw, "label_ids")
		if !ok {
			return
		}
		key := uuidToString(id)
		if _, dup := seen[key]; dup {
			writeError(w, http.StatusBadRequest, "label_ids must not contain duplicates")
			return
		}
		seen[key] = struct{}{}
		ids[i] = id
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	current, err := qtx.LockLabelIDsForReorder(r.Context(), db.LockLabelIDsForReorderParams{
		WorkspaceID: wsUUID, ResourceType: resourceType,
	})
	if err != nil {
		slog.Warn("ReorderLabels lock failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to reorder labels")
		return
	}
	if len(current) != len(ids) {
		writeError(w, http.StatusConflict, "label list is out of date")
		return
	}
	for _, id := range current {
		if _, ok := seen[uuidToString(id)]; !ok {
			writeError(w, http.StatusConflict, "label list is out of date")
			return
		}
	}

	if err := qtx.SetLabelPositions(r.Context(), db.SetLabelPositionsParams{
		WorkspaceID: wsUUID, ResourceType: resourceType, LabelIds: ids,
	}); err != nil {
		slog.Warn("ReorderLabels update failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to reorder labels")
		return
	}
	labels, err := qtx.ListLabels(r.Context(), db.ListLabelsParams{
		WorkspaceID: wsUUID, ResourceType: resourceType,
	})
	if err != nil {
		slog.Warn("ReorderLabels list failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to reorder labels")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit label order")
		return
	}

	resp := make([]LabelResponse, len(labels))
	for i, label := range labels {
		resp[i] = labelListRowToResponse(label)
	}
	h.publish(protocol.EventLabelUpdated, workspaceID, "member", userID, map[string]any{"resource_type": resourceType, "labels": resp})
	writeJSON(w, http.StatusOK, map[string]any{"labels": resp, "total": len(resp)})
}
