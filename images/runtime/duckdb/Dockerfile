# Curated `duckdb` runtime image (ADR-0086, F48, NEW) — the DuckLake + DuckDB + Quack add-on engine,
# built on funcd's curated python314 runtime base.
#
# F48 is an ADD-ON PROVIDER: DuckDB is a native C++ engine, so embedding it in the pure-Go daemon
# would require cgo (forbidden by ADR-0065). Instead the engine runs OUT-OF-PROCESS inside this
# sandboxed, governed `duckdb` Function — reached through the ingress gateway, governed like any
# function. The engine is driven from the `duckdb` PYTHON package: that's fine — it loads the native
# library into THIS function's own process, never into (nor cgo-binds to) the daemon.
#
# Pinned versions (verified 2026-06-30, all MIT):
#   - DuckDB 1.5.4 (Variegata, 2026-06-17) — the engine (the `duckdb` Python wheel @ 1.5.4).
#   - the `ducklake` extension implementing DuckLake spec v1.0 (Apr 2026, production-ready).
#   - `httpfs` (S3/http filesystem) + the `quack` extension (beta, bundled with this DuckDB build).
# The whole engine is ONE pinned DuckDB build — Quack is not a separate artifact but the `quack`
# extension shipped with DuckDB 1.5.4.
#
# The shim entrypoint is Python (images/runtime/duckdb/shim.py): it reads the injected AWS_* keypair
# (ADR-0085) + FUNCD_DUCKLAKE_CATALOG + FUNCD_QUACK_PORT, configures + CONFINES the engine
# (http(s) filesystem disabled, s3_endpoint pinned, lock_configuration), syncs the SQLite catalog
# down from blob via boto3, ATTACHes DuckLake, starts the Quack server, and checkpoints the catalog
# back (VACUUM INTO → whole-object PutObject, data-before-metadata) per-commit + on shutdown.
#
# This image is EMBEDDED into funcd (go:embed; see internal/runtime/embedimg) and imported into the
# managed containerd at startup — NO registry pull. Build + export: just build-runtime-images.
# NOTE: the real multi-MB image is built/exercised only on the node-gated lane (FUNCD_IT=1) — never
# `just ci`. `go build` / `just ci` are green on the committed placeholder tar
# (internal/runtime/embedimg/duckdb.tar).

ARG DUCKDB_VERSION=1.5.4
ARG BOTO3_VERSION=1.40.0

# --- stage 1: pip-install the pinned duckdb + boto3 wheels + pre-install the DuckDB extensions -----
# Built on python:3.14-slim-bookworm to match the python314 curated base's interpreter + glibc
# (ADR-0054/0071 — the cc-debian12 final stage links the SAME glibc 2.36).
FROM python:3.14-slim-bookworm AS deps
ARG DUCKDB_VERSION
ARG BOTO3_VERSION
RUN set -eux; \
    pip install --no-compile --no-deps --target /opt/funcd-duckdb \
      duckdb==${DUCKDB_VERSION} boto3==${BOTO3_VERSION} botocore s3transfer jmespath \
      python-dateutil urllib3 six; \
    # Pre-install the pinned DuckDB extensions into a known dir so the shim LOADs them OFFLINE at
    # runtime (the sandbox has no arbitrary egress). The exact pin set is the DuckDB 1.5.4 bundle.
    PYTHONPATH=/opt/funcd-duckdb DUCKDB_EXTENSION_DIRECTORY=/opt/duckdb-ext \
      python3 -c "import os,duckdb; os.makedirs('/opt/duckdb-ext',exist_ok=True); \
        c=duckdb.connect(config={'extension_directory':'/opt/duckdb-ext'}); \
        [c.execute(f'INSTALL {e}') for e in ('httpfs','ducklake','quack')]"

# --- stage 2: the runtime image, on the custom distroless 3.14 base (mirrors python314/Dockerfile) -
FROM gcr.io/distroless/cc-debian12

# The Python 3.14 runtime + stdlib, exactly as the python314 curated image lays them in (ADR-0054).
COPY --from=deps /usr/local/bin/python3.14 /usr/local/bin/python3.14
COPY --from=deps /usr/local/lib/libpython3.14.so* /usr/local/lib/
COPY --from=deps /usr/local/lib/python3.14 /usr/local/lib/python3.14
# The duckdb + boto3 wheels (the engine + the S3 client) and the pre-installed DuckDB extensions.
COPY --from=deps /opt/funcd-duckdb /opt/funcd-duckdb
COPY --from=deps /opt/duckdb-ext /opt/duckdb-ext
# The funcd Python shim entrypoint.
COPY images/runtime/duckdb/shim.py /opt/funcd/shim.py

ENV LD_LIBRARY_PATH=/usr/local/lib \
    PYTHONHOME=/usr/local \
    PYTHONPATH=/opt/funcd-duckdb \
    PYTHONDONTWRITEBYTECODE=1 \
    DUCKDB_EXTENSION_DIRECTORY=/opt/duckdb-ext \
    FUNCD_QUACK_PORT=8080

# The shim binds FUNCD_QUACK_PORT in the per-function netns and serves Quack until SIGTERM.
ENTRYPOINT ["/usr/local/bin/python3.14", "/opt/funcd/shim.py"]
