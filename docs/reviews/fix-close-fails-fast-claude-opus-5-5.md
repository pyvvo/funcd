## Verdict: pass — 0 blockers, 0 majors, 2 minors  (Close fail-fast fix, no issue, model: claude-opus-5-5)

Change: branch `fix/close-fails-fast-on-dead-containerd`, commit 9273e545 `fix(runtime): fail Close fast when the
private containerd has exited`, on `origin/main` c37b2a0a (8 files, +183/-6; 129 of the added lines are tests).

No issue exists. The defect was found on the way and proven on a Lima VM, so `/fix` fixes it in its own commit and
records it in a PR comment. The defect: when the private containerd exits before or during the runtime's `Close`
(`systemctl kill`, which signals every process of the unit, or a unit with `KillMode=control-group`), containerd
removes its socket. Every call `Close` then made redialed the missing socket, and the containerd client retries
that for its connect timeout (10 s). `Shutdown` waited 10 s, then failed.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — two mutants survive the committed tests** · attribution: `model`.
  - M4 removed `Exited: mgr.Exited(),` from `containerdOptions` (`cmd/funcd/main.go:1384`).
    `go test -count=1 ./cmd/funcd/` still returned `ok` (27.5 s). This line turns the fix on in the daemon. Its only
    test, `TestContainerdModeSweepsLeftoversAtBoot`, uses an external socket, where `Exited()` is nil anyway. A unit
    test cannot reach the private path without root, so a lane step is the realistic guard: for example, run
    `systemctl kill` on the funcd unit and require that it stops well under 10 s.
  - M6 removed `case <-done:` from the watcher in `(*driver).Close`
    (`internal/runtime/containerd/containerd_linux.go:974-980`). Every committed test in the package still passed. A
    scratch goroutine count caught it: 52 goroutines before 50 Closes, 102 after. The leak is one goroutine per
    `Close`, and the daemon calls `Close` once, so the effect is small.

  Both are optional follow-ups.
- **Minor 2 — the reason for the place of `m.reap()` is not in the tree** · attribution: `model`.
  `Ensure` calls `m.reap()` right after `startContainerd` (`internal/runtime/ctrmanager/manager_linux.go:70`), not
  inside `startContainerd`. The reason: TestIssue630 and TestIssue707 run `startContainerd` inside a `synctest`
  bubble. A reaper started there blocks in `wait4`, which is not durably blocked, so `synctest.Wait` waits until the
  fake containerd exits. Mutant M5 moved the call into `startContainerd`: TestIssue630 failed after 30.02 s with
  "containerd was gone before Close stopped it: cancelling the Ensure context killed it", and TestIssue707 took
  30.01 s instead of 0.00 s. The test catches the move, but its message names the wrong cause. A one-line why
  comment at the call would spare the next reader that trip.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the defect's reason.** The tests are Linux-only. Each test
  binary was cross-compiled (`GOOS=linux GOARCH=arm64 CGO_ENABLED=0`) and run as a non-root user in the
  flake-pinned Lima test VM. A literal `origin/main` overlay of all six changed non-test files does not compile
  (`close_linux_test.go:195:4: unknown field Exited in struct literal of type Config`; `manager_linux_test.go:54:4:
  m.reap undefined`). The behavior revert therefore overlays `origin/main` plus only the API the tests compile
  against: the driver's `Exited` field, and manager `reap`/`Exited` stubs that return nil.
  - `before_Close`: FAIL (10.00 s), `Close returned 10.001s after containerd exited: runtime.containerd.Stop: load
    container "exited-1.r0": connection error: … dial unix …/containerd.sock: timeout`.
  - `during_Close`: FAIL (10.00 s), `Close returned 10.001s after containerd exited: … error reading from server:
    EOF`. The call in flight fails at once; the next call (the sweep's namespace list) redials for 10 s.
  - `TestPrivateContainerdReportsItsExit`: FAIL (5.01 s), `Exited is still open after containerd died`.

  This matches the stated cause. In containerd v2.3.1, `client/client.go:112` sets the default timeout to 10 s,
  `:141` uses it as `MinConnectTimeout`, and `pkg/dialer/dialer.go:52-53` retries a missing socket every 10 ms
  until that timeout.
- **It passes with the fix**, un-skipped, on Linux: `TestClose_FailsFastOnceContainerdExited/before_Close` and
  `/during_Close` (0.00 s each) and `TestPrivateContainerdReportsItsExit` (0.00 s). The whole
  `internal/runtime/containerd` package (50 tests, none skipped or failed) and the whole
  `internal/runtime/ctrmanager` package (9 tests) pass on Linux.
- **The requested mutants fail a test.** M1 (`Close` checks `Exited` only at its start, no watcher): `during_Close`
  FAIL after 10 s, while `before_Close` still passes, so the during case is the one that pins the watcher. M2
  (`Close` returns nil once containerd has exited): both subtests FAIL at once with `An error is expected but got
  nil` and `Close must fail, so Shutdown keeps the egress fence`. M3 (the reaper never closes `exited`):
  `TestPrivateContainerdReportsItsExit` FAIL, `Exited is still open after containerd died`.
- **Cause, not symptom.** The supervisor knows when its child exits, so the driver stops calling a containerd that
  cannot answer. The alternatives would mask the defect: a shorter client connect timeout would also cut off a slow
  but live containerd, and a socket-file probe would race. `Close` still fails (`fault.Unavailable`,
  `containerd_linux.go:992-993`), so `Shutdown` keeps the egress fence (`pkg/funcd/funcd.go:1672-1675`, ADR-0115).
  No error is swallowed, no timeout changed, no test skipped. `Exited` closes when containerd itself exits:
  `cmd.Wait` also waits for the output pipe, but the long-lived runc-v2 shims start without that pipe (containerd
  v2.3.1, `cmd/containerd-shim-runc-v2/manager/manager_linux.go`, `newCommand`). One bounded wait remains: a worker
  whose task was loaded before the exit still waits up to `outputWait` (1 s, ADR-0168) for its output.
- **The normal shutdown order is unchanged while containerd runs.** `Shutdown` closes the runtime first
  (`pkg/funcd/funcd.go:1672`). The daemon stops containerd later, in the deferred `closeExec`
  (`cmd/funcd/main.go:185-189`, `mgr.Close` from `:1397`). The installed unit has `KillMode=mixed`
  (`cmd/funcd/install.go:58`), so `systemctl stop` signals funcd alone. With `Exited` open, `Close` takes the
  `default` branch and closes the client at the end, as before. The four existing `TestClose_*` tests and
  `TestStop_KeepsWorkerOnLoadError` pass on Linux. A scratch test (not committed) ran the assertions of
  `TestClose_StopsEveryWorker` with an open, non-nil `Exited`: PASS.
- **No goroutine leak.** The watcher ends when `Close` closes `done`, and every path reaches that line. A scratch
  test (not committed) ran 50 Closes with `Exited` nil and 50 with `Exited` open: 3 goroutines before and 3 after in
  both cases.
- **Concurrency.** `sync.OnceValue` turns the watcher's close and the final close into one call. The containerd
  client guards its connection with `connMu`, and a closed gRPC connection fails pending and later calls at once;
  `during_Close` depends on that. `m.exited` is written by `reap` and read by `Exited` on one goroutine (the
  composition root's `Ensure`, then `Exited`, later `Close`). `cmd.Wait` now has one caller, `reap`.
  `privateManager.Close` waits on the same channel, so its interrupt-then-kill after 10 s is unchanged, and a
  containerd that dies early is reaped at once instead of staying a zombie until `Close`.
- **Scope.** Every hunk serves the defect: `containerd.Config.Exited` (Linux and the non-Linux stub), `Exited` on
  the manager (private, external and the non-Linux stub), the composition-root wiring, the `Close` watcher and
  error, the reaper (which replaces the Wait goroutine `Close` already had), and the tests. No test was weakened or
  deleted. Siblings: the bench (`internal/testkit/bench/containerd_linux.go:48`) passes no `Exited`; its containerd
  runs in its own process group and stops only after the lane returns, so it is not exposed the same way. An
  external containerd keeps `Exited` nil by design ("nil ⇒ never known"). No living doc describes this behavior.
- **Reuse.** `sync.OnceValue` is the standard library, and the reaper moves the existing Wait goroutine. The
  `stubContainerd` gRPC server is new, but no other exists in the repo (`grpc.NewServer` appears only in this
  file), and the package's in-memory `fakeClient` has no socket, so it cannot show a removed socket being
  redialed. The tests reuse `removedCNI`, `requireNoneReleased`, `workerNames` and `startFakeContainerd`.
- **Conventions.** An `api/fault` error; the new field and interface method follow the doc style of the existing
  `Private` field; top-level imports; why comments only. The one `//nolint:forbidigo` names its reason
  (`grpc.StreamHandler` takes `any`). The stub's socket sits under `os.MkdirTemp("", "funcd")`, inside the Unix
  path limit (#41).
- **ADRs.** No ADR file changed. ADR-0054 (Implemented) lists `Manager` with `Ensure` and `Close`. Adding `Exited`
  extends the supervision that ADR-0054 Decision 3 gives the Manager and changes no decision, as the earlier fix
  that added `containerd.Config.Private` did (#625). The ADR-0115 fence rule holds.
- **Checks.** `go build ./...` for darwin and `GOOS=linux`; `go vet` and `golangci-lint` (0 issues) on `cmd/funcd`,
  `internal/runtime/containerd` and `internal/runtime/ctrmanager` for darwin and `GOOS=linux` (the lint as
  `scripts/agent/gate.sh` runs it); `gofmt -l` clean. `go test -race -count=1` on the three packages on darwin:
  `ok` (cmd/funcd 31.7 s, containerd 2.5 s, ctrmanager 2.2 s). The Linux-only tests do not build on darwin.
- **Shape.** One commit, `fix(runtime): …`. The body states the cause, the fix and the regression tests, and it
  carries the attribution trailer. With no issue there is no `Fixes #N` and no `TestIssue<N>_…` name; the tests
  follow the file's `TestClose_*` pattern. The PR comment that records the defect is still due when the PR opens.

Not run here: `-race` for the Linux-only tests (the test VM has no Go or C toolchain, and a race build needs cgo;
attribution `env`), the defect's own `systemctl kill` reproduction on a real daemon (it needs a lane VM), the gate
(`scripts/agent/gate.sh`) and the Lima lane. The fix touches `internal/runtime/containerd`, so the PR's gate adds
the lane (`/fix` Step 5.4).

### Definition of Done

11 of 12 items hold. Item 3 is verified only in part: the tests pass un-skipped on Linux, but `-race` could not run
there (`env`). Items 1 and 11 hold in the form that fits a fix with no issue: a `TestClose_…` regression test and
no `Fixes #N`. Item 4 holds; its two survivors are Minor 1. Item 8 holds for the checks a review runs; the gate and
the lane are still due.

### Model scorecard

No ledger row: a fix row needs an issue number.

### Recommendation

Pass. The branch is ready for the PR gate with the Lima lane. The Minors are optional: a lane step that pins the
composition-root wiring, and a why comment at `m.reap()` in `Ensure`.
