# ADR-0175 implementation review — claude-opus-5-5 (loop 2)

- **ADR**: ADR-0175 "A catalog engine is its own principal" (Realizes FEAT-0003/F58)
- **Work**: one commit `24fe33b8` on `origin/main` (31 files, +727 −152)
- **Model**: claude-opus-5-5
- **Gate**: ADR-0000 gate 5 (adr-impl-review), loop 2
- **Verdict**: **pass**. There are no Blockers, no Majors and no Minors. All three loop-1 Minors are resolved.

## Loop-1 findings

| Loop-1 finding | Status | Evidence |
|---|---|---|
| Minor 1: `kindRuntime` duplicated the same-package `recordingRuntime` | **resolved** | `kindRuntime` is gone. `pkg/funcd/own_principal_internal_test.go` now seeds the existing `recordingRuntime` through a 6-line `runningWorker` helper, and `TestScenarioKeysReissuedOnUpgrade` reads `rt.created()`. |
| Minor 2: Decision 1's "a failed CatalogService list returns `prev`" was untested | **resolved** | New subtest `TestScenarioEngineEgressIsCatalogService/a failed CatalogService list keeps the index` uses a `catalogListFails` store wrapper and asserts that the returned map equals `prev` and that the engine stays indexed as `KindCatalogService`. Loop-1's surviving mutant G is now killed (mutant M1 below). |
| Minor 3: the `WorkerIndex` comment still said `(namespace, function)` | **resolved** | `internal/network/egress/gateway.go:40-41` now reads "the principal Ref of its owner kind (a Function or a CatalogService)". The package doc (`:5-7`), the `MemoryWorkerIndex` doc (`workerindex.go:10-12`) and `EgressWorkerIndex` (`pkg/funcd/funcd.go`) were updated too. |

## Verification run (captured)

All commands ran in the worktree through `scripts/agent/d`, except the Linux tests, which ran in Docker.

| Check | Command | Result |
|---|---|---|
| Build (darwin) | `go build ./...` | exit 0 |
| Build (Linux) | `GOOS=linux go build ./...` | exit 0 |
| Vet (darwin + Linux) | `go vet` on the 8 touched packages | exit 0 / exit 0 |
| Lint (darwin) | `go tool golangci-lint run` on the 8 touched packages | `0 issues.`, exit 0 |
| Lint (Linux) | the host lint binary (`go tool -n golangci-lint`) with `GOOS=linux … run` on the 8 touched packages | `0 issues.`, exit 0 |
| Tests under `-race` (darwin) | `go test -race -count=1` over `cmd/funcdctl`, `internal/auth/cedar`, `internal/blob/s3gateway`, `internal/catalog/gateway`, `internal/function`, `internal/network/egress`, `internal/services/catalog`, `pkg/funcd` | all 8 `ok`, exit 0 |
| Tests under `-race` (Linux, `golang:1.26.4` on colima) | `internal/network/egress` (has `gateway_linux.go`), `internal/auth/cedar`, `internal/blob/s3gateway`, `internal/services/catalog`; then `pkg/funcd -run 'TestScenario(EngineEgressIsCatalogService\|KeysReissuedOnUpgrade)\|Egress'` | all `ok`, exit 0 |
| Scenario tests (`-race -v`, darwin) | the seven `TestScenario…` + `TestDeriveKeypairKindsDiffer` | all `--- PASS`, including every subtest, exit 0 |
| Bloat audit | `scripts/agent/audit.py --base origin/main --report-only --no-lint` | **PASS**: no hard flags, no production clones, no new dependencies. The masking hits are the existing `_, _ = mac.Write` (`iam.go:72`) and its test mirror in `preADRKeypair`; `hash.Write` never errors. |
| Tree | `git status --short` after the run | clean |

Environment note: the first Docker run failed `TestIssue381_NoBlankVarKeepsImportAlive` on `._auth.go: illegal character NUL`. The macOS tar pipe had added AppleDouble `._*` files, and that test parses the package sources. A rerun with `COPYFILE_DISABLE=1 tar --no-mac-metadata` (0 `._*` files in the container) passed. This is `env`, not the work.

Not run, as instructed: e2e, `go test ./...`, and the Lima lanes.

### Overlay mutants (`go test -overlay`; the worktree was not touched)

| # | Mutation | Test | Outcome |
|---|---|---|---|
| M1 | `pkg/funcd/funcd.go:1690` failed `List`: `return prev` → `continue` (loop-1 mutant G) | `TestScenarioEngineEgressIsCatalogService` | **killed** (`a failed CatalogService list keeps the index`) |
| M2 | `capabilities.go:340` `catalogServiceOwnsPrefix`: a Function lookup error counts as "no Function" (`… && err == nil`) | `TestScenarioEngineWritesOwnedPrefix` | **killed** (`a Function lookup error adds no engine writer`) |
| M3 | `pkg/funcd/funcd.go:1706` owner-kind filter removed (`if false`) | `TestScenarioEngineEgressIsCatalogService` | **killed** (`only a CatalogService in the namespace`: the `KindIdentity` worker is indexed) |
| M4 | `iam.go:125` `decodeAccess`: code `C` decodes to `KindFunction` | `TestScenarioFunctionKeyCannotSignAsEngine`, `TestDeriveKeypairKindsDiffer`, `TestDecodeAccessRoundTrip` | **killed** |

All 4 of 4 mutants were killed. The branch that survived in loop 1 is now covered.

## Contracts

| Contract | Code | Holds |
|---|---|---|
| `DeriveKeypair(master, kind, ns, name)`; access body `code + NUL + ns + NUL + name` (code `F`/`C`), MAC `"s3:" + kind + ":" + ns + "/" + name` | `internal/blob/s3gateway/iam.go` (`kindCode`, `DeriveKeypair`) | yes |
| `decodeAccess` → `(kind, ns, name, ok)`; ok=false unless the body has three non-empty parts with code F or C | `iam.go` (`SplitN(…, 3)`, a code switch with default refuse) | yes; a two-part key is refused (`TestScenarioPreADRKeyRefused`) |
| `Derive func(kind v1.Kind, ns, name string) (access, secret string)` on `S3GatewayInjection` and catalog `ReconcilerDeps`/`Reconciler` | `internal/function/function.go:171-174`, `internal/services/catalog/catalog.go:61-64,100` | yes |
| Egress index `Type: in.OwnerKind` over Function **or** CatalogService namespaces; other kinds are not indexed; a failed list returns `prev` | `pkg/funcd/funcd.go` `reconcileEgressWorkers` | yes (mutants M1 and M3) |
| Whole-namespace permit `principal is Function && principal.namespace == "<ns>"` | `internal/auth/cedar/egress_compile.go` `namespaceScope` + `estIs` | yes; `TestEgress_CollapseFunctionCountIndependent` counts `principal is Function` |
| `principalFor` uses the decoded kind, and `GetUserAccount` re-derives with it | `internal/blob/s3gateway/auth.go`, `iam.go` `GetUserAccount` | yes (mutant M4) |
| Principal sources resolve by `Type`; `principalUID` maps `KindCatalogService` → `catalogServiceUID` | `internal/auth/cedar/capabilities.go` (both sources), `entities.go` | yes |
| Prefix `writers`: `Function::"<ns>/<owner>"`, plus `CatalogService` only if it exists, binds (bucket, prefix) and no Function has the name; any lookup error or miss adds none; the `owner` attribute stays the Function UID | `capabilities.go` `s3Resource` + `catalogServiceOwnsPrefix` | yes (mutant M2) |

## Review checklist (7/7)

- [x] No `Type: v1.KindFunction` literal remains in `reconcileEgressWorkers` or `principalFor`. The remaining non-test `KindFunction` refs are the invoke path, KV, the blob service and catalog tokens, which are all out of scope (ADR Scope: catalog tokens are held only by Functions and Identities).
- [x] The MAC input carries the full kind, and a two-part key decodes to ok=false.
- [x] Each principal source returns ok=false for a ref of another type, and `principalUID` accepts `KindCatalogService`.
- [x] The whole-namespace permit carries `principal is Function`. The `egress_compile.go` diff touches only `namespaceScope` and adds `estIs`, so `appliesTo` and its validation are unchanged.
- [x] `s3Resource` adds a CatalogService writer only under the three conditions, checks `spec.blob` itself at evaluation, and adds none on a lookup error. The negative cases cover an unbound prefix, a same-named Function and a Function lookup error.
- [x] A test pins that a policy comparing `principal == resource.owner` does not match an engine.
- [x] Every `Derive` caller passes the worker's kind: `function.go:1712` (Function), `reconcile.go:273` (CatalogService), `funcd.go:626` (pass-through), and `cmd/funcdctl/dev.go:492,889` (Function). ADR-0158's pool `Derive` has not landed. Each scenario has one named, passing test that cannot pass on main, because the 4-argument `DeriveKeypair` and the type-scoped sources do not exist there.

## Scenarios → tests

| Scenario | Test | Status |
|---|---|---|
| engine-s3-bindings-are-its-own | `internal/auth/cedar/own_principal_test.go` `TestScenarioEngineS3BindingsAreItsOwn` | PASS |
| function-s3-bindings-are-its-own | `TestScenarioFunctionS3BindingsAreItsOwn` | PASS |
| engine-writes-owned-prefix | `TestScenarioEngineWritesOwnedPrefix` (6 subtests, including the plan's store-error and delete-Function cases) | PASS |
| function-key-cannot-sign-as-engine | `internal/blob/s3gateway/own_principal_test.go` `TestScenarioFunctionKeyCannotSignAsEngine` (real SigV4, 403 and an empty body) | PASS |
| pre-adr-key-refused | `TestScenarioPreADRKeyRefused` (decode refuses; SigV4 with empty ExternalKeys returns 403 `InvalidAccessKeyId`) | PASS |
| keys-reissued-on-upgrade | `pkg/funcd/own_principal_internal_test.go` `TestScenarioKeysReissuedOnUpgrade` (full platform, both reconcilers, env key equals `DeriveKeypair(master, kind, …)` and reads through the gateway) | PASS (darwin and Linux) |
| engine-egress-is-catalogservice | `TestScenarioEngineEgressIsCatalogService` (whole-namespace, `appliesTo: [lake]`, only-CatalogService and failed-list subtests) | PASS (darwin and Linux) |

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
None.

### Not scored

- **Docs follow-through** (attribution: `env`/process, carried from loop 1). The F58 feat-row split, the `Superseded in part by: ADR-0175` back-links in ADR-0085, ADR-0088 and ADR-0117, the citations in ADR-0152 and ADR-0158, the ADR `Accepted → Reviewing` bump and the board card are not in the branch, and the ADR file is not in the repository yet. This workflow assigns all of that to the wave's docs PR, so this gate stamped nothing.
- **Lanes** (attribution: `env`). The `duckdb` and `egress` Lima lanes were not run, as instructed. `TestScenarioKeysReissuedOnUpgrade` exercises the engine's new `C`-coded key end to end in-process.
- **Test fidelity note** (no finding, unchanged from loop 1). `egressConnect` rebuilds `decideConnect`'s Authorize call and its `AuditRecord` mapping, as the ADR's plan asks, because `decideConnect` is unexported.

## ✅ Verified correct — keep it

- The rework is minimal and targeted. It changes only what the three Minors named, and it reuses `recordingRuntime` instead of adding a second fake. The diff is 57 lines smaller than in loop 1.
- The failed-list subtest uses an embedding store wrapper (`catalogListFails`), which fails only the CatalogService `List`. It therefore proves that the second `List`, not the first, returns `prev`.
- The key format matches the Contracts exactly. A Function cannot compute the engine's secret, and `kindCode` returns `""` for an unkeyed kind, which `decodeAccess` refuses (pinned with `KindIdentity`).
- `catalogServiceOwnsPrefix` is fail-closed in the order the ADR requires (Function lookup first, then the CatalogService lookup, the type assertion and the exact (bucket, prefix) match), and it is evaluated per request.
- The egress index is the minimal change, and a `KindIdentity` worker is asserted not to be indexed.
- The tests are strong. The S3 scenarios sign real SigV4 requests, and the upgrade scenario runs the full platform with both reconcilers. Every key line kills a mutant.
- The commit is scoped and conventional, uses the house identity, and carries no unrelated changes.

## Recommendation

**Pass.** The work meets every Contract, all 7 Review-checklist items and all seven Scenarios. All 4 overlay mutants are killed, including the branch that survived in loop 1. Build, vet and lint pass on darwin and Linux, and the race tests pass on both. Per this workflow, the wave's docs PR stamps `Reviewing → Implemented`, advances the F58 row, adds the back-links and moves the board card.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0175",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 0,
  "model_attributed": 0,
  "dod_passed": 9,
  "dod_total": 9,
  "report": "docs/reviews/adr-0175-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2: all 3 loop-1 Minors resolved (recordingRuntime reused, failed-CatalogService-list subtest added, WorkerIndex comments updated); all Contracts match, 7/7 checklist, seven scenario tests pass under -race on darwin and Linux, build/vet/lint green on both, bloat audit PASS, 4/4 overlay mutants killed (incl. loop-1 survivor); docs/back-links/status deferred to the wave docs PR, lanes not run (env)"
}
```
