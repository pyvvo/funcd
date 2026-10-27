#!/usr/bin/env python3.14
"""funcd ``duckdb`` runtime shim entrypoint (ADR-0086, F48) — the curated image's engine contract.

This module is IMAGE-ONLY: it runs inside the sandboxed ``duckdb`` Function, NEVER in the funcd
daemon (which stays pure-Go / no-cgo, ADR-0065). The native DuckDB engine is reached here through
the ``duckdb`` Python package — that is fine: it runs OUT-OF-PROCESS in this function's own
process, so no cgo is ever linked into the daemon. It is node-gated — exercised only on the
real-DuckDB lane (FUNCD_IT=1 / ``just lima-example-duckdb``) on real containerd, never by
``just ci``. Every API call below was verified against DuckDB 2.0.0 + the ducklake/httpfs/quack
extensions (ADR-0263, experiment 0006); engine_test.py runs this module against them.

Injected environment (ADR-0085 keypair + ADR-0032 fixed port):
    AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY  — the function's per-fn S3 keypair.
    AWS_ENDPOINT_URL / FUNCD_S3_ENDPOINT       — the injected funcd S3 gateway endpoint (path-style).
    AWS_REGION                                 — the S3 region the gateway expects.
    FUNCD_DUCKLAKE_CATALOG                     — the catalog blob key in the catalog store (ADR-0293), an
                                                 ``s3://funcd.lake-catalogs/<bucket>/<prefix>/_ducklake/
                                                 catalog.db`` URL.
    FUNCD_DUCKLAKE_DATA_PATH                   — the Parquet DATA_PATH, ``s3://<bucket>/<prefix>/``; without
                                                 it (an engine started by hand) it is derived from the
                                                 catalog URL, as before the catalog store.
    FUNCD_QUACK_PORT                           — the fixed netns port the Quack server binds.

Engine confinement (the "Confine the SQL engine's reach" constraint, ADR-0086 B1 fix, an allow-list
since ADR-0263 Decision 8): pin the S3 client to the INJECTED funcd endpoint, turn S3 URL parameters
and the arbitrary ``HTTPFileSystem`` off, keep spills and secrets inside the engine; after the
catalog's ATTACH, allow only ``s3://`` paths and the temp directory, turn external access off, then
``SET lock_configuration=true`` LAST so no later query can change any of it. A query then reaches no
other path — isolation is the F47 keypair (cryptographic), not a query filter; remaining outbound is
bounded by the egress policy.

Durability (ADR-0086, #1198): DuckLake keeps its catalog in a LOCAL DuckDB file; Parquet data is
written to S3 (the F47 surface) durably as part of each commit (data-before-metadata). A checkpoint
copies the catalog's metadata database with ``COPY FROM DATABASE`` (one transaction of the engine's
own DuckDB, so the copy is consistent while commits land) into a fresh file and uploads it
whole-object to the catalog blob key. A torn Put leaves the prior checkpoint intact; recovery is
GetObject → local file → ATTACH. With FUNCD_LAKE_SAVE_PORT (no lake.saveInterval, ADR-0279) the shim saves when the
Lake PEP proxy asks, before it answers a client, instead of at an interval, and saves again every second while the
last save failed (ADR-0289).
"""

from __future__ import annotations

import json
import os
import re
import secrets
import signal
import sys
import threading
import time
import urllib.parse
from collections.abc import Callable, Mapping
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import boto3  # the catalog's blob sync against the injected funcd S3 endpoint.
import duckdb  # the native engine (out-of-process in THIS function — no cgo in the daemon).

# Where the local DuckDB catalog lives during the engine's lifetime (sandbox ephemeral storage).
LOCAL_CATALOG = "/var/funcd/ducklake/catalog.ducklake"
# Where the engine spills: the only local directory its queries may reach (ADR-0263 Decision 8).
LOCAL_TMP = "/var/funcd/ducklake/tmp"
# Where a checkpoint is copied before its upload: under the temp directory, the one local directory the engine may
# still attach after the confinement.
LOCAL_CHECKPOINT = LOCAL_TMP + "/checkpoint.ducklake"
# DuckLake's metadata database of the catalog attached as lakehouse.
METADATA = "__ducklake_metadata_lakehouse"
# The catalog's latest snapshot id: every committed transaction adds one.
LATEST_SNAPSHOT = f"SELECT max(snapshot_id) FROM {METADATA}.ducklake_snapshot"
# The same, in the closed catalog file a stop uploads (attached as funcd_closed).
CLOSED_SNAPSHOT = "SELECT max(snapshot_id) FROM funcd_closed.ducklake_snapshot"

# How often the commit watcher reads the catalog's snapshot id: commits within one interval share one checkpoint. The
# platform sets it from lake.saveInterval, as FUNCD_LAKE_SAVE_INTERVAL (ADR-0274); one second for an engine started by
# hand with neither it nor FUNCD_LAKE_SAVE_PORT, which runs the save endpoint instead (ADR-0279).
COMMIT_POLL_SECONDS = 1.0
SAVE_INTERVAL_KEY = "FUNCD_LAKE_SAVE_INTERVAL"
# The port of the save endpoint the Lake PEP proxy calls before it answers, set when lake.saveInterval is not
# (ADR-0279); without it the engine saves at its interval.
SAVE_PORT_KEY = "FUNCD_LAKE_SAVE_PORT"
# How often a failed save is retried in that mode, the start-up save's (ADR-0276 Decision 2) or any other (ADR-0289).
SAVE_RETRY_SECONDS = 1.0
# How long a stop lets the save calls it covered send their answers before Quack stops (ADR-0279 Decision 2).
SAVE_ANSWER_WAIT_SECONDS = 1.0
# How long a stop lets the clients of the queries it cancelled read their interrupt error before Quack stops: a client
# reads it on its next poll, tens of milliseconds after the cancel (ADR-0273).
CANCEL_ANSWER_WAIT_SECONDS = 1.0
# ADR-0194's duration grammar (api/types/v1alpha1/duration.go): whole numbers with the units h, m, s and ms, largest
# unit first, as funcdctl backup plan writes them.
DURATION = re.compile(r"^([0-9]+h([0-9]+m)?([0-9]+s)?([0-9]+ms)?|[0-9]+m([0-9]+s)?([0-9]+ms)?|[0-9]+s([0-9]+ms)?|[0-9]+ms)$")
UNIT_SECONDS = {"h": 3600.0, "m": 60.0, "s": 1.0, "ms": 0.001}
# How long a stop lets the running queries drain, while the commit watcher keeps saving the catalog, before it detaches
# the catalog and cancels what still runs: well inside the runtime's 10 s SIGTERM grace (#1198, #1217, ADR-0273).
STOP_WAIT_SECONDS = 5.0
# The engine's report file in its boot dir, which the runtime reads into the Lake's CatalogSaved condition, and the
# runtime's bound on what it reads (ADR-0276).
REPORT_KEY = "FUNCD_REPORTFILE"
REPORT_MAX_BYTES = 1024

# The extensions the engine loads — pre-installed OFFLINE into the image's extension dir (the sandbox
# has no arbitrary egress): httpfs (S3), ducklake, quack.
EXTENSIONS = ("httpfs", "ducklake", "quack")

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


def _s3_client(endpoint: str, region: str, access: str, secret: str):
    """A boto3 S3 client pinned to the injected funcd gateway endpoint + the per-fn keypair."""
    return boto3.client(
        "s3",
        endpoint_url=endpoint,
        region_name=region,
        aws_access_key_id=access,
        aws_secret_access_key=secret,
        # path-style addressing (the F47 surface is path-style, ADR-0080); no proxy from the environment.
        config=boto3.session.Config(s3={"addressing_style": "path"}, proxies={}),
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


def _save_interval(environ: Mapping[str, str]) -> float:
    """The catalog save interval in seconds (ADR-0274): FUNCD_LAKE_SAVE_INTERVAL, an ADR-0194 duration from 500ms to 1h,
    or COMMIT_POLL_SECONDS without it; another value ends the start (exit 2) with a message naming the key."""
    value = environ.get(SAVE_INTERVAL_KEY)
    if value is None:
        return COMMIT_POLL_SECONDS
    seconds = sum(int(n) * UNIT_SECONDS[u] for n, u in re.findall(r"([0-9]+)(ms|h|m|s)", value))
    if not DURATION.fullmatch(value) or not 0.5 <= seconds <= 3600:
        sys.stderr.write(f"funcd duckdb shim: {SAVE_INTERVAL_KEY} {value}: want a duration from 500ms to 1h\n")
        raise SystemExit(2)
    return seconds


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


def _configure(con: "duckdb.DuckDBPyConnection", endpoint: str, region: str, access: str, secret: str) -> None:
    """Pin the S3 client to the injected endpoint and keep spills and secrets inside the engine, before the ATTACH.

    Disabling ``HTTPFileSystem`` blocks arbitrary ``http(s)://`` reads while leaving the S3FileSystem
    (the funcd data path) enabled. ``allow_persistent_secrets`` must be set before anything uses the
    secret manager. The key pair goes only into a temporary secret, whose value a query sees redacted; a
    plain setting would hand it to any query.
    """
    host = endpoint.split("://", 1)[-1]  # DuckDB's s3_endpoint is host[:port], no scheme.
    use_ssl = endpoint.lower().startswith("https://")
    con.execute("SET s3_endpoint=?", [host])
    con.execute("SET s3_region=?", [region])
    con.execute("SET s3_url_style='path'")
    con.execute(f"SET s3_use_ssl={'true' if use_ssl else 'false'}")
    con.execute("SET s3_enable_deprecated_url_parameters=false")
    con.execute("SET disabled_filesystems='HTTPFileSystem'")
    os.makedirs(LOCAL_TMP, exist_ok=True)
    con.execute(f"SET temp_directory='{LOCAL_TMP}'")
    con.execute("SET allow_persistent_secrets=false")
    con.execute(
        "CREATE TEMPORARY SECRET funcd_lake_s3 (TYPE s3, KEY_ID ?, SECRET ?, ENDPOINT ?, REGION ?, "
        "URL_STYLE 'path', USE_SSL ?, SCOPE 's3://')",
        [access, secret, host, region, use_ssl],
    )


def _confine(con: "duckdb.DuckDBPyConnection") -> None:
    """After the catalog's ATTACH: allow only ``s3://`` paths and the temp directory, turn external access off, and
    lock the configuration LAST so no query can change any of it (ADR-0263 Decision 8). The attached catalog stays
    writable; remaining outbound is bounded by funcd's egress policy."""
    con.execute(f"SET allowed_directories=['s3://', '{LOCAL_TMP}/']")
    con.execute("SET enable_external_access=false")
    con.execute("SET lock_configuration=true")


def _recover_catalog(s3, bucket: str, key: str) -> bool:
    """boto3 GetObject the catalog → the local file, before ATTACH, and returns whether there was one.

    On first deploy the object does not exist yet — skip (DuckLake creates a fresh catalog on
    ATTACH). The min-replica=1 replica owns the prefix, so this is the only writer.
    """
    os.makedirs(os.path.dirname(LOCAL_CATALOG), exist_ok=True)
    try:
        s3.download_file(bucket, key, LOCAL_CATALOG)
    except Exception as exc:  # noqa: BLE001 — a 404-shaped error means "no catalog yet" (fresh deploy).
        if "404" in str(exc) or "NoSuchKey" in str(exc) or "Not Found" in str(exc):
            return False
        raise
    return True


def _report(path: str | None, failing_since: datetime | None, error: BaseException | None) -> None:
    """Replaces the engine's report whole, so the daemon never reads half of one (ADR-0276): ``saved <time>`` after a
    save, ``failing since <time of the first failure>: <error>`` while saves fail. No path, no report; a write that
    fails is only logged."""
    if not path:
        return
    if error is None:
        line = "saved " + datetime.now(timezone.utc).isoformat(timespec="seconds")
    else:
        line = f"failing since {failing_since.isoformat(timespec='seconds')}: {str(error).replace(chr(10), ' ')}"
    tmp = path + ".tmp"
    try:
        with open(tmp, "w") as fh:
            fh.write(line[:REPORT_MAX_BYTES])
        os.replace(tmp, path)
    except OSError as exc:
        sys.stderr.write(f"funcd duckdb shim: the report could not be written: {exc}\n")


class _Saved:
    """What the save endpoint knows (ADR-0279 Decision 2): this engine's instance id, drawn at start; the latest
    snapshot id the last good save covers, which starts at the catalog's; the outcome of the last save attempt, so a
    call that waited behind a failed save answers its error; and the calls in flight, which a stop lets answer."""

    def __init__(self, covered: int | None) -> None:
        self.instance = secrets.token_hex(8)
        self.covered = covered
        self.attempts = 0
        self.error: Exception | None = None
        # stopping: the stop detaches the catalog; stopped: its upload of the closed catalog succeeded.
        self.stopping = False
        self.stopped = False
        self._calls = 0
        self._calls_lock = threading.Lock()

    def begin(self) -> None:
        with self._calls_lock:
            self._calls += 1

    def end(self) -> None:
        with self._calls_lock:
            self._calls -= 1

    def drain(self, seconds: float) -> None:
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            with self._calls_lock:
                if self._calls == 0:
                    return
            time.sleep(0.01)


class _Checkpointer:
    """Checkpoints the catalog to blob: a copy of its metadata database → whole-object PutObject.

    The copy is one transaction of the engine's own DuckDB, so it is consistent while commits land. The
    whole-object Put is atomic — a torn Put leaves the prior checkpoint intact. Ordering is data-before-metadata:
    DuckLake makes the Parquet durable on S3 BEFORE a commit lands, so a checkpoint never names a missing object.
    The catalog is metadata-sized (file-lists + snapshots, not row data), within the gateway's upload cap.
    Coalesced: _CommitWatcher calls checkpoint once per poll interval in which a transaction committed, or, with the
    save endpoint, each call that no save covers calls save_covering (ADR-0279) and _SaveRetry calls retry (ADR-0289);
    the SIGTERM handler calls stop once; the lock serializes them.
    """

    def __init__(self, cur: "duckdb.DuckDBPyConnection", s3, bucket: str, key: str, report: str | None = None,
                 saved: _Saved | None = None) -> None:
        self._cur = cur
        self._s3 = s3
        self._bucket = bucket
        self._key = key
        self._lock = threading.Lock()
        self._report = report
        self._failing_since: datetime | None = None
        self.saved = saved

    def checkpoint(self) -> None:
        with self._lock:
            self._checkpoint()

    def _checkpoint(self) -> None:
        """One save, under the lock. With the save endpoint it reads the latest snapshot id before the copy, which the
        copy therefore covers, and records it once the upload succeeded; a failure records nothing (ADR-0279)."""
        if self.saved is None:
            self._saved(lambda: (self._copy(METADATA), self._upload()))
            return
        covers = self._cur.execute(LATEST_SNAPSHOT).fetchone()[0]
        try:
            self._saved(lambda: (self._copy(METADATA), self._upload()))
        except Exception as exc:
            self.saved.attempts += 1
            self.saved.error = exc
            sys.stderr.write(f"funcd duckdb shim: catalog checkpoint failed: {exc}\n")
            raise
        self.saved.attempts += 1
        self.saved.error = None
        self.saved.covered = covers

    def stop_covered(self) -> int | None:
        """A save call that found the catalog detached by the stop: every commit made before the detach is in the closed
        catalog the stop uploads, so it answers that upload's snapshot once the stop's save ended, or its error
        (ADR-0279 Decision 2)."""
        with self._lock:
            if not self.saved.stopped:
                raise self.saved.error or RuntimeError("the stop's save of the closed catalog did not succeed")
            return self.saved.covered

    def save_covering(self, target: int | None) -> int | None:
        """Returns the saved snapshot id once a good save covers snapshot target: at once when the last one does,
        else after the save that ends while the call waits for the lock, or its own. Raises the error of a save that
        failed, its own or one that ended while it waited (ADR-0279 Decision 2)."""
        saved = self.saved
        if target is None or (saved.covered is not None and saved.covered >= target):
            return saved.covered
        attempts = saved.attempts
        with self._lock:
            if saved.covered is not None and saved.covered >= target:
                return saved.covered
            if saved.attempts != attempts and saved.error is not None:
                raise saved.error
            self._checkpoint()
            return saved.covered

    def retry(self, stop: threading.Event) -> None:
        """Saves when the last save failed and stop is not set, else returns: a good save that ended while it waited for
        the lock leaves nothing to retry, and a stop that began meanwhile makes its own save (ADR-0289)."""
        with self._lock:
            if self.saved.error is not None and not stop.is_set():
                self._checkpoint()

    def _saved(self, save: Callable[[], object]) -> None:
        """Runs one save and replaces the report with its outcome, keeping the first failure's time while saves fail
        (ADR-0276)."""
        try:
            save()
        except Exception as exc:
            if self._failing_since is None:
                self._failing_since = datetime.now(timezone.utc)
            _report(self._report, self._failing_since, exc)
            raise
        self._failing_since = None
        _report(self._report, None, None)

    def stop(self, serving: list[str], token: str) -> None:
        """Ends the engine's commits and uploads its last checkpoint, once the running queries drained and the watcher
        stopped (ADR-0273). After DETACH no new transaction can use the catalog, and DuckDB closes its file once the
        last transaction that used it ends, so the copy of the closed file holds every commit a client saw
        acknowledged. A query that still holds the file is cancelled (#1217). A file that never closes is left to the
        runtime's kill, which keeps the watcher's last checkpoint, as any SIGKILL does (#998)."""
        with self._lock:
            if self.saved is not None:
                self.saved.stopping = True
            self._cur.execute("DETACH lakehouse")
            cancelled: list[str] = []
            if not _closed(self._cur):
                cancelled = _cancel(self._cur, serving, token)
                while not _closed(self._cur):
                    time.sleep(0.05)
            covers: list = []

            def save_closed() -> None:
                try:
                    if self.saved is not None:
                        covers.append(self._cur.execute(CLOSED_SNAPSHOT).fetchone()[0])
                    self._copy("funcd_closed")
                finally:
                    self._cur.execute("DETACH funcd_closed")
                self._upload()

            try:
                self._saved(save_closed)
            except Exception as exc:
                if self.saved is not None:
                    self.saved.error = exc
                raise
            if self.saved is not None:
                self.saved.covered = covers[0]
                self.saved.stopped = True
        # The clients of the cancelled queries read their interrupt error first (ADR-0273). Behind the Lake PEP proxy
        # each such read starts a save call, which the drain then waits for with the other save calls this upload
        # covers, before Quack stops; the others failed or fail (ADR-0279).
        _await_cancelled(self._cur, cancelled, CANCEL_ANSWER_WAIT_SECONDS)
        if self.saved is not None:
            self.saved.drain(SAVE_ANSWER_WAIT_SECONDS)
        for uri in serving:
            self._cur.execute("CALL quack_stop(?)", [uri]).fetchall()

    def _copy(self, source: str) -> None:
        for path in (LOCAL_CHECKPOINT, LOCAL_CHECKPOINT + ".wal"):
            if os.path.exists(path):
                os.remove(path)
        self._cur.execute(f"ATTACH '{LOCAL_CHECKPOINT}' AS funcd_checkpoint")
        try:
            self._cur.execute(f"COPY FROM DATABASE {source} TO funcd_checkpoint")
        finally:
            self._cur.execute("DETACH funcd_checkpoint")

    def _upload(self) -> None:
        # whole-object PutObject — atomic; never a partial/multipart commit of the catalog.
        with open(LOCAL_CHECKPOINT, "rb") as fh:
            self._s3.put_object(Bucket=self._bucket, Key=self._key, Body=fh.read())


def _drain(cur: "duckdb.DuckDBPyConnection", seconds: float) -> None:
    """Waits up to seconds for Quack to run no query: the commit watcher keeps saving the catalog meanwhile, so a kill
    during the drain loses no more than a crash does (ADR-0273)."""
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if not cur.execute("SELECT count(*) FROM quack_active_connections() WHERE state = 'active'").fetchone()[0]:
            return
        time.sleep(0.05)


def _closed(cur: "duckdb.DuckDBPyConnection") -> bool:
    """Whether DuckDB closed the detached catalog file, which it then attaches read-only as funcd_closed. DuckDB refuses
    to attach the file again while a transaction that used it still runs."""
    try:
        cur.execute(f"ATTACH '{LOCAL_CATALOG}' AS funcd_closed (READ_ONLY)")
        return True
    except Exception as exc:  # noqa: BLE001 — only the conflict of an open file is waited out.
        if "Unique file handle conflict" not in str(exc):
            raise
        return False


def _cancel(cur: "duckdb.DuckDBPyConnection", serving: list[str], token: str) -> list[str]:
    """Cancels each query Quack still runs (#1217) and returns the connections it cancelled. quack_cancel needs a Quack
    client attachment, which the engine's own DuckDB cannot make once confined (external access off), so a separate
    instance of this process makes it; no client reaches that instance. A failed cancel leaves the stop waiting, to the
    runtime's kill."""
    active = [row[0] for row in cur.execute(
        "SELECT connection_id FROM quack_active_connections() WHERE state = 'active'").fetchall()]
    if not active:
        return []
    sys.stderr.write(f"funcd duckdb shim: cancelling {len(active)} queries that outlived the stop\n")
    cancelled: list[str] = []
    control = _connect()
    try:
        for i, uri in enumerate(serving):
            port = uri.rsplit(":", 1)[1]
            quoted = token.replace("'", "''")
            control.execute(f"ATTACH 'quack://127.0.0.1:{port}' AS funcd_engine{i} "
                            f"(TYPE quack, TOKEN '{quoted}', DISABLE_SSL true)")
            for connection in active:
                control.execute(f"SELECT * FROM quack_cancel('funcd_engine{i}', ?)", [connection]).fetchall()
                cancelled.append(connection)
    except Exception as exc:  # noqa: BLE001 — the stop still waits for the file to close.
        sys.stderr.write(f"funcd duckdb shim: the cancel failed: {exc}\n")
    finally:
        control.close()
    return cancelled


def _await_cancelled(cur: "duckdb.DuckDBPyConnection", cancelled: list[str], seconds: float) -> None:
    """Waits up to seconds until Quack lists none of the cancelled connections. Quack lists a cancelled query until its
    client has read the interrupt error; a client that polls once Quack stopped gets a connection error instead
    (ADR-0273)."""
    deadline = time.monotonic() + seconds
    while cancelled and time.monotonic() < deadline:
        listed = {row[0] for row in cur.execute("SELECT connection_id FROM quack_active_connections()").fetchall()}
        if listed.isdisjoint(cancelled):
            return
        time.sleep(0.01)


class _CommitWatcher(threading.Thread):
    """Checkpoints the catalog after each committed transaction (ADR-0086), coalesced per poll interval.

    DuckLake adds a row to ducklake_snapshot per commit; the watcher compares the latest id with the one it
    last checkpointed, starting from the id at start, so a fresh catalog nobody wrote to is not uploaded. A recovered
    catalog is saved once at start instead, so the engine's report holds a save before a commit does (ADR-0276); a
    misread 404 or a read before a restore wrote the catalog leaves a fresh one, never saved over the real one. A
    failed read or checkpoint is retried on the next tick, keeping the old id. A SIGKILL then loses at most the
    commits acknowledged within one interval plus the two saves around it, the one in progress and the next
    (ADR-0274), not every commit since the start (#998).
    """

    def __init__(self, checkpointer: _Checkpointer, latest: Callable[[], int | None], interval: float,
                 save_at_start: bool = False) -> None:
        super().__init__(name="commit-watcher", daemon=True)
        self._checkpointer = checkpointer
        self._latest = latest
        self._interval = interval
        self._done = threading.Event()
        self._seen = None if save_at_start else latest()

    def run(self) -> None:
        if self._seen is None:
            self._tick()
        while not self._done.wait(self._interval):
            if not self._tick():
                return

    def _tick(self) -> bool:
        """Saves when a commit landed since the last save, or a start-up save is pending; False once stopped."""
        try:
            latest = self._latest()
            if latest != self._seen:
                self._checkpointer.checkpoint()
                self._seen = latest
        except Exception as exc:  # noqa: BLE001 — keep watching; the next tick retries.
            if self._done.is_set():
                return False
            sys.stderr.write(f"funcd duckdb shim: catalog checkpoint failed, retrying: {exc}\n")
        return True

    def stop(self) -> None:
        self._done.set()


class _SaveRetry(threading.Thread):
    """ADR-0289 without the watcher (ADR-0279): a recovered catalog is saved once at start (ADR-0276 Decision 2), and
    every SAVE_RETRY_SECONDS a failed save, its own or a save call's, is made again, until the stop joins this thread.
    While the last save succeeded it saves nothing; a fresh catalog waits for its first commit's save."""

    def __init__(self, checkpointer: _Checkpointer, recovered: bool) -> None:
        super().__init__(name="save-retry", daemon=True)
        self._checkpointer = checkpointer
        self._recovered = recovered
        self._done = threading.Event()

    def run(self) -> None:
        if self._recovered:
            self._attempt(self._checkpointer.checkpoint)
        while not self._done.wait(SAVE_RETRY_SECONDS):
            self._attempt(lambda: self._checkpointer.retry(self._done))

    def _attempt(self, save: Callable[[], None]) -> None:
        try:
            save()
        except Exception as exc:  # noqa: BLE001 — retried; _checkpoint logged a failed save and the report shows it.
            if exc is not self._checkpointer.saved.error:
                sys.stderr.write(f"funcd duckdb shim: the save retry failed: {exc}\n")

    def stop(self) -> None:
        self._done.set()


def _serve_saves(port: int, checkpointer: _Checkpointer, con: "duckdb.DuckDBPyConnection") -> ThreadingHTTPServer:
    """The save endpoint (ADR-0279 Decision 2), which only the daemon reaches (lateral drop): POST /save answers 200
    once a good save covers the catalog's latest snapshot when the call came, 503 with the error of a failed save;
    both name this engine's instance. Each call reads on its own cursor."""
    saved = checkpointer.saved

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"
        # The headers and the body are two writes on a kept-alive connection: without TCP_NODELAY the body waits for
        # the client's delayed ACK of the headers.
        disable_nagle_algorithm = True

        def do_POST(self) -> None:  # noqa: N802 — BaseHTTPRequestHandler's name.
            if self.path != "/save":
                self._send(404, {"engine": saved.instance, "error": "not found"})
                return
            saved.begin()
            cur = None
            try:
                cur = con.cursor()
                try:
                    target = cur.execute(LATEST_SNAPSHOT).fetchone()[0]
                except Exception:
                    if not saved.stopping:
                        raise
                    self._send(200, {"engine": saved.instance, "saved": checkpointer.stop_covered()})
                    return
                self._send(200, {"engine": saved.instance, "saved": checkpointer.save_covering(target)})
            except Exception as exc:  # noqa: BLE001 — the proxy fails the answer with this error.
                self._send(503, {"engine": saved.instance, "error": str(exc).replace(chr(10), " ")[:500]})
            finally:
                if cur is not None:
                    cur.close()
                saved.end()

        def _send(self, code: int, body: dict) -> None:
            data = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, *_args: object) -> None:
            pass

    server = ThreadingHTTPServer(("0.0.0.0", port), Handler)
    threading.Thread(target=server.serve_forever, name="save-endpoint", daemon=True).start()
    return server


def _stop(drain, watcher: _CommitWatcher, watch, checkpointer: _Checkpointer, serving: list[str], token: str) -> None:
    """The graceful stop (#1198, ADR-0273). The watcher keeps saving while the running queries drain; its cursor holds
    the catalog open, which the stop waits out, so it stops and closes before the stop detaches the catalog. One more
    save then covers the commits of its last interval: a kill later in the stop loses only what completes after this
    save. With the save endpoint (ADR-0279) the watcher is the save retry (ADR-0289), and the save calls answer the
    stop's upload. The stop's own error is logged, as the engine then exits 0 whatever happened (ADR-0276)."""
    try:
        _drain(drain, STOP_WAIT_SECONDS)
        drain.close()
        watcher.stop()
        watcher.join()
        watch.close()
        try:
            checkpointer.checkpoint()
        except Exception as exc:  # noqa: BLE001 — the stop's own upload covers the same commits.
            sys.stderr.write(f"funcd duckdb shim: the save after the drain failed: {exc}\n")
        checkpointer.stop(serving, token)
    except Exception as exc:  # noqa: BLE001 — logged, then the exit the runtime expects.
        sys.stderr.write(f"funcd duckdb shim: the stop failed: {exc}\n")


def main() -> int:
    quack_port = int(_require("FUNCD_QUACK_PORT"))
    catalog_url = _require("FUNCD_DUCKLAKE_CATALOG")
    quack_token = _require("QUACK_TOKEN")  # >= 4 chars; consumers present the same token
    endpoint = os.environ.get("FUNCD_S3_ENDPOINT") or _require("AWS_ENDPOINT_URL")
    region = os.environ.get("AWS_REGION", "us-east-1")
    _require("AWS_ACCESS_KEY_ID")
    _require("AWS_SECRET_ACCESS_KEY")
    # DuckDB fills its S3 settings from these variables, and a query can read settings: take them out of the
    # environment before DuckDB starts, and pass them on only explicitly.
    access, secret = os.environ.pop("AWS_ACCESS_KEY_ID"), os.environ.pop("AWS_SECRET_ACCESS_KEY")
    bucket, key = _split_s3_url(catalog_url)
    save_interval = _save_interval(os.environ)
    data_path = os.environ.get("FUNCD_DUCKLAKE_DATA_PATH") or _data_path(catalog_url)

    s3 = _s3_client(endpoint, region, access, secret)
    con = _connect()
    _tune(con, os.environ)
    _configure(con, endpoint, region, access, secret)

    # Recover: GetObject the catalog from blob → local file → ATTACH (fresh on first deploy). The
    # Parquet DATA_PATH is the owned prefix on S3 (the F47 surface); the catalog is the local DuckDB file.
    recovered = _recover_catalog(s3, bucket, key)
    # OVERRIDE_DATA_PATH: a catalog restored under another prefix (ADR-0222 asLocation) records its old
    # DATA_PATH, which DuckLake refuses to replace otherwise; the data files are relative to it.
    # DUCKLAKE_VERSION: a new catalog is DuckLake 1.1 whatever DuckDB's default; a 1.0 catalog fails here (ADR-0263).
    con.execute(
        "ATTACH 'ducklake:" + LOCAL_CATALOG + "' AS lakehouse "
        "(DATA_PATH ?, OVERRIDE_DATA_PATH TRUE, DUCKLAKE_VERSION '1.1')",
        [data_path],
    )
    _confine(con)

    saves = con.cursor()
    watch = con.cursor()
    save_port = os.environ.get(SAVE_PORT_KEY)
    if save_port:
        # Save before answering (ADR-0279): the endpoint listens before Quack serves, and no watcher runs.
        saved = _Saved(watch.execute(LATEST_SNAPSHOT).fetchone()[0])
        checkpointer = _Checkpointer(saves, s3, bucket, key, os.environ.get(REPORT_KEY), saved)
        _serve_saves(int(save_port), checkpointer, con)
        watcher = _SaveRetry(checkpointer, recovered)
    else:
        checkpointer = _Checkpointer(saves, s3, bucket, key, os.environ.get(REPORT_KEY))
        watcher = _CommitWatcher(checkpointer, lambda: watch.execute(LATEST_SNAPSHOT).fetchone()[0], save_interval,
                                 save_at_start=recovered)
    watcher.start()
    drain = con.cursor()
    serving: list[str] = []

    # Graceful shutdown on SIGTERM/SIGINT: end the commits and checkpoint the catalog one final time (#1198).
    def _on_term(_signum, _frame):
        try:
            _stop(drain, watcher, watch, checkpointer, serving, quack_token)
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
    serve_uri = f"quack://0.0.0.0:{quack_port}"
    con.execute(
        "CALL quack_serve(?, token := ?, disable_ssl := true, allow_other_hostname := true)",
        [serve_uri, quack_token],
    ).fetchall()
    serving.append(serve_uri)

    signal.pause()  # block; the SIGTERM handler checkpoints + exits when the sandbox stops us.
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
