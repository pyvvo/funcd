"""Contract-aware build for the Python kv-counter (ADR-0058/0060/0069).

Mirrors the JS sibling's ``build.ts``: it reads ``src/counter.py``, generates the closed JSON Schema
from ``FuncInput``/``FuncOutput`` (pydantic, build-time only), bakes a precompiled, eval-free
``fastjsonschema`` validator into the runtime artifact, and writes:

  - ``counter.py``            — the runtime artifact (handler + baked ``__funcd_validate_*``)
  - ``counter.schema.json``   — for ``funcdctl push --schema counter.schema.json`` (one
                                ``{"input": …, "output": …}`` doc, both sides mandatory — ADR-0090)

So the contract is *enforced* (bad input → 422, bad output → 500), same as the JS sibling and
``examples/python/hello-world``. Run from this dir with the shim toolchain resolvable:

    uv run --group build python build.py
"""

from __future__ import annotations

import json
from pathlib import Path

from funcd_shim.build import build

HERE = Path(__file__).parent
result = build((HERE / "src" / "counter.py").read_text())

(HERE / "counter.py").write_text(result.runtime_source)
# ADR-0090: one mandatory `--schema` doc — both sides always present (a void side is {"type":"null"}).
contract = {"input": result.input_schema, "output": result.output_schema}
(HERE / "counter.schema.json").write_text(json.dumps(contract, indent=2) + "\n")
print("built counter.py (+ baked contract validators + counter.schema.json)")
