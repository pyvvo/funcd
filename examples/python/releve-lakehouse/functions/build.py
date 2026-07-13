"""Hermetic bundle build for the releve-lakehouse functions (ADR-0089/0090).

Each function runs on the STOCK curated `python314` runtime; its native deps travel in a deployment-package
BUNDLE (a directory) that `funcdctl push <bundle> <ref> --entry handler.py` turns into one OCI layer. This
builds one bundle per function, vendoring the right wheel closure for each plus any `.sql` next to the
handler. Blob I/O is native (`context.blob`, ADR-0127) — no `s3util`/boto3 to copy in.

Wheels are vendored INSIDE `python:3.14-slim-bookworm` (the base the curated runtime derives its Python
from), so the native `.so` closure is glibc/arch-matched to what the function runs on (ADR-0089 §4). Needs
Docker + network (inherently e2e). Mirrors examples/python/catalog-quack/build.py.

    python functions/build.py                 # build every function's bundle → functions/<fn>/bundle/
    python functions/build.py extract verify   # build only the named functions

Then push each (image refs match resources/functions.yaml):

    funcdctl push functions/extract/bundle       registry:extract       --entry handler.py
    funcdctl push functions/verify/bundle        registry:verify        --entry handler.py
    funcdctl push functions/build_silver/bundle  registry:build-silver  --entry handler.py
    funcdctl push functions/to_gold/bundle       registry:to-gold       --entry handler.py
    funcdctl push functions/to_gold/bundle       registry:catalog-reader --entry handler.py
"""

from __future__ import annotations

import shutil
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).parent
VENDOR_IMAGE = "python:3.14-slim-bookworm"

# Per-function wheel closures + DuckDB extensions. extract/verify parse PDFs (pdfplumber) and write Parquet
# (pyarrow); build_silver conforms with DuckDB over in-memory Arrow; to_gold talks to the catalog over Quack.
FUNCTIONS: dict[str, dict] = {
    # extract/verify/build_silver reach blob NATIVELY via context.blob (ADR-0127) — no boto3, no S3 keypair.
    "extract": {"wheels": ["pdfplumber>=0.11", "pyarrow>=17"], "ext": ()},
    "verify": {"wheels": ["pdfplumber>=0.11", "pyarrow>=17"], "ext": ()},
    "build_silver": {"wheels": ["duckdb>=1.5.4", "pyarrow>=17"], "ext": ()},
    "to_gold": {"wheels": ["duckdb>=1.5.4"], "ext": ("quack", "httpfs")},  # talks to the catalog over Quack
}


def _vendor(wheels: list[str], target: Path) -> None:
    """pip install --target the wheel closure inside the hermetic base image (arch/glibc-matched)."""
    target.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        ["docker", "run", "--rm", "-v", f"{target}:/out", VENDOR_IMAGE,
         "bash", "-c", "pip install --no-compile --target /out " + " ".join(f"'{w}'" for w in wheels)],
        check=True,
    )


def build(name: str) -> None:
    spec = FUNCTIONS[name]
    fn_dir = HERE / name
    bundle = fn_dir / "bundle"
    if bundle.exists():
        shutil.rmtree(bundle)
    bundle.mkdir(parents=True)

    shutil.copy2(fn_dir / "handler.py", bundle / "handler.py")   # the --entry
    for sql in fn_dir.glob("*.sql"):
        shutil.copy2(sql, bundle / sql.name)

    _vendor(spec["wheels"], bundle)
    # DuckDB extensions (quack/httpfs) are pre-installed offline into bundle/duckdb-ext/ — see
    # examples/python/catalog-quack/build.py for the extension-vendoring detail (elided here).
    if spec["ext"]:
        (bundle / "duckdb-ext").mkdir(exist_ok=True)
    print(f"  built {name} → {bundle.relative_to(HERE.parent)}  (wheels: {', '.join(spec['wheels'])})")


def main(argv: list[str]) -> int:
    names = argv or list(FUNCTIONS)
    for n in names:
        if n not in FUNCTIONS:
            print(f"unknown function {n!r}; known: {', '.join(FUNCTIONS)}", file=sys.stderr)
            return 2
    for n in names:
        build(n)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
