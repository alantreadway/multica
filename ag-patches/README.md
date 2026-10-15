# AG patches for Multica

A small patch set on top of a pinned upstream Multica tag, plus the tooling to
build a patched web image, a patched backend image and a patched CLI/daemon
binary and to rebase the set onto a new upstream tag. The web patches are
frontend-only. The daemon/CLI patch (drain mode) changes the `multica` binary that
runs the local daemon. The drain badge also patches the backend (no migration):
see [Backend image](#backend-image). Each part has its own rollback: for the web
image, removing one env var ([Roll back](#roll-back)); for the CLI, pointing the
daemon back at the stock binary ([Daemon/CLI patch](#daemoncli-patch)); for the
backend, switching the image back to stock.

- Patch branch: `ag/<tag>` (for example `ag/v0.6.2`), based on upstream tag `<tag>`.
- Web image: `multica-web-ag:<tag>-ag.<n>`, also tagged `multica-web-ag:<tag>`.
- Backend image: `multica-backend-ag:<tag>-ag.<n>`, also tagged `multica-backend-ag:<tag>`.
- CLI binary: `dist/multica-<tag>-ag.<n>-darwin-arm64`.
- Inventory of patches and their files: [`PATCHES.md`](./PATCHES.md).

## What is patched

See [`PATCHES.md`](./PATCHES.md). Web UI (`packages/views`, small additions to
`packages/core`), the local daemon and CLI (`server/internal/daemon`,
`server/cmd/multica`), and the drain badge's heartbeat handling in the backend.
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

Run the guard tests alone with `ag-patches/test.sh`. CI runs the same script on
PRs into `ag/**` (`.github/workflows/ag-patches.yml`).

The script also runs the Go `TestAGPatch*` tests (`server/internal/daemon`,
`server/cmd/multica`, `server/internal/daemonws`, `server/internal/agdrain` and
`server/pkg/protocol`) from `server/`: it uses local `go` if present, else
`docker run golang:<version from server/go.mod>`. It fails if fewer than
`AG_GO_MIN_TESTS` of them pass, since `go test -run` exits 0 when nothing
matches.

The drain badge's database-backed tests (`TestAGPatch*` in
`server/internal/handler`) need Postgres and skip without one, so the guard does
not run them. Before a PR that touches that code, run them against a migrated
test database:

```sh
cd server && DATABASE_URL=postgres://... go test ./internal/handler -run '^TestAGPatch' -count=1
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

Only the drain badge needs it: without it the daemon's drain report is ignored
and the badge never shows. Build from the patch branch, same rules as the web
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

Roll back: remove that line and run the same `up -d backend`. Nothing in the
schema changes. The `ag_drain` key already stored in `agent_runtime.metadata`
is ignored by the stock backend and is replaced on that runtime's next
registration (registration overwrites `metadata` wholesale), so there is nothing
to clean up. The web badge only reads that key, so a stock backend and a patched
web image just show no badge.

Heartbeats from stock or Homebrew daemons carry no drain field and leave
metadata alone, so mixed daemons on one backend are fine.

## Roll back

Remove the `MULTICA_WEB_IMAGE` line from `.env` and run
`docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml up -d frontend`
again. The frontend returns to the stock `ghcr.io/multica-ai/multica-web` image.
No backend or database change is involved.

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

### Build

From the patch branch with a clean tree and upstream tags fetched:

```sh
ag-patches/build-cli.sh --rev 2
```

It runs `ag-patches/test.sh`, then cross-compiles `./cmd/multica`
(`CGO_ENABLED=0 GOOS=darwin GOARCH=arm64`, ldflags `-X main.version=<tag>-ag.<n>`
and the commit, as in the `Makefile`) with local `go` if present, else in a
`golang:<version from server/go.mod>` container, to
`dist/multica-<tag>-ag.<n>-darwin-arm64`. `--skip-tests` skips the guard.
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

## Rebase onto a new upstream tag

For a new upstream tag `vX`, from the current patch branch `ag/v0.6.2`:

1. Fetch upstream tags: `git fetch upstream --tags`.
2. Create the new branch: `git switch -c ag/vX ag/v0.6.2`.
3. Replay the patches: `git rebase --onto vX v0.6.2`.
4. Resolve conflicts. The web patches concentrate in `swimlane-view.tsx`, and the
   daemon patch hooks into `daemon.go`, `health.go`, `auto_update.go` and
   `cmd_daemon.go`, so expect conflicts there.
5. Run `ag-patches/test.sh`.
6. Build: `ag-patches/build-web.sh --rev 1`, `ag-patches/build-cli.sh --rev 1` and
   `ag-patches/build-backend.sh --rev 1`.
7. Upgrade the backend to stock `vX` (or switch it to the new patched backend
   image), then switch the frontend to the new image and the daemon to the new CLI
   binary.
8. Update `PATCHES.md` (base tag and commit, files per patch).
9. Tag the built commit and push the branch and the tag:
   `git tag ag/vX-ag.1`, `git push origin ag/vX`,
   `git push origin refs/tags/ag/vX-ag.1`.

## Git tag rule

Tag a released build as `ag/<tag>-ag.N` (for example `ag/v0.6.2-ag.1`). Never
create a tag starting with `v`: upstream's `release.yml` triggers on `v*.*.*`
tags and Actions is enabled on this fork. Never push to `multica-ai/multica`.
