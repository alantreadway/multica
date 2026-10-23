#!/usr/bin/env bash
# Guard tests for the AG patch branch. The build scripts run this before
# building, and .github/workflows/ag-patches.yml runs it on PRs into ag/**.
#
# Nothing here lists a patch's tests, so a patch never edits this file. The
# tests are found by diffing against the upstream tag (ag_upstream_tag):
#
# - vitest, per package (views, core): every test file the patches add or
#   change; the upstream x.test.ts(x) beside every source file they change; and
#   the upstream test an AG sibling extends (x.ag.test.tsx and
#   x.<topic>.ag.test.tsx run beside x.test.tsx).
# - Go: TestAGPatch* in every package that declares one. `go test -run` passes
#   silently when nothing matches, so a package that reports
#   "[no tests to run]" fails the guard. The DB-backed tests (handler,
#   ghsnapshot, service) skip without Postgres; run them with
#   ag-patches/test-go-full.sh.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
. ./ag-patches/lib.sh

tag="$(ag_upstream_tag)"
if [ -z "$tag" ]; then
  echo "ag-patches/test.sh: can't tell the upstream tag; run git fetch upstream --tags or set AG_UPSTREAM_TAG" >&2
  exit 2
fi
echo "Guard against upstream $tag"

# ag_vitest_files <package dir>: test files to run, relative to that package.
ag_vitest_files() {
  local pkg="$1" f b t
  git diff --name-only --diff-filter=AMR "$tag" HEAD -- "$pkg" |
    while read -r f; do
      case "$f" in
        *.test.ts | *.test.tsx)
          echo "$f"
          case "$f" in
            *.ag.test.*)
              b="${f%.ag.test.*}"
              for t in "$b".test.ts "$b".test.tsx "${b%.*}".test.ts "${b%.*}".test.tsx; do
                [ -f "$t" ] && echo "$t"
              done
              ;;
          esac
          ;;
        *.ts | *.tsx)
          b="${f%.*}"
          for t in "$b".test.ts "$b".test.tsx; do
            [ -f "$t" ] && echo "$t"
          done
          ;;
      esac
    done | sed "s|^$pkg/||" | sort -u
}

pnpm install --frozen-lockfile

for pkg in views core; do
  files=()
  while IFS= read -r f; do files+=("$f"); done < <(ag_vitest_files "packages/$pkg")
  if [ "${#files[@]}" -eq 0 ]; then
    echo "No patched tests in packages/$pkg"
    continue
  fi
  echo "vitest @multica/$pkg: ${#files[@]} files"
  pnpm --filter "@multica/$pkg" exec vitest run "${files[@]}"
done

pnpm --filter @multica/views typecheck

# --- Go ---------------------------------------------------------------------

go_pkgs=()
while IFS= read -r d; do go_pkgs+=("./${d#server/}"); done < <(
  git grep -l 'func TestAGPatch' -- 'server/*_test.go' | sed 's|/[^/]*$||' | sort -u
)
if [ "${#go_pkgs[@]}" -eq 0 ]; then
  echo "No TestAGPatch tests"
  exit 0
fi

go_out="$(mktemp)"
trap 'rm -f "$go_out"' EXIT
if ! ag_go test "${go_pkgs[@]}" -run '^TestAGPatch' -count=1 -v >"$go_out" 2>&1; then
  cat "$go_out"
  echo "ag-patches/test.sh: Go patch tests failed" >&2
  exit 1
fi

if grep -q '\[no tests to run\]' "$go_out"; then
  grep '\[no tests to run\]' "$go_out" >&2
  echo "ag-patches/test.sh: a package declares TestAGPatch tests but -run matched none of them" >&2
  exit 1
fi
passed="$(grep -c '^--- PASS: TestAGPatch' "$go_out" || true)"
skipped="$(grep -c '^--- SKIP: TestAGPatch' "$go_out" || true)"
echo "Go patch tests: $passed passed, $skipped skipped (no database) in ${#go_pkgs[@]} packages"
