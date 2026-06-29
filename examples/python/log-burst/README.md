# funcd log-burst (Python)

A funcd function authored in **Python** that emits a **burst of ≥ 100 structured log records** through
the stdlib [`logging`](https://docs.python.org/3/library/logging.html) module, to exercise the
platform's **Path B function-log capture** (ADR-0081). It is the Python sibling of the capture demo:
the Lima e2e deploys it, invokes it once, and asserts funcd captured ≥ 100 records and persisted them
as OTLP-JSON-Lines on the blob substrate.

```
src/handler.py        # the function: handle(context, event) -> {"emitted": <count>}
tests/test_handler.py # the author's unit tests (no platform needed)
handler.yaml          # the Function manifest (runtime: python314, handler: handle)
pyproject.toml        # uv project; depends on funcd-shim for the typed contract
```

## Why `logging`, not `print`

Python has no `console` (the Node Path B hook point). The funcd Python runtime shim installs a
**`logging.Handler` on the root logger** as its Path B seam (ADR-0081), so anything emitted via
`logging.info/.warning/.error` is captured **structurally** — exact severity, the formatted message as
`body`, and any `extra={...}` fields as attributes — and streamed out over the function's side channel
to be batched and persisted host-side. A bare `print(...)` is **not** a Path B source; it falls to
**Path A** (raw stdout, coarse `INFO`). This function therefore logs everything it wants captured via
`logging.*`.

```python
import logging
log = logging.getLogger("log-burst")

def handle(context, event):
    for i in range(90):
        log.info("processing item %d", i, extra={"item": i, "phase": "scan"})
    for i in range(7):
        log.warning("slow item %d took %dms", i, 120 + i, extra={"item": i})
    for i in range(3):
        log.error("item %d failed validation", i, extra={"item": i})
    return {"emitted": 100}
```

The default burst is **90 INFO + 7 WARN + 3 ERROR = 100** records. An optional `count` in the event
raises the INFO portion (e.g. `{"data": {"count": 25}}` → 125 records) for a heavier capture run. The
function returns `{"emitted": <count>}` so a caller / e2e can compare the count it emitted against the
number funcd's host-side reader captured.

## Capture wire (host-side, for reference)

Each record becomes one NDJSON line on the side channel (ADR-0081), which funcd decodes into an OTLP
log record:

```json
{"ts":1730000000000000000,"sev":"INFO","body":"processing item 3","attrs":{"logger":"log-burst","funcName":"handle","lineno":"42","item":"3","phase":"scan"},"inv":"","trace_id":"","span_id":"","funcd.source":"logging"}
```

`sev` maps `levelno` (DEBUG·INFO·WARNING→`WARN`·ERROR·CRITICAL→`FATAL`); `funcd.source` is the literal
`"logging"` for Python (Node uses `"console"`); `attrs` are stringified (the host decodes them as
`map[string]string`).

## Develop

Managed with [uv](https://docs.astral.sh/uv/):

```bash
uv sync            # create the venv, install funcd-shim + dev tools
uv run mypy        # strict typecheck against the funcd_shim Handler contract
uv run ruff check  # lint
uv run pytest      # run the unit tests (asserts >= 100 records emitted)
```

## Deploy

The deliverable is `src/handler.py`. Push it and apply [`handler.yaml`](handler.yaml)
(`runtime: python314`, `handler: handle`):

```bash
funcd --config ../../funcdconfig.yaml &   # start the daemon (zero-infra dev config, ADR-0061)
funcdctl push src/handler.py
funcdctl apply -f handler.yaml
```

Invoke it once; the curated Python image runs the shim, which has installed the Path B
`logging.Handler`, so every `logging.*` the handler emits is captured and persisted as OTLP-JSON-Lines
in the reserved `funcd-system` namespace's blob — the input to log compaction (FEAT-0004 F53).
