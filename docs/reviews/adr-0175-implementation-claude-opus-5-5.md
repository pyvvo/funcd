# ADR-0175 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: ADR-0175 "A catalog engine is its own principal" (Realizes FEAT-0003/F58)
- **Work**: branch `feat/adr-0175-engine-own-principal`, one commit `2231ba3e` on `origin/main` (29 files, +784 −145)
- **Model**: claude-opus-5-5
- **Gate**: ADR-0000 gate 5 (adr-impl-review), loop 1
- **Verdict**: **pass**. There are no Blockers and no Majors, and there are 3 Minors, all attributed to the model.

## Verification run (captured)

All commands ran in the worktree through `scripts/agent/d`.

| Check | Command | Result |
|---|---|---|
| Build (darwin) | `go build ./...` | exit 0 |
| Build (Linux) | `GOOS=linux go build ./...` | exit 0 |
| Vet (darwin + Linux) | `go vet` on the 7 touched packages | exit 0 / exit 0 |
| Lint (darwin) | `go tool golangci-lint run` on the touched packages | `0 issues.`, exit 0 |
| Lint (Linux) | host-built lint binary (`go tool -n golangci-lint`), `GOOS=linux … run` on the touched packages | `0 issues.`, exit 0 |
| Tests under `-race` | `go test -race -count=1` over `cmd/funcdctl`, `internal/auth/cedar`, `internal/blob/s3gateway`, `internal/catalog/gateway`, `internal/function`, `internal/services/catalog`, `pkg/funcd` | all 7 `ok`, exit 0 |
| Scenario tests (`-race -v`) | the seven `TestScenario…` + `TestDeriveKeypairKindsDiffer` | all `--- PASS`, exit 0 |
| Bloat audit | `scripts/agent/audit.py --base origin/main --report-only --no-lint` | **PASS**: no hard flags, no production clones, no new dependencies. The one `discard-prod` hit is the existing `_, _ = mac.Write` at `internal/blob/s3gateway/iam.go:72`, and `hash.Write` never errors |
| Tree | `git status --short` after the run | clean |

Not run, as instructed: e2e, `go test ./...`, and the Lima lanes. The ADR's Done item "the `duckdb` and `egress` lanes pass" is left to the per-PR gate and CI. It is not scored here (env).

### Overlay mutants (`go test -overlay`, original lines restored)

| # | Mutation | Test | Outcome |
|---|---|---|---|
| A | `pkg/funcd/funcd.go` egress index: `Type: in.OwnerKind` → `Type: v1.KindFunction` | `TestScenarioEngineEgressIsCatalogService` | **killed** (all 3 subtests) |
| B | `egress_compile.go` `namespaceScope`: drop `principal is Function` | `TestScenarioEngineEgressIsCatalogService`, `TestEgress_CollapseFunctionCountIndependent` | **killed** (whole-namespace + only-CatalogService subtests; the count assertion) |
| C | `iam.go` MAC input without the kind (`"s3:" + ns + "/" + name`) | `TestScenarioFunctionKeyCannotSignAsEngine`, `TestDeriveKeypairKindsDiffer` | **killed** |
| D | `capabilities.go` `catalogServiceOwnsPrefix`: binding check `&& b.Prefix == prefix` loosened | `TestScenarioEngineWritesOwnedPrefix` | **killed** (`unbound prefix is denied`) |
| E | `FunctionPrincipalSource`: drop the `p.Type != KindFunction` guard | the three cedar scenarios | **killed** |
| F | `catalogServiceOwnsPrefix`: a Function lookup error treated as "no Function" (`err == nil`) | `TestScenarioEngineWritesOwnedPrefix` | **killed** (`a Function lookup error adds no engine writer`) |
| G | `reconcileEgressWorkers`: a failed CatalogService list `continue`s instead of returning `prev` | every `Egress` test in `pkg/funcd` | **survived** (see Minor 2) |

6 of 7 mutants were killed. The tests discriminate on every key line except the store-error branch of Decision 1.

## Contracts

| Contract | Code | Holds |
|---|---|---|
| `DeriveKeypair(master, kind, ns, name)`; access body `code + NUL + ns + NUL + name` (code `F`/`C`), MAC `"s3:" + kind + ":" + ns + "/" + name` | `internal/blob/s3gateway/iam.go:50-75` (`kindCode`, `DeriveKeypair`) | yes |
| `decodeAccess` → `(kind, ns, name, ok)`; ok=false unless three non-empty parts with code F or C | `iam.go:109-129` (`SplitN(…, 3)`, code switch) | yes; a two-part key is refused (`TestScenarioPreADRKeyRefused`) |
| `Derive func(kind v1.Kind, ns, name string) (access, secret string)` on `S3GatewayInjection` and catalog `ReconcilerDeps`/`Reconciler` | `internal/function/function.go:171-173`, `internal/services/catalog/catalog.go:61-64,100` | yes |
| Egress index `Type: in.OwnerKind`, over Function **or** CatalogService namespaces; other kinds not indexed; a failed list returns `prev` | `pkg/funcd/funcd.go:1683-1712` | yes |
| Whole-namespace permit `principal is Function && principal.namespace == "<ns>"` | `internal/auth/cedar/egress_compile.go` `namespaceScope` + new `estIs` | yes |
| `principalFor` uses the decoded kind; `GetUserAccount` re-derives with it | `internal/blob/s3gateway/auth.go:41-46`, `iam.go:158-160` | yes |
| Principal sources resolve by `Type`; `principalUID` maps `KindCatalogService` → `catalogServiceUID` | `internal/auth/cedar/capabilities.go` (both sources), `entities.go:158-159` | yes |
| Prefix `writers`: `Function::"<ns>/<owner>"` plus `CatalogService` only if it exists, binds (bucket, prefix), and no Function has the name; any lookup error/miss adds none; `owner` attr stays the Function UID | `capabilities.go:310-316` + `catalogServiceOwnsPrefix` (`:339-357`) | yes |

## Review checklist (7/7)

- [x] No `Type: v1.KindFunction` literal remains in `reconcileEgressWorkers` or `principalFor` (diff and grep confirm; mutant A).
- [x] The MAC input carries the full kind, and a two-part key decodes to ok=false (mutant C; `TestScenarioPreADRKeyRefused`).
- [x] Each principal source returns ok=false for a ref of another type, and `principalUID` accepts `KindCatalogService` (mutant E).
- [x] The whole-namespace permit carries `principal is Function`. The `appliesTo` path and its validation are untouched: the diff to `egress_compile.go` changes only `namespaceScope` and adds `estIs` (mutant B).
- [x] `s3Resource` adds a CatalogService writer only under the three conditions, checks `spec.blob` itself at evaluation time, and adds none on a lookup error. The negative cases cover an unbound prefix, a same-named Function, and a Function lookup error (mutants D, F).
- [x] A test pins that a policy comparing `principal == resource.owner` does not match an engine (`TestScenarioEngineWritesOwnedPrefix/a policy on resource.owner does not match the engine`).
- [x] Every `Derive` caller passes the worker's kind. The callers are `function.go:1630` (Function), `reconcile.go` `engineEnv` (CatalogService), `funcd.go:626` (pass-through), and `cmd/funcdctl/dev.go:492,889` (Function). ADR-0158's pool `Derive` has not landed, so it has no caller. Each scenario has one named, passing test that fails on main, because the 4-argument `DeriveKeypair` and the type-scoped sources do not exist on main.

## Scenarios → tests

| Scenario | Test | Status |
|---|---|---|
| engine-s3-bindings-are-its-own | `internal/auth/cedar/own_principal_test.go` `TestScenarioEngineS3BindingsAreItsOwn` | PASS |
| function-s3-bindings-are-its-own | `TestScenarioFunctionS3BindingsAreItsOwn` | PASS |
| engine-writes-owned-prefix | `TestScenarioEngineWritesOwnedPrefix` (6 subtests, including the plan's store-error and delete-Function cases) | PASS |
| function-key-cannot-sign-as-engine | `internal/blob/s3gateway/own_principal_test.go` `TestScenarioFunctionKeyCannotSignAsEngine` (real SigV4 through the gateway, 403 and an empty body) | PASS |
| pre-adr-key-refused | `TestScenarioPreADRKeyRefused` (decode refuses; SigV4 with empty ExternalKeys returns 403 `InvalidAccessKeyId`) | PASS |
| keys-reissued-on-upgrade | `pkg/funcd/own_principal_internal_test.go` `TestScenarioKeysReissuedOnUpgrade` (full platform with fresh runtime; both reconcilers; env key equals `DeriveKeypair(master, kind, …)` and signs through the gateway) | PASS |
| engine-egress-is-catalogservice | `TestScenarioEngineEgressIsCatalogService` (whole-namespace + `appliesTo: [lake]` + only-CatalogService subtest; also asserts that a worker of another kind is not indexed) | PASS |

The plan's "table test that keys of the two kinds differ" is `TestDeriveKeypairKindsDiffer`. The superseded ADR-0088 scenario `function-takes-precedence` was removed. The ADR supersedes it in part, so the removal is in scope. The other ADR-0088 provider tests now sign as `csPrincipal`, and their assertions were not weakened.

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor

1. **The new fake runtime duplicates an existing one in the same package** (attribution: `model`).
   `pkg/funcd/own_principal_internal_test.go:32-128` adds `kindRuntime`, a full `runtime.Runtime` fake of about 100 lines.
   The same package (`package funcd`) already has `recordingRuntime` in `pkg/funcd/funcd_test.go:457-545`. That fake records every `WorkerSpec`, including `OwnerKind`, lists by namespace, and exposes `insts`, which a test can seed with a running instance and an IP.
   The bloat audit does not flag the copy, because it checks only production clones, but it is test code that a helper would have avoided.
2. **Decision 1's "a failed CatalogService list returns `prev`" is untested** (attribution: `model`).
   The code at `pkg/funcd/funcd.go:1686-1689` (`return prev` at `:1688`) is correct. Mutant G changes that branch to `continue`, and every `Egress` test in `pkg/funcd` still passes. A small subtest is needed: a store whose CatalogService `List` fails, with the engine's entry kept in the index.
3. **The `WorkerIndex` comment still says `(namespace, function)`** (attribution: `model`).
   The ADR's supersession of ADR-0117 names "the `(namespace, function)` Ref … of the `WorkerIndex` comment, which now carries the owner kind". `internal/network/egress/gateway.go:39` still reads "to its (namespace, function) principal Ref", and the package doc at `:5` still says "whether that Function may egress::connect". This is a stale comment, with no effect on behavior. The ADR's Implementation plan file list omits `internal/network/egress`, which partly explains the miss.

### Not scored

- **Docs follow-through** (attribution: `env`/process). The F58 feat row split, the `Superseded in part by: ADR-0175` back-links in ADR-0085, ADR-0088 and ADR-0117, the citations in ADR-0152 and ADR-0158, the ADR `Accepted → Reviewing` bump, and the board card are not in the branch. The ADR file is also not in the repository yet. This workflow assigns all of that to the wave's docs PR, so this gate stamped nothing.
- **Lanes** (attribution: `env`). The `duckdb` and `egress` Lima lanes were not run, as instructed. The engine's new `C`-coded key is exercised end to end in-process by `TestScenarioKeysReissuedOnUpgrade`.
- **Test fidelity note** (no finding). `TestScenarioEngineEgressIsCatalogService` rebuilds `decideConnect`'s Authorize call and its `AuditRecord` mapping by hand, because `decideConnect` is unexported in `internal/network/egress`. The ADR's plan asks for exactly this ("as `decideConnect` calls it"). It also means that `rec.Function == "lake"` restates `decideConnect`'s `Function: string(ref.Name)` rather than exercising it.

## ✅ Verified correct — keep it

- The key format matches the Contracts exactly. The one-byte code and NUL separator go in the access body, and the full `v1.Kind` goes in the MAC input. A Function therefore cannot compute the engine's secret, and mutant C proves that the test catches it. `kindCode` returns `""` for a kind that holds no key, and `decodeAccess` refuses such a key. `TestDeriveKeypairKindsDiffer` pins this with `KindIdentity`.
- `catalogServiceOwnsPrefix` is fail-closed in the order the ADR requires. The Function lookup goes first, and any result other than NotFound means "a Function may exist". The CatalogService lookup and type assertion follow, and then the exact (bucket, prefix) binding check. The function is called only when `pfx.Owner != ""` and is evaluated per request, so creating or deleting a Function takes effect on the next request (the "deleting the Function gives the engine write" subtest).
- `resource.owner` stays `functionUID(...)` (ADR-0080 back-compatibility), and a test pins that a user policy comparing `principal == resource.owner` does not match the engine.
- Both principal sources are guarded by type, so a name held by both kinds resolves each kind to its own object. The source and registry doc comments were updated without padding.
- The egress index is the minimal change. It collects namespaces from both kinds, and any `List` error returns `prev`. It indexes `in.OwnerKind`, skips any kind other than Function or CatalogService, and the test asserts that a `KindIdentity` worker is not indexed.
- `engineEnv`'s misleading comment ("the Bucket prefix owner == cs.Name"), which the plan required to be corrected, now states the actual rule.
- The test strength is real. The S3 scenarios sign real SigV4 requests through the gateway and assert a 403 and an empty body, and the pre-ADR scenario asserts the `InvalidAccessKeyId` code. The upgrade scenario runs the full platform with both reconcilers and authenticates each derived key at the gateway.
- The commit is scoped and conventional, uses the house identity, and carries no unrelated changes.

## Recommendation

**Pass.** The work meets every Contract, all 7 Review-checklist items, and all seven Scenarios with discriminating tests (6 of 7 mutants killed). Build, vet and lint pass on darwin and Linux, and the race tests pass. The three Minors can be folded into the follow-up before merge or left as cleanup, and none of them blocks the gate. Per this workflow, the wave's docs PR stamps `Reviewing → Implemented`, advances the F58 row, adds the back-links, and moves the board card.

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
  "minors": 3,
  "model_attributed": 3,
  "dod_passed": 9,
  "dod_total": 9,
  "report": "docs/reviews/adr-0175-implementation-claude-opus-5-5.md",
  "notes": "all Contracts match, 7/7 checklist, all seven scenario tests pass under -race, build/vet/lint green on darwin+Linux, bloat audit PASS, 6/7 overlay mutants killed; kindRuntime duplicates the same-package recordingRuntime, Decision 1's CatalogService-list-error-returns-prev untested (surviving mutant), WorkerIndex comment still says (namespace, function) (model); docs/back-links/status deferred to the wave docs PR, lanes not run (env)"
}
```
