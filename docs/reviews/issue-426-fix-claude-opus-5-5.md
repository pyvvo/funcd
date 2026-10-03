# Fix review — issue #426 (funcdctl dev closes the durable stores under a running platform on a failed boot)

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #426 fix, model: claude-opus-5-5)

Change: branch `fix/i426`, commit b2c520e `fix(funcdctl): stop the dev platform on a failed boot before its
drivers close` — `cmd/funcdctl/dev.go` (+13/-1), `cmd/funcdctl/dev_phase2_test.go` (+67/-22).

The issue names three defects in `bootDev`: the deferred `closeDurable` fires on any boot error, even after
`funcd.New` owns the durable drivers; nothing stops the `p.Run` it started; and a failed `New` closes the
drivers twice. The fix sets `closeDurable = nil` right after `funcd.New` returns (New owns the drivers from
then on, whether it failed or not), runs `p.Run` on a child context, and adds a defer that, on error, cancels
that context and waits on `inst.runErr`. Defers run last-in-first-out, so the platform stops (Run →
`Shutdown`, which closes the drivers once) before the earlier cleanup defer runs.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### 🟡 Minors

1. **Mutant M2 survives: the failed-`New` double close is not pinned by a test** (model). Deleting
   `closeDurable = nil` leaves the touched package green under `-race`. On the tested path (a failed apply)
   the closer then runs after `Shutdown` and is harmless, because the closer discards errors; but the
   failed-`New` path that the issue names, where `New`'s own deferred `Shutdown` (#94) already closed the
   drivers, has no test. The test comment claims "closes each durable driver once", which nothing asserts.
   A test that fails `New` under `--persist` (for example a clashing fixed port) and counts closes, or
   retries the boot on the same persist dir, would pin it.
2. **Mutant M3 survives: the wait on `inst.runErr` is not pinned** (model). Keeping `cancelRun()` but
   dropping `<-inst.runErr` also leaves the package green: the listener closes fast enough that the dial
   and the retry boot pass. The commit message promises the platform stops "before the remaining boot
   cleanups run"; the test checks the port and the retry only after `startDev` returns, so the ordering is
   observed only by timing.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit b2c520e` with
  the new test file kept, then `go test -tags dev -run TestIssue426_ ./cmd/funcdctl/` →
  `--- FAIL: TestIssue426_DevFailedBootStopsPlatform` at "a failed boot stops the platform it started" (the
  dial to the control port succeeded: the platform outlived the failed boot). The worktree was reset to
  b2c520e and is clean.
- **Passes with the fix under `-race`**: `go test -tags dev -race -run
  'TestIssue426_|TestIssue398_|TestScenarioDevPersist'` → PASS for all four, and the whole touched package
  `go test -tags dev -race -count=1 ./cmd/funcdctl/` → ok.
- **Mutant M1 killed**: removing the cancel-and-wait body of the new defer → FAIL at "a failed boot stops
  the platform it started".
- **Cause, not symptom.** The ownership hand-off now matches the documented contract (the comment above
  `buildPersistDrivers` and the one in `bootDev`: the scoped closer fires only before `funcd.New` takes
  ownership), and the failed-boot path stops the platform it started instead of leaving `Run` alive until
  the caller's context ends. No timeout, retry or swallowed error. The child context is cancelled only on
  the error path; on success it ends with the caller's context, as `stop()` expects (`stop()` still reads
  `runErr`, which the error path drains only when no instance is returned). `go vet` reports no lost cancel.
- **The test drives the real path.** A `--persist` boot on a fixed `--cport` with the Function PUT rejected
  (`fault.Invalidf`) fails after `Run` started; the test then checks the control port is free and that a
  second boot reopens the same port and persist dir. It uses `os.MkdirTemp("", "funcd")`, not `t.TempDir()`.
- **Reuse.** The apply-fault transport of TestIssue398 (`conflictOnFirstPut` plus its inline install) is
  generalized into `failFirstPut(t, name, problem)` and both tests use it, so no second transport was
  written. `fault.WriteProblem`, `fault.Invalidf` and `fault.Conflictf` are reused for the response.
- **Scope.** Only `bootDev`'s ownership and failure path plus the test helper refactor; TestIssue398 keeps
  its assertions. No ADR file is touched.
- **ADRs.** Consistent with ADR-0125 Decision 7 (durable-local drivers: the platform owns and closes them on
  Shutdown) and ADR-0028 (Run owns the crash-only lifecycle). ADR-0002: `api/fault` errors, ctx-first, no
  new exported surface.
- **Conventions.** `net` import at the top level, no YAML added, two short why-comments in `dev.go`.
- **Checks (touched package only).** `go build`, `go vet` (with and without `-tags dev`), `golangci-lint`
  (with and without `--build-tags dev`) → 0 issues; `gofmt -l` clean.
- **Shape.** `fix(funcdctl):` subject, Cause/Fix/Test body, `Fixes #426`, the attribution trailer, one
  commit.

### Notes (not scored)

- `Platform.Run` returns early without `Shutdown` when the egress `netManager.Apply` fails. `funcdctl dev`
  sets no net manager, so the path is unreachable here; outside this issue's scope.
- `TestIssue427` (group sibling) also edits `dev_phase2_test.go`; expect a textual merge at integration.

## Recommendation

Pass. The two surviving mutants are test gaps on secondary lines of the fix; a follow-up test could pin the
failed-`New` single close and the stop-before-cleanup ordering.
