## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #166 fix, re-review round 2, model: claude-opus-5-5)

Change: branch `fix/i166`, commits 19ec775 `fix(controlplane): serve the wire shape the OpenAPI spec describes`
and 3c371b4 `fix(controlplane): address review of #166` (`git diff origin/main...HEAD`: 10 files,
+1067/−427, most of it the regenerated spec).

### Previous findings (round 1)
- **Major 1 (404/405/422 facets left broken) — resolved.** 3c371b4 installs chi `NotFound` and
  `MethodNotAllowed` handlers that render through huma as problem+json. The 405 sets one `Allow` header
  with every method that the router matches on the path. `huma.NewError` is overridden, as ADR-0005 §4
  requires, so huma's own 422 renders the `fault.Problem` members. `TestIssue166_ErrorsShareOneProblemShape`
  asserts the content type and the member set `type/title/status/detail` for the handler 404, the router 404,
  the 405 and the 422. It also asserts `Allow` = `["GET, PUT, DELETE"]` as one header value.
- **m1 (surviving mutant on the recursive `flatten`) — resolved.** `TestIssue166_NestedInlineEmbedsFlatten`
  nests two `,inline` levels. The same mutant (M4 below) is now killed.
- **m2 (orphaned `TypeMeta` component) — resolved.** `inlineRegistry.MarshalJSON` drops a schema that is
  only ever embedded `,inline` and that nothing references. The committed spec has no `TypeMeta` or
  `Status` component. `ObjectRef` is kept because something still references it.
  `TestIssue166_SpecHasNoDeadOrDanglingSchemas` checks every `$ref` in the served document.

### Minor
- **m1 — router and huma errors use the `about:blank` type, not the `api/fault` type for the same status**
  · attribution: model · evidence: `newFaultError` in `internal/controlplane/controlplane.go` builds
  `Type: "about:blank"` and `Title: http.StatusText(status)` for every huma-raised error. So an unknown
  route returns 404 with `type: about:blank`, while a missing object returns 404 with
  `type: urn:funcd:problem:not-found` (`api/fault/problem.go`). That file calls itself "the single site
  where HTTP status codes are decided". The members are the same, and RFC 9457 permits `about:blank`.
  The SDK keys on `status`, so no client breaks today. But a client that switches on `type` sees two
  types for one condition. The router's 404 could be a `fault.NotFoundf` that goes through `wrapFaultError`.
  That leaves `about:blank` only for the statuses that have no fault Kind, such as 405 and 422.

### ✅ Verified correct (keep it)
- **Regression tests fail without the fix, for the issue's reason**: `git revert --no-commit 3c371b4 19ec775`,
  with `server_test.go` restored from HEAD and the unit test file of the deleted helper removed →
  `--- FAIL: TestIssue166_WireShapeMatchesSpec` (`"422" is not less than "300"`, body
  `expected required property TypeMeta to be present`), `--- FAIL: TestIssue166_ErrorsShareOneProblemShape`
  (handler fault content type is not `application/problem+json`), and
  `--- FAIL: TestIssue166_SpecHasNoDeadOrDanglingSchemas`. Then `git reset --hard 3c371b4`: the worktree is clean.
- **Passes with the fix under `-race`**: all four `TestIssue166_…` tests report `--- PASS`, and none is skipped.
- **Root cause, not symptom**: the schema source is fixed. The inline registry promotes `,inline`
  fields as encoding/json does, and the `json:",inline"` tags of ADR-0003 stay. The error rendering is fixed
  at huma's documented override point, `huma.NewError`, which ADR-0005 §4 names. A `sync.Once` guards the
  process-global hook, and only `internal/controlplane` imports huma's runtime. The router's 404 and 405 now
  go through `huma.WriteErr`. chi exposes no accessor for its allowed methods, so `allowedMethods`
  (which calls `chi.Routes.Match`) is the narrowest correct fit. It does not duplicate any code.
- **Mutants (all killed, worktree restored after each)**: M1 `Allow` = first method only →
  `TestIssue166_ErrorsShareOneProblemShape` fails. M2 `installFaultErrors` no-op → the same test fails.
  M3 no pruning in `MarshalJSON` → `TestIssue166_NestedInlineEmbedsFlatten` and
  `TestIssue166_SpecHasNoDeadOrDanglingSchemas` fail. M4 (round 1's survivor) recursive `r.flatten(ref)`
  removed → `TestIssue166_NestedInlineEmbedsFlatten` fails.
- **Spec in sync**: `go run ./internal/controlplane/cmd/specgen/ -out <scratch>` → `cmp` against
  `api/openapi/funcd.v1alpha1.yaml` shows no difference. The error responses now reference `FaultError`,
  which requires `type/title/status/detail`, instead of huma's `ErrorModel`. This matches the bodies the
  server sends. Nothing outside the ADRs and the reviews refers to `ErrorModel`.
- **Scope**: every hunk serves one of the issue's facets. No test was weakened or deleted. The SDK comment
  change only drops the outdated statement about the two content types.
- **Conventions**: all imports are at the top level. The new helpers use `interface{}` only in test helpers
  (`keysOf`, `refsOf`), as the existing tests do. The doc comments are short and explain why. The only
  global is the `sync.Once`, and it carries a justified `nolint`.
- **ADRs**: no file under `docs/adr/` changed. The change follows ADR-0005 §4 (the `huma.NewError` override,
  problem+json) and ADR-0003 (the `,inline` tags are unchanged).
- **Checks (touched packages)**: `gofmt -l` is clean. `go vet` on `internal/controlplane/...`, `pkg/sdk/...`
  and `api/openapi/...` passes. `golangci-lint` reports `0 issues.` `go test -race -count=1` passes for
  `internal/controlplane`, `internal/controlplane/admission`, `pkg/sdk`, `api/openapi` and `cmd/funcdctl`.
  The e2e suite, the Linux lint and the lanes are left to the group gate.
- **Shape**: 19ec775 is `fix(controlplane): …` with `Fixes #166` and the attribution trailer. 3c371b4 is
  `fix(controlplane): …` with `Refs #166` and the trailer. Both commits cover the one issue.

### Definition of Done
11 / 11 hold. Item 8 was checked on the host for the touched packages only. The group gate runs the
Linux lint, the e2e suite and the lanes.

### Model scorecard
For the ledger: claude-opus-5-5 on issue #166 (fix, round 2) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Pass. Hand back to `/fix` for the PR. Optionally, route the router's 404 through `fault.NotFoundf` so that
each status that has a fault Kind has one problem type (m1).
