## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1126 fix, model: claude-opus-5-5)

This report reviews branch `fix/1126-pool-full-writer-rv`, one commit 38fac7ea on main e34dadad:
`fix(funcd): pooled-rig tests give their local pool host a 500 ms probe budget`. The diff
(`git diff origin/main...HEAD`) touches `pkg/funcd/funcd.go` and `pkg/funcd/issue796_internal_test.go`. The issue is
the flake of `TestScenarioPoolFull/newcomer-not-asleep` ("writer is untouched", -4 → -9).

### ✅ Verified correct (keep it)
- **The cause is the one the fix names, and the issue's guess is ruled out.** The probe of a pooled worker's
  `/health/members` is bounded by `probeTimeout = readinessPoll / 2` (100 ms, `internal/function/function.go:1271`).
  - The regression test makes the fake pool host answer after 150 ms. On a tree without the fix, the writer's version
    moves -4 → -8 while zulu is admitted; that is the flap through Degraded/Restarting.
  - The writer was settled when its version was read: with the fix and the same 150 ms answer, the version stays put.
- **The regression test fails without the fix, for the issue's reason.** With the rig's probe option turned into a
  no-op, so that the platform keeps its 100 ms default, `TestIssue1126_WriterUntouchedWhenMembersAnswerSlowly` fails 2
  of 2 at "writer is untouched" (`…-4` expected, `…-8` actual). The failure is deterministic, not load-dependent.
- **It passes with the fix.** `-race -count=3` on `TestIssue1126_…` and `TestScenarioPoolFull` (both subtests) passes
  every run, and none is skipped.
- **Mutants.**
  - M1, which drops the `ProbeTimeout: c.functionProbeTimeout` wiring in `buildControlPlane`, fails the test 2 of 2.
  - M2, which sets the rig's budget to 120 ms, below the 150 ms answer, also fails it 2 of 2.
- **Cause, not symptom.** The test's subject is that a newcomer to a full pool leaves the admitted writer alone, which
  has nothing to do with probe latency.
  - The 100 ms default is sized for a local shim that answers in microseconds. `function.Config.ProbeTimeout`
    (`function.go:201`) already states the rule the fix follows: "Tests that answer probes from local servers raise
    it". `internal/function/shim_test.go:582,681` raise it to the same 500 ms.
  - So the longer budget applies the documented test rule to the test's own fake host. It hides no production
    defect, and no assertion was weakened.
- **Scope and reuse.**
  - The config field is unexported, with no public option, following `bucketMaxPerNamespace` (set by
    `WithBucketQuotaForTest`). It is wired once, in `buildControlPlane`.
  - The scenario's body moved into `newcomerNotAsleep(t, slow)` unchanged, so the original subtest runs it with no
    delay.
  - `poolHost.slow` is the one new test seam. No test was deleted or weakened.
- **Siblings.** Every pooled-rig test gets the budget through `newPooledRig`. The 8 non-pooled `newAsleepRig` uses
  keep the default; no failure of theirs is on record, and the prove-before-fix rule leaves them.
- **Conventions and shape.** The comments name #1126 and say why. The subject is a conventional `fix(funcd):`, with
  `Fixes #1126` and the trailer, in one commit.
- **Checks.** gofmt is clean. `go build ./...`, `go vet ./pkg/funcd/` and `golangci-lint run ./pkg/funcd/` pass, the
  last with 0 issues, and `go test -race ./pkg/funcd/` passes (20.4 s). The repo-wide gate runs once on the final
  head, before the PR.

### Note (not scored, for the decider)
In production, one `/health/members` answer past 100 ms reads a ready pooled Function's member as unready and writes
Degraded with "a replica exited", though nothing exited. That is a question about the probe's semantics, not this
fix's job. The fixer is sending it to the DR session.

### Definition of Done
11 / 12 items hold. Item 8 is pending: the gate (repo-wide `ci-full`, the Linux build and the Linux tests) has not run
on this head yet (env).

### Model scorecard
Recorded: claude-opus-5-5 on #1126 (fix) → pass, 0/0/0, DoD 11/12.

### Recommendation
Ready for the gate and the PR.
