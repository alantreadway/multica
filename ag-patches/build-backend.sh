#!/usr/bin/env bash
# Build the patched Multica backend image from this patch branch (ag/<tag>).
#
#   ag-patches/build-backend.sh [--rev N] [--skip-tests]
#
# Produces multica-backend-ag:<tag>-ag.N and multica-backend-ag:<tag> from
# upstream's own Dockerfile. Carries the drain badge's heartbeat handling (two
# guarded UPDATE queries on agent_runtime.metadata); no migration. The image is
# the whole backend, so build it for the same <tag> as the web image.
set -euo pipefail

# shellcheck source=ag-patches/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
ag_prepare build-backend.sh image "$@"
ag_guard_tests

sha="$(git rev-parse HEAD)"

docker build \
  -f Dockerfile \
  --build-arg "VERSION=${version}" \
  --build-arg "COMMIT=${sha}" \
  --build-arg "DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --label "ag.upstream-tag=${tag}" \
  --label "org.opencontainers.image.revision=${sha}" \
  -t "multica-backend-ag:${version}" \
  -t "multica-backend-ag:${tag}" \
  .

cat <<MSG

Built multica-backend-ag:${version} (also tagged multica-backend-ag:${tag}) from ${sha}.

This backend needs the self-host to be on ${tag}: the image is the whole backend.
It runs the stock migrations; nothing is added to the schema.

Switch the self-host backend (compose resolves multica-backend-ag:\$MULTICA_IMAGE_TAG):
  echo 'MULTICA_BACKEND_IMAGE=multica-backend-ag' >> .env
  docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml up -d backend

Roll back:
  remove the MULTICA_BACKEND_IMAGE line from .env, then
  docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml up -d backend
  If a runtime is draining, the stock backend never clears its key and the
  patched web image shows the badge until that daemon re-registers. Either roll
  the web image back too, or clear it:
    UPDATE agent_runtime SET metadata = metadata - 'ag_drain' WHERE metadata ? 'ag_drain';

Do not run a bare 'docker compose pull' while switched; use 'docker compose pull frontend'
(or the other stock services you still need).
MSG
