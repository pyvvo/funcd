"""kv-counter authored in Python (ADR-0069) with a typed I/O contract (ADR-0058).

The Python sibling of ``examples/js/kv-counter``: each invoke reads a per-name counter from the KV
binding ``py-counters`` via ``context.kv``, increments it, writes it back, and returns it — so two
calls return ``1`` then ``2``. It proves the function-facing **durable-KV** path from a Python
function: ``context.kv`` → worker-node local API (UDS) → PDP-authorized Facade (ADR-0019) → durable
Badger driver (ADR-0066).

The I/O contract is the two ``TypedDict``\\s ``FuncInput`` / ``FuncOutput``. The push build reads
them to generate a closed JSON Schema and bakes a precompiled, eval-free ``fastjsonschema`` validator
into the artifact (ADR-0058/0060) — so a bad-shaped event returns **422** before ``handle`` runs and
a bad return is a **500**, exactly like the JS sibling.
"""

from typing import TypedDict

from funcd_shim import CloudEvent, FunctionContext


class FuncInput(TypedDict):
    """The event payload — a closed record carrying the counter name to increment."""

    name: str


class FuncOutput(TypedDict):
    """The 200 body — the name and its new count."""

    name: str
    count: int


def handle(context: FunctionContext, event: CloudEvent[FuncInput]) -> FuncOutput:
    """Increment the per-name counter in the ``py-counters`` KV binding and return it."""
    # The shim validated event["data"] against FuncInput (422 otherwise), so it is present here.
    assert "data" in event
    name = event["data"]["name"]
    count = int(context.kv.get_str("py-counters", name) or 0) + 1
    context.kv.put("py-counters", name, str(count))
    context.log("kv-counter", name, count)
    return {"name": name, "count": count}
