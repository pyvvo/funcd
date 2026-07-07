#!/usr/bin/env python3
"""Prime the local Lima download cache with the pinned Debian VM base image, so `just lima-example` lanes
boot with NO upstream dependency.

The lane VMs (scripts/lima.yaml, inherited by scripts/lima-lane.yaml) use a Debian cloud image pinned by
content digest. With the digest pin, Lima verifies the cache by digest and skips the network on repeat
boots — but a COLD cache (fresh machine, cleared cache) still needs one download, and the pinned host has
occasionally had TLS-handshake flakiness. This primes that cold cache once: it downloads the host-arch
image (from the pinned location, or a `--from` override), VERIFIES the sha512 digest, and writes it into
Lima's cache keyed by the pinned location URL. Idempotent — a cache that already matches the digest is
left untouched. After running it once, every lane boots offline.

    python3 scripts/lima-cache.py                 # from the pinned mirror in lima.yaml
    python3 scripts/lima-cache.py --from <url>     # from an alternate reachable source (verified by digest)
"""

from __future__ import annotations

import argparse
import hashlib
import os
import platform
import subprocess
import sys

import yaml

LIMA_YAML = "scripts/lima.yaml"


def lima_download_dir() -> str:
    """Lima's on-disk download cache root (OS-specific; no path is hard-coded into a tracked file)."""
    home = os.path.expanduser("~")
    if sys.platform == "darwin":
        return os.path.join(home, "Library", "Caches", "lima", "download")
    xdg = os.environ.get("XDG_CACHE_HOME") or os.path.join(home, ".cache")
    return os.path.join(xdg, "lima", "download")


def host_lima_arch() -> str:
    return "aarch64" if platform.machine().lower() in ("arm64", "aarch64") else "x86_64"


def url_key(url: str) -> str:
    return hashlib.sha256(url.encode()).hexdigest()


def file_digest(path: str, algo: str) -> str:
    h = hashlib.new(algo)
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main() -> None:
    ap = argparse.ArgumentParser(description="Prime the local Lima cache with the pinned Debian VM image.")
    ap.add_argument("--from", dest="src", default="", help="override download URL (default: the pinned location)")
    ap.add_argument("--arch", default=host_lima_arch(), help="lima arch (aarch64|x86_64); default: host arch")
    args = ap.parse_args()

    with open(LIMA_YAML) as f:
        images = yaml.safe_load(f).get("images", [])
    match = next((i for i in images if i.get("arch") == args.arch), None)
    if match is None:
        sys.exit(f"no image for arch {args.arch} in {LIMA_YAML}")
    location, digest = match["location"], match.get("digest", "")
    if ":" not in digest:
        sys.exit(f"the {args.arch} image has no digest pin in {LIMA_YAML} — nothing to verify against")
    algo, want = digest.split(":", 1)

    cache_dir = os.path.join(lima_download_dir(), "by-url-sha256", url_key(location))
    data = os.path.join(cache_dir, "data")
    if os.path.exists(data) and file_digest(data, algo) == want:
        print(f"already cached ({args.arch}, digest-verified): {data}")
        return

    url = args.src or location
    os.makedirs(cache_dir, exist_ok=True)
    tmp = data + ".download"
    print(f"downloading the {args.arch} image from {url} …")
    subprocess.run(["curl", "-fL", "--max-time", "1800", "-o", tmp, url], check=True)
    got = file_digest(tmp, algo)
    if got != want:
        os.unlink(tmp)
        sys.exit(f"digest MISMATCH ({args.arch}): got {algo}:{got}, want {digest} — refusing to seed the cache")
    os.replace(tmp, data)  # same dir ⇒ atomic, no cross-device issue
    with open(os.path.join(cache_dir, "type"), "w") as f:
        f.write("application/octet-stream")
    with open(os.path.join(cache_dir, "url"), "w") as f:
        f.write(location)  # Lima keys the cache by the LOCATION url, not the download source
    print(f"cached ({args.arch}, {os.path.getsize(data) // (1 << 20)} MiB, digest-verified) → {data}")


if __name__ == "__main__":
    main()
