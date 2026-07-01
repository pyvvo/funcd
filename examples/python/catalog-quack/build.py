"""Hermetic bundle build for the catalog-quack consumer (ADR-0089/0090).

Emits a deployment-package BUNDLE (a directory) that `funcdctl push ./bundle <ref> --entry handler.py`
turns into one tar+gzip OCI layer, so the F48 Quack consumer runs UNCHANGED on the stock curated
`python314` runtime — no `python-duckdb` image. The bundle carries:

  - ``handler.py``              — the runtime artifact (handler + baked ``__funcd_validate_*``, ADR-0058)
  - ``duckdb/`` + deps          — the vendored ``duckdb`` wheel closure (``pip install --target``)
  - ``duckdb-ext/``             — the pre-installed ``quack`` + ``httpfs`` extensions (offline)
  - ``__funcd_contract.json``   — the ADR-0090 ``{input, output}`` contract (both keys; void = null)

The vendoring runs INSIDE ``python:3.14-slim-bookworm`` (``docker run``) — the same base the curated
runtime derives its Python from — so the native ``.so`` closure is byte-for-byte glibc/arch-matched to
the runtime (ADR-0089 §4, corrected: the curated image itself is distroless with no pip/shell). This
build is inherently e2e: it needs Docker + network. Run from this dir (the ``build`` dep-group has pydantic):

    uv run --group build python build.py                 # hermetic (default): vendors in slim-bookworm
    uv run --group build python build.py --no-hermetic   # host-pip fast path (unsafe for release, ADR-0089)
"""

from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import sys
from pathlib import Path

from funcd_shim.build import build

HERE = Path(__file__).parent
BUNDLE = HERE / "bundle"
# The hermetic vendor image (ADR-0089 §4, corrected): vendor the wheel closure in
# python:3.14-slim-bookworm — the SAME base the curated runtime derives its Python from
# (images/runtime/python314/Dockerfile `FROM python:3.14-slim-bookworm AS py`), so the native
# .so closure is byte-for-byte glibc/arch-matched to what the function runs on. We do NOT vendor in
# the curated image itself: it is custom distroless (gcr.io/distroless/cc-debian12) with no pip/shell
# (ADR-0049 stdlib-only), so `pip install` cannot run there.
VENDOR_IMAGE = "python:3.14-slim-bookworm"
DUCKDB_SPEC = "duckdb>=1.5.4"
EXTENSIONS = ("quack", "httpfs")


def _reset_bundle() -> None:
    if BUNDLE.exists():
        shutil.rmtree(BUNDLE)
    BUNDLE.mkdir(parents=True)


def _bake_handler() -> None:
    """Compile src/handler.py → the runtime artifact with baked validators + emit the contract."""
    result = build((HERE / "src" / "handler.py").read_text())
    (BUNDLE / "handler.py").write_text(result.runtime_source)
    # ADR-0090: both sides are always present — a void side is {"type": "null"}, never omitted.
    contract = {"input": result.input_schema, "output": result.output_schema}
    (BUNDLE / "__funcd_contract.json").write_text(json.dumps(contract, indent=2) + "\n")


def _vendor_hermetic() -> None:
    """pip-install the duckdb wheel closure + pre-install the extensions in the glibc-matched vendor
    image, so the vendored native closure matches the runtime's glibc/arch exactly (ADR-0089 §4)."""
    ext_installs = " && ".join(
        f"python -c \"import duckdb; con=duckdb.connect(); "
        f"con.execute('SET extension_directory=\\'/out/duckdb-ext\\''); "
        f"con.execute('INSTALL {ext}')\""
        for ext in EXTENSIONS
    )
    script = (
        f"set -e; "
        f"pip install --no-cache-dir --target /out '{DUCKDB_SPEC}'; "
        f"mkdir -p /out/duckdb-ext; "
        # export so PYTHONPATH reaches EVERY chained `python -c` (not just the first before the `&&`).
        f"export PYTHONPATH=/out; {ext_installs}"
    )
    subprocess.run(  # noqa: S603 - fixed argv, no shell-injection surface
        [
            "docker", "run", "--rm",
            "-v", f"{BUNDLE}:/out",
            "--entrypoint", "sh",
            VENDOR_IMAGE, "-c", script,
        ],
        check=True,
    )


def _vendor_host() -> None:
    """Host-pip fast path (ADR-0089 temporary workaround) — unsafe for release; iteration only."""
    subprocess.run(  # noqa: S603
        [sys.executable, "-m", "pip", "install", "--no-cache-dir", "--target", str(BUNDLE), DUCKDB_SPEC],
        check=True,
    )
    print("WARNING: --no-hermetic vendored via host pip — NOT release-safe (glibc/arch may not match).")


def main() -> None:
    parser = argparse.ArgumentParser(description="Build the catalog-quack deployment bundle (ADR-0089).")
    parser.add_argument("--no-hermetic", dest="hermetic", action="store_false",
                        help="vendor via host pip instead of inside the curated image (unsafe for release)")
    args = parser.parse_args()

    _reset_bundle()
    _bake_handler()
    if args.hermetic:
        _vendor_hermetic()
    else:
        _vendor_host()
    exts = "+".join(EXTENSIONS)
    print(f"built bundle/ (handler + vendored duckdb + duckdb-ext/{exts} + __funcd_contract.json)")
    print(f"push it:  funcdctl push {BUNDLE.relative_to(HERE.parent.parent.parent)} <ref> --entry handler.py")


if __name__ == "__main__":
    main()
