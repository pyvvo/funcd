"""Run in lake-reader's container by the duckdb lane, never failing it (ADR-0262): records whether the consumer's
DuckDB 2.0 can ATTACH its bound Lake over Quack through the PEP proxy, with its own token, and query it."""

import os
import sys

sys.path.insert(0, os.environ["FUNCD_BUNDLE_DIR"])

import duckdb  # noqa: E402 - the consumer's bundled duckdb, on the path from the line above

con = duckdb.connect()
con.execute(f"SET extension_directories=['{os.environ['FUNCD_BUNDLE_DIR']}/duckdb-ext']")
con.execute("LOAD quack")
token = os.environ["FUNCD_LAKE_LAKE_TOKEN"].replace("'", "''")
for label, options in (("ATTACH", ""), ("ATTACH+DISABLE_SSL", ", DISABLE_SSL true")):
    try:
        con.execute(f"ATTACH 'quack:{os.environ['FUNCD_LAKE_LAKE_URL']}' AS qk (TOKEN '{token}'{options})")
        print(f"{label}=ok", con.execute("SELECT n FROM qk.lakehouse.issue998").fetchall())
        break
    except duckdb.Error as err:
        print(f"{label}=failed:", str(err).splitlines()[0])
