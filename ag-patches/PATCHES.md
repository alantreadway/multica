# AG patches

Patch inventory for the `ag/<tag>` branch. Update this file whenever a patch
lands, is dropped or is reworked during a rebase.

## Upstream base

- Tag: `v0.6.5`
- Commit: `654b8ca954034f8181eb6bbb4fc298dede760156`

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

Last checked 2026-10-23, twice: at the rebase onto `v0.6.5` (every
overlapping PR and issue re-read, and the `v0.6.3..v0.6.5` commits read for
anything that replaces a patch: none), and again after the deploy, when the
ranking below was added. Since the 2026-10-19 check, [#9279](https://github.com/multica-ai/multica/issues/9279) closed as completed
by [#9332](https://github.com/multica-ai/multica/pull/9332), upstream's REST snapshot rewrite, which forced a port of the PR
snapshot extras rather than a drop; [#9190](https://github.com/multica-ai/multica/pull/9190) opened as the fix for [#9176](https://github.com/multica-ai/multica/issues/9176);
and [#8679](https://github.com/multica-ai/multica/pull/8679) and RFC [#6650](https://github.com/multica-ai/multica/issues/6650) are active. None merged or was superseded. The full
preflight from the `multica-upstream-contribution` skill last ran 2026-10-16:
live `VISION.md` and `CONTRIBUTING.md`, plus issue and PR searches, open and
closed. Read-only: nothing has been opened, filed or commented on upstream.
Re-check on every rebase (README, "Rebase onto a new upstream tag").

### Ranking

Rows are ordered by how likely an upstream PR built from the patch would be
merged. Some patches appear in two parts where only one part is worth offering.

- **Fit:** how closely the patch matches what upstream has asked for or already
  ships: open requests, reactions, maintainer comments, precedent in the code.
- **Likelihood:** the chance a PR would merge, given its route (a bug fix can go
  straight to a PR; a feature needs a Feature Request agreed first), its size,
  competing PRs and the maintainers' stated position. For scale, about 55% of
  external PRs merge, at a median of 26h, and most of the rest stall unreviewed
  (the `multica-upstream-contribution` skill's evidence, 2026-10-15).
- **Next step:** what to do if we offer it. Nothing is opened upstream without
  alan's go-ahead for that item.

| # | Patch | Status | Overlapping upstream | Fit | Likelihood | Next step |
| --- | --- | --- | --- | --- | --- | --- |
| 6 | Tree-ordered parent lanes | `candidate` | [#2966](https://github.com/multica-ai/multica/pull/2966) (open, adjacent, no reviews, last touched 2026-07-07) | Medium: improves a shipped view | Medium | Feature Request, after reading #2966. |
| 7 | Lane drag rules and jump-to-lane chip | `candidate` (with tree lanes) | [#6704](https://github.com/multica-ai/multica/pull/6704), [#7904](https://github.com/multica-ai/multica/pull/7904) (open, edit the same file) | Medium | Medium (drag rules), low (jump chip) | Ride along with the tree lanes; offer the drag rules first. |
| 9 | Tree view mode (outline) | `candidate` | [#5685](https://github.com/multica-ai/multica/issues/5685) (open request, 3 👍), [#2966](https://github.com/multica-ai/multica/pull/2966) (open, overlaps) | Medium | Medium-low | After the tree lanes; read #2966 first. |

`VISION.md` is a statement of direction (humans and agents as one team, work and
decisions kept visible), not a feature list. None of the patches conflicts with
it and none advances it directly; the fit lines below stay modest because
upstream closes PRs it reads as out of scope.

## Patches

Test names for every patch start with `AG patch:` so they are easy to find and
to confirm survived a rebase (`grep -rn "AG patch:" packages/`).

Fork code stays out of upstream files where it can, so a rebase conflicts less:

- AG tests live in sibling files (`*.ag.test.ts(x)`, `ag_*_test.go`) beside the
  upstream test they extend. Upstream test files keep only edits a patch's
  behaviour forces (each is listed under its patch).
- New queries go in `server/pkg/db/queries/ag_*.sql`, so sqlc writes them to
  `ag_*.sql.go` instead of upstream's generated files.
- UI strings live in the `ag` i18n namespace, `packages/views/locales/<lang>/ag.json`
  (keys mirror the upstream namespace they sit beside, e.g.
  `issues.swimlane.jump_to_lane`). `locales/ag.ts` adds it to `RESOURCES` and
  `i18n/ag-resources-types.ts` to `I18nResources`, through one hook line each in
  `locales/index.ts` and `i18n/resources-types.ts`. Upstream's locale JSON is
  unchanged. The tree-ordered lanes patch carries this scaffolding.
- `ApiClient` methods are in `packages/core/api/ag-client.ts`, installed onto
  the class by one hook line at the end of `client.ts`.

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
  - `packages/views/issues/components/ag-swimlane-lane-header.tsx` (new: the
    header's ancestor path and hidden sub-lane count)
  - `packages/views/issues/components/swimlane-view.tsx` (hooks)
  - `packages/views/issues/components/swimlane-view.tree-lanes.ag.test.tsx` (new)
  - `packages/core/issues/queries.ts` (append only),
    `queries.tree-lanes.ag.test.ts` (new)
  - The `ag` i18n namespace: `packages/views/locales/ag.ts`,
    `locales/{en,fr,ja,ko,zh-Hans}/ag.json`, `i18n/ag-resources-types.ts` (new);
    one hook line in `locales/index.ts` and in `i18n/resources-types.ts`
    (`ag:issues.swimlane`)

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
  - `packages/views/issues/components/ag-swimlane-lane-drag.ts` (new: the
    sibling-lane collision rule, jump-to-lane scroll),
    `ag-jump-to-lane-chip.tsx` (new)
  - `packages/views/issues/components/swimlane-view.tsx`, `board-card.tsx` (hooks)
  - `packages/views/issues/components/swimlane-view.lane-drag.ag.test.tsx` (new)
  - `packages/views/locales/*/ag.json` (`issues.swimlane.jump_to_lane`)

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
  - Tree cases in siblings of upstream tests (new):
    `save-view-dialog.tree-mode.ag.test.tsx`, `actor-issues-panel.ag.test.tsx`,
    `project-detail.ag.test.tsx`, `issue-statuses-tab.ag.test.tsx`,
    `issue-surface.ag.test.tsx`
  - `packages/core/issues/stores/tree-view-store.ts`,
    `tree-view-store.test.ts` (new)
  - `packages/core/issues/stores/view-store.ts` (`ViewMode`, card options)
  - `packages/core/issues/queries.ts` (append only: `IssueAncestryNode` keeps
    `title` and `status`), `queries.tree-view.ag.test.ts` (new),
    `queries.tree-lanes.ag.test.ts`
  - `packages/views/issues/surface/types.ts`,
    `use-issue-surface-controller.ts`, `issue-surface.tsx` (mode plumbing)
  - `packages/views/issues/components/issues-header.tsx`, `issues-page.tsx`,
    `save-view-dialog.tsx`, `packages/views/my-issues/components/my-issues-header.tsx`,
    `my-issues-page.tsx`, `packages/views/projects/components/project-detail.tsx`
  - `packages/views/locales/*/ag.json` (`issues.view.tree`,
    `issues.view.tooltip_tree`, `issues.tree`)

- Upstream:
  - Status: `candidate`. Read-only check, 2026-10-16.
  - Overlap: open request [#5685](https://github.com/multica-ai/multica/issues/5685) for a tree view and closed request
    [#4180](https://github.com/multica-ai/multica/issues/4180); [#9266](https://github.com/multica-ai/multica/pull/9266) nests sub-issues on the board, not a
    tree. Open [#2966](https://github.com/multica-ai/multica/pull/2966) (hierarchy views) also adds a tree view, so it overlaps and
    must be read before proposing this. No upstream implementation has merged.
  - Fit: a new view mode, frontend only, no new dependency. `VISION.md` says
    nothing about views. Feature work, so it goes through a Feature Request
    first, after reading #2966 so we don't race it.

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
| `ag/v0.6.3-ag.1` | `d19013ab84e81751d031bb894f374ccae6afbffe` | `multica-web-ag:v0.6.3-ag.1` | `multica-backend-ag:v0.6.3-ag.1` | `dist/multica-v0.6.3-ag.1-darwin-arm64` | 2026-10-19 |
| `ag/v0.6.3-ag.2` | `9b15ccc316ded86a419f0eb7f532227ff0ae3f22` | `multica-web-ag:v0.6.3-ag.2` | `multica-backend-ag:v0.6.3-ag.2` | `dist/multica-v0.6.3-ag.2-darwin-arm64` | 2026-10-20 |
| `ag/v0.6.5-ag.1` | `bc4a44822385e3e04407e38cf19a9c8c10d0ba6b` | `multica-web-ag:v0.6.5-ag.1` | `multica-backend-ag:v0.6.5-ag.1` | `dist/multica-v0.6.5-ag.1-darwin-arm64` (installed as `~/.local/bin/multica-ag`) | 2026-10-23 |

The CLI column is the `dist/multica-<tag>-ag.<n>-darwin-arm64` binary from
`ag-patches/build-cli.sh`; `none (stock)` means the daemon runs the upstream
`multica`. Backend images are built with `ag-patches/build-backend.sh`.

`ag.2` is the first build with the drain patch and the drain badge. The self-host
backend and web run it (upgraded from stock v0.6.0 to v0.6.2 on 2026-10-16). The
launchd daemon still runs Homebrew `multica` until its plist is pointed at
`multica-ag`, so the Runtimes page shows no drain badge until then.

`v0.6.3-ag.1` is the first build on `v0.6.3`, with every patch merged into
`ag/v0.6.2` up to PR #30. It was deployed on 2026-10-19: the upgrade from
`v0.6.2-ag.2` applied upstream's `573`–`582` and the fork's `9001` and `9002` for
the first time.

`v0.6.3-ag.2` adds the Claude quota stack ported in PR #31 (Claude pacing, the
quota report and runtime backup setting, the backup failover, and the Runtimes
quota section and sidebar warning) and the `orgId` account identity fix (PR #32).
It applies no migration. Deployed 2026-10-20 after a `pg_dump` backup
(`~/multica-selfhost/backups/multica-pre-v0.6.3-ag.2-20261020-162210.sql.gz`):
backend, then frontend, both recreated from the `ag.2` images. The previous CLI stays beside the new one as
`~/.local/bin/multica-ag.v0.6.3-ag.1`. The launchd daemon picks up the new
`multica-ag` at its next idle auto-reload (or `~/bin/multica-daemon-reload`); the
Runtimes page has no quota report or backup status until it does. The backup
toggle is off until switched on from the runtime page.

`v0.6.5-ag.1` is the first build on `v0.6.5`: every patch on `ag/v0.6.3` up to
PR #34 (task GC meta), rebased, with the PR snapshot extras ported onto
upstream's REST snapshot. Built in the `agbuild` Colima profile (the self-host
VM does not mount the repo) and loaded into the self-host VM. Deployed
2026-10-23 after a `pg_dump` backup
(`~/multica-selfhost/backups/multica-pre-v0.6.5-ag.1-20261023-090833.sql.gz`;
`.env` saved as `.env.pre-v0.6.5-ag.1`): `MULTICA_IMAGE_TAG` to `v0.6.5`, backend
(applied upstream's `583` and `584`), then frontend. The previous CLI stays
beside the new one as `~/.local/bin/multica-ag.v0.6.3-ag.2`.

Full Go suite against a scratch Postgres and Redis (as a non-root user) passed
except two upstream failures that stock `v0.6.5` shows in the same container:
`TestClaimTasksForRuntimes_EmptyReclaimDefersHintForRetry` (timing flake, 12
of 40 runs failed on stock) and four Cursor background-process tests in
`pkg/agent` (`TestCaptureCursorBackground*`, `TestCursorBackgroundLifecycle`).
