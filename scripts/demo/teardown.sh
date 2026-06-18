#!/usr/bin/env bash
# Stop the demo server and remove the scratch dir. Inputs: docs/demo/demo.yaml.
cd "$(git rev-parse --show-toplevel)" 2>/dev/null || true
DEMO="$(yq '.demoDir' docs/demo/demo.yaml 2>/dev/null || echo /tmp/funcd-demo)"
[ -f "$DEMO/server.pid" ] && kill "$(cat "$DEMO/server.pid")" 2>/dev/null || true
lsof -ti tcp:8080 2>/dev/null | xargs kill 2>/dev/null || true
rm -rf "$DEMO"
