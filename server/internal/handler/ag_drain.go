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

// agApplyDrainReport stores a heartbeat's drain report. memo is what the caller
// knows of the stored state: the runtime row on the HTTP path, or the
// connection lease on the WebSocket path (lease non-nil, so the new memo is
// kept). A failure is logged and never fails the heartbeat: the badge is
// advisory and the next heartbeat retries.
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
		// The web client refetches the runtime list on this event.
		h.publish(protocol.EventDaemonRegister, workspaceID, "system", "", map[string]any{
			"action": "drain",
		})
	}
}
