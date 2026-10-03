#!/usr/bin/env bash
# lane-lock.sh <pid>: let one Lima lane VM run on this host at a time (issue #561). Every lane VM forwards
# 127.0.0.1:8080 and 8081, so a second VM's suite would talk to the first one.
#
# Every recipe that boots a funcd VM calls it first, with its shell's pid. The lock is a symlink whose target names
# the holder by pid and start time (read in the C locale and UTC, so callers in other locales or time zones agree),
# so a reused pid never passes for it; creating the symlink is atomic. A waiter waits for a live holder and takes over
# the lock of one that has exited. Every removal, a takeover or the release by the detached watcher that follows the
# holder, runs under a second lock and re-reads the holder first, so a live holder's lock is never removed; a removal
# that finds the lock gone, or waits two minutes for that second lock, gives up. The holder then refuses to start
# beside a running funcd VM. A recipe run by one that holds the lock (lima-example-all running lima-example) passes it
# on through FUNCD_LANE_LOCK_HOLDER.
#
#   scripts/lane-lock.sh $$          # under set -e, on its own line: a refusal must stop the recipe
#   export FUNCD_LANE_LOCK_HOLDER="${FUNCD_LANE_LOCK_HOLDER:-$$}"
#
# FUNCD_LANE_WAIT (seconds, default 3600) bounds the wait. FUNCD_LANE_LOCK_DIR, FUNCD_LANE_LIMACTL, FUNCD_LANE_POLL
# (seconds between tries, default 1) and FUNCD_LANE_GUARD_WAIT (seconds, default 120) are for tests.
set -uo pipefail
pid=${1:?usage: lane-lock.sh <pid of the recipe shell>}
base=${FUNCD_LANE_LOCK_DIR:-$HOME/.cache/funcd-lima}
lock=$base/lane.holder
guard=$base/lane.holder.guard
limactl=${FUNCD_LANE_LIMACTL:-limactl}
poll=${FUNCD_LANE_POLL:-1}
guard_wait=${FUNCD_LANE_GUARD_WAIT:-120}
deadline=$((SECONDS + ${FUNCD_LANE_WAIT:-3600}))

ident() { # <pid>: "<pid> <start time>", or nothing when no such process runs
  local s
  s=$(LC_ALL=C TZ=UTC0 ps -o lstart= -p "$1" 2>/dev/null | tr -s ' ')
  [ -n "$s" ] && echo "$1 ${s# }"
}
holder() { readlink "$lock" 2>/dev/null; }
live() { [ -n "$1" ] && [ "$(ident "${1%% *}")" = "$1" ]; }

# remove_if <identity>: remove the lock when it still names <identity>, under the guard. A guard older than a minute
# was left by a process that died holding it. Returns 1 after two minutes without the guard.
remove_if() {
  local give_up=$((SECONDS + guard_wait))
  until mkdir "$guard" 2>/dev/null; do
    [ "$(holder)" = "$1" ] || return 0
    [ "$SECONDS" -lt "$give_up" ] || return 1
    find "$guard" -maxdepth 0 -mmin +1 -exec rmdir {} \; 2>/dev/null
    sleep "$poll"
  done
  [ "$(holder)" = "$1" ] && rm -f "$lock"
  rmdir "$guard" 2>/dev/null
  return 0
}

running_vms() {
  command -v "$limactl" >/dev/null 2>&1 || return 0
  "$limactl" list --format '{{.Name}} {{.Status}}' 2>/dev/null | awk '$1 ~ /^funcd-bench/ && $2 == "Running" { print $1 }'
}

me=$(ident "$pid") || { echo "lane-lock: no process $pid to hold the lock" >&2; exit 2; }
nested=0
h=$(holder)
if [ -n "${FUNCD_LANE_LOCK_HOLDER:-}" ] && [ "${h%% *}" = "$FUNCD_LANE_LOCK_HOLDER" ] && live "$h"; then
  nested=1
fi

said=0
while [ "$nested" = 0 ]; do
  mkdir -p "$base" || exit 1 # on every try: the lock dir can vanish during a takeover
  if [ -d "$lock" ] && [ ! -L "$lock" ]; then
    echo "lane-lock: $lock is a directory, not a lock; remove it and rerun" >&2
    exit 1
  fi
  if ln -s "$me" "$lock" 2>/dev/null && [ "$(holder)" = "$me" ]; then
    (
      trap '' HUP
      while kill -0 "$pid" 2>/dev/null; do sleep "$poll"; done
      remove_if "$me"
    ) </dev/null >/dev/null 2>&1 &
    break
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "lane-lock: the lane run of pid $(holder | cut -d' ' -f1) still holds $lock; gave up waiting" >&2
    exit 1
  fi
  h=$(holder)
  if [ -n "$h" ] && ! live "$h"; then
    if ! remove_if "$h"; then
      if [ -d "$guard" ]; then
        echo "lane-lock: $guard has been held for two minutes; remove it if no lane is starting" >&2
      else
        echo "lane-lock: could not take the guard $guard for two minutes" >&2
      fi
      exit 1
    fi
    continue
  fi
  if [ "$said" = 0 ] || [ $((SECONDS - said)) -ge 60 ]; then
    echo "lane-lock: waiting for the lane run of pid ${h%% *} to finish (one Lima lane VM per host)" >&2
    said=$SECONDS
  fi
  sleep "$poll"
done

vms=$(running_vms)
if [ -n "$vms" ]; then
  [ "$nested" = 1 ] || remove_if "$me"
  echo "lane-lock: a funcd VM is already running: $vms. Its forwarded ports would collide with this lane." >&2
  echo "lane-lock: stop it first (just lima-down, or limactl delete -f $vms), then rerun." >&2
  exit 1
fi
