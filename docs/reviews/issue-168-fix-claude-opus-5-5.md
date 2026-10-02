## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #168 fix, model: claude-opus-5-5)

Change: branch `fix/i168`, commit a5554e8 `fix(secrets): reject Secret values that are not valid UTF-8 instead of corrupting them`.
Touched: `internal/secrets/secrets.go` (+6/-1), `internal/secrets/secrets_test.go` (+18).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The regression test sits at the resolver altitude only** · attribution: model.
  The commit message claims the Function "fails closed with SecretResolveFailed and no worker starts".
  `TestIssue168_NonUTF8ValueRejected` asserts only `ResolveEnv` returning `fault.Invalid`. The claim holds
  through the existing path: `internal/function/secrets.go` returns any `envresolve.ResolveEnv` error
  unchanged, and `TestScenarioMissingSecretFails` / `TestScenarioUnauthorizedSecretFailsMaterialization`
  (`internal/function/secrets_test.go`) already pin that any resolver error yields `SecretResolveFailed`.
  A Function-level case for the binary value would pin the user-visible outcome directly. Non-blocking.

### Observations (not scored)

- The check runs at resolve time, not in `Secret.Validate` (`api/types/v1alpha1/secret.go`). This is the
  right altitude: `spec.data` is `map[string][]byte` by contract, and ADR-0057 plans tmpfs delivery for
  file and binary secrets in V2, so rejecting binary data at admission would contradict the API type. The
  limitation belongs to env delivery, and the fix puts the check there.
- With several invalid keys, the key the error names depends on map iteration order. This is harmless,
  because any invalid key fails the resolution.
- The issue's in-process e2e probe was not committed and was not rerun. The unit test reproduces the
  root cause deterministically, and the caller path was checked by reading the code (above).

### ✅ Verified correct (keep it)

- **Fails without the fix.** `git revert --no-commit a5554e8` with the new test kept:
  `TestIssue168_NonUTF8ValueRejected` FAILs with `expected: "invalid"` / `actual: ""`. Before the fix,
  resolution succeeds and returns the bytes as a string, which is the issue's reason.
- **Passes with the fix**, run with `-race -count=1`: `--- PASS: TestIssue168_NonUTF8ValueRejected`, and the
  `internal/secrets` package reports `ok`. The worktree was reset to a5554e8 afterwards and is clean.
- **Mutants (3/3 killed):** `Invalidf`→`Internalf` (wrong fault kind); `return …`→`continue` (silently
  drops the key); naming `string(v)` in place of `k` (leaks the value into the error). Each one fails the
  test. The `NotContains` assertion is what catches the value-leak mutant.
- **Root cause.** The issue names `string(v)` without a check in `ResolveEnv`. The fix adds `utf8.Valid`
  there, before the conversion, which is the single point every driver (process and containerd OCI/JSON)
  receives env from. The fix does not mask the symptom.
- **Scope.** Two hunks, both for the issue: the check with its doc-comment sentence, and the test. No test
  was weakened or deleted, and no other file changed.
- **Reuse.** Uses the standard library's `unicode/utf8.Valid`. No helper was hand-rolled, and there was no
  existing UTF-8 check in the repo to reuse. The test reuses the package's existing `createSecret`, `dev`,
  `store.New(memory.New())` and `rbac.New()` harness.
- **Conventions (ADR-0002).** The error is `api/fault` (`fault.Invalidf(op, …)`) with the function's
  existing `op`. The import is at the top level. The comment is one sentence of *why*. The error names the
  secret and key and never the value, which matches the package's no-value-leak stance
  (`TestScenarioSecretValueNotPersisted`).
- **ADRs.** No `docs/adr/` file was touched. The fix conforms to ADR-0057 (env-only V1, fail closed) and
  ADR-0022.
- **Checks (touched package).** `gofmt -l internal/secrets` is clean; `go build ./...` is OK;
  `go test -race ./internal/secrets/ ./internal/envresolve/` is ok; `go vet ./internal/secrets/` is OK;
  `golangci-lint run ./internal/secrets/...` reports 0 issues. The repo-wide, Linux-lint, e2e and lane
  checks are left to the group gate.
- **Shape.** The subject is `fix(secrets): …`, the body has `Fixes #168` and the attribution trailer, and
  the commit covers one issue.

### Recommendation

Pass. Optionally, add a Function-level case to `internal/function/secrets_test.go` that sets a binary
Secret value and asserts `SecretResolveFailed` with no worker started.
