"""Contract-aware build for the Python log-burst (ADR-0081/0090).

Mirrors the JS sibling's ``build.ts`` (and examples/python/kv-counter/build.py): it reads
``src/handler.py``, generates the closed JSON Schema from ``FuncInput``/``FuncOutput`` (pydantic /
TypedDict, build-time only), bakes a precompiled, eval-free ``fastjsonschema`` validator into the
runtime artifact, and writes:

  - ``handler.py``            — the runtime artifact (handler + baked ``__funcd_validate_*``)
  - ``handler.schema.json``   — for ``funcdctl push handler.py <ref> --schema handler.schema.json``
                                (one ``{"input": …, "output": …}`` doc, both sides mandatory — ADR-0090)

log-burst's PURPOSE is the log burst, but a push now requires a contract (ADR-0090), and handler.py
already declares a real I/O shape (an optional ``{count}`` in, ``{emitted}`` out — the count the e2e
reads back), so the honest contract is that typed shape (a void ``{"type":"null"}`` output would
reject the ``{emitted}`` body at runtime). Run from this dir with the shim toolchain resolvable:

    uv run --group build python build.py
"""

from __future__ import annotations

import json
from pathlib import Path

from funcd_shim.build import build

HERE = Path(__file__).parent
result = build((HERE / "src" / "handler.py").read_text())

(HERE / "handler.py").write_text(result.runtime_source)
# ADR-0090: one mandatory `--schema` doc — both sides always present (a void side is {"type":"null"}).
contract = {"input": result.input_schema, "output": result.output_schema}
(HERE / "handler.schema.json").write_text(json.dumps(contract, indent=2) + "\n")
print("built handler.py (+ baked contract validators + handler.schema.json)")
