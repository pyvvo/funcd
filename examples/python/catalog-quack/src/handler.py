"""catalog-quack consumer — a funcd FUNCTION that consumes the F48 Quack catalog (ADR-0086/0087/0088).

A Quack client is a local DuckDB with the `quack` extension (Quack is DuckDB-to-DuckDB, Protobuf over
HTTP — there is no separate client library). So this handler runs SQL on a deployed `CatalogService`
over Quack and returns the rows, the funcd-native way: a deployed, invokable function, not a CLI.

How a function reaches the catalog (the bindings funcd injects):
  * FUNCD_CATALOG_<ALIAS>_URL / _TOKEN — the catalog's Quack endpoint + auth token, injected from a
    `spec.catalogs` consumer binding (the follow-up consumer-binding ADR) or, until that lands, from a
    bound Secret/ConfigMap. The egress PDP authorizes the function→catalog path (binding-as-grant).

DuckDB travels in the artifact (ADR-0089): this handler runs on the STOCK curated `python314` runtime.
The `duckdb` wheel + the `quack`/`httpfs` extensions are vendored into a deployment-package BUNDLE
(`build.py` → `bundle/`), which `funcdctl push --entry handler.py` ships as one OCI layer. funcd sets
PYTHONPATH + FUNCD_BUNDLE_DIR so `import duckdb` and the offline `duckdb-ext/` extensions resolve with no
runtime changes — no `python-duckdb` image needed.

DEPLOY PREREQUISITE (a follow-up, tracked on Project #4):
  * The consumer binding (`spec.catalogs`) that injects FUNCD_CATALOG_*_URL/_TOKEN + opens the egress
    grant. Until it exists, the URL/TOKEN come from a Secret/ConfigMap the function binds.
"""

from __future__ import annotations

import os
from typing import TypedDict

import duckdb  # the client IS a DuckDB; provided by the (future) duckdb-capable function runtime
from funcd_shim import CloudEvent, FunctionContext


class FuncInput(TypedDict, total=False):
    """The event payload: the SQL to run, and which catalog alias to run it against."""

    sql: str
    catalog: str  # the spec.catalogs alias (defaults to "lake")


class FuncOutput(TypedDict):
    """The 200 body: the query's rows (each a list of column values)."""

    rows: list[list[object]]


def _quack_uri(endpoint: str) -> str:
    """Normalize a catalog endpoint to a quack:// RPC URI."""
    return endpoint if endpoint.startswith("quack://") else "quack://" + endpoint


def handle(context: FunctionContext, event: CloudEvent[FuncInput]) -> FuncOutput:
    """Run the requested SQL on the bound catalog over Quack and return the rows."""
    data = event.get("data") or {}
    alias = (data.get("catalog") or "lake").upper()
    sql = data.get("sql") or "SELECT 42 AS answer"

    # Injected by the consumer binding (the funcd-native shape) — the catalog's Quack endpoint + token.
    url = os.environ[f"FUNCD_CATALOG_{alias}_URL"]
    token = os.environ[f"FUNCD_CATALOG_{alias}_TOKEN"]

    con = duckdb.connect()
    # The bundle ships the quack/httpfs extensions in <FUNCD_BUNDLE_DIR>/duckdb-ext (ADR-0089); load
    # them OFFLINE from there (no network install). Falls back to DUCKDB_EXTENSION_DIRECTORY, else the
    # DuckDB default. FUNCD_BUNDLE_DIR is set by funcd for both a bundle and a single-file artifact.
    bundle_dir = os.environ.get("FUNCD_BUNDLE_DIR")
    ext_dir = os.environ.get("DUCKDB_EXTENSION_DIRECTORY")
    if not ext_dir and bundle_dir:
        candidate = os.path.join(bundle_dir, "duckdb-ext")
        if os.path.isdir(candidate):
            ext_dir = candidate
    if ext_dir:
        con.execute("SET autoinstall_known_extensions=false")
        con.execute("SET autoload_known_extensions=false")
        con.execute(f"SET extension_directory='{ext_dir}'")
    con.execute("LOAD quack")
    rows = con.execute(
        "SELECT * FROM quack_query(?, ?, token := ?, disable_ssl := true)",
        [_quack_uri(url), sql, token],
    ).fetchall()
    context.log("catalog-quack", alias, len(rows))
    return {"rows": [list(r) for r in rows]}
