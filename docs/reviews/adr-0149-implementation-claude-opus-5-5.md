# ADR-0149 implementation review — claude-opus-5-5 (loop 1)

Work: branch `feat/adr-0149-runtime-availability`, one commit (5acfb309, `Fixes #457`), 15 files, +591/−45 against
`origin/main`. ADR: `docs/adr/0149-runtime-availability.md` (Accepted on main, cad79436).

## Verdict: pass — 0 blockers, 0 majors, 1 minor (ADR-0149 implementation, model: claude-opus-5-5)

All five Decisions and every Contract match the code. All nine Scenario tests and both classification tests exist
under the names the ADR gives and pass under `-race`. Builds, vet and lint pass on darwin and on Linux. Three
mutants on key lines each fail at least one ADR-0149 test. A fourth mutant shows that an ADR-0143 test, not an
ADR-0149 test, pins the stop of C's workers.

## Verification run (worktree, through `scripts/agent/d`)

| Check | Command (scope) | Result |
|---|---|---|
| Build | `go build ./...`, then `GOOS=linux go build ./...` | exit 0 / exit 0 |
| Vet | `go vet` on `internal/function`, `internal/runtime`, `internal/runtime/containerd`, `internal/runtime/embedimg`, `pkg/funcd` (darwin and `GOOS=linux`) | exit 0 / exit 0 |
| Lint | `golangci-lint run ./internal/function/... ./internal/runtime/... ./pkg/funcd/...` (darwin; Linux with a host-built binary under `GOOS=linux`) | `0 issues.` exit 0 / exit 0 |
| Scenario tests | `go test -race -count=1 -run 'TestADR0149\|TestIssue371\|TestNormalizedRef\|TestResolveImage' ./internal/function/ ./internal/runtime/containerd/` | 9 ADR-0149 tests + `TestIssue371_…` (solo, pooled) + `TestADR0149_ImageAbsentClassification` + `TestNormalizedRef_…`: all PASS, exit 0 |
| Touched packages | `go test -race -count=1 ./internal/function/ ./internal/runtime/ ./internal/runtime/containerd/ ./internal/runtime/embedimg/` | ok ×3 (`internal/runtime` has no tests), exit 0 |
| gofmt | `gofmt -l` on the 10 changed Go files | clean |
| Tree | `git status --short` | clean |

The Linux-only tests in `snapshotter_linux_test.go` (`TestADR0149_ResolveImageClassifiesPullErrors` and the
sentinel assertion added to `TestResolveImage_DefaultPrefixRuntimeIsNotPulled`) compile under Linux vet and lint
but were not run on this darwin host (env; CI runs them). This review did not run the Lima lane, the e2e suite or
`go test ./...`, as the task required.

### Mutants (`go test -overlay`)

| # | Mutation | Result |
|---|---|---|
| m1 | `shimFor` falls back to `r.shimCommand` for every runtime (the old Node fallback) | killed: `TestADR0149_UnknownRuntimeProcessSolo`, `…ProcessPool`, `TestIssue371_…` FAIL |
| m2 | `imageAbsent` drops `errors.Is(err, docker.ErrInvalidAuthorization)` | killed: `TestADR0149_ImageAbsentClassification` FAIL on the "docker hub 401" case. The test's own `httptest` registry made containerd's real resolver return `insufficient_scope` |
| m3 | `desiredReplicas` drops the `awaitsImage` case | killed: `TestADR0149_MissingImageScaleToZero`, `TestADR0149_PublishedImageRecovers` FAIL |
| m4 | `gateFailed` no longer stops the current revision while another serves | killed only by `TestGateFailureStopsTheBootingRevision` (ADR-0143). No ADR-0149 test fails; see Minor 1 |

## 🔴 Blocker

None.

## 🟡 Major

None.

## Minor

### Minor 1 — the keeps-serving test does not exercise "C's workers stop" · attribution: model

`TestADR0149_UnavailableRuntimeKeepsServingRevision` (`internal/function/runtime_availability_test.go:213-248`)
asserts `NotContains(states, "sw-2")`. The injected error fails C's first `Create`, so C never had a worker, and the
assertion holds whether or not `gateFailed` stops C. Mutant m4 survives every ADR-0149 test.
`TestGateFailureStopsTheBootingRevision` still pins the shared `gateFailed` path, so behavior is covered and the
checklist item holds. Strengthening the test is optional: let one C replica start before the image error is
injected.

### Not scored

- **Documents and status (process).** The ADR is still `Accepted` on the branch. The F12 row split, the
  "Superseded in part" back-links on ADR-0049/0050/0054 and the F28 cell (Implementation plan step 8) are not
  in this branch. The task scopes all document and status edits to the wave's docs PR, so these are not scored.
  The ADR's substance is unchanged: the diff touches no file under `docs/`.
- **The lane case was not run (env).** The Venom testcase and fixture were reviewed by reading them. The
  testcase sits after `daemon-restart-recovers`, the fixture uses block-style YAML, and the staging entry matches
  the existing `env-echo-*` fixtures. The containerd driver checks `spec.Image` and then calls `resolveImage`
  before it reads `spec.Command`. The `Command` is now nil for `ruby3`, but this cannot mask the sentinel, so
  the lane should see the "is not embedded" message.
- **Import closure (adr).** `internal/function` now imports `internal/services/catalog` for `DuckDBRuntime`. That
  brings `internal/provider` and `internal/edge/router` into its dependency closure (checked with `go list -deps`
  against main's `function.go`). The ADR's Contracts name `catalog.DuckDBRuntime` as the source, no depguard rule
  forbids the import, and the closure adds no cgo package. This is noted, not scored.

## ✅ Verified correct (keep it)

- **Decision 1 / Contracts.** `isNodeFamily` mirrors `isPythonFamily`. `shimFor` returns the family match, then
  the default only for node-family runtimes, else nil. In `poolHostFor`, `!isNodeFamily` replaces the
  python-only return, and the `shimByFamily` exclusion loop remains (`pool.go:43-62`). The `WithRuntimeShim` and
  `WithPoolShim` docs say each sets the node-family default (plan step 5).
- **Decision 2.** `runtimeUnavailable` never gates the legacy mode (`materializer == nil`). In process mode it
  gates a runtime when `shimFor == nil` and `poolKeyFor` is false. In containerd mode it gates only
  `catalogsvc.DuckDBRuntime`. Neither gate requeues, and the tests assert `RequeueAfter == 0` for both.
- **Decision 3.** Exactly two branches of `resolveImage` wrap `runtime.ErrImageUnavailable` as `fault.NotFound`:
  the not-pullable ref, with the ADR's new `imageOverride for its runtime` hint, and a pull for which
  `imageAbsent` is true. Every other pull error keeps `mapErr`. `imageAbsent` is in untagged `image_ref.go`. Its
  test runs containerd's real `docker.NewResolver` against `httptest` registries that return 404, Docker Hub's
  401 `insufficient_scope`, GHCR's token 403 and a 503. This shows the classification on real resolver errors,
  not only on hand-built ones. The `Tar` doc comment states the pull rule (plan step 3).
- **Decision 4.** `convergeRevision` adds the prefix with `tmpl.Spec.Runtime`, the runtime of the revision that
  failed, and keeps the kind. `Reconcile` maps the sentinel only on the `convergeSolo` error, so a pooled error
  is returned as before. `withoutOp` removes the `function.converge` op. The tests assert the full Decision 4
  message byte for byte on Ready and on RevisionReady. The `gateFailure` doc comment is widened as the plan says.
- **Decision 5.** `awaitsImage` (containerd mode, `Failed`, Ready reason `RuntimeUnavailable`) keeps a
  scale-to-zero Function woken. The tests show one `Create` attempt per pass over three passes, a requeue after
  `supervisionPeriod`, and recovery with no re-apply: the generation is unchanged, the phase is Ready and the
  worker uses `ruby3Image`.
- **Retryable outage.** `TestADR0149_RegistryOutageStaysRetryable` checks four things: `fault.Unavailable` is
  returned, the sentinel is absent, no Ready condition is written, and the status does not change between two
  passes, each of which calls `Create`.
- **Test fakes.** The `fakeRuntime` gains `imageErr` and `attempts` under its mutex (clean under `-race`), and
  `newContainerHarness` gains variadic `Deps` options. Both are small, additive changes, and the existing
  helpers (`withPeriod`, `withSwitch`, `withNodePool`, `setPhase`, `deployReady`) are reused.
- **Commit.** The commit lists every test and carries `Fixes #457` and the attribution line. No string with the
  old wording ("no python shim is registered", "imageOverride or imagePrefix") remains outside `docs/`.

## Definition of Done (ADR Review checklist + plan step 9)

| Item | Holds |
|---|---|
| No non-node runtime gets the default shim or pool host; a runtime-shim family still gets no default host | yes (m1, `pool.go`) |
| Process solo, process pooled and containerd solo show the Decision 4 reason and message; a pooled converge error is not mapped | yes |
| Only Decision 3's two cases wrap the sentinel; every other pull failure is returned from `Reconcile` | yes (m2; Linux test compiled only) |
| Decision 5 re-check, scale-to-zero and recovery; a serving revision keeps serving and C's workers stop | yes (m3; the stop is pinned by an ADR-0143 test, Minor 1) |
| `nodejs22`/`python314` unaffected; `Tar` doc updated (step-8 docs deferred to the wave docs PR) | yes |
| Each scenario has one named, passing test | yes (9 + 2) |
| `just ci` green | yes, scoped: build, vet and lint on both platforms, and the touched packages under `-race` |
| The lane case passes | not verified (env: Lima excluded) |
| The change carries `Fixes #457` | yes (commit message) |

8 of 9 hold. The unverified item is the env-attributed lane run.

## Recommendation

**Pass.** The wave's docs PR moves the ADR `Accepted → Reviewing → Implemented`, moves the F12 row as its step 8
says, and adds the three back-links. Minor 1 is optional and does not need another loop. The repo-wide gate,
which runs the lane, still runs once per PR.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0149",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 8,
  "dod_total": 9,
  "report": "docs/reviews/adr-0149-implementation-claude-opus-5-5.md",
  "notes": "all 5 decisions and contracts match; 9 scenario tests + 2 classification tests pass under -race (the classification test uses the real containerd resolver against httptest registries); darwin+Linux build/vet/lint green; 3/3 key mutants killed; keeps-serving test cannot see C's stop, which ADR-0143's test pins (model); lane not run, Linux-only resolveImage test compiled only (env); docs/status deferred to the wave docs PR, catalog import per Contracts (adr, not scored)"
}
```
