#!/usr/bin/env bash
# lane-lock.sh <pid>: let one Lima lane VM run on this host at a time (issue #561). Every lane VM forwards
# 127.0.0.1:8080 and 8081, so a second VM's suite would talk to the first one.
#
# Called first by every recipe that boots a funcd VM, with the recipe shell's pid. It waits for a live holder, takes
# over the lock of a holder that has exited, refuses to start beside a running funcd VM, then records <pid> as the
# holder. The lock frees itself when that process exits, so the recipe needs no trap. A recipe run by one that holds
# the lock (lima-example-all running lima-example) passes it on through FUNCD_LANE_LOCK_HOLDER.
#
#   scripts/lane-lock.sh $$          # under set -e, on its own line: a refusal must stop the recipe
#   export FUNCD_LANE_LOCK_HOLDER="${FUNCD_LANE_LOCK_HOLDER:-$$}"
#
# FUNCD_LANE_WAIT (seconds, default 3600) bounds the wait; FUNCD_LANE_LOCK_DIR and FUNCD_LANE_LIMACTL are for tests.
set -uo pipefail
pid=${1:?usage: lane-lock.sh <pid of the recipe shell>}
dir=${FUNCD_LANE_LOCK_DIR:-$HOME/.cache/funcd-lima}/lane.lock
limactl=${FUNCD_LANE_LIMACTL:-limactl}
deadline=$((SECONDS + ${FUNCD_LANE_WAIT:-3600}))

holder() { cat "$dir/pid" 2>/dev/null; }
alive() { [ -n "$1" ] && kill -0 "$1" 2>/dev/null; }

running_vms() {
  command -v "$limactl" >/dev/null 2>&1 || return 0
  "$limactl" list --format '{{.Name}} {{.Status}}' 2>/dev/null | awk '$1 ~ /^funcd-bench/ && $2 == "Running" { print $1 }'
}

nested=0
if [ -n "${FUNCD_LANE_LOCK_HOLDER:-}" ] && [ "$(holder)" = "$FUNCD_LANE_LOCK_HOLDER" ] && alive "$FUNCD_LANE_LOCK_HOLDER"; then
  nested=1
fi

mkdir -p "$(dirname "$dir")"
said=0
while [ "$nested" = 0 ]; do
  if mkdir "$dir" 2>/dev/null; then
    echo "$pid" >"$dir/pid"
    break
  fi
  h=$(holder)
  if [ -n "$h" ] && ! alive "$h"; then
    tomb="$dir.free.$$"
    if mv "$dir" "$tomb" 2>/dev/null; then
      if [ "$(cat "$tomb/pid" 2>/dev/null)" = "$h" ]; then
        rm -rf "$tomb"
      else
        mv "$tomb" "$dir" 2>/dev/null || rm -rf "$tomb"
      fi
    fi
    continue
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "lane-lock: another lane (pid ${h:-?}) still holds $dir; gave up waiting" >&2
    exit 1
  fi
  if [ $((SECONDS - said)) -ge 60 ] || [ "$said" = 0 ]; then
    echo "lane-lock: waiting for the lane run of pid ${h:-?} to finish (one Lima lane VM per host)" >&2
    said=$SECONDS
  fi
  sleep 2
done

vms=$(running_vms)
if [ -n "$vms" ]; then
  [ "$nested" = 1 ] || rm -rf "$dir"
  echo "lane-lock: a funcd VM is already running: $vms. Its forwarded ports would collide with this lane." >&2
  echo "lane-lock: stop it first (just lima-down, or limactl delete -f <name>), then rerun." >&2
  exit 1
fi
