#!/usr/bin/env python3
"""Generic HOST-side driver for a funcd example lane (the `scripts/lanes.yaml` registry, ADR-0077).

`scripts/lane.py <name> <deps-dir>` reads the lane's section, runs its `build` commands (from the repo
root), stages its `stage` files + the registry + a `LANE` marker into `<deps>/lane.tgz` (exactly what the
generic VM `scripts/lima-lane.yaml` extracts + interprets in-guest), and prints the ABSOLUTE path of the
lane's Venom suite (for the recipe to run once the VM is up). No per-lane logic lives here — a new lane is
just a section in lanes.yaml. Its guest-side twin is the python block inside `scripts/lima-lane.yaml`.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
import tarfile
import tempfile

import yaml

REGISTRY = "scripts/lanes.yaml"


def main() -> None:
    if len(sys.argv) != 3:
        sys.exit("usage: lane.py <lane-name> <deps-dir>")
    name, deps = sys.argv[1], sys.argv[2]

    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))  # repo root (scripts/..)
    os.chdir(root)
    lanes = yaml.safe_load(open(REGISTRY, encoding="utf-8"))
    if name not in lanes:
        sys.exit(f"lane {name!r} is not in {REGISTRY} (have: {', '.join(sorted(lanes))})")
    spec = lanes[name]
    example_dir = spec["dir"]

    # build (host): run each command from the repo root, in order. The child's stdout is redirected to
    # STDERR so ONLY the final suite path lands on stdout (the recipe captures stdout for the suite).
    for cmd in spec.get("build", []):
        print(f"[lane {name}] build: {cmd}", file=sys.stderr)
        subprocess.run(cmd, shell=True, check=True, stdout=sys.stderr)

    # stage the lane's files + the registry + the LANE marker into <deps>/lane.tgz.
    os.makedirs(deps, exist_ok=True)
    stage = tempfile.mkdtemp(prefix="funcd-lane-")
    try:
        for f in spec["stage"]:
            src = os.path.join(example_dir, f)
            dst = os.path.join(stage, f)
            if os.path.isdir(src):
                shutil.copytree(src, dst)
            else:
                shutil.copy2(src, dst)
        shutil.copy2(REGISTRY, os.path.join(stage, "lanes.yaml"))
        with open(os.path.join(stage, "LANE"), "w", encoding="utf-8") as fh:
            fh.write(name)
        tgz = os.path.join(deps, "lane.tgz")
        with tarfile.open(tgz, "w:gz") as tar:
            for entry in sorted(os.listdir(stage)):
                tar.add(os.path.join(stage, entry), arcname=entry)
        print(f"[lane {name}] staged {tgz}", file=sys.stderr)
    finally:
        shutil.rmtree(stage, ignore_errors=True)

    # the recipe reads stdout for the (absolute) Venom suite to run.
    print(os.path.join(root, spec["venom"]))


if __name__ == "__main__":
    main()
