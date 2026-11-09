package handler

// AG patch: runtime pool. A daemon declares its pool on register (ag_pool, from
// MULTICA_AG_POOL) and the name is stored in agent_runtime.metadata. A claim by
// a pool peer of the agent's runtime rewrites the task's runtime_id to the
// claimer, so the delivery rechecks accept a task whose runtime is a peer of
// the agent's. See internal/service/ag_runtime_pool.go.

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/agpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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
