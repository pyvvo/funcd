"""catalog-quack consumer — a funcd FUNCTION that consumes the F48 Quack catalog (ADR-0086/0087/0088).

A Quack client is a local DuckDB with the `quack` extension (Quack is DuckDB-to-DuckDB, Protobuf over
HTTP — there is no separate client library). So this handler runs SQL on a deployed `CatalogService`
over Quack and returns the rows, the funcd-native way: a deployed, invokable function, not a CLI.

How a function reaches the catalog (the bindings funcd injects):
  * FUNCD_CATALOG_<ALIAS>_URL / _TOKEN — the catalog's Quack endpoint + auth token, injected from a
    `spec.catalogs` consumer binding (the follow-up consumer-binding ADR) or, until that lands, from a
    bound Secret/ConfigMap. The egress PDP authorizes the function→catalog path (binding-as-grant).

DEPLOY PREREQUISITES (see README — both are follow-ups, tracked on Project #4):
  1. A DuckDB-capable function runtime. The curated `python314`/`nodejs22` runtimes are stdlib/JS-only
     and have NO DuckDB (+ its native lib closure); only the `duckdb` engine image carries it, and its
     entrypoint is the catalog shim, not a function handler. A `python-duckdb` function runtime (the
     duckdb deps + the funcd python shim entrypoint) is needed for this handler to `import duckdb`.
  2. The consumer binding (`spec.catalogs`) that injects FUNCD_CATALOG_*_URL/_TOKEN + opens the egress
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
    ext_dir = os.environ.get("DUCKDB_EXTENSION_DIRECTORY")
    if ext_dir:
        con.execute("SET autoinstall_known_extensions=false")
        con.execute(f"SET extension_directory='{ext_dir}'")
    con.execute("INSTALL quack")
    con.execute("LOAD quack")
    rows = con.execute(
        "SELECT * FROM quack_query(?, ?, token := ?, disable_ssl := true)",
        [_quack_uri(url), sql, token],
    ).fetchall()
    context.log("catalog-quack", alias, len(rows))
    return {"rows": [list(r) for r in rows]}
