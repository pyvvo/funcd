## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd issue #1291 fix, model: claude-opus-5-5)

Branch `fix/1291-lane-reset-runc-state`, commit d77c3915 `fix(ci): the lanes' simulated host loss removes crun's state
under /run`, on `origin/main` 5c852da5 (`git diff origin/main...HEAD`: `scripts/lane-reset.sh`,
`scripts/lane-leakcheck.sh`, `scripts/lanes_test.go`). Issue: "The lanes' simulated host loss leaves crun's state in
/run" (kind/task, area/ci). The review ran in a worktree detached at d77c3915, and the revert check in a worktree of
`origin/main` with the fix's test file copied in, as the test reads the scripts at runtime.

### Minor
- **The "nothing broader under /run" guard misses a glob** · attribution: `model` · evidence: the test's `NotRegexp`
  (`scripts/lanes_test.go`, `TestIssue1291_…`) catches an `rm -rf` of `/run`, `/run/containerd` or
  `/run/containerd/runc` (each with an optional trailing `/`), but not of a glob under them. A reset that adds
  `rm -rf /run/*`, or `rm -rf /run/containerd/runc/*`, next to the funcd line still passes; both mutants survived. The
  issue's done-when says "and nothing else under /run". · direction: take every path under `/run` that an `rm` in
  `lane-reset.sh` names and require each to be `/run/containerd/runc/funcd-*`.

### Routed (not this work's)
- **#1291's label** · home: elsewhere · destination: the DR session. `/fix` takes `kind/bug` or `kind/flake`, and #1291
  is `kind/task`. The defect is wrong behavior of a test tool (the reset does not simulate a host loss), which the
  taxonomy can read either way; the DR session chose the fix pipeline. Recorded, not scored (`issue`).

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** In the `origin/main` worktree with the fix's
  test file, `TestIssue1291_ResetRemovesTheRuntimeStateOfFuncdNamespaces` fails on "lane-reset.sh removes funcd's runc
  state".
- **It passes with the fix**, with `-race`, beside every `Lane` test of `./scripts/` and the lane guards
  (`go test -run Lane ./cmd/funcd/ ./internal/testkit/langmod/`).
- **Mutants.** Removing the leak check's `runc-state` count fails the test; adding `rm -rf /run` or
  `rm -rf /run/containerd` to the reset fails it. The two survivors are the Minor above.
- **Cause, not symptom.** funcd starts crun through the runc shim with no `Root`, so crun keeps each worker's state
  under `/run/containerd/runc/<namespace>/<id>`, outside the data dir the reset removes, while funcd's private
  containerd keeps its own root and state under that data dir (ADR-0054). Every containerd namespace funcd makes is
  `funcd-<ns>` (`nsPrefix`, `internal/runtime/containerd/containerd_linux.go:59`), so `funcd-*` names all of them and
  nothing else. The other `/run` paths in the code are mount targets inside workers, not host paths.
- **Order and safety.** The removal comes after the unit's SIGKILL, the cgroup kill of every worker, the cgroups'
  removal and the unmounts, so no crun container still runs. With no match the glob stays literal, and `rm -rf` of an
  absent path exits 0 (the script sets only `-u`). The leak check's `ls -d … | wc -l` counts 0 the same way.
- **Scope.** Four lines in the reset, one counter in the leak check, one test; the test follows the static style of
  `TestEveryLaneConfigSetsTheResetDataDir`, as the scripts run as root in the VM.
- **Checks.** `go vet ./scripts/`, host and `GOOS=linux`; `golangci-lint`, host and Linux: 0 issues;
  `TestLaneVMScriptsParse` parses both scripts. shellcheck is not part of the project's toolchain.
- **Conventions.** A `fix(ci):` subject, `Fixes #1291`, the attribution trailer; the reset's comment says why (#1291,
  "A real host's reboot clears /run").
- **Not verified here:** the issue's done-when on a lane ("a simulated host loss on a lane boots with no `already
  exists` line in funcd's journal"). The fixer's lane A/B with the fixed script is running; its line goes into the PR.

### Definition of Done
11 / 12 items hold now; item 8's lane part holds once the fixer's A/B shows no `already exists` line with the fix.

### Model scorecard
claude-opus-5-5 on #1291 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/12 (12/12 with the lane line).

### Recommendation
Ready for the PR with the lane A/B line. Tighten the guard (the Minor) before or in it.
