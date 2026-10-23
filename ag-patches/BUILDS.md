# AG builds

The records that change at every rebase and build: the upstream base, the
upstream checks, and each build with its deploy. The patch inventory is
[`PATCHES.md`](./PATCHES.md). Only the tooling commit touches this file, so a
change to it is a `fixup! chore(ag-patches): patch branch tooling, guard and
docs` commit, which the next rebase folds in (README, "Branch layout").

## Upstream base

- Tag: `v0.7.1`
- Commit: `aec9b0b1699939a6d993da9ca2ebd2c62900a8e0`

## Upstream checks

At each rebase: `ag-patches/upstream-status.sh`, then read the new upstream
commits for anything that replaces a patch (README, "Rebase onto a new upstream
tag"). Read-only: nothing has been opened, filed or commented on upstream.
Newest first:

- **2026-10-28, rebase onto `v0.7.1`.** `v0.7.0..v0.7.1` read: nothing replaces
  a patch, and no linked PR or issue changed state. `v0.7.1` adds a Traditional
  Chinese (`zh-Hant`) locale, so every patch with `ag.json` strings now ships a
  `zh-Hant/ag.json` too.
- **2026-10-23, rebase onto `v0.7.0`.** `v0.6.5..v0.7.0` read: none.
  [#8679](https://github.com/multica-ai/multica/pull/8679) closed unmerged, with
  no comment and no successor PR. `v0.7.0` enforces runtime ownership on the
  daemon routes (#9423), which the drain, quota and claim-filter reports ride
  on; no patch adds a daemon route, so nothing moved.
- **2026-10-23, rebase onto `v0.6.5`.** The ranking in PATCHES.md was added.
  Since the 2026-10-19 check,
  [#9279](https://github.com/multica-ai/multica/issues/9279) closed as completed
  by [#9332](https://github.com/multica-ai/multica/pull/9332), upstream's REST
  snapshot rewrite, which forced a port of the PR snapshot extras rather than a
  drop; [#9190](https://github.com/multica-ai/multica/pull/9190) opened as the
  fix for [#9176](https://github.com/multica-ai/multica/issues/9176); and RFC
  [#6650](https://github.com/multica-ai/multica/issues/6650) is active. None
  merged or was superseded.
- **2026-10-16.** The full preflight from the `multica-upstream-contribution`
  skill: live `VISION.md` and `CONTRIBUTING.md`, plus issue and PR searches,
  open and closed.

## Builds

| Git tag | Commit | Web image | Backend image | CLI | Built |
| --- | --- | --- | --- | --- | --- |
| `ag/v0.6.2-ag.1` | `c59bdb71d0713897e19371e4d11d4c316c56c71b` | `multica-web-ag:v0.6.2-ag.1` | none (stock backend) | none (stock) | 2026-10-14 |
| `ag/v0.6.2-ag.2` | `0ae4d6edc3c9651e1cfb5d2c711d062469828ab7` | `multica-web-ag:v0.6.2-ag.2` | `multica-backend-ag:v0.6.2-ag.2` | `dist/multica-v0.6.2-ag.2-darwin-arm64` | 2026-10-16 |
| `ag/v0.6.3-ag.1` | `d19013ab84e81751d031bb894f374ccae6afbffe` | `multica-web-ag:v0.6.3-ag.1` | `multica-backend-ag:v0.6.3-ag.1` | `dist/multica-v0.6.3-ag.1-darwin-arm64` | 2026-10-19 |
| `ag/v0.6.3-ag.2` | `9b15ccc316ded86a419f0eb7f532227ff0ae3f22` | `multica-web-ag:v0.6.3-ag.2` | `multica-backend-ag:v0.6.3-ag.2` | `dist/multica-v0.6.3-ag.2-darwin-arm64` | 2026-10-20 |
| `ag/v0.6.5-ag.1` | `bc4a44822385e3e04407e38cf19a9c8c10d0ba6b` | `multica-web-ag:v0.6.5-ag.1` | `multica-backend-ag:v0.6.5-ag.1` | `dist/multica-v0.6.5-ag.1-darwin-arm64` | 2026-10-23 |
| `ag/v0.7.0-ag.1` | `1e7aedc1e87f3675273b843135d5874543f34f21` | `multica-web-ag:v0.7.0-ag.1` | `multica-backend-ag:v0.7.0-ag.1` | `dist/multica-v0.7.0-ag.1-darwin-arm64` | 2026-10-23 |
| `ag/v0.7.0-ag.2` | `14e845ac6531ca412a2d2f7b0083c832f35893c5` | `multica-web-ag:v0.7.0-ag.1` (unchanged) | `multica-backend-ag:v0.7.0-ag.2` | `dist/multica-v0.7.0-ag.1-darwin-arm64` (unchanged) | 2026-10-27 |
| `ag/v0.7.1-ag.1` | `50892196d782143219ace0de349771b03c1b3061` | `multica-web-ag:v0.7.1-ag.1` | `multica-backend-ag:v0.7.1-ag.1` | `dist/multica-v0.7.1-ag.1-darwin-arm64` | 2026-10-28 |

The CLI column is the binary from `ag-patches/build-cli.sh`, installed as
`~/.local/bin/multica-ag`; `none (stock)` means the daemon ran the upstream
`multica`. Since `v0.7.1-ag.1` a deploy is `ag-patches/deploy-selfhost.sh`
(README, "Deploy to the self-host"); a note below says only what the script
does not.

Notes, newest first. Notes for builds before `v0.7.0` are in git history
(`git log -p -- ag-patches/PATCHES.md`).

- **`v0.7.1-ag.1`**, the first build on `v0.7.1`: the 23 patches, the task GC
  meta fix, the PR snapshot run fields (from `v0.7.0-ag.2`) and the live token
  rate (first deploy), rebased. One conflict: an import in
  `connect-remote-dialog.tsx` (upstream added `useDaemonSetupCommands`). Each
  patch with `ag.json` strings gained a `zh-Hant/ag.json` (Traditional Chinese
  in upstream's terms: 智慧體, 執行時, 載入, 檢視); the quota patch's
  `connect-remote-dialog.ag-quota.test.tsx` now sets the daemon config ready,
  since the dialog waits for it. Deployed 2026-10-28 by hand (backup
  `multica-pre-v0.7.1-ag.1-20261028-100534.sql.gz`); applied upstream's `593`
  and `594`. The daemon moved over with `multica-daemon-reload` (handoff).
  Full Go suite: the same five upstream failures as stock `v0.7.1`.
- **`v0.7.0-ag.2`**, backend only: `v0.7.0-ag.1` plus the PR snapshot run
  fields (migration `9003`). Deployed 2026-10-27 by re-tagging
  `multica-backend-ag:v0.7.0-ag.2` as `multica-backend-ag:v0.7.0`.
- **`v0.7.0-ag.1`**, the first build on `v0.7.0`. Two conflicts: the label
  row's import in `table-view.tsx` (upstream added `DeferredTooltip`), and in
  `daemon.go`'s `runTask` the backup-account resume now runs just before
  upstream's new worktree delivery repair, so a run the backup completes still
  gets the delivery check. Upstream's runtime ownership check (#9423) broke nine
  DB-backed `TestAGPatch*` handler tests, which now authenticate as the runtime
  owner (`newOwnerDaemonRequest`), as upstream's own tests do. Deployed
  2026-10-23; applied upstream's `585`–`592`.
