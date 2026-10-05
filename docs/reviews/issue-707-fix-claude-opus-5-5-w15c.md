## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #707 fix, model: claude-opus-5-5)

Change: commit b79d295f on `fix/w15c-i707`, `fix(runtime): wait until the private containerd listens, not until its socket file exists`.
Files: `internal/runtime/ctrmanager/manager_linux.go` (the fix), `manager_linux_test.go` (the regression test and two
test helpers), `lifetime_linux_test.go` (`TestIssue630` adapted, as the issue said it would need to be).

The touched files are `linux`-only, so every test ran in a Linux container (`golang:1.26.4`, as root, read-only module
cache, `GOPROXY=off`), the same setup as the issue's repro. `internal/runtime/ctrmanager` is unchanged between the
branch's merge base and the current `origin/main`, so the overlay of the `origin/main` file is the code of current main.

### Minor 1 — one doc-comment line left unwrapped · attribution: model
`internal/runtime/ctrmanager/manager_linux.go:97` is 144 characters: the rewrap of the `startContainerd` doc comment
joined "…containerd-shim-runc-v2 + crun there. ctx bounds only that wait: the child runs until Close, so the" into one
line, while the rest of the comment wraps near 100. The linters accept it (`0 issues`); it is cosmetic. Fix: rewrap the
line.

### ✅ Verified correct (keep it)
- **Prove-first holds: the regression test fails on current main for the issue's reason.** With
  `go test -overlay` of the `origin/main` `manager_linux.go` (the test files stay):
  `manager_linux_test.go:76: startContainerd returned (<nil>) while its socket was a stale file no containerd listened on`
  `--- FAIL: TestIssue707_StartWaitsForAListeningSocketNotAStaleFile`. The other package tests pass under the overlay,
  so the failure is the stale-file case only.
- **It passes with the fix, under `-race`**: the whole package `ok` with `-race -count=1`; `TestIssue707…` with
  `-race -count=20` `ok` (1.05 s; `synctest` keeps the 100 ms poll and the 30 s deadline on fake time). The test is not
  skipped in the container (`--- PASS` listed with `-v`).
- **Cause, not symptom.** The wait loop now returns only when `net.Dial("unix", m.socket)` succeeds. A stale socket
  file refuses the connect (ECONNREFUSED), and containerd v2 listens only after its plugins are up (the issue's root
  cause), so `Ensure` no longer returns before the daemon serves. No timeout was lengthened and no error swallowed; the
  timeout error text now says "did not listen on" instead of "did not create". Not removing the old socket before
  `cmd.Start` is sound: containerd unlinks it itself before it listens.
- **Mutants** (overlays, `-run 'TestIssue707|TestIssue630|TestPrivate'`):
  - M1, condition inverted (`dialErr != nil`): killed — `TestIssue630` and both `TestPrivate…` fail
    ("did not listen … within 30s"), and `TestIssue707` panics on the stale-file case.
  - M2, dial a wrong path (`m.socket+".x"`, never listens): killed — all four fail, `TestIssue707` with
    `startContainerd once containerd listens: … did not listen …`.
  - M3, the probe connection not closed (`_ = conn`): survives. It leaks one descriptor per start and is not
    observable by a unit test; recorded for completeness, not a finding.
- **Scope.** Every hunk serves the issue: the readiness check, its comment and error text, the regression test, and
  the fakes of `TestIssue630` and `startFakeContainerd`, which now listen on the socket in-process instead of creating
  a plain file (the issue predicted `TestIssue630` must change). No assertion was weakened or deleted; the fakes'
  data roots moved from `t.TempDir()` to `shortRoot` because a listening socket path must fit the Unix limit (#41).
- **Reuse.** No helper elsewhere waits on a Unix socket (`net.Dial("unix"` appears nowhere else in non-test code); the
  standard library dial is lighter than the issue's suggested `containerd.New` + `IsServing` per poll, and `Ensure`
  still builds the client right after. `shortRoot` mirrors the package-local `shortDataDir` helpers of `cmd/funcd`
  and `pkg/funcd` (test-only, not importable from this package).
- **Siblings.** The only other socket `os.Stat` (`cmd/funcd/bench.go:402`, `doctorContainerd`) is a presence probe
  of an external socket for a doctor report, not a readiness wait; no sibling has the same cause.
- **Every case of the issue is tested**: the stale-socket case by `TestIssue707…`; the no-stale-file control (fresh
  start) by `TestPrivateContainerdHasItsOwnProcessGroup`, `TestPrivateContainerdLogsThroughAPipe` and `TestIssue630`.
- **ADRs.** The change brings the code in line with ADR-0054 (the Manager owns the private containerd's health) and
  ADR-0167 Decision 8 (the boot sweep reaches the daemon); no ADR file is touched.
- **Checks** (touched package): tests with `-race` green on Linux; `go vet` green on Linux and on the host;
  `golangci-lint` `0 issues` with `GOOS=linux` and on the host.
- **Conventions.** `api/fault` errors kept, ctx-first, imports at top level, test comments state the why.
- **Shape.** `fix(runtime):` subject, `Fixes #707`, the attribution trailer, one issue in one commit.

### Definition of Done
12 / 12 items hold (e2e and the Lima lanes are left to the group gate, as this wave's scope says).

### Model scorecard
Ledger fields: claude-opus-5-5 on issue #707 (fix) → pass, 0/0/1, 1 model-attributed, DoD 12/12. Not recorded here;
the batch ledger PR records it.

### Recommendation
Ready to integrate. Optionally rewrap `manager_linux.go:97` before the PR.
