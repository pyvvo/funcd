"""Run in the 256Mi engine's container by the duckdb lane (ADR-0263 Decision 8): the image's DuckDB, configured and
confined by the image's own shim, spills a query larger than its memory to an allowed temp directory. memory_limit is
set here because the engine cannot see its container's limit: it has no cgroup view."""

import os
import sys

sys.path.insert(0, "/opt/funcd")

import shim  # noqa: E402 - the image's shim, on the path from the line above

shim.LOCAL_TMP = "/var/funcd/ducklake/tmp-spill-probe"
con = shim._connect()
con.execute("SET memory_limit='64MB'")
con.execute("SET threads=1")
endpoint = os.environ.get("FUNCD_S3_ENDPOINT") or os.environ["AWS_ENDPOINT_URL"]
shim._configure(con, endpoint, os.environ.get("AWS_REGION", "us-east-1"))
shim._confine(con)
window = "SELECT max(x) FROM (SELECT i, lead(i) OVER (ORDER BY hash(i)) AS x FROM range(20000000) t(i))"
spilled = con.execute(window).fetchone()[0]
limit = con.execute("SELECT current_setting('memory_limit')").fetchone()[0]
print(f"SPILLED={spilled} MEMORY_LIMIT={limit}")
