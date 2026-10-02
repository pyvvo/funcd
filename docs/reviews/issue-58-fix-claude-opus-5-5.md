## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #58 fix, model: claude-opus-5-5)

Change: branch `fix/i58`, commit 8bd9660 `fix(admission): refuse to delete a Bucket whose prefixes still hold objects`
(`git diff origin/main...HEAD`: `internal/controlplane/admission/bucket.go`, `internal/controlplane/admission/bucket_test.go`,
`pkg/funcd/funcd.go`, new `pkg/funcd/bucket_protection_test.go`).

Fix checklist: 10 of 11 items hold (item 4 holds only in part: one mutant survives, see Minor 1).

### 🟡 Major / Minor

- **Minor 1 — the test does not pin the prober to the prefix it is asked about** · attribution: `model`
  - Evidence: mutant M3 changes `blobProber.HasAny` in `pkg/funcd/funcd.go` to list the whole bucket view
    (`p.lister.List(ctx, ns, bucket, "")`) instead of `prefix+"/"`. `go test -run 'TestIssue58|Bucket' ./pkg/funcd ./internal/controlplane/admission`
    stays `ok` for both packages. The regression test drops `web` only while `web` itself holds the object, so it never
    covers dropping an empty prefix while a sibling prefix holds data. The `"/"` separator, which keeps `raw` from matching `raw2/…`, is also untested.
  - Impact: low. The surviving mutant refuses more than it should. It does not delete data. The real code is correct.
  - Fix: add one assertion to `TestIssue58_…`: put an object under `raw/`, then drop the empty `web` prefix. The drop must be
    admitted. Optionally add a sibling prefix such as `raw2` to cover the separator.

- **Minor 2 — two Implemented ADRs still describe the nil-wired prober** · attribution: `adr`
  - Evidence: `docs/adr/0139-site-declarative-static-web-app.md` §7 says the admission does not guard the old prefix
    because production wires it "with a nil blob prober". The `spec.bucket.deletion: delete` row repeats that rationale.
    After this fix, both statements are historical. ADR-0139 is `Implemented`, which makes it frozen, so the fix correctly did not edit it.
  - Fix: no work in this change. When ADR-0139's deferred drain-then-delete follow-on is written, it should note that the
    data-emptiness prober is now wired (issue #58).

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 8bd9660` (keeping the new test file):
  `TestIssue58_BucketWithObjectsIsDeletionProtected` FAILs at `bucket_protection_test.go:46`,
  `expected: "conflict" actual: ""`, which means the Bucket delete went through while `s3/default/lake/web/index.html`
  existed. That is exactly the issue's defect. The worktree was then reset to 8bd9660 and is clean.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/controlplane/admission ./pkg/funcd` → both `ok`.
  The test drives the real `Platform` (`New(InMemory())` plus `Run`) over the HTTP SDK, so it exercises the production admission
  wiring, not a fake.
- **Root cause fixed, not masked.** Both causes the issue names are removed. (1) The prober is no longer nil at
  `pkg/funcd/funcd.go` (the `NewBucketDeletionProtectionAdmission` wiring). (2) The wrong `bucketPrefix` helper
  (`<ns>/<bucket>/<prefix>/`, which lacked the `s3/` root) is deleted. `BlobProber.HasAny` now takes `(ns, bucket, prefix)`, and the
  wiring resolves the key layout through `s3BucketFor`, the same view the s3gateway, the static handler, the Site reconciler and the
  BlobWatcher use. The substrate key layout therefore has a single owner. The probe fails closed: a resolve or list error becomes `fault.Internal` and the delete is refused.
- **Mutants.** M1 (`len(items) > 0` → `> 1`) → `TestIssue58` FAIL. M2 (Update-path probe replaced by `false, nil`) →
  `TestIssue58` FAIL. M3 survives (Minor 1). The full revert also fails the test. Every mutant was restored, and the tree is clean.
- **Scope.** Every hunk serves #58. The `bucket_test.go` edit only adapts the fake prober to the new signature. No assertion is weakened or deleted.
- **Reuse.** `blobProber` composes the existing `blobBucketLister` and `s3BucketFor`. It does not build a new key layout, and it mirrors the
  shape of the existing `kvProber`. `internal/blob.Bucket` has no bounded or exists-under-prefix list, so a full `List` is the available primitive,
  as it is for `kvProber`.
- **ADRs.** The change implements ADR-0080's stated contract: block Delete, and an Update that removes a prefix, while that prefix is bound or
  non-empty, through a blob-prefix prober. No Accepted or Implemented ADR file was edited. Site prefixes are added to `Bucket.spec.prefixes`
  (ADR-0139 §5), so they are covered by the probe as well.
- **Conventions.** `api/fault` kinds are used, the prober port is declared in the consumer (as for `KVProber`), the API uses typed
  `v1.NamespaceName`/`v1.ObjectName`, ctx comes first, imports are at the top level, and there is no comment bloat.
- **Checks (touched packages).** `gofmt -l` is clean; `go vet` passes; `golangci-lint run ./internal/controlplane/admission/... ./pkg/funcd/...` → `0 issues`.
  The Linux lint, the e2e suite and the lanes are left to the group gate, as instructed.
- **Commit shape.** The subject is `fix(admission): …`, the body has `Fixes #58` and the `Co-Authored-By` trailer, and the commit covers one issue.

### Recommendation

Pass. Minor 1 is an optional test hardening, and the fixer can fold it in before the PR. Minor 2 needs no action in this change.
