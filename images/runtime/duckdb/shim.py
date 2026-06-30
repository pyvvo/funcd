#!/usr/bin/env python3.14
"""funcd ``duckdb`` runtime shim entrypoint (ADR-0086, F48) — the curated image's engine contract.

This module is IMAGE-ONLY: it runs inside the sandboxed ``duckdb`` Function, NEVER in the funcd
daemon (which stays pure-Go / no-cgo, ADR-0065). The native DuckDB engine is reached here through
the ``duckdb`` Python package — that is fine: it runs OUT-OF-PROCESS in this function's own
process, so no cgo is ever linked into the daemon. It is node-gated — exercised only on the
real-DuckDB lane (FUNCD_IT=1) on real containerd, never by ``just ci``. Read it as the spec the
live lane verifies.

Injected environment (ADR-0085 keypair + ADR-0032 fixed port):
    AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY  — the function's per-fn S3 keypair.
    AWS_ENDPOINT_URL / FUNCD_S3_ENDPOINT       — the injected funcd S3 gateway endpoint (path-style).
    AWS_REGION                                 — the S3 region the gateway expects.
    FUNCD_DUCKLAKE_CATALOG                     — the ``_ducklake/catalog.db`` blob key
                                                 (``s3://<bucket>/<prefix>/_ducklake/catalog.db``).
    FUNCD_QUACK_PORT                           — the fixed netns port the Quack server binds.

Engine confinement (the "Confine the SQL engine's reach" constraint, ADR-0086 B1 fix): pin the S3
client to the INJECTED funcd endpoint, disable the http(s) filesystem so a query can't read
arbitrary URLs, then ``SET lock_configuration=true`` LAST so a subsequent query cannot re-point S3
or re-enable the disabled filesystem. After that the engine can physically reach only the injected
funcd S3 endpoint — isolation is the F47 keypair (cryptographic), not a query filter; remaining
outbound is bounded by the egress policy.
"""

from __future__ import annotations

import os
import signal
import sys
import threading
import urllib.parse
from typing import Optional

import boto3  # SQLite-catalog blob sync against the injected funcd S3 endpoint.
import duckdb  # the native engine (out-of-process in THIS function — no cgo in the daemon).

# Where the local SQLite catalog lives during the engine's lifetime (sandbox ephemeral storage).
LOCAL_CATALOG = "/var/funcd/ducklake/catalog.db"
LOCAL_SNAPSHOT = LOCAL_CATALOG + ".snapshot"


def _require(name: str) -> str:
    """Read a required env var or exit non-zero (fail closed)."""
    val = os.environ.get(name)
    if not val:
        sys.stderr.write(f"funcd duckdb shim: {name} is required\n")
        raise SystemExit(2)
    return val


def _split_s3_url(url: str) -> tuple[str, str]:
    """Split ``s3://bucket/key...`` into (bucket, key). FUNCD_DUCKLAKE_CATALOG is such a URL."""
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme != "s3" or not parsed.netloc:
        sys.stderr.write(f"funcd duckdb shim: FUNCD_DUCKLAKE_CATALOG {url!r} is not an s3:// URL\n")
        raise SystemExit(2)
    return parsed.netloc, parsed.path.lstrip("/")


def _s3_client(endpoint: str, region: str):
    """A boto3 S3 client pinned to the injected funcd gateway endpoint + the per-fn keypair."""
    return boto3.client(
        "s3",
        endpoint_url=endpoint,
        region_name=region,
        aws_access_key_id=os.environ["AWS_ACCESS_KEY_ID"],
        aws_secret_access_key=os.environ["AWS_SECRET_ACCESS_KEY"],
        # path-style addressing (the F47 surface is path-style, ADR-0080).
        config=boto3.session.Config(s3={"addressing_style": "path"}),
    )


def _configure_and_confine(con: "duckdb.DuckDBPyConnection", endpoint: str, region: str) -> None:
    """LOAD the extensions, pin the S3 client to the injected endpoint, then confine + lock.

    Confinement order matters: disable the http(s) filesystem and pin s3_* FIRST, then
    ``lock_configuration=true`` LAST so no later query can re-point S3 or re-enable the filesystem.
    """
    # The extensions are pre-installed into the image (offline) — LOAD only, no network INSTALL.
    for ext in ("httpfs", "ducklake", "quack"):
        con.execute(f"INSTALL {ext}")  # no-op if already present in the image's extension dir
        con.execute(f"LOAD {ext}")

    # Pin the S3 client to the INJECTED funcd gateway endpoint + the per-fn keypair (ADR-0085).
    host = endpoint.split("://", 1)[-1]  # DuckDB's s3_endpoint is host[:port], no scheme.
    use_ssl = endpoint.lower().startswith("https://")
    con.execute("SET s3_endpoint=?", [host])
    con.execute("SET s3_region=?", [region])
    con.execute("SET s3_access_key_id=?", [os.environ["AWS_ACCESS_KEY_ID"]])
    con.execute("SET s3_secret_access_key=?", [os.environ["AWS_SECRET_ACCESS_KEY"]])
    con.execute("SET s3_url_style='path'")
    con.execute(f"SET s3_use_ssl={'true' if use_ssl else 'false'}")

    # Confine the engine: disable the arbitrary http(s) filesystem so no query can read a non-S3
    # URL (e.g. SELECT … FROM 'https://elsewhere/x'). The S3 data path stays the F47 PEP.
    con.execute("SET disabled_filesystems='HTTPFileSystem'")

    # Lock the configuration LAST — a query can no longer re-point s3_endpoint or re-enable the
    # disabled filesystem. Remaining outbound is bounded by funcd's egress policy.
    con.execute("SET lock_configuration=true")


def _recover_catalog(s3, bucket: str, key: str) -> None:
    """boto3 GetObject the SQLite catalog → the local file, before ATTACH.

    On first deploy the object does not exist yet — skip (DuckLake creates a fresh catalog on
    ATTACH). The recreate-rollout min-replica=1 replica owns the prefix, so this is the only writer.
    """
    os.makedirs(os.path.dirname(LOCAL_CATALOG), exist_ok=True)
    try:
        s3.download_file(bucket, key, LOCAL_CATALOG)
    except s3.exceptions.NoSuchKey:
        pass  # fresh catalog (first deploy) — leave the local file absent so ATTACH creates it.
    except Exception as exc:  # noqa: BLE001 — a 404-shaped error also means "no catalog yet".
        # boto3 raises ClientError (not NoSuchKey) for a missing key on some S3 surfaces; treat a
        # not-found as a fresh catalog, re-raise anything else (a real auth/transport failure).
        if "404" in str(exc) or "NoSuchKey" in str(exc) or "Not Found" in str(exc):
            return
        raise


class _Checkpointer:
    """Checkpoints the catalog to blob: VACUUM INTO a consistent snapshot → whole-object PutObject.

    Ordering is data-before-metadata: DuckLake makes the Parquet durable through the F47 S3 PEP
    BEFORE this runs, so the catalog snapshot never names a missing object. ``VACUUM INTO`` folds
    the WAL into one consistent file (a naive copy would lose un-checkpointed WAL pages); the
    whole-object Put is atomic — a torn Put leaves the prior snapshot intact (never a corrupt or
    dangling-reference state). The catalog is metadata-sized (file-lists + snapshots, not row data),
    so it stays within the gateway's upload cap. Coalesced: callers invoke per committed transaction
    (debounced by the engine) and once on graceful shutdown.
    """

    def __init__(self, con, s3, bucket: str, key: str) -> None:
        self._con = con
        self._s3 = s3
        self._bucket = bucket
        self._key = key
        self._lock = threading.Lock()

    def checkpoint(self) -> None:
        with self._lock:  # serialize concurrent (txn + shutdown) checkpoints — single writer.
            # VACUUM INTO a fresh, consistent snapshot file (overwrites the prior local snapshot).
            try:
                os.remove(LOCAL_SNAPSHOT)
            except FileNotFoundError:
                pass
            self._con.execute("VACUUM INTO ?", [LOCAL_SNAPSHOT])
            # whole-object PutObject — atomic; never a partial/multipart commit of the catalog.
            with open(LOCAL_SNAPSHOT, "rb") as fh:
                self._s3.put_object(Bucket=self._bucket, Key=self._key, Body=fh.read())


def main() -> int:
    quack_port = int(_require("FUNCD_QUACK_PORT"))
    catalog_url = _require("FUNCD_DUCKLAKE_CATALOG")
    endpoint = os.environ.get("FUNCD_S3_ENDPOINT") or _require("AWS_ENDPOINT_URL")
    region = os.environ.get("AWS_REGION", "us-east-1")
    _require("AWS_ACCESS_KEY_ID")
    _require("AWS_SECRET_ACCESS_KEY")
    bucket, key = _split_s3_url(catalog_url)

    s3 = _s3_client(endpoint, region)
    con = duckdb.connect()  # an in-process engine connection (this function's own process).
    _configure_and_confine(con, endpoint, region)

    # Recover: GetObject the catalog from blob → local file → ATTACH (fresh on first deploy).
    _recover_catalog(s3, bucket, key)
    con.execute(f"ATTACH 'ducklake:sqlite:{LOCAL_CATALOG}' AS lakehouse")

    checkpointer = _Checkpointer(con, s3, bucket, key)

    # Graceful shutdown: checkpoint the catalog one final time on SIGTERM/SIGINT (the coalesce point
    # in addition to the per-committed-transaction checkpoints the engine triggers).
    def _on_term(_signum, _frame):
        try:
            checkpointer.checkpoint()
        finally:
            raise SystemExit(0)

    signal.signal(signal.SIGTERM, _on_term)
    signal.signal(signal.SIGINT, _on_term)

    # Serve Quack over HTTP on the fixed netns port; the ingress gateway routes to it behind the
    # gateway auth/TLS + PEP (ADR-0013 — the authoritative gate on who may reach the endpoint).
    con.execute("CALL start_quack_server(?)", [quack_port])

    # Block forever serving Quack; the SIGTERM handler checkpoints + exits when the sandbox stops us.
    signal.pause()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
