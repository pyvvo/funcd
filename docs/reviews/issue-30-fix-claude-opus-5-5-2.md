## Verdict: pass — 0 blockers, 0 majors, 2 minors (both carried over, not scored)  (issue #30 fix, model: claude-opus-5-5, re-review 2)

Commits reviewed: `f40d987` `fix(blob): bound each S3 multipart upload by maxUploadBytes and drop abandoned uploads`
and the rework `10bdd8d` `fix(blob): address review of #30` (branch `fix/199-unbounded-memory`, group HEAD `10bdd8d`).
Only #30's commits are in scope. Together they touch `internal/blob/s3gateway/multipart.go`,
`internal/blob/s3gateway/multipart_unit_test.go`, `internal/blob/s3gateway/harness_test.go` and
`internal/blob/s3gateway/scenarios_test.go`.

The rework resolves both model findings of the first review. `TestIssue30_AbandonedMultipartUploadExpires` now
creates the live upload outside the idle window and sends its part inside it, so the test proves that a part
refreshes the window. The mutant that survived in round 1 is now killed. The multipart store reads time through
the ADR-0002 `clock.Clock` port. The fix still closes both halves of the issue at the root: a per-upload running
total is enforced at UploadPart, and abandoned uploads are swept on Create.

### Round-1 findings
| Round-1 finding | Attribution | Status |
|---|---|---|
| Major 1 — the idle-window refresh on UploadPart is untested | model | **Resolved.** The `notouch` mutant now fails `TestIssue30_AbandonedMultipartUploadExpires`. |
| Minor — the clock seam duplicates the ADR-0002 clock port | model | **Resolved.** `multipartStore.clock` is a `clock.Clock`, and its default is `clock.System()`. |
| Minor — no daemon-wide bound across concurrent uploads | adr | Still open, as expected. Carried below. |
| Minor — a pre-existing fasthttp shutdown race in `-race` runs | env | Still present at the same rate on the base. Carried below. |

### Minor
- **No daemon-wide bound across concurrent uploads** · attribution: adr · This finding is carried from round 1.
  Each upload is capped at `maxUploadBytes` and is dropped after one idle hour. A principal can still open many
  uploads within the hour, and each one can hold bytes up to the cap. ADR-0080 caps a single buffered object and
  sets no aggregate budget, so an aggregate cap is a decision and not part of this fix. Recorded, not scored.
  Route it to `/adr`, or to the streaming blob seam that ADR-0080 already names as its exit.
- **A data race in the package's `-race` runs predates this fix** · attribution: env (third-party) · This finding
  is carried from round 1. fasthttp `(*Server).ShutdownWithContext` races with `(*RequestCtx).Done`. The race is
  reached through `s3gateway.(*Server).Close` in the cleanup of `newGateway`. Interleaved loop of race-built
  test binaries: the head failed **1/20** and the pre-fix base (`f40d987^`, built with overlays) also failed **1/20**.
  Both failures were in `TestScenarioOwnerWrites` and had the same pair of stacks. During the `-race -count=1`
  loops of `TestIssue30_`, the race also fired once in `TestIssue30_MultipartTotalCappedAtUploadPart`, in 1 of 6
  runs, at gateway cleanup. It is still unfiled. File it as a flaky-test issue for `area/blob`.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 10bdd8d f40d987` applied cleanly,
  because no later commit touches `internal/blob/`. The revert also reverts the new tests, so the HEAD versions of
  `harness_test.go` and `scenarios_test.go` were restored on top of the pre-fix `multipart.go`. The result was
  `--- FAIL: TestIssue30_MultipartTotalCappedAtUploadPart … An error is expected but got nil … a part past the
  upload's cap must be rejected`. This is the reported behavior: only each part was capped, never the upload's
  total. The white-box `TestIssue30_AbandonedMultipartUploadExpires` does not compile against the pre-fix API,
  because the pre-fix code has no `clock` field and no `multipartIdleExpiry`. Its behavioral revert was therefore
  checked with mutants, listed below.
- **Passes with the fix under `-race`.** After `git reset --hard 10bdd8d`, `go test -race -count=3 -run TestIssue30_`
  reported 5 PASS and 1 FAIL. The FAIL was the pre-existing fasthttp race at gateway cleanup. The next six
  `-race -count=1` runs had 5 PASS and 1 FAIL from the same race. Both tests are un-skipped. The non-race package
  run passes.
- **Mutants are killed.** Each of these overlay mutants of `multipart.go` fails a `TestIssue30_` test:
  removing `u.touched = m.clock.Now()` from `putPart` (the survivor from round 1), removing the sweep's `delete`,
  creating an upload without setting `touched`, removing the size check, and dropping the subtraction of the
  replaced part. One boundary mutant survives: changing the sweep's `>` to `>=`. It only changes the behavior at
  exactly one hour of idle time, and that edge has no contract and no user-visible effect, so it is not a finding.
- **The timeline in the test is sound.** `abandoned` is touched at T, so at T+h+1s it is past the window and is
  dropped. `live` is created at T and receives a part at T+h−1s, so it is kept only because the part refreshed
  the window. `idle` is created at T+h/2 and is kept because it was created inside the window.
- **Reuse.** The store now uses `internal/platform/clock`, as ADR-0002 requires. The test-local `settableClock`
  is needed because `clock.Fake` cannot advance. It follows the same per-package precedent as `manualClock` and
  `stepClock` in the `internal/activator` tests, which other packages cannot import. The harness's variadic
  `opts ...func(*s3gateway.Deps)` follows the harness idiom in `internal/function`.
- **Root cause, not symptom.** The total is enforced at UploadPart under the store mutex, so concurrent parts
  cannot overshoot the cap. Uploads are swept on Create, which is the only way the number of uploads grows. The
  existing check at Complete stays as a backstop. An over-cap part fails closed with `EntityTooLarge` (HTTP 400),
  per ADR-0080. The Decision and Contracts of ADR-0080 are not contradicted, and no ADR file was edited.
- **Scope.** Every hunk in both commits serves #30. No test was weakened or deleted.
- **Checks.** `gofmt -l internal/blob` is clean. `go build ./...` passes on the host and with `GOOS=linux`.
  `go vet ./internal/blob/...` passes on the host and on Linux. `golangci-lint` reports 0 issues on the host and on
  Linux. The Linux run used the host-built binary with `GOOS=linux`. `go test ./internal/blob/...` passes.
  `-race` is green apart from the pre-existing race above. `go test -tags e2e ./pkg/funcd/...` is ok (105.8s),
  although no e2e test exercises multipart. `just check-hygiene` is clean. The Lima lanes were not run, as
  instructed.
- **Shape.** `f40d987` has the subject `fix(blob):`, `Fixes #30` and the attribution trailer. The rework
  `10bdd8d` has the subject `fix(blob):`, `Refs #30` and the trailer. Each commit covers only #30.

### Recommendation
Pass, and hand back to `/fix` Step 8. The PR body for the batch must carry `Fixes #30`, because the rework commit
says only `Refs #30`. Squash merge makes the split into two commits harmless. Before the PR, the fixer may fold
`10bdd8d` into `f40d987` so that the batch keeps its one-commit-per-issue shape. Two follow-ups remain outside this
fix: file the fasthttp shutdown race in s3gateway as a flaky-test issue, and route the aggregate multipart budget
to `/adr` if a daemon-wide bound is needed before the streaming blob seam lands.
