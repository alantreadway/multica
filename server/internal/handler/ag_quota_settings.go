package handler

// AG patch: runtime backup setting. The UI stores it in
// agent_runtime.metadata["ag_settings"]; the heartbeat ack returns it to the daemon.

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/agquota"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type agRuntimeSettingsRequest struct {
	BackupEnabled       *bool `json:"backup_enabled"`
	BackupMaxConcurrent *int  `json:"backup_max_concurrent"`
}

// agRuntimeIsClaude reports whether the runtime speaks the Claude protocol.
func agRuntimeIsClaude(provider string) bool {
	if provider == "claude" {
		return true
	}
	family, _ := agent.RuntimeProtocolFamily(provider)
	return family == "claude"
}

// UpdateAGRuntimeSettings serves PUT /api/runtimes/{runtimeId}/ag-settings.
// Gated like any runtime edit (canEditRuntime); Claude runtimes only.
func (h *Handler) UpdateAGRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	runtimeUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "runtimeId"), "runtime_id")
	if !ok {
		return
	}
	rt, err := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	wsID := uuidToString(rt.WorkspaceID)
	member, ok := h.requireWorkspaceMember(w, r, wsID, "runtime not found")
	if !ok {
		return
	}
	if !canEditRuntime(member, rt) {
		writeError(w, http.StatusForbidden, "you can only edit your own runtimes")
		return
	}
	if !agRuntimeIsClaude(rt.Provider) {
		writeError(w, http.StatusBadRequest, "backup account settings apply to Claude runtimes only")
		return
	}

	var req agRuntimeSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.BackupEnabled == nil {
		writeError(w, http.StatusBadRequest, "backup_enabled is required")
		return
	}
	settings := protocol.AGRuntimeSettings{BackupEnabled: *req.BackupEnabled, BackupMaxConcurrent: agquota.DefaultBackupMaxConcurrent}
	if req.BackupMaxConcurrent != nil {
		settings.BackupMaxConcurrent = *req.BackupMaxConcurrent
	}
	if settings.BackupMaxConcurrent < 1 || settings.BackupMaxConcurrent > agquota.MaxBackupMaxConcurrent {
		writeError(w, http.StatusBadRequest, "backup_max_concurrent must be between 1 and 100")
		return
	}

	payload, err := json.Marshal(settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode settings")
		return
	}
	rows, err := h.Queries.SetAgentRuntimeAGSettings(r.Context(), db.SetAgentRuntimeAGSettingsParams{ID: rt.ID, Settings: payload})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save settings")
		return
	}
	if rows > 0 {
		h.PublishRuntimeRefresh(wsID, "member", uuidToString(member.UserID), "ag_settings")
	}
	writeJSON(w, http.StatusOK, settings)
}
