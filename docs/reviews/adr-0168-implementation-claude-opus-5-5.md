# ADR-0168 implementation review (loop 1) — claude-opus-5-5

- **ADR**: [ADR-0168](../adr/0168-raw-output-pipes-and-record-bound.md) — raw output through two pipes, a bound on one log record
- **Work**: funcd `feat/adr-0168-raw-output-pipes` (4c60015f, 33 files, +1233/-219); funcd-typescript
  `feat/adr-0168-raw-output-pipes` (ae7ae24, 12 files); funcd-python `feat/adr-0168-raw-output-pipes` (62da442, 8 files)
- **Producing model**: claude-opus-5-5
- **Verdict**: **changes-requested** — 0 Blocker, 1 Major, 3 Minor. The code is correct, the checks pass, and every mutant
  was caught. The one Major is a missing plan deliverable: the `funclog` Lima lane cases.
- **Judged against**: the ADR's Contracts, Scenarios, Review checklist and Definition of done, and the preflight brief's
  agreed handling of drift. Each preflight item was checked.

## Verification run

Both sides were run together. The funcd worktree used a local `go.work` over the two language worktrees. The file was
not committed, and it was removed after the run.

| Check | Command (through each repo's `scripts/agent/d`) | Result |
|---|---|---|
| funcd build | `go build ./...` | exit 0 |
| funcd vet, touched packages | `go vet` workerpipe, process, runtimecontract, runtime, funclog, function, config, provider, `pkg/funcd`, `cmd/funcd` | exit 0 |
| funcd tests, touched packages | `go test -race -count=1` on the same set | exit 0 |
| funcd lint | `go tool golangci-lint run` on the same set + containerd | `0 issues`, exit 0 |
| Linux | `GOOS=linux go build ./...`; `go vet ./internal/runtime/... ./pkg/funcd/... ./cmd/funcd/...`; golangci-lint `./internal/runtime/... ./pkg/funcd/...`; containerd test compile | all exit 0, lint `0 issues` |
| Scenario tests | `go test -tags e2e -race -run '^TestScenario(RawOutputSeverity\|LoadErrorInLogs\|RawOutputSurvivesRestart\|RecordCutAtBound\|RecordBoundReachesShim\|RecordBoundOverReaderCap\|PooledRawOutput)$' ./pkg/funcd/` | 7/7 PASS (Node and Python subtests), exit 0 |
| Load error, repeated | `-count=20 -race -run '^TestScenarioLoadErrorInLogs$'` | exit 0 |
| funcd-typescript | `just ci` (install, lint, typecheck, test, build, go-check) | exit 0; tree clean after `build`, so `shim.mjs`/`pool.mjs` match the sources |
| funcd-python | `just ci` (ruff, mypy, pytest for every project, go-check) | exit 0; tree clean |

The containerd driver's `runtimecontract` subtests run only on the Linux integration lane (`FUNCD_IT=1`, root). Here
they were compiled and linted for Linux, not run. No full e2e suite and no Lima lane was run (out of scope for this
gate).

### Mutants (all caught)

| # | Side | Mutation | Caught by |
|---|---|---|---|
| G1 | funcd | `Output.Tail` renders in arrival order, not stdout then stderr | `TestTailKeepsTheNewestLinesStdoutThenStderr` FAIL |
| G2 | funcd | Path A hook drops the pool fan-out (`memberSink`) | `TestScenarioPooledRawOutput` FAIL |
| G3 | funcd | process `Start` keeps the parent's pipe write ends open | `TestProcessDriverContract` FAIL |
| T1 | TS | the cut record loses `attrs.truncated` | 6 tests FAIL, both scenario titles among them |
| T2 | TS | `recordBound` ignores `FUNCD_FUNCLOG_MAX_RECORD_BYTES` | 6 tests FAIL, `record-bound-reaches-shim` among them |
| P1 | Python | the cut record loses `truncated` | 5 tests FAIL, both scenario tests among them |
| P2 | Python | `_poolworker.init` does not line-buffer stdout | `test_pool_member_stdout_is_line_buffered` FAIL |

The funcd mutants ran through `go test -overlay`. The shim mutants ran in scratch copies. None of the work was edited.

## 🟡 Major

1. **The `funclog` Lima lane has no new cases** (attribution: **model**). Implementation plan step 3 says the
   `funclog` lane gains `raw-output-severity` and `record-cut-at-bound` for both languages. The Definition of done
   requires that lane to be green. The preflight handling was to add venom cases that grep for `attrs.truncated`. The
   funcd diff does not touch `e2e/funclog.venom.yml`, and `git diff --stat origin/main...HEAD -- e2e scripts` is empty.
   The shim halves are ready: both `examples/log-burst` handlers gained a stdout line, a stderr line and an optional
   large value, and `burst.mjs` was rebuilt. Nothing in funcd calls them. These cases would be the only end-to-end
   evidence for the containerd FIFO path with real shims. Fix: add the two cases for `log-burst` and `pylog-burst` to
   `e2e/funclog.venom.yml`, and stage the inputs in `scripts/lanes.yaml` if needed. The gate's lane run then validates
   them.

## Minor

1. **A line of exactly `LineBytes` gives an extra empty line** (attribution: **model**). In
   `internal/runtime/workerpipe/workerpipe.go` (`write`), the split loop runs at
   `len(st.partial)+len(seg) >= o.opts.LineBytes`. That flushes the full-length segment, and the newline then files an
   empty `st.partial`. An overlay probe with `LineBytes: 8` and the write `"12345678\nab\n"` read back
   `"12345678\n\nab\n"`. `rawReader` stores an empty line as an empty INFO or ERROR record. So any raw line of exactly
   64 KiB (or a multiple) adds one spurious empty record. The ADR splits lines "over 64 KiB" only. Fix: split on `>`,
   or skip the empty remainder when a split ends exactly at the newline. Add a unit case.
2. **Some tests named in the plan are missing** (attribution: **model**). No test installs `SetOutputCapture` outside
   the `pkg/funcd` scenarios (grep found no `*_test.go` that calls it). As a result, nothing checks that an engine's
   worker stores nothing (plan: "an engine stores nothing"). Nothing checks that no Pump is left after a failed
   `cmd.Start` or a failed containerd Create. The code paths look right: `fail` closes `out`, and the deferred cleanup
   closes `out` and the task IO. The `workerpipe` tests cover drops and the Warn at `Close`. They do not cover the
   once-a-minute cadence: `lastWarn` uses the wall clock, and no clock is injected. `internal/provider/runtime_test.go`
   dropped its "stopped engine's log file is removed" assertion with no replacement. That is acceptable, because the
   file no longer exists.
3. **The tracking docs were not moved** (attribution: **workflow**, not scored). The ADR still reads
   `Status: Accepted`. The F50 row in `docs/feat/0004-feat-platform-observability.md` still reads
   `raw output + record bound: accepted`. ADR-0152 has no `Superseded in part by: ADR-0168` back-link, which the
   preflight's ADR-0152 handling requires. The funcd diff touches no docs, so this looks deferred to the integration
   PR. It must land there: ADR `→ Reviewing`, F50 `→ reviewing`, and the ADR-0152 back-link.

## Not yet satisfiable at this stage (sequencing, not a finding)

- Checklist item "`go.mod` pins both releases": `go.mod` still pins funcd-typescript v0.7.0 and funcd-python v0.4.0
  (ADR-0158's tags). The ADR-0168 tags do not exist yet. Per Decision 4 and the preflight, the funcd PR runs `go get`
  on both tags after the two releases. This review verified the pairing through `go.work` instead.

## safeBeforeFuncd claims — both hold

- **funcd-typescript (true)**: the listening lines moved to stdout (`shim.ts:159`, `pool.ts:466`). funcd main's
  non-test code has no consumer of the listening line, and main's process driver merges both streams into one file, so
  the load-error read is unchanged. With no `FUNCD_FUNCLOG_MAX_RECORD_BYTES`, `recordBound` gives 65536. No funcd main
  test logs a value over 64 KiB through a shim (grep of `pkg/funcd`, `cmd/funcd`, `e2e`). `dropBrokenPipes` only
  swallows `EPIPE`. `buildLine` keeps the `member` argument.
- **funcd-python (true)**: `FuncLogHandler(channel, member=None, max_record_bytes=...)` keeps `member` second, as the
  preflight required, so `install_log_capture`'s positional call still binds. A line-buffered stdout only flushes
  earlier. Under main's merged file, this puts a `print` before a later stderr load error, which is an improvement.

## ✅ Verified correct (keep it)

- **workerpipe** matches its Contract. `Write` never blocks and returns `len(p), nil`. Each stream has its own 128 KiB
  queue budget (`TestStreamsHaveTheirOwnQueueBudget`). There is one shared tail with arrival-order eviction, rendered
  stdout then stderr, per the preflight resolution. `CloseRead` seals the Reader. A Drain writes past `Close`, and
  `Done` waits for every Drain. Drops are counted and Warned with the key `dropped`, reused from ADR-0158.
- **Process driver**: the port file is reserved with `os.CreateTemp("", "funcd-worker-*.port")` and truncated on
  `Start` rather than removed, per the preflight. The Path A hook runs last, before `cmd.Start`. The parent's write
  ends close after start (mutant G3 shows the contract test depends on it). `wait` closes the Output after the group
  kill and waits at most 1 s on `Done` before it sets the terminal state. `saveLocked` saves only the port file, and
  `TestStartSavesTheDriverOwnedFiles` was rewritten to that single case. Every error return closes `out` and the pipe
  ends made so far.
- **containerd driver**: the Output is made just before `NewTask`, with
  `cio.NewCreator(cio.WithStreams(nil, out.Writer(Stdout), out.Writer(Stderr)), cio.WithFIFODir(fifoDir))`. The hook
  runs after the network setup and before registration. A waiter runs `Wait`, `out.Close`, `io.Close`. `Stop` waits at
  most 1 s on `Done` before `task.Delete`. `fifoDir` is `<stateDir>/fifo`, emptied by `New`, or a private
  `funcd-fifo-*` temp dir that `Close` removes. The `LogPath`/`cio.LogFile` code is gone. `git grep` finds no
  `LogPath` or `cio.LogFile` in any Go file. Issue424 and Issue492 were turned into "no FIFO dir left" checks.
- **Ingest**: Path A is installed on Path B's gate, selected only by `OwnerKind == KindFunction`, per the preflight.
  The dead `Command` fallback is gone. Each stream goes through `funclog.Pump(NewRawReader(...))` into the same tee'd
  `logs` sink, tracked by `logRoutes`, which seals it through `CloseRead` at Shutdown. A pool worker's lines fan out to
  a `PoolMembers` snapshot through `memberSink` (mutant G2). `rawReader` stamps `Entry.Time`, and the scenario test
  asserts it.
- **Record bound**: the value is rejected unless it is 0 or in [1024, `MaxLineBytes`]. The `fault.Invalid` error
  names the key and 1048576, and the test covers 2097152, 1023 and -1. 0 resolves to the default, so
  `Deps.LogMaxRecordBytes` is never 0. The env reaches the process shim, the container shim and `poolSharedEnv`
  (create and restart). The config key, the config_test row, `examples/funcdconfig.yaml` (the new key and the
  corrected `enabled` comment) and the `WithoutFunclog` doc were all updated.
- **Shims**: the Contract symbols are all present (`DEFAULT_MAX_RECORD_BYTES`, `recordBound`, `Budget`,
  `boundedStringify`, `buildLine(..., bound, member?)`; `_DEFAULT_MAX_RECORD_BYTES`, `_record_bound`, `_Budget`,
  `_bounded_json/_str/_repr/_message`). The tests prove each serializer stops at the budget: a counting Proxy, a Set
  subclass, typed arrays and a 50 MiB Buffer that never calls `toJSON`; in Python, 50 MB of `bytes` and a large `%s`
  body. Today's text is kept for the issue 82/r23/r24/r32 values. The user keys `truncated` and `keptBytes` are
  skipped. Span `status_msg` is cut in both shims. Python gained a module-level `_emit_span` helper, per the
  preflight. Python stdout is line-buffered in `main` and in `_poolworker.init`.

## Recommendation

Loop back to `adr-impl` for the Major (the lane cases) and Minors 1 and 2. Minor 3 belongs to the integration PR.
The ADR stays at its current status, and nothing was stamped.

```json
{
  "adr": "0168",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 1,
  "minors": 3,
  "model_attributed": 3,
  "dod_passed": 7,
  "dod_total": 9,
  "report": "docs/reviews/adr-0168-implementation-claude-opus-5-5.md",
  "notes": "Major(model): funclog Lima lane lacks raw-output-severity/record-cut-at-bound cases. Minor(model): exact-64KiB raw line yields an extra empty record. Minor(model): missing engine-stores-nothing / no-Pump-after-failed-start tests. Minor(workflow): ADR/F50 status and ADR-0152 back-link not moved. Checks green on both sides; 7/7 mutants caught; safeBeforeFuncd true for both shims."
}
```
