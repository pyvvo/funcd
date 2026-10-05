## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #697 fix, model: claude-opus-5-5, re-review 2)

Change: branch `fix/w15c-i697`, commits aa7acb4c `fix(artifact): bound the wait for a registry's response headers`
and 18c19e6f `fix(artifact): address review of #697`. The registry transport in `registryClient`
(`internal/artifact/artifact.go`) now sets `ResponseHeaderTimeout = 10s`, and a new `registryRetry` policy wraps
oras-go's `retry.DefaultPolicy` but does not retry a timed-out request. Tests: `TestIssue697_StalledRegistryCallEnds`
(`internal/artifact/stall_test.go`) and `TestIssue697_StalledRegistryDoesNotStopOtherReconciles`
(`tests/chaos/registry_stall_test.go`), over a shared stub in `internal/testkit/stallregistry`.

### Round-1 findings
- **Major 1 (model), resolved.** The stub now stalls every request until the client abandons it, as in the issue's
  reproduction. The fix no longer lets oras-go retry a header timeout five times (about 67 s per call): each call
  ends after one 10 s wait, and the test asserts exactly one request per repository. The test comment states the
  real bound ("one wait for response headers, which is not retried, and a margin").
- **Minor, platform case in `internal/artifact` (model), resolved.** The whole-platform case moved to `tests/chaos`,
  with a short `os.MkdirTemp("", "funcd")` data dir.
- **Observation, large uploads: no longer a concern.** `ResponseHeaderTimeout` starts after the request, body
  included, is fully written, so a slow blob PUT is not cut off by it.

### 🟡 Minor
- **A stall after the headers is still unbounded** · issue. Carried from round 1. A registry that sends headers and
  then stops sending a body is not bounded by `ResponseHeaderTimeout`. The issue reports, and reproduces, only the
  header stall, and the issue's own suggested fix is a `ResponseHeaderTimeout`. Not scored against the model.

### ✅ Verified correct (keep it)
- **Proof on current main (prove-first rule).** `internal/artifact/artifact.go` is identical at the merge base and at
  current `origin/main` (a394c6f1). With an overlay of the `origin/main` file and the tests kept, both tests fail for
  the issue's reason: all five unit subtests (`resolve`, `platforms`, `materialize`, `site`, `contract`) report "the
  call still waits on the stalled registry after 30s"; the chaos test reports "a KVStore applied while a Function's
  reconcile waits on a stalled registry never reconciles" (30.05 s).
- **Passes with the fix under `-race`:** each unit subtest ends in 10.01 s; the chaos test passes in 10.16 s (KVStore
  `after` reconciles, the Function reports `Failed`); no data race. `go test -race ./internal/artifact/...` passes.
- **Mutants, each fails all five unit subtests with the stall message:**
  - M1 removes the `ResponseHeaderTimeout` assignment;
  - M2 makes `registryRetry` delegate timeouts to `retry.DefaultPolicy` (the retry storm returns: over 30 s).
- **Cause, not symptom:** the request that had no deadline is now bounded, and the fix removes a retry rather than
  adding one; the error surfaces as `net/http: timeout awaiting response headers` and the reconcile requeues.
- **Coverage:** every call path the issue names (`Resolve`, `Platforms`, `Materialize`, `ResolveSite`,
  `InspectContract`) and the cross-kind starvation case each have a test.
- **Siblings:** `registryClient` is the only registry HTTP client, so pulls, resolves, pushes and `Login` all get
  the bound.
- **Reuse:** the change uses oras-go's own `retry.Transport.Policy` hook and delegates to `retry.DefaultPolicy` for
  everything except a timeout; it keeps `httpx.Transport()`. No existing stalling-registry helper exists under
  `internal/testkit` or the test harnesses; the new `stallregistry` package is shared by both tests rather than
  copied.
- **Conventions:** top-level imports, short why-comments that cite #697, the one `//nolint:forbidigo` is justified
  on its line, no YAML touched. `stallregistry.Start` documents that it mutates `http.DefaultTransport` and
  `DOCKER_CONFIG` and must not run beside another test; both callers are non-parallel top-level tests (the unit
  subtests run in parallel with each other only, after `Start`).
- **Scope:** four files, every hunk serves #697, no test weakened.
- **ADRs:** none contradicted or edited (ADR-0031 and ADR-0035 set no deadline; ADR-0143 accepts only a bounded
  block of the worker).
- **Checks on the touched packages** (`internal/artifact`, `internal/testkit/stallregistry`, `tests/chaos`): vet
  passes, golangci-lint reports 0 issues. The worktree is clean.
- **Shape:** `fix(artifact):` subjects, `Fixes #697` in aa7acb4c, the attribution trailer on both commits.

### Definition of Done
11 / 11 applicable items hold (item 8's Linux lint, e2e and lane runs belong to the group gate and are not counted
here).

### Model scorecard
Not recorded by this gate (the ledger row is returned to the orchestrator): claude-opus-5-5 on issue #697 (fix) →
pass, 0/0/1, 0 model-attributed, DoD 11/11.

### Recommendation
Pass. Hand back to `/fix` Step 8 for integration; the group gate runs the repo-wide checks.
