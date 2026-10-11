#!/usr/bin/env bash
# host-check.sh: fail fast when the host's ephemeral ports are nearly used up (issue #562). A stress loop or a probe
# that opens a connection per request leaves thousands of sockets in TIME_WAIT, and every test on the host then fails
# with "connect: can't assign requested address", which reads like a code defect. gate.sh runs this first.
#
# FUNCD_GATE_MAX_TIME_WAIT (default 8000, about half of macOS's 16,384 ephemeral ports) is the limit;
# FUNCD_TIME_WAIT_COUNT replaces the measured count (tests).
#
# It first kills test binaries left without a parent: a go test killed from outside (a tool's timeout) leaves its
# test binary running, adopted by launchd or init (parent 1), and one that then hangs keeps a core busy for hours. A
# process of the caller whose command ends in FUNCD_ORPHAN_TEST_SUFFIX (default .test, tests narrow it) with parent 1
# that has run FUNCD_GATE_ORPHAN_TEST_MINUTES (default 30) or more gets SIGKILL, since a hung binary may ignore SIGTERM.
set -uo pipefail
orphan_minutes=${FUNCD_GATE_ORPHAN_TEST_MINUTES:-30}
orphan_suffix=${FUNCD_ORPHAN_TEST_SUFFIX:-.test}
uid=$(id -u)
etime='^(([0-9]+)-)?(([0-9]+):)?([0-9]+):[0-9]+$' # [[dd-]hh:]mm:ss
while read -r puid pid ppid age cmd _; do
  [ "$puid" = "$uid" ] && [ "$ppid" = 1 ] && [[ $cmd == *"$orphan_suffix" && $age =~ $etime ]] || continue
  minutes=$((10#${BASH_REMATCH[2]:-0} * 1440 + 10#${BASH_REMATCH[4]:-0} * 60 + 10#${BASH_REMATCH[5]}))
  if [ "$minutes" -ge "$orphan_minutes" ] && kill -9 "$pid" 2>/dev/null; then
    echo "host: killed orphaned test binary $pid (${cmd##*/}, no parent, running $age)"
  fi
done < <(ps -A -ww -o uid=,pid=,ppid=,etime=,args= 2>/dev/null)
limit=${FUNCD_GATE_MAX_TIME_WAIT:-8000}
if [ -n "${FUNCD_TIME_WAIT_COUNT:-}" ]; then
  n=$FUNCD_TIME_WAIT_COUNT
elif command -v ss >/dev/null 2>&1; then
  n=$(ss -Htan state time-wait 2>/dev/null | wc -l | tr -d ' ')
else
  n=$(netstat -an 2>/dev/null | grep -c TIME_WAIT)
fi
if [ "$n" -gt "$limit" ]; then
  echo "host: $n sockets in TIME_WAIT (limit $limit): the ephemeral port range is nearly used up, so tests would fail" \
    "to connect. Find the source (macOS: netstat -anv | grep TIME_WAIT shows the process; Linux, where these sockets" \
    "have no owner: ss -Htan state time-wait | awk '{print \$4}' | sort | uniq -c | sort -rn | head shows the peers)," \
    "stop the stress loop or probe, and wait for the sockets to expire before gating." >&2
  exit 1
fi
echo "host: $n sockets in TIME_WAIT (limit $limit)"
