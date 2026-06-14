# Review report — ADR-0003 implementation

- **ADR**: [ADR-0003 — Resource model & API typing (v1alpha1)](../adr/0003-resource-model-and-api-typing.md)
- **Phase**: implementation (ADR-0000 review gate #5)
- **Implemented by**: `deepseek-v4-pro`
- **Date**: 2026-06-14
- **Reviewer**: `adr-impl-review` skill (`claude-opus-4-8`) — authored/judged the ADR but **did not
  implement it**, so this scores deepseek's code independently
- **Realizes**: [FEAT-0000/F03](../feat/0000-feat-v1.md), [FEAT-0000/F22](../feat/0000-feat-v1.md)

## Verdict: pass — 0 Blockers, 0 Majors, 1 Minor

A faithful, high-quality implementation of the ADR-0003 Contracts. All 9 Scenarios have named,
un-skipped, passing tests; lint is clean; conventions hold; the ADR substance is unchanged. The
single literal DoD gap — `just ci` exit 0 — is the uncommitted-tracked-files diff gate (process,
not code), green on commit.

## Verification run (captured evidence)

| Check | Command | Result |
|---|---|---|
| compiles | `go build ./...` | **exit 0** |
| vets | `go vet ./...` | **exit 0** |
| formatted | `gofmt -l api/` | **empty** (clean) |
| lints | `go tool golangci-lint run ./...` | **0 issues**, exit 0 |
| tests | `go test ./api/types/v1alpha1/ -v -count=1` | **PASS** — 9/9 `TestScenario_*` pass, none skipped |
| deps | `go mod verify` + `git diff go.mod go.sum` | verified; **clean** (no new deps) |
| full gate | `just ci` | **exit 1** — diff gate only (see ⚙️ below) |
| tree | vs ADR *Repository surface* | 15 kind files + metadata/status/ids/enums + 3 tests — nothing missing/extra |
| hygiene | identity grep; ADR/feat status | clean; ADR `Reviewing`, substance unchanged (only `Accepted → Reviewing`); F03/F22 `reviewing` |

Scenario tests, all passing: `objectmeta-requires-resource-group`, `tags-optional`,
`name-rejects-non-dns-label`, `scope-enforced`, `json-roundtrip-stable` (+ `_FullEquality`),
`generic-object-access`, `generic-status-writeback`, `kind-registry-roundtrips-every-kind`
(+ `TestValidateMeta_RejectsTypeMetaMismatch`), `conditions-upsert-by-type`.

## 🔴 Blockers — None.
## 🟡 Majors — None.

## Minor (1)
- **`namespace.go` defines `NamespaceSpec struct{}` but the `Namespace` struct does not embed a
  `Spec` field** — a dead exported type (the only kind with this inconsistency; the other 14 are
  clean). *Attribution: `model`.* Fix: drop the unused type or embed it. Non-blocking.

## ⚙️ Non-model note — the `just ci` exit 1
Not a code defect. `gofmt` is clean and build/vet/lint/test/mod-verify all exit 0. `just ci` fails
only because `enums.go`/`ids.go`/`types_test.go` are modified-but-**uncommitted**, tripping the
recipe's "no dirty tracked files" diff gate (CLAUDE.md *Known pitfalls* #2) — identical to ADR-0002,
resolved by committing. *Attribution: process/tooling, not the model.* `just ci` goes green on commit.

## ✅ Verified correct (keep it)
- **Contracts honored verbatim**: `Object` / `StatusObject`; `validateMeta` checks `TypeMeta` GVK
  match **and** derives scope from `Kind.Namespaced()` (single-sourced — m1/m2 from the judge);
  `NewObject`/`AllKinds` over all 15 with stamped `TypeMeta`; full envelope + carrier fields;
  `ObjectRef`/`OwnerReference`.
- **Judge-driven revisions implemented & tested**: `TestScenario_GenericStatusWriteback` (M1
  `StatusObject`) and `TestValidateMeta_RejectsTypeMetaMismatch` (m2) both pass.
- **`StatusObject` membership exact**: 11 implementers; the 4 `Object`-only kinds are precisely
  `Config`/`Secret`/`Grant`/`EgressPolicy`.
- **`Conditions.Set`** advances `LastTransitionTime` only on a real `Status` change — the subtlest
  contract, correct.
- **`Phase` reconciled** to the 7 blueprint values; ADR-0002 placeholders (`Scaling`/`Reconciling`/
  `Deleted`) removed — resolves ADR-0002's deferred enum question.
- **Conventions**: stdlib + `api/fault` only (depguard green); no `any`/`interface{}`/`map[string]any`
  in hand-written sigs (`map[string]string`/`[]byte` for `Tags`/`Config.Data`/`Secret.Data`); no
  globals (gochecknoglobals green); no `panic`/non-slog. Identity clean.

## Definition of Done
10/10 ADR Review-checklist items hold on the merits (build/vet/lint/test/gofmt/mod-verify all green
with evidence). The only literal `just ci` red is the uncommitted-files diff gate — a commit-state
artifact, not a code failure; green on commit. No `model`-attributed Blockers or Majors.

## Recommendation
**Pass** — status advanced to `Implemented`. Land it by committing the work + status edits together
(makes `just ci` verifiably green). The one Minor (dead `NamespaceSpec`) is optional cleanup the
builder can fold into the commit; it doesn't block. No superseding ADR needed.
