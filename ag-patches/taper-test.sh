#!/bin/bash
# Tests for taper.sh with a fake clock (TAPER_NOW) and a fake multica-ag that
# records its calls. Never touches the real daemon or server.
#
#   ag-patches/taper-test.sh
set -euo pipefail

TAPER=$(cd "$(dirname "$0")" && pwd)/taper.sh
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
FAKE=$WORK/fake
mkdir -p "$FAKE"

cat >"$FAKE/multica-ag" <<'EOF'
#!/bin/bash
set -euo pipefail
d=$(dirname "$0")
echo "$*" >>"$d/calls"
edit() { jq "$@" "$f" >"$f.new" && mv "$f.new" "$f"; }
case "$1 $2" in
  "daemon status") cat "$d/status.json" ;;
  "runtime list") cat "$d/runtimes.json" ;;
  "runtime concurrency") f=$d/runtimes.json edit --arg id "$3" --argjson n "$4" \
    'map(if .id == $id then .max_concurrent_tasks = $n else . end)' ;;
  "daemon drain") f=$d/status.json edit '.draining = true' ;;
  "daemon resume") f=$d/status.json edit '.draining = false | .idle = false' ;;
esac
EOF
cat >"$FAKE/osascript" <<'EOF'
#!/bin/bash
echo "$*" >>"$(dirname "$0")/notes"
EOF
chmod +x "$FAKE/multica-ag" "$FAKE/osascript"

export TAPER_CONF=$WORK/taper.conf TAPER_STATE_DIR=$WORK/state
export TAPER_MULTICA=$FAKE/multica-ag TAPER_NOTIFY=$FAKE/osascript

# reset [draining] [idle]: running daemon D1 with runtime R1 at cap 3, plus
# another daemon's runtime R9 that must never be touched.
reset() {
  rm -rf "$TAPER_STATE_DIR" "$TAPER_CONF" "$FAKE/calls" "$FAKE/notes"
  echo "{\"status\":\"running\",\"daemon_id\":\"D1\",\"draining\":${1:-false},\"idle\":${2:-false},\"active_task_count\":1,\"workspaces\":[{\"id\":\"W1\",\"runtimes\":[\"R1\"]}]}" >"$FAKE/status.json"
  echo '[{"id":"R1","daemon_id":"D1","max_concurrent_tasks":3},{"id":"R9","daemon_id":"OTHER","max_concurrent_tasks":2}]' >"$FAKE/runtimes.json"
}

# at "YYYY-MM-DD HH:MM" args...: run taper.sh at that time (2026-11-09 is a Monday).
at() { local t=$1; shift; TAPER_NOW=$t "$TAPER" "$@"; }
desired() { at "$1" --dry-run tick | sed -n 's/^desired: //p' | sed 's/ (.*//'; }
# changes: the mutating calls since the last reset, one per line.
changes() { grep -Ev '^(daemon status|runtime list)' "$FAKE/calls" 2>/dev/null || true; }

FAILS=0
check() {
  if [[ $2 == "$3" ]]; then return; fi
  FAILS=$((FAILS + 1))
  printf 'FAIL %s\n  got:  %s\n  want: %s\n' "$1" "$2" "$3"
}

# --- schedule: leave 17:00, p90 25, step 15, cap 3 -----------------------------
reset
check "before resume" "$(desired '2026-11-09 08:59')" "drain, cap 1"
check "resume" "$(desired '2026-11-09 09:00')" "claim, cap 3"
check "last full-cap minute" "$(desired '2026-11-09 16:04')" "claim, cap 3"
check "cap 2" "$(desired '2026-11-09 16:05')" "claim, cap 2"
check "cap 1" "$(desired '2026-11-09 16:20')" "claim, cap 1"
check "last claim minute" "$(desired '2026-11-09 16:34')" "claim, cap 1"
check "drain = leave - p90" "$(desired '2026-11-09 16:35')" "drain, cap 1"
check "weekend" "$(desired '2026-11-14 12:00')" "drain, cap 1"

echo 'TAPER_LEAVE=17:30' >"$TAPER_CONF"
check "config file leave" "$(desired '2026-11-09 17:04')" "claim, cap 1"
check "config file drain" "$(desired '2026-11-09 17:05')" "drain, cap 1"

# --- overrides -----------------------------------------------------------------
reset
at '2026-11-09 15:00' late 19:00 >/dev/null
check "late: full cap after normal drain" "$(desired '2026-11-09 18:04')" "claim, cap 3"
check "late: ramp" "$(desired '2026-11-09 18:05')" "claim, cap 2"
check "late: drain" "$(desired '2026-11-09 18:35')" "drain, cap 1"
check "late expires at midnight" "$(desired '2026-11-10 18:05')" "drain, cap 1"
at '2026-11-09 15:00' clear >/dev/null
check "clear" "$(desired '2026-11-09 16:35')" "drain, cap 1"

reset
at '2026-11-09 15:00' late >/dev/null
check "late, no time: no taper" "$(desired '2026-11-09 20:00')" "claim, cap 3"
check "late, no time: drain before midnight" "$(desired '2026-11-09 23:35')" "drain, cap 1"

reset true
at '2026-11-09 07:30' start >/dev/null
check "start early" "$(desired '2026-11-09 07:30')" "claim, cap 3"
check "start early keeps the taper" "$(desired '2026-11-09 16:35')" "drain, cap 1"

reset
at '2026-11-14 10:00' start >/dev/null
check "start on a weekend" "$(desired '2026-11-14 10:00')" "claim, cap 3"
check "weekend start tapers" "$(desired '2026-11-14 16:35')" "drain, cap 1"
check "weekend start expires" "$(desired '2026-11-15 10:00')" "drain, cap 1"

reset
at '2026-11-09 16:25' start >/dev/null
check "start inside the ramp: full cap" "$(desired '2026-11-09 16:25')" "claim, cap 3"
check "start inside the ramp: no taper" "$(desired '2026-11-09 18:00')" "claim, cap 3"

reset
at '2026-11-09 15:00' commute 18:15 >/dev/null
check "commute: full cap" "$(desired '2026-11-09 18:14')" "claim, cap 3"
check "commute: then drain" "$(desired '2026-11-09 18:15')" "drain, cap 1"

reset
at '2026-11-09 11:00' now >/dev/null
check "now" "$(desired '2026-11-09 11:00')" "drain, cap 1"
check "now holds all day" "$(desired '2026-11-09 15:00')" "drain, cap 1"
check "now expires" "$(desired '2026-11-10 09:00')" "claim, cap 3"
at '2026-11-09 11:30' start >/dev/null
check "start after now" "$(desired '2026-11-09 11:30')" "claim, cap 3"

reset
check "bad time" "$(at '2026-11-09 15:00' late 25:00 2>&1 >/dev/null || true)" "taper: bad time '25:00' (latest is 24:00)"
check "commute needs a time" "$(at '2026-11-09 15:00' commute 2>&1 >/dev/null || true)" "taper: usage: taper.sh commute HH:MM"

# --- tick against the fake daemon ----------------------------------------------
reset
at '2026-11-09 16:35' tick >/dev/null
check "drain + cap 1, other daemon untouched" "$(changes)" "daemon drain
runtime concurrency R1 1 --workspace-id W1"
check "drain marker" "$([[ -f $TAPER_STATE_DIR/drained-by-taper ]] && echo yes)" "yes"
: >"$FAKE/calls"
at '2026-11-09 16:40' tick >/dev/null
check "idempotent while draining" "$(changes)" ""
check "no note while busy" "$(cat "$FAKE/notes" 2>/dev/null)" ""
jq '.idle = true' "$FAKE/status.json" >"$FAKE/s" && mv "$FAKE/s" "$FAKE/status.json"
at '2026-11-09 16:50' tick >/dev/null
at '2026-11-09 16:55' tick >/dev/null
check "one idle notification" "$(wc -l <"$FAKE/notes" | tr -d ' ')" "1"
: >"$FAKE/calls"
at '2026-11-10 09:00' tick >/dev/null
check "morning resume + full cap" "$(changes)" "daemon resume
runtime concurrency R1 3 --workspace-id W1"
check "marker cleared" "$([[ -f $TAPER_STATE_DIR/drained-by-taper ]] || echo gone)" "gone"

reset
mkdir -p "$TAPER_STATE_DIR" && : >"$TAPER_STATE_DIR/drained-by-taper"
at '2026-11-09 18:00' tick >/dev/null
check "re-drain after daemon restart" "$(changes)" "daemon drain
runtime concurrency R1 1 --workspace-id W1"

reset true
at '2026-11-10 10:00' tick >/dev/null
check "manual drain is left alone" "$(changes)" ""
at '2026-11-10 10:05' start >/dev/null
check "start lifts a manual drain" "$(changes)" "daemon resume"

reset
echo '{"status":"stopped"}' >"$FAKE/status.json"
at '2026-11-09 16:35' tick >/dev/null
check "daemon stopped: nothing" "$(changes)" ""

reset
out=$(at '2026-11-09 16:35' --dry-run tick)
check "dry run changes nothing" "$(changes)$(ls "$TAPER_STATE_DIR" 2>/dev/null)" ""
check "dry run prints the plan" "$out" "would run: multica daemon drain
would create $TAPER_STATE_DIR/drained-by-taper
would run: multica runtime concurrency R1 1 --workspace-id W1
desired: drain, cap 1 (drain from 16:35)"

reset
TAPER_PROFILE=lap at '2026-11-09 16:35' tick >/dev/null
check "profile passed through" "$(head -1 "$FAKE/calls")" "--profile lap daemon status --output json"

if ((FAILS)); then echo "taper-test: $FAILS failure(s)" >&2; exit 1; fi
echo "taper-test: ok"
