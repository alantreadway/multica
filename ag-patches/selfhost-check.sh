#!/usr/bin/env bash
# selfhost-check.sh: one-screen health check of the patched self-host on Bastion
# (home-sysadmin's multica-ag and multica-ag-daemon Compose projects). Read-only.
# Run it after a deploy and whenever something looks off. See README.md
# ("Deploy to the self-host").
#
# Host, URLs and helpers come from home-sysadmin's scripts/multica-ag/lib.sh, so
# no tailnet names live in this repo.
#
# Settings (environment):
#   AG_SYSADMIN     home-sysadmin checkout   (default ~/git/home-sysadmin)
#   AG_CHECK_SINCE  log window               (default 15m)
#   MULTICA_BIN     Mac CLI                  (default multica-ag)
#
# Exit status: 0 all OK, 1 at least one FAIL (WARNs alone exit 0).
set -u -o pipefail

SYSADMIN=${AG_SYSADMIN:-$HOME/git/home-sysadmin}
LIB=$SYSADMIN/scripts/multica-ag/lib.sh
[ -f "$LIB" ] || { echo "selfhost-check: $LIB not found (set AG_SYSADMIN)" >&2; exit 1; }
# shellcheck source=/dev/null
. "$LIB"
set +e # lib.sh sets -e; this script reports failures instead of stopping on them

SINCE=${AG_CHECK_SINCE:-15m}
MULTICA=${MULTICA_BIN:-multica-ag}

fails=0 warns=0
ok() { printf '  OK    %s\n' "$*"; }
warn() { printf '  WARN  %s\n' "$*"; warns=$((warns + 1)); }
fail() { printf '  FAIL  %s\n' "$*"; fails=$((fails + 1)); }

if ! b true 2>/dev/null; then
  echo "selfhost-check: cannot ssh $HOST" >&2
  exit 1
fi

pin=$(deployed_version)
echo "Containers ($HOST, compose pins ${pin:-?})"
[ -n "$pin" ] || fail "no multica-backend-ag pin in $CM_DIR/docker-compose.yml"
for c in multica-ag-backend multica-ag-web multica-ag-postgres multica-ag-edge "$D_CTR"; do
  line=$(b "docker ps --filter name=^$c\$ --format '{{.Image}}  {{.Status}}'" 2>/dev/null)
  if [ -z "$line" ]; then fail "$c not running"; continue; fi
  case "$c" in
    multica-ag-backend | multica-ag-web)
      rev=$(b "docker inspect $c --format '{{index .Config.Labels \"org.opencontainers.image.revision\"}}'" 2>/dev/null)
      img=${line%%  *}
      if [ -n "$pin" ] && [ "${img##*:}" != "$pin" ]; then
        fail "$c $line: compose pins $pin (run deploy.sh --apply $pin)"
      else
        ok "$c $line${rev:+ (${rev:0:9})}"
      fi
      ;;
    *) ok "$c $line" ;;
  esac
done

echo "Database"
migs=$(b "docker exec multica-ag-postgres sh -c 'psql -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\" -Atc \"select version from schema_migrations order by version desc limit 4\"'" 2>/dev/null | tr '\n' ' ')
[ -n "$migs" ] && ok "latest migrations: $migs" || fail "cannot read schema_migrations"

echo "Endpoints"
for u in "$API_URL/health" "$WEB_URL/"; do
  code=$(curl -s -o /dev/null -m 8 -w '%{http_code}' "$u")
  [ "$code" = 200 ] && ok "$u 200" || fail "$u -> ${code:-no answer}"
done
health=$(curl -fsS -m 8 "$API_URL/health" 2>/dev/null)
api_commit=$(jq -r '.commit // empty' <<<"$health" 2>/dev/null)
[ -n "$api_commit" ] && ok "backend commit ${api_commit:0:9}"

echo "Backend log (last $SINCE)"
errs=$(b "docker logs --since $SINCE multica-ag-backend 2>&1 | grep -cE ' (ERR|FTL) |panic:'" 2>/dev/null)
[ "${errs:-0}" -eq 0 ] && ok "no ERR/FTL/panic lines" || warn "$errs ERR/FTL/panic lines: docker logs --since $SINCE multica-ag-backend"

echo "GitHub polling (last $SINCE)"
if [ "$(b "docker exec multica-ag-backend sh -c 'echo \${#GITHUB_POLL_TOKEN}'" 2>/dev/null)" -gt 0 ] 2>/dev/null; then
  polls=$(b "docker logs --since $SINCE multica-ag-backend 2>&1 | grep -c 'ghpoll: poll failed'" 2>/dev/null)
  if b "docker exec multica-ag-backend wget -qO- -T 8 https://api.github.com/zen" >/dev/null 2>&1; then
    ok "api.github.com reachable from the backend"
  else
    fail "api.github.com unreachable from the backend"
  fi
  [ "${polls:-0}" -eq 0 ] && ok "no poll failures" || warn "$polls poll failures"
else
  warn "GITHUB_POLL_TOKEN is empty in the backend: polling is off"
fi

echo "Daemon ($D_CTR)"
st=$(daemon_status_json 2>/dev/null)
if [ "$(jq -r '.status // empty' <<<"$st" 2>/dev/null)" = running ]; then
  ok "$(jq -r '"daemon \(.cli_version // "?") claiming=\((.draining // false) | not) active=\(.active_task_count // 0) cap=\(.max_concurrent_tasks // "?")"' <<<"$st")"
  dv=$(jq -r '.cli_version // empty' <<<"$st")
  [ -z "$pin" ] || [ "$dv" = "$pin" ] || warn "daemon CLI $dv, compose pins $pin"
  [ "$(jq -r '.draining // false' <<<"$st")" = true ] && warn "daemon is draining: resume it unless a deploy or handoff is under way"
else
  fail "daemon not running (docker logs $D_CTR)"
fi
b "test -e $D_HOME/.multica/hold-daemon" && warn "restart loop is held ($D_HOME/.multica/hold-daemon): a deploy stopped mid-way?"

echo "Mac CLI"
if command -v "$MULTICA" >/dev/null 2>&1; then
  mv=$("$MULTICA" version 2>/dev/null | head -n 1)
  case "$mv" in
    *"$pin"*) ok "$mv" ;;
    *) warn "$mv (self-host is $pin; deploy.sh installs it when dist/multica-$pin-darwin-arm64 exists)" ;;
  esac
else
  warn "$MULTICA not on PATH"
fi

echo
if [ "$fails" -gt 0 ]; then
  echo "selfhost-check: $fails FAIL, $warns WARN"
  exit 1
fi
if [ "$warns" -gt 0 ]; then echo "selfhost-check: OK ($warns WARN)"; else echo "selfhost-check: OK"; fi
