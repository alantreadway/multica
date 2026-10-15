#!/usr/bin/env bash
# Build the patched Multica CLI (daemon included) from this patch branch
# (ag/<tag>).
#
#   ag-patches/build-cli.sh [--rev N] [--skip-tests]
#
# Cross-compiles ./cmd/multica for darwin/arm64 to
# dist/multica-<tag>-ag.N-darwin-arm64 with the same version ldflags as the
# Makefile. Uses local go if present, else a golang container (version from
# server/go.mod). Installing the binary is a separate, manual step: see
# "Daemon/CLI patch" in ag-patches/README.md.
set -euo pipefail

usage() {
  echo "usage: ag-patches/build-cli.sh [--rev N] [--skip-tests]" >&2
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
  echo "error: working tree is dirty; commit or stash first so the binary matches HEAD" >&2
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
short_sha="$(git rev-parse --short HEAD)"
built_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
out_name="multica-${version}-darwin-arm64"
mkdir -p dist

ldflags="-X main.version=${version} -X main.commit=${short_sha} -X main.buildCommit=${sha} -X main.date=${built_at}"

if command -v go >/dev/null 2>&1; then
  (cd server && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
    go build -ldflags "$ldflags" -o "../dist/${out_name}" ./cmd/multica)
elif command -v docker >/dev/null 2>&1; then
  go_version="$(awk '/^go /{print $2; exit}' server/go.mod)"
  echo "go not found; building in golang:${go_version} via docker"
  docker run --rm \
    -v "$PWD":/src -w /src/server \
    -v multica-ag-gomod:/go/pkg/mod \
    -e CGO_ENABLED=0 -e GOOS=darwin -e GOARCH=arm64 -e GOFLAGS=-buildvcs=false \
    -e LDFLAGS="$ldflags" -e OUT="/src/dist/${out_name}" \
    -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
    "golang:${go_version}" \
    sh -c 'go build -ldflags "$LDFLAGS" -o "$OUT" ./cmd/multica && chown "$HOST_UID:$HOST_GID" "$OUT"'
else
  echo "error: need either go or docker to build the CLI" >&2
  exit 1
fi

cat <<MSG

Built dist/${out_name} (${version}) from ${sha}.

Install (does not touch the running daemon; see ag-patches/README.md):
  install -m 0755 dist/${out_name} ~/.local/bin/multica-ag
  ~/.local/bin/multica-ag version

Tag the built commit: git tag ag/${version} (never a tag starting with 'v').
MSG
