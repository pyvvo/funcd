## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0170 implementation, model: claude-opus-5-5)

Work reviewed: the single commit `b8210e3e` (`feat(gc): collect the children of deleted owners and guard ResourceGroup
deletes`), `git diff origin/main...HEAD`, 33 files, +2391/−55. This gate did not stamp the ADR and did not edit any
document. The ADR is not in the repository yet (it lands with the ADR PR), so the `Accepted → Reviewing` bump and the
feat-row move are not part of this commit. That is a process fact and is not scored.

### Verification run (in the worktree, through `scripts/agent/d`)

| Check | Command (abridged) | Result |
|---|---|---|
| build (darwin) | `go build ./...` | exit 0 |
| build (linux) | `GOOS=linux go build ./...` | exit 0 |
| vet | `go vet` on the touched packages + `./pkg/funcd/`, and again with `-tags e2e` | exit 0, exit 0 |
| vet (linux) | `GOOS=linux go vet` on the same set | exit 0 |
| lint | `go tool golangci-lint run` on the touched packages | `0 issues.` exit 0 |
| lint (linux) | the host-built golangci-lint binary with `GOOS=linux` | `0 issues.` exit 0 |
| unit tests | `go test -race -count=1` on internal/gc, controlplane(+admission), workflow, services/identity, function, site, platform/config, pkg/sdk, cmd/funcdctl, cmd/funcd, api | all `ok`, exit 0 |
| e2e scenarios | `go test -race -tags e2e -v -run 'TestScenario(WorkflowDelete…\|Resourcegroup)' ./pkg/funcd/` | 13/13 `--- PASS`, none skipped, exit 0 |
| benchmark | `go test -bench BenchmarkSweep -benchtime 3x -benchmem ./internal/gc/` | 37.2 ms/op, 19.8 MB/op, 300 876 allocs/op (10 000 children, 1 000 encrypted Secrets, all owners live) |

No touched package has a Linux-only file (`*_linux.go`), so no Docker run was needed: the Linux build, vet and lint
above cover the Linux side. `just ci-full` and the Lima lanes (Implementation plan step 5) were not run here, as
instructed; they belong to the per-PR gate.

**Overlay mutants (`go test -overlay`, each must fail a test):**

| Mutant | Line changed | Result |
|---|---|---|
| m1 | `internal/gc/gc.go` `ownerLive`: `live = owner.GetObjectMeta().UID == ref.UID` → `live = owner != nil` (a re-created owner adopts the old children) | killed: `TestCollectNamespaceDeletesChildrenOfDeletedOrReplacedOwners` fails |
| m2 | `internal/function/function.go` `ensureRevision`: drop the `retireStale` call before `store.Create(rev)` | killed: `TestScenarioRecreatedFunctionRetiresStaleWorkers` fails (`the deleted Function's worker left the runtime`) |
| m3 | `internal/controlplane/handlers.go` `deleteMember`: `h.deleteObjIf(…)` → `h.store.Delete(…)` (force bypasses authorization, admissions and the ADR-0147 lock) | killed: `TestResourceGroupForceStopsAtProtectedMember` and `TestResourceGroupForceRacesLinkWrites` fail |

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor

- **Minor 1: the controller-ref lookup is duplicated across the change.** Attribution: model. `hasController` in
  `internal/controlplane/handlers.go` (the loop at line 416) is the same function as `controlled` in
  `internal/controlplane/admission/resourcegroup.go:38`, and `controlplane` already imports `admission`.
  `internal/workflow/reconcile_workflow.go` walks the same ref list three times (`controlledBy`, `hasController`,
  `ownerUID`). `gc.controllerRef` and `identity.secretControl` repeat the same loop. Fix: one
  `controllerRef(refs) (v1.OwnerReference, bool)` helper, for example a method on `v1.ObjectMeta`, with the other
  helpers built on it. The ADR-named functions (`controlledBy`, `hasController`, `secretControl`) can stay as thin
  wrappers. Not blocking.
- **Minor 2: the unit test for the watch path does not isolate the watch.** Attribution: model.
  `internal/gc/gc_test.go:226` `TestRunCollectsADeletedOwnersChildrenFromItsWatch` starts `Run` through
  `runCollector`, which returns at once, and deletes the four owners right away. `Run`'s start sweep can therefore
  collect the children instead of the `Deleted` events. A regression that stops recording watch events could still
  pass the unit lane (`just ci`). This comes from reading the code; no mutant was run for it. The e2e scenarios
  (`pkg/funcd/gc_e2e_test.go`, `WithGCSweepInterval(time.Hour)` with 5 s windows) do pin the watch path, but only in
  `just ci-full`. Fix: before the deletes, wait for the start sweep to finish, for example by waiting until a seeded
  leftover is collected.

### ✅ Verified correct (keep it)

- **Contracts surface matches the ADR exactly.** `internal/gc` (`DefaultInterval`, `Pair`, `Pairs()` with Revision
  last, `Deps`, `New` with Store required and `<0` ⇒ `fault.Invalid`, `Run`, `CollectNamespace`); workflow
  `controlledBy`/`hasController`/`notOwnedError`/`pruneFunctions` and `NewMaterializer(…, supervisionPeriod)`;
  `identity.ReconcilerDeps.SupervisionPeriod` and `secretControl`; `function.retireStale`; `admission.Members` and
  `NewResourceGroupDeletionProtectionAdmission`; `deleteObjIf`; `Handlers.DeleteResourceGroup(…, force bool)`;
  `deleteResourceGroupInput` with the `force` query; `controlplane.Deps.Collector` and `OwnerCollector`, with
  `NewStoreHandlers` taking the collector; `sdk.Client.Delete(…, opts ...DeleteOption)` and `sdk.Force()`;
  `config.Controller.GCSweepInterval` (env `FUNCD_CONTROLLER_GC_SWEEP_INTERVAL`, default `"5m"`);
  `funcd.WithGCSweepInterval`. The OpenAPI document was regenerated and `api/openapi` tests pass.
- **Decision 1.** The owner is judged by Get with the child's namespace when the ref names none: live only for the same
  UID. The delete carries the child's resourceVersion. NotFound counts as collected. A Conflict re-Gets and re-judges
  with a fresh, non-memoized owner Get (`TestConflictRejudgesWithAFreshOwnerRead`, both branches). A second Conflict
  waits for the next sweep. Memoization is per pass, keyed by the full (kind, ns, name, UID). List errors are logged
  and joined.
- **Decision 2.** The watches open before the start sweep, so nothing falls into a gap. Deleted keys are coalesced,
  a re-watch resumes from the last resourceVersion, and Unavailable re-watches from now and requests a sweep.
  `Run` errors only when a start watch fails, and funcd logs it as "garbage collector stopped". The code also requests
  a sweep when a stream closed before it delivered any event (`last == 0`). That is a safe superset of the ADR's
  wording, because such a stream cannot resume without a gap. It is documented in the function comment, and it is
  not a finding.
- **Decision 4.** `ensureFunction` refuses unless its controller ref names this Workflow's kind and name (any UID).
  `ensureKVStore` also keeps an unowned store (#149). Both write their own ownerRefs, so a re-created Workflow
  re-stamps the new UID (`TestScenarioRecreatedWorkflowTakesNewUid`, two `CollectNamespace` passes delete nothing).
  A refusal performs no write on the foreign object, sets Ready=False `FunctionNotOwned` or `KVStoreNotOwned`, and
  requeues after the supervision period. The Identity reconciler re-issues the Secret in place for another UID and
  stamps the current ref (new `secretAccessKey` and `catalogToken`; the old key stops resolving, checked through
  `NewExternalKeys(…).Lookup`). Any other Secret gets `SecretNotOwned` with no write: the resourceVersion, ownerRefs
  and data are unchanged.
- **Decision 5.** `pruneFunctions` runs only after steps 1–3 succeed. It deletes only Functions controlled by this
  Workflow's kind, name and UID that are no image step's name, each with its resourceVersion. A step changed to a
  `function:` reference is pruned, KVStores never are (`TestPruneFunctionsDeletesRemovedStepsOnly`), and a Function
  under another UID is left alone.
- **Decision 6.** `retireStale` runs on every create path of `ensureRevision`, both after `dropRevision` and when no
  Revision existed. `dropRevision` now only deletes. Mutant m2 confirms the scenario test depends on it.
- **Decisions 7–8.** The admission is Validating, handles ResourceGroup Delete only, and sets `ReadsNamespace` so the
  ADR-0147 lock is taken. Members are the namespaced kinds other than ResourceGroup, without a controller ref.
  A refusal names up to five `Kind/name` plus the count. Force checks in order 403 → 503 → 404
  (`TestResourceGroupForceChecksInOrder`). Sensors are deleted first. Every member goes through `deleteObjIf` with the
  listed resourceVersion, under a per-member lock. A 409 triggers one re-Get and a retry if the object is still a
  member. `CollectNamespace` runs on every pass, and its errors stop the loop. After a pass that deleted nothing, the
  loop returns the first 409 or deletes the group through the normal path. The 40-pair race of forced deletes against
  link PUTs reuses the ADR-0147 harness (`racePairs`) and passes under `-race`. Mutant m3 shows that the race test and
  the protected-member test depend on `deleteObjIf`.
- **Decision 9.** `parseDuration("controller.gcSweepInterval", …, 0, false)`. `TestGCSweepIntervalMustBePositive`
  refuses `0`, `0s`, `-1m` and `bogus` with `fault.Invalid` naming the key. `examples/funcdconfig.yaml` documents the
  key commented out with "default: 5m", and the `cmd/funcd` example-coverage test passes.
- **All 19 scenarios have a named, un-skipped, passing test.** 13 run in `pkg/funcd/gc_e2e_test.go` (tag `e2e`). The
  others are `TestScenarioLiveOwnerChildrenNeverCollected` (gc), `TestScenarioRecreatedWorkflowTakesNewUid`
  (workflow), `TestScenarioRecreatedFunctionRetiresStaleWorkers` (function), the two Identity scenarios (identity)
  and `TestScenarioForceOnlyOnResourceGroup` (funcdctl: refused before any request, and `delete rg --force` reaches
  the route). `TestScenarioCrashBeforeCollectionSwept` uses `shortDataDir` on a Badger store with no collector running.
- **`TestPairsCoverEveryControllerRef`** scans the non-test sources for ownerRef-list writers and matches them against
  `gc.Pairs()`. The scan finds four files and five pairs, and Revision is last. A grep of the tree finds no other
  writer.
- **Plan step 2 edits** are exact: `internal/site/reconcile.go` and `TestScenarioDeletedSiteReclaimsNothing` drop "no
  collector exists" and keep the assertion; `api/types/v1alpha1/site.go` says "(needs a drain-then-delete teardown,
  ADR-0139)".
- **Wiring.** `pkg/funcd` builds the collector after the controller, runs it in its own goroutine beside the
  controller, passes it to `Deps.Collector`, and registers the admission. ADR-0002 conventions hold: ctx comes first,
  logging uses slog only, `api/fault` kinds are used throughout, there is no `panic`, and no `any` appears in the new
  APIs.

### Definition of Done
7 / 7 ADR Review-checklist items hold: Decisions 1–9 with a passing test per scenario; the Pairs test and the writer
match; Decision 4 refusals make no write; force never bypasses protections (mutant m3); forced deletes run under the
ADR-0147 lock through `deleteObjIf`; `0` and negative intervals are refused naming the key; no identity or path leak.
Generic DoD: build, vet and lint pass on darwin and linux, and the touched packages pass under `-race`. Outside this
commit: the ADR status and feat-row tracking (the ADR is not in the repository yet), the acceptance-time back-links
(plan step 6), the release note (plan step 7), and the `BenchmarkSweep` numbers for the PR description (the numbers
above can be used). `just ci-full` and the Lima lanes are left to the PR gate.

### Model scorecard
To record: claude-opus-5-5 on ADR-0170 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 7/7.

### Recommendation
Ship it through the PR gate (`scripts/agent/gate.sh`, then CI). Put the `BenchmarkSweep` result in the PR
description. The two minors are optional follow-ups for the builder: one shared controller-ref helper, and a watch
unit test that waits for the start sweep.

```json
{
  "adr": "0170",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 2,
  "dod_passed": 7,
  "dod_total": 7,
  "report": "docs/reviews/adr-0170-implementation-claude-opus-5-5.md",
  "notes": "pass: all 19 scenarios pass (13 e2e + 6 unit, -race); build/vet/lint green on darwin and linux; 3/3 overlay mutants killed (gc UID judgment, retireStale, force via deleteObjIf); minor(model): controller-ref lookup duplicated (controlplane hasController = admission controlled; 3 loops in workflow); minor(model): watch-path unit test races the start sweep (e2e covers it)"
}
```
