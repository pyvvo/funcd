## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #87 fix, model: claude-opus-5-5)

Change: branch `fix/i87`, commit b2b318f `fix(funcd): keep the edge ingress limits off internal fn-to-fn invokes`
(`pkg/funcd/funcd.go`, `pkg/funcd/invoke_e2e_test.go`, `pkg/funcd/limits_e2e_test.go`).

The cause named in the issue is that `pkg/funcd/funcd.go` late-bound the worker-node local API invoker
(`dpHolder`, ADR-0064) to the full data-plane listener chain, including `limit.Chain`. The fix builds
`dataplane.Handler` once (`dpCore`) and gives the listener `Recover → RequestID → observ → limit → shape → dpCore`,
while the invoker gets the same chain without `limit.Chain`. The listener chain and the ordering
in ADR-0114 do not change. Internal calls still go through `observ` and `shape`, as they did before the fix.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The test copies the fn-to-fn deploy sequence inline** · attribution: model · evidence: the `deploy`
  closure in `pkg/funcd/limits_e2e_test.go` (`tsExample` → `pushExampleFn` ×2 → `applyFnObj` ×2 → `waitReady`)
  repeats a block that already appears inline five times in `pkg/funcd/invoke_e2e_test.go` (lines 129, 150, 173, 196 and 238).
  Every step reuses an existing helper, and the inline form matches the file's current idiom, so the
  cost is small. · fix (optional): extract one shared `deployFnToFn(t, c)` helper next to `loadFn` in
  `invoke_e2e_test.go`.

### ✅ Verified correct (keep it)
- **The test fails without the fix, for the issue's reason.** After `git revert --no-commit b2b318f`, with the
  new tests restored, `go test -tags e2e -run TestIssue87_ ./pkg/funcd/` gave FAIL. In `maxInFlight`, call 0
  returned `context.invoke("greeter") failed: 503 … edge.limit: in-flight concurrency ceiling reached`. In
  `rate`, the nested call returned `429 … edge.limit: rate limit exceeded`. These are the two failure modes the issue reports.
- **It passes with the fix under `-race`.** After `git reset --hard b2b318f`, the run with `-race -v` showed both subtests
  `--- PASS` and `ok github.com/pyvvo/funcd/pkg/funcd`. Neither subtest was skipped, because Node was present.
- **The issue's own steps pass.** The test is the issue's e2e probe on the real platform: the Node shim, the
  OCI deploy path, and the `fn-to-fn` example, with `maxInFlight: 1` (3 sequential calls) and a rate key per function
  (`RatePerMin: 1, Burst: 1`).
- **Mutants:**
  - M1 binds the invoker back to `dpHandler`. TestIssue87 fails, so the mutant is killed.
  - M2 drops `limit.Chain` from the listener chain. `TestScenarioE2ELimitsRateLimit` and `TestScenarioE2ELimitsBodySize`
    fail, so the mutant is killed and the fix cannot quietly turn off the ingress limits.
  - M3, a contrived mutant, adds a separate limiter with ceiling 1 to the internal chain. It survives, as expected: each external call
    makes only one nested call. The realistic regression, sharing the listener's limiter, is caught by M1.
- **The root cause is fixed, not masked.** There is no timeout, retry or swallowed error. Internal traffic no longer enters the
  limiter, which matches ADR-0112: the limiter guards the data-plane listener. It also matches ADR-0064 and ADR-0110: internal
  invocation is by name and is not gated. No ADR file was edited.
- **Scope:** every hunk serves the issue. The variadic `opts ...funcd.Option` on `shimPlatformOCI` is
  backward-compatible, and all 12 call sites compile (`go vet -tags e2e` is clean). No test was weakened.
- **Reuse:** the fix reuses the same `dataplane.Handler` instance and the same `observ` and `shape` middlewares instead of building a
  second dataplane. The test uses the existing `shimPlatformOCI`, `tsExample`, `pushExampleFn`, `loadFn`, `applyFnObj` and `waitReady`.
- **Conventions:** the imports are at the top level, there is no YAML, and there is one comment that states why. The test is named `TestIssue87_…`.
- **Checks on the touched package:**
  - `gofmt -l pkg/funcd` reports nothing, and `go build ./...` passes.
  - `go vet ./pkg/funcd/` passes with and without `-tags e2e`.
  - `golangci-lint run ./pkg/funcd/...` reports 0 issues with and without `--build-tags e2e`.
  - `go test -race ./pkg/funcd/` passes.
  - `go test -tags e2e -race -run 'TestScenarioE2ELimits|TestIssue87_|OCI|FnToFn' ./pkg/funcd/` passes.
- **Shape:** the subject follows `fix(funcd):`, and the body has `Fixes #87` and the attribution trailer. There is one commit for one issue.

### Definition of Done
11 / 11 items hold. Item 8 was checked only on the touched package. Linux lint, the full e2e suite and the Lima lanes
are left to the group gate, as this review was told.

### Model scorecard
Not recorded by this review. The ledger fields are returned to the caller: claude-opus-5-5 on issue #87 (fix) → pass,
0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Sign off. The single Minor is optional cleanup of the test and does not block the merge.
