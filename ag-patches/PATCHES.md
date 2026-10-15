# AG patches

Patch inventory for the `ag/<tag>` branch. Update this file whenever a patch
lands, is dropped or is reworked during a rebase.

## Upstream base

- Tag: `v0.6.2`
- Commit: `7ab92d7622ecd81d179a8d83ae2dea74835a5069`

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

### Lane drag rules and jump-to-lane chip (landed)

Drag rules between swimlanes and a chip that jumps to the related lane.
Status: landed in [#3](https://github.com/alantreadway/multica/pull/3).
Depends on the tree-ordered lanes patch.

- Test prefix: `AG patch:`
- Files:
  - `packages/views/issues/components/swimlane-view.tsx`
  - `packages/views/issues/components/swimlane-view.test.tsx`

### Daemon drain mode (in progress)

The daemon can be put into drain: it stops claiming new tasks, lets running
tasks finish, reports `idle` on `/health`, and resumes on request. Held in
memory only, so any restart comes back claiming; self-reload, auto-update and
server-triggered update refuse to restart while draining and say so in
`reload_pending_reason`. This unit is the daemon core (`POST /drain`,
`POST /resume`, `/health` fields `draining`, `drain_started_at`, `idle`); the
CLI commands and the web badge follow in later units.
Overlaps upstream multica-ai/multica#5631 and #6677: drop this patch if either
merges.

- Test prefix: `TestAGPatch` / `AG patch:` (run by `ag-patches/test.sh` and the
  `guard` job; the script asserts a minimum number of passing tests)
- Files:
  - `server/internal/daemon/ag_drain.go` (new)
  - `server/internal/daemon/ag_drain_test.go` (new)
  - `server/internal/daemon/daemon.go` (one-line hooks: `agDrain` field,
    `tryEnterClaim`, `trySetClaimBarrier`, `tryBeginServerUpdate`,
    `handleUpdate`, `pollLoop`)
  - `server/internal/daemon/health.go` (routes and `/health` fields)

## Tooling (not a patch)

`ag-patches/` and `.github/workflows/ag-patches.yml` exist only on the patch
branches. They do not touch product code. Landed in
[#1](https://github.com/alantreadway/multica/pull/1). The guard also runs the
Go `TestAGPatch*` daemon tests (local `go`, else docker `golang:<server/go.mod
version>`).

## Builds

| Git tag | Commit | Image | Built |
| --- | --- | --- | --- |
| `ag/v0.6.2-ag.1` | `c59bdb71d0713897e19371e4d11d4c316c56c71b` | `multica-web-ag:v0.6.2-ag.1` | 2026-10-14 |
