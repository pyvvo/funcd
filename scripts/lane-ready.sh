#!/usr/bin/env bash
# lane-ready.sh: wait until the lane's `ready` target in scripts/lanes.yaml is Ready (ADR-0230), in the guest; the
# readiness probe of the lane template (scripts/lima-lane.yaml) and, in `just lima-example-all`, the step after a lane's
# provision. Exits 0 at once when no lane is deployed (no /opt/lane/LANE) or the lane sets no `ready`.
set -eu -o pipefail
[ -f /opt/lane/LANE ] || exit 0
export FUNCD_SERVER=http://127.0.0.1:8080
python3 - <<'PY'
import json, os, subprocess, sys, time, yaml
d = "/opt/lane"
lane = open(d + "/LANE").read().strip()
spec = yaml.safe_load(open(d + "/lanes.yaml"))[lane]
os.environ["FUNCD_TOKEN"] = spec.get("tokens", {}).get("developer", "funcd-dev-token")
r = spec.get("ready")
if not r:
    sys.exit(0)
kind, names, tries = r["kind"], r["names"], int(r.get("tries", 60))
def phase(n):
    out = subprocess.run(["funcdctl", "get", kind, n, "-n", "default", "-o", "json"],
                         capture_output=True, text=True)
    try:
        return json.loads(out.stdout).get("status", {}).get("phase", "?")
    except Exception:
        return "?"
for _ in range(tries):
    if all(phase(n) == "Ready" for n in names):
        sys.exit(0)
    time.sleep(2)
sys.exit(1)
PY
