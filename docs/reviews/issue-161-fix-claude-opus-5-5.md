# Fix review — issue #161 (static weak ETag truncates ModTime to seconds) — claude-opus-5-5

- **Issue**: [#161](https://github.com/pyvvo/funcd/issues/161) — `kind/bug`, `priority/low`, `area/services`
- **Change**: branch `fix/i161`, commit `2ae5222` — `fix(static): keep full ModTime precision in the weak ETag so a same-second redeploy is not a stale 304`
- **Files**: `internal/edge/static/static.go` (+4/−2), `internal/edge/static/static_test.go` (+38)
- **Producing model**: claude-opus-5-5
- **Governing ADRs**: ADR-0120 (static asset serving Route; Implemented), ADR-0119 (object-store EventSource; Implemented — the `(ModTime, Size)` fingerprint precedent), ADR-0007 (`blob.Attributes`), ADR-0002 (conventions)
- **Verdict**: **pass**

## Summary

`weakETag` built `W/"<size>-<ModTime.Unix()>"`, so a same-length rewrite within one second kept its ETag and the
handler's `If-None-Match` short-circuit answered `304` for changed content. The fix keeps the sub-second part
(`ModTime.UnixNano()`), which removes the collision at its cause. The regression test reproduces the issue
deterministically, fails on the pre-fix code for the reported reason, and passes under `-race`. Two of three
mutants fail the test; the surviving one (millisecond precision) is a small test-precision gap, not a defect in
the fix.

## Blockers

None.

## Majors

None.

## Minors

1. **The millisecond-precision mutant survives** — attribution: `model`.
   Evidence: replacing `a.ModTime.UnixNano()` with `a.ModTime.UnixMilli()` leaves
   `go test ./internal/edge/static/` green (`ok`). The test steps ModTime by `300 * time.Millisecond`, so it
   cannot tell millisecond precision from full precision. A sub-millisecond same-length rewrite would still
   collide under that mutant. Using a sub-millisecond step (for example `300 * time.Microsecond`) in
   `TestIssue161_SameSecondRedeployIsNotStale304` would pin the "full precision" claim that the code comment makes.

2. **The issue's claim that the change needs a superseding ADR is overstated** — attribution: `issue` (recorded, not scored).
   Evidence: ADR-0120 Decision §2 and its Definition of Done describe the validator as a weak
   `(ModTime, Size)` ETag, written `W/"<size>-<modtime-unix>"`, with no per-request `sha256`. The fix keeps all
   of that: the ETag is still weak, still `<size>-<unix time of ModTime>`, still derived from `blob.Attributes`
   without a body read, and `http.ServeContent` still gets the real `ModTime`. Only the unit of the Unix time
   changes, and an ETag is opaque to clients (RFC 9110 §8.8.3). ADR-0119's implemented `versionOf`
   (`internal/eventing/blobwatch.go:220`) already fingerprints the same port with `UnixNano()`. No
   Accepted/Implemented ADR's Decision or Contracts is contradicted. The decider may still want ADR-0120's
   `<modtime-unix>` notation read as "Unix nanoseconds" when a follow-on ADR (the digest-ETag one) next touches it.

## Verified correct

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 2ae5222` with the
  new test file restored, then `go test -run TestIssue161 ./internal/edge/static/`:
  `expected: 200, actual: 304` — `changed content revalidated as unchanged (ETag W/"23-1790905216")`. This is the
  exact ETag and symptom from the issue's "Actual behavior". The worktree was then reset to `2ae5222` and is clean.
- **Passes with the fix under `-race`.** `go test -race -count=1 -v -run TestIssue161 ./internal/edge/static/` →
  `--- PASS: TestIssue161_SameSecondRedeployIsNotStale304`, `ok`. The test is not skipped.
- **The test is deterministic and drives the real path.** The `pinnedModTime` wrapper overrides only `List`,
  which is exactly what `stat` uses to read `blob.Attributes` (`static.go` `stat`). The test then sets two
  ModTimes 300 ms apart inside the same second, on a real `gocloud` `mem://` bucket, through `static.Handler.Serve`.
  It asserts the `200`, the new body, and a changed ETag.
- **Cause, not symptom.** The second-granular `ModTime.Unix()` named in the issue's "Root cause" is gone. No
  timeout, retry, cache bypass or swallowed error was added.
- **Mutants.** M1 `a.ModTime.Truncate(time.Second).UnixNano()` → `--- FAIL: TestIssue161_…`. M2 ModTime dropped
  from the ETag (size only) → `--- FAIL: TestIssue161_…`. M3 `UnixMilli()` → survives (Minor 1). Each mutant was
  restored with `git checkout`.
- **Scope.** Two hunks: the one-line `weakETag` change with its doc comment, and the new test plus its wrapper type.
  No existing test was changed, weakened or deleted.
- **Reuse, no duplication.** The change adds no helper, type or dependency to production code. The closest
  existing code, `eventing.versionOf`, serves a different contract (`data.version`, with a different field order)
  in a package that `internal/edge` should not import. The test reuses the file's own `serve` and `backend`
  helpers and the `gocloud` memory driver. The `pinnedModTime` embed-and-override wrapper is test-local and minimal.
- **Conventions.** Imports stay at the top level (`time` added to the test's import block). There is no YAML. The
  doc comment states the reason and cites the issue, without restating the code. Naming matches the file's idiom.
- **ADRs.** No `docs/adr/` file is touched (`git diff origin/main...HEAD --name-only` shows none). ADR-0120's
  weak `(ModTime, Size)` validator and its no-body-read 304 short-circuit are preserved; see Minor 2.
- **Other assertions on the ETag format.** `e2e/s3.venom.yml`, `pkg/funcd/static_e2e_test.go` and
  `pkg/funcd/site_e2e_test.go` assert only the `W/` prefix, so they are unaffected.
- **Checks (touched packages).** `gofmt -l internal/edge/static/` → clean. `go build ./...` → ok.
  `go test -race -count=1 ./internal/edge/...` → all 8 packages `ok`. `go vet ./internal/edge/static/` → ok.
  `golangci-lint run ./internal/edge/static/` → `0 issues.` By the gate's scope, Linux lint, the e2e suite and the
  lanes are left to the group gate.
- **Commit shape.** The subject is `fix(static): …`, the body explains the cause and the precedent, `Fixes #161`
  is present, the attribution trailer is present, and the commit covers one issue. No PR exists yet; the PR
  description is not reviewed here.

## Fix checklist

| # | Item | Result |
|---|---|---|
| 1 | `TestIssue161_…` reproduces the behavior | yes |
| 2 | Fails on pre-fix code for the reported reason | yes (`304` for `W/"23-1790905216"`) |
| 3 | Passes with the fix, un-skipped, under `-race` | yes |
| 4 | Reverting or mutating the key lines fails a test | yes (revert, M1 and M2 fail; M3 survives — Minor 1) |
| 5 | Root cause fixed, not masked | yes |
| 6 | Only the issue's scope; no test weakened | yes |
| 7 | No ADR contradicted or edited; living docs true | yes (Minor 2 records the reading) |
| 8 | Build, vet, lint and tests green | yes for the touched packages (host); Linux lint and e2e are left to the group gate |
| 9 | Conventions hold | yes |
| 10 | Reuses what exists; no duplication | yes |
| 11 | Commit shape (`fix(<scope>):`, `Fixes #N`, trailers) | yes |

11 of 11 items hold.

## Recommendation

**pass.** The fix is minimal, removes the root cause, and is proven by a deterministic regression test. An
optional follow-up: tighten the test's ModTime step to sub-millisecond so the "full precision" claim is pinned
(Minor 1). The change can go to the PR step.
