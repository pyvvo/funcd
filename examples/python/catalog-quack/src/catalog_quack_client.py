#!/usr/bin/env python3
"""A real Quack consumer client for the F48 catalog (examples/python/catalog-quack, ADR-0086/0087/0088).

A Quack client is just a local DuckDB with the `quack` extension loaded — there is no separate
JS/Python Quack protocol library (Quack is DuckDB-to-DuckDB, Protobuf over HTTP). This module connects
to a deployed CatalogService's Quack endpoint and runs SQL on the remote DuckLake catalog. It is the
shape both an in-platform consumer (run via the curated `duckdb` image) and an external consumer (run
on any host with `uv sync` / `pip install duckdb`) take.

Run it:
    uv run catalog-quack-client --endpoint <host:port|quack://host:port> --token <quack-token> --sql "<SQL>"
or via env: FUNCD_CATALOG_URL / FUNCD_CATALOG_TOKEN (the shape funcd would inject for a bound consumer).

Notes:
- The Quack client MUST present a token (verified: a token-less client fails "Could not find a Quack
  authentication token"); the catalog serves with QUACK_TOKEN, the client presents the same.
- `quack_query(uri, sql, token, disable_ssl)` is the verified RPC; the token is bound via a `?`
  placeholder (injection-safe). `--ssl` flips disable_ssl off (TLS terminated at the ingress for an
  external consumer; plain HTTP for an in-platform one).
"""

from __future__ import annotations

import argparse
import os
import sys

import duckdb  # the client IS a DuckDB; `pip install duckdb` (external) or the curated image (in-platform)


def quack_uri(endpoint: str) -> str:
    """Normalize a catalog endpoint to a quack:// RPC URI (a bare host:port gets the quack:// scheme)."""
    return endpoint if endpoint.startswith("quack://") else "quack://" + endpoint


def query(endpoint: str, token: str, sql: str, *, use_ssl: bool = False) -> list[tuple]:
    """Run `sql` on the remote catalog over Quack and return the rows."""
    uri = quack_uri(endpoint)
    con = duckdb.connect()
    # In the curated image the extensions are pre-installed (offline); on a plain host LOAD will
    # autoinstall `quack` from the DuckDB extension repo on first use.
    ext_dir = os.environ.get("DUCKDB_EXTENSION_DIRECTORY")
    if ext_dir:
        con.execute("SET autoinstall_known_extensions=false")
        con.execute(f"SET extension_directory='{ext_dir}'")
    con.execute("INSTALL quack")
    con.execute("LOAD quack")
    return con.execute(
        "SELECT * FROM quack_query(?, ?, token := ?, disable_ssl := ?)",
        [uri, sql, token, not use_ssl],
    ).fetchall()


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="Quack consumer client for the F48 catalog")
    ap.add_argument("--endpoint", default=os.environ.get("FUNCD_CATALOG_URL"),
                    help="the catalog's Quack endpoint (host:port or quack://host:port)")
    ap.add_argument("--token", default=os.environ.get("FUNCD_CATALOG_TOKEN"),
                    help="the Quack auth token (the catalog's QUACK_TOKEN)")
    ap.add_argument("--sql", default="SELECT 42 AS answer", help="the SQL to run on the catalog")
    ap.add_argument("--ssl", action="store_true",
                    help="use TLS (default: plain HTTP — TLS is terminated at the ingress)")
    args = ap.parse_args(argv)
    if not args.endpoint or not args.token:
        sys.stderr.write("client.py: --endpoint and --token (or FUNCD_CATALOG_URL/_TOKEN) are required\n")
        return 2
    rows = query(args.endpoint, args.token, args.sql, use_ssl=args.ssl)
    for r in rows:
        print(r)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
