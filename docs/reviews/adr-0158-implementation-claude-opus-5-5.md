## Verdict: changes requested — 0 blockers, 1 major, 3 minors  (ADR-0158 implementation, model: claude-opus-5-5)

Work reviewed: funcd `feat/adr-0158-pool-member-identity` (f73ee95a), funcd-typescript (949c956), funcd-python
(4ff9061), each `git diff origin/main...HEAD`. funcd was verified against both language worktrees through a local
`go.work` (removed after; all three trees clean at the end). The ADR is HELD, so no tracking or status check applies;
nothing was stamped.

### 🟡 Major 1 — four of the unit rules the Implementation plan names have no test  ·  attribution: model

Plan step 3 asks for "units for every rule of Decisions 1–5" and lists them; the Review checklist asks that "every
test the plan names asserts its behavior". Mapping the added and rewritten tests to that list:

| Plan-named unit | Test |
|---|---|
| key order and nil | `TestAccessHashOfIgnoresBindingOrderAndTreatsNilAsEmpty` |
| `restarting` vs pool liveness | `TestIssue72_SiblingThreadFaultKeepsMembersReady` (`setMember("crasher","restarting")` → Degraded) |
| per-row credentials, shared env from `self`, stable signature | `TestPoolManifestCarriesMemberEnv` |
| manifest refusals, no stale manifest | `TestWritePoolManifestRefusesUnsafeDir`, `TestPoolManifestDirIsEmptiedAndOwnedTempRemoved` |
| `status.pool` | `TestScenarioPoolSplitsByAccess`, `TestParsePoolRoundTripsString` |
| `RoutePool` records | `TestRoutePool{StoresRecordsUnderMember,DropsNonMember,FlushesMembers}`, `TestRouteIgnoresMember` |
| **owned table vs prefix** | **none**: only the pure `AccessHashOf` "owned data counts" assertion. Nothing pins `poolKeyFor`'s rule (`internal/function/poolaccess.go`, `poolKeyFor`): count a KV table only when it is both owned and bound, and count an owned prefix whether bound or not. The only `Owner:` in the test diff is in the e2e file. |
| **five Lists** | **none**: no test makes one of the five `accessIn` Lists fail and asserts that the pass fails and reclaims nothing. |
| **`failed` → `ready`** | **none**: no test moves a member from `failed` to `ready` on a later pool start and asserts Ready with no spec change. |
| **`failed` serving → Degraded** | **none**: `TestIssue355_…` covers `failed` before serving (ShapeInvalid) and `load timed out` while serving (CrashLoopBackOff). Nothing covers a plain load error in a pass that already serves, which is the `!v.serving` guard in `convergePooled` (`internal/function/pool.go`). |

Not a correctness finding: the code for all four rules reads correctly. But the plan names these tests, and nothing
guards the rules against regression. Fix (builder): add the four units in `internal/function`.

### Minor
- **The `go.mod` pin of both language releases is not there yet** · attribution: env (sequencing). Plan step 3 and
  "Done" ask for a funcd PR that `go get`s both tags. The tags do not exist until the language PRs merge and release
  (safeBeforeFuncd ordering). The integrator must add the pin before the funcd PR is queued.
  `GOWORK=off go build ./...` passes on the branch, so funcd builds against the currently pinned tags in the meantime.
- **`TestReclaimDuringRepairBackoffIsNotAShapeFailure` failed once** in the first full `go test -race ./internal/function/`
  run (`supervision_test.go:291`, expected Deploying, got Ready, "a wake inside the backoff waits"). The diff leaves
  this test unchanged. It passed 4/4 alone under `-race` on the branch and 4/4 on `origin/main`, and the whole package
  then passed on a rerun (exit 0). It looks like a timing flake that predates this work · attribution: env.
- **The cached dev env of both language repos was stale.** `scripts/agent/d just ci` exited 127: `biome` and `ruff`
  were not found because the cached `nix print-dev-env` points at store paths that are no longer on disk. Both repos'
  `just ci` were then run through `nix develop -c`, and both passed · attribution: env.

### ✅ Verified correct (keep it)
- **funcd checks, with `go.work` over both language worktrees**:
  - `go build ./...`: exit 0. `go vet ./...` on darwin and Linux: exit 0.
  - `golangci-lint run ./...`: 0 issues. The Linux lint (gate form, `GOOS=linux` with the host linter binary) over `./internal/... ./pkg/... ./cmd/...`: 0 issues.
  - `go test -race -count=1` passed on all touched packages: `internal/auth/cedar`, `internal/funclog/...`, `internal/function` (on the rerun), `internal/pooling`, `internal/testkit/bench`, `internal/workernode/local`, `pkg/funcd`, `cmd/funcd` and `api/types/v1alpha1`.
  - `go vet -tags e2e ./pkg/funcd/` passed, so the e2e scenario tests compile: `TestScenarioPooledMember{KV,S3Identity,Logs}`, `TestScenarioPoolMemberLoadFailure` and `TestScenarioWorkflowStepsSplitBySecrets`. Per the brief, no e2e or Lima lane was run.
- **Language repos**, `nix develop -c just ci` (lint, typecheck/mypy, tests, build, Go embed check, and the
  clean-tree check that catches stale `shim.mjs`/`pool.mjs`):
  - funcd-typescript: TS EXIT 0, 102/102 shim tests.
  - funcd-python: PY EXIT 0, 188 shim tests plus every example's tests passed.
- **Mutants: 7 of 7 killed.**
  - Go (via `-overlay`):
    - Dropping the grants in `poolKeyFor` → `TestScenarioPoolSplitsByGrant` FAIL.
    - Serving a request with no `X-Funcd-Member` header as a member → `TestScenarioPoolRefusesOutsider` FAIL; the port recorder caught `store team-a/b invoke team-a/t`.
    - `RoutePool` accepting any named member → `TestRoutePoolDropsNonMember` and `TestRoutePoolFlushesMembers` FAIL.
  - TS:
    - The load bound never fails a first load → "a member whose import hangs … fails with load timed out" FAIL.
    - `kv.ts` drops the member header → "scenario pooled-member-kv: every local API call names its pool member" FAIL.
  - Python:
    - `kv.py` drops the header → `test_scenario_pooled_member_kv` FAIL.
    - A load timeout reads ready → `test_pool_member_whose_import_hangs_fails_with_load_timed_out` FAIL.
- **Contracts**:
  - `PoolKey.AccessHash`, `String`/`ParsePool` (three non-empty segments), `accessDoc`/`accessLink` (link timeout excluded), `AccessHashOf` (16 hex, sorts by alias, keeps declared order for secrets and config, sorts owned and grants, nil equals empty), `KeyOf(fn, owned, grants)`, `Assign(fn, key, sameKey, limit)` and the PoolFull text "pool <runtime>/<worker>/<access> is full …" all match.
  - `MemberHeader`, and `PoolSocketFor`, which swaps the member set atomically, refuses a pool key held by a solo listener, and returns 403 before any port is called. `RoutePool` drops non-members, counts them and warns at most once a minute.
  - `PoolManifestDir`, `WithPoolManifestDir`, and `writePoolManifest` (`O_EXCL` temp file, rename, `0600`, refuses a dir that is a symlink, foreign-owned or open to others).
  - `pooled`, `accessIn` (five namespace-scoped Lists; a List error fails the pass), `poolKeyFor`, `MapAccess` (Idle included; watches on the five kinds in `pkg/funcd`), `PoolMembers`, `poolLastLive`/`markPoolLive`, and `FunctionStatus.Pool` (OpenAPI regenerated).
- **Decisions**:
  - D1: one socket per pool, the member named on every call; solo ignores the header (`TestSoloSocketIgnoresMemberHeader`).
  - D2: per-row `AWS_*` and catalog token and `FUNCD_BUNDLE_DIR`. The shared env comes from `self` only (`poolSharedEnv`). A row omits any key the shared env sets. The pooled gate is removed (`resolveBindingEnv` lost its `pooled` arm; `TestScenarioPooledConfigSoloGated` and `pooled-fails-closed` were rewritten so each member's own gate fails).
  - D3: `funcd.member` is stamped in both shims and on spans. `pkg/funcd` routes a `__pool__` worker through `RoutePool`, with the member set snapshotted at process start.
  - D4: hosts listen at once and load members independently under `FUNCD_POOL_LOAD_TIMEOUT_MS`. `/health/members` reports `loading`, `ready`, `failed` and `restarting` (Node only). `convergePooled` maps each entry as the ADR states: `load timed out` counts on `bootBackoff` under `NewInstanceID(ns, name, rev, 0)` per pool `CreatedAt`. `stopNeverReady` is no longer used for pools, and a silent pool is restarted on its own liveness (`TestIssue422_…`, `TestPooledLoadTimeoutRereadsAtBackoffDeadline`).
  - D5: the key counts exactly the listed fields. RolesAssignment grants hash `{roleRef, scope}` with the scope default applied, and duplicates are compacted. EgressPolicy grants hash `spec.rules`. Policy statements are found through the cedar AST walk (`FunctionGrants`; references in scope and conditions are tested). The resource group is counted too. `TestScenarioPoolSplitsByGrant` has a subtest for each grant kind and for the group.
- **safeBeforeFuncd** (`pyvvo/funcd-typescript` true, `pyvvo/funcd-python` true): **true for both, under Decision 7's
  condition.**
  - The funcd branch does not depend on any new Go API in the language modules (`GOWORK=off` build exits 0).
  - Each language repo's own CI is green with the change.
  - The solo shim path is unchanged: `shim/src/shim.ts` is not in the diff, a solo shim passes no member (no header, no `funcd.member` field), and funcd's decoder ignores unknown JSON keys.
  - The new pool host no longer exits 3 on a bad member, so a pre-0158 funcd would read a failed member as Ready. No funcd PR may pin the new tags before this one; a sibling that pins first takes an earlier tag (Decision 7). Merging and releasing the language PRs first is safe.

### Definition of Done
2 / 5 items hold. Counted: Review checklist item 1 and item 2, plus the plan's "Done" items: scenario tests pass,
`just ci-full` green, `go.mod` pins both releases.
- Hold: Decisions 1–5 hold as stated; every unit and scenario test that was run passes under `-race`, with build, vet and lint green on darwin and Linux.
- Misses:
  - The plan-named tests are incomplete (Major 1, model).
  - `just ci-full` and the e2e scenarios were not run here, per the brief (unverified, env).
  - The `go.mod` pin is pending the releases (env).

### Model scorecard
Ledger row below (not recorded in `docs/reviews/`; this is loop 1 of a held ADR). 1 major and 3 minors; 1 of them is
model-attributed; DoD 2/5.

### Recommendation
Add the four missing units (the owned-and-bound KV versus owned-prefix filter in `poolKeyFor`, an `accessIn` List
error failing the pass, `failed` → `ready` on a later pool start, and `failed` in a serving pass → Degraded rather
than ShapeInvalid), then re-review. After that, the integrator pins both language tags in `go.mod` and runs
`just ci-full` once in the PR gate.

```json
{
 "date": "2026-10-05",
 "adr": "0158",
 "phase": "implementation",
 "model": "claude-opus-5-5",
 "verdict": "changes-requested",
 "blockers": 0,
 "majors": 1,
 "minors": 3,
 "model_attributed": 1,
 "dod_passed": 2,
 "dod_total": 5,
 "report": "docs/reviews/adr-0158-implementation-claude-opus-5-5.md",
 "notes": "contracts and Decisions 1-7 match on all three repos; build/vet/lint green darwin+Linux, touched packages pass -race, TS 102/102 and Py shim+examples green via nix develop; 7/7 mutants killed (3 Go, 2 TS, 2 Py); four plan-named units missing: owned table vs prefix, accessIn List error, failed->ready, failed serving->Degraded (model); go.mod pin pending releases, pre-existing TestReclaimDuringRepairBackoff flake, stale language devshell cache (env); safeBeforeFuncd true for both under Decision 7"
}
```
