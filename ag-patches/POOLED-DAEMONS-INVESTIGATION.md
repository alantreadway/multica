# Pooled daemons: Bastion + laptop sharing agents (investigation)

Research only, on `ag/v0.8.1` (HEAD `060558a52`). No code changed. Line numbers are
for that revision. Upstream checked read-only on 2026-11-09.

## Recommendation

1. **Build a small "runtime pool" patch (approach b): bind the task at claim time.**
   Tasks still enqueue on the agent's home runtime (Bastion). Any online runtime in
   the same pool (same workspace, owner and provider) may claim them, and the claim
   **rewrites `agent_task_queue.runtime_id` to the claimer**. Every later step
   (start, lease, cancel watch, orphan recovery, offline sweep, runtime cap, session
   resume gate, usage) is already keyed on `task.runtime_id`, so it carries on
   unchanged and per machine. The pool is declared by the daemon
   (`MULTICA_AG_POOL=alan` on both machines), stored in `agent_runtime.metadata`
   the way `ag_drain` is. **No migration.** About 14-20 h with tests, plus 4-6 h
   for issue affinity (below).
2. **Taper with tools that already exist; skip new daemon code at first.** A
   launchd job on the laptop steps the laptop runtime's server-side cap 3→2→1
   (`PATCH /api/runtimes/{id}` `max_concurrent_tasks`, upstream #9795, already in
   our base). It then runs `multica-ag daemon drain --wait` (the drain patch) and
   shows a macOS notification once the laptop is idle. In the morning it runs
   `daemon resume` and sets the cap back to 3. About 1-2 h. Later, if wanted, a
   `MULTICA_AG_TAPER` schedule inside the daemon (4-6 h).
3. **Until (1) lands, use approach (a) as a zero-code stopgap.** Run a second
   daemon on the laptop with its **own** daemon id, and add copies of the busiest
   agents bound to the laptop runtime ("Dev · laptop"). You choose the machine by
   choosing the assignee.
4. **Never reuse Bastion's `daemon.id` on the laptop.** The runbook already forbids
   it (home-sysadmin `docs/multica-ag.md:67-69`). The reasons are below.
5. **Lower `MULTICA_RUNTIME_RECONNECT_GRACE`** from 3 h to about 20-30 min once the
   laptop joins. The server and the Bastion daemon share a host, so in practice
   this setting only affects the laptop.

Bastion now runs with cap 2 (runbook line 76). The laptop at cap 3 restores the 5
slots the Mac had.

## 1. Daemons, runtimes and agents in v0.8.1

- **A runtime is one row per (workspace, daemon_id, provider).**
  `UpsertAgentRuntime` (`server/pkg/db/queries/runtime.sql:66`) uses that conflict
  key. The `daemon_id` comes from `MULTICA_DAEMON_ID`, otherwise from the UUID
  persisted in `~/.multica/daemon.id` (`server/internal/daemon/config.go:496-511`).
  A second machine therefore gets its own runtime rows.
- **An agent has exactly one `runtime_id`.** Every enqueue path copies it into
  the task (`server/internal/service/task.go:1328,1501,1685,2125`).
- **Claims are fenced on `agent.runtime_id = task.runtime_id`.** This happens in
  five SQL places: `ClaimAgentTask` (`server/pkg/db/queries/agent.sql:788`), both
  candidate listings (`:2193`, `:2315`) and both stale-dispatch reclaims (`:906`,
  `:953`). It also happens in four Go places: `claimTask` `runtime_mismatch`
  (`server/internal/service/task.go:3647`), the delivery rechecks
  (`server/internal/handler/daemon.go:2496`, `:2950`) and
  `server/internal/service/issue_wakeup.go:1019`.
- **Answer: two daemons cannot serve the same agent today.** With distinct ids
  they register distinct runtimes, and an agent's tasks are claimable only by its
  one runtime. With a shared id they collide on one runtime row, which breaks
  things (next item).
- **A shared `daemon.id` is not a shortcut.** Two things go wrong:
  - A daemon that starts or re-registers calls `RecoverOrphans`
    (`server/internal/daemon/daemon.go:4382`, `:1979`). The server then fails
    **every** dispatched or running task on that runtime
    (`server/internal/handler/task_lifecycle.go:17-27`). Each morning the laptop
    would kill Bastion's runs. The handoff patch suppresses this only for a
    same-machine predecessor (`server/internal/daemon/ag_handoff.go:277`).
  - The offline sweeper fails tasks only when the runtime is offline
    (`FailTasksForOfflineRuntimes`, `runtime.sql:300`). While Bastion keeps
    heartbeating the shared runtime, a run stranded by a closed lid would stay
    "running" forever.

  Heartbeat metadata (drain badge, quota report) would also flip back and forth
  between the two daemons. The WS hub does accept several connections per runtime
  (`server/internal/daemonws/hub.go:332`), so it is not the blocker.
- **Upstream #9795** (runtime-wide concurrency limit) merged 2026-11-02 and is in
  v0.8.0, so it is in our base. It adds `agent_runtime.max_concurrent_tasks`, which
  the claim transaction enforces under an advisory lock
  (`task.go:3678-3698`, `runtime.sql:708`) and which PATCH can change live
  (`server/internal/handler/runtime.go:486-507`; range 1-50,
  `server/internal/agentconfig/concurrency.go:7-8`). **That is our taper lever.**
- **Upstream #9960** (move pending tasks when an agent switches runtime) merged
  2026-11-04 and is in v0.8.1: `ReassignPendingAgentTasksToRuntime`
  (`agent.sql:649`). Queued and deferred rows follow a rebind; dispatched and
  running rows stay. This makes "rebind agents to Bastion at 17:00" safe, but it
  is not pooling.

## 2. AG patches to reuse

| Patch | Use here |
| --- | --- |
| Drain mode | The final taper step: `daemon drain --wait` stops claims and exits 0 once idle. It is held in memory, so a laptop reboot comes back claiming. Fine for a morning start; the taper job must re-issue drain if the laptop restarts mid-taper. |
| Drain badge | Works per runtime with no changes: the laptop runtime shows "Draining · N running", then "Idle" (= safe to close the lid). Pool-aware affinity can read `metadata.ag_drain` to stop favouring a draining laptop. |
| Handoff / overlap | Not needed across machines. Its slot locks are per machine (`ag_slotlock.go`), so the two machines' slots never interact. `agOrphanScope` shows the hook point if orphan recovery ever needs scoping. |
| Urgent-only claim filter | Not needed for pooling. Optional: during the last taper step, let the laptop claim only `urgent` work, through the same per-runtime `ag_min_issue_priority` map. |
| Claude pacing cap | Per daemon and per account. Its cap is `min(pace, MAX)` at `server/internal/daemon/ag_quota.go:319`. A daemon-side taper would add one more `min()` here. See the quota note in §3. |
| Backup account failover | Per machine. The laptop needs its own `~/.claude-backup` plus `MULTICA_AG_CLAUDE_BACKUP_DIR`, or leave it unset (a quota hit then holds instead of failing over). Never copy logins between machines (runbook :73-75). |
| Runtime `max_concurrent_tasks` | Server-side, live, per runtime. It steps the laptop down 3→2→1 with no daemon restart. Its floor is 1, so 0 means drain. |
| Pacing work-hours parser | `agParsePaceHours` (`ag_quota_pace_hours.go:36`) parses "Mon-Fri HH:MM-HH:MM". A daemon taper schedule can reuse it. |

**Taper = drain + a cap ramp.** The cap ramp limits how many runs the laptop
starts and never pre-empts a running one. Drain is the cap-0 step and also shows
when the laptop is idle. Under pooling, whatever the laptop does not claim stays
in the shared queue for Bastion.

## 3. Approaches, ranked

### (b) Runtime pool with claim-time binding (recommended)

- **Backend changes:**
  - Add a pool predicate wherever the fence `a.runtime_id = atq.runtime_id`
    appears: "`atq.runtime_id = a.runtime_id` and the claimer runtime is
    `a.runtime_id` or a pool peer". The listings at `agent.sql:2193,2315` widen
    the runtime set to pool peers.
  - In `ClaimAgentTask` (`:760`), match `atq.runtime_id = a.runtime_id` and
    `SET runtime_id = @claimer`.
  - After that rewrite, the four Go fences compare against the agent's home
    runtime or the pool.
  - Stale-dispatch reclaims (`:906`, `:953`) compare the task's runtime with the
    claimer, not with `a.runtime_id`.
  - Re-home `runtime_offline` retries to the agent's home runtime.
    `CreateRetryTask` copies the parent's runtime (`agent.sql:519`), and deferred
    rows promote only when their runtime heartbeats (`agent.sql` Promote…,
    `task.go:5511`). Without re-homing, a laptop-orphan retry would wait for the
    laptop.
  - Pool membership is computed server-side from `metadata.ag_pool` with the same
    workspace, owner and provider. Add an `ag_pool` field to the register request
    and one hook line in the metadata map (`handler/daemon.go:778`).
  - Put the new queries in `ag_runtime_pool.sql`. The fence edits are in upstream
    `agent.sql`, on the same lines the urgent filter already touches, so expect
    rebase conflicts there.
- **Daemon:** send `ag_pool` from the env on register. No other changes.
- **Web:** nothing is required. The task rows already show the runtime that ran
  them. An optional "pool: alan" chip on the runtime card is about 1 h.
- **Migration:** none (it uses the metadata key). If a server-set pool is
  preferred to a daemon-declared one, use `9018_ag_runtime_pool`: `ALTER TABLE
  agent_runtime ADD COLUMN ag_pool text`, no FK, plus a separate CONCURRENTLY
  index file if it is ever filtered on. With only a handful of runtimes, no index
  is needed.
- **Double claim:** safe. `claimTask` locks the agent row (`GetAgentForClaimUpdate
  ... FOR UPDATE`, `agent.sql:1671`), so the two daemons serialise per agent.
  `FOR UPDATE SKIP LOCKED` plus the per-(issue, agent) `NOT EXISTS` (`agent.sql:801-817`)
  and the unique pending index (migration 452) mean one agent never runs one issue
  on both machines at once. The per-agent `max_concurrent_tasks` still caps the
  agent across both machines: raise it if one agent should use 5 slots.
- **Lid closed mid-run:**
  1. The runtime goes offline after 150 s (`task.go:198`). The run stays
     `running` for the reconnect grace, 3 h by default
     (`server/cmd/server/runtime_sweeper.go:43`, env
     `MULTICA_RUNTIME_RECONNECT_GRACE`, `main.go:744`). During that time the
     (issue, agent) slot blocks follow-ups on that issue.
  2. After the grace, the task fails as `runtime_offline` with a deferred retry.
     With re-homing, Bastion picks it up and starts a fresh session.
  3. If the lid reopens before the grace ends, the suspended Claude stream most
     likely errors (`provider_network` → immediate retry, claimable by either
     machine).
  4. If it reopens after the grace, `watchTaskCancellation` sees the terminal
     status and kills the stale local run (`daemon.go:5600-5627`). There is a
     small window before the first poll in which it could still push.
  5. The laptop worktree is left on disk and GC removes it (72 h orphan TTL).
     Unpushed commits stay on the laptop.

  Hence the advice to drop the grace to 20-30 min: the server and the Bastion
  daemon share a host, so the only partitions it covers are the laptop's.
- **Quota:** see "Claude quota per machine" below.
- **Size:** 14-20 h, including DB-backed handler tests (`ag_*_test.go`). Add 4-6 h
  for affinity.

### (a) A second runtime with duplicated agents (stopgap, zero code)

- **Setup:** install the laptop daemon (`multica-ag`, new `daemon.id`,
  `MULTICA_DAEMON_MAX_CONCURRENT_TASKS=3`). Add `local_directory` resources for
  the laptop's daemon id, then copy agents onto the laptop runtime
  (`multica agent copy`, `cmd_agent_copy.go`).
- **Touches:** nothing. Migration: none.
- **Failure modes:**
  - Load sharing is manual: assignees, @mentions, autopilots and ag-jira-sync
    target one specific agent.
  - At taper time, queued tasks for laptop agents wait for the next morning unless
    you rebind (#9960 moves queued rows) or reassign.
  - A lid-close orphan's retry waits for the laptop.
  - Affinity is perfect, because the agent fixes the machine.
- **Size:** 1-2 h of setup.
- **Variant (a′):** bind some agents to the laptop for the day and rebind them to
  Bastion at taper time. #9960 moves their queued work and running tasks finish
  where they are. Still a manual split, and Bastion can sit idle while laptop
  agents queue.

### (c) One logical runtime served by several daemons (don't)

- **What it needs:** a per-daemon lease on each task (an `ag_daemon_id` column,
  migration 9018 or later). Orphan recovery, the offline sweeper, the
  running-timeout backstop, the heartbeat-memo badges and the quota report all
  have to become daemon-scoped instead of runtime-scoped. The local_directory
  resources are keyed by daemon id (`server/internal/handler/project_resource.go:134-147`),
  so they would need a new keying as well.
- **Touches:** about 3× the surface of (b), with the same user-visible result.
- **Size:** 35-50 h. High rebase risk.

### Upstream

- **#8579** (open, 69 files, +6375, merge state "dirty"): ordered multi-runtime
  bindings with **fallback** on quota or auth failure, not concurrent load
  sharing. It will not land soon and does not solve this.
- **RFC #6650** (open, 5 👍, active 2026-11-05): agent identity vs `agent_binding`
  rows. Pooling would fall out of it eventually. Our (b) is the narrow version.
  Drop it if bindings land.
- **#1981** "Horizontal scaling of agents with runtime pools" (open feature
  request): sylvainDNS commented on 2026-11-04 with exactly this laptop plus home
  server case. A maintainer only suggested fallback as a first step (May). If you
  want to signal interest, a 👍 or a use-case comment there is the cheapest step.
  Nothing has been posted.
- **#5282** and **#1398** (fallback runtime) are the same fallback direction.

### Claude quota per machine (all approaches)

Each machine has its own `claude auth login`.

- **Same Anthropic account on both machines:** each pacer reads the account-wide
  utilisation but divides the burn by its **local** run count
  (`ag_quota_pace.go:9-12`, `sample()` at `:259`). Each daemon therefore
  overestimates the per-run burn and allows itself roughly its share of the total.
  Combined use stays at or under target, but the result is conservative and each
  machine keeps a floor of 1 run. Acceptable; revisit only if pacing visibly
  starves the laptop.
- **Different accounts:** the two machines pace independently and correctly.
- **Backup failover:** per machine (see §2).

## 4. Taper design

- **Use a schedule by default and allow a manual override.** Closing the lid is
  routine, so a schedule removes the need to remember. A manual trigger covers
  leaving early.
- **When to step down:** what matters is the last claim time, not the leave time.
  Runs started after `leave − p90(run duration)` probably won't finish. Measure
  the p90 before choosing the times:
  `SELECT percentile_cont(0.9) WITHIN GROUP (ORDER BY completed_at - started_at)
  FROM agent_task_queue WHERE status='completed' AND started_at > now() - interval '14 days';`
  Run on 2026-11-09: 21.6 min over 1,894 runs (p95 30 min). `taper.sh` uses 25
  (see README.md, "Laptop taper").
- **Default schedule (if p90 is about 45 min and you leave at 17:30):**
  - cap 3 until 16:00
  - 2 at 16:00
  - 1 at 16:30
  - drain at 16:45
  - notification once idle
- **v1 (scripts, no fork code):** a laptop launchd job (`~/.multica/taper.sh`)
  does the steps below.
  1. PATCH the laptop runtime cap at each step (`multica-ag` CLI token, or `curl`
     with a PAT).
  2. Run `multica-ag daemon drain --wait --timeout 90m`, then `osascript -e
     'display notification "Multica laptop idle"'`.
  3. At 08:30, run `daemon resume` and PATCH the cap back to 3.

  A manual early leave is `taper.sh now`: cap 1, then drain.
- **v2 (daemon patch, 4-6 h):**
  - `MULTICA_AG_TAPER="Mon-Fri 16:00=2 16:30=1 16:45=0"`, parsed with the
    pace-hours parser and applied as one more `min()` at `ag_quota.go:319` and on
    the slot count (`daemon.go:5452`).
  - Cap 0 calls `agStartDrain()` with a "taper" source. It auto-resumes at the
    next window start, but never overrides a manual drain.
  - Add `multica daemon taper [--leave 17:30]` for ad-hoc use, and show the
    current taper cap in `/health` and `daemon status`.
- **Queued tasks:** the laptop never holds a backlog. It claims at most its free
  slots per batch (`daemon.go:5452`), and draining stops further claims.
  - Under (b), everything not yet dispatched stays in the shared queue and Bastion
    takes it.
  - A task already dispatched to the laptop when drain starts still runs there.
    Drain does not hand it back.
  - Under (a), the laptop agents' queues wait until morning unless rebound.

## 5. Issue and workdir locality

**What happens when consecutive runs of one issue land on different machines:**

- **Claude session:** not resumed. The server sends `PriorSessionID` only when
  the prior run's runtime equals the task's (`handler/daemon.go:3529`). Under (b)
  the claim rewrites the runtime first, so a machine switch quietly takes the cold
  path with full context, without the "prior session unavailable" notice. It costs
  an uncached first turn. The stable-path patch needs a resume, so it is skipped
  (fails safe).
- **Worktree and branch:** uncommitted files and **unpushed commits** on machine A
  are invisible to machine B. B creates or resets its own branch from its own
  checkout. This is the real loss.
- **`PriorWorkDir`:** sent even across machines (`handler/daemon.go:3540-3541`; retries via
  `applyFreshSessionRetryWorkdir`, `:2822`). The daemon checks
  `sameExistingDir` (`internal/daemon/daemon.go:6757`), so a foreign path is
  ignored. Verify this in a test when building (b).
- **local_directory:** resolved per daemon id
  (`internal/daemon/local_directory.go:121`). A project without a laptop entry
  runs on the laptop without the local checkout.
  - Each project needs a laptop `local_directory` resource.
  - `HOME=/Users/bls` on both machines, so `~/git/<repo>` paths match. However,
    on the laptop that is your own working copy: worktrees branch from its HEAD,
    whatever you have checked out.
  - Use dedicated agent checkouts on the laptop (for example `~/multica-git/<repo>`
    with an ff job like ag-ff-checkouts).

**Affinity rule (claim-side, part of (b)):** a non-home pool member skips a
queued issue task when all of the following hold:

- The agent's last run on that issue ran on another pool runtime and finished
  less than 60 min ago (the resume window used by `ag_claude_resume.go`).
- That runtime is online, not draining, and below its cap.
- The task is younger than about 3 min.

Otherwise anyone in the pool may claim it.

This keeps follow-ups on the warm machine, fails over quickly when that machine
is full, tapering or asleep, and costs no enqueue-path changes (one `NOT EXISTS`
in the pool candidate query). Pair it with an agent instruction to push the
branch at the end of every run, so a cross-machine follow-up loses only the
session cache.

## Prerequisites and open questions

- **Network:** the laptop must reach the self-host API and WS from the work
  network (Tailscale or similar). Not checked.
- **Claude accounts:** do the laptop and Bastion use the same Anthropic account?
  This decides the quota note above.
- **Per-agent `max_concurrent_tasks`:** check the current values on the agents
  that should use all 5 slots.
- **Leftover rows:** old Mac runtime rows now belong to Bastion's id (the
  runbook's "same daemon identity"). The laptop's new `daemon.id` creates fresh
  rows. Confirm that no leftover hostname-derived legacy id merges into them
  (`FindLegacyRuntimesByDaemonID`, `runtime.sql:509`).
