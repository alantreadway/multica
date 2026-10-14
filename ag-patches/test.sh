#!/usr/bin/env bash
# Guard tests for the AG patch branch. build-web.sh runs this before building,
# and .github/workflows/ag-patches.yml runs it on PRs into ag/**.
#
# vitest treats each filter as a substring match and only fails when NO file
# matches any filter, so listing swimlane-tree.test.ts before it exists (it
# arrives with the tree-ordered lanes patch) is safe while swimlane-view.test.tsx
# is present.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

pnpm install --frozen-lockfile

pnpm --filter @multica/views exec vitest run \
  issues/components/swimlane-view.test.tsx \
  issues/components/swimlane-tree.test.ts

pnpm --filter @multica/views typecheck
