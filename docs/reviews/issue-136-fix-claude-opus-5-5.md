## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #136 fix, model: claude-opus-5-5)

Change: branch `fix/i136`, one commit `d7e6454 fix(sdk): refuse redirects that turn a write into a GET`
(`pkg/sdk/sdk.go`, `pkg/sdk/sdk_test.go`). Governing ADRs: ADR-0024 (SDK `Apply` returns the server's
stored object), ADR-0012 (TLS termination sits outside funcd), ADR-0002 (conventions).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **Test gaps on two secondary behaviors of the policy** · attribution: model · evidence: mutant M3
  (`len(via) >= 10` → `>= 1000`) survives `go test -run 'TestIssue136|TestScenario' ./pkg/sdk/` (`ok`).
  The 10-hop cap that the new policy re-implements (Go's `defaultCheckRedirect` is unexported, so the
  re-implementation itself is justified) has no test. Nothing checks either that `New` leaves a
  caller-supplied client, or `http.DefaultClient`, unmutated (the struct copy at `pkg/sdk/sdk.go:60`);
  a mutant that assigned `CheckRedirect` on the shared client in place would also pass. Neither gap
  touches the issue's defect. Fix: add one subtest with a redirect loop on a 307 and one that asserts
  `http.DefaultClient.CheckRedirect == nil` after `sdk.New`.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit d7e6454`
  with the fix's test file restored: `TestIssue136_WritesRefuseMethodChangingRedirect` fails on the
  301, 302 and 303 subtests with "Apply reported the redirected GET as the stored object; seen [PUT
  …/functions/fn1 GET /final DELETE …/functions/fn1 GET /final]", which is exactly the issue's PUT →
  GET /final, err=nil trace. The 307 and 308 subtests pass on both sides.
- **Passes with the fix under `-race`.** After `git reset --hard d7e6454`: `go test -race -count=1
  ./pkg/sdk/` → `ok`. The worktree was left clean at that HEAD.
- **Mutants.** M1 (the method-change test disabled) → FAIL. M2 (every redirect refused) → FAIL, because
  the 307/308 subtests catch it. M4 (the `CheckRedirect == nil` guard inverted, so the default client
  gets no policy) → FAIL. M3 survives (see Minor).
- **Root cause, not symptom.** The issue names the missing `CheckRedirect` on the SDK's default client.
  `New` now installs `refuseMethodChange` on any client without its own policy, the default one
  included. Every funcdctl path (`cmd/funcdctl/cli.go` `sdkClient`, `dev.go`) goes through `sdk.New`,
  so the CLI is covered without a separate change. The policy copies the `http.Client` struct
  rather than mutating `http.DefaultClient` or the caller's client. A caller's own `CheckRedirect` is
  respected. GET redirects and 307/308 still follow, so the refusal hides no legitimate use. The
  error flows through `do()` as a `fault.Unavailable`, the same kind as any transport failure, and
  Apply does not mistake it for the 404 → POST fallback.
- **Matches the issue's expected behavior and ADR-0024.** A redirecting intermediary now surfaces as an
  error with a clear message (the code, the target and the method downgrade, plus "point the server
  URL at the final address"), never a false success. No Accepted/Implemented ADR is contradicted or
  edited; no living doc needed a change.
- **Scope.** Two files: the policy plus its wiring and doc comment in `sdk.go`, the regression test in
  `sdk_test.go`. No test was weakened or deleted.
- **Reuse.** No existing redirect policy in the repo to reuse (searched for `CheckRedirect` and
  `ErrUseLastResponse` across all Go sources). The standard library exposes no composable default
  policy, so the small re-implementation of the 10-hop cap is the minimum. The test reuses the file's
  `newFunction` helper and `httptest`.
- **Conventions.** Top-level imports, ctx-first calls, functional-options facade untouched, doc comments
  explain the why without bloat, table-driven test with `t.Parallel` and a mutex around the shared
  slice. `go vet ./pkg/sdk/` clean; `golangci-lint run ./pkg/sdk/...` → 0 issues.
- **Shape.** Subject `fix(sdk): …`, body explains cause and fix, `Fixes #136`, attribution trailer, one
  issue per commit.

### Not run here (by design)
Repo-wide tests, Linux lint, e2e and the Lima lanes: the group gate runs them.

### Recommendation
Pass. The Minor can be folded into a follow-up or left as is; it does not block.
