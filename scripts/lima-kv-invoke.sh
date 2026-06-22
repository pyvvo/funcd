#!/usr/bin/env bash
# The KV demo's invoke phase (ADR-0069). Run INSIDE the Lima VM by `just lima-example-kv` (piped to
# `sudo bash -s`). POSTs to counter twice; the handler increments a per-name counter via context.kv (→
# worker-node local API UDS → PDP Facade → durable Badger KV), so the count goes 1 then 2 — proving a
# function uses DURABLE KV on a REAL containerd sandbox. Pure guest bash.
set -euo pipefail
count() { curl -sS -X POST http://127.0.0.1:8081/function/counter -H 'content-type: application/json' \
  -d '{"data":{"name":"lima"}}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["count"])'; }

echo "=== invoke counter twice (context.kv increments a durable counter) ==="
c1=$(count); echo "invoke #1 → count=$c1"
c2=$(count); echo "invoke #2 → count=$c2"
echo "=== the per-function local-API socket (the context.kv/invoke channel) ==="
ls -la /var/lib/funcd/data/invoke/ 2>/dev/null || true
echo "=== the durable KV store on disk (ADR-0066) ==="
ls /var/lib/funcd/data/kv/ 2>/dev/null | head -3 || true
if [ "$c1" = 1 ] && [ "$c2" = 2 ]; then
  echo "RESULT: PASS — context.kv durable counter 1→2 on real containerd"
else
  echo "RESULT: FAIL — expected 1 then 2, got $c1 then $c2"; exit 1
fi
