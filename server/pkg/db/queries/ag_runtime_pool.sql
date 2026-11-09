-- =====================
-- AG patch: runtime pool. Daemons that declare the same pool
-- (agent_runtime.metadata.ag_pool, sent on register from MULTICA_AG_POOL) share
-- the queue of every agent bound to one of their runtimes. Two runtimes are pool
-- peers when they are different rows with the same non-empty pool name,
-- workspace, owner, provider and profile. Tasks still enqueue on the agent's
-- runtime (its home); a peer that claims one rewrites the task's runtime_id to
-- itself (ClaimAgentTask). Kept apart from agent.sql so the patch rebases
-- cleanly. See ag-patches/PATCHES.md.
--
-- The peer predicate is repeated in each query (and in the two stale-dispatch
-- reclaims in agent.sql); keep the copies identical.
--
-- An agent whose runtime_config has "ag_pool_pinned": true (agpool.PinnedKey)
-- is out of the pool: ListAGPoolClaimCandidates and ListAGPoolClaimTasks skip
-- it, so only its own runtime claims its tasks, as upstream. Its runtime stays
-- pooled for other agents.
-- =====================

-- name: ListAGPooledRuntimeIDs :many
-- The runtimes of a claim poll that are in a pool. Empty for a daemon without
-- one, and the claim path then skips every other pool query.
SELECT id FROM agent_runtime
WHERE id = ANY(@runtime_ids::uuid[])
  AND NULLIF(metadata->>'ag_pool', '') IS NOT NULL;

-- name: ListAGPoolPeerRuntimeIDs :many
-- Peers of one runtime, to wake when work appears or frees up on it.
SELECT ag_c.id
FROM agent_runtime ag_h
JOIN agent_runtime ag_c
  ON ag_c.id <> ag_h.id
 AND NULLIF(ag_h.metadata->>'ag_pool', '') = ag_c.metadata->>'ag_pool'
 AND ag_c.workspace_id = ag_h.workspace_id
 AND ag_c.owner_id = ag_h.owner_id
 AND ag_c.provider = ag_h.provider
 AND ag_c.profile_id IS NOT DISTINCT FROM ag_h.profile_id
WHERE ag_h.id = @runtime_id;

-- name: AGRuntimesArePoolPeers :one
SELECT EXISTS (
    SELECT 1
    FROM agent_runtime ag_h
    JOIN agent_runtime ag_c ON ag_c.id = @other_id::uuid
    WHERE ag_h.id = @runtime_id::uuid
      AND ag_c.id <> ag_h.id
      AND NULLIF(ag_h.metadata->>'ag_pool', '') = ag_c.metadata->>'ag_pool'
      AND ag_c.workspace_id = ag_h.workspace_id
      AND ag_c.owner_id = ag_h.owner_id
      AND ag_c.provider = ag_h.provider
      AND ag_c.profile_id IS NOT DISTINCT FROM ag_h.profile_id
)::boolean;

-- name: ListAGPoolClaimCandidates :many
-- Queued tasks a pooled runtime may claim from a peer: the task sits on its
-- agent's runtime (the home) and the claimer is a peer of that runtime.
-- claimer_runtime_id is the runtime in @runtime_ids that would claim it; the
-- claim loop uses it in place of the task's runtime_id. Mirrors the wakeup and
-- urgent-filter predicates of ListQueuedClaimCandidatesByRuntimes (the filter
-- keyed by the claimer); ClaimAgentTask re-checks everything, affinity included.
-- A pinned agent's tasks are not offered.
SELECT sqlc.embed(atq), ag_c.id AS claimer_runtime_id
FROM agent_task_queue atq
JOIN agent a ON a.id = atq.agent_id AND a.runtime_id = atq.runtime_id
 AND NOT COALESCE(a.runtime_config->'ag_pool_pinned' = 'true'::jsonb, false)
JOIN agent_runtime ag_h ON ag_h.id = atq.runtime_id
JOIN agent_runtime ag_c
  ON ag_c.id = ANY(@runtime_ids::uuid[])
 AND ag_c.id <> ag_h.id
 AND NULLIF(ag_h.metadata->>'ag_pool', '') = ag_c.metadata->>'ag_pool'
 AND ag_c.workspace_id = ag_h.workspace_id
 AND ag_c.owner_id = ag_h.owner_id
 AND ag_c.provider = ag_h.provider
 AND ag_c.profile_id IS NOT DISTINCT FROM ag_h.profile_id
WHERE atq.status = 'queued'
  AND (atq.context->>'wakeup_id' IS NULL OR EXISTS (SELECT 1 FROM issue_wakeup w WHERE w.id=(atq.context->>'wakeup_id')::uuid AND w.disabled_at IS NULL AND w.revision=(atq.context->>'wakeup_revision')::bigint))
  AND ((sqlc.narg('ag_claim_filter')::jsonb -> ag_c.id::text) IS NULL OR (atq.issue_id IS NOT NULL AND EXISTS (SELECT 1 FROM issue ag_i WHERE ag_i.id = atq.issue_id AND jsonb_exists(sqlc.narg('ag_claim_filter')::jsonb -> ag_c.id::text, ag_i.priority))))
ORDER BY atq.priority DESC, atq.created_at ASC;

-- name: ListAGPoolClaimTasks :many
-- For one claim by a pooled runtime: the agent's queued tasks on the claimer or
-- on a peer of it. via_pool marks a task on a peer (claimable only through the
-- pool); held marks a task the affinity rule keeps for another peer. A task is
-- held when it is an issue task younger than @affinity_task_age_secs and the
-- agent's last finished run on that issue ran on a different peer that
-- finished within @affinity_window_secs, is online, is not draining and is
-- below its runtime cap (when it has one). Runs in the claim transaction,
-- under the agent row lock. No rows for a pinned agent: nothing via the pool
-- and nothing held, so its home claims as upstream.
SELECT atq.id,
       (atq.runtime_id <> ag_c.id)::boolean AS via_pool,
       (
           atq.issue_id IS NOT NULL
           AND atq.created_at > now() - make_interval(secs => @affinity_task_age_secs::double precision)
           AND EXISTS (
               SELECT 1
               FROM (
                   SELECT last.runtime_id, last.completed_at
                   FROM agent_task_queue last
                   WHERE last.agent_id = atq.agent_id
                     AND last.issue_id = atq.issue_id
                     AND last.started_at IS NOT NULL
                     AND last.completed_at IS NOT NULL
                   ORDER BY last.completed_at DESC, last.id DESC
                   LIMIT 1
               ) ag_last
               JOIN agent_runtime ag_l ON ag_l.id = ag_last.runtime_id
               WHERE ag_l.id <> ag_c.id
                 AND ag_last.completed_at > now() - make_interval(secs => @affinity_window_secs::double precision)
                 AND NULLIF(ag_l.metadata->>'ag_pool', '') = ag_c.metadata->>'ag_pool'
                 AND ag_l.workspace_id = ag_c.workspace_id
                 AND ag_l.owner_id = ag_c.owner_id
                 AND ag_l.provider = ag_c.provider
                 AND ag_l.profile_id IS NOT DISTINCT FROM ag_c.profile_id
                 AND ag_l.status = 'online'
                 AND COALESCE(ag_l.last_seen_at, ag_l.updated_at) >=
                     now() - make_interval(secs => @runtime_stale_secs::double precision)
                 AND COALESCE(ag_l.metadata->'ag_drain'->>'draining', '') <> 'true'
                 AND (
                     ag_l.max_concurrent_tasks IS NULL
                     OR (
                         SELECT count(*) FROM agent_task_queue busy
                         WHERE busy.runtime_id = ag_l.id
                           AND busy.status IN ('dispatched', 'running', 'waiting_local_directory')
                     ) < ag_l.max_concurrent_tasks
                 )
           )
       )::boolean AS held
FROM agent_task_queue atq
JOIN agent_runtime ag_c ON ag_c.id = @claimer_id::uuid
JOIN agent a ON a.id = atq.agent_id
 AND NOT COALESCE(a.runtime_config->'ag_pool_pinned' = 'true'::jsonb, false)
WHERE atq.agent_id = @agent_id::uuid
  AND atq.status = 'queued'
  AND (
      atq.runtime_id = ag_c.id
      OR EXISTS (
          SELECT 1 FROM agent_runtime ag_h
          WHERE ag_h.id = atq.runtime_id
            AND ag_h.id <> ag_c.id
            AND NULLIF(ag_h.metadata->>'ag_pool', '') = ag_c.metadata->>'ag_pool'
            AND ag_h.workspace_id = ag_c.workspace_id
            AND ag_h.owner_id = ag_c.owner_id
            AND ag_h.provider = ag_c.provider
            AND ag_h.profile_id IS NOT DISTINCT FROM ag_c.profile_id
      )
  );

-- name: RehomeAGPoolPendingTasks :many
-- Moves queued and deferred tasks that sit on a pool peer of their agent's
-- runtime back to that runtime. A claim binds a task to the claimer, so
-- anything cloned or requeued from it (retries, including the runtime_offline
-- retry of a run stranded on a closed laptop, and a requeue after a failed
-- claim) lands on the claimer. There it is claimable by nobody (the claim
-- fences on the agent's runtime) and a deferred row would only promote once the
-- claimer is back online. Scoped to the pools of the polling runtimes.
-- Pinned agents are included: home is the only runtime that may claim them.
UPDATE agent_task_queue t
SET runtime_id = a.runtime_id
FROM agent a, agent_runtime ag_h, agent_runtime ag_c
WHERE a.id = t.agent_id
  AND ag_h.id = a.runtime_id
  AND ag_c.id = t.runtime_id
  AND t.status IN ('queued', 'deferred')
  AND ag_c.id <> ag_h.id
  AND NULLIF(ag_h.metadata->>'ag_pool', '') = ag_c.metadata->>'ag_pool'
  AND ag_c.workspace_id = ag_h.workspace_id
  AND ag_c.owner_id = ag_h.owner_id
  AND ag_c.provider = ag_h.provider
  AND ag_c.profile_id IS NOT DISTINCT FROM ag_h.profile_id
  AND EXISTS (
      SELECT 1 FROM agent_runtime ag_p
      WHERE ag_p.id = ANY(@runtime_ids::uuid[])
        AND ag_p.metadata->>'ag_pool' = ag_h.metadata->>'ag_pool'
        AND ag_p.workspace_id = ag_h.workspace_id
        AND ag_p.owner_id = ag_h.owner_id
  )
RETURNING t.*;
