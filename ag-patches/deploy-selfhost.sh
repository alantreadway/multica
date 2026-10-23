#!/usr/bin/env bash
# deploy-selfhost.sh: deploy a built ag/<tag>-ag.<n> to the self-host and the
# local daemon. See README.md ("Deploy to the self-host").
#
#   ag-patches/deploy-selfhost.sh v0.7.1-ag.1 [--dry-run] [--skip-daemon]
#
# Steps, each skipped when already done:
#   1. preflight: images built for this version, CLI binary, .env and compose
#      file as expected. Nothing changes if any check fails.
#   2. load the images from the build context into the self-host context and
#      point :<tag> at this version (the compose file runs :<MULTICA_IMAGE_TAG>)
#   3. pg_dump backup to backups/multica-pre-<version>-<time>.sql.gz
#   4. on a new upstream tag: save .env and the compose file, install the tag's
#      docker-compose.selfhost.yml, set MULTICA_IMAGE_TAG
#   5. recreate the backend (migrations run on start), wait for /health, then
#      recreate the frontend
#   6. install the CLI as $MULTICA_BIN, keeping the previous one beside it
#   7. hand the daemon over with multica-daemon-reload (unless --skip-daemon)
#   8. selfhost-check.sh
#
# Settings (environment): AG_SELFHOST_DIR, AG_SELFHOST_CONTEXT, AG_SELFHOST_API,
# AG_SELFHOST_WEB (as selfhost-check.sh), AG_BUILD_CONTEXT (default
# colima-agbuild), AG_COMPOSE_FILES (default: the three files DEPLOY.md
# requires), MULTICA_BIN (default multica-ag, installed to ~/.local/bin).
#
# Exit status: 0 deployed, 1 a step failed, 2 usage or preflight.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/.." && pwd)"
DIR=${AG_SELFHOST_DIR:-$HOME/multica-selfhost}
SELFHOST_CTX=${AG_SELFHOST_CONTEXT:-colima}
BUILD_CTX=${AG_BUILD_CONTEXT:-colima-agbuild}
API=${AG_SELFHOST_API:-http://localhost:8080}
WEB=${AG_SELFHOST_WEB:-http://localhost:3000}
read -r -a COMPOSE_FILES <<<"${AG_COMPOSE_FILES:-docker-compose.selfhost.yml docker-compose.isolated.yml docker-compose.github-poll.yml}"
MULTICA_NAME=${MULTICA_BIN:-multica-ag}
BIN_DIR=$HOME/.local/bin

die() { echo "deploy-selfhost: $1" >&2; exit "${2:-1}"; }
say() { echo "$(date +%T) $*"; }

version="" dry=0 skip_daemon=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run | -n) dry=1 ;;
    --skip-daemon) skip_daemon=1 ;;
    -h | --help) sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    v*-ag.*) version="$1" ;;
    *) die "usage: deploy-selfhost.sh <tag>-ag.<n> [--dry-run] [--skip-daemon]" 2 ;;
  esac
  shift
done
[ -n "$version" ] || die "usage: deploy-selfhost.sh <tag>-ag.<n> [--dry-run] [--skip-daemon]" 2
tag="${version%-ag.*}"
[ -n "${MULTICA_TASK_ID:-}" ] && die "refusing to run inside a Multica agent run: it replaces the daemon that run lives under" 2

run() {
  if [ "$dry" = 1 ]; then echo "  would run: $*"; else "$@"; fi
}
dsh() { docker --context "$SELFHOST_CTX" "$@"; }
dbuild() { docker --context "$BUILD_CTX" "$@"; }
compose() {
  local f args=()
  for f in "${COMPOSE_FILES[@]}"; do args+=(-f "$f"); done
  (cd "$DIR" && docker --context "$SELFHOST_CTX" compose "${args[@]}" "$@")
}
env_of() { sed -n "s/^$1=//p" "$DIR/.env" | tail -n 1; }
image_id() { "$1" image inspect "$2" --format '{{.Id}}' 2>/dev/null || true; }
label_of() { "$1" image inspect "$2" --format "{{index .Config.Labels \"$3\"}}" 2>/dev/null || true; }

# --- 1. preflight -------------------------------------------------------------

say "Preflight for $version (upstream $tag)"
[ -f "$DIR/.env" ] || die "no $DIR/.env" 2
for f in "${COMPOSE_FILES[@]}"; do [ -f "$DIR/$f" ] || die "missing $DIR/$f" 2; done
[ "$(env_of MULTICA_WEB_IMAGE)" = multica-web-ag ] || die "MULTICA_WEB_IMAGE is not multica-web-ag in .env; switch by hand first (README 'Switch the self-host')" 2
[ "$(env_of MULTICA_BACKEND_IMAGE)" = multica-backend-ag ] || die "MULTICA_BACKEND_IMAGE is not multica-backend-ag in .env (README 'Backend image')" 2
cur_tag="$(env_of MULTICA_IMAGE_TAG)"
[ -n "$cur_tag" ] || die "no MULTICA_IMAGE_TAG in .env" 2
git -C "$repo" rev-parse --verify --quiet "refs/tags/$tag^{commit}" >/dev/null || die "upstream tag $tag not found; git fetch upstream --tags" 2

# image_src <image>: dsh when the self-host already has <image>:<version>, else
# dbuild when the build context has it, else nothing.
image_src() {
  if [ -n "$(image_id dsh "$1:$version")" ]; then echo dsh
  elif [ -n "$(image_id dbuild "$1:$version")" ]; then echo dbuild
  fi
}
for img in multica-backend-ag multica-web-ag; do
  src="$(image_src "$img")"
  [ -n "$src" ] || die "$img:$version is in neither $SELFHOST_CTX nor $BUILD_CTX; build it (README 'Build'; start the agbuild VM if it is stopped)" 2
  built_on="$(label_of "$src" "$img:$version" ag.upstream-tag)"
  [ "$built_on" = "$tag" ] || die "$img:$version was built on '$built_on', not $tag" 2
  echo "  $img:$version in $([ "$src" = dsh ] && echo "$SELFHOST_CTX" || echo "$BUILD_CTX"), revision $(label_of "$src" "$img:$version" org.opencontainers.image.revision | cut -c1-9)"
done

cli="$repo/dist/multica-$version-darwin-arm64"
[ -x "$cli" ] || die "no CLI binary $cli; run ag-patches/build-cli.sh" 2
echo "  CLI $("$cli" version | head -n 1)"

new_compose="$(git -C "$repo" show "$tag:docker-compose.selfhost.yml")"
if [ "$cur_tag" != "$tag" ]; then
  # Local edits belong in docker-compose.isolated.yml; refuse to overwrite any.
  git -C "$repo" show "$cur_tag:docker-compose.selfhost.yml" | cmp -s - "$DIR/docker-compose.selfhost.yml" ||
    die "$DIR/docker-compose.selfhost.yml differs from upstream $cur_tag's; move local edits into docker-compose.isolated.yml first" 2
  echo "  upgrade: MULTICA_IMAGE_TAG $cur_tag -> $tag, compose file replaced with $tag's"
  echo "  $(git -C "$repo" diff --stat "$cur_tag" "$tag" -- server/migrations | tail -n 1 | sed 's/^ *//') (server/migrations)"
else
  printf '%s\n' "$new_compose" | cmp -s - "$DIR/docker-compose.selfhost.yml" ||
    die "$DIR/docker-compose.selfhost.yml differs from upstream $tag's; move local edits into docker-compose.isolated.yml first" 2
  echo "  same upstream tag ($tag): images and CLI only"
fi
[ "$dry" = 1 ] && echo "Dry run: the steps below show what would change."

# --- 2. images ----------------------------------------------------------------

for img in multica-backend-ag multica-web-ag; do
  if [ "$(image_src "$img")" = dbuild ]; then
    say "Loading $img:$version into $SELFHOST_CTX"
    [ "$dry" = 1 ] && echo "  would run: docker --context $BUILD_CTX save $img:$version | docker --context $SELFHOST_CTX load" ||
      dbuild save "$img:$version" | dsh load
  fi
  if [ "$(image_id dsh "$img:$tag")" != "$(image_id dsh "$img:$version")" ]; then
    run docker --context "$SELFHOST_CTX" tag "$img:$version" "$img:$tag"
  fi
done

# --- 3. backup ----------------------------------------------------------------

stamp="$(date +%Y%m%d-%H%M%S)"
backup="$DIR/backups/multica-pre-$version-$stamp.sql.gz"
if [ "$dry" = 1 ]; then
  say "Would back up the database to $backup"
else
  say "Backing up the database to $backup"
  mkdir -p "$DIR/backups"
  pg_user="$(env_of POSTGRES_USER)" pg_db="$(env_of POSTGRES_DB)"
  dsh exec multica-postgres-1 pg_dump -U "${pg_user:-multica}" -d "${pg_db:-multica}" | gzip >"$backup"
  gzip -t "$backup" || die "backup $backup is not a valid gzip"
  gunzip -c "$backup" | tail -n 5 | grep -q 'PostgreSQL database dump complete' || die "backup $backup is truncated"
  echo "  $(du -h "$backup" | cut -f1)"
fi

# --- 4. upstream upgrade --------------------------------------------------------

if [ "$cur_tag" != "$tag" ]; then
  say "Upgrading the compose file and MULTICA_IMAGE_TAG to $tag"
  run cp -p "$DIR/.env" "$DIR/.env.pre-$version"
  run cp -p "$DIR/docker-compose.selfhost.yml" "$DIR/docker-compose.selfhost.yml.pre-$tag"
  if [ "$dry" = 0 ]; then
    printf '%s\n' "$new_compose" >"$DIR/docker-compose.selfhost.yml"
    sed -i '' "s/^MULTICA_IMAGE_TAG=.*/MULTICA_IMAGE_TAG=$tag/" "$DIR/.env"
  fi
fi

# --- 5. backend, then frontend --------------------------------------------------

wait_200() {
  local url="$1"
  for _ in $(seq 1 60); do
    [ "$(curl -s -o /dev/null -m 5 -w '%{http_code}' "$url")" = 200 ] && return 0
    sleep 2
  done
  return 1
}

say "Recreating the backend"
if [ "$dry" = 0 ]; then
  started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  compose up -d backend
  wait_200 "$API/health" || die "backend not healthy at $API/health; roll back: README 'Backend image'"
  dsh logs --since "$started" multica-backend-1 2>&1 | grep -E '^  up  ' | sed 's/^  up  /  applied migration /' || echo "  no new migrations"
else
  echo "  would run: docker compose (${COMPOSE_FILES[*]}) up -d backend"
fi

say "Recreating the frontend"
if [ "$dry" = 0 ]; then
  compose up -d frontend
  wait_200 "$WEB/" || die "frontend not answering at $WEB/; roll back: README 'Roll back'"
else
  echo "  would run: docker compose (${COMPOSE_FILES[*]}) up -d frontend"
fi

# --- 6. CLI -------------------------------------------------------------------

installed="$BIN_DIR/$MULTICA_NAME"
if [ -x "$installed" ] && cmp -s "$cli" "$installed"; then
  say "CLI already $version"
else
  old_version="$("$installed" version 2>/dev/null | awk 'NR == 1 { print $2 }')"
  say "Installing the CLI as $installed (previous ${old_version:-none} kept beside it)"
  [ -z "$old_version" ] || run cp -p "$installed" "$installed.$old_version"
  run install -m 0755 "$cli" "$installed"
fi

# --- 7. daemon ----------------------------------------------------------------

daemon_version="$("$installed" daemon status --output json 2>/dev/null | sed -n 's/^  "cli_version": "\(.*\)",*$/\1/p')"
if [ "$skip_daemon" = 1 ]; then
  say "Skipping the daemon handoff (--skip-daemon); it moves to the new CLI at its next idle auto-reload"
elif [ "$daemon_version" = "$version" ]; then
  say "Daemon already runs $version"
else
  say "Handing the daemon over to the new CLI"
  reload="$HOME/bin/multica-daemon-reload"
  cmp -s "$here/multica-daemon-reload" "$reload" || die "$reload differs from ag-patches/multica-daemon-reload; reinstall it (README 'Two-label reload script')"
  if [ "$dry" = 1 ]; then
    MULTICA_BIN="$MULTICA_NAME" "$reload" --dry-run
  else
    MULTICA_BIN="$MULTICA_NAME" "$reload"
  fi
fi

# --- 8. check -----------------------------------------------------------------

[ "$dry" = 1 ] && { echo "deploy-selfhost: dry run, nothing changed."; exit 0; }
say "Checking"
"$here/selfhost-check.sh" || die "selfhost-check reported a FAIL; see above"
say "Deployed $version"
