package handler

// AG patch: runtime pool. A daemon declares its pool on register (ag_pool, from
// MULTICA_AG_POOL) and the name is stored in agent_runtime.metadata. A claim by
// a pool peer of the agent's runtime rewrites the task's runtime_id to the
// claimer, so the delivery rechecks accept a task whose runtime is a peer of
// the agent's. See internal/service/ag_runtime_pool.go.
//
// The heartbeat ack of a pooled Claude runtime also carries ag_pool_pace: its
// online peers' active runs and claim slots, for the daemon's Claude pacing
// (agpool.Pace).

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/agpool"
	"github.com/multica-ai/multica/server/internal/agquota"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// agRegisterPoolMetadata adds the declared pool to a runtime's register
// metadata. Registration overwrites metadata wholesale, so a daemon restarted
// without a pool leaves it. An invalid name is dropped (the runtime stays out
// of any pool); the daemon refuses to start with one, so only a hand-made
// request gets here.
func agRegisterPoolMetadata(metadata []byte, pool string) []byte {
	name, ok := agpool.Normalize(pool)
	if !ok {
		slog.Warn("register: ignoring invalid ag_pool", "ag_pool", pool)
		return metadata
	}
	return agpool.WithMetadata(metadata, name)
}

// agRuntimePoolPeers reports whether taskRuntime is a pool peer of
// agentRuntime. A lookup error counts as no, which keeps the upstream refusal.
func agRuntimePoolPeers(ctx context.Context, q *db.Queries, agentRuntime, taskRuntime pgtype.UUID) bool {
	if !agentRuntime.Valid || !taskRuntime.Valid {
		return false
	}
	ok, err := q.AGRuntimesArePoolPeers(ctx, db.AGRuntimesArePoolPeersParams{RuntimeID: agentRuntime, OtherID: taskRuntime})
	if err != nil {
		slog.Warn("runtime pool: peer check failed", "error", err)
		return false
	}
	return ok
}

// agPoolPace builds a heartbeat ack's ag_pool_pace. Only a heartbeat with a
// quota report (a patched daemon's Claude runtime) asks; nil for a runtime
// outside a pool or on a failed lookup, and the daemon then paces on its own
// runs alone.
func (h *Handler) agPoolPace(ctx context.Context, runtimeID pgtype.UUID, report *protocol.DaemonQuotaReport) *protocol.AGPoolPace {
	if report == nil || !runtimeID.Valid {
		return nil
	}
	rows, err := h.Queries.ListAGPoolPaceRuntimes(ctx, db.ListAGPoolPaceRuntimesParams{
		RuntimeID:        runtimeID,
		RuntimeStaleSecs: service.RuntimeClaimFreshnessSeconds,
	})
	if err != nil {
		slog.Warn("runtime pool: pace lookup failed", "runtime_id", util.UUIDToString(runtimeID), "error", err)
		return nil
	}
	in := make([]agpool.PaceRuntime, 0, len(rows))
	for _, r := range rows {
		in = append(in, agpool.PaceRuntime{ID: util.UUIDToString(r.ID), Self: r.IsSelf, Active: int(r.Active), Slots: int(r.Slots), WeeklyResetsAt: r.WeeklyResetsAt})
	}
	pace, ok := agpool.Pace(in, report.Windows["seven_day"].ResetsAt, report.Max)
	if !ok {
		return nil
	}
	return &pace
}

// agAddPoolPaceToHeartbeatResp is DaemonHeartbeat's hook. The runtime row is
// already loaded, so a runtime outside a pool costs no query.
func (h *Handler) agAddPoolPaceToHeartbeatResp(ctx context.Context, resp map[string]any, rt db.AgentRuntime, report *protocol.DaemonQuotaReport) {
	if report == nil || !agpool.InMetadata(rt.Metadata) {
		return
	}
	if pace := h.agPoolPace(ctx, rt.ID, report); pace != nil {
		resp["ag_pool_pace"] = pace
	}
}

// agAckPoolPace is HandleDaemonWSHeartbeat's hook. The report rides in ctx
// (agquota.ReportFrom); the lease does not know the pool, so the query decides.
func (h *Handler) agAckPoolPace(ctx context.Context, ack *protocol.DaemonHeartbeatAckPayload, runtimeID string) {
	if ack == nil {
		return
	}
	report := agquota.ReportFrom(ctx)
	if report == nil {
		return
	}
	if id, err := util.ParseUUID(runtimeID); err == nil {
		ack.AGPoolPace = h.agPoolPace(ctx, id, report)
	}
}
