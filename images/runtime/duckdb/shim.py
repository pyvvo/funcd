#!/usr/bin/env python3.14
"""funcd ``duckdb`` runtime shim entrypoint (ADR-0086, F48) — the curated image's engine contract.

This module is IMAGE-ONLY: it runs inside the sandboxed ``duckdb`` Function, NEVER in the funcd
daemon (which stays pure-Go / no-cgo, ADR-0065). The native DuckDB engine is reached here through
the ``duckdb`` Python package — that is fine: it runs OUT-OF-PROCESS in this function's own
process, so no cgo is ever linked into the daemon. It is node-gated — exercised only on the
real-DuckDB lane (FUNCD_IT=1 / ``just lima-example-duckdb``) on real containerd, never by
``just ci``. Every API call below was verified against DuckDB 2.0.0 + the ducklake/httpfs/quack/
sqlite extensions (ADR-0263, experiment 0006).

Injected environment (ADR-0085 keypair + ADR-0032 fixed port):
    AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY  — the function's per-fn S3 keypair.
    AWS_ENDPOINT_URL / FUNCD_S3_ENDPOINT       — the injected funcd S3 gateway endpoint (path-style).
    AWS_REGION                                 — the S3 region the gateway expects.
    FUNCD_DUCKLAKE_CATALOG                     — the catalog blob key, an ``s3://<bucket>/<prefix>/
                                                 _ducklake/catalog.db`` URL. The Parquet DATA_PATH is
                                                 derived from it (``s3://<bucket>/<prefix>/``).
    FUNCD_QUACK_PORT                           — the fixed netns port the Quack server binds.

Engine confinement (the "Confine the SQL engine's reach" constraint, ADR-0086 B1 fix, an allow-list
since ADR-0263 Decision 8): pin the S3 client to the INJECTED funcd endpoint, turn S3 URL parameters
and the arbitrary ``HTTPFileSystem`` off, keep spills and secrets inside the engine; after the
catalog's ATTACH, allow only ``s3://`` paths and the temp directory, turn external access off, then
``SET lock_configuration=true`` LAST so no later query can change any of it. A query then reaches no
other path — isolation is the F47 keypair (cryptographic), not a query filter; remaining outbound is
bounded by the egress policy.

Durability (ADR-0086, the empirically-validated mechanism): DuckLake keeps its catalog in a LOCAL
SQLite file; Parquet data is written to S3 (the F47 surface) durably as part of each commit
(data-before-metadata). The checkpoint snapshots the catalog with SQLite's ``VACUUM INTO`` (a
consistent, WAL-folded single file — run through Python's ``sqlite3``, since ``VACUUM INTO`` is a
SQLite statement, NOT a DuckDB one) and uploads it whole-object to the catalog blob key. A torn Put
leaves the prior snapshot intact; recovery is GetObject → local file → ATTACH.
"""

from __future__ import annotations

import os
import signal
import sqlite3
import sys
import threading
import urllib.parse
from collections.abc import Mapping

import boto3  # SQLite-catalog blob sync against the injected funcd S3 endpoint.
import duckdb  # the native engine (out-of-process in THIS function — no cgo in the daemon).

# Where the local SQLite catalog lives during the engine's lifetime (sandbox ephemeral storage).
LOCAL_CATALOG = "/var/funcd/ducklake/catalog.db"
LOCAL_SNAPSHOT = LOCAL_CATALOG + ".snapshot"
# Where the engine spills: the only local directory its queries may reach (ADR-0263 Decision 8).
LOCAL_TMP = "/var/funcd/ducklake/tmp"

# How often the commit watcher reads the catalog's snapshot id: commits within one interval share one checkpoint.
COMMIT_POLL_SECONDS = 1.0

# The extensions the engine loads — pre-installed OFFLINE into the image's extension dir (the sandbox
# has no arbitrary egress): httpfs (S3), sqlite (the ducklake catalog backend), ducklake, quack.
EXTENSIONS = ("httpfs", "sqlite", "ducklake", "quack")

# The DUCKDB_* keys a Lake may set, and the DuckDB setting each one sets (ADR-0265). Any other DUCKDB_* key but the
# image's DUCKDB_EXTENSION_DIRECTORY ends the start: the sandbox's settings stay the shim's (ADR-0263 Decision 8).
TUNING = {
    "DUCKDB_MAX_TEMP_DIRECTORY_SIZE": "max_temp_directory_size",
    "DUCKDB_MEMORY_LIMIT": "memory_limit",
    "DUCKDB_THREADS": "threads",
}


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


def _data_path(catalog_url: str) -> str:
    """Derive the Parquet DATA_PATH (``s3://<bucket>/<prefix>/``) from the catalog URL.

    The catalog lives at ``s3://<bucket>/<prefix>/_ducklake/catalog.db``; DuckLake writes Parquet
    under the owned prefix root ``s3://<bucket>/<prefix>/`` (same owned prefix → the F47 owner-write).
    """
    marker = "/_ducklake/"
    i = catalog_url.find(marker)
    if i < 0:
        # No _ducklake/ marker — fall back to the URL's directory (strip the final path segment).
        return catalog_url.rsplit("/", 1)[0] + "/"
    return catalog_url[: i + 1]


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


def _connect() -> "duckdb.DuckDBPyConnection":
    """Open the engine connection, pinned to the image's OFFLINE extension directory.

    The extensions are pre-installed into DUCKDB_EXTENSION_DIRECTORY at image-build time; LOAD must
    look there (a bare connect() looks in ~/.duckdb and fails). autoinstall is disabled so a LOAD
    never reaches the network (the sandbox is confined).
    """
    ext_dir = os.environ.get("DUCKDB_EXTENSION_DIRECTORY")
    config = {"extension_directories": [ext_dir]} if ext_dir else {}
    con = duckdb.connect(config=config)
    con.execute("SET autoinstall_known_extensions=false")
    con.execute("SET autoload_known_extensions=false")
    for ext in EXTENSIONS:
        con.execute(f"LOAD {ext}")  # offline LOAD from the pre-installed extension dir.
    return con


def _tune(con: "duckdb.DuckDBPyConnection", environ: Mapping[str, str]) -> None:
    """Applies the allowed DUCKDB_* keys of environ; any other DUCKDB_* key but DUCKDB_EXTENSION_DIRECTORY, or a value
    DuckDB refuses, ends the start (exit 2) with a message naming the key (ADR-0265)."""
    for name in sorted(environ):
        if name.startswith("DUCKDB_") and name not in TUNING and name != "DUCKDB_EXTENSION_DIRECTORY":
            allowed = ", ".join(sorted(TUNING))
            sys.stderr.write(f"funcd duckdb shim: {name} is not an engine setting a Lake may set; allowed: {allowed}\n")
            raise SystemExit(2)
    for name in sorted(TUNING):
        if name not in environ:
            continue
        value = environ[name]
        try:
            con.execute(f"SET {TUNING[name]} = ?", [value])
        except Exception as exc:  # noqa: BLE001 — DuckDB's error names no key, so the message adds it.
            sys.stderr.write(f"funcd duckdb shim: {name}={value} is not a valid {TUNING[name]}: {exc}\n")
            raise SystemExit(2) from exc


def _configure(con: "duckdb.DuckDBPyConnection", endpoint: str, region: str) -> None:
    """Pin the S3 client to the injected endpoint and keep spills and secrets inside the engine, before the ATTACH.

    Disabling ``HTTPFileSystem`` blocks arbitrary ``http(s)://`` reads while leaving the S3FileSystem
    (the funcd data path) enabled. ``allow_persistent_secrets`` must be set before anything uses the
    secret manager.
    """
    host = endpoint.split("://", 1)[-1]  # DuckDB's s3_endpoint is host[:port], no scheme.
    use_ssl = endpoint.lower().startswith("https://")
    con.execute("SET s3_endpoint=?", [host])
    con.execute("SET s3_region=?", [region])
    con.execute("SET s3_access_key_id=?", [os.environ["AWS_ACCESS_KEY_ID"]])
    con.execute("SET s3_secret_access_key=?", [os.environ["AWS_SECRET_ACCESS_KEY"]])
    con.execute("SET s3_url_style='path'")
    con.execute(f"SET s3_use_ssl={'true' if use_ssl else 'false'}")
    con.execute("SET s3_enable_deprecated_url_parameters=false")
    con.execute("SET disabled_filesystems='HTTPFileSystem'")
    os.makedirs(LOCAL_TMP, exist_ok=True)
    con.execute(f"SET temp_directory='{LOCAL_TMP}'")
    con.execute("SET allow_persistent_secrets=false")


def _confine(con: "duckdb.DuckDBPyConnection") -> None:
    """After the catalog's ATTACH: allow only ``s3://`` paths and the temp directory, turn external access off, and
    lock the configuration LAST so no query can change any of it (ADR-0263 Decision 8). The attached catalog stays
    writable; remaining outbound is bounded by funcd's egress policy."""
    con.execute(f"SET allowed_directories=['s3://', '{LOCAL_TMP}/']")
    con.execute("SET enable_external_access=false")
    con.execute("SET lock_configuration=true")


def _recover_catalog(s3, bucket: str, key: str) -> None:
    """boto3 GetObject the SQLite catalog → the local file, before ATTACH.

    On first deploy the object does not exist yet — skip (DuckLake creates a fresh catalog on
    ATTACH). The min-replica=1 replica owns the prefix, so this is the only writer.
    """
    os.makedirs(os.path.dirname(LOCAL_CATALOG), exist_ok=True)
    try:
        s3.download_file(bucket, key, LOCAL_CATALOG)
    except Exception as exc:  # noqa: BLE001 — a 404-shaped error means "no catalog yet" (fresh deploy).
        if "404" in str(exc) or "NoSuchKey" in str(exc) or "Not Found" in str(exc):
            return
        raise


class _Checkpointer:
    """Checkpoints the catalog to blob: SQLite ``VACUUM INTO`` snapshot → whole-object PutObject.

    ``VACUUM INTO`` is a SQLite statement — run through Python's ``sqlite3`` against the catalog
    FILE, NOT the DuckDB connection (DuckDB has no VACUUM INTO). It works while DuckDB holds the
    catalog attached (verified), folding the WAL into one consistent, integrity-complete file (a
    naive copy would lose un-checkpointed WAL pages). The whole-object Put is atomic — a torn Put
    leaves the prior snapshot intact. Ordering is data-before-metadata: DuckLake makes the Parquet
    durable on S3 BEFORE this runs, so the catalog snapshot never names a missing object. The catalog
    is metadata-sized (file-lists + snapshots, not row data), within the gateway's upload cap.
    Coalesced: _CommitWatcher calls it once per poll interval in which a transaction committed, and
    the SIGTERM handler once on graceful shutdown; the lock serializes the two.
    """

    def __init__(self, s3, bucket: str, key: str) -> None:
        self._s3 = s3
        self._bucket = bucket
        self._key = key
        self._lock = threading.Lock()

    def checkpoint(self) -> None:
        with self._lock:
            try:
                os.remove(LOCAL_SNAPSHOT)
            except FileNotFoundError:
                pass
            # VACUUM INTO a fresh consistent snapshot via SQLite (not DuckDB).
            scon = sqlite3.connect(LOCAL_CATALOG, timeout=30)
            try:
                scon.execute("VACUUM INTO ?", [LOCAL_SNAPSHOT])
            finally:
                son_close = getattr(scon, "close", None)
                if son_close:
                    son_close()
            # whole-object PutObject — atomic; never a partial/multipart commit of the catalog.
            with open(LOCAL_SNAPSHOT, "rb") as fh:
                self._s3.put_object(Bucket=self._bucket, Key=self._key, Body=fh.read())


def _snapshot_id(path: str) -> int | None:
    """The latest DuckLake snapshot id in the SQLite catalog at path; every committed transaction adds one."""
    scon = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=30)
    try:
        row = scon.execute("SELECT max(snapshot_id) FROM ducklake_snapshot").fetchone()
    finally:
        scon.close()
    return row[0] if row else None


class _CommitWatcher(threading.Thread):
    """Checkpoints the catalog after each committed transaction (ADR-0086), coalesced per poll interval.

    DuckLake adds a row to ducklake_snapshot per commit; the watcher compares the latest id with the one it
    last checkpointed, starting from the id at start, so a catalog nobody wrote to is not uploaded. A failed
    read or checkpoint is retried on the next tick, keeping the old id. A SIGKILL then loses at most the
    commits of one interval, not every commit since the start (#998).
    """

    def __init__(self, checkpointer: _Checkpointer, path: str, interval: float) -> None:
        super().__init__(name="commit-watcher", daemon=True)
        self._checkpointer = checkpointer
        self._path = path
        self._interval = interval
        self._done = threading.Event()
        self._seen = _snapshot_id(path)

    def run(self) -> None:
        while not self._done.wait(self._interval):
            try:
                latest = _snapshot_id(self._path)
                if latest != self._seen:
                    self._checkpointer.checkpoint()
                    self._seen = latest
            except Exception as exc:  # noqa: BLE001 — keep watching; the next tick retries.
                sys.stderr.write(f"funcd duckdb shim: catalog checkpoint failed, retrying: {exc}\n")

    def stop(self) -> None:
        self._done.set()


def main() -> int:
    quack_port = int(_require("FUNCD_QUACK_PORT"))
    catalog_url = _require("FUNCD_DUCKLAKE_CATALOG")
    endpoint = os.environ.get("FUNCD_S3_ENDPOINT") or _require("AWS_ENDPOINT_URL")
    region = os.environ.get("AWS_REGION", "us-east-1")
    _require("AWS_ACCESS_KEY_ID")
    _require("AWS_SECRET_ACCESS_KEY")
    bucket, key = _split_s3_url(catalog_url)
    data_path = _data_path(catalog_url)

    s3 = _s3_client(endpoint, region)
    con = _connect()
    _tune(con, os.environ)
    _configure(con, endpoint, region)

    # Recover: GetObject the catalog from blob → local file → ATTACH (fresh on first deploy). The
    # Parquet DATA_PATH is the owned prefix on S3 (the F47 surface); the catalog is the local SQLite.
    _recover_catalog(s3, bucket, key)
    # OVERRIDE_DATA_PATH: a catalog restored under another prefix (ADR-0222 asLocation) records its old
    # DATA_PATH, which DuckLake refuses to replace otherwise; the data files are relative to it.
    # DUCKLAKE_VERSION: a new catalog is DuckLake 1.1 whatever DuckDB's default; a 1.0 catalog fails here (ADR-0263).
    con.execute(
        "ATTACH 'ducklake:sqlite:" + LOCAL_CATALOG + "' AS lakehouse "
        "(DATA_PATH ?, OVERRIDE_DATA_PATH TRUE, DUCKLAKE_VERSION '1.1')",
        [data_path],
    )
    _confine(con)

    checkpointer = _Checkpointer(s3, bucket, key)
    watcher = _CommitWatcher(checkpointer, LOCAL_CATALOG, COMMIT_POLL_SECONDS)
    watcher.start()

    # Graceful shutdown: checkpoint the catalog one final time on SIGTERM/SIGINT.
    def _on_term(_signum, _frame):
        watcher.stop()
        try:
            checkpointer.checkpoint()
        finally:
            raise SystemExit(0)

    signal.signal(signal.SIGTERM, _on_term)
    signal.signal(signal.SIGINT, _on_term)

    # Serve Quack over HTTP on the fixed netns port (a quack:// RPC URI). A Quack client ALWAYS needs a
    # token (a token-less client is refused "Could not find a Quack authentication token"), so the
    # engine serves WITH one: QUACK_TOKEN, injected from the Lake's spec.bindings.secret. disable_ssl:
    # the ingress gateway terminates TLS and is the authoritative OUTER auth gate (ADR-0013) — Quack
    # runs plain HTTP behind it, the token is the inner gate (allow_other_hostname: the gateway proxies
    # a different Host). quack_serve is non-blocking (spawns the server) — we then block on signal.pause().
    quack_token = _require("QUACK_TOKEN")  # >= 4 chars; consumers present the same token
    serve_uri = f"quack://0.0.0.0:{quack_port}"
    con.execute(
        "CALL quack_serve(?, token := ?, disable_ssl := true, allow_other_hostname := true)",
        [serve_uri, quack_token],
    ).fetchall()

    signal.pause()  # block; the SIGTERM handler checkpoints + exits when the sandbox stops us.
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
