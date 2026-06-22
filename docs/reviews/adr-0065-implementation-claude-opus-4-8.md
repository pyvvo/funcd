# ADR-0065 implementation review — pure-Go Badger metastore engine

- **ADR**: [0065](../adr/0065-metastore-badger-engine.md) · **phase**: implementation · **model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met, no Blockers/Majors) · 2026-06-22

## Verification run (evidence)

| check | result |
|---|---|
| `CGO_ENABLED=0 go build ./...` | **OK** — whole module compiles pure-Go (the cgo lane is gone) |
| `go test ./...` | **pass** — incl. `internal/store/badger` (4 scenarios) + the full suite |
| `go tool golangci-lint run ./internal/store/... ./cmd/funcd/... ./internal/version/...` | **0 issues** |
| `go mod verify` | **all modules verified**; `slatedb.io/slatedb-go` removed from `go.mod` |

## Scenarios → tests (all named, un-skipped, passing)

- `TestScenarioBadgerEnginePassesStoreContract` — `storecontract.RunContract` green against Badger (parity with memory).
- `TestScenarioDurableMetastoreSurvivesRestart` — write → `Close` → reopen same dir → object + `resourceVersion` recovered.
- `TestScenarioOptimisticConcurrencyConflict` — stale-RV `Update` → `fault.Conflict`.
- `TestScenarioPureGoBuildNoCgo` — asserts `internal/store/slatedb` is removed; the `CGO_ENABLED=0` build is the check above.

## ✅ Verified correct (keep)

- **Driver** ([internal/store/badger/badger.go](../../internal/store/badger/badger.go)) implements `store.Engine`/`Txn`
  verbatim, one file, own subpackage; NUL-separated `bucket\x00key` (matches the ADR's correction); RAM-frugal options
  profile; `SyncWrites` default on; `RunValueLogGC` ticker stopped on `Close`; `badger.ErrConflict → fault.Conflict`
  (defensive — the store wrapper's `writeMu` serializes writers).
- **Wiring** — `buildStore` selects Badger at `<dataDir>/store` for file mode, memory unchanged, `WithEncryptor`
  threading preserved; the durable metastore is now real in the shipped daemon (was memory-only).
- **Removals complete** — `internal/store/slatedb/` deleted; the dep, the `slatedb` build tag, the `slatedb-lib`/
  `test-slatedb` recipes, and the `scripts/build.sh` cgo release path all gone; `internal/version/artifacts_test.go`
  updated to assert the pure-Go build.
- **Hygiene** — no `any` in the driver surface; ctx-first; `api/fault` kinds; no identity/path leak; a stray
  repo-relative `store/` dir from a test was eliminated (temp dirs + `.gitignore` safety net).

## Findings

None (Blocker/Major/Minor). The platform-level in-process + containerd e2e is sequenced to the batch's end phase
(per the user's "test them at the end") and does not gate this engine ADR; the engine-level durable-restart scenario
passes here.

## DoD

ADR Review-checklist items: 10/10 satisfied.

## Recommendation
**pass** — stamp `Reviewing → Implemented`. Done.
