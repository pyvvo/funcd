#!/usr/bin/env python3
"""Generic HOST-side driver for a funcd example lane (the `scripts/lanes.yaml` registry, ADR-0077).

`scripts/lane.py <name> <deps-dir>` reads the lane's section, runs its `build` commands (in the lane's
dir), stages its `stage` files + the registry + a `LANE` marker into `<deps>/lane.tgz` (exactly what the
generic VM `scripts/lima-lane.yaml` extracts + interprets in-guest), and prints the ABSOLUTE path of the
lane's Venom suite (for the recipe to run once the VM is up). No per-lane logic lives here — a new lane is
just a section in lanes.yaml. Its guest-side twin is the python block inside `scripts/lima-lane.yaml`.

A lane's `module` names the pinned Go module its `dir` lives in (ADR-0141): the funcd-typescript or
funcd-python example, resolved through `scripts/moddir.sh` (the module cache, or a go.work override). The
module cache is read-only, so `copy: true` builds in a writable copy of the module under `.modcopy/lane/`.
"""

from __future__ import annotations

import os
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile

import yaml

REGISTRY = "scripts/lanes.yaml"
MODCOPY = ".modcopy"


def module_root(module: str | None, roots: dict[str, str]) -> str:
    """The root a lane path resolves against: the funcd repo root, or a pinned module's root."""
    if not module:
        return "."
    if module not in roots:
        out = subprocess.run(["scripts/moddir.sh", module], check=True, capture_output=True, text=True)
        roots[module] = out.stdout.strip()
    return roots[module]


def writable_copy(src: str, dst: str) -> None:
    """Copy a file or dir, possibly out of the read-only module cache, leaving the copy owner-writable."""
    if os.path.isdir(src):
        shutil.copytree(src, dst)
    else:
        shutil.copy2(src, dst)
    for path in [dst] + [os.path.join(d, n) for d, ds, fs in os.walk(dst) for n in ds + fs]:
        os.chmod(path, os.stat(path).st_mode | stat.S_IWUSR)


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
    roots: dict[str, str] = {}
    module = spec.get("module")
    example_dir = os.path.join(module_root(module, roots), spec["dir"])
    if spec.get("copy"):
        # A module lane copies its WHOLE module, so the example's relative references (a uv path dep on
        # ../../shim) still resolve. Lane copies live apart from the dev copies scripts/example-copy.sh
        # keeps, so a lane refresh never wipes `funcdctl dev` state.
        src = module_root(module, roots) if module else example_dir
        copy = os.path.join(MODCOPY, "lane", os.path.basename(module) if module else spec["dir"])
        shutil.rmtree(copy, ignore_errors=True)
        os.makedirs(os.path.dirname(copy), exist_ok=True)
        writable_copy(src, copy)
        example_dir = os.path.join(copy, spec["dir"]) if module else copy

    # build (host): run each command in the lane's dir, in order. The child's stdout is redirected to
    # STDERR so ONLY the final suite path lands on stdout (the recipe captures stdout for the suite).
    for cmd in spec.get("build", []):
        print(f"[lane {name}] build: {cmd}", file=sys.stderr)
        subprocess.run(cmd, shell=True, check=True, stdout=sys.stderr, cwd=example_dir)

    # stage the lane's files + the registry + the LANE marker into <deps>/lane.tgz.
    os.makedirs(deps, exist_ok=True)
    stage = tempfile.mkdtemp(prefix="funcd-lane-")
    try:
        for f in spec["stage"]:
            # A stage entry is either a plain string (relative to the lane's `dir`, copied under the
            # same basename) OR a `{from: <path>, module: <optional>, to: <tgz-relative>}` mapping — the
            # mapping form pulls a file from ANY dir of funcd (or of `module`) into a chosen (possibly
            # nested, e.g. `py/…`) tgz path.
            if isinstance(f, dict):
                src = os.path.join(module_root(f.get("module"), roots), f["from"])
                dst = os.path.join(stage, f["to"])
            else:
                src = os.path.join(example_dir, f)
                dst = os.path.join(stage, f)
            os.makedirs(os.path.dirname(dst), exist_ok=True)
            writable_copy(src, dst)
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
