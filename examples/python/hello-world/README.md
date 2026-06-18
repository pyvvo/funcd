# funcd hello-world (Python)

A minimal funcd function authored in **Python**, typed against the `funcd_shim` contract
(ADR-0049). The Python sibling of [`examples/js/hello-world`](../../js/hello-world) — same
`handle(context, event)` contract, same JTD `event_schema`, validated identically by the
platform's Python runtime shim.

```
src/handler.py        # the function: handle(context, event) + an event_schema (JTD/RFC 8927)
tests/test_handler.py # the author's unit tests (no platform needed)
pyproject.toml        # uv project; depends on funcd-shim for the typed contract + validator
```

## Develop

Managed with [uv](https://docs.astral.sh/uv/):

```bash
uv sync            # create the venv, install funcd-shim + dev tools
uv run mypy        # strict typecheck against the funcd_shim Handler contract
uv run ruff check  # lint
uv run pytest      # run the unit tests
```

## Deploy

The deliverable is `src/handler.py` itself (no build step — Python ships its stdlib, and the
contract `event_schema` lives in the module). Push it and apply a `Function` with
`runtime: python312` and `handler: handle`:

```bash
funcdcli push src/handler.py
funcdcli apply -f function.yaml   # spec.runtime: python312, spec.handler: handle
```

The platform's curated Python image runs the shim, which loads this module, validates each
event's `data` against `event_schema`, and invokes `handle` — returning the dict as the 200 JSON
body. A mismatched event is rejected **422** before `handle` runs.
