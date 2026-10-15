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

usage() {
  echo "usage: ag-patches/build-backend.sh [--rev N] [--skip-tests]" >&2
  exit 2
}

rev=1
skip_tests=0
while [ $# -gt 0 ]; do
  case "$1" in
    --rev)
      [ $# -ge 2 ] || usage
      rev="$2"
      shift 2
      ;;
    --skip-tests)
      skip_tests=1
      shift
      ;;
    *) usage ;;
  esac
done

case "$rev" in
  '' | *[!0-9]*)
    echo "error: --rev must be a non-negative integer, got '$rev'" >&2
    exit 2
    ;;
esac

cd "$(git rev-parse --show-toplevel)"

if [ -n "$(git status --porcelain)" ]; then
  echo "error: working tree is dirty; commit or stash first so the image matches HEAD" >&2
  exit 1
fi

branch="$(git symbolic-ref --quiet --short HEAD || true)"
case "$branch" in
  ag/*) tag="${branch#ag/}" ;;
  *)
    echo "error: must be on a patch branch named ag/<upstream-tag> (on '${branch:-detached HEAD}')" >&2
    exit 1
    ;;
esac

if ! git rev-parse --verify --quiet "refs/tags/${tag}^{commit}" >/dev/null; then
  echo "error: upstream tag '${tag}' not found; run: git fetch upstream --tags" >&2
  exit 1
fi
if ! git merge-base --is-ancestor "${tag}" HEAD; then
  echo "error: ${tag} is not an ancestor of HEAD; the branch is not based on that tag" >&2
  exit 1
fi

if [ "$skip_tests" -eq 0 ]; then
  ./ag-patches/test.sh
else
  echo "warning: --skip-tests set; guard tests not run" >&2
fi

version="${tag}-ag.${rev}"
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
  (the drain key stays in metadata, is ignored by the stock backend and is
  overwritten when the runtime next registers)

Do not run a bare 'docker compose pull' while switched; use 'docker compose pull frontend'
(or the other stock services you still need).
MSG
