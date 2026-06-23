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

# NEGATIVE (ADR-0076): the spec.kv binding IS the read capability — removing it must DENY context.kv.get
# (fail-closed). This proves binding-as-read-grant is bound-only, NOT default-allow, on a real sandbox.
echo "=== NEGATIVE: remove the JS counter's spec.kv binding → context.kv.get must be DENIED ==="
export FUNCD_SERVER=http://127.0.0.1:8080 FUNCD_TOKEN=funcd-dev-token
python3 -c 'import yaml;d=yaml.safe_load(open("/opt/kv-counter/counter.yaml"));d["spec"].pop("kv",None);print(yaml.safe_dump(d))' \
  | funcdcli apply -f -
phase() { funcdcli get function counter -n default -o json 2>/dev/null \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("status",{}).get("phase","?"))' 2>/dev/null; }
sleep 3                                      # let the controller observe the unbinding before polling Ready
for i in $(seq 1 30); do [ "$(phase)" = Ready ] && break; sleep 2; done
has_count() { printf '%s' "$1" | python3 -c 'import sys,json;sys.exit(0 if "count" in json.load(sys.stdin) else 1)' 2>/dev/null; }
neg=""
for i in $(seq 1 5); do                      # retry across any redeploy settling; it must FAIL once settled
  neg=$(curl -sS -X POST "http://127.0.0.1:8081/function/counter" -H 'content-type: application/json' \
        -d '{"data":{"name":"lima"}}' || true)
  has_count "$neg" || break                  # denied (no "count") → fail-closed as expected
  sleep 2
done
if has_count "$neg"; then
  echo "RESULT: FAIL — removing spec.kv did NOT deny the read (got: $neg) — that would mean default-allow!"; exit 1
fi
echo "NEG OK — with spec.kv removed, context.kv.get is DENIED (fail-closed): $neg"

if [ "$j1" = 1 ] && [ "$j2" = 2 ] && [ "$p1" = 1 ] && [ "$p2" = 2 ]; then
  echo "RESULT: PASS — context.kv 1→2 from nodejs22 + python314 with NO read Policy (binding grants read),"
  echo "               AND removing the binding denies the read (fail-closed) — on real containerd"
else
  echo "RESULT: FAIL — expected 1 then 2 for each, got JS $j1/$j2, PY $p1/$p2"; exit 1
fi
