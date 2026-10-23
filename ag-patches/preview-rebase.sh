#!/usr/bin/env bash
# Lists the files that would conflict if the patch branch were rebased onto a
# new upstream tag. Read-only: it writes no refs and leaves the work tree alone.
#
#   ag-patches/preview-rebase.sh <new-tag> [<branch>]
#
# <branch> defaults to the current branch, which must be a patch branch ag/*.
# It runs `git merge-tree --write-tree` of the branch against <new-tag>; git
# picks the merge base itself, normally the old upstream tag. That merges the
# final trees in one step, so it shows the conflicts the finished rebase has to
# resolve, not each commit's: a rebase can also stop on a commit whose change a
# later commit undoes. Fetch tags first (git fetch upstream --tags).
#
# Exit status: 0 no conflicts, 1 conflicts (listed), 2 usage or other error.
set -euo pipefail

usage() {
  echo "usage: ag-patches/preview-rebase.sh <new-tag> [<branch>]" >&2
  exit 2
}

[ $# -ge 1 ] && [ $# -le 2 ] || usage
new_tag="$1"
case "$new_tag" in -*) usage ;; esac

cd "$(git rev-parse --show-toplevel)"

if [ $# -eq 2 ]; then
  branch="$2"
else
  branch="$(git symbolic-ref --quiet --short HEAD || true)"
  case "$branch" in
    ag/*) ;;
    *)
      echo "error: not on a patch branch ag/* (on '${branch:-detached HEAD}'); name the branch as the second argument" >&2
      exit 2
      ;;
  esac
fi

if ! git rev-parse --verify --quiet "refs/tags/${new_tag}^{commit}" >/dev/null; then
  echo "error: tag '${new_tag}' not found; run: git fetch upstream --tags" >&2
  exit 2
fi
if ! git rev-parse --verify --quiet "${branch}^{commit}" >/dev/null; then
  echo "error: branch '${branch}' not found" >&2
  exit 2
fi

base="$(git merge-base "$branch" "$new_tag" || true)"
if [ -z "$base" ]; then
  echo "error: ${branch} and ${new_tag} have no common history" >&2
  exit 2
fi
if git merge-base --is-ancestor "$branch" "$new_tag"; then
  echo "${branch} is already contained in ${new_tag}: nothing to replay"
  exit 0
fi

base_name="$(git describe --tags --exact-match --match 'v[0-9]*' "$base" 2>/dev/null || git rev-parse --short "$base")"
old_tag="$(git describe --tags --abbrev=0 --match 'v[0-9]*' "$branch" 2>/dev/null || echo "$base_name")"
commits="$(git rev-list --count --no-merges "${old_tag}..${branch}")"

echo "Preview: rebase ${branch} onto ${new_tag}"
echo "  merge base: ${base_name}"
echo "  patch commits since ${old_tag}: ${commits}"
echo

out="$(mktemp)"
trap 'rm -f "$out"' EXIT
set +e
git merge-tree --write-tree --name-only "$branch" "$new_tag" >"$out" 2>&1
rc=$?
set -e

case "$rc" in
  0)
    echo "No conflicts."
    exit 0
    ;;
  1) ;;
  *)
    cat "$out" >&2
    echo "error: git merge-tree failed (exit $rc)" >&2
    exit 2
    ;;
esac

# Output: the tree id, the conflicted paths, a blank line, then messages.
files="$(awk 'NR == 1 { next } /^$/ { exit } { print }' "$out")"
count="$(printf '%s\n' "$files" | grep -c . || true)"

echo "Conflicts in ${count} file(s):"
printf '%s\n' "$files" | sed 's/^/  /'
echo
echo "Details:"
awk 'f && /^CONFLICT/ { print "  " $0 } /^$/ { f = 1 }' "$out"
exit 1
