## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #1409 fix, model: claude-opus-5-5)

This report reviews branch `fix/1409-dev-lake-added-while-running`, commit b7c2f979 `fix(funcdctl): a Lake added while
funcdctl dev runs gets dev's Lake engine` (`git diff origin/main...HEAD`: `cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_app.go`,
`cmd/funcdctl/dev_lake_test.go`). Issue: "A Lake added while funcdctl dev runs never gets dev's Lake engine".

**This review is not independent.** The model that wrote the fix ran it, in the same session, because the decider's
standing rule for fix work is no review agent. Every check below was run, not read, but the judgement calls are the
fixer's own.

### Minor
- **The guard on the extension extraction is not covered on a build without the engine** · attribution: env ·
  evidence: mutant M2 (`bound := true || …` in `devLakeOptions`) leaves both regression tests passing (`ok` in 3.2 s).
  The extraction runs only when `embedengine.Bundled()` holds, and no CI build embeds the engine, so no test can see
  whether dev extracts the extensions while no Lake is bound. The guard keeps the condition the code had before the fix
  (ADR-0130 Decision 2), so the gap predates the fix. A test of it needs a build that embeds the engine.

### ✅ Verified correct (keep it)
- **The regression tests fail without the fix, for the issue's reason.** With `origin/main`'s `cmd/funcdctl/dev.go` and
  `cmd/funcdctl/dev_app.go` as an overlay, `TestIssue1409_FolderDevServesALakeAddedWhileRunning` and
  `TestIssue1409_AppDevServesALakeAddedWhileRunning` both fail after 60 s with `expected: "LakeEngineNotBundled"`,
  `actual: "EngineConvergeFailed"`. The unfixed run's log holds the line `spec.Command must not be empty for the process
  driver` 132 times.
- **They pass with the fix**, un-skipped, under `-race`, in 1.17 s and 1.68 s.
- **Mutant M1** (the options without `funcd.WithLakeProviderRuntime(catEngine)`) fails both tests with
  `actual: "EngineConvergeFailed"`.
- **The issue's own steps pass with the real binary.** A `-tags dev` build of the branch ran `funcdctl dev app` on a copy
  of the todo App with `-f values/later.yaml` (empty at start) and `--apply dr/lake-fixtures.yaml`. Before the save,
  `funcdctl get lake todo-lake -n default` answered `Lake "todo-lake" not found`. After `lake: true` was written to the
  values file, the Lake read `Pending LakeEngineNotBundled`, dev logged `lake engine not embedded in this dev build —
  lake unavailable`, its output held no `spec.Command` or `EngineConvergeFailed` line, and dev exited 0 on SIGINT. The
  build does not embed the engine, so `Ready` was not observable; the engine's `Converge` is the same code that serves a
  Lake present at start.
- **Cause, not symptom.** `devLakeOptions` returned no option when no Lake existed at start, so the platform kept the
  production Lake runtime (`pkg/funcd/binding_providers.go:628`), which cannot run on the process driver. The fix
  removes that early return. It adds no retry, timeout or skip, and it hides no error.
- **Both cases are tested.** The issue names App mode and folder mode, which share `devLakeOptions` through
  `startPlatform` (App mode builds its folder from the first render's `spec.lakes`, `cmd/funcdctl/dev_app.go:942`), and
  each mode has its own test.
- **Scope.** Every hunk serves the issue: the wiring, the comment of `devLakeOptions`, the comment of `platformFolder`,
  which still claimed that the Lakes wire the lake engine, and the new test file. No test was weakened or removed.
- **It is a bug.** The issue's Design check holds: ADR-0253's scenario `app-dev-reload` serves a saved template change,
  and `docs/behavior/funcdctl-dev.md` rows 38 and 49 and `docs/behavior/services.md` row 31 describe the expected
  behavior. No ADR, issue or card accepts or defers a Lake added after start.
- **ADR conformance.** ADR-0125 Decision 5 (dev serves a Lake with its embedded engine) holds in more cases now.
  ADR-0130 Decision 2 holds: the extensions are still extracted once at boot, only when a Lake is bound and the engine is
  embedded. No ADR file was edited.
- **Reuse.** The tests reuse `nodeFunction`, `devProject`, `writeProject`, `tryStartDev`, `appCopy`, `startApp`,
  `todoCfg`, `requireApp` and the todo App's `dr/lake-fixtures.yaml`. The one new helper, `requireLakeOnDevEngine`, has
  no counterpart in `cmd/funcdctl`.
- **Siblings.** Dev injects two provider runtimes, the Lake one and the SQL one (`cmd/funcdctl/dev.go:1116`, `:1153`).
  The SQL one depends only on whether the build embeds its engine, not on what exists at start, and `devVolumeOptions`
  reads no folder.
- **Conventions.** The test YAML is block style, the imports are at the top, the comments state a reason, and the
  commit is one `fix(funcdctl):` commit with `Fixes #1409` and the attribution trailer.
- **Checks.** `gofmt` reports nothing; `go build ./...` and `go build -tags dev ./cmd/funcdctl` succeed; `go vet` passes
  with and without `-tags dev`; `golangci-lint` reports 0 issues on the five dev packages with `--build-tags dev` and on
  `cmd/funcdctl` without it; `go test -race -tags dev ./cmd/funcdctl/` passes in 187.5 s. `scripts/agent/gate.sh` passed: the audit (3 s), `ci-full` with e2e (868 s), the Linux build, vet and lint
  (33 s), the stress run of the two new tests (80 s) and the unchanged tree. It skipped the Linux tests because the
  funcd-linux-tests VM was not running; the change touches no Linux-specific package.

### Definition of Done
12 / 12 items that apply hold. Item 13 does not apply, because no design spec models dev's Lake wiring. Item 4 holds on
the fix's key line (the revert and M1 both fail); M2 is recorded as an env-limited Minor.

### Model scorecard
Recorded: claude-opus-5-5 on #1409 (fix) → pass, 0/0/1, 0 model-attributed, DoD 12/12.

### Recommendation
Ready for the PR, with `Fixes #1409` in its body.
