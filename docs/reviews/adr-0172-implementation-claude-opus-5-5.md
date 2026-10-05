# ADR-0172 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: [ADR-0172](../adr/0172-revision-integrity.md) — Revision integrity (read-only API, fail closed when missing, a name for every Function)
- **Work**: branch `feat/adr-0172-revision-integrity`, one commit `e1d64bf6` on `origin/main` `6773aabc` (19 files, +931/−251)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Date**: 2026-10-05

## Summary

The work implements every Decision and Contract of ADR-0172. The API serves only get and list for Revisions, the SDK
and `funcdctl` refuse writes before any request, a missing stamped Revision fails closed, every valid Function name
gets a Revision name of at most 63 characters, and pooled members whose Revision is gone or held by another Function
are kept off the manifest. ADR-0161 has not merged, so the code follows the preflight's agreed fallback: the
`RevisionStampFailed` error is returned directly after `gateFailed`, and both new gate literals set
`zeroReplicas: true`. ADR-0158 has merged, and Decision 7's filter is folded into its `sameKeyFunctions`. Build,
vet and lint pass on macOS and Linux, and the touched packages pass under `-race`. Three overlay mutants on key lines
each fail a test. There are no Blockers or Majors and two Minors.

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` on `internal/function`, `internal/controlplane`, `pkg/sdk`, `cmd/funcdctl`, `pkg/funcd` | exit 0 |
| the same `go vet` with `GOOS=linux` | exit 0 |
| `go vet -tags e2e ./pkg/funcd/` (compiles `revision_e2e_test.go`) | exit 0 |
| `go test -race -count=1` on the four touched packages | controlplane, sdk and funcdctl `ok`; `internal/function` failed once on `TestIssue359_PoolStartFailureWritesFailedStatus` while the mutant runs loaded the host; it passed on an immediate rerun (exit 0) and 40/40 times alone (see m2) |
| `golangci-lint run` on the five packages | 0 issues, exit 0 |
| `golangci-lint run` with `GOOS=linux` (the host binary, Linux analysis) | 0 issues, exit 0 |
| Diff scan for an absolute path or a personal identity | none; commit author `green-0-rabbit` |

The e2e scenario `TestScenarioRefusedEditKeepsDigest` (build tag `e2e`) compiles but was not run here: the review
brief excludes e2e, and the PR gate (`just ci-full` through `scripts/agent/gate.sh`) runs it. No Lima lane was run.

### Overlay mutants (`go test -overlay`): each one must fail a test

| # | Mutation | Test run | Result |
|---|---|---|---|
| m1 | `ensureRevision`: drop the `currentRevision == revName` → `errRevisionMissing` case (the fail-closed branch) | `TestScenarioMissingRevisionFailsClosed`, `TestScenarioReapplyRecoversMissingRevision` | **killed** (`MissingRevisionFailsClosed` FAIL) |
| m2 | `pinnedMember`: never return `errRevisionMissing` (`case false && …`) | `TestScenarioPooledMissingRevisionLeftOut` | **killed** (`a` stays in the pool manifest) |
| m3 | `revisionOf`: judge a controller ref that names another Function as `revNamesake` instead of `revOther` | `TestScenarioRevisionNameTakenWritesStatus`, `TestRevisionOf` | **killed** (both FAIL) |

## Contracts and Decisions against the code

| Item | Evidence | Holds |
|---|---|---|
| D1: `registerRevision` keeps list and get; create, replace and delete are gone with their `Handlers`, `storeHandlers` and `StubHandlers` methods; no PATCH | `internal/controlplane/routes.go`, `controlplane.go` (interface lines 50, 52 and 53 removed), `handlers.go`, `stubs.go`; `grep MethodPatch routes.go` finds nothing | yes |
| D1: the OpenAPI spec has no Revision write | `api/openapi/funcd.v1alpha1.yaml` −102 lines; only `listRevisions` and `getRevision` remain; the spec-sync and `TestIssue166` tests pass | yes |
| D1: 405 problem+json, `Allow: GET`, developer and admin; store unchanged; get and list 200 | `internal/controlplane/revision_test.go` `TestScenarioRevisionWritesRefused` (asserts the ResourceVersion, the digest and the count after the writes) | yes |
| D2: `sdk.ReadOnlyKind`; `Apply`, `Create` and `Delete` refuse with `fault.Invalid` and the exact message before any request | `pkg/sdk/kinds.go`, `pkg/sdk/sdk.go`; `TestSDKRevisionWritesRefusedBeforeRequest` counts 0 server hits | yes |
| D2: `funcdctl apply -f` refuses in the offline pre-flight, so no document applies; `delete revision` fails in the SDK | `cmd/funcdctl/cli.go` pre-flight loop; `TestCLIRevisionIsReadOnly` (the ConfigMap is NotFound afterwards) | yes |
| D2: `TestSDKKindPaths_MatchServerRoutes` requires only GET for read-only kinds and fails if a write route exists | `pkg/sdk/kinds_test.go` | yes |
| D3: resolve only on the create path; NotFound with `currentRevision` equal to the name returns `errRevisionMissing` without resolving or creating | `internal/function/function.go` `ensureRevision`; m1 killed | yes |
| D4: `revisionName` is the Contracts code verbatim; `ensureRevision`, `pinnedMember` and `servingMember` call it | `function.go` `revisionName`; `pool.go` `servingMember` calls `revisionName(m)`; `TestRevisionName` covers short, 61 at gen 9 and 10, 62 and 63 at gen 1, and `math.MaxInt64`, each valid, at most 63 characters and stable | yes |
| D5: `revisionOf` replaces `ownedByAnother`; `revSelf`, `revNamesake` and `revOther` as specified; a controller ref of another kind is `revOther` (preflight handling) | `function.go` `revisionOf`; `TestRevisionOf` (9 cases); m3 killed | yes |
| D5: adopting a ref-less `revSelf` appends the controller ref and keeps any others, with a metadata-only `store.Update`; an error (Conflict included) is returned before `currentRevision` is set | `function.go` `adoptRevision` (the only `store.Update` of a Revision, near `function.go:1552`); `TestAdoptionWritesOwnerRef`, `TestAdoptionUpdateErrorReturned` | yes |
| D5: `revisionTemplate` reads only `revSelf` (`revOther` → Conflict, `revNamesake` → NotFound) | `function.go` `revisionTemplate` | yes |
| Contracts table: create Conflict re-reads (adopt, Conflict returned, or stamp failure); `fault.Invalid` is wrapped in `errRevisionStampFailed`; any other Create error is returned and retryable | `function.go` `settleCreateConflict` and the Create error switch | yes |
| D6: `RevisionMissing` → `gateFailed`, phase Failed, requeue at the supervision period, nil error; `RevisionStampFailed` → status write, then the error is returned | `function.go` `Reconcile` switch (lines 478–490); `TestScenarioRevisionCreateRefusedWritesStatus` asserts `errors.Is` through `export_test.go` | yes (the pre-ADR-0161 fallback agreed in the preflight) |
| D6: `RevisionStampFailed` names the other Function | `stampTaken` writes `"<name> is Function \"S\"'s"`; `TestScenarioRevisionNameTakenWritesStatus` asserts that S appears in the message and that S-1's ResourceVersion is unchanged | yes |
| D7: `pinnedMember` returns `errRevisionMissing` or a Conflict; `sameKeyFunctions` uses `servingMember` or leaves the member out with a Warn; other errors still fail | `internal/function/pool.go`; m2 killed | yes |

## Findings

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **m1 [model]**: `TestScenarioLongNameDeploysWithHashedRevision` (`internal/function/digest_test.go`) shows that the
  hashed name is stable across two reconcile passes of one Reconciler. The scenario says "stable across a restart",
  which would need a new Reconciler over the same store. The risk is low because `revisionName` is a pure function of
  the name and the generation, and the test also asserts that exactly one Revision is stored. The test still covers
  the restart clause only by proxy.
- **m2 [env, not scored]**: The pool tests in `internal/function` are sensitive to host load under `-race`. On the
  branch, `TestIssue359_PoolStartFailureWritesFailedStatus` failed once while the mutant runs shared the host. It
  passed on the rerun and 40/40 times alone. On a baseline overlay of `origin/main`, made from the production and test
  files of `internal/function`, `TestIssue70_FailedPoolHostRespawnsOncePerPeriod` failed under the same concurrent
  load, while the branch passed `-count=3`. The flake is older than this change and does not come from it.

## ✅ Verified correct (keep it)

- **Exact Contracts code.** `revisionName`, the constants, the sentinels and the `revisionOf` doc match the ADR
  verbatim. Its unit table includes the `math.MaxInt64` edge, which needs a 34-character prefix.
- **A minimal, complete API removal.** The interface methods in `controlplane.go`, which the ADR's plan did not
  mention, are removed. The OpenAPI spec is regenerated, and no orphan schema is left (TestIssue166 passes).
- **Tests that prove "before any request".** The SDK test counts server hits. The CLI test proves that the ConfigMap
  placed before the Revision is not applied.
- **Fail-closed assertions that are complete.** Both conditions carry `RevisionMissing`, the resolver count does not
  change, no Revision is created, no worker is left in a non-terminal state, there is no route, and no digest B
  reaches the materializer.
- **Correct ADR-0158 merge.** Decision 7's filter is folded into the merged `sameKeyFunctions(ctx, key, idx)`, and
  the pooled scenario runs on ADR-0158's harness and pool names.
- **Preflight handling followed.** `ErrRevisionStampFailed` is exported only through `export_test.go`. The small
  `RefusingStore` test wrapper lets Revision Create or Update fail. The internal tests live in
  `revision_internal_test.go`. The pre-ADR-0161 fallback sets `zeroReplicas` and the requeue.
- **A one-PR scope with nothing extra.** Every changed file appears in the ADR's plan or the preflight brief. No shim
  is touched and no `go.mod` pin is bumped.

## Tracking

The ADR is still `Accepted`, and the F13 token reads `revision integrity: accepted`. The review brief makes the
wave's docs PR responsible for the `Accepted → Reviewing → Implemented` moves and the feat row, so this review stamps
nothing. The back-links from ADR-0005, 0018, 0020 and 0035 are present. The ADR's substance is unchanged on the
branch: the diff touches no file under `docs/`.

## Recommendation

**pass.** The wave's docs PR can stamp ADR-0172 `Implemented` and set the F13 token to `implemented`, once the PR
gate has run `TestScenarioRefusedEditKeepsDigest` (e2e) and `just ci-full` green. The builder can optionally fix m1
by reconciling the long-name case once more through a new Reconciler over the same store.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0172",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 1,
  "dod_passed": 8,
  "dod_total": 8,
  "report": "docs/reviews/adr-0172-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (e1d64bf6 on 6773aabc; pre-ADR-0161 fallback per preflight, folded into ADR-0158's sameKeyFunctions): all 7 Decisions and the Contracts table hold; 3 overlay mutants killed (fail-closed branch, pinnedMember missing, revisionOf revOther); build/vet/lint clean also Linux; 4 touched pkgs -race ok; 9/10 scenario tests run and pass, e2e RefusedEditKeepsDigest compiled only (runs at the PR gate). m1 [model] long-name stability shown across passes, not a restart; m2 [env] pre-existing load-sensitive pool-test flake (TestIssue359 on branch, TestIssue70 on main baseline)."
}
```
