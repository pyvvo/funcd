"""Flatten funcd dev OTLP logs into one merged table.

The dev log files are JSONL with one OTLP `resourceLogs` envelope per line
(resourceLogs -> scopeLogs -> logRecords), one file per function/replica. Generic OTLP/log viewers
don't drill into that nesting, so this flattens every file with DuckDB into columns
(time, function, severity, body, inv, trace_id) and merges them sorted by time.

The path may be a directory (scans <dir>/**/*.jsonl), a single file, or a glob.

Usage:
  python scripts/otlp-logs.py <dir|file|glob>                 # print one merged table
  python scripts/otlp-logs.py <dir|file|glob> --parquet out.parquet   # write a flat, typed parquet
"""
from __future__ import annotations

import argparse
import os

import duckdb


def _source(path: str) -> str:
    """A directory scans all JSONL under it; a file or glob is used as-is."""
    if os.path.isdir(path):
        return os.path.join(path, "**", "*.jsonl")
    return path


def _flatten_sql(src: str, cols: str) -> str:
    return f"""
    WITH rl AS (SELECT unnest(resourceLogs) AS r FROM read_json_auto('{src}', filename = true)),
         sl AS (SELECT r.resource.attributes AS atts, unnest(r.scopeLogs) AS s FROM rl),
         lr AS (SELECT atts, unnest(s.logRecords) AS rec FROM sl),
         flat AS (
           SELECT
             to_timestamp(CAST(rec.timeUnixNano AS BIGINT) / 1e9)                 AS time,
             list_filter(atts, a -> a.key = 'function')[1].value.stringValue      AS function,
             rec.severityText                                                     AS severity,
             rec.body.stringValue                                                 AS body,
             list_filter(rec.attributes, a -> a.key = 'inv')[1].value.stringValue AS inv,
             rec.traceId                                                          AS trace_id
           FROM lr
         )
    SELECT {cols} FROM flat ORDER BY time
    """


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("path", help="a dir (scans **/*.jsonl), a single file, or a glob")
    ap.add_argument("--parquet", metavar="OUT", default=None)
    args = ap.parse_args()
    src = _source(args.path)
    if args.parquet:
        duckdb.sql(_flatten_sql(src, "*")).write_parquet(args.parquet)
        print(f"wrote {args.parquet}")
    else:
        duckdb.sql(_flatten_sql(src, "time, function, severity, body")).show(max_width=200, max_rows=2000)


if __name__ == "__main__":
    main()
