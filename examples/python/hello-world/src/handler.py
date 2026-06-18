"""hello-world funcd function authored in Python (ADR-0049).

Typed against the ``funcd_shim`` contract, so ``context``, the CloudEvent ``event``, and the
return type are checked by ``uv run mypy``. ``funcdcli push`` ships this ``.py`` as the artifact;
the export name (``handle``) is what ``FUNCD_HANDLER`` resolves.
"""

from __future__ import annotations

from typing import Any

from funcd_shim import CloudEvent, FunctionContext

# The event-data contract (JSON Type Definition, RFC 8927). The shim validates ``event.data``
# against it before ``handle`` runs, so a bad-shaped event returns 422 and never reaches this
# code. One neutral schema, reusable across runtimes (this is the same shape the JS example uses).
event_schema: dict[str, Any] = {
    "optionalProperties": {"hello": {"type": "string"}},
}


def handle(context: FunctionContext, event: CloudEvent) -> dict[str, Any]:
    """Echo the event payload back as the 200 JSON body."""
    context.log("handling event:", event.get("data"))
    return {"echoed": event.get("data"), "by": "funcd"}
