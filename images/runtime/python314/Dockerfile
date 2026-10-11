# Curated Python 3.14 runtime image (ADR-0054, NEW) — custom distroless.
#
# ADR-0050's subinterpreter pool needs Python 3.14, but off-the-shelf distroless/python3 is
# 3.11. So we build a CUSTOM distroless 3.14: a multi-stage build that takes the Python 3.14
# runtime + its stdlib C-extension shared libs from python:3.14-slim and lays them into
# gcr.io/distroless/cc-debian12 (the glibc + libstdc++ minimal base distroless ships for
# C-extension binaries). The funcd shim is STDLIB-ONLY (ADR-0049) ⇒ no pip, no venv, no
# site-packages path-rewrite breakage — the venv-portability problem that blocks the general
# custom-python case does not apply to us.
#
# Execution mechanics are unchanged from ADR-0011/0032: the function artifact is bind-mounted
# read-only at /var/funcd/artifact, FUNCD_ARTIFACT points at it, and the shim binds FUNCD_PORT
# in the per-function netns. This image is EMBEDDED into funcd (go:embed; see
# internal/runtime/embedimg) and imported into the managed containerd at startup — NO registry
# pull. Build + export with: just build-runtime-images (gzip -9 OCI tars per arch; the cc base
# layer is shared with other cc-based images where the build arranges it).

# Both bases are pinned by the digest of their multi-arch index (resolved 2026-10-11), so every build reads
# the same bytes; move one deliberately, from `docker buildx imagetools inspect <image>:<tag>`, and keep
# python:3.14-slim-bookworm at the digest images/runtime/duckdb/Dockerfile and scripts/lanes.yaml use.

# --- stage 1: the stock 3.14 build, source of the runtime + stdlib C-ext libs --------------
# Pinned to the BOOKWORM variant (glibc 2.36) so the interpreter + its C-ext libs link against the SAME
# glibc the distroless/cc-debian12 final stage ships — the default `:3.14-slim` drifted to trixie
# (glibc 2.41), whose libpython won't load on cc-debian12 (ADR-0071: a working, reproducible rebuild).
FROM python:3.14-slim-bookworm@sha256:48b13b003dda20b16f9442b8475aa05fe21bf6579a8c881db92ffb4d8fd20f83 AS py

# The funcd Python shim package (stdlib-only — ADR-0049/0050). Laid into the site path the
# embedded 3.14 will see. `shim` is a named build context: the pinned funcd-python module's shim/ dir
# (ADR-0141).
COPY --from=shim src/funcd_shim /opt/funcd/funcd_shim
# fastjsonschema (pure-Python, BSD-3) — the ONE runtime dependency the build-baked contract validators
# import (ADR-0060). Shipped onto the same PYTHONPATH as the shim so a contract-validated artifact can
# import its validator at runtime; without it such functions crash at startup (ADR-0071). Pinned by version
# and wheel hash (requirements.txt); the final distroless stage carries it with no pip/venv.
COPY images/runtime/python314/requirements.txt /tmp/requirements.txt
RUN pip install --no-compile --no-deps --require-hashes --target /opt/funcd -r /tmp/requirements.txt
# The system shared-library closure the 3.14 interpreter + its stdlib C-extensions link against (zlib,
# openssl, ffi, sqlite, lzma, …). distroless/cc ships ONLY glibc + libstdc++/libgcc, so the stdlib's
# C-extensions (zlib→binascii, ssl, sqlite3, …) fail to load there. Gather the resolved ldd closure here —
# excluding the core libs distroless already provides (else we'd clobber its glibc) — and copy it into the
# final stage's LD_LIBRARY_PATH. This is the closure ADR-0054 deferred as an open question (ADR-0071).
RUN set -eux; mkdir -p /opt/funcd-libs; \
    { ldd /usr/local/lib/libpython3.14.so* || true; \
      for f in /usr/local/lib/python3.14/lib-dynload/*.so; do ldd "$f" || true; done; } 2>/dev/null \
      | awk '/=>/ {print $3}' \
      | grep -E '^/(usr/)?lib' \
      | grep -vE 'libc\.so|libm\.so|libpython|ld-linux|libstdc|libgcc|libdl|libpthread|librt' \
      | sort -u \
      | while read -r lib; do cp -L "$lib" /opt/funcd-libs/; done
# The solo-shim entry (mirrors shimEntryScript in funcd-python's shim/embed.go) — `python3 <entry>`.
RUN printf 'from funcd_shim.shim import main\nraise SystemExit(main())\n' > /opt/funcd/entry.py

# --- stage 2: the custom distroless 3.14 image ---------------------------------------------
# distroless/cc carries glibc + libstdc++/libgcc — what the 3.14 interpreter + its lib-dynload
# C-extensions link against. We copy the interpreter, its full stdlib (incl. lib-dynload), the
# shared libpython, and the shim package. The exact lib set is the `ldd` closure of
# python3.14 + lib-dynload/*.so (ADR-0054 Open question — enumerated at impl, stable once
# pinned); copying the whole /usr/local/lib/python3.14 tree + libpython is the safe superset.
FROM gcr.io/distroless/cc-debian12@sha256:e5d81ddde149641e2a9ba55be4545bc125c67de07508b03ba4c22e6eb0ded5aa

COPY --from=py /usr/local/bin/python3.14 /usr/local/bin/python3.14
COPY --from=py /usr/local/lib/libpython3.14.so* /usr/local/lib/
COPY --from=py /usr/local/lib/python3.14 /usr/local/lib/python3.14
# The stdlib C-extension shared-lib closure gathered in the build stage (zlib/openssl/ffi/sqlite/…).
COPY --from=py /opt/funcd-libs/ /usr/local/lib/
COPY --from=py /opt/funcd /opt/funcd

ENV LD_LIBRARY_PATH=/usr/local/lib \
    PYTHONHOME=/usr/local \
    PYTHONPATH=/opt/funcd \
    PYTHONDONTWRITEBYTECODE=1 \
    FUNCD_ARTIFACT=/var/funcd/artifact/handler.py

# The shim entry is the entrypoint target (python runs the stdlib-only shim; no venv).
ENTRYPOINT ["/usr/local/bin/python3.14", "/opt/funcd/entry.py"]
