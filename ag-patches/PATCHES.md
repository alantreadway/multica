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
tasks finish, reports `idle` on `/health` (no active tasks, no claim in flight,
no update installing), and resumes on request. Held in memory only, so any
restart comes back claiming; `triggerRestart` refuses while draining
(self-reload, auto-update and server-triggered update all go through it) and
says so in `reload_pending_reason`. Auto-update and server-triggered update
also skip the upgrade itself when draining starts first. The daemon core
(`POST /drain`, `POST /resume`, `/health` fields `draining`, `drain_started_at`,
`idle`) and the CLI commands (`daemon drain [--wait] [--timeout]`, `daemon
resume`, `daemon stop|restart --drain [--timeout]`, extra `daemon status` lines)
are in; the web badge follows in a later unit. Needs a CLI built with
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
    `daemon restart` call `agDrainBeforeStop`, `printDaemonStatusReport` adds
    `agDaemonStatusRows`)
  - `ag-patches/build-cli.sh` (new, tooling: builds the CLI binary)

## Tooling (not a patch)

`ag-patches/` (including `build-web.sh` and `build-cli.sh`) and
`.github/workflows/ag-patches.yml` exist only on the patch branches. They do not
touch product code. Landed in [#1](https://github.com/alantreadway/multica/pull/1).
The guard also runs the Go `TestAGPatch*` daemon and CLI tests (local `go`, else
docker `golang:<server/go.mod version>`).

## Builds

| Git tag | Commit | Image | CLI | Built |
| --- | --- | --- | --- | --- |
| `ag/v0.6.2-ag.1` | `c59bdb71d0713897e19371e4d11d4c316c56c71b` | `multica-web-ag:v0.6.2-ag.1` | none (stock) | 2026-10-14 |

The CLI column is the `dist/multica-<tag>-ag.<n>-darwin-arm64` binary from
`ag-patches/build-cli.sh`; `none (stock)` means the daemon runs the upstream
`multica`. The first build that includes the drain patch gets a row with its CLI
filled in.
