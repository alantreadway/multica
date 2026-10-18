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
day; the polling patch's entry checked the same day), using the preflight from the `multica-upstream-contribution` skill: live
`VISION.md` and `CONTRIBUTING.md`, plus issue and PR searches, open and closed.
Read-only: nothing has been opened, filed or commented on upstream. Re-check on
every rebase (README, "Rebase onto a new upstream tag").

| Patch | Status | Overlapping upstream |
| --- | --- | --- |
| Tree-ordered parent lanes | `candidate` | [#2966](https://github.com/multica-ai/multica/pull/2966) (open, adjacent) |
| Lane drag rules and jump-to-lane chip | `candidate` (with tree lanes) | [#6704](https://github.com/multica-ai/multica/pull/6704), [#7904](https://github.com/multica-ai/multica/pull/7904) (open, edit the same file) |
| Tree view mode (outline) | `candidate` | [#2966](https://github.com/multica-ai/multica/pull/2966) (open PR, overlaps), [#5685](https://github.com/multica-ai/multica/issues/5685) (open request) |
| Daemon drain mode | `fork-only` | [#5631](https://github.com/multica-ai/multica/pull/5631), [#6677](https://github.com/multica-ai/multica/pull/6677) (open) |
| Drain badge (backend + web) | `fork-only` | [#6679](https://github.com/multica-ai/multica/pull/6679), [#6678](https://github.com/multica-ai/multica/pull/6678), [#6677](https://github.com/multica-ai/multica/pull/6677) (open) |
| Daemon handoff: machine-wide slot cap and single-instance fixes | `fork-only` | [#5631](https://github.com/multica-ai/multica/pull/5631), [#6677](https://github.com/multica-ai/multica/pull/6677) (open, drain-then-stop, not overlap) |
| GitHub PR polling (token mode) | `candidate` | [#8111](https://github.com/multica-ai/multica/issues/8111) (open feature request, same need) |
| Daemon handoff (overlap mode) | `fork-only` | [#5631](https://github.com/multica-ai/multica/pull/5631), [#6677](https://github.com/multica-ai/multica/pull/6677) (open, drain-then-stop, not overlap) |
| Claude quota tracker and auto-hold (daemon) | `fork-only` (the `rate_limit_event` parsing alone is a `candidate`) | [#4123](https://github.com/multica-ai/multica/pull/4123), [#8931](https://github.com/multica-ai/multica/pull/8931) (open), [#6255](https://github.com/multica-ai/multica/issues/6255), #8965 (closed), #8969 (withdrawn) |
| Claude pacing: 80% knee, weekly curve, EWMA cap (daemon) | `fork-only` | [#6011](https://github.com/multica-ai/multica/pull/6011) (adaptive admission, open), [#8331](https://github.com/multica-ai/multica/pull/8331) (static caps, closed unmerged), [#7015](https://github.com/multica-ai/multica/issues/7015), [#4205](https://github.com/multica-ai/multica/issues/4205) (ask for static caps) |

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
  - `packages/views/issues/components/swimlane-band.ts` (new)
  - `packages/views/issues/components/swimlane-band.test.ts` (new)
  - `packages/views/issues/components/swimlane-view.tsx`
  - `packages/views/issues/components/swimlane-view.test.tsx`
  - `packages/core/issues/queries.ts` (append only)

Nesting is shown by an alternating band, not an indent: every root lane and its
sub-lanes share one band (`data-lane-band="odd|even"`, odd tinted with
`bg-muted/60`), consecutive roots alternate, and the pinned lanes (No parent,
Other parents) have none and don't count. The band tints the header row and
the gutters; each cell sits on an opaque `bg-background` underlay, so the band
never shows through the cells. Lane headers all start at the same
x; a nested header shows `↳ <parent identifier>` with the full path in the
tooltip. Lanes inside one tree sit `pt-2` apart, a new root `pt-4`. Non-parent
groupings are unchanged. `SwimlaneTreeInfo` gained `rootRawId` and
`rootOrdinal`. Rollback: switch the web image back.

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

### Tree view mode (outline)

A new issue view mode `tree` beside board, list, table, swimlane and gantt, on
`/issues`, `/my-issues` and project pages (not the actor panel). The same filter
bar and sort apply; the rows are an outline of the parent / sub-issue hierarchy
with ├ └ connectors, a status icon, identifier, title, assignee, `done/total`
sub-issue progress and the agent-working dot. Each node expands and collapses
(children load page by page when a parent opens), and expand all opens to depth 2
at most (and no more than 100 nodes). A node's focus action makes it the root,
with a breadcrumb back up. A root whose parent the filters removed shows that
hidden ancestor path dimmed (`MAG-1 › MAG-2 ›`); clicking a crumb focuses that
ancestor. While a node is focused, its subtree is fetched without the filter-bar
filters, search or surface scope (sort stays, sub-issues stay on), because the
server only returns a branch whose parent passes the filters and a hidden
ancestor would otherwise come back empty; the bar shows "Filters are paused
while focused" with a clear-focus action, and leaving focus restores the
filtered tree. Filter counts are those of the filtered view. Peek and open work
as in List. Frontend only: it reuses the Table row
query (`hierarchy: { enabled: true }`, one parent per request, pages of 50), so
filtering, sort, counts and WebSocket invalidation match the other modes, and
tree mode forces `include_sub_issues` on. Expand state persists per surface
scope in `multica_issue_tree_view`; focus lives in the surface controller
(`controller.tree`) so a follow-on layout can read the same rows. A stored
`tree` mode on a surface that does not offer it falls back to that surface's
first mode. Rollback: switch the web image back to a build without this patch.

- Test prefix: `AG patch:`
- Files:
  - `packages/views/issues/components/tree-view.tsx` (new)
  - `packages/views/issues/components/use-tree-rows.ts` (new)
  - `packages/views/issues/components/tree-rows.ts` (new)
  - `packages/views/issues/components/tree-rows.test.ts`,
    `tree-view.test.tsx`, `issues-header.tree-mode.test.tsx` (new)
  - `packages/views/issues/surface/tree-mode.test.tsx`,
    `tree-mode-surfaces.test.tsx`, `packages/views/locales/ag-tree.test.ts` (new)
  - Tree cases added to the existing `save-view-dialog.test.tsx`,
    `actor-issues-panel.test.tsx`, `project-detail.test.tsx`,
    `issue-statuses-tab.test.tsx` and `issue-surface.test.tsx`
  - `packages/core/issues/stores/tree-view-store.ts`,
    `tree-view-store.test.ts` (new)
  - `packages/core/issues/stores/view-store.ts` (`ViewMode`, card options)
  - `packages/core/issues/queries.ts` (append only: `IssueAncestryNode` keeps
    `title` and `status`), `queries.test.ts`
  - `packages/views/issues/surface/types.ts`,
    `use-issue-surface-controller.ts`, `issue-surface.tsx` (mode plumbing)
  - `packages/views/issues/components/issues-header.tsx`, `issues-page.tsx`,
    `save-view-dialog.tsx`, `packages/views/my-issues/components/my-issues-header.tsx`,
    `my-issues-page.tsx`, `packages/views/projects/components/project-detail.tsx`
  - `packages/views/locales/*/issues.json` (`view.tree`, `view.tooltip_tree`,
    `tree` group)

- Upstream:
  - Status: `candidate`. Read-only check, 2026-10-16.
  - Overlap: open request [#5685](https://github.com/multica-ai/multica/issues/5685) for a tree view and closed request
    [#4180](https://github.com/multica-ai/multica/issues/4180); [#9266](https://github.com/multica-ai/multica/pull/9266) nests sub-issues on the board, not a
    tree. Open [#2966](https://github.com/multica-ai/multica/pull/2966) (hierarchy views) also adds a tree view, so it overlaps and
    must be read before proposing this. No upstream implementation has merged.
  - Fit: a new view mode, frontend only, no new dependency. `VISION.md` says
    nothing about views. Feature work, so it goes through a Feature Request
    first, after reading #2966 so we don't race it.

### Tree mode org-chart layout

Tree mode gains an Outline | Chart choice in the Display menu (the view menu,
under the view list, only while Tree is the active view). Chart draws a top-down
org chart: a hand-rolled tidy-tree layout (per-subtree contours, siblings pushed
apart only as far as needed, parent centred over its children; no `d3-hierarchy`,
no new dependency), elbow connectors, and pan / zoom by a CSS transform (drag the
background, wheel pans, ctrl / pinch / buttons zoom, `+` `-` `0` and arrows on the
keyboard, Fit button). It reads the outline's rows, so filters, sort, paging,
focus, peek and the node's focus action behave as in Outline; a node's count chip
opens and closes its branch. Nodes at depth 2 and below start collapsed (and only
the first 20 children of a branch open by default, so opening the chart never
fans out into a request per sibling); the toolbar has "Show next level",
"Collapse all" and "Reset". Above 200 visible issues the chart draws nothing and
asks the user to collapse or focus deeper, with a way back to Outline.

The chart fits itself on every layout or size change for the current root until
the user first adjusts the view (pan, zoom, Fit, expand or collapse); after that
it never auto-fits that root again, and re-rooting starts afresh. It does not try
to detect when loading has finished.

Where it is available: from a focused node anywhere tree mode runs, and on a
project page without focusing (the project is a virtual top node over the
project's root issues, subject to the current filters). On `/issues` and
`/my-issues` with nothing focused, Chart is disabled with a hint. The choice
persists per surface in `multica_issue_tree_layout`; it only applies while the
chart is available, so clearing focus on `/issues` shows the outline. The chart
keeps its own open state (scoped to the surface and root, reset when the root
changes), so the outline's expanded nodes and focus are untouched. Frontend only.
Rollback: switch the web image back to a build without this patch.

- Test prefix: `AG patch:`
- Files:
  - `packages/views/issues/components/tree-chart-layout.ts`,
    `tree-chart-viewport.ts`, `tree-chart-view.tsx`, `tree-layout-context.tsx`,
    `tree-layout-menu.tsx` (new)
  - `packages/views/issues/components/tree-chart-layout.test.ts`,
    `tree-chart-layout.invariants.test.ts`, `tree-chart-viewport.test.ts`,
    `tree-chart-view.test.tsx`, `issues-header.tree-layout.test.tsx` (new)
  - `packages/views/issues/surface/tree-chart-mode.test.tsx`,
    `tree-chart-surface.test.tsx` (new)
  - `packages/core/issues/stores/tree-layout-store.ts`,
    `tree-layout-store.test.ts`, `tree-layout-store.persist.test.ts` (new)
  - Hooks into tree mode's own files: `use-tree-rows.ts` (layout, chart open
    state), `tree-rows.ts` (`flattenTree` accepts an open rule), `tree-view.tsx`
    (shared `TreeToolbar`)
  - One-line hooks in upstream files: `issues-header.tsx` (menu section),
    `issue-surface.tsx` (layout switch and context provider),
    `use-issue-surface-controller.ts` (`projectRoot`)
  - `packages/views/locales/*/issues.json` (`tree.layout_*`, `tree.chart_*`),
    `locales/ag-tree.test.ts`

- Upstream:
  - Status: `fork-only` for now; it extends the tree view patch above and would
    follow it, so it is not a separate proposal.
  - Overlap: none found beyond the tree view entry (open [#2966](https://github.com/multica-ai/multica/pull/2966) is an outline, not a chart).
  - Fit: a second renderer for a view that does not exist upstream yet. Offer it
    only after the outline lands, and only if asked.

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

### Daemon handoff (overlap mode) (in review)

A new daemon starts and claims while the old one drains its running tasks and
exits once it has none left: zero-downtime replacement. No server or DB change.
Two parts. First, groundwork that makes the daemon safe to run as two processes
on one machine (slot locks, single-instance fixes, repo cache lock, GC gate).
Second, the handoff and overlap modes on top.

**Groundwork:**

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

**Handoff and overlap modes:**

- **Handoff on the old daemon** (`daemon drain --handoff`, and `--handoff` on
  `daemon stop|restart`; `POST /handoff` on the health port). Implies drain. Then
  it stops heartbeating (HTTP and WebSocket) and registering (workspace sync,
  agent discovery, runtime-gone recovery, the register calls themselves), so
  runtime liveness and heartbeat-carried requests belong to the successor. It
  writes `daemon.handoff.json` (PID and port; a normal start beside a live one
  refuses, since its startup recovery would fail the draining tasks), releases
  `daemon.lock`, moves its logging to `daemon.handoff.<pid>.log`
  (through a switchable writer, so the daemon's logger is not replaced), skips
  `deregisterRuntimes` on exit and exits when idle, but not before
  `agHandoffMinHold` (30s) so an idle daemon doesn't vanish before the successor
  reads its `/health`. It keeps reporting for its own tasks and keeps serving its
  health port. One-way: `/resume` answers 409 and `daemon resume` prints why.
  It also skips GC cycles for the whole drain (the successor's task dirs would
  look orphaned to it), answers `/handoff` only after claims already in flight
  have become tracked tasks (bounded at 3s, so the successor's first `/health`
  read lists them), and refuses `/handoff` with 409 while it is itself still
  overlapping its own predecessor.
  Upstream's `MULTICA_DAEMON_HANDOFF` env var and self-spawn restart path (binary
  update handoff) are untouched.
- **Overlap on the new daemon** (`daemon start --overlap`). Before writing its
  port or PID file it reads the predecessor's PID, port, task IDs and cap
  (`/health` gained `handoff`, `max_concurrent_tasks`, `active_task_ids` and, on
  an overlap daemon, `predecessor_pid`). It skips the startup recover-orphans
  loop and the runtime-gone one while the predecessor lives, and skips GC cycles
  (the predecessor's task dirs would look orphaned) and self-restarts (a
  restart would recover-orphans). It claims through the slot locks. The
  background launcher doesn't write `daemon.pid` in overlap mode (the child
  reads the old one first and writes its own).
- **Fail closed.** `--overlap` exits non-zero, before registering runtimes, when
  there is no live PID in the PID file, when the predecessor's `/health` can't be
  read or answers with another PID, when it isn't in handoff mode, or when its
  cap is missing or differs from this daemon's (both values named). Never a
  fallback to a normal start.
- **Predecessor death.** While the predecessor runs, the successor polls its PID
  and `/health` every 2s and keeps the task-ID set current (it only shrinks). A
  transient failure with the PID alive changes nothing. If the PID dies with
  tasks left, only those IDs are failed through the daemon's terminal-report fail
  path (reason `runtime_offline`, message names the predecessor's exit
  mid-handoff), best effort per task. Then overlap ends and the daemon is
  normal. Windows can't probe a PID, so it counts failed `/health` reads
  instead.
- **GC preemption.** A running heavy `reflog expire` / `git gc` step watches the
  slot probe every 500ms and is cancelled with `repocache.ErrMaintenancePreempted`
  as soon as another daemon holds a slot (probe errors count as busy). The
  step is retried on a later cycle.
- **Same cap.** `/health` reports `max_concurrent_tasks`; an overlap start with
  a different or missing cap refuses, because a daemon with a larger N could take
  slots at or above the smaller one's N.
- **Reload script.** `ag-patches/multica-daemon-reload` alternates two launchd
  labels (`…multica-daemon.a` / `.b`), hands the running one off, bootstraps the
  other with `--overlap` (through a one-shot marker file read by the plist's
  command line) and never boots the draining label out. Not installed by any
  script here; setup is in the README ("Daemon/CLI patch").

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
  - Handoff and overlap:
    - `server/internal/daemon/ag_handoff.go`, `ag_handoff_unix.go`,
      `ag_handoff_other.go` (new: handoff state, `/handoff`, overlap loop,
      predecessor-death handling, GC step preemption)
    - `server/internal/daemon/daemon.go` (`agHandoff` field; task ID tracking in
      `pollLoop`; `go d.agOverlapLoop`; hooks in `deregisterRuntimes`,
      `runHeartbeatTick`, `handlePendingWorkHint`, `syncWorkspacesFromAPI`,
      `handleRuntimeGone`, the two register calls and the two recover-orphans
      loops), `wakeup.go` (`sendWSHeartbeats`), `agents_refresh.go` (two
      discovery entry points), `gc.go` (`runGC` skip, heavy-step context),
      `health.go` (`agHandoffHealth`, `/handoff`), `ag_drain.go` (`/resume`
      refusal, restart deferral)
    - `server/cmd/multica/ag_daemon_handoff.go` (new: flags, predecessor
      discovery, log switch, `stop|restart --handoff`),
      `ag_daemon_drain.go` (`drain --handoff`, 409 message, status rows),
      `cmd_daemon.go` (one-line hooks in the launcher, `runDaemonForeground`,
      `runDaemonStop`, `runDaemonRestart`)
    - `ag-patches/multica-daemon-reload` (new), `ag-patches/README.md`
    - Tests: `server/internal/daemon/ag_handoff_test.go`,
      `server/internal/daemon/ag_handoff_run_test.go` (new: an overlap daemon
      driven through `Run`),
      `server/cmd/multica/ag_daemon_handoff_test.go` (new: handoff and overlap
      behaviour, predecessor discovery, the CLI flags; predecessor death and GC
      step preemption are in `server/internal/daemon/ag_handoff_test.go`),
      `server/cmd/multica/ag_daemon_handoff_run_test.go` (new: the handoff hook
      and the plain-start guard driven through `runDaemonForeground`) and `server/cmd/multica/ag_reload_script_test.go` (new: runs
      `multica-daemon-reload` against stub `launchctl`, `plutil` and `multica`;
      the cases that run the script need `bash` and `jq` and skip without them)
    - `ag-patches/test.sh` (`AG_GO_MIN_TESTS` 70)

- Upstream:
  - Status: `fork-only`. It lets two daemons overlap during a handoff, which
    upstream's drain-then-stop work does not do, and the overlap model is not
    something to offer upstream yet. The single-instance fixes (PID file owner
    check, repo cache flock) are self-contained and could be offered separately
    as bug fixes once the handoff has proven out.
  - Overlap: [#5631](https://github.com/multica-ai/multica/pull/5631) and
    [#6677](https://github.com/multica-ai/multica/pull/6677) (drain-then-stop,
    not overlap). #6677's server-side "draining" runtime status would clash with
    two processes sharing one runtime row.
  - Fit: operator tooling for self-hosters (upgrade without a gap in service),
    in the same spirit as drain. The machine-wide slot cap changes how every
    daemon bounds concurrency, which upstream would want agreed first.

### GitHub PR polling, token mode (backend)

Lets a self-host without a GitHub App keep PR cards, issue linking, merge
auto-complete and the `--until-pr checks` / `--until-pr merged` wakeups.
Set `GITHUB_POLL_TOKEN` (a personal access token, e.g. `gh auth token`) and a
poller reads `GET /repos/{o}/{r}/pulls?state=all&sort=updated&direction=desc`
every `GITHUB_POLL_INTERVAL` (default `60s`, floor `15s`) for each GitHub
`owner/repo` listed in any workspace's `repos`. It sends `If-None-Match`, so an
unchanged list is a 304. Each PR updated since the repo's in-memory cursor is
mapped onto the webhook payload and mirrored through `mirrorPullRequestForWorkspace`
for every workspace that lists the repo (same link policy as a webhook), then
handed to `PRRefresh.Enqueue` for its CI and mergeability snapshot. The first
pass after a start scans one page (100 PRs); an already-merged PR first seen
that way is treated as a merge. Rows the poller writes carry `installation_id = 0`.
No schema change and no migration.

The snapshot client (`ghsnapshot.Client`) takes the token too: with no App vars
it sends the token on every GraphQL call for any installation id.

When it runs:

- App vars unset, `GITHUB_POLL_TOKEN` set: poller on (token mode).
- App configured: poller off, and the client is unchanged, unless
  `GITHUB_POLL_ENABLED=true` and `GITHUB_POLL_TOKEN` are both set. Then the
  poller runs next to the webhooks. **installation_id choice:** polled rows are
  written with `0`, as in token mode, and the App client uses the poll token for
  installation `0` only (every real installation still mints App tokens). Chosen
  over looking up each workspace's installation row because a repo can be
  reachable by the token and not by that installation. In this opt-in mode only, a PR
  whose stored row in a workspace already carries a real installation id (an App
  webhook wrote it) is skipped for that workspace, so the poll never downgrades the
  id, zeroes the diff stats or strands `check_suite`/`check_run` refreshes. The
  poller only fills PRs the webhooks have not seen, and refreshes a PR only when it
  owns a row for it. In token mode no webhook arrives, so the poll takes such rows
  over (rewrites them to installation `0`) and refreshes them.
- Neither: nothing runs. An empty or whitespace `GITHUB_POLL_TOKEN` is unset.

Failure policy (never crashes): a GitHub error, timeout or 5xx on a repo logs a
warning, skips that repo for the tick and keeps its cursor and ETag. A 401/403
logs one error per tick and the loop goes on. A rate-limit answer
(`X-RateLimit-Remaining: 0`, `Retry-After`, or a secondary-limit body) pauses
the poller until the reset (at most an hour). A failed mirror for one workspace
is logged, the others carry on, and the cursor stays before that PR (its ETag is
dropped) so it retries. Entries in `repos` that are not on github.com are
skipped. The token is never logged.

Known limits: the list endpoint has no diff stats, so polled PRs show 0
additions, 0 deletions and 0 files. A PR is re-mirrored when its `updated_at`
equals the cursor second (GitHub timestamps are whole seconds, so `>` would miss
a later change in the same second); the upsert makes that idempotent. CI state
changes do not touch a PR's `updated_at`: the snapshot's chase backoff, the
10-minute sweep and page visits keep it fresh. Web Code settings still reads
"Not connected" with no App; the PR toggles there work.

- Test prefix: `TestAGPatch` / `AG patch:`. Hermetic ones (`ghpoll`,
  `ghsnapshot`) run in `ag-patches/test.sh`; the DB-backed ones in
  `server/internal/handler` and `server/internal/service` need Postgres (see
  README)
- Files:
  - `server/internal/integrations/ghpoll/` (new: `config.go`, `repos.go`,
    `poller.go`, `ag_ghpoll_test.go`, `ag_ghpoll_policy_test.go`)
  - `server/internal/integrations/ghsnapshot/ag_static_token.go` (new),
    `ag_static_token_test.go`, `ag_static_token_policy_test.go` (new),
    `client.go` (hooks: `staticToken` field, `NewClientFromEnv`, `Enabled`,
    `installationToken`)
  - `server/internal/handler/ag_ghpoll.go` (new: `AGStartGitHubPoll`, the
    sink), `ag_ghpoll_test.go`, `ag_ghpoll_flow_test.go`, `ag_ghpoll_refresh_test.go` (new)
  - `server/internal/service/ag_ghpoll_wakeup_test.go` (new: DB-backed, the
    `--until-pr checks` / `merged` wakeups on a polled row)
  - `server/cmd/server/main.go` (one-line hook next to `PRRefresh.Start`)
  - `ag-patches/docker-compose.github-poll.yml` (new, tooling: passes the three
    variables to the backend), `ag-patches/test.sh` (new packages, minimum 69)

- Upstream:
  - Status: `candidate`. Nothing is proposed; upstream wants a Feature Request
    agreed first.
  - Overlap: open feature request [#8111](https://github.com/multica-ai/multica/issues/8111) ("Allow GitHub
    integration via `gh` command and polling", enhancement, last touched
    2026-10-01) asks for the same fallback: a polling GitHub integration for
    people who cannot install an App. It proposes the `gh` CLI; ours is a server
    poller with a token. No PR for it. [#5006](https://github.com/multica-ai/multica/pull/5006) (merged) already
    gives Forgejo, Gitea and GitLab a token-based provider in `vcs_connection`;
    that is the shape a clean upstream version would take, and this patch keeps
    to the `github_pull_request` tables to avoid a migration.
  - Fit: removes a stated adoption blocker for self-hosters without an App, and
    adds no dependency. Add our design to #8111 only if alan says so.

### Sidebar agent and drain status (web)

One compact row at the top of the sidebar footer, above Analytics: "3 agents ·
4 running · 2 queued" with a pulsing brand dot while anything runs ("No agents
running" when idle, a muted dash while loading, never 0). While any runtime is
draining, a second line adds "Draining · N running", or "Drained · safe to
restart" once every draining runtime is idle; the counts always stay. Hover lists
the working agents (`WorkingAgentsHoverContent`) and each draining runtime;
click goes to Agents, or to Runtimes while draining. It reads the presence map
and runtime list that `useWorkspacePresencePrefetch` already keeps warm over
WebSocket, so it adds no request and no polling. The drain half needs the
daemon patch running (`multica-ag`); without it the row shows agent counts only.
Frontend only: rollback is switching the web image back.

- Test prefix: `AG patch:` (`layout/agent-status-indicator.test.tsx`, in `ag-patches/test.sh`)
- Files:
  - `packages/views/layout/agent-status-indicator.tsx` (new),
    `agent-status-indicator.test.tsx` (new)
  - `packages/views/layout/app-sidebar.tsx` (one import, one JSX line),
    `app-sidebar.test.tsx` (mocks the indicator)
  - `packages/views/locales/*/layout.json` (`agent_status` group)

- Upstream:
  - Status: `fork-only`. The drain half depends on the drain patches above, which
    are fork-only; the agent-count half reads existing presence data.
  - Overlap: no open upstream PR or issue found for a sidebar agent or running
    status row (searched `multica-ai/multica` PRs, October 2026).
  - Fit: the agent-count half could go upstream alone, as a small sidebar
    addition with no new request. The drain marker would come out first.

### Claude quota tracker and auto-hold (daemon)

Every headless `claude` run emits a `rate_limit_event` on stream-json; upstream
drops it. The patch surfaces it through `ExecOptions.AGOnRateLimit` and keeps
per-account state in the daemon, keyed by the run's effective
`CLAUDE_CONFIG_DIR` (an agent's own `custom_env` value is its own account):
each window's utilisation and `resetsAt`, the status and when it was last seen.
On `status: rejected` the account is held until `resetsAt` + 2 minutes. While the
daemon's own (default) account is held, the batch claim leaves out Claude
runtimes (`rt.Provider == "claude"` or protocol family `claude`); Codex, Cursor
and the rest keep claiming, running tasks are never touched, and the hold clears
itself at reset. A hold on another account is tracked and shown in `daemon
status` as `..., not gating` (and with `gating: false` in `/health`) but does not
gate claims, because the claim cannot know which account a task will use.

State is machine-wide in `<~/.multica>/ag_quota.json` (beside the slot dir):
atomic writes, re-read when the mtime or size changes (once per claim cycle),
merged per account and window by newest `observed_at`; holds and `quota clear`
overrides carry timestamps and the latest wins, so a hold survives a restart or a
handoff. `/health` gains `claude_quota` (default-account hold, each account's
windows and last seen, dropped-event count; each hold carries `gating`). `multica daemon quota clear` (POST
`/quota/clear`) ends the hold; `daemon status` shows `Claude: quota hold
(five_hour) until 21:10`. The failed-task report carries `ag_retry_not_before`
(RFC 3339), the `resetsAt` of the `rejected` event that run saw, for the
server-side retry in a later unit; an unpatched server ignores it. Needs a CLI
built with `ag-patches/build-cli.sh`. No DB migration, no backend change.

Fail policy: no event, no state, or no `utilization` is open (no hold from it);
a garbled or cut-off event is dropped, counted and warned about at most once per
10 minutes; `rejected` with an unknown or past `resetsAt` holds for 1 hour with
reason `reset_unknown`, which the next non-rejected event or `quota clear` ends;
an unreadable or corrupt state file runs on in-memory state with a warning, and
the next write replaces it.

- Test prefix: `TestAGPatch` / `AG patch:` (run by `ag-patches/test.sh`; it now
  also runs `./pkg/agent`)
- Files:
  - `server/pkg/agent/ag_ratelimit.go`, `ag_ratelimit_test.go` (new: parser and
    the stream-hook tests), `testdata/claude-code-2.1.299-rate-limit-allowed.jsonl`
    (captured), `testdata/claude-code-rate-limit-rejected.jsonl` (synthetic),
    `testdata/README-ag-rate-limit.md`
  - `server/pkg/agent/agent.go` (`AGOnRateLimit` field), `claude.go` (the
    `rate_limit_info` field, the `rate_limit_event` case, the garbled-line hook)
  - `server/internal/daemon/ag_quota.go`, `ag_quota_store.go`,
    `ag_quota_test.go`, `ag_quota_edge_test.go`, `ag_quota_runtask_test.go`
    (new; the last drives `runTask` with a test-created `claude` printing the
    rejected fixture)
  - `server/internal/daemon/daemon.go` (one-line hooks: `agQuota` field, the
    observer after the `ExecOptions` literal, `agQuotaClaim` in the batch poller),
    `health.go` (embedded `agQuotaHealth`, `/quota/clear` route), `client.go`
    (`agAddRetryNotBefore` in the fail body)
  - `server/cmd/multica/ag_daemon_quota.go`, `ag_daemon_quota_test.go` (new:
    `daemon quota clear`, status rows); `ag_daemon_drain.go` appends the rows in
    `agDaemonStatusRows`
  - `ag-patches/test.sh` (`./pkg/agent`, `AG_GO_MIN_TESTS`)

On Claude Code 2.1.299 a plain `status: allowed` event does carry
`unifiedWindows.<window>.utilization` (captured fixture above), so utilisation is
available on every run, not only near a limit. No real `rejected` frame has been
captured yet; that fixture is synthetic.

- Upstream:
  - Status: `fork-only`. Parsing `rate_limit_event` alone is a `candidate`:
    nothing upstream parses it.
  - Overlap: [#4123](https://github.com/multica-ai/multica/pull/4123) (runtime
    hold/resume, open); [#8931](https://github.com/multica-ai/multica/pull/8931)
    (provider quota pools, open); [#6255](https://github.com/multica-ai/multica/issues/6255)
    (stop accepting work per account); the dispatch-gate design is pending
    (#8965 closed, #8969 withdrawn). Last checked 2026-10-17.
  - Fit: quota-aware dispatch is operator tooling for self-hosters on a
    subscription plan; the obstacle is the open overlapping work and the missing
    dispatch-gate design, not the vision.

### Claude pacing: 80% knee, weekly curve, EWMA cap (daemon)

Builds on the quota tracker above. From the daemon's own Claude account's 5-hour
and 7-day windows it derives a cap C on Claude runs in flight, and the batch
claim starts at most `C - active` more. `active` counts Claude runs on this
account (running ones, tasks claimed until their dispatch goroutine exits, and
during a handoff overlap the predecessor's); runs on an agent's own `CLAUDE_CONFIG_DIR` are
tracked per account but do not count. C limits new claims only; running tasks
are never touched, and it is read every claim cycle, so no restart is needed.

- Session: full `MULTICA_DAEMON_MAX_CONCURRENT_TASKS` while 5h utilisation is
  below 80%; above that `n = (0.95 - u) / (T * b)` runs, to land on 95% at the reset.
- Weekly: full max while utilisation is at or under the linear curve
  `c = min(elapsed fraction of 7d, 0.97)`; over it `n = (0.97 - u) / (T * b)`.
- `b` is the burn per run in fraction/hour: an EWMA (alpha 0.3) of du/dt over
  the time-weighted mean active runs (at least 1), sampled when utilisation
  changes or 15 minutes after the last sample. A negative du is dropped; a
  sample with no change and under 0.05 runs on average is skipped so idle time
  does not decay the estimate.
- C = clamp(floor(min(n_5h, n_7d)), 1, Max). C moves one step toward that at most
  every 10 minutes (the first step after a quiet period is immediate). When a
  window rolls over or a hold clears, C jumps straight to the target.
- Split claim: with headroom `h = C - active` below the free slots, Claude
  runtimes claim with `max_tasks = h`, then the other runtimes claim the
  remaining slots; the two results are merged. No headroom drops the Claude
  runtimes from the claim, like a hold.
- `/health` gains `ag_quota.active_claude` and `ag_quota.effective_cap`. Utilisation
  and resets come from the shared `ag_quota.json`; burn estimates and C are
  per process.
- Handoff: the successor is the only daemon that claims during an overlap. It
  adds the predecessor's `ag_quota.active_claude`, read from the `/health` it
  already polls, to its own count: the last known value when the read fails,
  all of the predecessor's tasks when it never reported one (an older build),
  and only its own once the overlap ends. The first `ag_quota.effective_cap` it
  reads (clamped to 1..Max) seeds its limiter, so it never starts above the
  predecessor's pace.

Fail policy: no windows or no state is Max; where the formula applies and the
burn is unknown C holds its position, and a fresh process (a restart, or a
successor before its first read of the predecessor's cap) starts that position
at 1; an expired window constrains nothing; C
never goes below 1, so a lone run keeps refreshing utilisation. The maths takes
the account as a parameter (`agPaceEvaluate`), so a backup account can reuse it.

- Test prefix: `TestAGPatch` / `AG patch:` (`TestAGPatchPace*`, incl. `TestAGPatchPaceSpec*`; worked examples
  A-H from the design as table tests)
- Files:
  - `server/internal/daemon/ag_quota_pace.go`, `ag_quota_pace_test.go`,
    `ag_quota_pace_spec_test.go`, `ag_quota_pace_start_test.go` (new)
  - `server/internal/daemon/ag_quota.go` (`agQuotaClaim` applies C,
    `agQuotaObserver` also returns a `done` func, the observe hook, `/health`),
    `ag_handoff.go` (reads the predecessor's `ag_quota` block in the overlap
    step; `agUntrackTask` releases the task's claim), `daemon.go` (the `agPace` field; the observer hook is now
    `defer d.agQuotaWire(...)()`)
  - `ag_quota_test.go`, `ag_quota_edge_test.go` (the observer's second return value)
  - `ag-patches/test.sh` (`AG_GO_MIN_TESTS`)

- Upstream:
  - Status: `fork-only`.
  - Overlap: [#6011](https://github.com/multica-ai/multica/pull/6011) (adaptive
    admission, open) is the nearest. [#8331](https://github.com/multica-ai/multica/pull/8331)
    (static per-provider caps) was closed unmerged; [#7015](https://github.com/multica-ai/multica/issues/7015)
    and [#4205](https://github.com/multica-ai/multica/issues/4205) ask for static
    caps. Last checked 2026-10-18.
  - Fit: pacing against a subscription's rolling windows is operator tooling
    for self-hosters. Upstream has so far preferred static caps or generic
    admission control over provider-specific quota maths.

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
