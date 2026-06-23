#!/usr/bin/env bash
# Unbind the JS kv-counter, IN the VM (ADR-0076 fail-closed). Run co-located with the daemon via
# `limactl shell … -- sudo bash -s < this` — invoked from the Venom suite's exec step (the HYBRID:
# mutation in-VM next to the control plane, assertion host-side). Removing spec.kv drops the read
# capability; the function reconciles back to Ready WITHOUT the binding, so the next read is DENIED.
set -eu
export FUNCD_SERVER=http://127.0.0.1:8080 FUNCD_TOKEN=funcd-dev-token
python3 -c 'import yaml;d=yaml.safe_load(open("/opt/kv-counter/counter.yaml"));d["spec"].pop("kv",None);print(yaml.safe_dump(d))' \
  | funcdcli apply -f -
phase() { funcdcli get function counter -n default -o json 2>/dev/null \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("status",{}).get("phase","?"))' 2>/dev/null; }
sleep 3                                      # let the controller observe the unbinding before polling Ready
for i in $(seq 1 30); do [ "$(phase)" = Ready ] && break; sleep 2; done
echo "stripped spec.kv; counter phase=$(phase)"
