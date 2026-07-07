# ADR-0070: context.kv typed read accessors — bytes / text / JSON, client-side

- **Status**: Implemented (2026-06-22)
- **Date**: 2026-06-22 (judged 2026-06-22 — sound, additive, no Blockers/Majors; the two `getJSON`/`get_json`
  `unknown`/`object` return types are honest for untyped wire bytes)
- **Deciders**: green-0-rabbit
- **Tags**: kv, shim, ergonomics, dx
- **Realizes**: [FEAT-0001/F39](../feat/0001-feat-v1.1.md) (context.kv typed read accessors)
- **Relates to**: [ADR-0069](0069-kv-data-plane.md) (defines `context.kv` `get/put/del/list`; this **adds
  read accessors** to the client, not the wire)

## Context & Need

`context.kv.get` (ADR-0069) returns the raw stored bytes (`Uint8Array | null` / `bytes | None`) — correct
for the wire and binary-safe, but it pushes a decode dance onto every caller of the common case (a string
or a JSON value). The kv-counter examples showed it: reading a counter meant `Number(new
TextDecoder().decode(cur))` in JS and `int(cur.decode())` in Python — noise around a one-line intent. KV
**stores opaque bytes** (that stays); the missing piece is a thin **client-side decoder** so the caller
states the value's type once instead of decoding by hand. `put` already takes `string | bytes`, so only
the read side is asymmetric.

## Scenarios

- **scenario: get-text-decodes** — Given a key holding UTF-8 `"5"`, When the function calls
  `getText`/`get_str`, Then it gets the string `"5"` (not bytes).
- **scenario: get-json-parses** — Given a key holding `{"n":5}`, When the function calls
  `getJSON`/`get_json`, Then it gets the parsed object/dict.
- **scenario: get-typed-missing-is-null** — Given a missing key, When the function calls
  `getText`/`getJSON` (`get_str`/`get_json`), Then it gets `null`/`None` (a miss is not an error).
- **scenario: get-bytes-unchanged** — Given any key, When the function calls `get`, Then it still returns
  the raw `Uint8Array | null` / `bytes | None` (the binary primitive is untouched).

## Scope

**In**: additive, read-only convenience accessors on the **shim KV client** in both runtimes — `getText` +
`getJSON` (Node), `get_str` + `get_json` (Python) — each a thin wrapper over `get`. Update the kv-counter
examples to use them.

**Out**: any change to the **wire** (`/kv/{binding}/{key}` stays bytes), the `kvstore.KV` port, the Facade,
or `put` (it already accepts `string`/`str`; a structured-`put` overload is a possible later follow-up,
deliberately not bundled here — keeps this one altitude: *read* ergonomics). No new methods on `get`'s
return type (no wrapper object — accessors keep `get` itself unchanged).

## Constraints & Decision drivers

- **Additive, non-breaking** — `get/put/del/list` keep their exact ADR-0069 signatures; this only *adds*.
- **No new deps** — text via the platform's `TextDecoder`/`bytes.decode`, JSON via stdlib `JSON.parse` /
  `json.loads`.
- **Client-only** — the decoder lives in the shim KV client; the wire, port, and Facade are untouched.
- **Symmetric across runtimes** — the same three-tier shape (bytes / text / JSON) in Node and Python.

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| **Typed sibling accessors** (`getText`/`getJSON`, `get_str`/`get_json`) ✅ | Mirrors `put`'s `string\|bytes`; explicit; type-safe per call; additive | Method count grows by two — accepted; clearest, no breaking change. **Chosen** (decider-selected) |
| **Value wrapper** — `get()` → `{bytes, text(), json()}` | One read method, decode on demand | **Breaks** `get`'s return type (ADR-0069) and adds a wrapper object to every read; rejected |
| **Typed binding views** — `kv.text('b').get(k)` | Clean when a binding is mono-typed | More surface; assumes one type per binding; defer as a possible future sugar, not now |
| **Encoding param** — `get(b,k,'text')` | One method | Union return type, worse in TS; reads less clearly than a named method; rejected |

## Decision

Add, on the shim KV client only, two read accessors per runtime, each a thin wrapper over `get`:

- **Node** (`shim/nodejs/src/kv.ts`): `getText(binding,key): Promise<string|null>` (UTF-8 decode);
  `getJSON<T=unknown>(binding,key): Promise<T|null>` (decode + `JSON.parse`).
- **Python** (`shim/python/src/funcd_shim/kv.py`): `get_str(binding,key) -> str | None` (UTF-8 decode);
  `get_json(binding,key) -> object | None` (decode + `json.loads`).

A miss (`get` → null/None) propagates as null/None — never an error. `get` keeps returning raw bytes. The
kv-counter examples use the text accessor: `Number(await ctx.kv.getText('counters', name) ?? 0)` /
`int(context.kv.get_str("py-counters", name) or 0)`.

## Temporary workarounds

None.

## Contracts

```ts
// shim/nodejs/src/kv.ts — additive members on KVClient (get/put/del/list unchanged).
export interface KVClient {
  get(binding: string, key: string): Promise<Uint8Array | null>;
  getText(binding: string, key: string): Promise<string | null>;        // UTF-8 decode
  getJSON<T = unknown>(binding: string, key: string): Promise<T | null>; // decode + JSON.parse
  put(binding: string, key: string, value: Uint8Array | string): Promise<void>;
  del(binding: string, key: string): Promise<void>;
  list(binding: string, prefix?: string): Promise<string[]>;
}
```

```python
# shim/python/src/funcd_shim/kv.py — additive methods on KVClient.
class KVClient:
    def get(self, binding: str, key: str) -> bytes | None: ...
    def get_str(self, binding: str, key: str) -> str | None: ...      # UTF-8 decode
    def get_json(self, binding: str, key: str) -> object | None: ...  # decode + json.loads
    def put(self, binding: str, key: str, value: bytes | str) -> None: ...
    def delete(self, binding: str, key: str) -> None: ...
    def list(self, binding: str, prefix: str = "") -> list[str]: ...
```

| consumes | exposes |
|---|---|
| the existing `get` (HTTP-over-UDS, ADR-0069) | `getText`/`getJSON` (Node), `get_str`/`get_json` (Python) |
| `TextDecoder` / `bytes.decode`; `JSON.parse` / `json.loads` (no new dep) | text + JSON reads, miss ⇒ null/None |

## Implementation plan

**Files**: `shim/nodejs/src/kv.ts` (+ regenerate `shim.mjs`/`pool.mjs` via `just build-shim`);
`shim/python/src/funcd_shim/kv.py`; the kv-counter handlers (`examples/js/kv-counter/src/counter.ts`,
`examples/python/kv-counter/src/counter.py`) to use the text accessor; their READMEs.

**go.mod / deps**: none (shim-only).

**Test plan** — one named test per scenario, in each shim, over a minimal fake UDS server:
- Node `shim/nodejs/test/kv.test.ts`: `get-text-decodes`, `get-json-parses`, `get-typed-missing-is-null`,
  `get-bytes-unchanged`.
- Python `shim/python/tests/test_kv.py`: the same four, against a `socketserver` UDS handler.

**Definition of done**: both shims green (`just build-shim`: typecheck + node tests + esbuild; `ruff` +
`mypy` + `pytest`); the four scenarios pass in each runtime; `get/put/del/list` unchanged; no new dep; the
examples read via the text accessor; no identity/path leak.

## Review checklist

- [ ] `getText`/`getJSON` (Node) + `get_str`/`get_json` (Python) added; `get/put/del/list` unchanged.
- [ ] A miss returns null/None (not an error) for the typed accessors.
- [ ] `get` still returns raw bytes (binary primitive untouched).
- [ ] Four named scenario tests pass in **each** shim over a fake UDS server.
- [ ] `shim.mjs`/`pool.mjs` regenerated from `kv.ts`; both examples use the text accessor.
- [ ] No new dependency; no wire/port/Facade change; no identity/path leak.

## Consequences

**Positive**: the common read (text/JSON) is a one-liner symmetric with `put`; binary stays first-class via
`get`; zero new deps; no breaking change.
**Negative (accepted)**: two extra methods per runtime (surface growth); `getJSON`/`get_json` return
`unknown`/`object` (the caller asserts the type — honest, since the value is untyped bytes on the wire).
**Neutral**: a structured-`put` overload and per-binding typed views remain open as future sugar.

## Open questions

- **Structured `put`** (accept an object → JSON-encode) — a small symmetric follow-up if write-side friction
  shows up; out of scope here.

## References

- [ADR-0069](0069-kv-data-plane.md) — the `context.kv` surface this extends.
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F39.
