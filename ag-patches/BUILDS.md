# AG builds

The records that change at every rebase and build: the upstream base, the
upstream checks, and each build with its deploy. The patch inventory is
[`PATCHES.md`](./PATCHES.md). Only the tooling commit touches this file, so a
change to it is a `fixup! chore(ag-patches): patch branch tooling, guard and
docs` commit, which the next rebase folds in (README, "Branch layout").

## Upstream base

- Tag: `v0.8.1`
- Commit: `b1ea927def6490c43bedff537b6f4ed9a47fa828`

## Upstream checks

At each rebase: `ag-patches/upstream-status.sh`, then read the new upstream
commits for anything that replaces a patch (README, "Rebase onto a new upstream
tag"). Read-only: nothing has been opened, filed or commented on upstream.
Newest first:

- **2026-11-09, rebase onto `v0.8.1`.** `v0.7.1..v0.8.1` read. Nothing replaces
  a patch. [#9734](https://github.com/multica-ai/multica/pull/9734) is in
  `v0.8.0`, so its cherry-pick commit was dropped.
  [#4205](https://github.com/multica-ai/multica/issues/4205) closed with
  [#9795](https://github.com/multica-ai/multica/pull/9795), a static
  runtime-wide concurrency limit, which leaves Claude pacing in place.
  [#4416](https://github.com/multica-ai/multica/pull/4416) is in the base.
  "Multica 1.0 part 1" ([#9635](https://github.com/multica-ai/multica/pull/9635))
  replaced the sidebar with an app rail and space panels and the view menu with
  the Display panel: the agent status row moved into the space-panel footer,
  Overview and Decisions became rail spaces, the Tree layouts moved into the
  Display panel, and the save-view dialog's label-display chooser went with the
  dialog. `@multica/ui/icons` replaced `lucide-react` in views. `v0.8.0` adds a
  Spanish (`es`) locale, so every patch with `ag.json` strings now ships an
  `es/ag.json` too.
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
| `ag/v0.7.1-ag.2` | `616d6fd2b4e1f35b9c762a867d3641a29b9dc240` | `multica-web-ag:v0.7.1-ag.2` | `multica-backend-ag:v0.7.1-ag.2` | `dist/multica-v0.7.1-ag.2-darwin-arm64` | 2026-11-05 |
| `ag/v0.7.1-ag.3` | `f360bbcc3dc57f2170f46005085a445fe39be804` | `multica-web-ag:v0.7.1-ag.3` | `multica-backend-ag:v0.7.1-ag.2` (unchanged) | `dist/multica-v0.7.1-ag.2-darwin-arm64` (unchanged) | 2026-11-05 |
| `ag/v0.7.1-ag.4` | `596f2bc083d920f8719e6d84821bec370b326608` | `multica-web-ag:v0.7.1-ag.4` | `multica-backend-ag:v0.7.1-ag.4` | `dist/multica-v0.7.1-ag.4-darwin-arm64` | 2026-11-05 |
| `ag/v0.7.1-ag.5` | `985104c1556012f8675911a3fcf7e44afff11e73` | `multica-web-ag:v0.7.1-ag.5` | `multica-backend-ag:v0.7.1-ag.5` | `dist/multica-v0.7.1-ag.5-darwin-arm64` | 2026-11-05 |
| `ag/v0.7.1-ag.6` | `4e869772002a8696dfc96e512803700807a8852a` | `multica-web-ag:v0.7.1-ag.6` | `multica-backend-ag:v0.7.1-ag.6` | `dist/multica-v0.7.1-ag.6-darwin-arm64` | 2026-11-05 |
| `ag/v0.7.1-ag.7` | `32953c629ff2f24cf99d2a81965c781569f4b383` | `multica-web-ag:v0.7.1-ag.7` | `multica-backend-ag:v0.7.1-ag.7` | `dist/multica-v0.7.1-ag.7-darwin-arm64` | 2026-11-06 |
| `ag/v0.7.1-ag.8` | `e282400141904c27d5f7707e75981d47f4f6e498` | `multica-web-ag:v0.7.1-ag.8` | `multica-backend-ag:v0.7.1-ag.7` (unchanged) | `dist/multica-v0.7.1-ag.7-darwin-arm64` (unchanged) | 2026-11-06 |
| `ag/v0.7.1-ag.9` | `1773042681a957cd8588c8670b7f8e4ef1063b7d` | `multica-web-ag:v0.7.1-ag.9` | `multica-backend-ag:v0.7.1-ag.7` (unchanged, retagged `ag.9`) | `dist/multica-v0.7.1-ag.9-darwin-arm64` | 2026-11-06 |
| `ag/v0.7.1-ag.10` | `692b25e75313cf20784a2dc39e6de847474645d0` | `multica-web-ag:v0.7.1-ag.10` | `multica-backend-ag:v0.7.1-ag.10` | `dist/multica-v0.7.1-ag.10-darwin-arm64` | 2026-11-06 |
| `ag/v0.7.1-ag.11` | `fe9b98e6e70aaa065216b88a95bd451d87a19c69` | `multica-web-ag:v0.7.1-ag.10` (unchanged, retagged `ag.11`) | `multica-backend-ag:v0.7.1-ag.10` (unchanged, retagged `ag.11`) | `dist/multica-v0.7.1-ag.11-darwin-arm64` | 2026-11-06 |
| `ag/v0.7.1-ag.12` | `dfb1ca24591e55a21d322a58abab57214986bce8` | `multica-web-ag:v0.7.1-ag.12` | `multica-backend-ag:v0.7.1-ag.12` | `dist/multica-v0.7.1-ag.12-darwin-arm64` | 2026-11-06 |
| `ag/v0.7.1-ag.13` | `69c86a82fcc28349594c1d82188b5cf31227db4b` | `multica-web-ag:v0.7.1-ag.13` | `multica-backend-ag:v0.7.1-ag.13` | `dist/multica-v0.7.1-ag.13-darwin-arm64` | 2026-11-06 |

The CLI column is the binary from `ag-patches/build-cli.sh`, installed as
`~/.local/bin/multica-ag`; `none (stock)` means the daemon ran the upstream
`multica`. Since `v0.7.1-ag.1` a deploy is `ag-patches/deploy-selfhost.sh`
(README, "Deploy to the self-host"); a note below says only what the script
does not.

Notes, newest first. Notes for builds before `v0.7.0` are in git history
(`git log -p -- ag-patches/PATCHES.md`).

- **`v0.7.1-ag.13`**: urgent issues on the backup before a hold. An "Urgent issues
  on the backup now" switch in the quota card's backup block stores
  `backup_urgent_until` (the primary's weekly reset); until then the daemon claims
  urgent issues onto the backup first, at `backup_max_concurrent` with no pacing,
  and the rest of the claim stays paced on the primary. The sidebar shows "Urgent
  on backup until <time>". Guard: 1024 views, 645 core and 297 Go patch tests
  passed; full Go suite: the five known upstream failures only. No migrations.
  Deployed 2026-11-06; the daemon handed over `.b` to `.a`.
- **`v0.7.1-ag.12`**: the Overview dashboard (patch 28): an `Overview` page
  above Inbox with what runs now, the queue, what waits on you, units in a state
  longer than their p90, and pinned work trees split into known scope (stage
  strip, ETA range from the per-unit stage method) and unknown scope signals.
  Migrations `9014`–`9017` add `ag_overview_pin` and `ag_overview_forecast` (one
  row per pinned tree per day). Guard: 1019 views, 644 core and 296 Go patch
  tests passed. The CLI has no change but was rebuilt for the deploy script.
  Deployed 2026-11-06 with `--skip-daemon`; the daemon moves to `ag.12` at its
  next idle auto-reload.
- **`v0.7.1-ag.11`**, CLI only: a quick-created issue's first Claude run resumes
  in the quick-create task's directory when its resumed session came from that
  task (it fell back to a copied transcript, about 75% first-call cache), and a
  new daemon in overlap mode asks the server for each of the predecessor's tasks
  before failing them, so a run that finished during the handoff no longer logs
  "predecessor exited with tasks still running". Guard: 1013 views, 631 core
  and 296 Go patch tests passed. The `ag.10` backend and web images were tagged
  `ag.11`; both containers kept running. Deployed 2026-11-06; the daemon handed
  over `.a` to `.b`.
- **`v0.7.1-ag.10`**: the Claude pacing override. A "Pace Claude runs" switch on
  the Runtimes quota card stores `pace_off`; while it is off the daemon claims up
  to its max (a quota hold still applies) and the sidebar shows "Claude pacing
  off". Guard: 1013 views, 631 core and 295 Go patch tests passed; full Go suite:
  the five known upstream failures only, after updating the `ag_settings`
  wire-format test for the new field. No migrations. Deployed 2026-11-06; the
  daemon handed over `.b` to `.a`.
- **`v0.7.1-ag.9`**, web and CLI: weekly Claude pacing over working hours
  (`MULTICA_AG_PACE_WORK_HOURS="Mon-Fri 08:00-19:00"`, added to both launchd
  plists), the sidebar's "Claude paced: 1 of 5 · weekly 91%" line, and the
  token hover card's runtime name on its own line. Guard: 1009 views, 629 core
  and 294 Go patch tests passed. The backend image did not change; `ag.7`'s was
  tagged `ag.9` so `deploy-selfhost.sh` could run as usual, and compose left the
  backend container running. Deployed 2026-11-06; the daemon handed over `.a`
  to `.b`.
- **`v0.7.1-ag.8`**, web only: the Decisions page redesign. Compact rows beside
  the selected card's detail, number-key and `j`/`k`/`z` shortcuts, and a
  clicked card animating out of the queue into an Activity list with an undo
  toast. Guard: 1006 views, 626 core and 293 Go patch tests passed. Deployed
  2026-11-06 like `ag.3`: the image loaded into the self-host's VM as
  `multica-web-ag:v0.7.1` and only the frontend recreated (`up -d --no-deps
  frontend`). Backend, CLI, daemon and database did not change.
- **`v0.7.1-ag.7`**: the Claude resume patches merged on GitHub (#39, #40, #41)
  and the Decision Desk line built locally since `ag.4`, in one branch. The 22
  local commits were rebased onto `origin/ag/v0.7.1` at `764bc43eb`; the only
  conflict was `PATCHES.md`, where both sides added sections, and both were kept.
  Full Go suite: the four known Cursor failures only. Deployed 2026-11-06 with
  `--skip-daemon` (backup `multica-pre-v0.7.1-ag.7-20261106-105225.sql.gz`, no
  new migrations). The running daemon had been started outside launchd at its
  idle auto-reload, so neither label owned it and `multica-daemon-reload` would
  have seen no daemon. It was handed over by the script's overlap steps run by
  hand: `daemon drain --handoff`, the overlap marker, then `bootout` and
  `bootstrap` of label `.a`.
- **`v0.7.1-ag.6`**: Decision Desk card clearing (an action whose goal is
  already reached succeeds, and a successful action clears its card at once)
  and GitHub PR polling that skips closed and merged PRs on the first pass after
  a restart. Deployed 2026-11-05 with `--skip-daemon`.
- **`v0.7.1-ag.5`**: `ag.4` plus the PR polling fix. Built 2026-11-05, never deployed;
  `ag.6` replaced it.
- **`v0.7.1-ag.4`**: Decision Desk phase 2, one-tap actions (migrations
  `9009`-`9013`). Full Go suite: the same five upstream failures as stock
  `v0.7.1`. Deployed 2026-11-05 with `--skip-daemon`, so the daemon moves to the new
  CLI at its next idle auto-reload.
- **`v0.7.1-ag.3`**, web only: Decisions cards show summary bullets and when the
  issue entered its status, and the list lines up with the tabs. Deployed
  2026-11-05 by re-tagging the image as `multica-web-ag:v0.7.1` and recreating only
  the frontend. Backend, CLI and database did not change.
- **`v0.7.1-ag.2`**: `v0.7.1-ag.1` plus the Decision Desk (first deploy).
  Deployed 2026-11-05 with `--skip-daemon`, because the daemon had runs active,
  so it stays on `v0.7.1-ag.1` until its next idle reload. Applied `9004`–`9008`.
  The self-host's own Docker context cannot mount the checkout: run the guard and
  builds with `DOCKER_CONTEXT=colima-agbuild`. Full Go suite: the four known
  Cursor failures only, after fixing two real failures in this patch that the
  guard does not catch (index cleanup registration and workspace deletion).
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
