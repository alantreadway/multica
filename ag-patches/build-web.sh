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

# From v0.8.1 `next build` outgrows Node's default heap (about 2 GB in the build
# VM) and aborts. Dockerfile.web has no hook for NODE_OPTIONS, so build from a
# copy with one ENV line before the build step; upstream's file stays untouched.
heap_mb="${AG_WEB_NODE_HEAP_MB:-4096}"
dockerfile="$(mktemp)"
trap 'rm -f "$dockerfile"' EXIT
awk -v heap="$heap_mb" '
  /^RUN pnpm --filter @multica\/web build/ && !done { print "ENV NODE_OPTIONS=--max-old-space-size=" heap; done = 1 }
  { print }
  END { if (!done) exit 1 }
' Dockerfile.web > "$dockerfile" || { echo "build-web.sh: web build step not found in Dockerfile.web" >&2; exit 1; }

docker build \
  -f "$dockerfile" \
  --build-arg "NEXT_PUBLIC_APP_VERSION=${version}" \
  --label "ag.upstream-tag=${tag}" \
  --label "org.opencontainers.image.revision=${sha}" \
  -t "multica-web-ag:${version}" \
  -t "multica-web-ag:${tag}" \
  .

cat <<MSG

Built multica-web-ag:${version} (also tagged multica-web-ag:${tag}) from ${sha}.

Switch the self-host frontend (the drain badge also needs the patched backend image,
see ag-patches/build-backend.sh; without it the backend stays on the stock ${tag} image):
  echo 'MULTICA_WEB_IMAGE=multica-web-ag' >> .env
  docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml up -d frontend

Roll back:
  remove the MULTICA_WEB_IMAGE line from .env, then
  docker compose -f docker-compose.selfhost.yml -f docker-compose.isolated.yml up -d frontend

Do not run a bare 'docker compose pull' while switched; name only the stock services you
still need ('pull backend' while only the web image is switched, 'pull postgres' once the
backend image is switched too).
MSG
