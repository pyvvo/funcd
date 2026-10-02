# Issue #32 Fix Review — one log line over 1 MiB ends a worker's log and trace capture

**Verdict**: **pass**. The regression test fails on the pre-fix code for the reported reasons (Route stops after the
long line, Pump spins, the raw reader reports a fatal error) and passes with the fix under `-race`. Three mutants of the
fix's key lines each fail the test. A probe over a real `os.Pipe`, wired like the `pkg/funcd` capture hook, now keeps
all 51 later records with no writer errors. The change removes the cause the issue names and touches nothing else.
There are two Minors, and neither blocks sign-off.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #32 · ADR-0081 (Pump contract: a malformed line is skipped, never killing the stream; no
loss) · ADR-0101 (one span per invocation) · ADR-0002 · `CLAUDE.md` style rules

The change is one commit, `255d034` (`fix(funclog): skip a log line over 1 MiB instead of ending capture`), on
`fix/201-log-capture`. It touches `internal/funclog/reader.go`, `internal/funclog/route.go` and
`internal/funclog/funclog_test.go`.

## Verdict: pass — 0 blockers, 0 majors  (issue #32 fix, model: claude-opus-5-5)

### Minor
- **Minor · model — an unterminated last line whose length is an exact multiple of 64 KiB is lost at EOF.**
  `lineReader.next` (`internal/funclog/reader.go:33-55`) returns the reader error as soon as `ReadLine` fails, which
  discards what it has already accumulated in `l.line`. `bufio.Reader.ReadLine` returns a 64 KiB fragment with
  `more=true` when the buffer fills, and then `nil, io.EOF` when the channel ends right after it. So a final line with no
  newline and a length of k·65536 bytes (k = 1..16) is dropped, where `bufio.Scanner` returned it. A scratch probe
  compared `lineReader` with `bufio.Scanner` on `"a\n"` followed by n bytes and no newline: n = 65535 and 65537 give 2
  lines for both; n = 65536 and 131072 give 1 line for `lineReader` and 2 for the scanner. In the same case an over-cap
  unterminated tail ends with `io.EOF` and no warning. The same probe found parity with `bufio.ScanLines` on every other
  case it tried: empty input, a final line without a newline under 64 KiB, CRLF, a lone `\r`, blank lines, a 200 KiB
  line, and a line of exactly 1 MiB. The impact is small: the only production caller is `Route`, and its NDJSON writer
  ends every record with a newline. **Fix**: on `io.EOF`, return the accumulated line (or `errLineTooLong`) first when
  `len(l.line) > 0` or `tooLong`, and add a case for an unterminated 64 KiB tail to the test.
- **Minor · model — the `Route` doc comment still describes the scanner.** `internal/funclog/route.go:70` says "the
  blocking Scan pauses the drain". After this change `Route` blocks in `lineReader.next`, not in a `Scan`. **Fix**: say
  "the blocking read".

### ✅ Verified correct (keep it)
- **The regression test fails without the fix.** In a detached worktree I ran `git revert --no-commit 255d034` (no
  conflict), restored the new test file from `255d034`, and ran `go test -race -count=1 -run TestIssue32 ./internal/funclog/`.
  All three subtests fail for the issue's reasons:
  - `route`: `expected: []string{"before", "after"}`, `actual: []string{"before"}` (`funclog_test.go:277`). Route
    returned at the long line and dropped everything after it.
  - `pump`: `expected: 1`, `actual: 100` warnings (`funclog_test.go:290`, "not a spin"). Pump spun on the sticky
    `bufio.ErrTooLong` until the test's warning limit cancelled it.
  - `raw`: `expected: "invalid"`, `actual: "internal"` (`funclog_test.go:301`). The raw reader reported the long line as a
    fatal channel error.
- **It passes with the fix**: after `git reset --hard 255d034`, `-race -count=3` passes 3/3 for all three subtests.
  Nothing is skipped. The `warnCounter` cancels the context at a limit, so a regression fails the test instead of
  hanging it.
- **The user-visible loss is gone.** A scratch overlay test runs `Route` over an `os.Pipe` in a goroutine and closes the
  read end when `Route` returns, as the `SetLogCapture` hook in `pkg/funcd/funcd.go` does. It then writes one record, one
  record with a ~1.1 MB body, and 50 more records. With the fix, under `-race`, it reports `records=51 writeErrors=0`. The
  issue reported 1 record kept and 50 `EPIPE` writer errors.
- **Mutants**: each overlay mutant fails `TestIssue32_LongLineIsSkipped`:
  - m1, never set `tooLong`, so the cap is gone: route, pump and raw fail.
  - m2, `Route` returns on `errLineTooLong` instead of `continue`: route fails.
  - m3, return `errLineTooLong` at once instead of reading through to the newline: route, pump and raw fail. The rest of
    the long line leaks out as more lines.
- **Cause, not symptom.** The issue names `bufio.Scanner`'s sticky `ErrTooLong` as the cause. The fix replaces the
  scanner with a line reader that reads an over-long line through its newline, never buffers more than the cap, returns a
  distinct `errLineTooLong` and keeps the reader usable. `Route` warns and continues on that error. The NDJSON and raw
  readers map it to `fault.Invalid`, which `Pump` already treats as a skippable record. The 1 MiB cap is unchanged, so
  daemon memory stays bounded. `Route` still returns on EOF or a real read error, so the hook still closes the channel
  when the worker goes away. The fix covers the inferred co-pooled case as well, because the pool worker's shared channel
  goes through the same `Route`.
- **Scope**: every hunk serves the issue. The `op` constants and the "read channel" / "read fd" wording replace the
  removed scanner calls. No test was weakened or deleted.
- **Reuse**: one line reader now serves `Route`, `NewNDJSONReader` and `NewRawReader`, which removes three copies of the
  scanner setup. No helper in the repo already does capped line reading; the only other `ReadLine` call is this one.
  The reader is built on `bufio.Reader.ReadLine` from the standard library. The test reuses the package's harness:
  `memBucket`, `newSink`, `newTraceSink`, `readBackOne`, `readBackTraces`, `defaultRes`, `testTraceID` and `testSpanID`.
- **Conventions**: errors cross the `Reader` boundary as `api/fault` (`fault.Invalidf`, `fault.Wrapf`). The unexported
  sentinel `errLineTooLong` follows the package-private sentinel idiom used elsewhere (`errArtifactUnresolved`,
  `errPeekDone`). Logging is `slog` only, imports are at the top level, there is no `any` in signatures, and the comments
  explain why (the cap, why `bufio.Scanner` was replaced).
- **ADRs**: the fix restores ADR-0081's Pump and Route contract ("a single malformed line is skipped (logged), never
  killing the stream") and ADR-0101's one span per invocation; the `route` subtest checks that the span after the long
  line is kept. No ADR file changed.
- **Checks** (all through `nix develop -c`): `gofmt -l internal/funclog` is empty. `go build ./...` and
  `GOOS=linux go build ./...` pass. `go vet` (host and Linux) passes on `internal/funclog/...` and `pkg/funcd/...`.
  `golangci-lint` reports `0 issues.` on host and Linux. `go test -race -count=1 ./internal/funclog/... ./pkg/funcd/...`
  passes. The e2e suite `go test -tags e2e -count=1 ./pkg/funcd/...` passes (`ok … 114.688s`). `just check-hygiene`
  reports `hygiene: clean`. No Lima lane covers this path.
- **Shape**: the subject is `fix(funclog): …`, the body has `Fixes #32` and the `Co-Authored-By` trailer, and the commit
  covers one issue.

### Observation (outside this fix, not scored)
- `Pump` (`internal/funclog/pump.go:28-30`) still runs `continue` on any non-EOF read error. A read error that never
  clears, such as a closed pipe, would still spin it. `Pump` has no production caller, since `Route` replaced it in the
  composition root, and this issue concerns only the over-long line. The fix closes that path.

### Definition of Done
11 / 11 items hold: regression test present; fails pre-fix for the reported reason; passes under `-race`; mutants
fail it; root cause fixed; scope clean; no ADR contradicted or edited; build, vet, lint (host and Linux), tests and e2e
green (no Lima lane covers the path); conventions; reuse; commit shape. Misses: none. The two Minors are model-attributed
and do not fail an item.

### Model scorecard
To be recorded by a later stage: claude-opus-5-5 on issue #32 (fix) → pass, 0/0/2, 2 model-attributed, DoD 11/11.

### Recommendation
Sign off. Fixing the EOF tail case in `lineReader.next` (and the one-word doc comment) is cheap and fits this branch,
but neither blocks the merge.
