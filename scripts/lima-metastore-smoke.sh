#!/usr/bin/env bash
# The metastore smoke for the containerd lane (ADR-0065). Run INSIDE the Lima VM by
# `just lima-example-metastore` (piped to `sudo bash -s` over `limactl shell`). It runs the funcd daemon
# with the REAL production config — runtime.mode: containerd + storage.mode: file (the pure-Go Badger
# metastore at <dataDir>/store) — applies a Config over the control plane, RESTARTS the daemon, and reads
# the Config back: proving the Badger metastore persists control-plane state across a real daemon restart
# under containerd. Pure guest bash (no limactl, no nested quoting).
set -euo pipefail

export FUNCD_SERVER=http://127.0.0.1:8080 FUNCD_TOKEN=funcd-dev-token
FUNCD=/mnt/funcd-deps/funcd CLI=/mnt/funcd-deps/funcdcli

cat > /tmp/funcd-meta.yaml <<'YAML'
apiVersion: funcd.io/v1alpha1
kind: FuncdConfig
server: { listenAddr: 127.0.0.1:8080, dataPlaneAddr: 127.0.0.1:8081 }
storage: { mode: file, dataDir: /var/lib/funcd/data }   # durable: Badger metastore at /var/lib/funcd/data/store
auth: { token: funcd-dev-token, namespaces: [default] }
runtime: { mode: containerd }                            # the real production runtime
log: { format: text, level: info }
YAML

cat > /tmp/cfg.yaml <<'YAML'
apiVersion: funcd.io/v1alpha1
kind: Config
metadata: { name: persisted, namespace: default, resourceGroup: rg1 }
spec: { data: { hello: containerd-metastore } }
YAML

start() {
  mkdir -p /var/lib/funcd/data
  systemd-run --unit funcd-meta "$FUNCD" --config /tmp/funcd-meta.yaml
  for i in $(seq 1 30); do
    code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/healthz 2>/dev/null || true)
    { [ -n "$code" ] && [ "$code" -ge 200 ] 2>/dev/null; } && return 0
    sleep 1
  done
  echo "control plane did not come up"; journalctl -u funcd-meta --no-pager | tail; exit 1
}

phase() { "$CLI" get config persisted -n default -o json 2>/dev/null | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d["spec"]["data"]["hello"], "rv="+d["metadata"]["resourceVersion"])'; }

echo "=== boot #1 (containerd + file/Badger metastore) — apply a Config ==="
start
"$CLI" apply -f /tmp/cfg.yaml
echo "applied: $(phase)"

echo "=== restart the funcd daemon (stop → start, same data dir) ==="
systemctl stop funcd-meta
sleep 1
start

echo "=== after restart — the Config must be recovered from the durable Badger metastore ==="
got=$(phase)
echo "recovered: $got"
case "$got" in
  containerd-metastore*) echo "RESULT: PASS — durable Badger metastore persisted across a containerd-daemon restart" ;;
  *) echo "RESULT: FAIL — Config not recovered after restart"; exit 1 ;;
esac
