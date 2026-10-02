## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #94 fix, model: claude-opus-5-5)

Change: branch `fix/i94`, commit `88015e4` — `fix(funcd): release everything a failed New acquired so a retry works`.
Touched: `pkg/funcd/funcd.go`, `pkg/funcd/funcd_test.go`.

The fix gives `New` a named `err` return and a deferred `p.Shutdown` that runs on every failure path
(option, `validate`, logger, provider catalog, `buildControlPlane`). `Shutdown` now goes through a
nil-safe `closeDriver` for the five required drivers, because they are nil when `New` fails before an
option sets them. This removes the cause the issue names: `New` had no cleanup path, and only `Shutdown`
released what it acquired.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The doc comment of `New` does not say that a failed `New` closes the injected drivers** · attribution:
  `model` · evidence: `pkg/funcd/funcd.go:316-321` still says only "a nil *Platform … never a partial
  platform". After the fix, a failed `New` also closes every driver that a `With*` option injected
  (`WithStore`, `WithBus`, `WithBlob`, `WithRuntime`, `WithGateway`, and a `WithKVStore` that is an
  `io.Closer`). That is consistent with ADR-0014 §3, where the platform owns the closers it registered.
  But it is a change that embedders can observe: to retry after a failure with its own drivers, an
  embedder must build them again. `cmd/funcdctl/dev.go` already closes its durable drivers on a failed
  `New` (`closeDurable`), so they are now closed twice. This is harmless, because Badger's `DB.Close` is
  guarded by `closeOnce`. Fix: add one sentence to the doc comment of `New` that says it takes ownership
  of the injected drivers and releases them when it fails.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** I ran
  `git revert --no-commit 88015e4`, kept the new test from HEAD, and ran
  `go test -run TestIssue94 ./pkg/funcd/`. It printed `FAIL`:
  `listen tcp 127.0.0.1:<port>: bind: address already in use — "the control-plane port is released"`.
  This is the leaked control-plane listener from the issue's "Actual behavior". After that, I reset the
  worktree to `88015e4` and it was clean.
- **The test passes with the fix under `-race`.** `go test -race -count=3 -run TestIssue94 ./pkg/funcd/`
  printed `ok`. The whole package with `-race` printed `ok`. The test is not skipped.
- **The behavior the user sees is fixed, beyond the unit test.** I wrote a scratch overlay probe that is
  not committed. It ran three failed `New` calls in a row, with the data-plane port busy and a persistent
  workflow dir and DLQ dir. The goroutine count was 2 before the calls and 2 after them. The issue
  reported +64 goroutines per failed call. The test also shows that the port is released, that the bus is
  closed, and that a retry with the same workflow dir and DLQ dir succeeds, so the Badger directory locks
  are released.
- **The fix removes the cause, not the symptom.** Every resource that `buildControlPlane` opens is stored
  on `p` immediately after it opens (`p.deadLetters`, `p.workflowRuns`, `p.listener`,
  `p.dataPlaneListener`, `p.s3gw`, `p.traceSink`, `p.logSink`, `p.invokeMgr`, `p.catalogProxy`). So the
  existing `Shutdown` releases them. The fix adds no timeout, no retry and no swallowed error. The fix
  also covers failures in an option or in `validate`, which release the preset's drivers.
- **Mutants: all three were killed** (run as overlays, with `-run 'TestIssue94|TestScenario|TestWith'`):
  - M1, the deferred cleanup is registered after the option loop → `TestIssue94` fails with "a failed
    option releases the preset's bus".
  - M2, `closeDriver` never calls `Close` → `TestIssue94` fails with "the preset's bus is closed", and
    `TestScenarioRunShutdownLifecycle` fails too.
  - M3, the nil guard in `closeDriver` is removed → `TestScenarioMissingRequiredDep` panics with a nil
    dereference. This shows that the existing missing-dependency scenario needs the guard.
- **Scope**: every hunk serves the issue. No test was weakened or deleted.
- **Reuse**: `Shutdown` is reused as the single release path, so the change does not add a second
  cleanup list. `closeDriver` is the only nil-safe `io.Closer` helper in the repository; I searched for a
  similar helper and found none. The test uses only the standard library, `testify` and the package's own
  options.
- **Conventions**: the new failure path keeps the `api/fault` errors unchanged. The change adds one short
  comment for the "why", imports are at the top level, and there is no YAML. ADR-0002 holds, and the
  facade signature `New(opts ...Option) (*Platform, error)` is unchanged in shape.
- **ADRs**: the fix honors ADR-0014 ("never a partial platform"; `Shutdown` is idempotent through
  `sync.Once` and best-effort). No ADR file was edited.
- **Checks on the touched package**: `go vet ./pkg/funcd/` passed, `golangci-lint run ./pkg/funcd/...`
  reported `0 issues`, `gofmt -l` reported nothing, and the `-race` tests printed `ok`. The group gate runs
  the e2e suite, the Linux lint and the lanes.
- **Shape**: the subject is `fix(funcd): …`, the body has `Fixes #94`, the commit has the
  `Co-Authored-By` trailer, and the commit covers one issue.

### Recommendation

Pass. Optionally, fold the one-sentence ownership note into the doc comment of `New` before the PR. It
does not block the fix.
