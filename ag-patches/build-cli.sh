#!/usr/bin/env bash
# Build the patched Multica CLI (daemon included) from this patch branch
# (ag/<tag>) to dist/multica-<tag>-ag.N-darwin-arm64.
#
#   ag-patches/build-cli.sh [--rev N] [--skip-tests]
set -euo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ag_prepare build-cli.sh binary "$@"
ag_guard_tests

sha="$(git rev-parse HEAD)"
short_sha="$(git rev-parse --short HEAD)"
built_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
out_name="multica-${version}-darwin-arm64"
mkdir -p dist

# The same version ldflags as the Makefile.
ldflags="-X main.version=${version} -X main.commit=${short_sha} -X main.buildCommit=${sha} -X main.date=${built_at}"

export CGO_ENABLED=0 GOOS=darwin GOARCH=arm64
AG_GO_ENV="CGO_ENABLED GOOS GOARCH" \
  ag_go build -buildvcs=false -ldflags "$ldflags" -o "../dist/${out_name}" ./cmd/multica

cat <<MSG

Built dist/${out_name} (${version}) from ${sha}.

Install (does not touch the running daemon; see ag-patches/README.md):
  install -m 0755 dist/${out_name} ~/.local/bin/multica-ag
  ~/.local/bin/multica-ag version

Tag the built commit: git tag ag/${version} (never a tag starting with 'v').
MSG
