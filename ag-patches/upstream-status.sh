#!/usr/bin/env bash
# Status of every upstream PR and issue that PATCHES.md links to, for the
# "Re-check upstream" step of a rebase. Read-only: one GraphQL query, no writes.
#
#   ag-patches/upstream-status.sh [<patches-file>]
#
# Collects each https://github.com/multica-ai/multica/(pull|issues)/N link in
# PATCHES.md (or the file given), dedupes by number, and prints one row per
# number: kind (PR/issue), state (open, closed, merged; closed issues add the
# reason), last update, review decision (PRs), comment and reaction counts, and
# the title. Needs `gh` logged in.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
file="${1:-ag-patches/PATCHES.md}"

if [ ! -f "$file" ]; then
  echo "error: ${file} not found" >&2
  exit 2
fi

numbers="$(grep -oE 'https://github\.com/multica-ai/multica/(pull|issues)/[0-9]+' "$file" |
  sed 's#.*/##' | sort -n -u)"
if [ -z "$numbers" ]; then
  echo "No multica-ai/multica links in ${file}."
  exit 0
fi

fields='number title updatedAt reactions { totalCount } comments { totalCount }'
query='query { repository(owner: "multica-ai", name: "multica") {'
for n in $numbers; do
  query+=" n${n}: issueOrPullRequest(number: ${n}) {
    __typename
    ... on Issue { ${fields} state stateReason }
    ... on PullRequest { ${fields} state reviewDecision }
  }"
done
query+=' } }'

# Each alias becomes one tab-separated row. A number that no longer resolves
# comes back null and is listed as missing.
jq_rows='.data.repository | to_entries[] |
  if .value == null then
    [(.key | ltrimstr("n")), "?", "missing", "-", "-", "-", "-", "-"]
  else .value |
    [ (.number | tostring),
      (if .__typename == "PullRequest" then "PR" else "issue" end),
      ((.state | ascii_downcase) +
        (if .__typename == "Issue" and .state == "CLOSED" and .stateReason != null
         then "/" + (.stateReason | ascii_downcase) else "" end)),
      (.updatedAt[0:10]),
      (if .__typename == "PullRequest" then ((.reviewDecision // "none") | ascii_downcase) else "-" end),
      (.comments.totalCount | tostring),
      (.reactions.totalCount | tostring),
      (if (.title | length) > 60 then .title[0:57] + "..." else .title end) ]
  end | @tsv'

rows="$(gh api graphql -f query="$query" --jq "$jq_rows")"

{
  printf 'NUMBER\tKIND\tSTATE\tUPDATED\tREVIEW\tCOMMENTS\tREACTIONS\tTITLE\n'
  printf '%s\n' "$rows" | sort -t$'\t' -k1,1n
} | column -t -s$'\t'
