# ADR-0168 implementation review (loop 2) — claude-opus-5-5

- **ADR**: [ADR-0168](../adr/0168-raw-output-pipes-and-record-bound.md) — raw output through two pipes, a bound on one log record
- **Work**: funcd `feat/adr-0168-raw-output-pipes` (fd751cb8, one commit, 36 files, +1513/-219; loop 1 reviewed
  4c60015f); funcd-typescript `feat/adr-0168-raw-output-pipes` (ae7ae24, unchanged since loop 1); funcd-python
  `feat/adr-0168-raw-output-pipes` (62da442, unchanged since loop 1)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — 0 Blocker, 0 Major, 2 Minor (1 model, 1 workflow). Every loop 1 finding the builder owned is
  resolved, each with a test that a mutant shows is not vacuous. The checks are green on all three repos. The items
  still open (the release pins, the lane run, the tracking docs) belong to the integration PR by the agreed sequence.
- **Judged against**: the ADR's Contracts, Scenarios, Review checklist and Definition of done; the preflight brief's
  agreed handling of drift; the loop 1 report.

## Loop 1 findings — resolution

The loop 1 → loop 2 delta (`git diff 4c60015f fd751cb8`) touches 7 files, +284/-4, and nothing outside the findings.

| Loop 1 finding | Status | Evidence |
|---|---|---|
| Major (model): the `funclog` Lima lane has no new cases | **Resolved** | `e2e/funclog.venom.yml` gains four cases: `raw-output-severity` and `record-cut-at-bound` for `log-burst` and `pylog-burst`. The first pair checks for a `burst stdout` line at `[INFO]` with `source=stdout` and a `burst stderr` line at `[ERROR]` with `source=stderr`. The second pair invokes with `{"big":3500000}` and checks for a `big value` record at `[INFO]` that has `truncated=true` and `keptBytes` ≤ 65536. The greps match the renderer (`cmd/funcdctl/logs.go` `wideSuffix`: `source=`, then sorted attrs as `key=value`). `--limit` returns the most recent records (`logs.go:49`). The JS case tags its line with `batch=rawsev`, which `burst.ts` echoes. The shim examples carry the inputs (`burst.ts` `big`, `handler.py` `big`). The cases are block-style YAML, and the header comment gains item 5. The lane run itself is outside this gate (see Integration obligations). |
| Minor 1 (model): a raw line of exactly `LineBytes` gives an extra empty line | **Resolved** | `workerpipe.go:146` now splits only when `len(st.partial)+len(seg) > o.opts.LineBytes`, and a comment states the rule. `TestLineOfExactlyLineBytesIsOneLine` covers an exact line, an exact line built from two Writes, and an over-long line. Mutant G1 (back to `>=`) fails with `"12345678\n\nab\n12345678\n\n12345678\n9\n"`. |
| Minor 2 (model): tests named in the plan are missing | **Resolved** | "Engine stores nothing": `pkg/funcd/rawoutput_internal_test.go` `TestEngineWorkerRawOutputIsNotStored` feeds the installed Path A hook a `KindCatalogService` run and a `KindFunction` run. It asserts that only `logs/default/fn/` is Put and that the engine keeps its tail (`"out\nerr\n"`). "No Pump after a failed `cmd.Start`": `internal/runtime/process/capture_test.go` `TestFailedStartLeavesNoPump` asserts that the hook ran once, that both Readers reach EOF, and that `Done` closes. "No Pump after a failed Create": containerd `TestCreate_FailedNetworkSetupKillsTask` now asserts that the hook never ran, because it runs after the network setup. Warn cadence: `workerpipe.go` gains an injectable `now` (set by `export_test.go` `SetClock`). `TestDropsAreWarnedAtMostOnceAMinute` checks no Warn at 59 s, one Warn with 4 drops at 60 s, none 30 s later, and a last Warn with the 2 unreported drops at `Close`, with `Dropped() == 6`. Mutants G2 and G3 below. |
| Minor 3 (workflow): the tracking docs were not moved | **Still open, deferred** | The funcd diff touches no file under `docs/` (`git diff --stat origin/main...HEAD -- docs` is empty). The ADR still reads `Status: Accepted`, F50 still reads `accepted`, and ADR-0152 has no back-link. The orchestrator defers these moves to the integration PR, and this gate was told not to edit docs. Carried below as a workflow Minor, not scored. |

## Verification run

Both sides were run together. The funcd worktree used a local `go.work` over the two language worktrees. The file was
gitignored and not committed, and it was removed after the run (`git status --short` clean).

| Check | Command (through each repo's `scripts/agent/d`) | Result |
|---|---|---|
| funcd build | `go build ./...` | exit 0 |
| funcd vet, touched packages | `go vet` on workerpipe, process, runtimecontract, runtime, funclog, function, config, provider, `pkg/funcd`, `cmd/funcd` | exit 0 |
| funcd tests, touched packages | `go test -race -count=1` on the same set | exit 0; every package `ok` (`pkg/funcd` 10.3 s, `cmd/funcd` 20.1 s) |
| funcd lint | `go tool golangci-lint run` on the same set + `./internal/runtime/containerd/...` | `0 issues.`, exit 0 |
| Linux | `GOOS=linux go build ./...`; `GOOS=linux go vet ./internal/runtime/... ./pkg/funcd/... ./cmd/funcd/...`; `GOOS=linux go test -c ./internal/runtime/containerd/`; host-built golangci-lint with `GOOS=linux` on `./internal/runtime/... ./pkg/funcd/...` | exit 0, exit 0, exit 0, `0 issues.` |
| Scenario tests | `go test -tags e2e -race -count=1 -run '^TestScenario(RawOutputSeverity\|LoadErrorInLogs\|RawOutputSurvivesRestart\|RecordCutAtBound\|RecordBoundReachesShim\|RecordBoundOverReaderCap\|PooledRawOutput)$' ./pkg/funcd/` | 7/7 `--- PASS`, `ok`, exit 0 |
| Load error, repeated | `go test -tags e2e -race -count=20 -run '^TestScenarioLoadErrorInLogs$' ./pkg/funcd/` | `ok` (11.9 s), exit 0 |
| funcd-typescript | `just ci` (install, lint, typecheck, test, build, go-check) | exit 0; tree clean after `build`, so `shim.mjs`/`pool.mjs` match the sources |
| funcd-python | `just ci` (ruff, mypy, pytest for the shim and every example, go-check) | exit 0 (shim 198 passed); tree clean |

Environment note: `GOOS=linux go tool golangci-lint` builds the tool itself for Linux, and the host cannot run that
binary (`exec format error`). The Linux lint was rerun with the host-built binary and `GOOS=linux` set. This is a
tooling quirk, not a finding. The containerd driver's tests run only on the Linux integration lane (`FUNCD_IT=1`,
root). Here they were compiled and linted for Linux, not run. No e2e suite and no Lima lane was run.

### Mutants (all caught)

| # | Side | Mutation | Caught by |
|---|---|---|---|
| G1 | funcd | the split loop back to `>= LineBytes` (the loop 1 defect) | `TestLineOfExactlyLineBytesIsOneLine` FAIL |
| G2 | funcd | process `Start`'s `fail` closure no longer closes `out` | `TestFailedStartLeavesNoPump` FAIL ("a Pump still reads the output of a run that never started") |
| G3 | funcd | the Path A hook drops its `OwnerKind == KindFunction` check | `TestEngineWorkerRawOutputIsNotStored` FAIL ("an engine's raw output must not be stored") |
| T3 | TS | `boundSpan` returns the line uncut | `tracespan.test.ts` "a span record over the bound has its status_msg cut and marked" FAIL |
| P3 | Python | `_emit_span` never cuts | `test_span_status_msg_is_cut_at_the_bound` FAIL |

The funcd mutants ran through `go test -overlay`. The TS mutant ran in a scratch copy of the repo. The Python mutant
ran from a scratch copy of `funcd_shim` placed first on `PYTHONPATH`, and a check confirmed that the copy was the
module imported. None of the work was edited. Loop 1's seven mutants (G1-G3, T1-T2, P1-P2) covered the rest. The
shim commits are unchanged, so those results still hold.

## Minor

1. **`validate`'s doc comment now documents `minRecordBytes`** (attribution: **model**). In `pkg/funcd/funcd.go:297-302`,
   the new `minRecordBytes` comment and const sit between `validate`'s doc comment and the function. Go attaches
   `// validate returns the first missing required dependency, …` to the const, so `validate` loses its doc comment and
   the const has two unrelated paragraphs. The lint does not flag it because both are unexported. Fix: move the const
   block above `validate`'s comment. This was already present in loop 1.
2. **The tracking docs are still not moved** (attribution: **workflow**, not scored). Carried from loop 1, Minor 3.
   The integration PR must set the ADR `Accepted → Reviewing` (the review gate then stamps `Implemented`), move F50
   in `docs/feat/0004-feat-platform-observability.md` to `reviewing`, and add `Superseded in part by: ADR-0168` to
   ADR-0152 for its `LogPath` contract line, as the preflight requires.

## Integration obligations (sequencing, not findings)

- **`go.mod` pins both releases.** `go.mod` still pins funcd-typescript v0.7.0 and funcd-python v0.4.0. After both
  shim releases, `go get` the two new tags. This review verified the pairing through `go.work`.
- **The `funclog` Lima lane must be green.** The four new cases need the new examples, which the lane stages from the
  pinned modules (`scripts/lanes.yaml:328-363` stages `burst.mjs` and `py/handler.py`). The lane can therefore pass only
  after the `go get`. The cases also need the `log-burst` rebuild of `burst.mjs`, which loop 1 verified as committed.
- **Rebase on funcd main.** origin/main is 6 commits past the branch's merge base (4fe29030), including the 0.5.0
  release. Both language branches sit on their current origin/main.
- The tracking docs (Minor 2).

## safeBeforeFuncd claims — both still hold

Both shim commits are unchanged, so loop 1's analysis stands. It was rechecked against the current funcd origin/main:

- **funcd-typescript (true)**: no non-test Go code in funcd main reads the shim's listening line. The `listening` hits
  are comments on the port-file `Listened` gate (`internal/function/bootbackoff.go`, `function.go`) and an engine
  readiness poll. main's process driver merges both streams into one file, so a listening line on stdout changes no
  read. Without `FUNCD_FUNCLOG_MAX_RECORD_BYTES`, `recordBound` gives 65536, today's reader still accepts every cut
  record, and `buildLine` keeps the `member` argument.
- **funcd-python (true)**: `FuncLogHandler(channel, member=None, max_record_bytes=...)` keeps `member` second, so
  `install_log_capture`'s positional call still binds. A line-buffered stdout only flushes earlier.

## Contracts, Review checklist and Definition of done

| Item | Holds | Evidence |
|---|---|---|
| No `LogPath`/`cio.LogFile`/temp log file; error returns close the `Output` and pipe ends; drivers per Decision 1 | yes | loop 1 grep; `process.go:170-175` `fail` closes every pipe end and `out` (mutant G2); containerd failed-Create test asserts no hook and no FIFO dir |
| Write never blocks; drops counted; Warn at most once a minute + one at `Close`, with the instance identity | yes | `TestDropsAreWarnedAtMostOnceAMinute` (injected clock); the process Output's logger carries namespace, name, revision and replica (`process.go:166-167`) |
| Function workers only: stdout → INFO, stderr → ERROR, read time, sealed at Shutdown; `loadError` unchanged | yes | `TestEngineWorkerRawOutputIsNotStored` (mutant G3); `TestScenarioRawOutputSeverity`, `TestScenarioLoadErrorInLogs` ×20 |
| A nonzero bound outside [1024, `MaxLineBytes`] fails startup; the env reaches every shim and pool host | yes | `TestScenarioRecordBoundOverReaderCap`, `TestScenarioRecordBoundReachesShim`, `recordbound_test.go` |
| Both shims stop at the budget and mark per Decision 3; Python stdout line-buffered everywhere | yes | both `just ci` green; mutants T3, P3 (plus loop 1's T1-T2, P1-P2) |
| `go.mod` pins both releases; each scenario has its named, passing test | partly | 7/7 scenarios pass; the pins wait for the releases (integration) |
| DoD: every scenario test passes, `TestScenarioLoadErrorInLogs` with `-count=20` | yes | above |
| DoD: `just ci` | yes | funcd sub-checks (build, vet, `-race` tests, lint, Linux) with the `go.work`; both shims' `just ci` |
| DoD: `funclog` lane green | pending | cases present and checked by reading them; the lane runs at integration, after the pins |

## ✅ Verified correct (keep it)

- **The fixes are minimal and targeted.** The loop 2 delta changes one comparison and adds one clock field in
  `workerpipe.go`. Everything else is tests and lane cases. No production behavior outside the findings moved.
- **The new tests are sensitive.** Each one fails under a mutant of the code it guards (G1-G3, T3, P3). The cadence
  test uses an injected clock rather than sleeps, and `SetClock` lives in `export_test.go`, so production gains only the
  `now` field, which defaults to `time.Now`.
- **The lane cases are grounded in the real renderer.** They grep the exact `-o wide` shape that `wideSuffix`
  produces (`[SEV]`, `source=`, sorted `key=value` attrs, a JSON `true` rendered bare). They use the existing
  `--limit 1000` most-recent read, block-style YAML, and the existing `retry`/`delay` wait pattern.
- **Everything loop 1 listed as correct is unchanged.** The commits for workerpipe's Contract, both drivers, the
  ingest, the record bound and both shims did not change. Loop 1's evidence still applies, and the full touched-package
  `-race` run and the 7 scenarios re-passed.

## Recommendation

**Pass.** The builder resolved every model finding from loop 1. Hand the work to the integration step, which must
release both shims, `go get` the two tags, rebase on funcd main, run the `funclog` Lima lane and the full gate, and
move the tracking docs (ADR → `Reviewing`, F50 → `reviewing`, the ADR-0152 back-link). The integration step can fold
in Minor 1 (one moved const block). This gate stamped nothing and edited no doc, as instructed. Once the integration
PR is green, the `Reviewing → Implemented` stamp, the F50 → `implemented` move and the board card → `Done` follow.

```json
{
  "adr": "0168",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 1,
  "dod_passed": 7,
  "dod_total": 9,
  "report": "docs/reviews/adr-0168-implementation-claude-opus-5-5-2.md",
  "notes": "Loop 2: all loop-1 model findings resolved (funclog lane cases for both languages; exact-LineBytes split; engine-stores-nothing, failed-start/Create no-Pump and Warn-cadence tests). Minor(model): minRecordBytes const steals validate's doc comment. Minor(workflow): ADR/F50 status and ADR-0152 back-link still deferred to integration. Pending by sequence: go.mod pins and funclog lane run. Checks green on all three repos; 5/5 new mutants caught; safeBeforeFuncd true for both shims."
}
```
