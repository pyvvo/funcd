## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #838 fix, model: claude-opus-5-5)

Change: branch `fix/838-pooled-sibling-degraded`, commit 2baabb6b `fix(function): keep a gated pooled member Ready
when its pool host misses one health probe` (`internal/function/function.go`, `internal/function/pool.go`,
`internal/function/ready_test.go`, `internal/function/shim_test.go`; +56/-11).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — no test pins that `convergePooled` still reads an unanswered probe as not ready** · attribution: `model`.
  The fix gives `memberIn` an error return and discards it in `convergePooled` (`internal/function/pool.go:221`,
  `in, m, ok, _ := r.memberIn(...)`). The commit message states that this path still judges an unanswered probe not
  ready (ADR-0158 Decision 4: "a failed probe ... → not ready (Degraded once serving)"). Mutant M3 propagated the
  error from `convergePooled` instead (`if perr != nil { return verdict{}, 0, perr }`), and the whole
  `internal/function` suite still returned `ok` (9.0s). With M3, a serving member whose pool host misses a probe in a
  converging pass would take `failPass` and keep its status as read, which is not ADR-0158's Degraded. Before this
  change the error did not exist, so the new degree of freedom is the fix's own. Fix: add a converge-pass case to
  `TestIssue838_…` or a sibling test (a serving member, `setMembersDown(true)`, no gate) that requires `Degraded`,
  `Ready=False` and `replicas: 0`.

### Observations (not scored)

- ADR-0161 Decision 1 names only a `List` error as the case where `failPass` keeps phase, `Ready` and replicas as read.
  The fix puts an unanswered `/health/members` probe in the same class: the reconciler could not read the member, so
  it does not judge it. This does not contradict ADR-0161: Decision 2 counts the pool worker "only while its
  `/health/members` entry reads ready", and `countWorkers` now does not count it at all (it returns an error);
  `gateFailed`'s table states that a `List` error goes through `failPass`, and the unanswered probe now does too.
  If a pool host stays silent while a member's gate fails, the member keeps the status it was read with, under the
  engine's error backoff, with `RevisionReady=False/ReconcileFailed` carrying the probe error. The pool host is then
  restarted by a sibling's `ensurePool` (`poolSilent`, `bootTimeout`). A gated solo Function behaves the same way:
  `gateFailed` counts its workers by the stored `Listened` flag and never probes them. No action is needed for this
  issue.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the reported reason.** An overlay of `origin/main`'s `function.go` and `pool.go` with
  the branch's tests: `go test -race -run TestIssue838 ./internal/function/` → `FAIL` at `ready_test.go:553`,
  `expected: "Ready"`, `actual: "Failed"`, "an unanswered probe keeps the phase as read". One unanswered probe sent a
  Ready member, whose pool worker runs, to the gate's phase (`gateFailed`'s "none runs" row), which is the cause the
  issue suspected. The test uses the ShapeInvalid gate (phase `Failed`) instead of the issue's Secret gate; both go
  through the same `gateFailed` → `servingWorkers` → `countWorkers` → `memberIn` path.
- **Passes with the fix**, un-skipped, under `-race` (`--- PASS ... (0.01s)`). The second half of the test then
  restores the probe and requires that the next gate-failed pass keeps `Ready` with `replicas: 1`,
  `RevisionReady=False/ShapeInvalid` and the supervision-period requeue. This covers the issue's second step, where
  the next pass "never restores Ready".
- **Cause, not symptom.** The defect was that `memberIn` returned the same `ok=false` for "the pool host did not answer"
  and for "it answered without this member", and `countWorkers` read both as "no worker runs". The fix separates the
  two. An unanswered probe is now an `api/fault` `Unavailable` error, and `countWorkers` returns it as it returns a
  `List` error, so `gateFailed` hands the pass to `failPass`, whose `lerr` branch keeps the status as read (ADR-0161
  Decision 1, #353). There is no timeout, retry, skip or swallowed error.
- **Mutants**: M1 (`countWorkers` returns `nil` on the probe error) and M2 (`memberIn` returns no error on an
  unanswered probe) each fail `TestIssue838_…`. M3 survives (Minor 1).
- **User-visible behavior**: the issue's own test, `go test -race -count=3 -run TestIssue796 ./pkg/funcd/`, returned
  `ok` (11.2s). The issue's failure was a one-off under host load, so this confirms that nothing regressed, not the
  fix itself. The unit test reproduces the cause deterministically.
- **Scope**: every hunk serves the issue. The changes are the `memberIn` signature, its two callers, two doc comments
  that now name #838, the regression test and the `membersDown` switch on the fake runtime. No test was weakened or
  deleted.
- **Reuse**: `fault.Unavailablef` and the existing `membersPath` constant. `setMembersDown` sits next to the
  existing `setMember` and follows its locking idiom in `fakeRuntime`. Nothing duplicates existing code.
- **Siblings**: `probeMembers` has one caller (`memberIn`). The other live probes are `readyReplicas`' readiness probe
  and `poolSilent`'s liveness probe. Both run only in converging passes, and `poolSilent` is time-bounded by
  `bootTimeout`, so neither has the same cause.
- **ADRs**: no file under `docs/` changed. The change conforms to ADR-0161 Decisions 1–2 (see Observations), to
  ADR-0158 Decision 4 (`convergePooled` still treats an unanswered probe as not ready), and to ADR-0142 (supervision
  by re-convergence: the probe error goes to the engine's backoff and is read again). The asleep paths of
  ADR-0192/0193 never call `servingWorkers`, so they are untouched.
- **Conventions**: `ctx`-first, an `api/fault` error with a `function.<fn>` op, doc comments that state the why and
  the issue, no comment narration in the code.
- **Checks**: `go test -race -count=1 ./internal/function/` → `ok` (10.5s); `go build ./...` and
  `go vet ./internal/function/` pass on darwin and with `GOOS=linux`; `golangci-lint run ./internal/function/...` → `0
  issues.` on darwin and with `GOOS=linux`; `gofmt -l` is clean. No gate run on the branch yet (the repo-wide set
  runs once per PR).
- **Shape**: `fix(function):` subject, `Fixes #838`, the attribution trailer, one issue in one commit.

### Definition of Done

12 / 12 items hold. Item 4 holds for the key lines (M1, M2 killed); Minor 1 records the surviving M3 on the
discarded error in `convergePooled`. E2e and the lanes were not run per review; they run once in the PR gate and CI.

### Model scorecard

Recorded: claude-opus-5-5 on issue #838 (fix) → pass, 0/0/1, 1 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation

Ship. Adding a converge-pass test for an unanswered probe on a serving pooled member would close Minor 1. It can land
with this PR or later; it does not block.
