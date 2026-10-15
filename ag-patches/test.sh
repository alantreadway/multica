#!/usr/bin/env bash
# Guard tests for the AG patch branch. build-web.sh runs this before building,
# and .github/workflows/ag-patches.yml runs it on PRs into ag/**.
#
# vitest treats each filter as a substring match and only fails when NO file
# matches any filter, so listing swimlane-tree.test.ts before it exists (it
# arrives with the tree-ordered lanes patch) is safe while swimlane-view.test.tsx
# is present.
#
# The Go half runs TestAGPatch* in server/internal/daemon (see PATCHES.md). `go test
# -run` passes silently on no match, so the passing top-level tests are counted
# against AG_GO_MIN_TESTS: raise it when a Go patch adds tests.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

pnpm install --frozen-lockfile

pnpm --filter @multica/views exec vitest run \
  issues/components/swimlane-view.test.tsx \
  issues/components/swimlane-tree.test.ts

pnpm --filter @multica/views typecheck

# --- Go: daemon patch guard -------------------------------------------------

AG_GO_MIN_TESTS=5

go_version="$(awk '/^go /{print $2; exit}' server/go.mod)"

if command -v go >/dev/null 2>&1; then
  go_test() { (cd server && go "$@"); }
elif command -v docker >/dev/null 2>&1; then
  echo "go not found; running Go tests in golang:${go_version} via docker"
  go_test() {
    docker run --rm \
      -v "$PWD":/src -w /src/server \
      -v multica-ag-gomod:/go/pkg/mod \
      "golang:${go_version}" go "$@"
  }
else
  echo "ag-patches/test.sh: need either go or docker to run the daemon patch tests" >&2
  exit 1
fi

go_out="$(mktemp)"
trap 'rm -f "$go_out"' EXIT
if ! go_test test ./internal/daemon -run '^TestAGPatch' -count=1 -v >"$go_out" 2>&1; then
  cat "$go_out"
  echo "ag-patches/test.sh: daemon patch tests failed" >&2
  exit 1
fi

passed="$(grep -c '^--- PASS: TestAGPatch' "$go_out" || true)"
if [ "$passed" -lt "$AG_GO_MIN_TESTS" ]; then
  cat "$go_out"
  echo "ag-patches/test.sh: expected at least $AG_GO_MIN_TESTS passing TestAGPatch tests, saw $passed" >&2
  exit 1
fi
echo "daemon patch tests: $passed passed"
