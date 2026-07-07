# ADR-0070 implementation review — context.kv typed read accessors

- **ADR**: [0070](../adr/0070-kv-client-typed-read-accessors.md) · **phase**: implementation · **model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met, no Blockers/Majors) · 2026-06-22

## Verification (evidence)

| check | result |
|---|---|
| Node shim (`just build-shim`: typecheck + tests + esbuild) | **pass** — 26 tests (was 22; +4 KV) |
| Python shim (`ruff` + `mypy` + `pytest`) | **pass** — 43 tests (was 39; +4 KV) |
| `shim.mjs`/`pool.mjs` regenerated from `kv.ts` | yes |
| in-process KV e2e (JS handler reads via `getText`) | **pass** (count 1→2) |
| new deps | **none** (stdlib decode + JSON) |

## Scenarios → tests (named, passing, in BOTH shims)

- `get-text-decodes` · `get-json-parses` · `get-typed-missing-is-null` · `get-bytes-unchanged` —
  `shim/nodejs/test/kv.test.ts` (4) + `shim/python/tests/test_kv.py` (4), each over a minimal fake UDS
  worker-node local API.

## ✅ Verified correct (keep)

- **Additive, non-breaking** — `get/put/del/list` keep their exact ADR-0069 signatures; only `getText`/
  `getJSON` (Node) and `get_str`/`get_json` (Python) are added, each a thin wrapper over `get`.
- **`get` still returns raw bytes** — the binary primitive is untouched (`get-bytes-unchanged`).
- **A miss is null/None, not an error** — the typed accessors propagate the 404→null/None of `get`.
- **Symmetric across runtimes** — same bytes/text/JSON shape in both shims; both examples read via the text
  accessor, dropping the hand-rolled `TextDecoder`/`.decode()` noise.
- **Client-only** — the wire (`/kv/{binding}/{key}`), the `kvstore.KV` port, and the Facade are untouched.
- **Hygiene** — no new dep; no identity/path leak.

## Findings
None (Blocker/Major/Minor).

## DoD
ADR Review-checklist: 6/6 satisfied (accessors added + `get/put/del/list` unchanged; miss→null/None; `get`
raw bytes; four named scenario tests in each shim; `mjs` regenerated + examples use the text accessor; no new
dep / no wire-port-Facade change / no leak).

## Recommendation
**pass** — `Reviewing → Implemented`. Done.
