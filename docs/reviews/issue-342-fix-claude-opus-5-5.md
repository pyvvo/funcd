# Fix review — issue #342 (internal/secrets package comment says worker env injection is not built)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #342 fix, model: claude-opus-5-5)

Change: branch `fix/i342`, one commit `20b3126 fix(secrets): describe the built secret injection in the package comment`
(`internal/secrets/secrets.go` +4/−2, `internal/secrets/secrets_test.go` +13).

The old package comment said the env/tmpfs injection was "a P-M-successor's job". The new comment names the
function reconciler's secret gate (ADR-0057, `internal/function`), says it resolves the bound Secrets before
any worker is provisioned and merges them into the worker env, and says that V1 delivers env only (tmpfs is V2).
I checked each claim against the code and the ADR:

- `internal/function/function.go` step "3c. secret injection gate (ADR-0057 …)" calls `resolveBindingEnv`
  before provisioning, and fails closed with `SecretResolveFailed`.
- `secretEnv` is passed through `convergeSolo` → `convergeRevision` → `workerSpec`, so it reaches the worker env.
- `docs/adr/0057-secret-injection-last-mile.md` puts tmpfs out of scope ("V2 … V1 ships env").

### 🟡 Minor 1 — the comment names only the function reconciler as the injector  ·  attribution: model
`internal/provider/env.go` also resolves secrets through the same `SecretResolver` seam (re-exported from
`internal/envresolve`, ADR-0093) for the catalog/provider controller. "The injection is the function reconciler's
secret gate" is accurate for functions, but it leaves out that second consumer. The issue's "Done when" only asks
for the comment to point at the secret gate, so this does not block the fix. If the comment is edited again, a
short clause would make it complete, for example "(providers resolve the same way via internal/envresolve)".

### ✅ Verified correct (keep it)
- **The regression test fails on the pre-fix code, for the reported reason.** With the `origin/main` version of
  `secrets.go` and the new test in place, `TestIssue342_PackageDocDescribesTheBuiltInjection` fails with:
  the comment `should not contain "P-M-successor"`. (A plain `git revert` of the single commit also removes the
  test and gives "no tests to run", so I restored only the non-test file.)
- **It passes with the fix**: `go test -race -count=1 -run TestIssue342 ./internal/secrets/` → `--- PASS`.
- **Mutants: 3 of 3 killed.**
  (1) Removing `ADR-0057` from the reference → `does not contain "ADR-0057"`.
  (2) Removing "secret gate" → `does not contain "secret gate"`.
  (3) Putting back a "P-M-successor job" deferral → `should not contain "P-M-successor"`.
- **The root cause is fixed.** The stale sentence is gone, and the replacement is true, checked against the code
  and the ADR above.
- **Scope**: two hunks, both about the issue. No test was weakened or deleted.
- **Reuse**: the test uses the standard library (`go/parser`, `go/token`) and `testify` only. No repository
  helper already parses package doc comments, so nothing is duplicated.
- **Conventions**: imports are at the top of the file. The test follows the `TestIssue<N>_…` naming used in this
  file (`TestIssue168_…`). The comment is concise. No new types, errors or signatures.
- **ADRs**: ADR-0022 and ADR-0057 are both `Implemented`. Neither file was edited, and the comment agrees with
  both.
- **Checks (touched packages only)**: `go test -race -count=1 ./internal/secrets/...` → `ok` (secrets, aesgcm);
  `go vet` passes; `golangci-lint run ./internal/secrets/...` → `0 issues`. The repo-wide gate, Linux lint and
  e2e are left to the group gate.
- **Shape**: the subject is `fix(secrets): …`, the body has `Fixes #342` and the attribution trailer, and the
  commit covers one issue.
- **Worktree**: left at `20b3126` and clean.

### Definition of Done
11 / 11 applicable items hold. Item 8 (build, vet, lint, tests) was checked for the touched packages only; the
group gate runs the repo-wide set, Linux lint and e2e.

### Model scorecard
claude-opus-5-5 · issue #342 fix · pass · 0 blockers / 0 majors / 1 minor · model-attributed 1 · DoD 11/11.

### Recommendation
Pass. The fix can go to the group PR. The Minor is optional and can be addressed at the next edit of the comment.
