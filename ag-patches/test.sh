#!/usr/bin/env bash
# Guard tests for the AG patch branch. build-web.sh runs this before building,
# and .github/workflows/ag-patches.yml runs it on PRs into ag/**.
#
# vitest treats each filter as a substring match and only fails when NO file
# matches any filter, so listing swimlane-tree.test.ts before it exists (it
# arrives with the tree-ordered lanes patch) is safe while swimlane-view.test.tsx
# is present.
#
# The Go half runs TestAGPatch* in server/internal/daemon,
# server/internal/daemon/repocache, server/cmd/multica, server/internal/daemonws,
# server/internal/agdrain, server/internal/agquota, server/internal/agclaim, server/pkg/protocol,
# server/pkg/agent, server/pkg/taskfailure, server/internal/service,
# server/internal/integrations/ghpoll and server/internal/integrations/ghsnapshot
# (see PATCHES.md). `go test -run` passes silently on no match, so the passing
# top-level tests are counted against AG_GO_MIN_TESTS: raise it when a Go patch
# adds tests. The DB-backed TestAGPatch* tests in server/internal/handler and
# server/internal/integrations/ghsnapshot and server/internal/service are not run
# here (CI has no Postgres; they skip without one); run them with DATABASE_URL set
# (and REDIS_TEST_URL for the empty-claim test):
#   go test ./internal/handler ./internal/integrations/ghsnapshot ./internal/service -run '^TestAGPatch'
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
. ./ag-patches/lib.sh

pnpm install --frozen-lockfile

pnpm --filter @multica/views exec vitest run \
  issues/components/swimlane-view.test.tsx \
  issues/components/swimlane-view.lane-drag.ag.test.tsx \
  issues/components/swimlane-view.tree-lanes.ag.test.tsx \
  issues/components/swimlane-tree.test.ts \
  issues/components/swimlane-band.test.ts \
  issues/components/tree-rows.test.ts \
  issues/components/tree-view.test.tsx \
  issues/components/tree-chart-layout.test.ts \
  issues/components/tree-chart-layout.invariants.test.ts \
  issues/components/tree-chart-viewport.test.ts \
  issues/components/tree-chart-view.test.tsx \
  issues/components/issues-header.tree-layout.test.tsx \
  issues/components/issues-header.tree-mode.test.tsx \
  issues/surface/tree-mode.test.tsx \
  issues/surface/tree-mode-surfaces.test.tsx \
  issues/surface/tree-chart-mode.test.tsx \
  issues/surface/tree-chart-surface.test.tsx \
  issues/surface/issue-surface.test.tsx \
  issues/surface/issue-surface.ag.test.tsx \
  issues/components/save-view-dialog.test.tsx \
  issues/components/save-view-dialog.tree-mode.ag.test.tsx \
  common/actor-issues-panel.test.tsx \
  common/actor-issues-panel.ag.test.tsx \
  projects/components/project-detail.test.tsx \
  projects/components/project-detail.ag.test.tsx \
  settings/components/issue-statuses-tab.test.tsx \
  settings/components/issue-statuses-tab.ag.test.tsx \
  runtimes/components/drain-badge.test.tsx \
  runtimes/components/runtime-list-drain.test.tsx \
  runtimes/components/runtime-detail-drain.test.tsx \
  locales/ag-drain.test.ts \
  locales/ag-tree.test.ts

pnpm --filter @multica/core exec vitest run \
  runtimes/ag-drain.test.ts \
  issues/stores/tree-view-store.test.ts \
  issues/stores/tree-layout-store.test.ts \
  issues/stores/tree-layout-store.persist.test.ts \
  issues/queries.test.ts \
  issues/queries.tree-lanes.ag.test.ts \
  issues/queries.tree-view.ag.test.ts \
  issues/queries.test.ts

pnpm --filter @multica/views typecheck

# --- Go: daemon, CLI, drain-badge and GitHub polling patch guard ------------

AG_GO_MIN_TESTS=96

go_out="$(mktemp)"
trap 'rm -f "$go_out"' EXIT
if ! ag_go test ./internal/daemon ./internal/daemon/repocache ./cmd/multica ./internal/daemonws ./internal/agdrain ./internal/agclaim ./pkg/protocol ./internal/integrations/ghpoll ./internal/integrations/ghsnapshot ./internal/service -run '^TestAGPatch' -count=1 -v >"$go_out" 2>&1; then
  cat "$go_out"
  echo "ag-patches/test.sh: Go patch tests failed" >&2
  exit 1
fi

passed="$(grep -c '^--- PASS: TestAGPatch' "$go_out" || true)"
if [ "$passed" -lt "$AG_GO_MIN_TESTS" ]; then
  cat "$go_out"
  echo "ag-patches/test.sh: expected at least $AG_GO_MIN_TESTS passing TestAGPatch tests, saw $passed" >&2
  exit 1
fi
echo "Go patch tests: $passed passed"
