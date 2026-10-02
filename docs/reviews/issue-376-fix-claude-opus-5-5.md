# Fix review — issue #376 (provider readiness probe opens a new TCP connection per call)

## Verdict: pass — 0 blockers, 0 majors  (issue #376 fix, model: claude-opus-5-5)

Change: branch `fix/i376`, commit `505adba` — `internal/provider/probe.go` (+10/−1),
`internal/provider/runtime_test.go` (+30).

No blocker, major or minor findings.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 505adba` with the test file
  restored: `TestIssue376_ReadinessProbeReusesConnection` fails with `expected: int(1) actual: int32(10)`.
  That is 10 probes and 10 new connections, which matches the issue's reproduction exactly.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/provider/` passes. After the
  check, the worktree was reset to `505adba` and is clean.
- **The root cause is fixed, not hidden.** `probeReady` now reads at most `probeBodyMax` (4 KiB) of the body
  before `Close`. A body read to EOF goes back to the transport's keep-alive pool. The fix adds no timeout,
  no retry and no swallowed error, and the probe's ready/not-ready result does not change.
- **Mutants.** Setting `probeBodyMax = 0` is killed (10 connections). Setting `LimitReader(resp.Body, 1)` is
  killed (10 connections). Deleting the drain line is killed by a compile failure (`io` becomes unused),
  and the full revert above already covers that case.
- **The test checks what users see, end to end.** The test drives the public `provider.NewRuntime` →
  `Converge` path 10 times. It counts `http.StateNew` on a real `httptest` server, so it tests what a user
  would see, not a private helper.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted.
- **Reuse.** The drain idiom and the `probeBodyMax = 4 << 10` constant match the Function readiness probe
  from #236 (`internal/function/function.go`), as the commit says. The repo has no shared drain helper, so
  the fix does not reinvent existing code. It keeps the two probes consistent with each other.
- **Conventions.** The `io` import is at the top level, there is one short "why" comment for the constant,
  and the test follows the `TestIssue<N>_…` naming used in the same file. Errors are discarded with `_ =`,
  as the surrounding code does.
- **ADRs.** ADR-0087 (provider readiness probe) is unchanged in behavior. The fix supports ADR-0041's
  connection-pooling intent. No ADR file was edited.
- **Checks (touched package only).** `go vet ./internal/provider/` is clean.
  `golangci-lint run ./internal/provider/` reports 0 issues. The tests pass with `-race`.
- **Shape.** The subject is `fix(provider): …`. The body contains `Fixes #376` and the attribution
  trailer. There is one issue per commit.

### Definition of Done

10 of 10 applicable items hold. Item 8 was checked for the touched package only: host build, vet, lint and
race tests. The group gate runs Linux lint and the repo-wide tests.

### Model scorecard

claude-opus-5-5 — pass. Blockers 0, majors 0, minors 0. Model-attributed findings: 0.

### Recommendation

Merge with the group. No rework is needed.
