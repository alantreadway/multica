#!/usr/bin/env bash
# Build the patched Multica CLI (daemon included) from this patch branch
# (ag/<tag>) to dist/multica-<tag>-ag.N-<os>-<arch>: darwin-arm64 for the Mac CLI,
# linux-amd64 for the daemon container on Bastion.
#
#   ag-patches/build-cli.sh [--rev N] [--skip-tests]
set -euo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ag_prepare build-cli.sh binary "$@"
ag_guard_tests

sha="$(git rev-parse HEAD)"
short_sha="$(git rev-parse --short HEAD)"
built_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
mkdir -p dist

# The same version ldflags as the Makefile.
ldflags="-X main.version=${version} -X main.commit=${short_sha} -X main.buildCommit=${sha} -X main.date=${built_at}"

for target in darwin-arm64 linux-amd64; do
  export CGO_ENABLED=0 GOOS="${target%-*}" GOARCH="${target#*-}"
  AG_GO_ENV="CGO_ENABLED GOOS GOARCH" \
    ag_go build -buildvcs=false -ldflags "$ldflags" -o "../dist/multica-${version}-${target}" ./cmd/multica
done

cat <<MSG

Built dist/multica-${version}-{darwin-arm64,linux-amd64} (${version}) from ${sha}.

Deploy both with home-sysadmin's scripts/multica-ag/deploy.sh (ag-patches/README.md,
"Deploy"): the Mac copy is the CLI, the Linux copy runs the daemon on Bastion.

Tag the built commit: git tag ag/${version} (never a tag starting with 'v').
MSG
