## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #461 fix, model: claude-opus-5-5)

Change: branch `fix/i461`, commit ef693c3 `fix(kv): clamp a stored KVStore's caps to the key and value limits`
(`api/types/v1alpha1/kvstore.go`, `internal/services/kv/kv_test.go`; +45/-4).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** A plain `git revert --no-commit ef693c3`
  also removes the test, because the fix and the test are in one commit. That run reported `[no tests to run]`, so
  it proves nothing. The check was therefore run as Step 2.1 describes: an overlay of the `origin/main`
  `api/types/v1alpha1/kvstore.go`, with the fixed test kept. Result:
  `--- FAIL: TestIssue461_StoredOverLimitCapsAreClamped` — `expected: "invalid"`, `actual: "internal"`. That is the
  issue's symptom: a 70000-byte key passes the facade and fails in Badger with an Internal error. The worktree was
  reset to ef693c3 and is clean.
- **It passes with the fix under `-race`.** `go test -race -count=1 -run TestIssue461 ./internal/services/kv/` →
  `--- PASS` (0.08s). The full packages `./internal/services/kv/` and `./api/types/v1alpha1/` → `ok`.
- **The root cause is fixed, not masked.** The issue gives the cause as `EffectiveMaxKeyBytes` and
  `EffectiveMaxValueBytes`, which apply the default but never clamp, combined with `Validate`, which runs only on
  writes. Both methods now return `min(spec, limit)`. The only production reader of these caps is
  `internal/services/kv/resolver.go:143-144`. Nothing outside `kvstore.go` reads the raw `Spec.MaxKeyBytes` or
  `Spec.MaxValueBytes`, so every path gets the clamped cap. The fix sits at the single point that turns a
  stored spec into an effective cap. It is not a check added downstream.
- **Mutants on the key lines fail a test.** Each overlay mutant was run against `TestIssue461|TestIssue377|KVStore`:
  - M1 removes the key clamp: FAIL (`invalid` expected, got `internal`).
  - M2 removes the value clamp: FAIL (`invalid` expected, got no error). The over-limit value passes the facade.
  - M3 clamps the key to `MaxKeyBytesLimit-1`: FAIL in both `TestIssue461_…` (the key at the limit is rejected) and
    `TestIssue377_DeclaredKeyCapIsStorable`.
- **Scope.** The change has two hunks: the two clamps, with their doc comments, and the regression test. No test
  was weakened or deleted.
- **Reuse.** The fix uses the existing `MaxKeyBytesLimit` and `MaxValueBytesLimit` constants and the built-in `min`.
  It adds no new limit and no new helper. The test adds one small fake, `rawMeta`, next to the existing
  `metaReader` in `resolver_test.go`. `metaReader` cannot replace it: `metaReader` wraps `store.Store`, which
  runs `Validate` on `Create`, so it cannot hold the over-limit KVStore that the issue is about. The fake is
  justified, and its doc comment explains why it exists. The test reuses the package's `mkKVStore`,
  `mkFunctionWithKV` and `allowAll` helpers and the real `kvbadger` engine.
- **Conventions.** Errors are checked with `fault.KindOf` against `fault.Invalid` and `fault.Internal`
  (ADR-0002). Imports are at the top level, and the doc comments are short and explain why. The test uses
  `t.Parallel()`, and Badger gets `t.TempDir()`, which is allowed here because the test does not call
  `funcd.New`. There is no YAML in the change.
- **ADRs.** ADR-0073 says that per-op caps (`maxValueBytes`/`maxKeyBytes`) → `Invalid`, and the fix restores that
  for stored legacy specs. No ADR file was edited.
- **Checks (the touched packages).** `go test -race` → `ok` for both packages. `go vet` → clean.
  `golangci-lint run` → `0 issues.` The repo-wide checks, the Linux lint and the e2e suite are left to the group
  gate.
- **Shape.** The subject is `fix(kv): …`. The body explains the cause, the fix and the test, and contains
  `Fixes #461` and the attribution trailer. The commit fixes one issue.

### Recommendation

Pass. Hand the change back to `/fix` Step 8.
