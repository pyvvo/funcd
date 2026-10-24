#!/usr/bin/env bash
# lane-provision.sh [boot|lane]: provision a lane VM, as root in the guest (ADR-0230). `boot` installs funcdctl and the
# packages the lane tools need. `lane` deploys the lane staged in /mnt/funcd-deps/lane.tgz (by scripts/lane.py): it
# extracts it into /opt/lane, writes the lane's tokens, starts funcd as the unit funcd-demo, then runs the lane's
# `push` and `apply` from its section of the staged scripts/lanes.yaml; with no lane.tgz it does nothing. With no
# argument it runs both: Lima passes no argument to the provision step of the lane template (scripts/lima-lane.yaml).
# `just lima-example-all` boots its shared VM from that template with no lane.tgz, then runs `lane` once per lane.
set -euo pipefail
part=${1:-all}
case $part in
  boot | lane | all) ;;
  *) echo "usage: lane-provision.sh [boot|lane]" >&2; exit 2 ;;
esac
d=/opt/lane; reg=/mnt/funcd-deps/registry

if [ "$part" != lane ]; then
  install -m 0755 /mnt/funcd-deps/funcdctl /usr/local/bin/funcdctl
  # PyYAML so the guest reads the registry directly (the base image is python3 stdlib-only); nftables for the nft
  # that scripts/lane-reset.sh and scripts/lane-leakcheck.sh run (funcd itself programs nftables through netlink).
  # The versions of the Debian snapshot scripts/lima-apt-snapshot.sh points apt at: move them together.
  apt-get update -qq && apt-get install -y -qq python3-yaml=6.0-3+b2 nftables=1.0.6-2+deb12u2
fi
[ "$part" != boot ] || exit 0
if [ ! -f /mnt/funcd-deps/lane.tgz ]; then
  echo "lane-provision: no /mnt/funcd-deps/lane.tgz, so no lane to deploy"
  exit 0
fi

mkdir -p "$d" /var/lib/funcd/data
rm -rf "$reg"
tar xzf /mnt/funcd-deps/lane.tgz -C "$d"   # staged files + lanes.yaml + a LANE marker
# ADR-0171: write the lane's `tokens` (root, 0600) before funcd reads them; funcdctl takes the developer one.
token=$(python3 - <<'PY'
import os, yaml
d = "/opt/lane"
tokens = yaml.safe_load(open(d + "/lanes.yaml"))[open(d + "/LANE").read().strip()].get("tokens", {})
for name, token in tokens.items():
    os.makedirs("/etc/funcd/tokens", mode=0o700, exist_ok=True)
    fd = os.open("/etc/funcd/tokens/" + name, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.write(fd, token.encode())
    os.close(fd)
print(tokens.get("developer", "funcd-dev-token"))
PY
)
export FUNCD_SERVER=http://127.0.0.1:8080 FUNCD_TOKEN="$token"
# The installed funcd.service's KillMode and stop timeout: a stop signals funcd alone, so it stops its workers
# before its private containerd.
systemd-run --unit funcd-demo -p KillMode=mixed -p TimeoutStopSec=45 /usr/local/bin/funcd --config "$d/funcdconfig.yaml"
curl -s --retry-connrefused --retry 30 --retry-delay 1 -o /dev/null http://127.0.0.1:8080/healthz || true
# DATA-DRIVEN deploy: read this lane's section and run its push(es) then its apply order.
python3 - <<'PY'
import json, os, subprocess, yaml
d, reg = "/opt/lane", "/mnt/funcd-deps/registry"
lane = open(d + "/LANE").read().strip()
spec = yaml.safe_load(open(d + "/lanes.yaml"))[lane]
os.chdir(d)
for p in spec.get("push", []):
    cmd = ["funcdctl", "push", p["artifact"], f"oci-layout://{reg}:{p['ref']}"]
    # The artifact's contract file gives its contract and runtime (ADR-0247).
    if p.get("entry"):
        cmd += ["--entry", p["entry"]]
    if p.get("site"):  # a prebuilt static web app dir → a site artifact (ADR-0139)
        cmd += ["--site"]
    print("+", *cmd)
    subprocess.run(cmd, check=True)
for m in spec.get("apply", []):
    # An apply entry is either a plain string (`funcdctl apply -f <file>`) OR a
    # `{file: <path>, transform: <shell-cmd>}` mapping meaning `cat <file> | <transform> |
    # funcdctl apply -f -` — the transform (a sed/python one-liner; python3-yaml is present in
    # the guest) reads the manifest on stdin and writes the mutated manifest on stdout.
    if isinstance(m, dict):
        file, transform = m["file"], m["transform"]
        print("+ cat", file, "|", transform, "| funcdctl apply -f -")
        src = subprocess.run(["cat", file], check=True, capture_output=True)
        xf = subprocess.run(transform, shell=True, check=True, input=src.stdout,
                            capture_output=True)
        subprocess.run(["funcdctl", "apply", "-f", "-"], check=True, input=xf.stdout)
    else:
        print("+ funcdctl apply -f", m)
        subprocess.run(["funcdctl", "apply", "-f", m], check=True)
PY
