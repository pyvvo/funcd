## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #126 fix, model: claude-opus-5-5)

Fix under review: commit `31fc291` `fix(workflow): read a step's answer only up to the payload limit`
on branch `fix/199-unbounded-memory` (reviewed at group HEAD `474064f`). Touched files:
`internal/workflow/dispatch.go`, `internal/workflow/engine.go`, `internal/workflow/dispatch_test.go`.

Issue: `HTTPDispatcher.Dispatch` read every step response with an unbounded `io.ReadAll`, and the engine
applied `workflow.payloadLimit` only afterwards, so a 200 MiB answer under a 1 MiB limit allocated about
410 MiB, and a 5xx body was read in full on every retry and then discarded.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

Observation (not scored): mutant M4 below (the 2xx read capped at exactly `MaxOutput`, without the `+1`)
is killed by a nil dereference of `rec` in the test's `Fatalf`, not by a clean assertion. The test follows
the idiom of the surrounding engine tests (`builtin_test.go` dereferences `rec` the same way), and the
mutant still fails, so this is not a finding.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 31fc291` reverts the test
  with the code, and the restored test does not compile against the pre-fix code (it uses the new
  `DispatchRequest.MaxOutput` field and the `errBodyMax` constant). To see the behavior, I added only those
  two identifiers to the reverted code, unused. Result: `--- FAIL: TestIssue126_DispatchReadsOnlyWhatTheStepNeeds`,
  with every subtest reading the whole 8 MiB body:
  `read 8388608 bytes of the answer under a 1048576-byte payload limit`,
  `read 8388608 bytes of a rejection whose error keeps 256`,
  `read 8388608 bytes of a 5xx answer that is thrown away`.
- **Passes with the fix under `-race`.** After `git reset --hard 474064f`:
  `go test -race -count=1 -run TestIssue126 -v ./internal/workflow/` gives
  `--- PASS: TestIssue126_DispatchReadsOnlyWhatTheStepNeeds`, not skipped.
- **The issue's own steps.** A scratch probe (not committed) ran the real `HTTPDispatcher` against an
  `httptest` server that streams 200 MiB, with `PayloadLimit` 1 MiB, and measured `MemStats.TotalAlloc`
  around `Execute`. Pre-fix (overlay of the `31fc291~1` files): 2xx 410 MiB, 5xx 410 MiB, the same numbers
  as the issue. With the fix: 2xx 2 MiB (the run fails with `step "a" output exceeds payload limit 1048576`),
  5xx 0 MiB (the step stays retryable: `returned 500`).
- **Root cause, not symptom.** The read itself is now bounded. A 2xx answer is read up to `MaxOutput+1`
  bytes, so the engine's existing `len(out) > PayloadLimit` check still rejects an over-limit output and
  never accepts a truncated one. A 4xx answer is read up to `errBodyMax+1` bytes, so `truncate()` still
  appends its ellipsis. A 5xx answer is not read at all. The engine passes `e.cfg.PayloadLimit` at both
  dispatch sites (the step and the `onFailure` handler, `engine.go:651` and `engine.go:749`), and these
  are the only two `DispatchRequest{` literals outside the tests. `MaxOutput == 0` keeps the documented
  "0 = unbounded" meaning of `Config.PayloadLimit` (`engine.go:152`). The status classes are unchanged:
  2xx returns the output, 4xx is `Permanent`/`Invalid`, and every other status is retryable `Unavailable`.
- **Mutants (overlay); each one fails the test:**
  - M1: the engine stops passing `MaxOutput`. The `an over-limit output` subtest fails (read 8388608 bytes).
  - M2: the 4xx read has no `LimitReader`. The `a 4xx` subtest fails (read 8388608 bytes).
  - M3: the 2xx cap is `MaxOutput*2`. The `an over-limit output` subtest fails (read 2097152 bytes).
  - M4: the 2xx cap is `MaxOutput` with no `+1`, which would silently accept a truncated output. The
    `an over-limit output` subtest fails (by a panic, see the observation above).
- **Scope.** Every hunk serves the issue. The one behavioral side effect is the changed over-limit
  message: it no longer states the exact output size, because the output is no longer read to its end.
  The commit message states this, and no test, doc or suite matched the old text (I grepped for
  `bytes exceeds payload limit`). No test was weakened or deleted, and the existing
  `TestPayloadLimitCapsStepOutput` still passes.
- **Reuse.** The fix uses the standard `io.LimitReader(…, max+1)` pattern, as
  `internal/runtime/provision/provision.go:222` and `internal/blob/s3gateway/backend.go:412` do. The
  256-byte constant moved out of `truncate()` into the package-level `errBodyMax`, so there is still
  one source of the number. The test helpers (`streamedBody`, `roundTripFunc`) are local to the test file,
  and neither `internal/testkit` nor `internal/platform` has an equivalent. The counting readers in
  `internal/catalog/gateway` are test-only in another package.
- **Conventions (ADR-0002, CLAUDE.md).** Errors are `api/fault` with the existing kinds, signatures are
  ctx-first and contain no `any`, and imports are at the top level. The new field has a doc comment that
  cites ADR-0094, and the one inline comment states why the read is bounded. There is no YAML in the change.
- **ADRs.** The fix conforms to ADR-0094's payload cap on step outputs and makes the cap bound memory
  as well as the stored output. No ADR file was edited (`git show --stat 31fc291` touches only
  `internal/workflow`).
- **Checks, run through the pinned dev shell:**
  - `gofmt -l internal/workflow`: no output.
  - `go build ./...` and `GOOS=linux go build ./...`: ok.
  - `go vet ./internal/workflow/...`, host and Linux: ok.
  - `golangci-lint run ./internal/workflow/...`, host and Linux: `0 issues.` on both.
  - `go test -race -count=1 ./internal/workflow/...`: ok, including `runstate/badger`.
  - The e2e suite `go test -tags e2e -count=1 ./pkg/funcd/...`: `ok github.com/pyvvo/funcd/pkg/funcd 111.173s`.
    It covers the workflow path through `workflow_*_e2e_test.go`.
  - `just check-hygiene`: ok.
  - No Lima lane: the change touches none of the lane-covered paths.
- **Commit shape.** A `fix(workflow):` subject, a cause-and-fix body that names the regression test,
  `Fixes #126`, the attribution trailer, and one commit for this issue.

### Definition of Done
11 of 11 applicable items hold: the regression test reproduces the issue, fails before the fix, passes
under `-race`, and catches the mutants; the root cause is fixed; the scope holds; no ADR is contradicted;
the checks pass (host and Linux, plus e2e); the conventions and reuse hold; the commit shape holds.
No misses.

### Model scorecard
To record: claude-opus-5-5 on issue #126 (fix): pass, 0/0/0, 0 attributed to the model, DoD 11/11.
A later stage writes the ledger row.

### Recommendation
Sign off. No changes are needed for #126; it can ship with the group PR.
