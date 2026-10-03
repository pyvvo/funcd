# Fix review — issue #445 (claude-opus-5-5)

- **Issue**: pyvvo/funcd#445 — a step stopped in retry backoff is labelled "run deadline" when none passed
- **Change**: branch `fix/i445`, commit 6b06907 `fix(workflow): keep a step's dispatch cause when its backoff is stopped`
- **Files**: `internal/workflow/engine.go` (+8/−2), `internal/workflow/subworkflow_test.go` (+64)
- **Governing ADRs**: ADR-0094 (retry backoff, fail-fast), ADR-0099 (inline sub-workflows), ADR-0100 (step error reporting), ADR-0107 (fail-fast siblings back to Pending)
- **Verdict**: **pass** — 0 Blocker, 0 Major, 1 Minor (model)
- **Checklist**: 10 of 11

## Verification run

| Check | Result |
|---|---|
| `TestIssue445_…` on pre-fix `engine.go` (origin/main version checked out over the fix) | **FAIL** in both subtests, for the issue's reason: step `s` error is `workflow.engine: run deadline during backoff: context canceled` (parent-fail-fast) and `…: context deadline exceeded` (run-deadline), not the dispatch cause `retryable 5xx` |
| `TestIssue445_…` with the fix, `-race -count=5` | PASS 5/5 |
| `go test -race ./internal/workflow` | ok |
| `go vet ./internal/workflow` | clean |
| `golangci-lint run ./internal/workflow` | 0 issues |
| Worktree after review | clean, at 6b06907 |

### Mutants (overlay on `engine.go`, whole package run)

| # | Mutation | Result |
|---|---|---|
| M2 | drop the `if stopped != nil { break }` after the backoff select | **killed** (`TestIssue445_…`: `s` dispatched again) |
| M1 | backoff branch sets the old `"run deadline during backoff"` envelope instead of `runStopped(ctx.Err())` | survived |
| M4 | backoff branch always sets `runTimedOut(ctx.Err())` | survived |
| M3 | skip `return nil, stopped`, so the step returns the `"failed after retries"` wrap of `lastErr` | survived |

## Findings

### Blocker
None.

### Major
None.

### Minor

1. **The returned `stopped` error is not covered by any test** (`internal/workflow/engine.go:841-843`, test gap) — *attribution: model*.
   M1, M3 and M4 all survive the whole package. For both subtests the run's error comes from `settle`'s
   `runCtx.Err() != nil` branch (`engine.go:704-706`), which re-derives `runStopped(runCtx.Err())` and
   ignores what `dispatchStep` returned, so the label `dispatchStep` picks is unobservable there. The path
   where the returned value *does* matter is a sibling that waits in backoff when the same run's fail-fast
   cancels `stepCtx`: `settle` (`engine.go:682`) puts it back to `Pending` only if the error `errors.Is`
   `context.Canceled`. `stopped` wraps `ctx.Err()`, so the fix is correct (the commit message says so), but
   M3 — which returns the `lastErr` wrap instead — would mark that sibling `Failed` and break the ADR-0107
   replay, and no test catches it. A subtest with a same-run sibling in backoff when another step fails
   permanently, asserting the sibling is `Pending`, would kill M3.

## ✅ Verified correct

- **Cause fixed, not masked**: the backoff `ctx.Done()` branch no longer returns early; it leaves the loop so
  the ADR-0100 bare-cause stamp (`n.errMsg = capErr(lastErr.Error())`) runs, and `markFailed` keeps that
  stamp (`engine.go:95`). The issue's visible symptom — the step's describe showing the engine envelope
  instead of its dispatch error — is gone in both the stop and the deadline case.
- **Label correct**: `runStopped` (`engine.go:1098`) is reused rather than a new classifier — RunTimedOut only
  for `context.DeadlineExceeded`, "run stopped" otherwise, the same mapping `settle` and `startReady` use.
- **`break` semantics**: the `break` sits after the `select`, so it leaves the `for` loop, not the `select`
  (M2 proves it).
- **Fail-fast sibling path preserved**: the returned error still wraps the context's error, so
  `errors.Is(r.err, context.Canceled)` holds and `settle` resets a cancelled sibling to `Pending` as before.
- **Scope**: two hunks in `dispatchStep` plus one test; nothing unrelated; no test weakened or deleted.
- **Reuse**: the test reuses `retryStep`, `spec`, `step`, `subwfStep`, `childEngine`, `fakeChildren`,
  `stepState` and `fakeDispatcher`; the new `backoffStopDispatcher` is a small wrapper with an ordering need
  (`x` fails only after `s` has failed) that `liveCtxDispatcher` / `stopChildDispatcher` do not cover.
- **Test robustness**: the 50 ms sleep is not load-bearing for correctness — if `s` has not reached the
  backoff `select`, the `ctx.Err() != nil` break still stamps `retryable 5xx`; the 30 s backoff keeps `s`
  in the wait for both subtests. 5/5 under `-race`.
- **Conventions**: `api/fault` errors, ctx-first, top-level imports, comments state the why only, no YAML.
- **ADRs**: no Accepted/Implemented ADR file touched; the change aligns with ADR-0100 (bare cause) and
  ADR-0094 (RunTimedOut only for the run deadline).
- **Shape**: `fix(workflow):` subject, Cause/Fix/Test body, `Fixes #445`, attribution trailer, one commit.

## Checklist

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue445_…` reproduces the behavior | yes |
| 2 | Fails on pre-fix code for the reported reason | yes |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Reverting or mutating the key lines fails a test | partly — revert and M2 killed; M1/M3/M4 survive (Minor 1) |
| 5 | Root cause fixed, not masked | yes |
| 6 | Only the issue's scope; no weakened test | yes |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build, vet, lint, tests green (touched package; Linux lint, e2e and lanes left to the group gate) | yes |
| 9 | Conventions hold | yes |
| 10 | Reuses what exists | yes |
| 11 | Commit shape | yes |

## Recommendation

**pass.** Optionally, in the same PR, add a same-run sibling-in-backoff subtest asserting the sibling returns
to `Pending` after fail-fast, which pins the reason `dispatchStep` must return the context-wrapping error.
