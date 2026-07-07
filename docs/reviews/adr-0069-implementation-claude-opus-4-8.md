# ADR-0069 implementation review — KV data-plane (functions can call KV)

- **ADR**: [0069](../adr/0069-kv-data-plane.md) · **phase**: implementation · **model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met, no Blockers/Majors) · 2026-06-22

## Verification (evidence)

| check | result |
|---|---|
| `CGO_ENABLED=0 go build ./...` | OK |
| `go tool golangci-lint run` (touched pkgs) | **0 issues** |
| Go KV scenario tests + e2e | **pass** |
| Node shim (`just build-shim`: typecheck + 22 tests + esbuild) | **pass** |
| Python shim (`ruff` + `mypy` + `pytest`) | **pass** (39 tests) |

## Scenarios → tests (named, passing)

- `TestScenarioKVPutGetRoundtrip` · `TestScenarioKVTenancyIsolation` · `TestScenarioKVAuthzDenied` (403) ·
  `TestScenarioKVListPrefix` — the local-API `/kv` routes through the real Facade + memory driver.
- `TestScenarioE2EKVCounterViaContextKV` — the **full path**: the kv-counter function reads/increments a
  counter via `context.kv` across two POSTs → count **1 → 2** (handler → local-API UDS → PDP Facade → driver).

## ✅ Verified correct (keep)

- **Local-API KV routes** ([internal/workernode/local/kv.go](../../internal/workernode/local/kv.go)) —
  `GET/PUT/DELETE /kv/{binding}/{key...}` + list; RFC 9457 errors; `{key...}` so hierarchical keys work.
- **Identity is connection-scoped** — `sandboxIdentity(caller)` (namespace-scoped, V1.1) is derived from the
  fixed `Ref`; the handler never reads a caller from the request (proven by tenancy-isolation).
- **Facade wiring** — the platform builds `kv.NewFacade` with the `WithKVStore`-injected driver (in-memory
  default), passes it to the local-API Manager, and closes it on shutdown — resolving ADR-0066's deferred
  facade-selection. `kvstore.engine`/`dataDir` config + `buildKVStore` select memory|Badger.
- **Socket provisioning generalized** — every function gets the local-API socket; the link-as-grant invoke
  check stays at resolve time (fn-to-fn tests still green).
- **`context.kv` in both shims** — Node (`kv.ts`, regenerated `shim.mjs`/`pool.mjs`) + Python (`kv.py`),
  stdlib-only, mirroring `context.invoke`.
- **Example** — `examples/js/kv-counter` is built + run by the e2e (the KV analogue of fn-to-fn).
- **Hygiene** — no `any` in the e2e (typed struct after the forbidigo catch); no identity/path leak; idempotent
  driver Close.

## Findings
None (Blocker/Major/Minor).

## DoD
ADR Review-checklist items: 7/7 satisfied (routes, connection-scoped identity, facade construct+close, socket
for links-or-KV, context.kv in both shims, the kv-counter example + e2e, Facade logic unchanged).

## Recommendation
**pass** — `Reviewing → Implemented`. Done.
