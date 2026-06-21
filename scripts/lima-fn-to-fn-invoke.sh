#!/usr/bin/env bash
# The fn-to-fn link demo's INVOKE phase (ADR-0064/0058). Run INSIDE the Lima VM by
# `just lima-example-fn-to-fn`, which pipes it to `sudo bash -s` over `limactl shell` — so this is pure
# guest bash (no limactl, no nested quoting). The VM is already deployed (the Ready probe gated boot, so
# both functions are Ready); this just drives the three scenarios and shows the broker log + invoke socket.
set -euo pipefail

echo "=== 1) happy path: front invokes greeter ==="
curl -sS -w '\nhttp %{http_code}\n' -X POST http://127.0.0.1:8081/function/front \
  -H 'content-type: application/json' -d '{"data":{"name":"lima"}}'

echo; echo "=== 2) propagation: missing name → greeter 422 propagates back through front ==="
curl -sS -w '\nhttp %{http_code}\n' -X POST http://127.0.0.1:8081/function/front \
  -H 'content-type: application/json' -d '{"data":{}}'

echo; echo "=== 3) direct greeter, bad type (name=123) → contract 422 before the handler ==="
curl -sS -w '\nhttp %{http_code}\n' -X POST http://127.0.0.1:8081/function/greeter \
  -H 'content-type: application/json' -d '{"data":{"name":123}}'

echo; echo "=== broker log (worker-node local API, ADR-0064): caller → alias → target ==="
journalctl -u funcd-demo --no-pager | grep -iE 'fn-to-fn invoke' || echo '(no broker lines)'

echo; echo "=== invoke socket — ONLY front (the linked caller) gets one (default-deny) ==="
ls -la /var/lib/funcd/data/invoke/ 2>/dev/null || true
