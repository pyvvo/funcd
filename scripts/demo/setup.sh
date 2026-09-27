#!/usr/bin/env bash
# Hidden setup for the demo: build the binaries and boot the embedded, execution-wired
# funcd so scripts/demo/journey.sh only narrates the user commands. Inputs: docs/demo/demo.yaml.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
cfg() { yq "$1" docs/demo/demo.yaml; }

DEMO="$(cfg .demoDir)"; SERVER="$(cfg .server)"
rm -rf "$DEMO"; mkdir -p "$DEMO/bin"

go build -o "$DEMO/bin/funcdctl" ./cmd/funcdctl
go build -o "$DEMO/bin/demo-server" ./docs/demo/server

# the function artifact: hello-world's committed handler.mjs in the pinned funcd-typescript module
# (ADR-0141; that repo builds it from its TypeScript source). Resolve the module once so journey.sh
# stays offline.
scripts/moddir.sh github.com/pyvvo/funcd-typescript >"$DEMO/hello-world.dir"

lsof -ti tcp:8080 2>/dev/null | xargs kill 2>/dev/null || true
nohup "$DEMO/bin/demo-server" >"$DEMO/server.log" 2>&1 &
echo $! >"$DEMO/server.pid"
disown
for _ in $(seq 1 150); do curl -s -o /dev/null "$SERVER/" && break || sleep 0.1; done
