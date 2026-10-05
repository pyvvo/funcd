# ADR-0188 implementation review (claude-opus-5-5, loop 1)

- **ADR**: [ADR-0188](../adr/0188-s3-multipart-memory-budget.md): S3 multipart memory budget, one daemon-wide bound on buffered parts and assembled copies
- **Work**: branch `feat/adr-0188-s3-multipart-budget`, commit 8282713d on origin/main (one commit, 5 files, +586/-50: `internal/blob/s3gateway/{multipart.go,s3gateway.go,budget_test.go,multipart_unit_test.go}`, `examples/funcdconfig.yaml`)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**. There are no Blockers or Majors. There is one Minor, attributed to the model (a test gap; the code is correct on those lines).
- **Preflight brief**: the one ADR-0188 item in the brief holds. The Put-blocking test bucket (`blockingPutBucket`, `budget_test.go`) embeds `blob.Bucket`, so it keeps compiling once ADR-0184 adds `ListAfter`. Every touched file is one the ADR names. The ADR status and the feat row were not touched, as the brief asks (the wave's docs PR does that).

## Verification run (captured)

| Check | Command (via `scripts/agent/d`) | Result |
|---|---|---|
| Build darwin | `go build ./...` | exit 0 |
| Build linux | `GOOS=linux go build ./...` | exit 0 |
| Vet darwin and linux | `go vet ./internal/blob/s3gateway/` | exit 0, both |
| Tests | `go test -race -count=1 ./internal/blob/s3gateway/` | `ok internal/blob/s3gateway 13.7s` |
| Tests (config example) | `go test -race -count=1 ./internal/platform/config/` | `ok` (the example YAML comment changed) |
| Lint darwin | `go tool golangci-lint run ./internal/blob/s3gateway/` | 0 issues |
| Lint linux | host-built golangci-lint binary (`go tool -n golangci-lint`) with `GOOS=linux` | 0 issues (`GOOS=linux go tool golangci-lint` builds a linux binary that cannot run on the host, an `env` artifact) |
| Prove-first | origin/main `multipart.go`, `s3gateway.go` and `multipart_unit_test.go` through `-overlay`, with the new `budget_test.go` | **FAIL as the ADR predicts**: `TestScenarioMultipartSharePerPrincipal` fails at `budget_test.go:69` ("An error is expected but got nil", part of upload 1 answers 200), and `TestScenarioConcurrentCompleteSingleFlight` fails ("a second Complete of the same id built its own copy and reached Put"). HEAD: PASS |
| Scenario tests | `go test -race -v -run 'TestScenario…|TestMultipartBytesReturned|TestIssue30'` | all 7 `TestScenario*`, `TestMultipartBytesReturned` and both `TestIssue30_*` PASS |
| Tree | `git diff --stat origin/main...HEAD` | no file under `docs/`, no `go.mod`/`go.sum` change |

### Overlay mutants (`go test -overlay`, the whole `internal/blob/s3gateway` package)

| # | Mutation (in `multipart.go`) | Killed by |
|---|---|---|
| m1 | `assemble` drops the `u.completing` SlowDown (no single flight) | TestScenarioConcurrentCompleteSingleFlight |
| m2 | `putPart` refreshes `touched` at `u.size >= u.peak` (a same-size re-send keeps the upload alive) | TestScenarioNonGrowingPartDoesNotHoldBudget, TestMultipartBytesReturned/a_smaller_or_regrown_part… |
| m3 | `putPart` checks again after a refusal without sweeping | TestScenarioExpiredUploadFreesBudgetOnPart, TestScenarioNonGrowingPartDoesNotHoldBudget |
| m4 | `assemble` checks again after a refused copy without sweeping | **survived** (see n1) |
| m5 | `release` leaves `completing` set | TestMultipartBytesReturned/refused_release_keeps_the_parts_and_touched, TestScenarioStaleIfMatchPutRejected |
| m6 | the `writeConditions` exit calls `release(…, true)` (drops the parts) | TestScenarioStaleIfMatchPutRejected (ADR-0159) |

Five of six mutants were killed. The survivor is a test gap; the code is correct on that line.

## Contracts and Decisions

- `multipartBudgetFactor`, `multipartStore` (`maxUpload`, `budget`, `buffered`, `assembling`, `owned`) and `upload` (`peak`, `owner`, `completing`, the new `touched` meaning) match the Contracts block field for field. The signatures of `newMultipartStore`, `create`, `putPart`, `assemble`, `release` and `sweep` are as specified; `abort` and `parts` keep theirs, and `abort` now returns the bytes. The new helpers `drop`, `grow`, `partFits` and `copyFits` are unexported and hold `m.mu` by contract.
- Site table: `s3gateway.go` passes `newMultipartStore(maxUpload)`; the loop in `create` is `m.sweep(now)`; `CreateMultipartUpload` passes `pr.ref`; `UploadPart` drops the `maxUpload` argument; `CompleteMultipartUpload` calls `release(…, true)` on the cap refusal and on success, and `release(…, false)` on the `writeConditions` exit and the Put error. No exit after an admitted `assemble` skips `release`.
- D1: `budget = 3 × maxUpload` when `maxUpload <= MaxInt64/3`, else 0 (off). Every comparison is a subtraction of bounded terms (`maxUpload − owned`, `maxUpload − size`, `budget − maxUpload − buffered`, `budget − buffered − assembling`), so none can overflow. `n` is a sum of distinct parts (part numbers strictly ascend), so `n ≤ size ≤ maxUpload`.
- D2: the owner is `pr.ref`, cloned field by field in `create` (all four `EntityRef` fields), so the map key does not alias fiber's buffers. Every part counts against `u.owner`, whoever sends it.
- D3: the per-upload cap (`delta > maxUpload − size`, 400 `EntityTooLarge`) is checked before the share and the budget (503). A part with `delta ≤ 0` is always admitted. `touched` moves only when `size` passes `peak`.
- D4: a refused part sweeps once, checks again, answers `NoSuchUpload` if the sweep dropped this upload, and otherwise `SlowDown`, with no part and no `touched` change. `sweep` skips `completing` uploads, in `create` too.
- D5: the order is `NoSuchUpload`, the part-list errors, then `SlowDown` for an in-flight Complete, then the copy check with one sweep. The copy is built only after `completing` is set and `n` is added to `assembling`.
- D6 and D7: `release` returns `n`, clears `completing`, and drops the upload only with `drop`; a kept upload's `touched` is unchanged. On an upload aborted meanwhile it returns only `n`. `grow` deletes an owner at 0, so `owned` never holds a zero entry.
- Implementation plan step 4: `examples/funcdconfig.yaml` and `Deps.MaxUploadBytes` name the 3 × M budget and the M share.

## Review checklist

| Item | Holds | Evidence |
|---|---|---|
| Cap (400) before share and budget (503) | yes | `putPart`; TestScenarioSizeCapStaysEntityTooLarge |
| Parts ≤ `budget − maxUpload`, parts + copies ≤ `budget`, off on overflow, no overflow | yes | `partFits`, `copyFits`, `newMultipartStore`; `requireAccounting` asserts both bounds after every step; the overflow subtest |
| A refused part or Complete sweeps once, then `SlowDown`, refused upload unchanged | yes (code); the Complete half is untested (n1) | m3 killed, m4 survived |
| Only growth past `peak` refreshes `touched`; kept `release` does not; sweep skips completing | yes | m2 and m5 killed; the sweep subtest |
| Second Complete refused; every exit releases; 503, 412 and Put error keep the parts | yes | m1 and m6 killed |
| Abort, Complete and sweep return bytes; no zero `owned` entry | yes | TestMultipartBytesReturned (abort, release, aborted-meanwhile, sweep, owner at 0) |
| One test per scenario, named as in the plan; step-1 failure shown | yes | all 8 names present; the failure on origin/main is reproduced above |
| No new key, port method, metric or dependency; no machine-specific value in the diff | yes | diff and `go.mod` unchanged |

## Findings

### Minor

- **n1 [model]: no test covers the sweep on a refused Complete, or the `NoSuchUpload` answer when a refusal sweep drops the refused upload itself.** Mutant m4 removes `m.sweep` from the copy check in `assemble`, and every test in the package still passes. Decision 5 and the third Review-checklist item require that sweep, and the code has it. The `NoSuchUpload` branches after the sweep in `putPart` and `assemble` are also unexercised. If one of those `get` checks were lost, a part could be counted into an upload that is no longer in the map, and `buffered` would keep the bytes for good. A unit case closes both gaps. Hold one copy in flight (`assembling = M`), fill the parts limit with one upload of M/2 that has been idle for more than `multipartIdleExpiry`, and Complete a third upload of M/2. The Complete should be admitted only after the sweep. A second case should check that a refused part to an idle upload answers `NoSuchUpload` and leaves the counters consistent.

## Verified correct (keep)

- The step-1 test reproduces #731 on origin/main code: all 20 full-share parts of one principal are admitted, and two concurrent Completes of one id each build a copy.
- The bounds are asserted directly. `requireAccounting` recomputes `buffered` and `owned` from the uploads and checks both budget inequalities after every step. Each unit test therefore guards the accounting, and not only the status code it names.
- The concurrency test is deterministic. The second Complete starts only after the first is inside Put. Its `select` fails the test the moment a second copy reaches Put, and it unblocks Put before `t.Fatal` so that no goroutine leaks. The SDK runs with one attempt, as the plan asks.
- The accounting has one site. All size changes go through `grow`, and every removal goes through `drop`, which `abort`, `sweep` and `release` share. The counters therefore cannot drift between paths.
- `TestIssue30_AbandonedMultipartUploadExpires` takes the new signatures only, and its assertions are unchanged. The existing ADR-0159 test `TestScenarioStaleIfMatchPutRejected` still passes, and it kills m5 and m6. The parts kept on a refused Complete are therefore guarded by two tests.
- The change is small and stays inside the files the ADR names. It adds no dependency, config key or metric.

## Open PR-time items (owed by the PR, not scored)

- The Definition of done asks the PR to state whether DuckDB httpfs (the version in `images/runtime/duckdb/Dockerfile`) retries a 503 `SlowDown`, citing its source or a `just lima-example duckdb` run, and to file an issue if it does not. The PR also has to include the step-1 failure output (reproduced above) and to run the repo-wide checks once in `scripts/agent/gate.sh`. No PR exists yet, so this review could not check these items. The integrator has to carry them into the group PR.

## Recommendation

Pass. n1 is a small test addition that the builder can add in a follow-up or with the group PR. It does not block the ADR from reaching `Implemented` in the wave's docs PR.

```json
{
  "date": "2026-10-05",
  "adr": "0188",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 8,
  "dod_total": 8,
  "report": "docs/reviews/adr-0188-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (8282713d): TestScenarioMultipartSharePerPrincipal and ConcurrentCompleteSingleFlight fail on origin/main code (20 parts all 200; second Complete builds a copy), pass on HEAD; all Contracts + Decisions 1-7 hold; build/vet/lint clean also Linux; s3gateway -race ok; 7/7 scenario tests + TestMultipartBytesReturned pass; mutants 5/6 killed (single flight, peak touch, part sweep, release completing, 412 keeps parts). n1 [model] no test for the sweep on a refused Complete (m4 survived) or the NoSuchUpload branch after a refusal sweep. PR still owes the DuckDB httpfs SlowDown-retry statement."
}
```
