#!/usr/bin/env bash
# selfhost-check.sh: one-screen health check of the patched self-host and the
# local daemon. Read-only. Run it after a deploy (deploy-selfhost.sh does) and
# whenever something looks off. See README.md ("Deploy to the self-host").
#
# Settings (environment):
#   AG_SELFHOST_DIR       compose directory          (default ~/multica-selfhost)
#   AG_SELFHOST_CONTEXT   Docker context it runs in  (default colima)
#   AG_SELFHOST_API       backend URL                (default http://localhost:8080)
#   AG_SELFHOST_WEB       frontend URL               (default http://localhost:3000)
#   AG_CHECK_SINCE        log window                 (default 15m)
#   MULTICA_BIN           patched CLI                (default multica-ag)
#   MULTICA_DAEMON_LABEL_BASE  launchd labels <base>.a/.b (default uk.blogic.multica-daemon)
#
# Exit status: 0 all OK, 1 at least one FAIL (WARNs alone exit 0).
set -u -o pipefail

DIR=${AG_SELFHOST_DIR:-$HOME/multica-selfhost}
export DOCKER_CONTEXT=${AG_SELFHOST_CONTEXT:-colima}
API=${AG_SELFHOST_API:-http://localhost:8080}
WEB=${AG_SELFHOST_WEB:-http://localhost:3000}
SINCE=${AG_CHECK_SINCE:-15m}
MULTICA=${MULTICA_BIN:-multica-ag}
BASE_LABEL=${MULTICA_DAEMON_LABEL_BASE:-uk.blogic.multica-daemon}
DOMAIN=gui/$(id -u)

fails=0 warns=0
ok() { printf '  OK    %s\n' "$*"; }
warn() { printf '  WARN  %s\n' "$*"; warns=$((warns + 1)); }
fail() { printf '  FAIL  %s\n' "$*"; fails=$((fails + 1)); }
env_of() { sed -n "s/^$1=//p" "$DIR/.env" 2>/dev/null | tail -n 1; }

echo "Containers ($DOCKER_CONTEXT, $DIR)"
tag=$(env_of MULTICA_IMAGE_TAG)
[ -n "$tag" ] && ok "MULTICA_IMAGE_TAG=$tag" || fail "no MULTICA_IMAGE_TAG in $DIR/.env"
for svc in backend frontend postgres edge; do
  c="multica-$svc-1"
  line=$(docker ps --filter "name=^$c\$" --format '{{.Image}}  {{.Status}}' 2>/dev/null)
  if [ -z "$line" ]; then fail "$c not running"; continue; fi
  case "$svc" in
    backend | frontend)
      rev=$(docker inspect "$c" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' 2>/dev/null)
      base=$(docker inspect "$c" --format '{{index .Config.Labels "ag.upstream-tag"}}' 2>/dev/null)
      if [ -n "$base" ] && [ "$base" != "$tag" ]; then
        fail "$c $line: built on $base, MULTICA_IMAGE_TAG is $tag"
      else
        ok "$c $line${rev:+ (${rev:0:9})}"
      fi
      ;;
    *) ok "$c $line" ;;
  esac
done

echo "Database"
migs=$(docker exec multica-postgres-1 psql -U "$(env_of POSTGRES_USER | grep . || echo multica)" -d "$(env_of POSTGRES_DB | grep . || echo multica)" -Atc \
  "select version from schema_migrations order by version desc limit 4" 2>/dev/null | tr '\n' ' ')
[ -n "$migs" ] && ok "latest migrations: $migs" || fail "cannot read schema_migrations"

echo "Endpoints"
for u in "$API/health" "$WEB/"; do
  code=$(curl -s -o /dev/null -m 8 -w '%{http_code}' "$u")
  [ "$code" = 200 ] && ok "$u 200" || fail "$u -> ${code:-no answer}"
done

echo "GitHub polling (last $SINCE)"
if [ "$(docker exec multica-backend-1 sh -c 'echo ${#GITHUB_POLL_TOKEN}' 2>/dev/null)" -gt 0 ] 2>/dev/null; then
  polls=$(docker logs --since "$SINCE" multica-backend-1 2>&1 | grep -c 'ghpoll: poll failed')
  resolver=$(docker logs --since "$SINCE" multica-edge-1 2>&1 | grep -c 'could not be resolved')
  if docker exec multica-backend-1 wget -qO- -T 8 https://api.github.com/zen >/dev/null 2>&1; then
    ok "api.github.com reachable from the backend"
  else
    fail "api.github.com unreachable from the backend (edge resolver errors: $resolver; try 'docker exec multica-edge-1 nginx -s reload')"
  fi
  [ "$polls" -eq 0 ] && ok "no poll failures" || warn "$polls poll failures, $resolver edge resolver errors"
else
  warn "GITHUB_POLL_TOKEN is empty in the backend: polling is off (all three compose files used?)"
fi

echo "Daemon"
if command -v "$MULTICA" >/dev/null 2>&1; then
  ok "CLI $("$MULTICA" version 2>/dev/null | head -n 1)"
  st=$("$MULTICA" daemon status --output json 2>/dev/null)
  if [ "$(jq -r '.status // empty' <<<"$st" 2>/dev/null)" = running ]; then
    ok "$(jq -r '"daemon \(.cli_version // "?") pid \(.pid) claiming=\((.draining // false) | not) active=\(.active_task_count // 0)"' <<<"$st")"
    [ "$(jq -r '.draining // false' <<<"$st")" = true ] && warn "daemon is draining: run '$MULTICA daemon resume' unless a handoff is under way"
  else
    fail "daemon not running"
  fi
else
  warn "$MULTICA not on PATH; daemon not checked"
fi

echo "launchd ($BASE_LABEL.a/.b)"
running=0
for l in "$BASE_LABEL.a" "$BASE_LABEL.b"; do
  out=$(launchctl print "$DOMAIN/$l" 2>/dev/null) || { ok "$l not loaded"; continue; }
  field() { sed -n "s/^[[:space:]]*$1 = \(.*\)/\1/p" <<<"$out" | head -n 1; }
  pid=$(field pid) state=$(field state) runs=$(field runs) last=$(field 'last exit code')
  if [ -n "$pid" ]; then
    running=$((running + 1))
    ok "$l running pid $pid (runs $runs)"
  elif [ "$state" = "spawn scheduled" ] || { [ "${runs:-0}" -gt 20 ] && [ "${last:-0}" != 0 ]; }; then
    fail "$l respawn loop: state $state, runs $runs, last exit $last (see ~/.multica/launchd.${l##*.}.err.log; README 'Two-label reload script')"
  else
    ok "$l loaded, idle (last exit ${last:-?})"
  fi
done
[ "$running" -le 1 ] || warn "both labels have a running job: a handoff is draining ('$MULTICA daemon status' shows the Predecessor)"

echo
if [ "$fails" -gt 0 ]; then
  echo "selfhost-check: $fails FAIL, $warns WARN"
  exit 1
fi
if [ "$warns" -gt 0 ]; then echo "selfhost-check: OK ($warns WARN)"; else echo "selfhost-check: OK"; fi
