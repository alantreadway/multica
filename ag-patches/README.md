# AG patches for Multica

A small patch set on top of a pinned upstream Multica tag, plus the tooling to
build a patched web image, a patched backend image and a patched CLI/daemon
binary and to rebase the set onto a new upstream tag. The web patches are
frontend-only. The daemon/CLI patch (drain mode) changes the `multica` binary that
runs the local daemon. The drain badge also patches the backend (no migration):
see [Backend image](#backend-image). The PR snapshot fields patch the backend too,
with one additive migration, and add CLI commands: see
[GitHub PR snapshot fields](#github-pr-snapshot-fields-and-pull-requests-link). Each part has its own rollback: for the web
image, removing one env var ([Roll back](#roll-back)); for the CLI, pointing the
daemon back at the stock binary ([Daemon/CLI patch](#daemoncli-patch)); for the
backend, switching the image back to stock. Label ordering adds a column too, so
it needs a backend image as well as the web image: see
[Label ordering](#label-ordering).

- Patch branch: `ag/<tag>` (for example `ag/v0.6.2`), based on upstream tag `<tag>`.
- Web image: `multica-web-ag:<tag>-ag.<n>`, also tagged `multica-web-ag:<tag>`.
- Backend image: `multica-backend-ag:<tag>-ag.<n>`, also tagged `multica-backend-ag:<tag>`.
- CLI binary: `dist/multica-<tag>-ag.<n>-darwin-arm64`.
- Inventory of patches and their files: [`PATCHES.md`](./PATCHES.md).

## What is patched

See [`PATCHES.md`](./PATCHES.md). Web UI (`packages/views`, small additions to
`packages/core`), the local daemon and CLI (`server/internal/daemon`,
`server/cmd/multica`), the drain badge's heartbeat handling in the backend, and
GitHub PR polling for a self-host without an App (`server/internal/integrations`).
Patch tests are named with an `AG patch:` prefix (Go: `TestAGPatch*`).

## Build

From the patch branch with a clean tree and upstream tags fetched
(`git fetch upstream --tags`):

```sh
ag-patches/build-web.sh --rev 1
```

The script refuses a dirty tree, checks the upstream tag is an ancestor of
`HEAD`, runs `ag-patches/test.sh`, then builds upstream's own `Dockerfile.web`
with:

- build arg `NEXT_PUBLIC_APP_VERSION=<tag>-ag.<n>`
- labels `ag.upstream-tag=<tag>` and `org.opencontainers.image.revision=<HEAD sha>`
- tags `multica-web-ag:<tag>-ag.<n>` and `multica-web-ag:<tag>`

`--skip-tests` skips the guard tests. `--rev N` defaults to 1. Bump it for every
rebuild of the same base tag.

From `v0.6.3` the web build (`next build`) needs more memory than the self-host's
4 GiB Colima VM has spare, and is killed with `SIGKILL` / `cannot allocate
memory`. Build it in a separate Colima profile instead of resizing the one the
self-host runs in, then load the image into the self-host's VM:

```sh
colima start agbuild --activate=false --cpus 4 --memory 8 --disk 40
DOCKER_CONTEXT=colima-agbuild ag-patches/build-web.sh --rev N
docker --context colima-agbuild save multica-web-ag:<tag>-ag.N multica-web-ag:<tag> \
  | docker --context colima load
colima stop agbuild
```

`--activate=false` keeps `colima` as the default Docker context, so nothing else
on the machine starts talking to the build VM.

Run the guard tests alone with `ag-patches/test.sh`. CI runs the same script on
PRs into `ag/**` (`.github/workflows/ag-patches.yml`).

The script also runs the Go `TestAGPatch*` tests (`server/internal/daemon`,
`server/cmd/multica`, `server/internal/daemonws`, `server/internal/agdrain`,
`server/pkg/protocol`, `server/internal/integrations/ghpoll` and
`server/internal/integrations/ghsnapshot`) from `server/`: it uses local `go` if present, else
`docker run golang:<version from server/go.mod>`. It fails if fewer than
`AG_GO_MIN_TESTS` of them pass, since `go test -run` exits 0 when nothing
matches.

The database-backed tests (`TestAGPatch*` in
`server/internal/handler` and `server/internal/service`: the drain badge and the PR poller) need Postgres and skip without one, so the guard does
not run them. Before a PR that touches that code, run them against a migrated
test database:

```sh
cd server && DATABASE_URL=postgres://... go test ./internal/handler ./internal/service -run '^TestAGPatch' -count=1
```

## Switch the self-host to the patched frontend

`MULTICA_IMAGE_TAG` drives both the backend and the web image in
`docker-compose.selfhost.yml` (the `image:` lines at 56 and 222 upstream), so
leave it at the upstream tag. Only add the web image override to `.env`:

```sh
MULTICA_WEB_IMAGE=multica-web-ag
```

`docker-compose.isolated.yml` is the self-host directory's own override file
(not part of this repo); run the command from that directory. Recreate just the
frontend:

```sh
docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml up -d frontend
```

The frontend now runs `multica-web-ag:<MULTICA_IMAGE_TAG>`.

Rules while switched:

- The web image's base tag must equal the running backend tag. Never run a
  patched `v0.6.2` web image against a different backend version.
- Don't run a bare `docker compose pull`: it would fail on the local-only
  image. Name the stock services you still need (`pull backend` while only the
  web image is switched; with the patched backend image too, `pull` only
  services such as `postgres` that are still stock).
- To pin a particular build, re-tag it: `docker tag multica-web-ag:<tag>-ag.<n> multica-web-ag:<tag>`.
  The compose file resolves `multica-web-ag:<MULTICA_IMAGE_TAG>`, so that tag is
  the one that runs.

## Backend image

The drain badge and the PR snapshot fields need it: without it the daemon's drain
report is ignored and the badge never shows, and the PR list has none of the
snapshot fields (see "GitHub PR snapshot fields and `pull-requests link`"). Build from the patch branch, same rules as the web
build:

```sh
ag-patches/build-backend.sh --rev 1
```

It refuses a dirty tree, checks the tag is an ancestor of `HEAD`, runs
`ag-patches/test.sh`, then builds upstream's `Dockerfile` with
`VERSION=<tag>-ag.<n>` and tags `multica-backend-ag:<tag>-ag.<n>` and
`multica-backend-ag:<tag>`. The image is the whole backend (server, migrations,
CLI), so the self-host must already be on `<tag>`: this patch is written against
`v0.6.2`, not the `v0.6.0` the self-host runs today.

Switch (the compose file resolves `${MULTICA_BACKEND_IMAGE}:${MULTICA_IMAGE_TAG}`):

```sh
MULTICA_BACKEND_IMAGE=multica-backend-ag   # in .env
docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml up -d backend
```

Roll back: remove that line and run the same `up -d backend`. The drain badge
changes nothing in the schema; the PR snapshot fields add migration `9001`, which
is additive and stays behind harmlessly (see that section); label ordering adds `9002`, same story (see "Label ordering"). The stock backend ignores the `ag_drain` key in
`agent_runtime.metadata` and never clears it. Registration overwrites `metadata`
wholesale, so a runtime that re-registers loses the key. Until then, a runtime
that was draining at rollback keeps it, and a patched web image keeps showing
"Draining · N running" for it, because the runtime stays `online`. Either roll
the web image back too (see "Roll back" below), or clear the key:

```sql
UPDATE agent_runtime SET metadata = metadata - 'ag_drain' WHERE metadata ? 'ag_drain';
```

Heartbeats from stock or Homebrew daemons carry no drain field and leave
metadata alone, so mixed daemons on one backend are fine.

## GitHub PR polling (token mode)

For a self-host with no GitHub App. A backend poller reads each watched repo's
PRs with a personal access token, so PR cards, issue linking, merge
auto-complete and `--until-pr checks` / `merged` wakeups work without webhooks.
Backend only: no migration, no web change. Build the backend image with
`ag-patches/build-backend.sh` (see "Backend image").

Environment (add to the self-host `.env`):

| Variable | Meaning |
| --- | --- |
| `GITHUB_POLL_TOKEN` | A GitHub token that can read pull requests and checks on the watched repos. `gh auth token` works. Treat it as a secret: it is never logged. Empty or whitespace counts as unset. |
| `GITHUB_POLL_INTERVAL` | Go duration, default `60s`, floored at `15s`. |
| `GITHUB_POLL_ENABLED` | Only for a deployment that also has the App (`GITHUB_APP_ID` + `GITHUB_APP_PRIVATE_KEY`): `true` runs the poller next to the webhooks. Ignored without an App, where the token alone turns polling on. |

The stock compose file passes a fixed list of variables to the backend, so
layer the override that adds these three:

```sh
docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml \
  -f ag-patches/docker-compose.github-poll.yml up -d backend
```

Which repos are polled: every GitHub `owner/repo` in any workspace's repository
list (Settings, Code). Other hosts are skipped.

Mode matrix:

| App vars | `GITHUB_POLL_TOKEN` | `GITHUB_POLL_ENABLED` | Result |
| --- | --- | --- | --- |
| unset | set | any | polling on; snapshots use the token |
| unset | unset | any | nothing runs |
| set | unset or set | unset or `false` | polling off, App behaviour unchanged |
| set | set | `true` | polling on beside the webhooks |

Rate budget: one request per watched repo per interval, so at the default
`60s` that is at most 60 requests an hour per repo. A conditional request that
answers 304 does not count against GitHub's rate limit, so an idle repo is free.
Each changed PR costs one GraphQL snapshot query, plus a few retries while its CI
is undecided. A personal token has 5000 REST and 5000 GraphQL points an hour. On
a rate-limit answer the poller pauses until GitHub's reset time, then resumes.

Roll back: switch the backend image back (remove `MULTICA_BACKEND_IMAGE` from
`.env` and run the same `up -d backend`), or just unset `GITHUB_POLL_TOKEN` and
recreate the backend. This patch has no schema change. PR rows it wrote stay,
with `installation_id` `0`; they are valid rows and stop updating until polling
or an App webhook resumes.

The DB-backed `TestAGPatch*` tests for this patch live in
`server/internal/handler` (`ag_ghpoll_test.go`, `ag_ghpoll_flow_test.go`, `ag_ghpoll_refresh_test.go`) and
`server/internal/service` (`ag_ghpoll_wakeup_test.go`); run them with the command
under "Build".

## Roll back

Remove the `MULTICA_WEB_IMAGE` line from `.env` and run
`docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml up -d frontend`
again. The frontend returns to the stock `ghcr.io/multica-ai/multica-web` image.
This is the web image only; for the backend image see "Backend image" above.

## Daemon/CLI patch

Drain mode lets the daemon stop claiming, let running tasks finish, report when
it is idle, and resume, so a restart or upgrade never cuts a run off. The daemon
side is in `server/internal/daemon`; the commands are in `server/cmd/multica`:

```sh
multica daemon drain [--wait] [--timeout 30m]   # stop claiming; --wait blocks until idle
multica daemon resume                           # claim again
multica daemon stop --drain [--timeout 30m]     # drain, wait for idle, then stop
multica daemon restart --drain [--timeout 30m]  # drain, wait for idle, then restart
multica daemon status                           # Claiming / Active tasks / Idle
```

- `drain --wait` exits 0 once idle. If `--timeout` elapses first it exits 1 and
  **leaves drain on**; run `multica daemon resume`. `stop --drain` and
  `restart --drain` behave the same and do not stop the daemon on a timeout.
  `--timeout` defaults to 0, which waits indefinitely (Ctrl-C leaves drain on).
- All of these are human-only, like `daemon stop`: the CLI refuses inside an
  agent run. That is a CLI guard only; `/drain` on the daemon's loopback port is
  unauthenticated, like `/shutdown`.
- Drain is held in memory. A daemon that restarts for any reason comes back
  claiming. While draining, the daemon's own restart paths (self-reload,
  auto-update, server-triggered update) are deferred and say so in
  `reload_pending_reason`.

### Handoff: replace the daemon without a gap

`drain` + restart leaves the machine unclaiming until the old runs finish.
Handoff lets the new daemon claim at once while the old one finishes its runs and
exits:

```sh
multica daemon drain --handoff [--wait] [--timeout 30m]  # old daemon: hand over, exit when idle
multica daemon start --overlap                           # new daemon: start beside it
multica daemon restart --handoff                         # both of the above in one command
multica daemon stop --handoff [--timeout 30m]            # hand off and wait for the daemon to exit
```

- **Old daemon (`--handoff`).** Implies drain. It stops claiming, stops
  heartbeating and registering (the new daemon owns runtime liveness and the
  requests heartbeats carry: update, model list, skills), releases
  `daemon.lock`, moves its logging to `daemon.handoff.<pid>.log` (two rotating
  writers on `daemon.log` are not safe), and never deregisters its runtimes. It
  keeps reporting for its own runs and keeps serving its health port, which the
  agents it started use (`MULTICA_DAEMON_PORT`). It exits once it has no run
  left, but not sooner than 30 seconds after the handoff began, so the new daemon
  can find it. Handoff is one-way: `daemon resume` is refused.
- **New daemon (`--overlap`).** Reads the old daemon's PID, task IDs and task cap
  from its `/health` before it writes its own port or PID file, and does not call
  recover-orphans (which would fail the old daemon's runs) while the old daemon
  lives. It claims through the machine-wide slot locks, so the running agents
  across both processes never exceed `MULTICA_DAEMON_MAX_CONCURRENT_TASKS`; that
  is why both must have the **same cap**, and a mismatch is refused. It skips GC
  while the old daemon lives, and a heavy `git gc` is cancelled the moment the
  other daemon takes a slot.
- **Fails closed.** `--overlap` exits non-zero, before registering anything, when
  there is no live old daemon, when its `/health` can't be read, when it isn't in
  handoff mode, or when the caps differ. It never falls back to a normal start.
- **Old daemon dies mid-handoff.** The new daemon fails only the run IDs the old
  daemon last reported (failure reason `runtime_offline`, so the server retries
  them), then behaves as a normal daemon. A transient health failure while the
  PID is alive changes nothing.
- `daemon status` shows `Handoff` on the old daemon and `Predecessor` on the new.

#### Two-label reload script

`ag-patches/multica-daemon-reload` drives this under launchd by alternating
between two labels, `uk.blogic.multica-daemon.a` and `.b`. It never runs
`launchctl bootout` on the draining label (bootout kills its agents). The repo
does not install the script or the plists.

One-time setup, by hand:

1. Copy `~/Library/LaunchAgents/uk.blogic.multica-daemon.plist` to `….a.plist`
   and `….b.plist`, change `Label` to match, and bootout/delete the old
   single-label job once nothing is running (`multica-ag daemon drain --wait`
   first). Keep `MULTICA_DAEMON_MAX_CONCURRENT_TASKS` identical in both.
2. Make each plist's command line start the daemon with `--overlap` while a
   one-shot marker file is fresh, so the script can ask for an overlap start at
   bootstrap time without editing the plist:

   ```xml
   <key>ProgramArguments</key>
   <array>
     <string>/bin/sh</string>
     <string>-c</string>
     <string>export MULTICA_DAEMON_MAX_CONCURRENT_TASKS=5; if [ -n "$(find "$HOME/.multica/overlap-next" -mmin -10 2>/dev/null)" ]; then exec "$HOME/.local/bin/multica-ag" daemon start --foreground --overlap; fi; exec "$HOME/.local/bin/multica-ag" daemon start --foreground</string>
   </array>
   ```

   The `export MULTICA_DAEMON_MAX_CONCURRENT_TASKS=N` must stay in the command
   line: the script reads the cap from it (and `--cap` rewrites it).

   A launchd restart after a crash, with no fresh marker, starts normally and
   recovers orphans, as it should, unless a handed-off daemon is still draining:
   a handoff daemon leaves `daemon.handoff.json` (its PID and port) in the
   profile directory, and a normal start beside a live one exits non-zero rather
   than fail that daemon's tasks.
3. Do not set `KeepAlive` to restart on a clean exit: a handed-off daemon exits 0
   and must stay down. `<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>`
   restarts crashes only.
4. Install the script and run it with the patched CLI:

   ```sh
   install -m 0755 ag-patches/multica-daemon-reload ~/bin/multica-daemon-reload
   MULTICA_BIN=multica-ag ~/bin/multica-daemon-reload [--cap N] [--wait] [--dry-run]
   ```

It finds the label with a running job, runs `drain --handoff` on it, writes the
marker, bootstraps the other label, waits for it to report running with a new
PID, and prints what the old one is still draining. `--cap N` edits both plists
(backed up first); because a different cap can't overlap, it waits for the old
daemon to hand off and exit, then starts the other label normally. If no label is
running it starts `.a`. Neither label is touched while both have a running job.

### Build

From the patch branch with a clean tree and upstream tags fetched:

```sh
ag-patches/build-cli.sh --rev 2
```

It runs `ag-patches/test.sh`, then cross-compiles `./cmd/multica`
(`CGO_ENABLED=0 GOOS=darwin GOARCH=arm64`, ldflags `-X main.version=<tag>-ag.<n>`
and the commit, as in the `Makefile`) with local `go` if present, else in a
`golang:<version from server/go.mod>` container, to
`dist/multica-<tag>-ag.<n>-darwin-arm64`. The container keeps Go's module and
build caches in the `multica-ag-gomod` and `multica-ag-gocache` volumes; they
are shared by every container run, so pruning them makes the next build cold. `--skip-tests` skips the guard.
`AG_FORCE_DOCKER=1` uses the container even when `go` is installed. `--rev`
defaults to 1 and is shared with `build-web.sh`: both builds of one release use
the same number, and the script refuses a `--rev` whose `ag/<tag>-ag.<n>` tag
already marks a different commit.

### Install

Build and install do not touch the running daemon. Install the binary next to,
not over, the Homebrew one:

```sh
install -m 0755 dist/multica-v0.6.2-ag.2-darwin-arm64 ~/.local/bin/multica-ag
~/.local/bin/multica-ag version
```

Use `multica-ag` for the drain commands. Homebrew's `multica` on `PATH` (0.6.1)
has no `drain`, `resume` or `--drain`, and a daemon it started answers 404 to
`/drain`. To run the daemon itself from the patched binary, change the
`ProgramArguments` in its launchd plist to the `multica-ag` path and reload the
agent (or `~/bin/multica-daemon-reload`). The daemon comes up claiming.

Replacing the binary a running daemon was started from triggers its auto-reload:
within about 10 minutes the daemon notices the version on disk changed and
restarts into it, but only at a moment with no task running. While draining, that
reload is held even once the daemon is idle, and `reload_pending_reason` in
`daemon status` says so.

To swap binaries on your own schedule, with the daemon under launchd:

```sh
multica-ag daemon drain --wait          # blocks until idle
install -m 0755 <new binary> ~/.local/bin/multica-ag   # or edit the plist
~/bin/multica-daemon-reload             # relaunches under launchd, claiming again
```

Don't use `daemon restart` for a launchd daemon: `/shutdown` is a clean exit, so
launchd does not respawn it, and `restart` starts a new daemon outside launchd
with your shell's environment, losing the plist's settings
(`MULTICA_DAEMON_MAX_CONCURRENT_TASKS` and the rest). `stop --drain` and
`restart --drain` suit a daemon that is not under launchd.

Self-host defaults auto-update off (`MULTICA_DAEMON_AUTO_UPDATE`). Keep it off: a
self-update would replace the patched binary with a stock release.

### Backup Claude account (urgent issues only)

Off by default. While the daemon's own Claude account is on hold, urgent issues
can run on a second subscription. Setup, once:

1. `ag-patches/claude-backup-setup.sh` creates `~/.claude-backup` (or
   `$MULTICA_AG_CLAUDE_BACKUP_DIR`), copies `.claude.json` and symlinks the shared
   parts of `~/.claude` into it. Safe to run again; it never overwrites a real file.
2. `CLAUDE_CONFIG_DIR=$HOME/.claude-backup claude auth login`, with the other
   account.
3. Turn the backup on from the Runtimes page (needs the patched backend). The
   daemon checks the login every 30 minutes; the runtime's quota report shows
   `backup.detected`, `valid` and `reason`.

Rollback: switch the setting off, or roll the backend back (an ack without
`ag_settings` turns it off on the next heartbeat). See `PATCHES.md` for the rules.

### Rules

- Build the CLI from the same tag the backend runs (`MULTICA_IMAGE_TAG`). Never
  run a patched `v0.6.2` CLI against a different backend version.
- The CLI and the web image are independent builds: either can be rolled back
  without the other.

### Roll back

Point the launchd plist back at `/opt/homebrew/bin/multica` and run
`~/bin/multica-daemon-reload`; drain first with `multica-ag daemon drain --wait`
so no run is cut off. Nothing on the backend or in the database changed. Keep
the old binary in `dist/` until the new one has run for a while.

## GitHub PR snapshot fields and `pull-requests link`

Backend and CLI patch. `multica issue pull-requests <id> --output json` gains,
per PR, `required_checks_state`, `checks` (each with `required`),
`review_decision`, `latest_reviews` and `base_ref_name`, and
`multica issue pull-requests link|unlink` attaches or removes a PR by URL. Field
semantics and the failure policy are in [`PATCHES.md`](./PATCHES.md). There is no
new environment variable: the fields ride on the GitHub credentials the snapshot
pipeline already uses, and they stay null until a PR's next snapshot refresh
after the backend switch (a page visit, a check webhook or the sweep).

```sh
multica-ag issue pull-requests MUL-1 --output json | jq '.pull_requests[] | {number, required_checks_state, review_decision, base_ref_name}'
multica-ag issue pull-requests link MUL-1 https://github.com/<owner>/<repo>/pull/<n>
multica-ag issue pull-requests unlink MUL-1 https://github.com/<owner>/<repo>/pull/<n>   # or the PR id
```

`link` only finds PRs the workspace already mirrors (its repository is connected
and the PR has been synced by a webhook or a refresh); otherwise it exits
non-zero saying the PR has not been mirrored yet. A malformed URL or any other
4xx exits non-zero with the server's message.

### Deploy

Both halves are needed for the full effect, and neither touches the self-host
until you switch it:

1. Backend: `ag-patches/build-backend.sh --rev N`, then switch as in "Backend
   image". The backend's migrate step applies `9001_ag_github_pr_snapshot_extras`
   (four nullable columns, no index) on start.
2. CLI: `ag-patches/build-cli.sh --rev N`, then install it next to the stock one
   as in "Daemon/CLI patch, Install" (`~/.local/bin/multica-ag`). Use
   `multica-ag` for `link` and `unlink`; the stock `multica` has neither, and
   its `pull-requests` listing prints whatever fields the backend sends. A CLI
   from this branch against a stock backend lists and links as before, minus the
   new fields.

### Roll back

Switch the backend image back (remove `MULTICA_BACKEND_IMAGE` and `up -d
backend`). The stock backend ignores the new columns, never writes them, and its
migrate step does not mind a recorded migration it has no file for, so the
database needs no change; checked by running the unpatched branch's migrate and
PR tests against a database with `573` applied. The columns hold only the last
snapshot and are overwritten or cleared by the next patched one. To drop them
anyway (only with the stock backend running, and no patched backend to come
back to):

```sh
psql "$DATABASE_URL" -f server/migrations/9001_ag_github_pr_snapshot_extras.down.sql
psql "$DATABASE_URL" -c "DELETE FROM schema_migrations WHERE version = '9001_ag_github_pr_snapshot_extras'"
```

The patched CLI needs no rollback; keep using `multica` to avoid `link` and
`unlink`.

## Label ordering

Backend and web patch. Manage labels gets a drag handle per row, so a workspace
sets the order its labels appear in. The order is stored per workspace and label
type (issue, agent, skill) and every label list follows it, falling back to name
for labels that share a position. What it does and the endpoint's failure rules
are in [`PATCHES.md`](./PATCHES.md).

### Deploy

It needs the server half: the web image alone has nothing to save the order to.

1. Backend: `ag-patches/build-backend.sh --rev N`, then switch as in "Backend
   image". The backend's migrate step applies `9002_ag_label_position` on start: one
   `position` column on `issue_label` (default 0, no index, no foreign key),
   backfilled in each workspace's current alphabetical order, so no label moves
   until someone drags one.
2. Web: `ag-patches/build-web.sh --rev N`, then switch as in "Switch the
   self-host to the patched frontend".

Switch the backend first. A patched web image on a stock backend still shows the
drag handles, but a drop fails (the stock server answers 400) and the rows roll
back with an error toast. Stock web on the patched backend is fine: it ignores
`position` and the lists simply arrive in the saved order.

### Roll back

Switch the images back (remove `MULTICA_BACKEND_IMAGE` and `MULTICA_WEB_IMAGE`
from `.env` and `up -d backend frontend`). The extra column is harmless to the
stock backend: it never reads or writes `position`, its lists sort by name, its
inserts take the default 0, and its migrate step does not mind a recorded
migration it has no file for. Rolling back leaves behind:

- The `position` column and the saved order. Nothing reads them while stock runs.
- Labels created on the stock backend sit at position 0. If you switch back to
  the patched backend they list first, by name, ahead of the ordered labels; drag
  them into place once.

Rolling back the web image alone leaves the saved order in effect, since the
patched backend still sorts by it. It only removes the drag handles.

To drop the column anyway (only with the stock backend running, and no patched
backend to come back to; this loses the saved order):

```sh
psql "$DATABASE_URL" -f server/migrations/9002_ag_label_position.down.sql
psql "$DATABASE_URL" -c "DELETE FROM schema_migrations WHERE version = '9002_ag_label_position'"
```

The `down` migration has not been run in testing.

### Tests

The Go tests (`label_order*_test.go` in `server/internal/handler` and
`server/cmd/server`) are DB-backed and have ordinary names, not `TestAGPatch*`,
so `ag-patches/test.sh` does not run them and `AG_GO_MIN_TESTS` is unchanged. Run
them against a migrated test database before a PR that touches labels:

```sh
cd server && DATABASE_URL=postgres://... go test ./internal/handler ./cmd/server -run 'Label' -count=1
```

The web tests run with `pnpm test`.

## Rebase onto a new upstream tag

For a new upstream tag `vX`, from the current patch branch `ag/v0.6.3`:

1. Fetch upstream tags: `git fetch upstream --tags`.
2. Create the new branch: `git switch -c ag/vX ag/v0.6.3`.
3. Re-check upstream before replaying anything. For each patch in
   [`PATCHES.md`](./PATCHES.md) (see "Upstream status" there), look at its
   overlapping PRs and issues on `multica-ai/multica` (read-only; open nothing
   there) and search for newer duplicates. If upstream merged it in `vX` or
   earlier, or replaced it, drop the patch from the replay (`git rebase
   --onto vX v0.6.3` with the patch's commits excluded, or `git rebase -i`) and
   delete its entry. Otherwise refresh its **Upstream** line and the
   "Last checked" date.
4. Replay the patches: `git rebase --onto vX v0.6.3`. This flattens the PR
   merge commits, so a conflict a merge commit resolved comes back on the
   commit that caused it (usually `PATCHES.md` and the test lists in
   `test.sh`). Afterwards the tree must equal
   `git merge-tree --write-tree ag/v0.6.3 vX` plus your conflict fixes; diff the
   two to check.
5. Resolve conflicts. The web patches concentrate in `swimlane-view.tsx`, and the
   daemon patch hooks into `daemon.go`, `health.go`, `auto_update.go` and
   `cmd_daemon.go`, so expect conflicts there. Fork migrations are numbered
   from `9001` up so they never share a number with upstream's
   (`TestMigrationNumericPrefixesAreUnique` fails if they do); give a new one
   the next free `9xxx` number, never upstream's next one.
6. Run `ag-patches/test.sh`, then the whole Go suite against a scratch
   Postgres (`go run ./cmd/migrate up`, then `go test ./...` with
   `DATABASE_URL` set), since the guard skips the DB-backed tests. Run it as a
   non-root user: as root, Claude-launch tests fail on Claude's root check.
7. Build: `ag-patches/build-web.sh --rev 1`, `ag-patches/build-cli.sh --rev 1` and
   `ag-patches/build-backend.sh --rev 1`.
8. Upgrade the self-host to `vX`, then switch to the new images: the patched
   backend image from step 7 first, then the web image and the daemon's new CLI
   binary. A patched web image on a stock backend freezes the drain badge (see
   "Backend image"), so if the backend image wasn't built, leave the web image
   stock too.
9. Update `PATCHES.md` (base tag and commit, files per patch, upstream status).
10. Tag the built commit and push the branch and the tag:
   `git tag ag/vX-ag.1`, `git push origin ag/vX`,
   `git push origin refs/tags/ag/vX-ag.1`.

## Git tag rule

Tag a released build as `ag/<tag>-ag.N` (for example `ag/v0.6.2-ag.1`). Never
create a tag starting with `v`: upstream's `release.yml` triggers on `v*.*.*`
tags and Actions is enabled on this fork. Never push to `multica-ai/multica`.
