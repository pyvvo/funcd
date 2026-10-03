#!/usr/bin/env bash
# host-check.sh: fail fast when the host's ephemeral ports are nearly used up (issue #562). A stress loop or a probe
# that opens a connection per request leaves thousands of sockets in TIME_WAIT, and every test on the host then fails
# with "connect: can't assign requested address", which reads like a code defect. gate.sh runs this first.
#
# FUNCD_GATE_MAX_TIME_WAIT (default 8000, about half of macOS's 16,384 ephemeral ports) is the limit;
# FUNCD_TIME_WAIT_COUNT replaces the measured count (tests).
set -uo pipefail
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
