#!/usr/bin/env bash
# The SHOWN part of the demo: drive the real funcdctl + HTTP, narrated. Assumes
# scripts/demo/setup.sh already built the binaries and booted the server. Inputs:
# docs/demo/demo.yaml; the deployed CRD: docs/demo/function.yaml. Re-record: just demo-record.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
cfg() { yq "$1" docs/demo/demo.yaml; }

DEMO="$(cfg .demoDir)"; SERVER="$(cfg .server)"; DATA="$(cfg .dataPlane)"
TOKEN="$(cfg .token)"; FN="$(cfg .function)"
LAYOUT_REF="$(yq '.spec.artifact.uri' docs/demo/function.yaml)" # the CRD is the single source
CLI="$DEMO/bin/funcdctl"
HELLO="$(cat "$DEMO/hello-world.dir")/examples/hello-world"
fc() { "$CLI" --server "$SERVER" --token "$TOKEN" "$@"; }
say() { printf '\n\033[1;36m❯ %s\033[0m\n' "$*"; }

printf '\033[1;35mfuncd — deploy a function from a source artifact and invoke it, all via the CLI\033[0m\n'

say "cat src/handler.ts                            # the function, typed against @funcd-dev/shim"
cat "$HELLO/src/handler.ts"
sleep 1.4

say "funcdctl push handler.mjs oci-layout://…:v1     # bundle (esbuild) → push the OCI artifact"
fc push "$HELLO/handler.mjs" "$LAYOUT_REF"
sleep 1.4

say "funcdctl apply -f function.yaml                 # deploy — no digest; the platform pins it"
yq -o=json docs/demo/function.yaml | fc apply -f -
sleep 1.2

say "funcdctl get function $FN -o json              # reconcile to Ready"
until fc get function "$FN" -n default -o json 2>/dev/null | grep -q '"phase": "Ready"'; do sleep 0.2; done
fc get function "$FN" -n default -o json | grep -E '"name"|"phase"|"currentRevision"'
sleep 1.4

say "curl -XPOST $DATA/function/$FN -d '{…,\"data\":{\"hello\":\"funcd\"}}'   # invoke; data is contract-checked"
curl -s -XPOST "$DATA/function/$FN" -H 'content-type: application/json' \
  -d '{"specversion":"1.0","id":"demo-1","source":"funcd/demo","type":"com.funcd.demo.hello","data":{"hello":"funcd"}}'
printf '\n'
sleep 1.4

say "curl … -d '{…,\"data\":{\"hello\":42}}'   # wrong-shaped data → 422, rejected before the handler"
curl -s -XPOST "$DATA/function/$FN" -H 'content-type: application/json' \
  -d '{"specversion":"1.0","id":"demo-2","source":"funcd/demo","type":"com.funcd.demo.hello","data":{"hello":42}}'
printf '\n'
sleep 1.6

printf '\n\033[1;32m✓ pushed an OCI artifact · deployed via the API · invoked + contract-checked over HTTP — all through the CLI\033[0m\n'
sleep 2
