# Fix review — issue #90 (claude-opus-5-5)

- **Issue**: #90 — Data-plane servers set no body read timeout; silent connections hold all slots
- **Change**: branch `fix/i90`, commit `9ae5a35` — `fix(funcd): bound the data-plane request read so a silent body cannot hold an in-flight slot`
- **Files**: `pkg/funcd/funcd.go` (+4/-1), `pkg/funcd/limits_e2e_test.go` (+36)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**

## Summary

The data-plane `http.Server` now sets `ReadTimeout: 10s` next to the existing `ReadHeaderTimeout`.
Before the fix, a client could send complete headers and then stop sending the body. `dataplane.Handler`
then stayed blocked in `io.ReadAll`, and the ADR-0112 in-flight slot stayed taken. With the fix, the
stalled read fails at the deadline, the handler returns, and the limiter releases the slot. This removes
the cause named in the issue: nothing bounded the body read. The regression test reproduces the issue on
the real listener with a raw TCP connection that goes silent after 5 of 100 body bytes. The test fails
without the fix and passes with it.

## Verification run

| Check | Result |
|---|---|
| Revert (`git revert --no-commit 9ae5a35`, test file restored from the fix commit) → `go test -tags e2e -race -run TestIssue90_` | **FAIL** (20.1s): `Condition never satisfied` — "the read deadline must cut the silent request and free its slot". The silent connection held the only slot for the full 20s window. This is the issue's reason. |
| `git reset --hard` to the starting HEAD → same test, `-race`, `-v` | **PASS** (10.13s); the slot is freed when the 10s deadline fires |
| Mutant 1: `ReadTimeout` 10s → 30s | killed (FAIL, 20.06s) |
| Mutant 2: `ReadTimeout` → `WriteTimeout` on the data-plane server | killed (FAIL, 20.07s) |
| Mutant 3: `ReadTimeout` moved to the control-plane server (`p.httpServer`) | killed (FAIL, 20.04s) |
| Long handler is not cut (scratch probe, Go 1.26.3: `ReadTimeout` 1s, handler reads the body and then waits 3s on `r.Context()`) | context not canceled, 200 returned. net/http clears the read deadline before its background read, so the code comment's claim holds. |
| `gofmt -l pkg/funcd` | clean |
| `go vet ./pkg/funcd/` and `go vet -tags e2e ./pkg/funcd/` | ok |
| `golangci-lint run ./pkg/funcd/...` (with and without `--build-tags e2e`) | 0 issues |
| `go test -race ./pkg/funcd/` | ok |
| `go test -tags e2e -race -run 'TestScenarioE2ELimits\|TestIssue90'` | ok (the existing rate-limit and body-size scenarios still pass) |
| `go test -tags e2e -race -run 'TestScenarioE2EEdge\|TestScenarioE2EFullEdge'` (CORS and full-edge-chain streaming) | ok |
| Worktree after the review | at the starting HEAD, clean |

Not run here because the group gate runs them: the full e2e suite, repo-wide tests, Linux lint and the Lima lanes.

## 🔴 Blockers

None.

## 🟡 Majors

None.

## Minors

1. **The read bound on legitimate slow uploads is undocumented** (`model`). `ReadTimeout` is a whole-request
   deadline. A legitimate client must now deliver its whole body, up to `MaxBodyBytes`, within 10s of the
   request start, and the value is not configurable. The commit message documents the side effect on idle
   keep-alive connections (they close after 10s because `IdleTimeout` is unset). It does not mention this
   effect on slow uploads. For the platform's JSON invoke bodies this is a reasonable default, so it does
   not block the fix. A line in the commit or PR text would make the trade-off visible.

## ✅ Verified correct

- **Root cause, not symptom**: the change bounds the body read itself. It does not raise or lower a limiter
  knob, add a retry, or swallow an error. The slot is released because the handler actually returns.
- **Scope**: one line of production code and one regression test. The control-plane server
  (`pkg/funcd/funcd.go:887`) and the worker-node local servers keep only `ReadHeaderTimeout`. They are
  outside the issue's data-plane scope, and the fix correctly left them unchanged.
- **No conflict with long-running handlers**: SSE/streaming and long activations are not cut. This is shown
  by the scratch probe and by the passing full-edge-chain streaming e2e test.
- **Test quality**: the test uses the real `bringUp` platform and a raw `net.Dial`, which matches the issue's
  probe. It first asserts that the slot is held (503), then that the slot is freed (404 for an unknown
  function, which means the request reached the handler). It reuses `bringUp`, `applyFn` and
  `writeArtifact` from the package's e2e helpers. `postStatus` is new, and no existing helper does the same
  thing (searched `pkg/funcd/*_test.go` and `internal/testkit`).
- **Reuse**: the fix uses the standard library's `http.Server.ReadTimeout`. Nothing was hand-rolled.
- **Conventions**: imports at top level, no YAML, a single "why" comment that cites ADR-0112 and #90, and a
  test name in the `TestIssue<N>_…` form under the existing `e2e` build tag.
- **ADRs**: ADR-0112's "acquire on entry, release on exit" contract is unchanged. No ADR or living doc was
  edited, and none states that the data plane has no read timeout.
- **Shape**: `fix(funcd):` subject, `Fixes #90`, the attribution trailer, and one issue in one commit.

## Fix checklist

11 of 11 apply, and 11 pass. Item 8 is limited to the touched package. The group gate runs the
repo-wide, Linux and e2e checks.

## Recommendation

Pass. Optionally, state the slow-upload bound (the whole body must arrive within 10s) in the PR description.
