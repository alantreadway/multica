package handler

// AG patch: live token rate. See package agtokenrate.

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/multica-ai/multica/server/internal/agtokenrate"
	"github.com/multica-ai/multica/server/internal/daemonws"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// agTokenRateBoard is swapped in tests.
var agTokenRateBoard = agtokenrate.Default

// agApplyTokenRate records a heartbeat's rate and publishes it to the
// workspace when agtokenrate.Board.Apply says so.
func (h *Handler) agApplyTokenRate(runtimeID, workspaceID string, report *protocol.DaemonTokenRateReport) {
	if report == nil || runtimeID == "" || workspaceID == "" {
		return
	}
	if !agtokenrate.Valid(report) {
		slog.Warn("ignoring malformed runtime token rate", "runtime_id", runtimeID)
		return
	}
	rate, publish := agTokenRateBoard.Apply(runtimeID, workspaceID, report, time.Now())
	if publish {
		h.publish(protocol.EventAGRuntimeTokenRate, workspaceID, "system", "", rate)
	}
}

// agApplyHTTPTokenRate is DaemonHeartbeat's hook.
func (h *Handler) agApplyHTTPTokenRate(rt db.AgentRuntime, report *protocol.DaemonTokenRateReport) {
	h.agApplyTokenRate(uuidToString(rt.ID), uuidToString(rt.WorkspaceID), report)
}

// agApplyWSTokenRate is HandleDaemonWSHeartbeat's hook. The report rides in ctx
// (see daemonws.handleHeartbeatFrame).
func (h *Handler) agApplyWSTokenRate(ctx context.Context, runtimeID string, lease *daemonws.RuntimeLease) {
	h.agApplyTokenRate(runtimeID, lease.Snapshot().WorkspaceID, agtokenrate.ReportFrom(ctx))
}

// ListAGTokenRates serves the workspace's current rates, so a page that loads
// between heartbeats does not wait up to 15s for its first event.
func (h *Handler) ListAGTokenRates(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	rates := agTokenRateBoard.List(workspaceID, time.Now())
	sort.Slice(rates, func(i, j int) bool { return rates[i].RuntimeID < rates[j].RuntimeID })
	writeJSON(w, http.StatusOK, rates)
}
