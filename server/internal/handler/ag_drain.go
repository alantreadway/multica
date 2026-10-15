package handler

// AG patch: drain badge. See package agdrain for the write-only-on-change rules.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agdrain"
	"github.com/multica-ai/multica/server/internal/daemonws"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// agDrainStore adapts the sqlc queries to agdrain.Store.
type agDrainStore struct{ q *db.Queries }

func (s agDrainStore) SetAGDrain(ctx context.Context, id pgtype.UUID, state []byte) (int64, error) {
	return s.q.SetAgentRuntimeAGDrain(ctx, db.SetAgentRuntimeAGDrainParams{ID: id, Drain: state})
}

func (s agDrainStore) ClearAGDrain(ctx context.Context, id pgtype.UUID) (int64, error) {
	return s.q.ClearAgentRuntimeAGDrain(ctx, id)
}

// agApplyDrainReport stores a heartbeat's drain report; memo is the caller's
// view of the stored state. A failure is logged and never fails the heartbeat:
// the next one retries. A non-nil lease keeps the new memo (WS path).
func (h *Handler) agApplyDrainReport(ctx context.Context, runtimeID pgtype.UUID, workspaceID string, memo agdrain.Memo, lease *daemonws.RuntimeLease, report *protocol.DaemonDrainReport) {
	if report == nil {
		return
	}
	next, changed, err := agdrain.Apply(ctx, agDrainStore{h.Queries}, runtimeID, memo, report, time.Now())
	if err != nil {
		slog.Warn("store runtime drain state failed", "runtime_id", uuidToString(runtimeID), "error", err)
		return
	}
	if lease != nil {
		lease.SetAGDrain(next)
	}
	if changed && workspaceID != "" {
		h.PublishRuntimeRefresh(workspaceID, "system", "", "drain")
	}
}
