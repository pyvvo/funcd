"""Contract-aware build for the Python kv-counter (ADR-0058/0060/0069).

Mirrors the JS sibling's ``build.ts``: it reads ``src/counter.py``, generates the closed JSON Schema
from ``FuncInput``/``FuncOutput`` (pydantic, build-time only), bakes a precompiled, eval-free
``fastjsonschema`` validator into the runtime artifact, and writes:

  - ``counter.py``                     — the runtime artifact (handler + baked ``__funcd_validate_*``)
  - ``counter-input.schema.json``      — for ``funcdctl push --contract-input``
  - ``counter-output.schema.json``     — for ``funcdctl push --contract-output``

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
if result.input_schema is not None:
    (HERE / "counter-input.schema.json").write_text(json.dumps(result.input_schema, indent=2) + "\n")
if result.output_schema is not None:
    (HERE / "counter-output.schema.json").write_text(json.dumps(result.output_schema, indent=2) + "\n")
print("built counter.py (+ baked contract validators + schemas)")
