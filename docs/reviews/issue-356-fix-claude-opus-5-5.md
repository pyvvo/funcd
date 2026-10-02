# Fix review — issue #356 (NUL byte in a Secret or ConfigMap value passes the env gate)

- **Change**: branch `fix/i356`, commit 2a28b66 `fix(envresolve): reject a NUL byte in a ConfigMap or Secret value`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Counts**: 0 Blocker · 0 Major · 1 Minor (1 model-attributed)
- **Fix checklist**: 11 / 11

## Verification run

| Check | Result |
|---|---|
| Revert 2a28b66 (keep the test), run `TestIssue356_NULValueRejected` | FAIL: `An error is expected but got nil` — `got env map["CFG_NUL":"a\x00b"]`; the NUL value flows into the env map, the issue's reason |
| Reset to 2a28b66, `go test -race` for `internal/envresolve` and `internal/secrets` | ok, ok |
| Mutant 1: drop the NUL case in `secrets.EnvValueProblem` | `TestIssue356_NULValueRejected` FAIL |
| Mutant 2: disable the ConfigMap value loop in `envresolve.ResolveEnv` | `TestIssue356_NULValueRejected` FAIL |
| Mutant 3: disable the UTF-8 case in `EnvValueProblem` | `TestIssue168_NonUTF8ValueRejected` FAIL (the #168 guard survives the refactor) |
| Mutant 4: disable the Secret value check in `secrets.ResolveEnv` | `TestIssue356_NULValueRejected` FAIL on the Secret half (`got env map["SEC_NUL":"a\x00b"]`) |
| `go vet` on the touched packages | clean |
| `golangci-lint` on the touched packages | 0 issues |
| Worktree after review | clean, at 2a28b66 |

Not run here (the group gate runs them): the repo-wide tests, Linux lint, the e2e suite, the lanes. The issue's
end-to-end steps (a process-driver start) were not rerun; the unit test asserts the gate at the single
resolver both Functions and providers use (`internal/function/secrets.go`, `internal/provider/env.go` →
`envresolve.ResolveEnv`), which is upstream of any worker creation.

## Blockers

None.

## Majors

None.

## Minors

1. **Unwrapped doc-comment line** (`internal/envresolve/envresolve.go`, the `ResolveEnv` doc comment) —
   attribution **model**. The edited sentence "…nil (unchanged catalog semantics), or if a value cannot be
   delivered as env (EnvValueProblem). On failure the returned error is joined with ErrConfig or" runs to
   roughly 170 characters, while the rest of the comment wraps near 95. Cosmetic; rewrap it.

## ✅ Verified correct

- **Cause, not symptom.** The issue names two gaps: `secrets.ResolveEnv` checked only `utf8.Valid`, and
  `envresolve.ResolveEnv` copied ConfigMap values unchecked. Both are closed, before any worker spec exists;
  nothing is retried, swallowed or timed out.
- **Reuse, no duplication.** The existing UTF-8 check moved into one helper, `secrets.EnvValueProblem`, in
  `internal/secrets/guard.go` beside the existing `IsReservedKey`/`MergeEnvGuarded` env guards, and both
  sides call it. A search of `internal` and `api` found no other env-value check to reuse. Standard library
  only (`unicode/utf8`, `strings.IndexByte`).
- **Error attribution.** A ConfigMap failure is `fault.Invalid` joined with `ErrConfig`; a Secret failure keeps
  `fault.Invalid` and is joined with `ErrSecret` by the existing wrapping (ADR-0093 §4). So the Function reports
  `ConfigResolveFailed` or `SecretResolveFailed`, as the issue expects. The test asserts the kind, the sentinel,
  the object name and the key, and asserts that the value is absent from the message.
- **Layering.** `envresolve` still imports only `internal/secrets` and `internal/store`; the test's new imports
  (`internal/auth/rbac`, `internal/secrets`) are test-only and top-level.
- **Scope.** Every hunk serves the issue. ConfigMap values are now also checked for valid UTF-8, a side effect
  of using the one shared check; this is the same env-delivery constraint and is consistent with #168. No test
  was weakened or deleted.
- **ADRs.** No ADR file was touched; the change conforms to ADR-0057, ADR-0092 (reserved-key guard unchanged)
  and ADR-0093.
- **Shape.** `fix(envresolve):` subject, a Cause/Fix/Test body, `Fixes #356`, the attribution trailer, one
  issue in one commit.

## Recommendation

Pass. Rewrap the one long doc-comment line when convenient (it is not a gate). Hand back to `/fix` Step 8.
