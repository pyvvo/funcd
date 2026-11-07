## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1411 fix, model: claude-opus-5-5)

This report reviews branch `fix/1411-s3-store-read-error`, commit ad787e91 `fix(blob): a failed read in a Bucket's lookup
answers InternalError, not NoSuchBucket` (`git diff origin/main...HEAD`: 25 files). Issue: "A store read error in the S3
frontend's Bucket lookup answers NoSuchBucket".

**This review is not independent.** The model that wrote the fix ran it, in the same session, because the decider's
standing rule for fix work is no review agent. Every check below was run, not read, but the judgement calls are the
fixer's own.

### ✅ Verified correct (keep it)
- **The regression tests fail without the fix, for the issue's reason.** With `origin/main`'s six changed production
  files and the five `pkg/funcd` test files whose call sites only changed type as an overlay, the three new tests fail:
  the S3 write answers `expected: 500`, `actual: 404`; `context.blob` answers `expected: "unavailable"`,
  `actual: "not_found"`; the static handler answers `expected: 503`, `actual: 404`. Before the fix, the issue's own
  reproduction ran as a probe on `origin/main` 5b0c0b2e: `HTTP 404 NoSuchBucket`, and 0 log lines named the error.
- **They pass with the fix**, un-skipped, under `-race`.
- **Mutants.** M1 (remove the `s3 bucket lookup error` log line) fails the S3 test on its log assertion. M2 (`context.blob`
  takes any lookup error for a missing Bucket) fails with `actual: "not_found"`. M3 (the static handler does the same)
  fails with `actual: 404`.
- **Cause, not symptom.** `s3BucketFor` turned every store read error into `false`, which each caller reads as a missing
  Bucket. The resolver now returns the error, and a missing Bucket is `fault.NotFound`. No retry, timeout or skip was
  added.
- **Siblings.** The same resolver serves six callers. The three that answered wrongly now keep the error: the S3 frontend
  answers `InternalError` and logs it, `context.blob` returns it with its kind, and the static handler answers the
  kind's status. The three that already behaved correctly keep their behavior with the real cause in the error:
  deletion protection still refuses the delete, the Site reconciler still requeues, and the blob watcher still logs and
  skips the poll.
- **Missing Buckets are unchanged.** `fault.NotFound` still answers `NoSuchBucket` on the S3 frontend, "references missing
  bucket" in `context.blob` and 404 from the static handler (the tenancy guard, ADR-0080), and the static test checks
  a Bucket that does not exist.
- **Scope.** Every hunk serves the issue: the resolver, its six callers, the fakes and call sites that take the new
  signature, the three tests and one registry row. No test was weakened or removed.
- **It is a bug.** The issue's Design check holds: no ADR decides what a store read error answers on these paths, the
  registry row "The metastore errors" is held by #1396, and the decider approved this answer on 2026-11-07 after the
  reproduction. No Accepted or Implemented ADR is contradicted, and no ADR file was edited.
- **Conventions.** The S3 path mirrors the gateway's own answer to a failed policy check (`s3 PEP error`, then
  `InternalError`); the static handler and `context.blob` keep the error's kind, as the static handler already does for
  a failed `Stat`. Imports are at the top, and the one new helper in `internal/dataplane` replaces three copies of the
  same fake.
- **Registry.** `docs/behavior/services.md` gains the row "A Bucket's lookup read fails in an S3 request", `decided`,
  citing the decider's record and the test; `just check-behavior` passes.
- **Checks.** `go build ./...`; `go vet` on the seven touched packages; `golangci-lint` on them: 0 issues; `go test -race`
  on them: all pass (`pkg/funcd` 57.1 s, `internal/blob/s3gateway` 43.4 s). `scripts/agent/gate.sh` on ad787e91: the audit, the Linux build, vet and lint, the stress run of the three new tests
  (220 s) and the unchanged tree passed, and the Linux tests were skipped because the funcd-linux-tests VM was not
  running. `ci-full` failed on one test only, `TestSite_RaisedBucketCapDeploysTheRefusedBundle` (`resourceVersion
  mismatch`), the open flake #1401; that test passed 100 of 100 runs under `-race` here and on `origin/main`'s code.

### Definition of Done
12 / 12 items that apply hold. Item 13 does not apply, because no design spec models the Bucket lookup.

### Model scorecard
Recorded: claude-opus-5-5 on #1411 (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12.

### Recommendation
Ready for the PR, with `Fixes #1411` in its body.
