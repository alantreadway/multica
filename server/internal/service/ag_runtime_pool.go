package service

// AG patch: runtime pool. Daemons that set the same MULTICA_AG_POOL share the
// queue of every agent bound to one of their runtimes (see ag_runtime_pool.sql
// for who counts as a peer). Tasks still enqueue on the agent's runtime, the
// home. A peer that claims one rewrites its runtime_id to itself inside
// ClaimAgentTask, so lease, cancel, orphan recovery, the offline sweep, the
// runtime cap, session resume and usage all stay per machine.
//
// Without a pool every helper here is a no-op and the claim path is upstream's:
// ListAGPooledRuntimeIDs returns nothing, the context carries no set, and
// ClaimAgentTask gets NULL pool arrays. An agent pinned to its runtime
// (agpool.Pinned) gets the same treatment while its runtime stays pooled.

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/agpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	// agPoolAffinityWindow matches the Claude resume window
	// (agClaudeResumeWindow in the daemon): past it the next run starts a
	// fresh session anyway, so there is nothing warm to keep.
	agPoolAffinityWindow = 60 * time.Minute
	// agPoolAffinityTaskAge bounds how long the affinity rule holds a task back
	// for the peer that ran the issue last. After it any peer may claim it.
	agPoolAffinityTaskAge = 3 * time.Minute
	agPoolNotifyTimeout   = 2 * time.Second
)

// agPoolPrepare runs at the start of a claim poll. It finds the pooled
// runtimes among runtimeIDs, moves stray pending tasks of their pools back to
// their agents' runtimes, and returns ctx carrying the pooled set for the rest
// of the poll. A failed query leaves the poll unpooled rather than failing it.
func (s *TaskService) agPoolPrepare(ctx context.Context, runtimeIDs []pgtype.UUID) context.Context {
	if s.Queries == nil || len(runtimeIDs) == 0 {
		return ctx
	}
	pooled, err := s.Queries.ListAGPooledRuntimeIDs(ctx, runtimeIDs)
	if err != nil {
		slog.Warn("runtime pool: list pooled runtimes failed", "error", err)
		return ctx
	}
	if len(pooled) == 0 {
		return ctx
	}
	moved, err := s.Queries.RehomeAGPoolPendingTasks(ctx, pooled)
	if err != nil {
		slog.Warn("runtime pool: rehome pending tasks failed", "error", err)
	}
	homes := map[string]pgtype.UUID{}
	for _, t := range moved {
		slog.Info("runtime pool: pending task moved back to its agent's runtime",
			"task_id", util.UUIDToString(t.ID),
			"agent_id", util.UUIDToString(t.AgentID),
			"runtime_id", util.UUIDToString(t.RuntimeID),
			"status", t.Status,
		)
		homes[util.UUIDToString(t.RuntimeID)] = t.RuntimeID
	}
	for _, home := range homes {
		s.notifyRuntimeMayHaveWork(home, "")
	}
	return agpool.WithSet(ctx, agpool.NewSet(pooled))
}

// agPoolAppendCandidates adds the queued tasks the poll's pooled runtimes may
// claim from their peers, each with RuntimeID set to the claimer, and keeps the
// list in claim order (priority, then FIFO).
func (s *TaskService) agPoolAppendCandidates(ctx context.Context, runtimeIDs []pgtype.UUID, candidates []db.AgentTaskQueue, claimFilter []byte) []db.AgentTaskQueue {
	set := agpool.From(ctx)
	if set == nil {
		return candidates
	}
	claimers := make([]pgtype.UUID, 0, len(runtimeIDs))
	for _, id := range runtimeIDs {
		if set.Has(id) {
			claimers = append(claimers, id)
		}
	}
	if len(claimers) == 0 {
		return candidates
	}
	rows, err := s.Queries.ListAGPoolClaimCandidates(ctx, db.ListAGPoolClaimCandidatesParams{
		RuntimeIds:    claimers,
		AgClaimFilter: claimFilter,
	})
	if err != nil {
		slog.Warn("runtime pool: list pool claim candidates failed", "error", err)
		return candidates
	}
	if len(rows) == 0 {
		return candidates
	}
	out := make([]db.AgentTaskQueue, 0, len(candidates)+len(rows))
	out = append(out, candidates...)
	for _, row := range rows {
		t := row.AgentTaskQueue
		t.RuntimeID = row.ClaimerRuntimeID
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].CreatedAt.Time.Before(out[j].CreatedAt.Time)
	})
	return out
}

// agPoolClaimAllowed reports whether claimer may claim for agent, which is
// bound to another runtime (its home): the agent is not pinned to its runtime,
// claimer is pooled in this poll and claimer is a peer of home.
func (s *TaskService) agPoolClaimAllowed(ctx context.Context, qtx *db.Queries, agent db.Agent, claimer pgtype.UUID) bool {
	home := agent.RuntimeID
	if !agpool.From(ctx).Has(claimer) || !home.Valid || agpool.Pinned(agent.RuntimeConfig) {
		return false
	}
	ok, err := qtx.AGRuntimesArePoolPeers(ctx, db.AGRuntimesArePoolPeersParams{RuntimeID: home, OtherID: claimer})
	if err != nil {
		slog.Warn("runtime pool: peer check failed", "error", err)
		return false
	}
	return ok
}

// agPoolClaimSets are ClaimAgentTask's pool arguments: tasks the claimer may
// take from a peer, and tasks the affinity rule keeps for another peer. Both
// are nil for a claimer outside a pool, which leaves the claim as upstream's.
type agPoolClaimSets struct {
	taskIDs []pgtype.UUID
	heldIDs []pgtype.UUID
}

func (s *TaskService) agPoolClaimSets(ctx context.Context, qtx *db.Queries, agentID, claimer pgtype.UUID) (agPoolClaimSets, error) {
	var sets agPoolClaimSets
	if !agpool.From(ctx).Has(claimer) {
		return sets, nil
	}
	rows, err := qtx.ListAGPoolClaimTasks(ctx, db.ListAGPoolClaimTasksParams{
		AffinityTaskAgeSecs: agPoolAffinityTaskAge.Seconds(),
		AffinityWindowSecs:  agPoolAffinityWindow.Seconds(),
		RuntimeStaleSecs:    RuntimeClaimFreshnessSeconds,
		ClaimerID:           claimer,
		AgentID:             agentID,
	})
	if err != nil {
		return sets, err
	}
	for _, row := range rows {
		if row.Held {
			sets.heldIDs = append(sets.heldIDs, row.ID)
		} else if row.ViaPool {
			sets.taskIDs = append(sets.taskIDs, row.ID)
		}
	}
	return sets, nil
}

// agPoolWakePeers extends notifyRuntimeMayHaveWork to the runtime's pool
// peers: work queued on, or freed up by, one machine is claimable by the
// others, which would otherwise only see it on their next poll.
func (s *TaskService) agPoolWakePeers(runtimeID pgtype.UUID, taskID string) {
	if s.Queries == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), agPoolNotifyTimeout)
	defer cancel()
	peers, err := s.Queries.ListAGPoolPeerRuntimeIDs(ctx, runtimeID)
	if err != nil {
		slog.Debug("runtime pool: list peers failed", "runtime_id", util.UUIDToString(runtimeID), "error", err)
		return
	}
	for _, peer := range peers {
		key := util.UUIDToString(peer)
		s.EmptyClaim.Bump(context.Background(), key)
		if s.Wakeup != nil {
			s.Wakeup.NotifyTaskAvailable(key, taskID)
		}
	}
}
