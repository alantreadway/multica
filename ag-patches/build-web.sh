#!/usr/bin/env bash
# Build the patched Multica web image from this patch branch (ag/<tag>).
#
#   ag-patches/build-web.sh [--rev N] [--skip-tests]
#
# Produces multica-web-ag:<tag>-ag.N and multica-web-ag:<tag> from upstream's
# own Dockerfile.web. Frontend only: no backend or database changes.
set -euo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ag_prepare build-web.sh image "$@"
ag_guard_tests

sha="$(git rev-parse HEAD)"

docker build \
  -f Dockerfile.web \
  --build-arg "NEXT_PUBLIC_APP_VERSION=${version}" \
  --label "ag.upstream-tag=${tag}" \
  --label "org.opencontainers.image.revision=${sha}" \
  -t "multica-web-ag:${version}" \
  -t "multica-web-ag:${tag}" \
  .

cat <<MSG

Built multica-web-ag:${version} (also tagged multica-web-ag:${tag}) from ${sha}.

Switch the self-host frontend (backend stays on the stock ${tag} image):
  echo 'MULTICA_WEB_IMAGE=multica-web-ag' >> .env
  docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml up -d frontend

Roll back:
  remove the MULTICA_WEB_IMAGE line from .env, then
  docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml up -d frontend

Do not run a bare 'docker compose pull' while switched; use 'docker compose pull backend'.
MSG
