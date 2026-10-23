package handler

// AG patch: Claude quota report. See package agquota for the write-only-on-change
// rules.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agquota"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// agQuotaStore adapts the sqlc query to agquota.Store.
type agQuotaStore struct{ q *db.Queries }

func (s agQuotaStore) SetAGQuota(ctx context.Context, id pgtype.UUID, report []byte) (bool, []byte, error) {
	row, err := s.q.SetAgentRuntimeAGQuota(ctx, db.SetAgentRuntimeAGQuotaParams{ID: id, Quota: report})
	if err != nil {
		return false, nil, err
	}
	return row.Changed > 0, row.AgSettings, nil
}

// agApplyQuotaReport stores a heartbeat's quota report and returns the memo to
// keep. A malformed report is logged and not written; a store failure is logged
// and never fails the heartbeat (the next one retries). A non-nil lease keeps the
// new memo (WS path).
func (h *Handler) agApplyQuotaReport(ctx context.Context, runtimeID pgtype.UUID, workspaceID string, memo agquota.Memo, lease *daemonws.RuntimeLease, report *protocol.DaemonQuotaReport) agquota.Memo {
	if report == nil {
		return memo
	}
	if !agquota.Valid(report) {
		slog.Warn("ignoring malformed runtime quota report", "runtime_id", uuidToString(runtimeID), "account", report.Account)
		return memo
	}
	next, changed, err := agquota.Apply(ctx, agQuotaStore{h.Queries}, runtimeID, memo, report, time.Now())
	if err != nil {
		slog.Warn("store runtime quota report failed", "runtime_id", uuidToString(runtimeID), "error", err)
		return memo
	}
	if lease != nil {
		lease.SetAGQuota(next)
	}
	// A write that only moved observed_at is not worth a workspace-wide refetch.
	if changed && workspaceID != "" && agquota.Material(memo, next.Report) {
		h.PublishRuntimeRefresh(workspaceID, "system", "", "quota")
	}
	return next
}

// agApplyHTTPQuotaReport is DaemonHeartbeat's hook. The HTTP path has no lease,
// so the memo comes from the runtime row the handler just loaded.
func (h *Handler) agApplyHTTPQuotaReport(ctx context.Context, rt db.AgentRuntime, report *protocol.DaemonQuotaReport) {
	if report == nil {
		return
	}
	h.agApplyQuotaReport(ctx, rt.ID, uuidToString(rt.WorkspaceID), agquota.MemoFromMetadata(rt.Metadata, time.Now()), nil, report)
}

// agApplyWSQuotaReport is HandleDaemonWSHeartbeat's hook. The report rides in
// ctx (see daemonws.handleHeartbeatFrame) and the memo on the lease.
func (h *Handler) agApplyWSQuotaReport(ctx context.Context, runtimeID string, lease *daemonws.RuntimeLease) {
	report := agquota.ReportFrom(ctx)
	if report == nil {
		return
	}
	if runtimeUUID, err := util.ParseUUID(runtimeID); err == nil {
		h.agApplyQuotaReport(ctx, runtimeUUID, lease.Snapshot().WorkspaceID, lease.AGQuota(), lease, report)
	}
}

// agAckSettingsFromLease puts the runtime setting cached on the lease into a
// WebSocket heartbeat ack.
func agAckSettingsFromLease(ack *protocol.DaemonHeartbeatAckPayload, lease *daemonws.RuntimeLease) {
	if ack != nil {
		ack.AGSettings = lease.AGQuota().Settings
	}
}

// agAddSettingsToHeartbeatResp puts the runtime's stored setting into an HTTP
// heartbeat response; a runtime with none gets no ag_settings key.
func agAddSettingsToHeartbeatResp(resp map[string]any, rt db.AgentRuntime) {
	if s := agquota.SettingsFromMetadata(rt.Metadata); s != nil {
		resp["ag_settings"] = s
	}
}
