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
	if changed && workspaceID != "" {
		h.PublishRuntimeRefresh(workspaceID, "system", "", "quota")
	}
	return next
}
