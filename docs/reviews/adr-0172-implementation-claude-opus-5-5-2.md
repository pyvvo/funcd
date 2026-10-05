# ADR-0172 implementation review — claude-opus-5-5 (loop 2)

- **ADR**: [ADR-0172](../adr/0172-revision-integrity.md) — Revision integrity (read-only API, fail closed when missing, a name for every Function)
- **Work**: branch `feat/adr-0172-revision-integrity`, one commit `003348fa` on `origin/main` `5881659b` (ADR-0161, #685), 20 files, +996/−253
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Date**: 2026-10-05

## Summary

The commit is the loop-1 work rebased onto ADR-0161 and adapted to it. The pre-ADR-0161 fallback is gone: both new
gate literals drop `zeroReplicas` and follow ADR-0161's `gateFailed`, and the `RevisionStampFailed` error is returned
as a `routeError` after the gate's status write, exactly as Decision 6's "Under ADR-0161" sentence asks. A new test,
`TestRevisionMissingCountsServingWorkers`, pins the ADR-0161 gate behaviour for a missing Revision (listening workers
keep the Function Ready with their count, a running one keeps it Degraded, nothing is replaced). Loop 1's model Minor
is resolved: the long-name scenario now restarts the daemon. Build, vet and lint pass on macOS and Linux; the four
touched packages pass under `-race` at the first run; all ten scenario tests pass, the e2e one included. Two of three
overlay mutants are killed; the third shows an untested requeue. No Blockers or Majors; two Minors.

## Loop-1 findings

| Loop-1 finding | Status |
|---|---|
| m1 [model]: long-name stability shown across passes, not a restart | **resolved**. `TestScenarioLongNameDeploysWithHashedRevision` now calls `h.restart(t)` (a new `Reconciler` over the same store and deps, a new scheduler and gateway, the fake runtime's existing `forget()` so no worker is listed) and asserts the same Revision `ResourceVersion` after the restart and one stored Revision. |
| m2 [env]: load-sensitive pool-test flake | **not reproduced**: `internal/function` passed `-race -count=1` at the first run. Not scored. |

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` on `internal/function`, `internal/controlplane/...`, `pkg/sdk`, `cmd/funcdctl`, `pkg/funcd` | exit 0 |
| the same `go vet` with `GOOS=linux` | exit 0 |
| `go vet -tags e2e ./pkg/funcd/` | exit 0 |
| `golangci-lint run` on the five packages | 0 issues, exit 0 |
| `golangci-lint run` with `GOOS=linux` (the host binary, Linux analysis) | 0 issues, exit 0 |
| `go test -race -count=1` on `internal/function`, `internal/controlplane/...`, `pkg/sdk`, `cmd/funcdctl` | all `ok`, exit 0 |
| `go test -race -tags e2e -run TestScenarioRefusedEditKeepsDigest ./pkg/funcd/` | PASS (0.44 s), exit 0 |
| `-race -v` run of the nine non-e2e scenario tests and the unit tests named in the plan | every one `--- PASS`, none skipped |
| Diff scan for an absolute path or a personal identity | none; commit author `green-0-rabbit` |

None of the touched packages is Linux-only (no `_linux.go` file and no `linux` build tag in them), so no Docker run
was needed; the Linux build, vet and lint cover the Linux compile. No Lima lane was run.

### Overlay mutants (`go test -overlay`): each one must fail a test

| # | Mutation | Test run | Result |
|---|---|---|---|
| M1 | `reconcileFunction`: return the stamp error bare instead of `routeError{err}` (the ADR-0161 adaptation) | `TestScenarioRevisionCreateRefusedWritesStatus` | **killed** ("a retry keeps the gate's reason": `failPass` overwrites the reason) |
| M2 | `reconcileFunction`: drop `requeue: r.supervisionPeriod` from the `RevisionMissing` gate literal | the whole `internal/function` package | **survived** (see n1) |
| M3 | `sameKeyFunctions`: no longer treat `errRevisionMissing` as a left-out member (only Conflict) | `TestScenarioPooledMissingRevisionLeftOut` | **killed** (unexpected error from b's pass) |

## Contracts and Decisions against the code

| Item | Evidence | Holds |
|---|---|---|
| D1: list and get only; create, replace and delete gone with their `Handlers`, `storeHandlers` and `StubHandlers` methods; no PATCH | `routes.go`, `controlplane.go`, `handlers.go`, `stubs.go` diffs; OpenAPI keeps only `listRevisions` and `getRevision` | yes |
| D1: 405 problem+json, `Allow: GET`, developer and admin; store unchanged | `TestScenarioRevisionWritesRefused` passes | yes |
| D2: `sdk.ReadOnlyKind`; `Apply`, `Create`, `Delete` refuse before any request; `funcdctl apply -f` pre-flight, `delete revision` fails | `pkg/sdk/kinds.go`, `pkg/sdk/sdk.go`, `cmd/funcdctl/cli.go`; `TestSDKRevisionWritesRefusedBeforeRequest`, `TestCLIRevisionIsReadOnly`, `TestSDKKindPaths_MatchServerRoutes` pass | yes |
| D3: NotFound with `currentRevision` equal to the name returns `errRevisionMissing`, nothing resolved or created | `ensureRevision` (`function.go` near line 1736); `TestScenarioMissingRevisionFailsClosed` | yes |
| D4: `revisionName` is the Contracts code verbatim and the only non-test name former | `function.go:1834`; callers `function.go:1722`, `:1783`, `pool.go:144`, `:165`; `TestRevisionName` | yes |
| D5: `revisionOf` and `stampTaken`; adoption is the reconciler's one Revision `store.Update` (`function.go:1801`; the other `store.Update` calls write Function status); `dropRevision` is reached only for `revNamesake` | grep of `internal/function`; `TestRevisionOf`, `TestAdoptionWritesOwnerRef`, `TestAdoptionUpdateErrorReturned` | yes |
| Contracts table: create Conflict re-reads; `fault.Invalid` wrapped in `errRevisionStampFailed`; other Create errors returned | `settleCreateConflict`, the Create error switch | yes |
| D6 under ADR-0161: `RevisionMissing` → `gateFailed`, nil error; `RevisionStampFailed` → `gateFailed`, then `routeError{err}` so `Reconcile` returns it as-is; a `gateFailed` error goes back unchanged; other `ensureRevision` errors reach `failPass` | `function.go:496-506`; M1 killed; `TestScenarioRevisionCreateRefusedWritesStatus` asserts `errors.Is` and the kept reason over two passes | yes |
| D6 / Exposes: the reasons on Ready only when nothing serves; a running worker serves on, none replaced | ADR-0161's `gateFailed` (no `zeroReplicas`); `TestRevisionMissingCountsServingWorkers` (Ready with 1 listening, Degraded with 0, no new create) | yes |
| D6: `gateFailed` requeues at the supervision period | set in both literals; asserted only when a worker runs, where ADR-0161's `gateFailed` forces the period anyway (M2) | yes, untested when nothing serves (n1) |
| D7: `pinnedMember` returns `errRevisionMissing` or Conflict; `sameKeyFunctions` keeps the serving revision or leaves the member out | `pool.go`; M3 killed; `TestScenarioPooledMissingRevisionLeftOut` | yes |

Review checklist: both items hold (no Revision write in OpenAPI or routes, 405 for both roles, SDK and CLI refuse
before sending; `revisionName` alone forms names, the one Revision `Update` is the adoption, no path touches a
`revOther`, every scenario has a passing test).

## Findings

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **n1 [model]**: No test pins the `RevisionMissing` gate's supervision-period requeue when nothing serves. Mutant M2
  removes `requeue: r.supervisionPeriod` from that literal and the whole `internal/function` package still passes.
  `TestRevisionMissingCountsServingWorkers` asserts `RequeueAfter == testPeriod`, but only with a running worker,
  where ADR-0161's `gateFailed` sets the period itself; `failMissingRevision` discards the result of the failing pass.
  Decision 6 states the requeue. A one-line `require.Equal(t, testPeriod, res.RequeueAfter)` in
  `TestScenarioMissingRevisionFailsClosed` would pin it.
- **n2 [model]**: The doc comment of `routeError` (`function.go:632`) still says it is "an error programming the
  routes after the pass's status write succeeded". It now also carries `errRevisionStampFailed` after the gate's
  status write. The call site explains the use and Decision 6 asks for it, but the type comment no longer describes
  every use; "an error after the pass's own status write succeeded" would.

## ✅ Verified correct (keep it)

- **A clean ADR-0161 adaptation.** The fallback fields are removed instead of kept beside the new behaviour, and the
  stamp error rides `routeError` so `failPass` cannot overwrite the gate's reason. M1 proves the test guards it.
- **The new gate test.** `TestRevisionMissingCountsServingWorkers` covers both ADR-0161 cases (listening and
  running-not-listening) and checks that no worker is replaced.
- **A real restart.** The long-name scenario builds a new `Reconciler` over the same store and reuses the existing
  `fakeRuntime.forget()` instead of a new helper.
- **Retry kept honest.** `TestScenarioRevisionCreateRefusedWritesStatus` runs two passes and checks the reason
  survives the retry.
- Everything loop 1 named as strong still holds: the exact Contracts code, the minimal API removal with no orphan
  schema, the hit-counting SDK test, the complete fail-closed assertions, the ADR-0158 fold into
  `sameKeyFunctions`, and the one-PR scope with no shim or `go.mod` change.

## Tracking

The ADR is still `Accepted` and its file is unchanged on the branch (the diff touches nothing under `docs/`). The
brief puts the `Accepted → Reviewing → Implemented` moves and the F13 feat-row update in the wave's docs PR, so this
review stamps nothing.

## Recommendation

**pass.** The wave's docs PR can stamp ADR-0172 `Implemented` and set the F13 token to `implemented` once the PR gate
(`scripts/agent/gate.sh`, `just ci-full`) is green. n1 and n2 are optional one-line fixes for the builder.

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
  "model_attributed": 2,
  "dod_passed": 8,
  "dod_total": 8,
  "report": "docs/reviews/adr-0172-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2 (003348fa on 5881659b, adapted to merged ADR-0161: zeroReplicas fallback dropped, RevisionStampFailed returned as routeError after the gate write, new TestRevisionMissingCountsServingWorkers); loop-1 m1 resolved (real restart), m2 env flake not reproduced; all 7 Decisions + Contracts hold; build/vet/lint clean also Linux; 4 touched pkgs -race ok; 10/10 scenario tests pass incl. e2e RefusedEditKeepsDigest; mutants 2/3 killed (routeError wrap, pooled missing filter). n1 [model] RevisionMissing supervision-period requeue untested when nothing serves (mutant survived); n2 [model] routeError doc comment no longer covers the stamp-error use."
}
```
