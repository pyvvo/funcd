# Fix review — issue #142 (a call to a Failed Function waits the 30 s activation timeout, then a 503)

- **Change**: branch `fix/i142`, commit `ea29232` `fix(activator): answer a call to a Failed function at once, not after the activation timeout`
- **Files**: `internal/activator/storescaler/storescaler.go` (+6), `internal/activator/storescaler/storescaler_test.go` (+42)
- **Producing model**: claude-opus-5-5
- **Governing ADRs**: ADR-0016 (activator, C2/C3, storescaler edges), ADR-0142 (Failed/ShapeInvalid never restarted), ADR-0002

## Verdict: changes requested — 0 blockers, 1 major, 3 minors  (issue #142 fix, model: claude-opus-5-5)

The fix is correct for the case the issue reproduces (a function that is *already* Failed when the call
arrives): `ScaleTo(fn, 1)` now refuses the wake with `fault.Unavailable` naming the phase and Ready reason,
and the activator's existing Scaler-error path answers every waiter at once. It does not fix the second
half of the root cause that the issue names — `drive` polls Endpoints until the timeout regardless of
phase — so a function that *becomes* Failed during an activation still holds every waiter for the whole
activation timeout and answers "did not become ready".

### 🟡 Major 1 — a function that turns Failed during the activation still stalls the full timeout  ·  attribution: model

The guard runs only once, when `drive` calls `ScaleTo`. The poll loop in `internal/activator/activator.go`
(`drive`) still reads only Endpoints, so a Failed phase written by the reconciler after the wake is never
seen. The issue's Root cause section names both places ("`drive` polls until activationTimeout regardless
of phase"), and its expected behavior is "a function that is Failed (ShapeInvalid) is answered
immediately".

This path is the common one for scale-to-zero, not a corner case: a runtime ShapeInvalid (the shim cannot
load the handler, `finish` in `internal/function/function.go`) is detected only once a worker starts, so
for a `minReplicas: 0` function it is first set *during* the wake. The idle reclaim edge `* → Idle` also
moves a Failed function back to Idle (`transition`, reclaim case, unchanged), so the stall recurs on the
first call after every idle cycle.

Evidence — a scratch probe in the package (not committed; removed after the run): Idle function, real
`storescaler`, a goroutine that sets Failed/ShapeInvalid as soon as the phase reads Deploying,
`ActivationTimeout: 2s`:

```
first call: 2s 503 {... "detail":"activator.activate: function default/late did not become ready within 2s"}
second call: 0s 503 {... "detail":"activator.activate: scale up default/late: storescaler.ScaleTo: function default/late is Failed (ShapeInvalid)"}
```

Direction for `/fix`: let the activation see a terminal phase while it polls (for example, re-check the
Function's phase in `drive`'s poll loop — the activator already holds `Store` — and resolve at once on
Failed), with a regression test for the became-Failed-mid-activation case. Any Failed seen after the wake
is genuine, because `ScaleTo` already refused a function that was Failed before it.

### 🟡 Minor 1 — mutant survivor: the guard's `target == v1.PhaseDeploying` clause is not pinned  ·  attribution: model

Mutant M1 (drop `target == v1.PhaseDeploying &&`, so a reclaim `ScaleTo(fn, 0)` of a Failed function also
errors) passes every test in the package. No test states whether reclaiming a Failed function still
succeeds; the behavior of the reclaim edge on Failed is now unspecified by the tests.

### 🟡 Minor 2 — the condition type is the literal `"Ready"`  ·  attribution: model

`f.Status.Conditions.Get("Ready")`. The neighbouring packages name it with a package constant
(`condReady` in `internal/function`, `internal/sensor`, `internal/site`, `internal/eventing`). Cosmetic.

### 🟡 Minor 3 — ADR-0016 C3 wording is narrower than the new behavior  ·  attribution: adr (not scored)

C3 says "only a genuine activation failure (timeout) surfaces as 503". The fix adds a non-timeout 503 for
a Failed function. This conforms in spirit — a Failed function is a genuine activation failure, and the
pre-existing Scaler-error path in `drive` already produced a non-timeout 503 — and the storescaler
contract ("writes nothing if the wake would create an off-diagram transition") still holds: nothing is
written. No ADR edit is needed; recorded so a future ADR touching the activator can state it.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason** (overlay of the `origin/main`
  `storescaler.go`): `--- FAIL: TestIssue142_FailedFunctionIsAnsweredWithItsState` — body was
  `"detail":"activator.activate: function default/broken did not become ready within 200ms"`.
  (`git revert --no-commit` also removes the test, so it reports "no tests to run"; the overlay is the
  meaningful check. Worktree then reset to `ea29232`, clean.)
- **Passes with the fix under `-race`**: `--- PASS: TestIssue142_FailedFunctionIsAnsweredWithItsState (0.00s)`.
- **Mutants**: M2 (drop the Ready reason from the message) → test FAIL; M3 (`return nil` instead of the
  error) → test FAIL; M1 survives (Minor 1).
- **Test drives the real path**: real `activator.New` + real `storescaler` + memory store, through
  `ServeHTTP`; asserts the 503, the state-naming detail, the absence of "did not become ready", and that
  the wake does not move the phase.
- **No write on Failed**: the guard returns before `transition`/`Update`, so the activator never writes
  the reconciler's edge (ADR-0016 C2, ADR-0142).
- **Reuse**: `fault.Unavailablef`, `Conditions.Get`, the existing Scaler-error branch of `drive`, and the
  package's existing test helpers (`putFunction`, `phaseOf`, `ref`); `coldEndpoints` is a minimal real
  fake — no existing never-ready Endpoints fake was found in `internal/activator` or `internal/testkit`.
- **Scope**: two hunks, both for the issue; no test weakened or deleted; no ADR file touched.
- **Other `Wake` callers** (`internal/sensor/invoker.go`, `internal/workflow/dispatch.go`) wrap the error
  with its kind and record it — a faster Unavailable changes nothing in their handling.
- **Checks (touched packages)**: `gofmt` clean; `go build ./...` ok; `go vet ./internal/activator/...` ok;
  `go test -race ./internal/activator/...` ok (both packages); `golangci-lint run ./internal/activator/...`
  0 issues. e2e, Linux lint and lanes deferred to the group gate.
- **Shape**: `fix(activator):` subject, `Fixes #142`, attribution trailer, one issue in one commit.

### Definition of Done

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue142_…` reproduces the issue | yes |
| 2 | fails on pre-fix code for the reported reason | yes |
| 3 | passes with the fix, un-skipped, `-race` | yes |
| 4 | reverting / mutating key lines fails a test | no — M1 survives |
| 5 | root cause fixed, not masked | no — the phase-blind poll in `drive` remains (Major 1) |
| 6 | only the issue's scope; no test weakened | yes |
| 7 | no Accepted/Implemented ADR contradicted or edited | yes (C3 wording note, Minor 3) |
| 8 | build, vet, lint, tests green (touched packages) | yes |
| 9 | conventions | yes (Minor 2 cosmetic) |
| 10 | reuse, no duplication | yes |
| 11 | commit shape | yes |

**9 / 11.**

### Model scorecard

claude-opus-5-5 · issue #142 · fix · changes-requested · 0 blockers · 1 major · 3 minors · 3 model-attributed · DoD 9/11.

### Recommendation

Back to `/fix`: keep the `ScaleTo` guard and its test; add a phase check to the activation's poll so a
function that becomes Failed mid-activation is answered at once, with a `TestIssue142_…` case for it; add
a test pinning reclaim of a Failed function; use a named condition-type constant.
