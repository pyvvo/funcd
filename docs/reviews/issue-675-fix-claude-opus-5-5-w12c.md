# Fix review — issue #675 (built-in Cedar deny named by position)

- **Change**: branch `fix/w12c-i675`, commit 1b65d555 `fix(auth): name built-in Cedar policies by a stable id in a deny reason`
- **Model**: claude-opus-5-5
- **Decision applied**: give each built-in policy a stable readable id (named ids or an `@id` annotation) so the deny reason names it; the deny decision stays unchanged.
- **Verdict**: **pass**. Blockers 0, Majors 0, Minors 0. Fix checklist 11 of 11.

## Blockers

None.

## Majors

None.

## Minors

None.

## Verified correct

- **The cause is fixed.** `compile` built the built-in set with `cedar.NewPolicySetFromBytes`, which assigns ids by position (`policyN`). The new `compileBuiltins` (`internal/auth/cedar/policies.go`) parses the built-ins with `NewPolicyListFromBytes` and adds each one as `builtin/<@id>`. Every built-in `.cedar` file now carries a unique `@id`, for 8 policies in total. `denyReason` (`cedar.go`) is unchanged, so the reason now reads `cedar: forbidden by policy builtin/kv-single-writer`. The fix follows the decision: it uses `@id` annotations, and the allow or deny outcome does not change.
- **The fix is safe against silent loss.** A built-in that has no `@id`, or that repeats an `@id`, is refused with `fault.Internal`. Without that check, `PolicySet.Add` would replace the earlier policy without an error. The `builtin/` prefix cannot collide with a user Policy id, because a user Policy id has the form `<namespace>/<name>#<n>`.
- **Revert check.** I overlaid the `origin/main` versions of `policies.go` and `capability.go` and kept the test. `TestIssue675_BuiltinDenyNamesStableID` fails for the reason in the issue: the expected reason is `…builtin/kv-single-writer`, and the actual reason is `cedar: forbidden by policy policy2`.
- **The test passes with the fix.** The test passed under `-race` with `-count=5`. The full `internal/auth/cedar` package passed under `-race`. The dependent package `internal/catalog/gateway`, which builds its PDP from `reg.Builtins()`, also passed under `-race`.
- **The test checks that the id is stable by position.** The test puts an extra built-in before the defaults, and the reason stays `builtin/kv-single-writer`.
- **Mutants.** Each of the 4 overlay mutants on `policies.go` fails `TestIssue675_BuiltinDenyNamesStableID`:
  1. A positional id (`"builtin/"+i`) replaces the `@id` name.
  2. The missing-`@id` check is disabled.
  3. The `Add` result check is inverted.
  4. The duplicate-`@id` check is disabled. The failure message is "a duplicate built-in @id must not silently replace a policy".
- **Scope.** Every hunk serves the issue. The hunks are the `@id` lines, `compileBuiltins`, a doc line on `Capability.Builtin` that states the new `@id` requirement, and the test. No test was weakened or deleted.
- **Reuse.** The change uses the cedar-go API that is already in use: `NewPolicyListFromBytes` (also in `references.go`, `schema.go` and the user-Policy path), `Policy.Annotations()` and `PolicySet.Add`. It uses the existing `fault` helpers. It adds no new helper, harness or dependency. The test reuses `twoNamespaces`, `mutableSource`, `decide`, `fnRef` and `itemsRef`.
- **Conventions.** Errors use `api/fault`. The comments are short and explain why. Imports stay at the top level. No YAML was touched. No Accepted or Implemented ADR file was edited. The change contradicts no ADR, because it changes only the reason text and the policy ids, not the decision.
- **Checks on the touched package.** `go vet` passed. `golangci-lint` reported 0 issues.
- **Shape.** The commit has a `fix(auth):` subject, a body that explains the cause, a `Test:` line, `Fixes #675` and the attribution trailer. The commit fixes one issue. The worktree was left clean.

## Recommendation

Pass. The fix can be integrated. The repository-wide gate, including Linux lint and e2e, runs once per PR at the group gate.
