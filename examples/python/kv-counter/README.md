# kv-counter (Python) — durable KV via `context.kv` (ADR-0069)

A **Python** function that maintains a per-name counter in the platform's **KV service** — the Python
sibling of [`examples/js/kv-counter`](../../js/kv-counter). Each invoke reads the current count via
`context.kv`, increments it, writes it back, and returns it, so two calls return `1` then `2`:

```
client ──HTTP──▶ pycounter ──context.kv.get/put("py-counters", name)──▶ [worker-node local API, UDS]
                                                                          │ PDP-authorized Facade (ADR-0019)
                                                                          ▼
                                                                       durable KV driver (ADR-0066)
```

`context.kv` dials the **same per-sandbox socket** as `context.invoke` (ADR-0064); the platform routes
`/kv/{binding}/{key}` to the Facade with the sandbox's **namespace-scoped identity** (never client-asserted).
The example uses its own binding (`py-counters`), so it counts independently of the JS sibling.

## The API

```python
context.kv.put("py-counters", name, str(count))    # PUT  /kv/py-counters/<name>
s = context.kv.get_str("py-counters", name)        # GET  → str | None   (ADR-0070)
o = context.kv.get_json("py-counters", name)       # GET  → parsed JSON | None
b = context.kv.get("py-counters", name)            # GET  → bytes | None  (raw bytes)
context.kv.delete("py-counters", name)             # DELETE
keys = context.kv.list("py-counters", "a")         # GET  /kv/py-counters?prefix=a  (list[str])
```

## Contract (ADR-0058/0060)

The two `TypedDict`s `FuncInput` / `FuncOutput` *are* the contract. `build.py` reads them to generate
the closed JSON Schema, **bakes an eval-free `fastjsonschema` validator** into `counter.py`, and writes
`counter-{input,output}.schema.json`. Those schemas are pushed as OCI metadata
(`funcdctl push --contract-input/--contract-output`), so a malformed call is rejected (**422**) before
the handler runs — KV functions are contract-validated, not just KV-enabled.

```bash
uv run --group build python build.py   # → counter.py (baked validators) + the I/O schemas
```

## Run it locally (`funcdctl dev`)

`funcdctl dev` runs the function from source — no hand-written CRDs — printing a colored services
banner + live logs (it builds the `-tags dev` funcdctl for you):

```bash
just dev-example python/kv-counter      # gateway :3005 · S3 :3006 — override: just dev-example python/kv-counter 4000 4001
```

Invoke the gateway the banner prints (default `http://127.0.0.1:3005`). The single generic
`funcdctl.yaml` names the function after its directory (`kv-counter`); the invoke is a **CloudEvent
envelope** — `{"data": <input>}` matching the manifest's `contract.input` (`{name: string}`). POST
**twice** and the durable `context.kv` counter increments `1 → 2`:

```bash
curl -sS -XPOST http://127.0.0.1:3005/function/kv-counter \
  -H 'Content-Type: application/json' -d '{"data":{"name":"alice"}}'
# → {"name":"alice","count":1}   then, on the second POST,   {"name":"alice","count":2}
```

## Run it (the containerd lane)

Built, pushed (with its contract), and invoked alongside the JS sibling by `just lima-example-kv`: it
boots funcd in containerd mode with the durable Badger KV engine, applies both functions on their own
runtimes (`nodejs22` + `python314`), and POSTs each twice — asserting the count goes `1 → 2` (KV
persisted across invocations) on a real sandbox.

```bash
nix develop -c just lima-example-kv   # builds + deploys both kv-counter functions, then invokes them
```

## Develop

Managed with [uv](https://docs.astral.sh/uv/):

```bash
uv sync                                  # create the venv, install funcd-shim + dev tools
uv run mypy                              # strict typecheck against the funcd_shim contract
uv run ruff check                        # lint
uv run --group build python build.py     # generate the artifact + schemas
```

KV is **namespace-scoped** (a function reaches the KV in its own namespace); fine-grained per-workload
`Grant` authz is V2. The durable Badger driver is opt-in (`kvstore.engine: badger`); the default is in-memory.
