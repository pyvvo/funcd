# ADR-0152 implementation review (claude-opus-5-5, loop 1)

- **ADR**: docs/adr/0152-runtime-worker-owner-kind.md (status `Accepted`; the substance is unchanged by the branch)
- **Work**: branch `feat/adr-0152-worker-owner-kind`, one commit `6040ff2a` over `origin/main` (22 files, +751/-51); the commit message carries `Fixes #18`
- **Model**: claude-opus-5-5
- **Verdict**: **pass**: 0 Blockers, 0 Majors, 1 Minor (model). Two Definition-of-done items are unverified for environment reasons (the duckdb Lima lane, and the Linux-only containerd tests, which compile here but run only on CI).

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` (darwin) | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` on `internal/function/... internal/provider/... internal/runtime/... pkg/funcd/...` (darwin and linux) | exit 0 / exit 0 |
| `GOOS=linux go test -c ./internal/runtime/containerd` (compiles the Linux-only tests, including `ownerkind_linux_test.go`) | exit 0 |
| `go test -race -count=1` on the same four package trees | exit 0 (function 10.3s, provider 2.3s, runtime/containerd, ctrmanager, embedimg, process, provision, pkg/funcd all `ok`) |
| golangci-lint (darwin) on the same packages | `0 issues.` |
| golangci-lint (linux, the host-built binary with `GOOS=linux`, as `scripts/agent/gate.sh` does) | `0 issues.` |
| `gofmt -l` on the changed Go files | empty |
| Tree after the run | clean (`git status --porcelain` empty) |

Not run, as instructed: e2e, `go test ./...`, and Lima (the duckdb lane).

### Overlay mutants (`git show`-free: `sed` copy + `go test -overlay`)

| # | Mutant | Tests run | Result |
|---|---|---|---|
| M1 | `internal/function/function.go` `namedInstances`: drop `in.OwnerKind == v1.KindFunction &&` | `-run TestScenario ./internal/function/` | **killed**: `TestScenarioSameNameFunctionAndCatalogStayReady`, `TestScenarioFunctionPassNeverTouchesEngine` FAIL; `TestScenarioOwnerKindAcrossDaemonRestart` passes (see Minor 1) |
| M2 | `internal/provider/runtime.go` `namedInstances`: drop `in.OwnerKind == r.ownerKind &&` | `-run TestScenario ./internal/provider/` and the three owner-kind scenarios in `./internal/function/` | **killed**: `TestScenarioProviderNeverTouchesFunctionWorker`, `TestScenarioOwnerKindAcrossDaemonRestart` ("only Function and CatalogService workers are listed": 1 kind instead of 2), `TestScenarioSameNameFunctionAndCatalogStayReady` FAIL |
| M3 | `internal/runtime/process/process.go` `Create`: disable the cross-kind `fault.Conflict` check | `-run Contract ./internal/runtime/process/` | **killed**: `TestProcessDriverContract/worker-id-of-another-kind-conflicts` FAIL |

Each of the four scenario tests fails under at least one mutant of the change, as the Review checklist asks.

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor

1. **`TestScenarioOwnerKindAcrossDaemonRestart` checks the kind filter only on the provider side after the restart** (`internal/function/owner_kind_test.go`, the restart test). After `forget()`, the test reconciles Function `lake` to Ready **before** the provider re-creates its engine. As a result, no post-restart Function pass ever lists a same-name engine, and the test still passes under M1 (the Function-side filter removed). The other two Function scenarios do kill M1, so the behaviour is covered. The restart scenario on its own, however, does not prove "each creating only its own workers" for the Function reconciler. Converging the engine first, as the daemon can do, or running one more Function pass after `convergeReady`, would cover both directions. *Attribution: model.*

Note (not scored): no test exercises the new `OwnerKind != KindFunction` guard in `reclaimOrphanPools` (`internal/function/pool.go:461`). A non-Function worker with the `__pool__` prefix cannot exist, because CatalogService names are DNS-1123 labels, so the guard is defence in depth that the ADR's `List`-reader table requires. The ADR's test plan names no test for it.

### Environment (recorded, not scored)
- The duckdb Lima lane cases `same-name-function-and-catalog-stay-ready` and `owner-kind-across-daemon-restart` were not run, because Lima is out of scope for this review. The suite reuses idioms the lane already uses: `funcdctl get catalogservice`, `ctr … task ls | awk` for `lake-r0`, and the env-echo `daemon-restart-recovers` kill and `systemd-run` steps, which are verbatim. Its container IDs agree with the code: a replicas change bumps the generation, so `revisionName` gives `lake-2` and the solo container is `lake-2.r0`; the engine is `lake-r0`. The `sed` and `printf` edits fit `consumer.yaml`: the file has `  name: catalog-reader` and `  replicas: 1`, and ends with a newline.
- `TestOwnerKindLabelAndConflicts` and `TestContainerIDsDisjointAcrossKinds` are `//go:build linux`. They compile and pass Linux vet and lint here, and they run on Ubuntu CI.
- Tracking: the ADR is still `Accepted` and the F12 feat row is unchanged. The orchestrating workflow does the `Accepted → Reviewing → Implemented` stamping in a docs PR per wave, so this review records no finding for it.

## Contracts and Review checklist vs code

| Item | Evidence | Holds |
|---|---|---|
| `WorkerSpec.OwnerKind`, `Instance.OwnerKind` (`v1alpha1.Kind`), placed as in Contracts | `internal/runtime/runtime.go` | yes |
| `Create` doc comment gains the ADR-0152 sentence | `internal/runtime/runtime.go` `Runtime.Create` | yes, word for word |
| Empty `OwnerKind` → `fault.Invalid` in every driver and fake | process `Create`; containerd `Create` (after the Image check, before any allocation); function fake and provider fake `Create`; contract `worker-owner-kind-required` also asserts that a refused Create keeps no instance | yes |
| Another kind's ID, live or exited → `fault.Conflict`; the same kind still replaces its exited worker (ADR-0142) | process: the check precedes the terminal check, as the Implementation plan says; containerd: an in-memory `d.instances[id]` check, and since `Stop` keeps the entry with `released=true`, exited is covered; contract `worker-id-of-another-kind-conflicts` (live, exited, then a same-kind re-create with the same ID) | yes |
| containerd label `funcd/owner-kind`; `reclaim` takes the kind, keeps and refuses another kind's labelled leftover, and reclaims an unlabelled one | `containerd_linux.go` `ownerKindLabel`, the labels map, `reclaim(…, kind)` reads `c.Labels` before `discard`; `TestOwnerKindLabelAndConflicts` (second driver, leftover kept with its label) | yes |
| Every driver returns `OwnerKind` from `Create`/`Status`/`List` | process `snapshotLocked` (used by all three); containerd's three `Instance` literals; contract `worker-owner-kind-round-trips` | yes |
| Every creator sets the kind | `workerSpec`'s three returns and `createPool` → `KindFunction`; provider `workerSpec` → `r.ownerKind`; a grep of all non-test `WorkerSpec{` literals finds no other creator | yes |
| Lookups keep only their kind | `function.namedInstances` and `reclaimOrphanPools` (`KindFunction`); `provider.namedInstances` (`Deps.OwnerKind`); `reconcileEgressWorkers` and the bench are untouched, as the table says | yes (M1/M2 killed) |
| `provider.Deps.OwnerKind`, `NewRuntime` → `fault.Invalid` when empty; `pkg/funcd` passes `KindCatalogService` | `internal/provider/runtime.go`; `pkg/funcd/funcd.go` `buildControlPlane`; the provider scenario asserts the `Invalid` | yes |
| CNI IDs unchanged | `workerNames` is not in the diff | yes |
| Fakes: the provider fake keeps `Revision`; the function fake's `forget()` drops everything | `internal/provider/runtime_test.go` `Create`; `internal/function/shim_test.go` `forget` (it now also clears `held`) | yes |
| Named tests from the Implementation plan exist | 3 contract subtests, 2 containerd tests, 3 function scenarios, 1 provider scenario, 2 lane cases | yes |
| The four scenario tests pass and fail without the change; the duckdb lane passes both cases | Go: passing, and each is killed by a mutant; lane: not run | Go part yes; lane unverified (env) |

## ✅ Verified correct (keep)
- The refusals sit where the ADR puts them: process before the terminal check, containerd before allocation, and the labelled-leftover check inside `reclaim` before `discard`. As a result, a refused Create leaves the other kind's worker and its container untouched. The tests assert this, including the post-refusal `StateRunning`, the kept leftover, and its label.
- The scenario tests are behavioural, not structural. A `kindGuard` wrapper records every Start, Stop and Remove that reaches the engine ID. The tests compare the engine's `CreatedAt` for "same engine worker" and check the upstream against the engine URL, and they run both the running and the exited engine.
- The scope is tight: no unrelated edits, every touched test only adds `OwnerKind` to spec literals, and the e2e additions reuse the lane's existing idioms.
- The comments state the reason and cite ADR-0152, without narration.

## Recommendation
Pass. Optionally harden the restart scenario (Minor 1) in a follow-up. Before the merge, the PR gate and CI must still run the Linux containerd tests and the duckdb lane.

```json
{
  "date": "2026-10-05",
  "adr": "0152",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 6,
  "dod_total": 8,
  "report": "docs/reviews/adr-0152-implementation-claude-opus-5-5.md",
  "notes": "all contracts match; darwin+linux build/vet/lint green, touched pkgs -race green, gofmt clean, 3/3 overlay mutants killed (function/provider kind filters, process cross-kind Conflict); restart scenario does not kill the Function-side filter mutant (model); duckdb lane not run, containerd Linux tests compile-only here (env)"
}
```
