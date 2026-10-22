# Curated `duckdb` runtime image (ADR-0086, F48, NEW) — the DuckLake + DuckDB + Quack add-on engine,
# built on funcd's curated python314 runtime base.
#
# F48 is an ADD-ON PROVIDER: DuckDB is a native C++ engine, so embedding it in the pure-Go daemon
# would require cgo (forbidden by ADR-0065). Instead the engine runs OUT-OF-PROCESS inside this
# sandboxed, governed `duckdb` Function — reached through the ingress gateway, governed like any
# function. The engine is driven from the `duckdb` PYTHON package: that's fine — it loads the native
# library into THIS function's own process, never into (nor cgo-binds to) the daemon.
#
# Pinned versions (all MIT; ADR-0263):
#   - DuckDB 2.0.0, the long-term-support release — the engine (the `duckdb` Python wheel @ 2.0.0).
#   - the `ducklake` extension; the shim creates new catalogs at DuckLake 1.1.
#   - `httpfs` (S3/http filesystem) + the `quack` extension, bundled with this DuckDB build.
# The whole engine is ONE pinned DuckDB build — Quack is not a separate artifact but the `quack`
# extension shipped with DuckDB 2.0.0. Every input is pinned: both bases by the digest of their multi-arch
# index, every pip package by version and wheel hash (requirements.txt), and every extension by the SHA-256
# of its file (duckdb.sha256); DuckDB's resolved 2026-10-23, the rest 2026-10-11. python:3.14-slim-bookworm
# stays at the digest images/runtime/python314/Dockerfile and scripts/lanes.yaml use.
#
# The shim entrypoint is Python (images/runtime/duckdb/shim.py): it reads the injected AWS_* keypair
# (ADR-0085) + FUNCD_DUCKLAKE_CATALOG + FUNCD_QUACK_PORT, configures the engine (s3_endpoint pinned,
# http(s) filesystem disabled), syncs the SQLite catalog down from blob via boto3, ATTACHes DuckLake,
# CONFINES the engine (only s3:// and its temp directory allowed, lock_configuration), starts the Quack
# server, and checkpoints the catalog back (VACUUM INTO → whole-object PutObject, data-before-metadata)
# per-commit + on shutdown.
#
# This image is EMBEDDED into funcd (go:embed; see internal/runtime/embedimg) and imported into the
# managed containerd at startup — NO registry pull. Build + export: just build-runtime-images.
# NOTE: the real multi-MB image is built/exercised only on the node-gated lane (FUNCD_IT=1) — never
# `just ci`. `go build` / `just ci` are green on the committed placeholder tar
# (internal/runtime/embedimg/duckdb.tar).

# --- stage 1: pip-install the pinned duckdb + boto3 wheels + pre-install the DuckDB extensions -----
# Built on python:3.14-slim-bookworm to match the python314 curated base's interpreter + glibc
# (ADR-0054/0071 — the cc-debian12 final stage links the SAME glibc 2.36).
FROM python:3.14-slim-bookworm@sha256:48b13b003dda20b16f9442b8475aa05fe21bf6579a8c881db92ffb4d8fd20f83 AS deps
ARG TARGETARCH
ARG SOURCE_DATE_EPOCH
COPY images/runtime/duckdb/requirements.txt images/runtime/duckdb/duckdb.sha256 /tmp/
RUN set -eux; \
    pip install --no-compile --no-deps --require-hashes --target /opt/funcd-duckdb -r /tmp/requirements.txt; \
    # The `import duckdb` below caches each module it loads as a .pyc that records its source's mtime: give the
    # sources the build's SOURCE_DATE_EPOCH, the mtime the image carries, so the .pyc is the same on every build
    # and stays valid at runtime.
    find /opt/funcd-duckdb -exec touch -h -d "@${SOURCE_DATE_EPOCH:-0}" {} +; \
    # Pre-install the pinned DuckDB extensions into a known dir so the shim LOADs them OFFLINE at
    # runtime (the sandbox has no arbitrary egress). The exact pin set is the DuckDB 2.0.0 bundle.
    PYTHONPATH=/opt/funcd-duckdb DUCKDB_EXTENSION_DIRECTORY=/opt/duckdb-ext \
      python3 -c "import os,duckdb; os.makedirs('/opt/duckdb-ext',exist_ok=True); \
        c=duckdb.connect(config={'extension_directories':['/opt/duckdb-ext']}); \
        [c.execute(f'INSTALL {e}') for e in ('httpfs','ducklake','quack','sqlite')]"; \
    # extensions.duckdb.org can replace an extension of a released DuckDB, so each installed file must
    # match its pin, and every installed file must have one.
    cd /opt/duckdb-ext; \
    grep "/linux_${TARGETARCH}/" /tmp/duckdb.sha256 > /tmp/extensions.sha256; \
    sha256sum -c /tmp/extensions.sha256; \
    test "$(find . -name '*.duckdb_extension' | wc -l)" -eq "$(wc -l < /tmp/extensions.sha256)"

# The system shared-library closure the 3.14 interpreter, its stdlib C-extensions, the native DuckDB
# wheel, AND the loaded extensions link against (zlib→binascii, openssl→ssl + httpfs S3-TLS, ffi,
# sqlite, lzma, …). distroless/cc ships ONLY glibc + libstdc++/libgcc, so without this closure the
# very first `import duckdb` dies on `libz.so.1: cannot open shared object file` (ADR-0071 — the same
# closure the python314 curated image gathers; extended here for the DuckDB native lib + extensions).
# Excludes the core libs distroless already provides (so we never clobber its glibc).
RUN set -eux; mkdir -p /opt/funcd-libs; \
    { ldd /usr/local/lib/libpython3.14.so* || true; \
      for f in /usr/local/lib/python3.14/lib-dynload/*.so; do ldd "$f" || true; done; \
      find /opt/funcd-duckdb -name '*.so*' -exec ldd {} \; 2>/dev/null || true; \
      find /opt/duckdb-ext -name '*.duckdb_extension' -exec ldd {} \; 2>/dev/null || true; } 2>/dev/null \
      | awk '/=>/ {print $3}' \
      | grep -E '^/(usr/)?lib' \
      | grep -vE 'libc\.so|libm\.so|libpython|ld-linux|libstdc|libgcc|libdl|libpthread|librt' \
      | sort -u \
      | while read -r lib; do cp -L "$lib" /opt/funcd-libs/; done

# --- stage 2: the runtime image, on the custom distroless 3.14 base (mirrors python314/Dockerfile) -
FROM gcr.io/distroless/cc-debian12@sha256:e5d81ddde149641e2a9ba55be4545bc125c67de07508b03ba4c22e6eb0ded5aa

# The Python 3.14 runtime + stdlib, exactly as the python314 curated image lays them in (ADR-0054).
COPY --from=deps /usr/local/bin/python3.14 /usr/local/bin/python3.14
COPY --from=deps /usr/local/lib/libpython3.14.so* /usr/local/lib/
COPY --from=deps /usr/local/lib/python3.14 /usr/local/lib/python3.14
# The stdlib + DuckDB native-lib + extension shared-lib closure gathered in the deps stage
# (zlib/openssl/ffi/sqlite/…) — without it the distroless base can't `import duckdb` (ADR-0071).
COPY --from=deps /opt/funcd-libs/ /usr/local/lib/
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
