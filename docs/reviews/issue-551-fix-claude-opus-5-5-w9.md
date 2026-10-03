## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #551 fix, model: claude-opus-5-5)

Change: branch `fix/w9-i551`, commit e8998e6 `refactor(funcdctl): split dev bootDev and reloadChanged into named phases`
(`cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_reload.go`). Issue #551 is a task (`kind/task`); its "Done when" is the
target, and the decided shape is a refactor with no behavior change, the existing dev tests passing unchanged.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The duplicate fixed-port check has no test** · attribution: model · evidence: mutant m3 (`checkFixedPorts`
  never reports a duplicate) passes `go test -tags dev -run Port ./cmd/funcdctl/` (`ok`); no test in
  `cmd/funcdctl` asserts "must be different ports". The issue names `checkFixedPorts` as pure and testable on its
  own, and the extraction made a table test trivial, but none was added. The gap predates the change (the inline
  loop on `origin/main` was untested too). The same holds for the durable-driver close on a `devS3Options`
  failure (moved from a deferred closer in `bootDev` into `devPlatformOptions`): no test reaches that path —
  `TestIssue432_DevFailsWhenS3PortIsTaken` fails later, at `WaitS3Gateway`. Fix: a small table test of
  `checkFixedPorts` (0 skipped, each pair, stable order of the message).

### ✅ Verified correct (keep it)
- **Done when, measured** with `scripts/agent/audit.golangci.yml` thresholds lowered to print every value
  (`-tags dev`): `bootDev` 47 statements, gocyclo 14, gocognit 17 (target < 80 statements, gocyclo < 25);
  `reloadChanged` 40 statements, gocyclo 17, gocognit 30 (target funlen < 50); `devShimOptions` gocyclo 15
  (target < 20). New helpers: `devPlatformOptions` 32/8/8, `prepareHandlers` 20/5/7, `applyBoot` 17/8/10,
  `devCatalogOptions` 16/6/9, `changedHandlers` 14/9/9, `checkFixedPorts` 9/4/5, `devInterpreter` 7/4/3 — none
  crosses a limit. The audit config at its real thresholds reports nothing for any of them.
- **No behavior change**, read hunk by hunk against `origin/main`:
  - option order is preserved (base options, catalog, log observer, ports, persist drivers, S3, shims last);
  - cleanup order is preserved: the durable closer still runs before `bootDev`'s accumulated cleanups (it is now
    called inline before `devPlatformOptions` returns, where it was the later-registered defer before), and
    `devS3Options` was the only failure between `buildPersistDrivers` and `funcd.New`;
  - `fnObjs` is link-ordered before `applyBoot`, so `wf.applied` and the apply groups see the same order;
  - dropping the `len(pfs) > 0` guard in `devPlatformOptions` is safe: `bootDev` returns "no function to run" on
    an empty set first, and `pfs[0]` was already used unguarded (`resolvePersistPlan`, `buildPersistDrivers`);
  - `changedHandlers` is the detection loop moved verbatim; `devInterpreter` keeps env > manifest > PATH.
- **Mutants** (overlay, `-tags dev`, `-run`): m1 drop `h.pf.m = m` in `changedHandlers` →
  `TestIssue320_DevHotReloadsImportsAndManifest` FAIL; m2 skip the env override in `devInterpreter` →
  `TestIssue430_MissingPythonFailsAtStartup` and `TestIssue503_MissingNodeFailsAtStartup` FAIL; m3 survives (Minor).
- **Tests unchanged and green**: no test file in the diff; `go test -race -count=1 ./cmd/funcdctl/` ok (2.0 s) and
  `go test -race -count=1 -tags dev ./cmd/funcdctl/` ok (15.0 s).
- **vet and lint**: `go vet` with and without `-tags dev` clean; `golangci-lint run` with and without
  `--build-tags dev` → 0 issues.
- **Scope**: two files, every hunk is the extraction the issue lists; builds on the current layout (the watcher
  already in `dev_reload.go` from #552).
- **Reuse**: `devInterpreter` reuses `envOr` and `resolveInterpreter`; `applyBoot` reuses `stageResources`,
  `applyDesired`, `pruneStale`. The similar interpreter lookup in `cmd/funcd/main.go` lives in another main
  package and is not a shared-helper candidate for this change.
- **Conventions**: `api/fault` errors, ctx-first, slog only, no `any` in signatures; doc comments state the why
  (ADR-0123/0124/0125 references kept) without narration.
- **ADRs**: no ADR file touched; ADR-0125's boot sequence and hot reload are unchanged.
- **Shape**: `refactor(funcdctl):` fits a task, `Fixes #551`, attribution trailer, one issue in one commit.

### Definition of Done
8 / 8 applicable items hold (items 4–11; items 1–3 do not apply: the decision is a refactor whose existing tests
pass unchanged, with no `TestIssue551` test). Item 4 holds for the key lines (m1, m2 killed); the m3 survivor is
the Minor above.

### Model scorecard
claude-opus-5-5 on issue #551 (fix) → pass, 0/0/1, 1 model-attributed, DoD 8/8. Ledger row to be recorded by the
batch ledger PR.

### Recommendation
Merge as is. Optionally add a table test for `checkFixedPorts` in a follow-up commit.
