"""hello-world funcd function authored in Python (ADR-0049), with a typed I/O contract (ADR-0058).

Typed against the ``funcd_shim`` contract, so ``context``, the CloudEvent ``event``, and the
return type are checked by ``uv run mypy``. ``funcdcli push`` ships this ``.py`` as the artifact;
the export name (``handle``) is what ``FUNCD_HANDLER`` resolves.

The I/O contract is the two ``TypedDict``\\s ``FuncInput`` / ``FuncOutput``. A ``TypedDict`` is the
runtime-honest choice: at runtime ``event["data"]`` is a *plain dict* (exactly what the shim hands
you), while the type still lets ``CloudEvent[FuncInput]`` give you typed-key autocomplete. The push
build reads these types to generate a JSON Schema (pydantic, build-time only) and bakes a
precompiled fastjsonschema validator into the artifact — there is no validator to hand-write here,
and no pydantic at runtime (so validation also works inside the subinterpreter pool, ADR-0050/0060).

Note: the contract module does **not** use ``from __future__ import annotations`` — the build reads
the field *types* (not their string forms) to generate the schema, so the annotations must stay
real. The build re-adds the future-import to the runtime artifact itself.
"""

from typing import TypedDict

from funcd_shim import CloudEvent, FunctionContext


class FuncInput(TypedDict):
    """The event payload this function accepts — the CloudEvent ``data``.

    A *closed record* in the supported profile. The shim validates ``event["data"]`` against the
    schema generated from this type **before** ``handle`` runs, so a bad-shaped event returns
    **422** and never reaches this code; inside ``handle`` the input is already valid.
    """

    name: str


class FuncOutput(TypedDict):
    """What the function returns — the 200 JSON body.

    The build bakes an *output* validator too; a return that doesn't match is a **500**. Returning
    ``None`` instead would be a **204**. See the README for the ``Json`` escape hatch and a
    discriminated-union output.
    """

    greeting: str


def handle(context: FunctionContext, event: CloudEvent[FuncInput]) -> FuncOutput:
    """Greet by name."""
    data = event["data"]  # validated against FuncInput → present & well-shaped
    context.log("greeting", data["name"])
    return {"greeting": f"Hello, {data['name']}."}
