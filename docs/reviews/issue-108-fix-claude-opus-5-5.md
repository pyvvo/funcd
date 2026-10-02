## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #108 fix, model: claude-opus-5-5)

Change under review: branch `fix/i108`, commit `4abd4db fix(edge): decode a static asset's URL path only once`
(`git diff origin/main...HEAD`: `internal/edge/static/static.go`, `internal/edge/static/static_test.go`, new
`internal/dataplane/static_test.go`).

### 🔴 Blocker

None.

### 🟠 Major

None.

### 🟡 Minor

None.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 4abd4db` with the
  new test file restored from HEAD: `--- FAIL: TestIssue108_StaticPathDecodedOnce` with `expected: 200 /
  actual: 404`, `GET /100%25.html: … "detail":"edge.static: no such object"`. This is the issue's reported
  404. After `git reset --hard 4abd4db` the worktree is clean at that HEAD.
- **Passes with the fix**: `go test -race -count=1 ./internal/dataplane/ ./internal/edge/static/` → both `ok`.
- **The test drives the real path.** It goes through `dataplane.Handler` with a real `router` and a `mem://`
  bucket, so the remainder is cut from `r.URL.Path` exactly as in production (`dataplane.go:227`). It covers
  every facet the issue names: `/100%25.html` → `100%.html`, `/a%2520b.txt` → `a%20b.txt` (no longer
  `a b.txt`), `/a%20b.txt` → `a b.txt`, the index, and three encoded traversals (`..%2f`, `%2e%2e`,
  `%252e%252e`) that must 404 without leaking the out-of-prefix object.
- **Root cause removed, not masked**: the second `url.PathUnescape` in `resolveKey` is gone and the
  `net/url` import with it. The path is now decoded once, by net/http. That matches ADR-0120 Decision §2
  ("decodes the remainder", once) and its Contracts note that `Serve` cleans the decoded remainder. The
  traversal defense (`..` rejection before `path.Clean`, rooted clean, prefix assertion before any Get) is
  unchanged.
- **Mutants** (on the fix's lines):
  - M1: `dataplane.go:227` passes `r.URL.EscapedPath()` instead of `r.URL.Path`. The regression test fails.
  - M3: a `url.PathUnescape` re-added at the top of `resolveKey`, with the unit harness kept. The regression
    test fails.
  - Both mutants were restored; `git status` was clean afterwards.
- **Scope**: every hunk serves the issue. The unit-test `serve` helper now passes `r.URL.Path`, which is what
  the data-plane passes, in place of the raw remainder. No test was weakened: `TestScenarioTraversalRejected`
  still sends `/..%2f..%2fsecret`, which `httptest.NewRequest` decodes to `/../../secret`, and it still
  rejects that path and checks that the secret does not leak. Without this helper change, the unit suite
  would have tested a contract that production never uses.
- **Reuse, no duplication (Step 2.7)**: the fix only deletes code. The new test adds no helper. It reuses the
  package's existing `fakeEndpoints` and `noScaler` (`dataplane_test.go`), the `gocloud` `mem://` driver,
  `router.New` and `static.New`.
- **Conventions (Step 2.8)**: imports are at top level and the comments are short why-comments that cite
  ADR-0120. The signature is unchanged except for the renamed parameter. `go vet`, `gofmt -l` and
  `golangci-lint run` on the touched packages are clean (`0 issues.`).
- **ADRs**: no ADR file was touched, and ADR-0120 §2 is now honored, not contradicted.
- **Shape**: the subject is `fix(edge): …`, the body carries `Fixes #108`, the Co-Authored-By trailer is
  present, and the change is one issue in one commit.

Observation, not a finding: a mutant that drops the `containsDotDot` rejection in `resolveKey` survives both
suites. A traversal still returns 404, because the rooted `path.Clean` absorbs `..`, so the separate check
is defense in depth whose "stated rejection" no test pins. That line is older than this fix and the fix
leaves it unchanged, so it is outside this issue's scope.

### Definition of Done

11 / 11 items hold (the fix checklist). Item 8 was checked for the touched packages only: tests with -race,
vet, lint and gofmt. The group gate runs Linux lint, the e2e suite (including the issue's site e2e probe)
and the lanes.

### Model scorecard

claude-opus-5-5 · issue #108 · phase fix · pass · B0 / M0 / m0 · model-attributed 0 · DoD 11/11.

### Recommendation

Pass. The fix is minimal and removes the cause named in the issue. The regression test fails on the old
code for the reported reason, and both mutants on the fix's lines are caught.
