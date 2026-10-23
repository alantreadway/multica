#!/usr/bin/env bash
# The whole Go suite, DB-backed tests included, against a throwaway Postgres and
# Redis. ag-patches/test.sh only runs TestAGPatch* without a database; run this
# before a PR that touches DB-backed code, and at each rebase.
#
#   ag-patches/test-go-full.sh
#
# It starts pgvector/pgvector:pg17 and redis:7-alpine on a private Docker
# network, then, in golang:<version from server/go.mod> as uid 1000 (as root,
# the Claude-launch tests fail on Claude's root check):
#   1. go run ./cmd/migrate up          (from server/)
#   2. scripts/test-go.sh --only regular
#   3. scripts/test-go.sh --only agent  (run separately: a plain test-go.sh
#      skips pkg/agent when the first half fails)
# The containers and network are removed on exit, even on failure or Ctrl-C.
# Names carry the PID (agfull-<pid>-*), so two runs don't collide. The module
# and build caches live in the volumes multica-ag-gomod-u and
# multica-ag-gocache-u, owned by uid 1000.
#
# Output goes to a log (AG_TEST_LOG, default $TMPDIR/ag-test-go-full.<time>.log).
# Failures are then split into known upstream failures, listed for the base
# upstream tag in ag-patches/known-upstream-failures.txt, and real ones; real
# failures of named tests are also printed as known-failure lines to paste after
# a run on stock upstream. The base tag comes from ag_upstream_tag (lib.sh).
#
# DOCKER_CONTEXT defaults to colima-agbuild (see README.md, "Build").
#
# Exit status: 0 only known failures (or none), 1 real failures, 2 setup error.
set -euo pipefail

export DOCKER_CONTEXT="${DOCKER_CONTEXT:-colima-agbuild}"

[ $# -eq 0 ] || {
  echo "usage: ag-patches/test-go-full.sh" >&2
  exit 2
}

here="$(cd "$(dirname "$0")" && pwd)"
cd "$(git rev-parse --show-toplevel)"
repo="$PWD"

. "$here/lib.sh"
upstream_tag="$(ag_upstream_tag)"
if [ -z "$upstream_tag" ]; then
  echo "error: can't tell the upstream tag; run git fetch upstream --tags or set AG_UPSTREAM_TAG" >&2
  exit 2
fi

go_version="$(awk '/^go /{print $2; exit}' server/go.mod)"
known_file="$repo/ag-patches/known-upstream-failures.txt"
tmp="${TMPDIR:-/tmp}"
log="${AG_TEST_LOG:-${tmp%/}/ag-test-go-full.$(date +%Y%m%d-%H%M%S).log}"
: >"$log"

id="agfull-$$"
net="$id"
pg="$id-pg"
redis="$id-redis"
runner="$id-go"

cleanup() {
  docker rm -f "$runner" "$pg" "$redis" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

start=$SECONDS
echo "test-go-full: ${repo} (upstream ${upstream_tag}, golang:${go_version}, context ${DOCKER_CONTEXT})"
echo "test-go-full: log ${log}"

if ! docker info >/dev/null 2>&1; then
  echo "error: Docker context ${DOCKER_CONTEXT} is not reachable; start it (colima start agbuild ...)" >&2
  exit 2
fi

docker network create "$net" >/dev/null
docker run -d --name "$pg" --network "$net" \
  -e POSTGRES_USER=multica -e POSTGRES_PASSWORD=multica -e POSTGRES_DB=multica_test \
  pgvector/pgvector:pg17 >/dev/null
docker run -d --name "$redis" --network "$net" redis:7-alpine >/dev/null

# Over TCP: during first-run init the image's temporary server answers on the
# socket only, so a socket check can pass before the real server is up.
ready=0
for _ in $(seq 1 90); do
  if docker exec "$pg" pg_isready -h 127.0.0.1 -U multica -d multica_test >/dev/null 2>&1 &&
    docker exec "$redis" redis-cli ping >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  docker logs "$pg" >>"$log" 2>&1 || true
  echo "error: Postgres or Redis did not become ready within 90s (logs in ${log})" >&2
  exit 2
fi

docker run --rm -v multica-ag-gomod-u:/m -v multica-ag-gocache-u:/c "golang:${go_version}" \
  sh -c 'find /m /c ! -user 1000 -exec chown -h 1000:1000 {} +' >/dev/null

# Step markers (@@ag-full ...) let the parser below attribute failures.
# shellcheck disable=SC2016 # expanded by bash inside the container
inner='
cd /src/server && go run ./cmd/migrate up
rc=$?
echo "@@ag-full step=migrate rc=$rc"
[ "$rc" -eq 0 ] || exit 0
cd /src
for half in regular agent; do
  echo "@@ag-full begin=$half"
  bash scripts/test-go.sh --only "$half"
  echo "@@ag-full step=$half rc=$?"
done
'

# GIT_CEILING_DIRECTORIES stops git in a test's working directory from finding
# /src/.git: in a linked worktree that file points at a host path the container
# doesn't have, and git commands run there fail with "not a git repository".
# The run goes in the background with a wait, so Ctrl-C reaches the traps at
# once instead of after the container ends; cleanup then removes the container.
set +e
docker run --rm --name "$runner" --network "$net" --user 1000:1000 \
  -v "$repo":/src -w /src \
  -v multica-ag-gomod-u:/go/pkg/mod -v multica-ag-gocache-u:/cache \
  -e HOME=/tmp -e GOCACHE=/cache -e GOMODCACHE=/go/pkg/mod -e GOFLAGS=-buildvcs=false \
  -e GIT_CEILING_DIRECTORIES=/src \
  -e "DATABASE_URL=postgres://multica:multica@${pg}:5432/multica_test?sslmode=disable" \
  -e "REDIS_TEST_URL=redis://${redis}:6379/0" \
  "golang:${go_version}" bash -c "$inner" 2>&1 | tee -a "$log" &
wait $!
docker_rc=$?
set -e

elapsed=$((SECONDS - start))
duration="$((elapsed / 60))m$((elapsed % 60))s"

step_rc() { sed -n "s/^@@ag-full step=$1 rc=\([0-9]*\)\$/\1/p" "$log" | tail -n 1; }
migrate_rc="$(step_rc migrate)"
regular_rc="$(step_rc regular)"
agent_rc="$(step_rc agent)"

echo
echo "=== test-go-full summary (${duration}, upstream ${upstream_tag}) ==="

if [ "$docker_rc" -ne 0 ] || [ "${migrate_rc:-x}" != 0 ] || [ -z "$regular_rc" ] || [ -z "$agent_rc" ]; then
  echo "setup error: docker exit ${docker_rc}, migrate rc ${migrate_rc:-none}, regular rc ${regular_rc:-none}, agent rc ${agent_rc:-none}"
  echo "log: ${log}"
  exit 2
fi

# One line per failure: <half> <test-or-package> <package>. A top-level
# `--- FAIL: TestX` is attributed to the package result line that follows it
# (go test prints each package's output as one block). A failed package with no
# failed test (build failure, timeout, panic outside a test) counts as a failure
# of the package itself, named "(package)".
failures="$(awk '
  /^@@ag-full begin=/ { sub(/^@@ag-full begin=/, ""); half = $0; n = 0; next }
  /^--- FAIL: / { names[n++] = $3; next }
  /^FAIL\t/ {
    pkg = $2
    if (n == 0) print half, "(package)", pkg
    for (i = 0; i < n; i++) print half, names[i], pkg
    n = 0
    next
  }
  /^(ok|\?) / { n = 0 }
' "$log")"

known_names="$(awk -v tag="$upstream_tag" '$1 == tag { print $2 }' "$known_file" 2>/dev/null || true)"

known_hits=""
real=""
while read -r half name pkg; do
  [ -n "${half:-}" ] || continue
  if [ "$name" != "(package)" ] && printf '%s\n' "$known_names" | grep -qxF "$name"; then
    known_hits+="  ${name}  (${pkg})"$'\n'
  else
    real+="  ${name}  (${pkg}, ${half})"$'\n'
  fi
done <<<"$failures"

# A half that failed with nothing parsed (for example the agent-CLI guard
# reporting an unexpected invocation) is a real failure too.
for half in regular agent; do
  rc_var="${half}_rc"
  if [ "${!rc_var}" -ne 0 ] && ! printf '%s\n' "$failures" | grep -q "^${half} "; then
    real+="  scripts/test-go.sh --only ${half} exited ${!rc_var} with no failed test or package (see log)"$'\n'
  fi
done

passed_known=""
for name in $known_names; do
  printf '%s' "$known_hits" | grep -qE "^  ${name}  " || passed_known+=" ${name}"
done

echo "regular half exit ${regular_rc}, agent half exit ${agent_rc}"
if [ -n "$known_hits" ]; then
  echo "Known upstream failures ($(printf '%s' "$known_hits" | grep -c .)):"
  printf '%s' "$known_hits"
else
  echo "Known upstream failures: none"
fi
[ -z "$passed_known" ] || echo "Known failures that passed this time:${passed_known}"
if [ -n "$real" ]; then
  echo "REAL failures ($(printf '%s' "$real" | grep -c .)):"
  printf '%s' "$real"
  # On a stock upstream tree every failure is upstream's own: ready to paste.
  suggest="$(printf '%s' "$real" | awk -v tag="$upstream_tag" '$1 ~ /^Test/ { print tag, $1, " # fails on stock " tag " in the Linux test container" }')"
  if [ -n "$suggest" ]; then
    echo "If this is stock ${upstream_tag}, add to known-upstream-failures.txt (check flakes first):"
    printf '%s\n' "$suggest" | sed 's/^/  /'
  fi
  echo "log: ${log}"
  exit 1
fi
echo "Real failures: none"
echo "log: ${log}"
