#!/bin/bash
# Laptop taper for the AG Multica daemon: step the laptop runtime's cap down
# before Alan leaves, drain it, and say when the lid is safe to close. See
# README.md, "Laptop taper". bash 3.2 (macOS /bin/bash) compatible.
#
#   taper.sh [--dry-run] tick            apply the desired state (launchd runs this)
#   taper.sh [--dry-run] start           resume now at full cap (also lifts a manual drain)
#   taper.sh [--dry-run] late [HH:MM]    leave at HH:MM today; no time = no taper today
#   taper.sh [--dry-run] commute HH:MM   full cap until HH:MM, then cap 1 + drain
#   taper.sh [--dry-run] now             cap 1 + drain now (leaving early)
#   taper.sh [--dry-run] clear           drop today's override
#   taper.sh status                      schedule, override, daemon state, lid safety
#
# Overrides apply to today only and expire at midnight. --dry-run prints the
# commands and state writes it would make instead of making them.
set -euo pipefail

TAPER_CONF=${TAPER_CONF:-$HOME/.multica/taper.conf}
# shellcheck source=/dev/null
if [[ -f $TAPER_CONF ]]; then . "$TAPER_CONF"; fi
: "${TAPER_RESUME:=09:00}"        # claiming starts
: "${TAPER_LEAVE:=17:00}"         # idle by
: "${TAPER_P90_MIN:=25}"          # run-duration p90: last start = leave - this
: "${TAPER_STEP_MIN:=15}"         # minutes per cap step before the drain
: "${TAPER_MAX_CAP:=3}"           # full cap
: "${TAPER_WORKDAYS:=1 2 3 4 5}"  # ISO weekdays, 1 = Monday
: "${TAPER_MULTICA:=$HOME/.local/bin/multica-ag}"
: "${TAPER_PROFILE:=}"
: "${TAPER_STATE_DIR:=$HOME/.multica/taper}"
: "${TAPER_NOTIFY:=osascript}"
# TAPER_NOW="YYYY-MM-DD HH:MM" fakes the clock (tests).

OVERRIDE_FILE=$TAPER_STATE_DIR/override   # "date workday resume leave mode end", minutes
MARKER_FILE=$TAPER_STATE_DIR/drained-by-taper
NOTIFIED_FILE=$TAPER_STATE_DIR/notified
DRY_RUN=0
FORCE_RESUME=0

die() { echo "taper: $*" >&2; exit 1; }
log() { echo "$(date '+%F %T') taper: $*"; }

# --- clock -------------------------------------------------------------------

now_epoch() {
  if [[ -n ${TAPER_NOW:-} ]]; then
    date -j -f '%Y-%m-%d %H:%M' "$TAPER_NOW" +%s
  else
    date +%s
  fi
}

# Sets TODAY (YYYY-MM-DD), DOW (1-7) and NOW_MIN (minutes since midnight).
read_clock() {
  local h m
  read -r TODAY DOW h m <<<"$(date -j -r "$(now_epoch)" '+%Y-%m-%d %u %H %M')"
  NOW_MIN=$((10#$h * 60 + 10#$m))
}

# hm_to_min HH:MM -> minutes since midnight; 24:00 is allowed (end of day).
hm_to_min() {
  [[ $1 =~ ^([0-9]{1,2}):([0-5][0-9])$ ]] || die "bad time '$1' (want HH:MM)"
  local mins=$((10#${BASH_REMATCH[1]} * 60 + 10#${BASH_REMATCH[2]}))
  ((mins <= 1440)) || die "bad time '$1' (latest is 24:00)"
  echo "$mins"
}

min_to_hm() { printf '%02d:%02d' $(($1 / 60)) $(($1 % 60)); }

# --- plan and desired state --------------------------------------------------

# load_plan sets today's P_WORKDAY P_RESUME P_LEAVE P_MODE P_END and P_OVERRIDE
# (1 when an override for today is in force). P_MODE: ramp | commute | now.
load_plan() {
  P_WORKDAY=0
  case " $TAPER_WORKDAYS " in *" $DOW "*) P_WORKDAY=1 ;; esac
  P_RESUME=$(hm_to_min "$TAPER_RESUME")
  P_LEAVE=$(hm_to_min "$TAPER_LEAVE")
  P_MODE=ramp
  P_END=0
  P_OVERRIDE=0
  if [[ -f $OVERRIDE_FILE ]]; then
    local date workday resume leave mode end
    read -r date workday resume leave mode end <"$OVERRIDE_FILE" || true
    if [[ $date == "$TODAY" ]]; then
      P_WORKDAY=$workday P_RESUME=$resume P_LEAVE=$leave P_MODE=$mode P_END=$end P_OVERRIDE=1
    fi
  fi
}

drain_at() { echo $((P_LEAVE - TAPER_P90_MIN)); }

# desired_state sets D_STATE (claim | drain), D_CAP and D_WHY for NOW_MIN.
desired_state() {
  D_STATE=drain D_CAP=1
  if ((!P_WORKDAY)); then D_WHY="not a workday"; return; fi
  if ((NOW_MIN < P_RESUME)); then D_WHY="before resume at $(min_to_hm "$P_RESUME")"; return; fi
  case $P_MODE in
    now) D_WHY="taper now"; return ;;
    commute)
      if ((NOW_MIN >= P_END)); then D_WHY="commute ended at $(min_to_hm "$P_END")"; return; fi
      D_STATE=claim D_CAP=$TAPER_MAX_CAP D_WHY="commute until $(min_to_hm "$P_END")"
      return ;;
  esac
  local drain left
  drain=$(drain_at)
  if ((NOW_MIN >= drain)); then D_WHY="drain from $(min_to_hm "$drain")"; return; fi
  left=$((drain - NOW_MIN))
  D_STATE=claim
  D_CAP=$(((left + TAPER_STEP_MIN - 1) / TAPER_STEP_MIN))
  ((D_CAP <= TAPER_MAX_CAP)) || D_CAP=$TAPER_MAX_CAP
  if ((D_CAP == TAPER_MAX_CAP)); then D_WHY="schedule"; else D_WHY="ramp to drain at $(min_to_hm "$drain")"; fi
}

describe_plan() {
  if ((!P_WORKDAY)); then echo "not a workday: drained all day"; return; fi
  local s
  s="resume $(min_to_hm "$P_RESUME")"
  case $P_MODE in
    now) echo "$s; taper now: cap 1 + drain"; return ;;
    commute) echo "$s; cap $TAPER_MAX_CAP until $(min_to_hm "$P_END"), then cap 1 + drain"; return ;;
  esac
  local drain k
  drain=$(drain_at)
  s="$s at cap $TAPER_MAX_CAP"
  for ((k = TAPER_MAX_CAP - 1; k >= 1; k--)); do
    s="$s, cap $k at $(min_to_hm $((drain - k * TAPER_STEP_MIN)))"
  done
  echo "$s, drain at $(min_to_hm "$drain"), idle by $(min_to_hm "$P_LEAVE")"
}

# --- overrides ---------------------------------------------------------------

save_plan() {
  local line="$TODAY $P_WORKDAY $P_RESUME $P_LEAVE $P_MODE $P_END"
  if ((DRY_RUN)); then echo "would write $OVERRIDE_FILE: $line"; return; fi
  mkdir -p "$TAPER_STATE_DIR"
  echo "$line" >"$OVERRIDE_FILE"
}

cmd_override() {
  local kind=$1; shift
  read_clock
  load_plan
  case $kind in
    start)
      (($# == 0)) || die "usage: taper.sh start"
      P_WORKDAY=1 P_MODE=ramp FORCE_RESUME=1
      ((NOW_MIN >= P_RESUME)) || P_RESUME=$NOW_MIN
      desired_state
      # Already inside the ramp: full cap means no taper today.
      if [[ $D_STATE != claim || $D_CAP != "$TAPER_MAX_CAP" ]]; then P_LEAVE=1440; fi ;;
    late)
      (($# <= 1)) || die "usage: taper.sh late [HH:MM]"
      P_WORKDAY=1 P_MODE=ramp
      P_LEAVE=$(hm_to_min "${1:-24:00}") ;;
    commute)
      (($# == 1)) || die "usage: taper.sh commute HH:MM"
      P_WORKDAY=1 P_MODE=commute
      P_END=$(hm_to_min "$1") ;;
    now)
      (($# == 0)) || die "usage: taper.sh now"
      P_MODE=now ;;
  esac
  save_plan
  echo "Today: $(describe_plan) (until midnight)"
  tick
}

cmd_clear() {
  if ((DRY_RUN)); then echo "would remove $OVERRIDE_FILE"; else rm -f "$OVERRIDE_FILE"; fi
  echo "Override cleared."
  tick
}

# --- daemon and runtimes -----------------------------------------------------

multica() {
  if [[ -n $TAPER_PROFILE ]]; then
    "$TAPER_MULTICA" --profile "$TAPER_PROFILE" "$@"
  else
    "$TAPER_MULTICA" "$@"
  fi
}

# act runs a command that changes something; --dry-run prints it instead.
act() {
  if ((DRY_RUN)); then echo "would run: multica $*"; return; fi
  log "multica $*"
  multica "$@" >/dev/null
}

state_touch() { if ((DRY_RUN)); then echo "would create $1"; else mkdir -p "$TAPER_STATE_DIR" && : >"$1"; fi; }
state_rm() { if ((DRY_RUN)) && [[ -e $1 ]]; then echo "would remove $1"; elif ((!DRY_RUN)); then rm -f "$1"; fi; }

notify() {
  if ((DRY_RUN)); then echo "would notify: $1"; return; fi
  log "notify: $1"
  "$TAPER_NOTIFY" -e "display notification \"$1\" with title \"Multica taper\"" >/dev/null || true
}

# read_daemon sets DAEMON_JSON and DAEMON_UP (1 when running).
read_daemon() {
  DAEMON_JSON=$(multica daemon status --output json 2>/dev/null) || DAEMON_JSON='{"status":"stopped"}'
  DAEMON_UP=0
  if [[ $(jq -r '.status' <<<"$DAEMON_JSON") == running ]]; then DAEMON_UP=1; fi
}

# runtimes prints "workspace_id runtime_id cap" for this daemon's runtimes; cap
# is "none" when unset. Matched on the daemon's own id, never hardcoded.
runtimes() {
  local daemon_id ws
  daemon_id=$(jq -r '.daemon_id' <<<"$DAEMON_JSON")
  for ws in $(jq -r '.workspaces[]?.id' <<<"$DAEMON_JSON"); do
    multica runtime list --workspace-id "$ws" --output json |
      jq -r --arg d "$daemon_id" --arg ws "$ws" \
        '.[] | select(.daemon_id == $d) | "\($ws) \(.id) \(.max_concurrent_tasks // "none")"'
  done
}

# --- tick --------------------------------------------------------------------

tick() {
  read_clock
  load_plan
  desired_state
  if [[ -f $OVERRIDE_FILE && $P_OVERRIDE == 0 ]]; then state_rm "$OVERRIDE_FILE"; fi
  read_daemon
  if ((!DAEMON_UP)); then
    if ((DRY_RUN)); then echo "daemon not running: nothing to do"; fi
    return 0
  fi

  local draining idle
  draining=$(jq -r '.draining // false' <<<"$DAEMON_JSON")
  idle=$(jq -r '.idle // false' <<<"$DAEMON_JSON")
  if [[ $D_STATE == drain ]]; then
    if [[ $draining != true ]]; then
      # Also after a daemon restart: drain lives in daemon memory only.
      act daemon drain
      state_touch "$MARKER_FILE"
      state_rm "$NOTIFIED_FILE"
    elif [[ $idle == true && -f $MARKER_FILE && ! -f $NOTIFIED_FILE ]]; then
      notify "Laptop idle: safe to close the lid"
      state_touch "$NOTIFIED_FILE"
    fi
  elif [[ $draining == true ]]; then
    if [[ -f $MARKER_FILE ]] || ((FORCE_RESUME)); then
      act daemon resume
      state_rm "$MARKER_FILE"
      state_rm "$NOTIFIED_FILE"
    elif ((DRY_RUN)); then
      echo "drained by hand: leaving it (taper.sh start resumes it)"
    fi
  else
    state_rm "$MARKER_FILE"
    state_rm "$NOTIFIED_FILE"
  fi

  # Drain first, so a failing API call cannot hold it up.
  local ws id cap
  while read -r ws id cap; do
    [[ -n $id && $cap != "$D_CAP" ]] || continue
    act runtime concurrency "$id" "$D_CAP" --workspace-id "$ws"
  done < <(runtimes)
  if ((DRY_RUN)); then echo "desired: $D_STATE, cap $D_CAP ($D_WHY)"; fi
  return 0
}

# --- status ------------------------------------------------------------------

cmd_status() {
  read_clock
  load_plan
  desired_state
  read_daemon
  local override=none
  if ((P_OVERRIDE)); then override="in force until midnight"; fi
  printf '%-10s %s\n' "Now" "$TODAY $(min_to_hm "$NOW_MIN")"
  printf '%-10s %s\n' "Today" "$(describe_plan)"
  printf '%-10s %s\n' "Override" "$override"
  printf '%-10s %s\n' "Desired" "$D_STATE, cap $D_CAP ($D_WHY)"
  if ((!DAEMON_UP)); then
    printf '%-10s %s\n' "Daemon" "not running"
    printf '%-10s %s\n' "Lid" "safe to close (daemon not running)"
    return 0
  fi
  local draining idle active by=-
  draining=$(jq -r '.draining // false' <<<"$DAEMON_JSON")
  idle=$(jq -r '.idle // false' <<<"$DAEMON_JSON")
  active=$(jq -r '.active_task_count // "?"' <<<"$DAEMON_JSON")
  if [[ $draining == true ]]; then
    by=hand
    if [[ -f $MARKER_FILE ]]; then by=taper; fi
    printf '%-10s %s\n' "Daemon" "draining (by $by), $active active"
  else
    printf '%-10s %s\n' "Daemon" "claiming, $active active"
  fi
  local ws id cap
  while read -r ws id cap; do
    [[ -n $id ]] && printf '%-10s %s\n' "Runtime" "$id cap $cap"
  done < <(runtimes)
  if [[ $draining == true && $idle == true ]]; then
    printf '%-10s %s\n' "Lid" "safe to close (idle)"
  elif [[ $draining == true ]]; then
    printf '%-10s %s\n' "Lid" "not yet: $active run(s) finishing"
  else
    printf '%-10s %s\n' "Lid" "not safe: still claiming"
  fi
}

main() {
  if [[ ${1:-} == --dry-run ]]; then DRY_RUN=1; shift; fi
  local cmd=${1:-}
  if (($#)); then shift; fi
  case $cmd in
    tick) (($# == 0)) || die "usage: taper.sh tick"; tick ;;
    start | late | commute | now) cmd_override "$cmd" "$@" ;;
    clear) cmd_clear ;;
    status) cmd_status ;;
    *) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2 ;;
  esac
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
