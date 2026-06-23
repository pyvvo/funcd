#!/usr/bin/env bash
# The KV demo's FAIL-CLOSED phase (ADR-0076). Run INSIDE the Lima VM by `just lima-example-kv` (piped to
# `sudo bash -s`). The POSITIVE assertions — each counter reads 1→2 with NO read Policy (binding grants
# kv::read) — are ported to the Venom suite e2e/kv-counter.venom.yml (`FUNCD_VENOM=1 just lima-example-kv`).
# This script keeps the part Venom can't do host-side: a mid-suite control-plane mutation + redeploy,
# co-located with the daemon — remove the spec.kv binding and prove context.kv.get is DENIED, so
# binding-as-read-grant is bound-only, NOT default-allow. Pure guest bash.
set -euo pipefail
export FUNCD_SERVER=http://127.0.0.1:8080 FUNCD_TOKEN=funcd-dev-token

invoke()    { curl -sS -X POST "http://127.0.0.1:8081/function/counter" -H 'content-type: application/json' -d '{"data":{"name":"lima"}}'; }
has_count() { printf '%s' "$1" | python3 -c 'import sys,json;sys.exit(0 if "count" in json.load(sys.stdin) else 1)' 2>/dev/null; }

# precondition: with its spec.kv binding (applied at provisioning), the JS counter reads its table —
# so a later denial is the UNBINDING, not a broken function (keeps the fail-closed test self-validating).
echo "=== precondition: the bound counter reads (binding grants kv::read) ==="
b=$(invoke); has_count "$b" || { echo "RESULT: FAIL — the bound counter could not read: $b"; exit 1; }
echo "bound read OK: $b"

# NEGATIVE (ADR-0076): remove the binding → the read must be DENIED (fail-closed).
echo "=== NEGATIVE: remove the spec.kv binding → context.kv.get must be DENIED ==="
python3 -c 'import yaml;d=yaml.safe_load(open("/opt/kv-counter/counter.yaml"));d["spec"].pop("kv",None);print(yaml.safe_dump(d))' \
  | funcdcli apply -f -
phase() { funcdcli get function counter -n default -o json 2>/dev/null \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("status",{}).get("phase","?"))' 2>/dev/null; }
sleep 3                                      # let the controller observe the unbinding before polling Ready
for i in $(seq 1 30); do [ "$(phase)" = Ready ] && break; sleep 2; done
neg=""
for i in $(seq 1 5); do                      # retry across any redeploy settling; it must FAIL once settled
  neg=$(invoke || true)
  has_count "$neg" || break                  # denied (no "count") → fail-closed as expected
  sleep 2
done
if has_count "$neg"; then
  echo "RESULT: FAIL — removing spec.kv did NOT deny the read (got: $neg) — that would mean default-allow!"; exit 1
fi
echo "NEG OK — with spec.kv removed, context.kv.get is DENIED (fail-closed): $neg"

echo "=== the per-function local-API sockets + the durable KV store on disk (ADR-0066) ==="
ls -la /var/lib/funcd/data/invoke/ 2>/dev/null || true
ls /var/lib/funcd/data/kv/ 2>/dev/null | head -3 || true

echo "RESULT: PASS — fail-closed verified (bound reads; removing the binding denies) on real containerd"
echo "         (the positive 1→2 assertions live in e2e/kv-counter.venom.yml — FUNCD_VENOM=1)"
