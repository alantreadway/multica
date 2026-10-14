# AG patches for Multica

A small, frontend-only patch set on top of a pinned upstream Multica tag, plus
the tooling to build a patched web image and to rebase the set onto a new
upstream tag. The backend, database and desktop app are untouched, so rolling
back is removing one env var.

- Patch branch: `ag/<tag>` (for example `ag/v0.6.2`), based on upstream tag `<tag>`.
- Image: `multica-web-ag:<tag>-ag.<n>`, also tagged `multica-web-ag:<tag>`.
- Inventory of patches and their files: [`PATCHES.md`](./PATCHES.md).

## What is patched

See [`PATCHES.md`](./PATCHES.md). Web UI only (`packages/views`, a small
append to `packages/core`). Patch tests are named with an `AG patch:` prefix.

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
  image. Use `docker compose ... pull backend`.
- To pin a particular build, re-tag it: `docker tag multica-web-ag:<tag>-ag.<n> multica-web-ag:<tag>`.
  The compose file resolves `multica-web-ag:<MULTICA_IMAGE_TAG>`, so that tag is
  the one that runs.

## Roll back

Remove the `MULTICA_WEB_IMAGE` line from `.env` and run
`docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml up -d frontend`
again. The frontend returns to the stock `ghcr.io/multica-ai/multica-web` image.
No backend or database change is involved.

## Rebase onto a new upstream tag

For a new upstream tag `vX`, from the current patch branch `ag/v0.6.2`:

1. Fetch upstream tags: `git fetch upstream --tags`.
2. Create the new branch: `git switch -c ag/vX ag/v0.6.2`.
3. Replay the patches: `git rebase --onto vX v0.6.2`.
4. Resolve conflicts. The patches concentrate in `swimlane-view.tsx`, so expect
   conflicts there.
5. Run `ag-patches/test.sh`.
6. Build: `ag-patches/build-web.sh --rev 1`.
7. Upgrade the backend to stock `vX`, then switch the frontend to the new image.
8. Update `PATCHES.md` (base tag and commit, files per patch).
9. Tag the built commit and push the branch and the tag:
   `git tag ag/vX-ag.1`, `git push origin ag/vX`,
   `git push origin refs/tags/ag/vX-ag.1`.

## Git tag rule

Tag a released build as `ag/<tag>-ag.N` (for example `ag/v0.6.2-ag.1`). Never
create a tag starting with `v`: upstream's `release.yml` triggers on `v*.*.*`
tags and Actions is enabled on this fork. Never push to `multica-ai/multica`.
