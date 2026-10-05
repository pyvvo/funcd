## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #700 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i700`, commit 7021b440 `fix(funcdctl): stop funcdctl dev and its workers on a hangup`.
The commit moves the daemon's stop-signal set (`stopSignals` in `cmd/funcd/main.go`, from #625) to the new
package `internal/platform/stopsignal` and uses it in the daemon, `funcd bench --containerd` and `funcdctl dev`
(`cmd/funcdctl/dev.go:141`). The daemon's two signal tests move with the code. The regression test is
`TestIssue700_DevHangupStopsWorkers` in `cmd/funcdctl/dev_crash_test.go`.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Proof first, on current `origin/main`**: the regression test was run with `-tags dev` and an overlay of the
  `origin/main` version of `cmd/funcdctl/dev.go`, the only changed non-test file that `cmd/funcdctl` uses.
  `cmd/funcdctl`, `cmd/funcd` and `internal/platform` do not differ between the branch's merge base and
  `origin/main` (a394c6f1). The test fails for each reason that the issue states:
  - `the hangup left the worker running` (the worker survives `funcdctl dev`);
  - `Received unexpected error: signal: hangup`, with the message `dev did not stop gracefully on a hangup`.
    Go's default action killed the process, which matches `exit=129` in the issue;
  - `the hangup left the dev shim temp dir` (the `funcdctl-dev-shim*` directory still exists).
  The captured dev output ends at `platform providers`, with no `platform stopping` line, so the stop path never
  ran. This matches the root cause that the issue names.
- **Passes with the fix**: `go test -race -tags dev -run '^TestIssue700_|^TestDevRestart|^TestDevStateDir' -count=3
  ./cmd/funcdctl/` passed (`ok`, 9.6 s). `go test -race ./internal/platform/stopsignal/ ./cmd/funcd/` passed (`ok`
  for both). The test runs un-skipped on a host that has the Node runtime.
- **The test checks the user-visible behavior**: it re-executes the test binary as a real `funcdctl dev --persist`
  run and sends SIGHUP to that process only. It then checks the three effects from the issue: the worker process
  (found through the ADR-0167 `procreg` registry) is gone, the exit is clean, and the shim directory (read from
  the worker's argv) is removed. This is the issue's own reproduction as an automated test.
- **The test is not weakened under nohup**: the parent handles SIGHUP before it forks the child, so the child
  starts with SIGHUP at its default action and does not inherit an ignored SIGHUP from a test run under nohup.
  The comment explains why, which is not obvious from the code.
- **Mutants** (overlays; the tree was clean afterwards):
  - Revert `dev.go` to `os.Interrupt, syscall.SIGTERM`: `TestIssue700_…` fails (the proof above).
  - M1: remove `syscall.SIGHUP` from the set that `Signals()` returns when SIGHUP is not ignored.
    `TestHangupStopsGracefully` fails and `TestIssue700_DevHangupStopsWorkers` fails.
  - M2: disable the `signal.Ignored(syscall.SIGHUP)` guard. `TestNohupKeepsHangupIgnored` fails.
  Every mutant failed a test.
- **Cause, not symptom**: SIGHUP joins the set of signals that run `inst.stop()`, so `devInstance.stop`/`unwind`
  and the process driver's Close run, as they do for SIGTERM. The change adds no timeout, retry or swallowed error.
- **Reuse, no duplication**: the change does not copy the daemon's helper. It moves the helper to one shared
  package, and all three callers use it. After the change, no SIGHUP stop-set logic remains anywhere else in
  production code. The test reuses `savedWorkers`, `devProject`, `requireRuntime` and `permissiveContract`. It
  extracts `reapAtCleanup` from `devRestartReaps`, so the cleanup block exists once instead of twice.
- **Scope**: every hunk serves the issue. The issue suggests sharing the daemon's stop-signal set. The bench call
  site is part of the same move, so its behavior does not change. The moved tests keep every assertion; only
  their wording changed from "funcd" to "a funcd process".
- **Siblings**: `grep` for `signal.NotifyContext`/`signal.Notify(` in non-test code finds the three callers that
  now share `stopsignal.Signals()`, and `docs/demo/server/main.go`. The demo is a standalone HTTP server with no
  child processes or process groups, so it does not have this issue's cause.
- **Conventions**: imports are at the top level, the comments explain why (no narration), the package doc names
  its role, and the location (`internal/platform/` beside `config`, `version`, `clock`) fits the cross-cutting
  helpers. The changed code adds no `any` signatures and no new dependency.
- **ADRs**: no ADR file was edited. ADR-0125 says nothing about signals, and ADR-0167's accepted orphan risk
  covers SIGKILL crashes, not a stop signal. The change contradicts neither.
- **Checks** (touched packages, host): `go vet` on `./internal/platform/stopsignal/ ./cmd/funcd/ ./cmd/funcdctl/`
  (with and without `-tags dev`) passed. `golangci-lint run` on the same packages, with and without
  `--build-tags dev`, reported `0 issues.` Linux lint, e2e and the repo-wide gate run once in the group gate.
- **Commit shape**: the subject is `fix(funcdctl): …`, the body has `Fixes #700` and names the regression test,
  and the commit has the attribution trailer and covers one issue.
- **Every case in the issue is covered**: the hangup stop (worker, exit, shim dir) is tested by
  `TestIssue700_…`, and the nohup case ("stays ignored") is tested by `TestNohupKeepsHangupIgnored`. The issue's
  "not tested" devengine duckdb case uses the same `inst.stop()` path that the fix now reaches. The issue does not
  claim it as a separate case.

### Definition of Done
12 / 12 items hold. For item 8, the host build, vet, lint and tests ran here. The Linux lint and the e2e suite
belong to the group gate.

### Model scorecard
Ledger fields (not recorded here; the batch's ledger PR records them): claude-opus-5-5 on issue #700 (fix) →
pass, 0/0/0, 0 model-attributed, DoD 12/12.

### Recommendation
Pass. Hand the change to the group integrator. The change is ready for the group gate.
