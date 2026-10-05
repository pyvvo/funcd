## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0191 implementation, loop 3, model: claude-opus-5-5)

Work: branch `feat/edge-response-write-deadline`, one commit `8956e244` (`git diff origin/main...HEAD`):
`internal/gateway/writestall.go` (+106), `internal/gateway/writestall_test.go` (+288),
`internal/dataplane/writestall_test.go` (+344), `pkg/funcd/funcd.go` (+5/-3), `pkg/funcd/writestall_e2e_test.go` (+52,
tag `e2e`). The bar is the held ADR-0191 with the loop-2 amendment and this loop's three edits: a clear moves the
deadline 100 years ahead and never sets the zero time; each request starts cleared (new Decision 2); and the unit test
`TestWriteStallEndedStreamNotReset`. The review did not edit the work, stamp the ADR or change any doc.

### Verification run (in the worktree, through `scripts/agent/d`, Go 1.26.4)

| Check | Result |
|---|---|
| `go build ./...` darwin / `GOOS=linux` | exit 0 / exit 0 |
| `go vet` gateway, dataplane, pkg/funcd + `-tags e2e ./pkg/funcd/`, darwin and `GOOS=linux` | exit 0 (all four) |
| `golangci-lint run` on the three packages: darwin, `--build-tags e2e`, and the host binary with `GOOS=linux` | `0 issues.` (all three) |
| `go test -race -count=1 ./internal/gateway/ ./internal/dataplane/` | ok 2.9 s / ok 6.8 s |
| `go test -race -count=1 ./pkg/funcd/` (non-e2e) | ok 12.9 s |
| `go test -race -count=3` on the 5 scenario tests + 6 `WriteStall` unit tests | all pass, 3 of 3 (`TestWriteStallEndedStreamNotReset` 0.93–0.97 s) |
| e2e `TestScenarioStalledReaderFreesSlotRealChain` (`-race -tags e2e`, by name) | **PASS (66.82 s)** |
| `go.mod` / `go.sum`; `go mod tidy` | unchanged; tidy gives no diff (`golang.org/x/net` is already a direct requirement) |
| `WriteTimeout` in non-test code | none |

Overlay mutants of `internal/gateway/writestall.go` (`go test -overlay`; the tree was not changed):

| Mutant | Killed by |
|---|---|
| mA `clear()` does nothing (the deadline stays armed while a stream is quiet; also no clear at the start) | `TestScenarioQuietStreamNotCut/http2` (`stream error: stream ID 1; INTERNAL_ERROR; received from peer`), `TestWriteStallArmSequence` ("the request starts cleared": expected 1, actual 0) |
| mB `clear()` sets the zero time (loop 2's mechanism) | `TestWriteStallEndedStreamNotReset`, **3 of 3** runs (`stream 31/73/65 was reset (INTERNAL_ERROR) after it ended`), `TestWriteStallArmSequence` |
| mC no clear before the handler runs | `TestWriteStallArmSequence` ("the request starts cleared"), 3 of 3; `TestWriteStallEndedStreamNotReset` passes, as expected: its handler writes, so the arm before `WriteHeader` creates the stream's timer early |

Additional probe (an overlay test file, discarded afterwards): HTTP/2 over TLS, `WriteStall(300ms)`, 2000 sequential
streams on one connection with 16 busy goroutines (`-cpu 4`) to delay the serve loop. Two handlers were tested: an
empty handler and a write-flush handler. The probe failed on any `RST_STREAM` during the run and for 3 S after the last
stream ended. Result with this change: **0 stray resets in 12,000 streams** (3 runs × 2 handlers). With mB: a
failure within 2 s in both handlers (stream 591, stream 3).

### 🟡 Major

None. Loop 2's Major 1 is fixed: no deadline message can create a timer after a stream closes once the clear at the
start of the request has created one. `closeStream` stops that timer, and every later `SetWriteDeadline` message then
gets `Stop() == false` and is ignored (`h2_bundle.go`, `http2responseWriter.SetWriteDeadline`).

### Minor

- **Minor 1 (carried from loops 1 and 2): the ADR's slowest-client figure does not match the mechanism.** Attribution:
  **adr** (not scored). The Consequences still say "about 550 B/s at the slowest". The decider owns this, and this
  change does not affect it.

### Note (not scored)

- **The new Decision 2 sentence "the stream has a timer from the start of the request" holds in practice, but
  net/http does not guarantee it.** The first clear is also a `sendServeMsg`. The serve loop picks between
  `serveMsgCh` and `wantWriteFrameCh` at random. In theory the first clear could still be pending when
  `closeStream` runs. For that stream, the result would be loop 2's defect: one late `RST_STREAM`. It would never be
  a timer that lasts 100 years, because the arm after the handler is always the stream's last deadline message, so
  any timer created that late fires within 60 s and is then released. The probe above saw 0 such resets in 12,000
  streams under CPU load, against loop 2's 51–66 in 200, and net/http offers no public API that would order these
  messages. Nothing needs to change. If the decider wants the text exact, "from the start of the request" could read
  "from the start of the request (applied before the response's frames in practice)".
- The HTTP/1.1 sentence in the new Decision 2 is correct for Go 1.26.4: `conn.serve` calls
  `c.rwc.SetWriteDeadline(time.Time{})` after `w.finishRequest()` (`net/http/server.go:2081`). The doc comment of
  `WriteStall` now says the same.

### ✅ Verified correct (keep it)

- **Amendment edit 1 (Decision 1)**: `clear()` sets `time.Now().Add(clearAhead)`, where `clearAhead = 100 * 365 * 24
  * time.Hour`, and never sets the zero time. `TestWriteStallArmSequence` asserts `zeros == 0`, one clear per arm, a
  cleared deadline between writes, and that no write or flush runs without a deadline in force.
- **Amendment edit 2 (Decision 2)**: `WriteStall` calls `sw.clear()` before `next.ServeHTTP` and arms once more in a
  `defer`, which also runs during an `ErrAbortHandler` unwind (asserted: 1 arm and 1 clear). The `stallWriter` doc
  comment explains the HTTP/2 ordering hazard in behavioral terms.
- **Amendment edit 3 (test)**: `TestWriteStallEndedStreamNotReset` sends 100 streams (ids 1–199) of
  write-flush-end responses on one HTTP/2 connection. It reads them with a raw `x/net/http2` `Framer` and fails on
  any `RST_STREAM` until 3 S after the last `END_STREAM`, as specified. It kills mB in 3 of 3 runs, so it pins the
  behavior and not only the call sequence.
- **Carried from loops 1–2**: arm/clear around `WriteHeader`, each ≤ 32 KiB piece of `Write`, and `Flush`. `Write`
  returns the bytes written so far and the first error. Nothing is set after a successful `Hijack`. Deadline errors
  are ignored. The writer has `Unwrap`, and its method set matches `commitWriter`. `d <= 0` returns `next` itself.
  `WriteStall(WriteStallTimeout)` is outermost in the listener chain (`pkg/funcd/funcd.go`), and the internal chain is
  unchanged. `limit.go`, `activator.go` and `calltracker.go` are untouched. There is no config key, metric or log line.
- **Scenarios**: all five tests exist by name and pass, including the HTTP/2 variants of the stalled-reader and
  quiet-stream tests. The e2e test passes on the real chain. Loop 2 showed that it fails on the unfixed chain with the
  503 ceiling detail.
- **Held publication**: the code, comments, test names and commit message describe the behavior only.
- **Reuse**: the tests reuse the package helpers. The new `h2Conn` and `readUntilEnd` helpers are used only by the
  new test, and no code is duplicated.

### Definition of Done

11 / 11 hold: 5 Review-checklist items and 6 DoD items. The at-acceptance doc edits are held and not counted. The
step 1 tests were shown failing on the unfixed chain in loops 1–2 (dataplane, and the e2e in loop 2), and both pass
now. The revert checks were run, and this loop added three mutants.

### Recommendation

Pass. Keep the change as it is. Minor 1 and the Note go to the decider as optional wording changes to the held ADR.
The model has no open finding.

```json
{
  "date": "2026-10-05",
  "adr": "0191",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 0,
  "dod_passed": 11,
  "dod_total": 11,
  "report": "docs/reviews/adr-0191-implementation-claude-opus-5-5-3.md",
  "notes": "loop 3 (8956e244, clear = 100 y ahead, request starts cleared); loop 2 M1 fixed; build/vet/lint clean also Linux; gateway+dataplane+pkg/funcd -race ok, scenarios + 6 unit tests -count=3 ok; e2e RealChain PASS 66.8 s; mutants 3/3 killed (left-armed -> h2 quiet stream INTERNAL_ERROR; zero-time clear -> EndedStreamNotReset 3/3 + ArmSequence; no initial clear -> ArmSequence); probe 0 stray RST in 12,000 h2 streams under CPU load (zero-time mutant fails within 2 s). n1 [adr] 550 B/s figure in Consequences (carried); note [adr, unscored] the initial clear is also async, so 'timer from the start' holds in practice, not by net/http contract."
}
```
