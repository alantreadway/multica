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

pnpm --filter @multica/views typecheck
