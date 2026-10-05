## Verdict: pass — 0 blockers, 0 majors, 4 minors  (ADR-0147 implementation, model: claude-opus-5-5)

Scope reviewed: funcd branch `feat/adr-0147-admission-nested-cap` (1 commit, 22 files, +1443/−4) and
pyvvo/funcd-typescript branch `feat/adr-0147-admission-nested-cap` (1 commit, 12 files, +262/−3), each as
`git diff origin/main...HEAD`. Both sides were verified together through a local `go.work` over the two
worktrees. The `go.work` was never committed and was removed afterwards, and both trees are clean. This branch
covers steps 1 and 2 of the ADR's Implementation plan. Step 3 (`go get` the tag, `scripts/lanes.yaml`, the
`e2e/fn-to-fn.venom.yml` cases) is a later funcd PR by design.

### Minor
- **Misplaced doc comment** · attribution: model · `pkg/funcd/funcd.go:1466-1476`. `invokeMeter` was inserted
  between the `storeReader` doc comment and `type storeReader`. The two-line `storeReader adapts store.Store …`
  comment now runs straight into `invokeMeter`'s doc, so godoc for `invokeMeter` begins with the storeReader
  text, and `type storeReader` is left undocumented. Fix: move `invokeMeter` (with its comment) above the
  storeReader comment block.
- **The contract test "the cap's map empties" is not asserted** · attribution: model ·
  `internal/workernode/local/nestedcap_test.go`. The ADR's Test plan lists it, but no test inspects
  `nestedCapInvoker.inFlight`, which `nsLocks` does have (an `entries()` check). Mutant M4 replaced
  `delete(n.inFlight, target)` in `done()` with a no-op, and `go test ./internal/workernode/local/` still gave
  `ok`, so a per-target map that grows without bound survives the suite. Fix: an internal test (or an
  `export_test.go` accessor) that asserts the map is empty after the loop and fan-out runs.
- **A cap off-by-one makes one test hang instead of fail** · attribution: model ·
  `TestNestedCapPerTargetAndReleased`. Under mutant M2 (`>=` → `>`), the second call to H is admitted into the
  blocking fake and blocks with no timeout guard, so the package run hangs until the 10-minute `go test`
  timeout. The scenario tests still kill M2 quickly (see below), so this is test ergonomics only. Fix: run the
  second call in a goroutine with a `select`/timeout, or give the fake a context deadline.
- **Tracking not advanced in this branch** · attribution: env (workflow) · ADR-0147 still reads
  `Accepted (2026-10-05)`, and FEAT-0001/F108 reads `accepted` (`docs/feat/0001-feat-v1.1.md:66`). Neither diff
  touches `docs/`. In this workflow the orchestrator owns the doc edits (this reviewer was told not to stamp),
  so the `Accepted → Reviewing → Implemented` moves and F108 `→ implemented` must still be made in the
  docs/ledger step. F108 is marked not user-facing, so no board card is needed.

### ✅ Verified correct (keep it)
- **Gate, funcd** (via `scripts/agent/d`, with `go.work` over the TS worktree):
  - `go build ./...` exit 0.
  - `go test -race -count=1` over `internal/controlplane/...`, `internal/workernode/local/...`,
    `internal/platform/config/...`, `pkg/funcd/` and `cmd/funcd/` exit 0. All packages `ok`.
  - `go vet ./...` exit 0, and `go vet -tags e2e ./pkg/funcd/` exit 0.
  - golangci-lint `0 issues` on the touched packages and on `pkg/funcd` with `--build-tags e2e`.
  - Linux: `GOOS=linux` build exit 0, vet exit 0, and lint `0 issues`.
  - `go mod verify`: "all modules verified".
- **Gate, funcd-typescript**: `scripts/agent/d just ci` exit 0 (install, `biome ci`, typecheck, tests, build,
  go-check) and an empty `git status --porcelain` afterwards. The committed `hold.mjs`/`fanout.mjs` are fresh.
  `fn-to-fn` example tests: 5 pass, 0 fail.
- **Mutants: funcd 3/3 killed, TS 2/2 killed** (plus M4 above, which survived). Every mutant ran through
  `go test -overlay` or a scratch copy, so the work was never edited.
  - M1, `lockFor` always a no-op: `TestScenarioLinkCycleRaceRejected`, `TestScenarioDanglingLinkRaceRejected`
    and `TestScenarioQuotaRaceRejected` all FAIL. The race tests really detect the unlocked state.
  - M2, cap `>=` → `>`: `TestScenarioInflightLoopStopped` FAILs (expected 20, actual 22) and
    `TestScenarioInflightFanoutOverCap` FAILs.
  - M3, refusal log/response branch disabled: `TestNestedCapRefusalResponseAndLog` FAILs.
  - TM1, `fanout.ts` treats any error as a refusal: "fanout fails on an error that is not a nested-cap refusal"
    fails.
  - TM2, `fanout.ts` stops counting refusals: "fanout calls the peer link n times … counts the nested-cap
    refusals" fails.
- **Decision 1 / Contracts (admission)**:
  - `NamespaceReading` and `Pipeline.ReadsNamespace` are as specified, scanning both phases and requiring
    `Handles` together with the marker.
  - Exactly four admissions are marked (`linkValidity`, `linkDeletionProtection`, and `kvStoreQuota`/`bucketQuota`
    only while `maxPerNamespace > 0`). `TestPipelineReadsNamespace` covers the four, the three negatives the ADR
    names, and the disabled quotas.
  - `nsLocks` matches the Contracts struct. A cancelled wait returns
    `fault.Wrapf(ctx.Err(), fault.Unavailable, "controlplane.admit", …)` and drops its ref. The entry is deleted
    at 0 refs, and the tests assert independence, cancellation with the holder keeping the slot, and that the
    map empties.
  - The lock is taken in `createObj` (after stamping and name generation), `replaceObj` and `deleteObj`, after
    authorize and before the `Old` fetch and `Admit`. It is released by `defer` on every return path, and
    unmarked writes get a no-op unlock.
  - `NewStoreHandlers`' signature is unchanged. No existence admission was added, and the store imports
    nothing new.
- **Decision 3 / Contracts (cap)**:
  - `NewNestedCapInvoker` uses one map under a mutex and deletes the entry at 0 in code. A refusal skips inner
    and returns `fault.ResourceExhaustedf(NestedCapOp, …)` with the exact ADR message, which the unit test
    asserts verbatim.
  - The `funcd.invoke.nested.refused` counter carries `namespace`/`function` attributes, checked with a manual
    reader.
  - `NewHandler` logs `fn-to-fn invoke refused: nested in-flight cap` at Warn instead of (never alongside) the
    failure line, and writes 429 through `fault.WriteProblem`, which sets no `Retry-After` (`api/fault/problem.go:58`).
  - The cap wraps only `local.NewInvoker(dpHolder)` in `pkg/funcd`. `dpHandler` is untouched, and no limiter
    was added on the nested chain (#87).
- **Decision 4**:
  - `invoke.maxNestedInFlight` / `FUNCD_INVOKE_MAX_NESTED_IN_FLIGHT` is `validate:"min=0"`. File, env and a
    negative value are tested, and the error names `invoke.maxNestedInFlight`.
  - `WithNestedInFlightCap` rejects negative values with `fault.Invalid`, and 0 maps to
    `defaultNestedInFlightCap = 10`.
  - `cmd/funcd` passes the option through. `invokeMeter` falls back to a no-op meter when telemetry is nil.
  - `examples/funcdconfig.yaml` gains the commented `# invoke:` block with the ADR's exact line.
- **Scenarios**: all 9 have a test of the same name.
  - The unit tests for the 3 races and 4 in-flight scenarios pass under `-race`. Each race test sends 40 pairs
    released on a closed channel and keeps its sequential control; the dangling-link test checks both orders.
  - The `pkg/funcd` e2e file adds all 9 scenarios, with the races on both the memory and Badger metastores and
    the in-flight scenarios through the real Node shim. That file compiles, vets and lints under `-tags e2e`.
    It was not executed here, because this review was told to run no e2e; the PR gate's `just ci-full` runs it.
- **Language repo vs ADR step 2**:
  - `src/hold.ts` and `src/fanout.ts` behave as specified (`{ok, refused, detail}`, a refusal recognized by
    `workernode.local.nested-cap`, any other error rethrown).
  - The built `.mjs` files, manifests, and `invoke.maxNestedInFlight: 2` in `examples/fn-to-fn/funcdconfig.yaml`
    are present.
  - The `shim/src/invoke.ts` change is to its doc comment only: it lists 429. Shim behaviour is unchanged.
  - All new YAML uses block style.
- **safeBeforeFuncd claim `pyvvo/funcd-typescript: false` is true.** funcd's loader uses `yaml.UnmarshalStrict`
  (`internal/platform/config/config.go:341`). Loading the TS example's `funcdconfig.yaml`:
  - With origin/main's `config.go` (by overlay), it fails with `json: unknown field "invoke"`.
  - With the branch's loader, it passes.
  So the TS change must not reach a funcd that lacks the key, which matches the ADR's step order.
- **ADR substance**: unchanged. Neither diff touches `docs/`.

### Definition of Done
9 / 11 items hold. The 11 items are the ADR's 3 Review-checklist items and the ADR's 8 DoD items.
- Review checklist, 3/3 hold: the lock, the cap, and the scenario names, race controls and block-style YAML.
- DoD, these hold:
  - build;
  - lint;
  - tests on the touched packages under `-race` (the repo-wide `go test ./...` is the gate's run, by the
    project rule);
  - `go mod verify`;
  - every scenario has a named test (unit tests passing, e2e compiled);
  - no identity or path leak.
- DoD, not verified here: `just ci-full`, and the e2e execution it includes, was excluded from this review and
  belongs to the PR gate. `just lima-example fn-to-fn` was excluded (no Lima) and belongs to ADR step 3.

### Model scorecard
To record: claude-opus-5-5 on ADR-0147 (implementation) → pass, 0/0/4, 3 model-attributed, DoD 9/11. The row
is below. The orchestrator writes the ledger and the scorecard.

### Recommendation
Sign-off is not blocked. Before the PR merges:
- The builder can fix the three model minors cheaply: move the doc comment, add a map-empties assertion for
  the cap, and add a timeout guard in `TestNestedCapPerTargetAndReleased`.
- The PR gate must run `just ci-full` so that the `pkg/funcd` e2e scenarios execute once.
- The docs step must advance ADR-0147 and F108.
- ADR step 3 (pin the TS release tag, lanes, Venom cases) follows the TS release.

```json
{
  "date": "2026-10-05",
  "adr": "0147",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 4,
  "model_attributed": 3,
  "dod_passed": 9,
  "dod_total": 11,
  "report": "docs/reviews/adr-0147-implementation-claude-opus-5-5.md",
  "notes": "all contracts match; unit scenario tests pass under -race; vet/lint (also Linux, also -tags e2e) green; TS just ci green with fresh bundles; 5/6 mutants killed (lock no-op, cap off-by-one, refusal branch, 2 TS); safeBeforeFuncd=false confirmed (main rejects the TS example config: unknown field invoke); storeReader doc comment displaced by invokeMeter, cap map-empties contract test missing (surviving mutant), off-by-one hangs TestNestedCapPerTargetAndReleased (model); ADR/F108 still Accepted, docs step owned by the workflow (env); e2e and Lima not run by instruction, left to the PR gate and ADR step 3"
}
```
