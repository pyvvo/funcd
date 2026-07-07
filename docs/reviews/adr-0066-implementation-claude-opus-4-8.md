# ADR-0066 implementation review — KV-service durable engine (driver + gateway + seams)

- **ADR**: [0066](../adr/0066-kv-service-durable-engine.md) · **phase**: implementation · **model**: claude-opus-4-8
- **Verdict**: **pass** · 2026-06-22

## Verification

`go build ./...` OK · golangci-lint 0 issues · `go test ./internal/kvstore/badger/` pass.

## Scenarios → tests (named, passing)
- `TestScenarioKVDurableDriverRoundtrips` — kvstorecontract parity + persist across Close+reopen.
- `TestScenarioPerStoreWritesSerialized` — 16×100 concurrent writers → 0 conflicts, all landed (the gateway).
- `TestScenarioStoreTeardownDropsPrefix` — DropPrefix removes one store's keys, others intact.
- `TestScenarioBaseDriverHasNoDurabilitySideEffects` — no seam ⇒ no reserved/CDC keys (raw scan).

## ✅ Verified correct
- `internal/kvstore/badger`: the `kvstore.KV` driver + greedy-drain group-commit gateway (NOT a timer) +
  prefix-per-store (NUL-reserved internal namespace) + `DropPrefix` + the `Op`/`OnWrite` `Backup`/`CDC` seams
  (default-absent) + `WithBackup`/`WithCDC`; idempotent Close.
- Separate Badger instance from the metastore (`<dataDir>/kv`); zero new deps (Badger from ADR-0065).
- **Facade-selection wiring delivered by ADR-0069** — the platform constructs `kv.NewFacade` with the
  config-selected driver (memory|badger) and closes it on shutdown.

## Findings
None.

## DoD
Driver scenarios 4/4 + the facade wiring (via ADR-0069) satisfy the checklist. **pass** → Implemented.
