## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #157 fix, model: claude-opus-5-5)

Change: branch `fix/i157`, one commit `bc2457f fix(s3gateway): gate HeadBucket on the caller's binding and list bound Buckets`.
Touched: `internal/blob/s3gateway/{backend.go,s3gateway.go,harness_test.go,scenarios_test.go}`, `pkg/funcd/funcd.go`.
Governing contract: ADR-0080 (Implemented), *Buckets are `Bucket` resources* — "`HeadBucket` succeeds iff a
`Bucket` of that name exists in the caller's namespace and the caller is bound to it; `ListBuckets` returns the
`Bucket`s the caller is bound to".

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **Pre-existing `-race` flake in `TestScenarioOwnerWrites`** · attribution: `env` (pre-existing, outside the fix's scope)
  · evidence: the first full-package `go test -race ./internal/blob/s3gateway/` run on the fix failed with a data race
  between `fasthttp.(*Server).ShutdownWithContext` (reached from `s3gateway.(*Server).Close` in the test cleanup) and
  `fasthttp.(*RequestCtx).Done` (reached from `be.PutObject` → `gocloud.(*bucket).Put`). Neither frame is in the
  changed code. The same race reproduced on the pre-fix tree (`bc2457f~1`, `-count=8`, 1 race in
  `TestScenarioOwnerWrites`); later runs on the fix were green (2 of 3 single runs, then `-count=3` and
  `-count=8 -run TestScenarioOwnerWrites`). · fix: file a separate `kind/flaky-test` issue for the versitygw/fasthttp
  shutdown-vs-in-flight-request race in the s3gateway test harness; it does not block this fix.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit bc2457f` with the new
  test file restored → `TestIssue157_BucketOpsHonorBindings` FAILs at `scenarios_test.go:225`
  ("An error is expected but got nil … reporting has no spec.blob binding to lakehouse") — the unbound HeadBucket
  200 from the issue. Worktree then reset to `bc2457f`, clean.
- **Passes with the fix under `-race`**: `--- PASS: TestIssue157_BucketOpsHonorBindings`, un-skipped.
- **Mutants (3/3 killed)**, each restored after:
  1. `bound()` final `return false, nil` → `return true, nil` → fails `scenarios_test.go:225`.
  2. HeadBucket `if ok {` → `if ok || berr == nil {` (skip the binding) → fails `scenarios_test.go:225`.
  3. ListBuckets `if ok {` → `if ok || berr == nil {` (list unbound Buckets) → fails `scenarios_test.go:240`.
- **Root cause fixed, not masked.** HeadBucket now asks the PDP (`bound` = `s3::read` allowed on at least one of
  the Bucket's prefixes) instead of only checking existence; ListBuckets enumerates the namespace's Bucket
  resources through a new `Deps.Buckets` lister (wired from the metastore in `pkg/funcd`) and keeps the bound ones.
  Probing through the PDP rather than reading `spec.blob` directly means every read grant (binding, role
  assignment, external key) counts, consistent with the object PEP. A missing bucket and an unbound one both answer
  403, closing the existence oracle the issue describes; ADR-0080 fixes only the success condition, so 403 for both
  contradicts nothing. A Bucket with no prefixes is never "bound", which matches the binding model (a `spec.blob`
  binding must name an existing prefix — `internal/function/references.go`).
- **Scope**: every hunk serves the issue. The `authorize` refactor into `caller` + `allowed` is behavior-preserving
  (same deny/internal-error mapping, same NoSuchBucket after Allow) and lets the bucket ops reuse the PEP; all
  pre-existing s3gateway scenarios and the spine test pass unchanged. No test weakened or deleted.
- **Reuse**: no duplicated logic. The PEP code is shared, not copied; `s3Buckets` follows the store
  `List` + type-assert idiom used in `internal/function/references.go` and `internal/services/catalog/reconcile.go`
  (no typed-list helper exists in the repo to reuse instead); the test harness extends the existing `fakeMeta`.
- **Conventions**: ADR-0002 holds — ctx-first lister signature, `fault.Invalidf` for the new required dep, S3
  errors via `s3err`, slog only, no `any` in signatures; comments are short and say why. `gofmt -l` clean.
- **ADRs / docs**: no ADR file touched; no living doc described the old empty ListBuckets.
- **Checks (touched packages)**: `go build ./...` ok; `go vet ./internal/blob/s3gateway/ ./pkg/funcd/` ok;
  `golangci-lint run ./internal/blob/s3gateway/... ./pkg/funcd/...` → 0 issues;
  `go test -race ./internal/blob/s3gateway/` ok (`-count=3`); `go test -race ./pkg/funcd/ -run 'S3|Bucket|Blob'` ok.
  Linux lint, e2e and lanes were deferred to the group gate by the orchestration.
- **Shape**: `fix(s3gateway):` subject, `Fixes #157`, attribution trailer, one issue in one commit.

### Fix checklist
11 of 11 hold (item 8 on host checks for the touched packages; Linux lint and e2e are left to the group gate).

### Recommendation
Pass. Hand back to `/fix` Step 8. Separately, file the pre-existing s3gateway `-race` shutdown flake as its own issue.
