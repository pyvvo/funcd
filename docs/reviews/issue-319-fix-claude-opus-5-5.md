## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #319 fix, model: claude-opus-5-5)

Change: branch `fix/i319`, commit b6c500c `fix(contract): reject type lists outside the nullable form`
(`internal/contract/contract.go`, `internal/contract/schema.go`, `internal/contract/contract_test.go`;
57 insertions, 4 deletions).

The issue: `contract.Check` switched only on `primaryType()`, the first non-"null" entry of a `type` list, so
`{"type":["string","foo"]}` (an unknown name) and `{"type":["string","integer"]}` (an untagged union) passed
the push-time gate. ADR-0058's profile table allows a list only in the nullable form, `{"type":[…,"null"]}`.

The fix: a new `typeField.inProfile()` checks the whole `type` keyword. Every name must be a profile type
(`object`, `array`, `string`, `integer`, `number`, `boolean`, `null`), and a list must have one entry or
exactly two entries with one "null". `walk` runs this check right after the empty-schema case, before
`$ref`, `oneOf` and `enum`. The old `default:` branch of the type switch ("unsupported scalar type") is
removed, because `inProfile` now rejects an unknown single name too.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** With `git revert --no-commit b6c500c`
  and the new test kept, `go test -race -run TestIssue319 ./internal/contract/` fails in all six reject
  subtests with `contract … must be rejected, got nil`. That includes the issue's two examples
  (`["string","foo"]`, `["string","integer"]`).
- **It passes with the fix under `-race`.** After `git reset --hard b6c500c`, `go test -race -count=1
  ./internal/contract/` gives `ok`. The worktree was left clean at b6c500c.
- **The test is thorough.** It rejects an unknown name after a valid one, an untagged union, a union plus
  null, a duplicate type, an unknown name beside an `enum`, and a union nested in an object field. Each
  must be a `fault.Invalid`. It also keeps five accepted forms in profile: nullable in both orders, a
  one-entry list, `["null"]`, and a nullable object with properties.
- **Mutants: 3 of 3 killed.**
  - M1: drop `nulls == 1` (any two-entry list passes) fails the `duplicate type` and `untagged union` cases.
  - M2: accept an extra name `foo` in the profile set fails the unknown-name cases.
  - M3: move the `inProfile` case after the `enum` case fails `unknown name beside an enum`.
- **The root cause is fixed, not masked.** The cause named in the issue (only the first non-"null" entry is
  inspected) is gone: the whole list is validated before any branch dispatches on `primaryType()`. Nothing
  is retried, skipped or loosened.
- **The check is placed before `enum` and `oneOf`.** Those cases return early, so an earlier type check is
  the only way they cannot bypass it. M3 proves the ordering is load-bearing and tested.
- **Scope.** Every hunk serves the issue. No existing test was changed or removed; the whole package passes.
- **Reuse.** The fix extends the existing `typeField` type and the existing `unsupported` helper. The only
  other type-name switch in the repo, `pkg/sdk/types_gen.go`, maps types for code generation; it is not a
  profile gate, so there is no duplicated logic.
- **Conventions.** Errors stay `fault.Invalid` through the existing `walk` → `Check` wrap (ADR-0002). The
  `strings` import is at the top level. Comments are short and state the why (ADR-0058).
- **ADRs.** The fix implements ADR-0058's profile table (nullable row; non-discriminated unions excluded).
  It contradicts no Accepted or Implemented ADR, and no ADR file is touched.
- **Checks.** `go test -race ./internal/contract/` ok, `go vet ./internal/contract/` clean,
  `golangci-lint run ./internal/contract/...` reports 0 issues. The repo-wide set, Linux lint and e2e are
  left to the group gate.
- **Shape.** Subject `fix(contract): …`, body has `Fixes #319` and the attribution trailer, one issue in one
  commit.

### Recommendation

Pass. Hand back to `/fix` Step 8.

Note (not a finding): the hint for a single unknown name such as `{"type":"foo"}` is now the nullable-form
hint instead of "unsupported scalar type". The error is still `fault.Invalid` and names the offending type.
