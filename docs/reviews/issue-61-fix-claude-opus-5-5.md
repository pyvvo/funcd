# Fix review — issue #61 (claude-opus-5-5)

- **Issue**: #61 — Create ignores the path namespace and stores the object in the body's namespace
- **Change**: branch `fix/i61`, commit `6dd5683` `fix(controlplane): reject a create whose body namespace differs from the path`
- **Producing model**: claude-opus-5-5
- **Governing ADRs**: ADR-0018 (Decision §4 step 2, scenario admission-rejects-invalid), ADR-0005 (Handlers `CreateX` signature), ADR-0002
- **Verdict**: **pass**

## Summary

Every namespaced create input now binds the `{namespace}` path segment, so huma validates it as it does on
GET/PUT/DELETE, and each create route runs the path/body consistency check before `CreateX`. The check was
extracted from `replaceObj` as `matchPathNamespace`, so Replace and Create share one implementation. The
OpenAPI spec is regenerated and matches `specgen` output. The regression test fails without the fix for the
issue's reason and passes with it under `-race`. Two Minor findings, both about breadth of coverage, none
blocking.

## Verification run

| Check | Result |
|---|---|
| Revert `6dd5683` (keep the HEAD test), run `TestIssue61_…` | FAIL: `expected: 400 actual: 200` on "function body namespace differs" — the issue's reported behavior |
| HEAD, `go test -race -run 'TestIssue61\|TestScenarioAdmission'` | ok |
| HEAD, `go test -race ./internal/controlplane/` | ok |
| `go vet ./internal/controlplane/...` | clean |
| `golangci-lint run ./internal/controlplane/...` (host) | 0 issues |
| `gofmt -l internal/controlplane` | clean |
| `specgen` to a scratch file vs `api/openapi/funcd.v1alpha1.yaml` | identical |
| Coverage of namespaced creates | 22 namespaced create routes, 22 `matchPathNamespace` calls; the 4 unchecked creates (Namespace, RuntimeClass, WorkerNode, Gateway) are cluster-scoped |
| Worktree after review | at `6dd5683`, clean |

Not run here (group gate): e2e, repo-wide tests, Linux lint, Lima lanes.

### Mutants

| # | Mutant | Result |
|---|---|---|
| M1 | `matchPathNamespace` condition forced false | killed — `TestIssue61_…` and `TestScenarioAdmissionRejectsInvalid` fail |
| M2 | drop the check from the ConfigMap create route | killed — `TestIssue61_…` fails |
| M3 | drop the check from the Secret create route | **survived** — the full package passes (see Minor 1) |

## Blockers

None.

## Majors

None.

## Minors

1. **The regression test covers 2 of 22 hand-wired create routes** (`model`). The guard is copied into every
   create handler in `internal/controlplane/routes.go` and `routes_rest.go`, but `TestIssue61_…` exercises
   only `functions` and `configmaps`. Evidence: mutant M3 (removing the guard from `createSecret`) passes the
   whole package. A later kind added by copy-paste without the guard would regress silently. A table over
   all namespaced collection paths (one mismatched-namespace POST each) would close it.
2. **The same three-line guard is repeated 22 times** (`model`, reuse). The consistency check itself is
   correctly shared (`matchPathNamespace`, reused by `replaceObj`), but the call site is duplicated per
   route because ADR-0005's `CreateX` carries no namespace. This is consistent with how the surrounding
   routes are written (each route already repeats its `wrapFaultError` block), so it is cosmetic; it is the
   root of Minor 1's gap. No change is required for this fix.

## Verified correct

- **Root cause fixed, not masked**: the issue names the missing path field on the create inputs and the
  absent comparison in `createObj`; both are addressed. The four symptoms map cleanly: (a)/(b) mismatch → 400,
  nothing stored (the test lists both namespaces' collections empty); (c) invalid path → 422 at
  `path.namespace`, as on GET; (d) omitted body namespace → 400 "does not match", not a misleading 403.
- **Reuse**: the check is extracted from `replaceObj` rather than reimplemented; the error code
  (`controlplane.admit`, `fault.Invalidf`) and message are unchanged, so the existing Replace scenario test
  still asserts the same text.
- **ADR conformance**: matches ADR-0018 Decision §4 step 2 (path/body consistency → `fault.Invalid` → 400)
  and its admission-rejects-invalid scenario; ADR-0005's `CreateX` signature is untouched; no ADR file edited.
- **Scope**: every hunk serves the issue (input structs, route guards, the extracted helper, the regenerated
  spec, one test). No test weakened or deleted.
- **Conventions**: `api/fault` error, typed `v1.NamespaceName`, block-style YAML in the generated spec, no
  comment bloat; the test follows the package's existing idiom (`functionBody`, `do`, `fnBase`, nested
  `TypeMeta` map as in `api_test.go`).
- **Shape**: `fix(controlplane):` subject, `Fixes #61`, attribution trailer, one issue in one commit.

## Fix checklist

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue61_…` reproduces the issue | yes |
| 2 | Fails on pre-fix code for the reported reason | yes |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Reverting/mutating the key lines fails a test | yes (M1, M2 killed; M3 is Minor 1) |
| 5 | Root cause fixed | yes |
| 6 | Scope only; no test weakened | yes |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build, vet, lint, tests green (touched package) | yes |
| 9 | Conventions hold | yes |
| 10 | Reuses what exists | yes (Minor 2 noted) |
| 11 | Commit shape | yes |

11 of 11.

## Recommendation

Pass. Optionally widen `TestIssue61_…` to a table over all 22 namespaced collection paths so a route that
loses or never gets the guard is caught.
