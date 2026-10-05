## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0170 implementation, loop 2, model: claude-opus-5-5)

Work reviewed: the single commit `3b491721` (`feat(gc): collect the children of deleted owners and guard ResourceGroup
deletes`), `git diff origin/main...HEAD`, 36 files, +2396/−61, on top of `origin/main` `2aea7a11`. It replaces the
loop-1 commit `b8210e3e`. `git range-diff` between the two shows a rebase onto the newer main plus the two loop-1
fixes, and nothing else. This gate did not stamp the ADR and did not edit any document. The ADR is not in the
repository yet, so the `Accepted → Reviewing` bump and the feat-row move are outside this commit (process fact, not
scored).

### Loop-1 findings

| Loop-1 finding | Status | Evidence |
|---|---|---|
| Minor 1: the controller-ref lookup is duplicated | **resolved** | New `v1alpha1.ControllerOf(refs) (OwnerReference, bool)` in `api/types/v1alpha1/metadata.go`, with `TestControllerOf` (nil, no controller, controller after a plain ref). `admission.controlled`, controlplane `hasController`, `gc.controllerRef`, workflow `ownerUID` and the loops in `secretControl` and `function.ownedByAnother` are gone; the ADR-named `controlledBy`, `hasController` and `secretControl` stay as thin wrappers. A grep of the non-test sources finds no other loop that tests `.Controller`; `ControllerOf` has 10 callers. |
| Minor 2: the watch unit test races the start sweep | **resolved** | `Collector.startSwept` (set only through `internal/gc/export_test.go` `OnStartSwept`) runs once the start sweep returns. `TestRunCollectsADeletedOwnersChildrenFromItsWatch` waits for it, asserts that the start sweep kept every live owner's child, and only then deletes the owners; the next sweep is an hour away. Mutant m1 below confirms the test now fails when the watch stops recording. Three `-race -count=3` runs pass. |

### Verification run (in the worktree, through `scripts/agent/d`)

| Check | Command (abridged) | Result |
|---|---|---|
| build (darwin) | `go build ./...` | exit 0 |
| build (linux) | `GOOS=linux go build ./...` | exit 0 |
| vet | `go vet` on `./api/...`, `cmd/funcd`, `cmd/funcdctl`, `internal/controlplane/...`, `internal/{function,gc,platform/config,services/identity,site,workflow}`, `pkg/funcd`, `pkg/sdk` | exit 0 |
| vet (linux) | the same set with `GOOS=linux`, and `pkg/funcd` with `-tags e2e` | exit 0, exit 0 |
| lint | `go tool golangci-lint run` on the same set | `0 issues.` |
| lint (linux) | the host-built golangci-lint binary (`go tool -n golangci-lint`) with `GOOS=linux` | `0 issues.` |
| unit tests | `go test -race -count=1` on `./api/...`, internal/gc, controlplane(+admission), workflow, services/identity, function, site, platform/config, pkg/sdk, cmd/funcdctl, cmd/funcd | all `ok` |
| e2e scenarios | `go test -race -tags e2e -v -run 'TestScenario(…\|Resourcegroup)' ./pkg/funcd/` | 13/13 `--- PASS`, none skipped |
| benchmark | `go test -bench BenchmarkSweep -benchtime 3x -benchmem ./internal/gc/` | 38.3 ms/op, 19.8 MB/op, 300 876 allocs/op (10 000 children, 1 000 encrypted Secrets) |

No touched package has a Linux-only file (`*_linux.go`), so no Docker run was needed; the Linux build, vet and lint
cover the Linux side. `just ci-full` and the Lima lanes were not run here, as instructed; they belong to the per-PR
gate.

**Overlay mutants (`go test -overlay`, each must fail a test):**

| Mutant | Line changed | Result |
|---|---|---|
| m1 | `internal/gc/gc.go:174` `watchOwner`: `if ev.Type == store.Deleted {` → `… && kind == "" {` (the watch never records a deleted owner) | killed: `TestRunCollectsADeletedOwnersChildrenFromItsWatch` fails (loop-1 Minor 2 fix holds) |
| m2 | `internal/workflow/reconcile_workflow.go:245` `controlledByUID`: drop `&& r.UID == wf.UID` (the refactored line) | killed: `TestPruneFunctionsLeavesAnotherUIDsFunction` fails |
| m3 | `internal/controlplane/admission/resourcegroup.go:30` `Members`: `… && !owned` → `… && (owned \|\| !owned)` (controlled children count as members) | **survived**: `internal/controlplane/...` unit tests and the four `TestScenarioResourcegroup*` e2e tests all pass → Minor 1 below |

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor

- **Minor 1: Decision 7's "no controller ownerRef" clause is not pinned by any test.** Attribution: model. Mutant m3
  makes `admission.Members` count controlled children as members, and every unit and e2e test still passes. The
  clause matters: every reconciler copies the owner's `resourceGroup` onto its children
  (`internal/workflow/reconcile_workflow.go:276`, `:320`; `internal/services/identity/reconcile.go:166`;
  `internal/site/reconcile.go:316`; `internal/function/function.go:1325`). Without the clause, a group that holds a
  Function would refuse with its Revision listed and a wrong count, and force would delete step Functions directly
  instead of leaving them to the collector. `TestResourceGroupNonEmptyDeleteRefused` asserts `1 member` but runs
  with no reconciler, so no Revision exists. The e2e `TestScenarioResourcegroupNonemptyDeleteRefused`
  (`pkg/funcd/gc_e2e_test.go:366`) holds a ConfigMap `a` and asserts `ConfigMap/a`, while the scenario says a
  Function `a` and `Function/a`. Fix: make the e2e scenario hold Function `a` as written and assert the message
  names `Function/a` and `1 member` (its Revision `a-1` carries the group and a controller ref); or add an
  `admission` unit test that seeds a controlled child with the group. The code is correct; this is a test gap, and
  this loop refactored exactly that line without a test that would catch a regression there. Not blocking.

### ✅ Verified correct (keep it)

- **Both loop-1 fixes are clean and minimal.** `ControllerOf` keeps each caller's semantics: `controlledBy`,
  `hasController`, `controlledByUID` and `secretControl` decide exactly as before (m2 confirms the UID check is
  still pinned). `ownedByAnother` now takes the first controller ref and then checks its kind, which equals the old
  loop because only `ensureRevision` writes a Revision's ownerRefs. The `startSwept` hook is nil in production,
  costs one nil check, and is set only from `export_test.go`.
- **Everything verified in loop 1 still holds; the rebase changed nothing else.** The range-diff shows no other
  hunk. The Contracts surface (`internal/gc`, workflow, identity, function, admission, `deleteObjIf`,
  `DeleteResourceGroup(…, force)`, `deleteResourceGroupInput`, `Deps.Collector`/`OwnerCollector`,
  `sdk.Force()`, `controller.gcSweepInterval`, `WithGCSweepInterval`) matches the ADR. Decisions 1, 2, 4, 5, 6, 8
  and 9 hold as recorded in loop 1. The force path still goes through `deleteObjIf` under the ADR-0147 lock (the
  race and protected-member tests pass under `-race`).
- **All 19 scenarios have a named, un-skipped, passing test.** 13 run in `pkg/funcd/gc_e2e_test.go` (tag `e2e`);
  the other six are `TestScenarioLiveOwnerChildrenNeverCollected` (gc), `TestScenarioRecreatedWorkflowTakesNewUid`
  (workflow), `TestScenarioRecreatedFunctionRetiresStaleWorkers` (function),
  `TestScenarioIdentityNeverTakesAnothersSecret` and `TestScenarioRecreatedIdentityGetsFreshCredential` (identity),
  and `TestScenarioForceOnlyOnResourceGroup` (funcdctl). The diff adds no `t.Skip`.

### Definition of Done
7 / 7 ADR Review-checklist items hold: Decisions 1–9 with a passing test per scenario (Decision 7's code is correct;
its controller-ref clause lacks a test, Minor 1); the Pairs test and the writer match; Decision 4 refusals make no
write; force never bypasses protections; forced deletes run under the ADR-0147 lock through `deleteObjIf`; `0` and
negative intervals are refused naming the key; no identity or path leak. Generic DoD: build, vet and lint pass on
darwin and linux, and the touched packages pass under `-race`. Outside this commit: the ADR status and feat-row
tracking, the acceptance-time back-links (plan step 6), the release note (plan step 7), and the `BenchmarkSweep`
numbers for the PR description. `just ci-full` and the Lima lanes are left to the PR gate.

### Model scorecard
To record: claude-opus-5-5 on ADR-0170 (implementation, loop 2) → pass, 0/0/1, 1 model-attributed, DoD 7/7.

### Recommendation
Ship it through the PR gate (`scripts/agent/gate.sh`, then CI), with the `BenchmarkSweep` result in the PR
description. Minor 1 is an optional follow-up for the builder: make the e2e non-empty scenario hold Function `a`
and assert `1 member`, which kills m3.

```json
{
  "adr": "0170",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 7,
  "dod_total": 7,
  "report": "docs/reviews/adr-0170-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2 pass: both loop-1 minors resolved (one v1alpha1.ControllerOf helper; watch test waits for the start sweep, mutant confirms); 19 scenarios pass (13 e2e + 6 unit, -race); build/vet/lint green on darwin and linux; mutants 2/3 killed; minor(model): Decision 7's no-controller-ref member clause unpinned (m3 survives; e2e nonempty scenario uses ConfigMap/a, not Function/a)"
}
```
