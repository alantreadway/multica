# AG patches

Patch inventory for the `ag/<tag>` branch. Update this file whenever a patch
lands, is dropped or is reworked during a rebase.

## Upstream base

- Tag: `v0.6.2`
- Commit: `7ab92d7622ecd81d179a8d83ae2dea74835a5069`

## Upstream status

Where each patch stands against `multica-ai/multica`, so we can decide which to
offer upstream. Every patch entry below ends with an **Upstream** line:

- **Status:**
  - `fork-only`: not worth offering (or upstream is already building the same
    thing and we won't race it).
  - `candidate`: worth offering. Nothing is proposed yet; upstream's
    `CONTRIBUTING.md` and our contribution routing want a Feature Request
    agreed before a feature PR.
  - `proposed <link>`: an upstream issue or PR is open for it.
  - `merged upstream in <tag>`: drop the patch at the next rebase.
  - `superseded by <link>`: upstream shipped something that replaces it; drop it
    at the next rebase.
- **Overlap:** related upstream PRs and issues, with their state when last checked.
- **Fit:** how it sits with upstream's `VISION.md` and roadmap, or why it doesn't.

Last checked 2026-10-16 against upstream `main` (release `v0.6.3` was cut that
day), using the preflight from the `multica-upstream-contribution` skill: live
`VISION.md` and `CONTRIBUTING.md`, plus issue and PR searches, open and closed.
Read-only: nothing has been opened, filed or commented on upstream. Re-check on
every rebase (README, "Rebase onto a new upstream tag").

| Patch | Status | Overlapping upstream |
| --- | --- | --- |
| Tree-ordered parent lanes | `candidate` | [#2966](https://github.com/multica-ai/multica/pull/2966) (open, adjacent) |
| Lane drag rules and jump-to-lane chip | `candidate` (with tree lanes) | [#6704](https://github.com/multica-ai/multica/pull/6704), [#7904](https://github.com/multica-ai/multica/pull/7904) (open, edit the same file) |
| Daemon drain mode | `fork-only` | [#5631](https://github.com/multica-ai/multica/pull/5631), [#6677](https://github.com/multica-ai/multica/pull/6677) (open) |
| Drain badge (backend + web) | `fork-only` | [#6679](https://github.com/multica-ai/multica/pull/6679), [#6678](https://github.com/multica-ai/multica/pull/6678), [#6677](https://github.com/multica-ai/multica/pull/6677) (open) |
| Daemon handoff: machine-wide slot cap and single-instance fixes | `fork-only` | [#5631](https://github.com/multica-ai/multica/pull/5631), [#6677](https://github.com/multica-ai/multica/pull/6677) (open, drain-then-stop, not overlap) |

`VISION.md` is a statement of direction (humans and agents as one team, work and
decisions kept visible), not a feature list. None of the patches conflicts with
it and none advances it directly; the fit lines below stay modest because
upstream closes PRs it reads as out of scope.

## Patches

Test names for every patch start with `AG patch:` so they are easy to find and
to confirm survived a rebase (`grep -rn "AG patch:" packages/`).

### Tree-ordered parent lanes (landed)

Swimlane view groups by parent issue and orders lanes as a tree, so sub-issue
lanes sit under their parent lane. Status: landed in
[#2](https://github.com/alantreadway/multica/pull/2).

- Test prefix: `AG patch:`
- Files:
  - `packages/views/issues/components/swimlane-tree.ts` (new)
  - `packages/views/issues/components/swimlane-tree.test.ts` (new)
  - `packages/views/issues/components/swimlane-view.tsx`
  - `packages/views/issues/components/swimlane-view.test.tsx`
  - `packages/core/issues/queries.ts` (append only)

- Upstream:
  - Status: `candidate`.
  - Overlap: the swimlane view and its parent grouping are upstream
    ([#3149](https://github.com/multica-ai/multica/pull/3149) merged; request [#2951](https://github.com/multica-ai/multica/issues/2951) closed).
    Upstream `main` has no tree ordering of parent lanes. Open [#2966](https://github.com/multica-ai/multica/pull/2966)
    (hierarchy views, last touched 2026-07-07) adds a separate tree view and
    parent context instead of ordering lanes, so it is adjacent, not a duplicate.
  - Fit: a small improvement to a shipped view, no new surface or dependency.
    `VISION.md` says nothing about views. It is feature work, so it goes through
    a Feature Request first, after reading #2966 so we don't race it.

### Lane drag rules and jump-to-lane chip (landed)

Drag rules between swimlanes and a chip that jumps to the related lane.
Status: landed in [#3](https://github.com/alantreadway/multica/pull/3).
Depends on the tree-ordered lanes patch.

- Test prefix: `AG patch:`
- Files:
  - `packages/views/issues/components/swimlane-view.tsx`
  - `packages/views/issues/components/swimlane-view.test.tsx`

- Upstream:
  - Status: `candidate`, bundled with tree lanes. The drag rules only make sense
    once lanes are a tree, so it follows that proposal.
  - Overlap: no duplicate. Open [#6704](https://github.com/multica-ai/multica/pull/6704) and [#7904](https://github.com/multica-ai/multica/pull/7904)
    both edit `swimlane-view.tsx` (conflict risk at rebase, no behavioural
    overlap); [#4416](https://github.com/multica-ai/multica/pull/4416) (drag settle-lock) is merged and already in
    the base.
  - Fit: as for tree lanes. The jump chip is the more subjective part; offer the
    drag rules first.

### Daemon drain mode (landed)

The daemon can be put into drain: it stops claiming new tasks, lets running
tasks finish, reports `idle` on `/health` (no active tasks, no claim in flight,
no update installing), and resumes on request. Held in memory only, so any
restart comes back claiming; `triggerRestart` refuses while draining
(self-reload, auto-update and server-triggered update all go through it) and
says so in `reload_pending_reason`. Auto-update and server-triggered update
also skip the upgrade itself when draining starts first. The daemon core
(`POST /drain`, `POST /resume`, `/health` fields `draining`, `drain_started_at`,
`idle`) and the CLI commands (`daemon drain [--wait] [--timeout]`, `daemon
resume`, `daemon stop|restart --drain [--timeout]`, extra `daemon status` lines)
are in; the web badge is the next patch. Needs a CLI built with
`ag-patches/build-cli.sh`: see README "Daemon/CLI patch". Overlaps upstream
multica-ai/multica#5631 and #6677: drop this patch if either merges.

- Test prefix: `TestAGPatch` / `AG patch:` (run by `ag-patches/test.sh` and the
  `guard` job; the script asserts a minimum number of passing tests)
- Files:
  - `server/internal/daemon/ag_drain.go` (new)
  - `server/internal/daemon/ag_drain_test.go` (new)
  - `server/internal/daemon/daemon.go` (one-line hooks: `agDrain` field,
    `tryEnterClaim`, `triggerRestart`, `tryBeginServerUpdate`,
    `handleUpdate`, `pollLoop`)
  - `server/internal/daemon/auto_update.go` (`tryAutoUpdate` skips while
    draining)
  - `server/internal/daemon/health.go` (routes and `/health` fields)
  - `server/cmd/multica/ag_daemon_drain.go` (new: `daemon drain`, `daemon
    resume`, `--drain`/`--timeout` handling, status rows)
  - `server/cmd/multica/ag_daemon_drain_test.go`,
    `server/cmd/multica/ag_daemon_drain_status_test.go` (new: command, status
    and human-only guard tests)
  - `server/cmd/multica/ag_build_cli_test.go` (new: `build-cli.sh` tests)
  - `server/cmd/multica/cmd_daemon.go` (one-line hooks: `daemon stop` and
    `daemon restart` call `agDrainBeforeStop` / `agDrainBeforeRestart`,
    `printDaemonStatusReport` adds `agDaemonStatusRows`)
  - `ag-patches/build-cli.sh` (new, tooling: builds the CLI binary) and
    `ag-patches/lib.sh` (new, tooling: preamble and Go runner shared with
    `build-web.sh` and `test.sh`)

- Upstream:
  - Status: `fork-only`. Upstream has open PRs for the same feature and we won't
    race them. Drop this patch if [#5631](https://github.com/multica-ai/multica/pull/5631) or
    [#6677](https://github.com/multica-ai/multica/pull/6677) merges; re-evaluate what is left (the in-memory-only
    design, the `daemon stop|restart --drain` wrapper) once they land.
  - Overlap: [#5631](https://github.com/multica-ai/multica/pull/5631) (CLI `daemon restart --drain`, open since
    2026-07-19, changes requested then addressed, awaiting re-review; closes
    issue [#5630](https://github.com/multica-ai/multica/issues/5630)); [#6677](https://github.com/multica-ai/multica/pull/6677) (daemon-core drain
    with a `/drain` endpoint, three-state `/health`, persistence in
    `drain.json`, open since 2026-08-10). Ours holds drain in memory only and
    uses `/drain` and `/resume`, so the APIs differ.
  - Fit: operator tooling for self-hosters (maintenance without failing
    in-flight agent work), which upstream's own reviewer judged right for the
    product on #5631. The obstacle is the open PRs, not the vision.

### Drain badge (backend + web)

Shows drain state on the runtimes page: "Draining · N running" while a patched
daemon is draining, "Idle" once it is drained and empty (safe to restart or
upgrade). A patched daemon attaches an `ag_drain` report
(`draining`, `idle`, `active_tasks`) to every heartbeat, over HTTP and
WebSocket. The backend merges it into `agent_runtime.metadata` under `ag_drain`
with a JSONB `||`, the way `offline_reason` is merged: no column and no
migration. It writes only when the report differs from what is stored (two
guarded `UPDATE`s, rows-affected tells whether anything changed), so a steady
heartbeat costs no query; the WebSocket path remembers the stored state on the
connection's runtime lease, and re-runs the guarded no-op `UPDATE` on a
connection's first beat and every 60s, draining or not (the HTTP pending-work
hint heartbeat writes the key behind the lease's memo, so a resume would
otherwise leave a stale "Draining"): one no-op query per WS runtime per minute.
A heartbeat without the field (stock or Homebrew daemon) leaves metadata alone, and registration's wholesale metadata overwrite
clears a stale key. A change publishes the existing `daemon:register` event
(`action: "drain"`) so open runtimes pages refetch. The web side parses the key
through a zod schema with `parseWithFallback`; an offline runtime shows no badge.
Needs the backend image from `ag-patches/build-backend.sh`, and the self-host on
v0.6.2. A server-triggered update refused because the daemon is draining shows as
a failed update result, not as drain state: the badge reports drain only.
Depends on the daemon drain core patch above.

- Test prefix: `TestAGPatch` / `AG patch:`. Hermetic ones
  (`server/internal/agdrain`, `server/internal/daemon`, `server/internal/daemonws`, `server/pkg/protocol`, views, core) run in
  `ag-patches/test.sh`; the DB-backed ones in `server/internal/handler` need
  Postgres (see README)
- Files:
  - `server/internal/agdrain/agdrain.go` (new), `agdrain_test.go` (new)
  - `server/pkg/protocol/ag_drain_test.go` (new)
  - `server/internal/handler/ag_drain.go` (new), `ag_drain_test.go` (new)
  - `server/internal/handler/daemon.go` (request field and two hooks)
  - `server/internal/daemonws/hub.go` (lease memo, report into the handler context),
    `ag_drain_test.go` (new)
  - `server/pkg/protocol/messages.go` (`DaemonDrainReport`, heartbeat field)
  - `server/pkg/db/queries/runtime.sql` and `server/pkg/db/generated/runtime.sql.go`
    (`SetAgentRuntimeAGDrain`, `ClearAgentRuntimeAGDrain`; regenerated with sqlc)
  - `server/internal/daemon/ag_drain.go` (`agDrainReport`),
    `ag_drain_report_test.go` (new), `client.go`, `daemon.go`, `wakeup.go`
    (send the report)
  - `packages/core/runtimes/ag-drain.ts` (new), `ag-drain.test.ts` (new),
    `packages/core/package.json` (export)
  - `packages/views/runtimes/components/drain-badge.tsx` (new),
    `drain-badge.test.tsx` (new), `runtime-list.tsx`, `runtime-list-drain.test.tsx` (new),
    `runtime-detail.tsx`, `runtime-detail-drain.test.tsx` (new)
  - `packages/views/locales/*/runtimes.json` (`drain` group), `packages/views/locales/ag-drain.test.ts` (new)
  - `ag-patches/build-backend.sh` (new), `ag-patches/lib.sh` (new, release gate shared with `build-web.sh`)

- Upstream:
  - Status: `fork-only`. Upstream is building the same feature in the open
    [#6679](https://github.com/multica-ai/multica/pull/6679) (server: runtime draining status, heartbeat
    keep-alive, `runtime_drained` attribution) and [#6678](https://github.com/multica-ai/multica/pull/6678)
    (desktop daemon draining UI), on top of [#6677](https://github.com/multica-ai/multica/pull/6677). Their server
    side adds a migration; ours merges an `ag_drain` key into
    `agent_runtime.metadata` and needs none.
  - Overlap: #6679, #6678, #6677 above, all open and last touched in August.
    Drop this patch once the drain state is shown upstream.
  - Fit: the metadata-only design is a stopgap that avoids a migration on the
    self-host, not a shape upstream would want next to its own column-backed
    status. Offer nothing; watch the three PRs.

### Daemon handoff: machine-wide slot cap and single-instance fixes (in review)

Makes the daemon safe to run as two processes on one machine, the groundwork for
a zero-downtime handoff (overlap and handoff modes are the next unit and extend
this entry). Four changes, no server or DB change:

- **Machine-wide slot cap.** The in-process channel of slot indices is replaced
  by one lock file per slot, `<config dir>/slots/<k>.lock` for k in
  `[0, MULTICA_DAEMON_MAX_CONCURRENT_TASKS)`, taken with a non-blocking
  exclusive flock for the life of the task. The kernel drops the lock when the
  holder exits, so a crashed daemon strands nothing. Total running agents across
  daemons stay within the cap and `MULTICA_TASK_SLOT` is unique across the
  machine. The slot dir is per machine (shared by every profile of one config
  root). A miss falls into the existing `capacityBackoff`. The slot dir or a
  lock file that can't be opened fails closed: no claim, one warning per backoff
  cycle, no in-process fallback.
- **PID file owner check.** `daemon start --foreground` and `daemon stop` remove
  `daemon.pid` only while it still holds their PID, so an outgoing daemon can't
  delete its successor's file.
- **Cross-process repo cache lock.** Each per-repo `repoLock` also holds a flock
  on `<bare repo>.multica-lock`, taken inside the in-process lock and released
  with it, so two daemons never fetch, prune or GC one bare repo at once. The
  high/low-priority behaviour is unchanged in-process. Fail closed: if the lock
  file can't be opened the operation returns that error and git doesn't run.
  Another daemon's maintenance can't be preempted, so a foreground checkout
  waits for it to finish. The lock files are left in place (unlinking a flock
  file another daemon may hold is unsafe), so an evicted repo's workspace cache
  dir keeps them.
- **Heavy git GC waits for other daemons.** `reflog expire` and `git gc` are
  skipped while any slot lock is held by another process, as well as while this
  daemon has active tasks. A probe error counts as busy.

On Windows (no flock) the slot set and repo locks stay in-process, as upstream.
Needs a CLI built with `ag-patches/build-cli.sh`.

- Test prefix: `TestAGPatch` / `AG patch:` (run by `ag-patches/test.sh`, which
  now also runs `server/internal/daemon/repocache`). The two-process slot-cap
  test re-executes the test binary as a helper process that holds slots.
- Files:
  - `server/internal/daemon/ag_slotlock.go`, `ag_slotlock_unix.go`,
    `ag_slotlock_other.go` (new: slot locks, wait loop, GC probe)
  - `server/internal/daemon/ag_slotlock_test.go`, `ag_slotlock_extra_test.go` (new)
  - `server/internal/daemon/daemon.go` (`agSlotDir` and `agSlots` fields,
    `pollLoop`, `runBatchPoller`; the old channel helpers `newTaskSlotSemaphore`,
    `waitForTaskSlot`, `drainAvailableSlots` moved to `ag_slotlock.go`)
  - `server/internal/daemon/gc.go` (two heavy-GC checks call
    `agOtherDaemonBusy`)
  - `server/internal/daemon/repocache/ag_repolock.go`, `ag_repolock_unix.go`,
    `ag_repolock_other.go` (new) and `ag_repolock_test.go`,
    `ag_repolock_extra_test.go` (new)
  - `server/internal/daemon/repocache/cache.go` (`agFlock` field, hooks in
    `LockContext`, `Unlock`, `tryLockMaintenance`, `lockForRepo`)
  - `server/cmd/multica/ag_daemon_pidfile.go`, `ag_daemon_pidfile_test.go` (new)
  - `server/cmd/multica/cmd_daemon.go` (two PID-file removals call
    `agRemoveDaemonPIDIfOwned`)
  - Upstream tests adjusted: `daemon_test.go`, `runtime_isolation_test.go`,
    `terminal_report_queue_test.go`, `ag_drain_test.go` (slot semaphore API and
    a temp slot dir for `pollLoop`), `repocache/cache_test.go` (real temp bare
    paths, since the repo lock now needs a lock file beside the repo),
    `gc_repo_evict_test.go` (tolerates the lock file)
  - `ag-patches/test.sh` (`repocache` package added, `AG_GO_MIN_TESTS` 45)

- Upstream:
  - Status: `fork-only`. It exists to let two daemons overlap during a handoff,
    which upstream's drain-then-stop work does not do, and the overlap model is
    not something to offer upstream yet. The single-instance fixes (PID file
    owner check, repo cache flock) are self-contained and could be offered
    separately as bug fixes once the handoff units have landed and proven out.
  - Overlap: [#5631](https://github.com/multica-ai/multica/pull/5631) and
    [#6677](https://github.com/multica-ai/multica/pull/6677) (drain-then-stop,
    not overlap). #6677's server-side "draining" runtime status would clash with
    two processes sharing one runtime row.
  - Fit: operator tooling for self-hosters (upgrade without a gap in service),
    in the same spirit as drain. The machine-wide slot cap changes how every
    daemon bounds concurrency, which upstream would want agreed first.

## Tooling (not a patch)

`ag-patches/` (including `build-web.sh`, `build-backend.sh`, `build-cli.sh` and
`lib.sh`) and `.github/workflows/ag-patches.yml` exist only on the patch
branches. They do not touch product code. Landed in
[#1](https://github.com/alantreadway/multica/pull/1). The guard also runs the Go
`TestAGPatch*` daemon, CLI and drain-badge tests (local `go`, else docker
`golang:<server/go.mod version>`).

## Builds

| Git tag | Commit | Web image | Backend image | CLI | Built |
| --- | --- | --- | --- | --- | --- |
| `ag/v0.6.2-ag.1` | `c59bdb71d0713897e19371e4d11d4c316c56c71b` | `multica-web-ag:v0.6.2-ag.1` | none (stock backend) | none (stock) | 2026-10-14 |
| `ag/v0.6.2-ag.2` | `0ae4d6edc3c9651e1cfb5d2c711d062469828ab7` | `multica-web-ag:v0.6.2-ag.2` | `multica-backend-ag:v0.6.2-ag.2` | `dist/multica-v0.6.2-ag.2-darwin-arm64` (installed as `~/.local/bin/multica-ag`) | 2026-10-16 |

The CLI column is the `dist/multica-<tag>-ag.<n>-darwin-arm64` binary from
`ag-patches/build-cli.sh`; `none (stock)` means the daemon runs the upstream
`multica`. Backend images are built with `ag-patches/build-backend.sh`.

`ag.2` is the first build with the drain patch and the drain badge. The self-host
backend and web run it (upgraded from stock v0.6.0 to v0.6.2 on 2026-10-16). The
launchd daemon still runs Homebrew `multica` until its plist is pointed at
`multica-ag`, so the Runtimes page shows no drain badge until then.
