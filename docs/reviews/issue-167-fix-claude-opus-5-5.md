## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #167 fix, model: claude-opus-5-5)

Change: branch `fix/i167`, commit e45a88e `fix(api): reject ConfigMap and Secret data keys that are not env-var names`
(`api/types/v1alpha1/configmap.go`, `api/types/v1alpha1/secret.go`, `api/types/v1alpha1/validate_test.go`).

Issue #167: `ConfigMap.Validate` checked only the metadata and `Secret.Validate` rejected only empty keys, so
keys such as `1BAD`, `BAD-DASH`, `HAS SPACE` and `CHAOS_A=B` were admitted and reached the worker env, where
`CHAOS_A=B` became the variable `CHAOS_A` and shadowed a declared `CHAOS_A` key nondeterministically.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit e45a88e`, with
  `validate_test.go` restored from e45a88e so the test exists, then
  `go test -race -count=1 -run TestIssue167 ./api/types/v1alpha1/` → `FAIL`. Seven subtests fail
  (`""`, `1BAD`, `BAD-DASH`, `HAS SPACE`, `CHAOS_A=B`, `dotted.key`, `NEW\nLINE`), each with
  "key … must be rejected as fault.Invalid": the pre-fix code admits the keys the issue lists.
  A plain revert also removes the test (it is in the same commit), so the run reports "no tests to run";
  restoring only the test file makes the check meaningful.
- **Passes with the fix under `-race`.** After `git reset --hard e45a88e`:
  `go test -race -count=1 ./api/types/v1alpha1/` → `ok`; `-v -run TestIssue167` → 12 `--- PASS`
  (the parent and 11 subtests), none skipped. The worktree was left clean at e45a88e.
- **Mutants (each restored afterwards):**
  1. Drop the `$` anchor from the pattern → 5 subtests fail.
  2. Allow a leading digit (`^[A-Za-z0-9_]*$`) → 2 subtests fail (`""`, `1BAD`).
  3. `Secret.Validate` returns `nil` instead of calling `validateEnvKeys` → 7 subtests fail.
  The ConfigMap call site is covered by the revert run. No survivor.
- **Root cause fixed, not masked.** Both `Validate` methods now enforce the env-name pattern
  `^[A-Za-z_][A-Za-z0-9_]*$` that ADR-0048 lists for ConfigMap data keys as "deferred". `Validate` runs in the
  built-in Validating admission (`internal/controlplane/admission/validate.go`), so the bad keys are rejected
  at apply time with `fault.Invalid`. This is the issue's first expected behavior. A key is never reinterpreted.
  The rule is deterministic: keys are checked in sorted order, so the same object always reports the same key.
- **No breakage of existing keys.** The Secret keys that funcd writes itself (`accessKeyId`, `secretAccessKey`,
  `catalogToken` in `internal/services/identity`) and the e2e fixture keys (`APP_MODE`, `SHARED`, `hello`) all
  match the pattern. The test asserts the camel-case `accessKeyId` explicitly. The package suite is green.
- **Scope.** Every hunk serves the issue. The old empty-key loop in `secret.go` is replaced, not weakened
  (the pattern rejects `""`), and the now-unused `fault` import is removed. No test was weakened or deleted.
- **Reuse, no duplication.** One generic helper, `validateEnvKeys[V string | []byte]`, serves both kinds
  instead of two copied loops. It reuses `fault.Invalidf` and the stdlib `maps`/`slices`/`regexp`, which
  `ids.go` (`dnsLabel`) also uses in this package. The only similar regex, `envRef` in `cmd/funcdctl/dev.go`,
  is a `${VAR}` substitution matcher in a different layer, not a duplicate of an admission rule.
- **Conventions.** Ops are named `ConfigMap.Validate`/`Secret.Validate` like the rest of the package.
  Imports are at the top level, there is no `any` in signatures, errors use `api/fault`, and doc comments are
  short and name the ADRs. The test follows the package's table style. `gofmt -l` is clean, `go vet` exits 0,
  and `golangci-lint run ./api/types/v1alpha1/` reports `0 issues`.
- **ADRs.** No ADR file is touched. The change implements the rule that ADR-0048 (Implemented) deferred. It
  does not contradict ADR-0048, ADR-0057 or ADR-0093. No JSON Schema change is needed, because the rule lives
  in `Validate`, as the existing Secret comment already states.
- **Commit shape.** The subject is `fix(api): …`. The body states the cause and names the regression test,
  and it carries `Fixes #167` and the attribution trailer. There is one issue per commit.

Not run here, per the gate's scope (the group gate runs them): Linux lint, the repo-wide test suite, e2e, and
the Lima lanes.

### Recommendation

Pass. Hand back to `/fix` Step 8.
