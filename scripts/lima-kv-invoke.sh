#!/usr/bin/env bash
# The KV demo's invoke phase (ADR-0069). Run INSIDE the Lima VM by `just lima-example-kv` (piped to
# `sudo bash -s`). POSTs to BOTH kv-counter functions twice — the JS one (nodejs22) and the Python one
# (python314). Each handler increments a per-name counter via context.kv (→ worker-node local API UDS →
# PDP Facade → durable Badger KV), so each count goes 1 then 2 — proving a function uses DURABLE KV from
# BOTH runtimes on a REAL containerd sandbox. Pure guest bash.
set -euo pipefail
count() { # $1 = function name, $2 = counter name
  curl -sS -X POST "http://127.0.0.1:8081/function/$1" -H 'content-type: application/json' \
    -d "{\"data\":{\"name\":\"$2\"}}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["count"])'
}

echo "=== invoke the JS counter twice (nodejs22, context.kv) ==="
j1=$(count counter lima);   echo "JS  invoke #1 → count=$j1"
j2=$(count counter lima);   echo "JS  invoke #2 → count=$j2"
echo "=== invoke the Python counter twice (python314, context.kv) ==="
p1=$(count pycounter lima); echo "PY  invoke #1 → count=$p1"
p2=$(count pycounter lima); echo "PY  invoke #2 → count=$p2"

echo "=== the per-function local-API sockets (the context.kv/invoke channel) ==="
ls -la /var/lib/funcd/data/invoke/ 2>/dev/null || true
echo "=== the durable KV store on disk (ADR-0066) ==="
ls /var/lib/funcd/data/kv/ 2>/dev/null | head -3 || true

if [ "$j1" = 1 ] && [ "$j2" = 2 ] && [ "$p1" = 1 ] && [ "$p2" = 2 ]; then
  echo "RESULT: PASS — context.kv durable counter 1→2 from BOTH nodejs22 + python314 on real containerd"
else
  echo "RESULT: FAIL — expected 1 then 2 for each, got JS $j1/$j2, PY $p1/$p2"; exit 1
fi
