# Fix review — issue #517 (freeLoopbackAddr is duplicated in two pkg/funcd test files)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #517 fix, model: claude-opus-5-5)

Change: branch `fix/i517`, commit b759bd0 `fix(funcd): define the freeLoopbackAddr test helper once`. Files:
`pkg/funcd/export_test.go` (+6), `pkg/funcd/s3gateway_internal_test.go` (+40), `pkg/funcd/site_e2e_test.go`
(+1/-11).

The fix does what the issue's "Done when" asks: `export_test.go` exports `FreeLoopbackAddr`, a one-line delegate
to the internal `freeLoopbackAddr`, next to the S3 gateway helpers it already exports (`TakenPortReserve`,
`StartWithS3Gateway`). The e2e file passes `funcd.FreeLoopbackAddr` to `startSitePlatform` and its local copy (and
the now-unused `net` import) is gone.

### 🟡 Minor 1 — the regression test checks the name, not the duplicated body  ·  attribution: model

Evidence: mutant M2 survives. Replacing the export's `return freeLoopbackAddr(t)` with an inline copy of the
listen-close-return body (`pkg/funcd/export_test.go:9-12`) leaves `TestIssue517_FreeLoopbackAddrDefinedOnce` at
`ok`. A renamed local copy in the e2e file would pass the same way. The test pins the issue's exact defect (a
second `freeLoopbackAddr` declaration, M1 below fails), and a duplication guard on bodies would be a clone
detector (the bloat audit's `dupl`), not a unit test; so this is a recorded limit, not a request.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit b759bd0` with the test file kept from
  HEAD: `freeLoopbackAddr is defined 2 times, want 1: [s3gateway_internal_test.go:31:1 site_e2e_test.go:280:1]`
  — the two locations the issue names. The worktree was then reset to b759bd0 and is clean.
- **Passes with the fix under `-race`**: `TestIssue517_FreeLoopbackAddrDefinedOnce`, `TestIssue288_…` and the
  `TestScenarioS3Gateway…` tests PASS; the whole `pkg/funcd` package passes with `-race` (9.8 s).
- **The test sees build-tagged files**: it reads the sources through `//go:embed *_test.go`, which matches
  `site_e2e_test.go` regardless of its `e2e` tag, so the default (non-e2e) run guards the e2e file. Embedding
  also makes an overlay revert reach the sources. Parsing source with `go/parser` in a test has precedent
  (`api/fault/issue327_test.go`, `internal/function/doc_test.go`, `cmd/funcd/main_test.go`).
- **Mutant M1** (a `freeLoopbackAddr` added back in a new `e2e`-tagged `funcd_test` file): fails with "defined 2
  times". Killed.
- **Cause, not symptom**: the cause named in the issue (the external package had no way to reach the internal
  helper, so it carried a copy) is removed by the export; no behavior change for the S3 gateway tests.
- **Reuse**: the fix reuses the existing internal helper and the existing `export_test.go` bridge rather than
  adding a helper to `internal/testkit`. No other copy of the helper exists in `pkg`, `internal`, `cmd` or `api`;
  the two raw `net.Listen("tcp", "127.0.0.1:0")` calls in `pkg/funcd/funcd_test.go` hold the listener open on
  purpose (a busy-port setup) and are not copies.
- **Scope**: every hunk serves the issue; no test weakened or deleted (the removed function was the duplicate).
- **The e2e file still compiles**: `go vet -tags e2e ./pkg/funcd` clean and `go test -tags e2e -run <none>`
  builds; the e2e suite itself was not run (group gate).
- **ADRs**: test-only change; no ADR contradicted, no ADR file touched.
- **Conventions**: imports at top level, one short why-comment on the embed and one on the export (matching the
  neighbours' style), `t.Helper()` on the export, `t.Parallel()` on the new test.
- **Checks** (touched package): `go vet ./pkg/funcd` and `go vet -tags e2e ./pkg/funcd` clean;
  `golangci-lint run ./pkg/funcd/...` 0 issues, with and without `--build-tags e2e`; `gofmt -l` empty.
- **Shape**: `fix(funcd):` subject, `Fixes #517`, attribution trailer, one issue in one commit.

### Definition of Done

11 of 11 applicable items hold. Item 8 is verified for the host-side package checks only (vet, lint with and
without the `e2e` tag, `-race` tests of `pkg/funcd`); the Linux lint, the repo-wide tests and the e2e suite are
left to the group gate. Item 11 is verified on the commit; the PR is opened later by `/fix`. Mutation (item 4)
holds through the revert check and M1; the M2 survivor is recorded as Minor 1.

### Model scorecard

claude-opus-5-5 — pass; 0 blockers, 0 majors, 1 minor (model-attributed: name-only regression test).

### Recommendation

Pass. No change required; the group gate's bloat audit is the right place to catch a body-level clone.
