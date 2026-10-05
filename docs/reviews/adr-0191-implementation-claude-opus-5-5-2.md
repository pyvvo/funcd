## Verdict: changes-requested — 0 blockers, 1 major, 1 minor  (ADR-0191 implementation, loop 2, model: claude-opus-5-5)

Work: branch `feat/edge-response-write-deadline`, one commit `c783491e` (`git diff origin/main...HEAD`):
`internal/gateway/writestall.go` (+94), `internal/gateway/writestall_test.go` (+196),
`internal/dataplane/writestall_test.go` (+344), `pkg/funcd/funcd.go` (+5/-3), `pkg/funcd/writestall_e2e_test.go` (+52,
tag `e2e`). The bar is the held ADR-0191 with the proposed amendment (deadline set before each piece and cleared when
the call returns; edits 1–11). The review did not edit the work, stamp the ADR or change any doc.

### Verification run (in the worktree, through `scripts/agent/d`, Go 1.26.4)

| Check | Result |
|---|---|
| `go build ./...` darwin / `GOOS=linux` | exit 0 / exit 0 |
| `go vet` gateway, dataplane, pkg/funcd + `-tags e2e ./pkg/funcd/`, darwin and `GOOS=linux` | exit 0 / exit 0 |
| `golangci-lint run` on the three packages, darwin and `GOOS=linux` (host binary) | `0 issues.` / `0 issues.` |
| `go test -race -count=1 ./internal/gateway/ ./internal/dataplane/` | ok 2.8 s / ok 7.0 s |
| `go test -race -count=1 ./pkg/funcd/` (non-e2e) | ok 13.1 s |
| `go test -race -count=3` on the 5 scenario tests + 5 `WriteStall` unit tests | all pass, 3 of 3 (`TestWriteStallLiveClientNotCut` 1.63 s, over 3 S) |
| e2e `TestScenarioStalledReaderFreesSlotRealChain` (`-tags e2e`, by name) | **PASS (62.52 s)** |
| the same e2e with `pkg/funcd/funcd.go` from the merge base (overlay; no `WriteStall` in the chain) | FAIL after 92.32 s: B got **503 `edge.limit: in-flight concurrency ceiling reached`** (the 30 s retry does not mask the defect) |
| `go.mod` / `go.sum`; `WriteTimeout` in non-test code | unchanged; none |

Overlay mutants of `internal/gateway/writestall.go` (`go test -overlay`; the tree was not changed):

| Mutant | Killed by |
|---|---|
| mA `clear()` does nothing (the deadline stays armed while a stream is quiet) | `TestScenarioQuietStreamNotCut/http2` (`stream error: stream ID 1; INTERNAL_ERROR; received from peer`; `/http1` passes, as the amended step 4 says), `TestWriteStallArmSequence` (clears: expected 34, actual 0) |
| mB one arm per `Write` call (cleared on return) instead of per 32 KiB piece | `TestWriteStallLiveClientNotCut` (cut after 0.30 s; loop 1's Minor 2 is fixed), `TestWriteStallArmSequence` (expected 33, actual 2) |
| mC each piece is written after its clear (no deadline in force during the write) | `TestWriteStallArmSequence` ("a write or flush ran without a deadline", 32), `TestWriteStallCutsStalledWrite` (not cut in 10 s), all three `TestScenarioStalledReaderFreesSlot` cases and `TestScenarioStalledStaticAssetFreesSlot` (503, the ceiling detail) |

### 🟡 Major

- **Major 1: on HTTP/2, the deadline set after the handler returns can outlive the stream, and 60 s later funcd sends
  `RST_STREAM INTERNAL_ERROR` on a stream that has already ended.** Attribution: **model** (the clear-to-zero mechanism
  and the amended Decision 2 wording are this loop's own proposal; the code follows that wording).
  The amended Decision 2 and the `WriteStall` doc comment (`internal/gateway/writestall.go:21`) say "the end of an
  HTTP/2 stream stops it (`closeStream`)". That holds only if the deadline message is processed before the stream
  closes. In net/http's HTTP/2 server, `SetWriteDeadline` is asynchronous (`sendServeMsg`), and the final
  `END_STREAM` frame arrives on a different channel of the same serve loop, which picks between ready channels at
  random. A zero deadline sets `st.writeDeadline = nil`. If the deferred arm is processed after `closeStream`, it
  finds `nil`, creates a new `time.AfterFunc` (h2_bundle `SetWriteDeadline`), and nothing stops that timer. When it
  fires, `onWriteTimeout` queues a `RST_STREAM` that `writeFrame` does not drop on a closed stream.
  Evidence (overlay probe test, HTTP/2 over TLS, `WriteStall(500ms)`, 200 sequential GETs per run, frames counted
  with `GODEBUG=http2debug=2`):

  | Handler | This change | mA (no clear, loop 1's behavior) |
  |---|---|---|
  | `Write` 1 KiB, return | 0 / 0 / 0 stray resets | 0 / 0 / 0 |
  | `Write` 1 KiB, `Flush`, return (the `ReverseProxy` pattern with `FlushInterval -1`) | **51 / 51 / 66** of 200 | 0 / 0 / 0 |

  Example from the log: stream 23 got `DATA flags=END_STREAM` from the server, the client read it, and about 1 s
  later the server wrote `RST_STREAM stream=23 ErrCode=INTERNAL_ERROR`.
  In production (S = 60 s), about a quarter to a third of the HTTP/2 responses on the main proxy path keep a
  runtime timer and their `http2stream` alive for 60 s after the response ends. If the connection is still open
  then, the server sends a frame on a closed stream (RFC 9113 §5.1). Go and most clients ignore it. A peer may
  instead treat a late frame as a connection error, which would also end the other streams on that connection, such
  as a live SSE stream. Neither main nor loop 1 had this behavior. No test covers it. Every scenario test passes,
  because the reset reaches a stream that is already complete.
  Fix (builder): make sure that no deadline message can create a timer after the stream closes. One option is to
  "clear" with a far-future deadline instead of a zero one, so that the stream keeps its timer and `closeStream`
  stops it. HTTP/1.1 is not affected. Add a regression test that reads frames with `golang.org/x/net/http2`'s
  `Framer` (already in `go.mod`) and asserts that no `RST_STREAM` arrives within 2 S after `END_STREAM` on a
  flushing handler. Then correct the amended Decision 2 sentence and the doc comment.

### Minor

- **Minor 1 (carried from loop 1): the ADR's slowest-client figure does not match the mechanism.** Attribution:
  **adr** (not scored). Consequences still say "about 550 B/s at the slowest". The amended step 2 now specifies a
  64 KiB read buffer and 64 KiB per S/10, so the plan and the test agree. Only the Consequences figure stays
  inaccurate.

### Note (not scored)

- The amended Decision 2 and the doc comment still say that on HTTP/1.1 the deadline "stays on the idle keep-alive
  connection". With Go 1.26.4, `conn.serve` calls `c.rwc.SetWriteDeadline(time.Time{})` right after
  `w.finishRequest()` (`net/http/server.go:2081`), so it is cleared. The behavior is fine; only the text is wrong.
  The text can be fixed together with Major 1.

### ✅ Verified correct (keep it)

- **Mechanism**: `arm` / `clear` around `WriteHeader`, each ≤ 32 KiB piece of `Write`, and `Flush`. `Write` returns
  the bytes written so far and the first error. A deferred arm also runs during an `ErrAbortHandler` unwind. Nothing
  is set after a successful `Hijack`. Deadline errors are ignored. `Unwrap` is present, and the method set matches
  `commitWriter`. `d <= 0` returns `next` itself.
- **Amended plan, step by step**: step 1's e2e retries a 503 for 30 s and then checks that A's connection is closed
  before the whole body. It passes now and fails on the unfixed chain with the ceiling detail, so the retry does
  not hide the defect. Step 2's `TestWriteStallArmSequence` checks each clear, `fw.last.IsZero()` between writes,
  that no write or flush runs without a deadline, the arm after the handler, and that nothing is set after a
  `Hijack`. `TestWriteStallLiveClientNotCut` (4 MiB, a 64 KiB read buffer, at most 64 KiB per S/10, over 3 S) now
  kills the per-call mutant. Step 4's quiet stream runs over HTTP/1.1 and over HTTP/2 with TLS, and the HTTP/2 case
  kills the left-armed mutant.
- **Decisions 3–6**: `WriteStall(WriteStallTimeout)` is outermost in the listener chain (`pkg/funcd/funcd.go`), and
  the internal chain is unchanged. `limit.go`, `activator.go` and `calltracker.go` are untouched. The stalled-reader
  scenario proves every release (B gets 200 with the full body, A is cut or reset, the upstream handler has
  returned, and `CallTracker.Idle` is true). There is no config key, metric or log line.
- **Scenarios**: all five tests exist by name and pass, including the HTTP/2 variants.
- **Held publication**: the code, comments, test names and commit message describe the behavior only.
- **Reuse**: the tests reuse the package helpers (`fakeEndpoints`, `noScaler`, `seedFunction`, `newShimRig`,
  `nodeFn`), and no code is duplicated.

### Definition of Done

11 / 11 checklist and DoD items hold as written: 5 Review-checklist items and 6 DoD items. The at-acceptance doc
edits are held and not counted. This loop showed the e2e half of step 1 failing on the unfixed chain. Major 1 is a
defect in the amended Decision 2's mechanism claim and its production behavior on HTTP/2, not in a checklist line.

### Recommendation

Changes requested. Fix Major 1: keep each HTTP/2 stream's timer from outliving the stream, add a frame-level
regression test, and correct the Decision 2 sentence and the doc comment, including the HTTP/1.1 text from the Note.
Keep everything else. Minor 1 goes to the decider.

```json
{
  "date": "2026-10-05",
  "adr": "0191",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 1,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 11,
  "dod_total": 11,
  "report": "docs/reviews/adr-0191-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2 (c783491e, set-then-clear rework); build/vet/lint clean also Linux; gateway+dataplane+pkg/funcd -race ok, scenarios -count=3 ok; e2e RealChain passes (62.5 s) and fails on the unfixed chain with the 503 ceiling detail; mutants 3/3 killed (left-armed -> h2 quiet stream INTERNAL_ERROR, per-call arm -> LiveClientNotCut, write-after-clear -> ArmSequence/Cuts/503). M1 [model] on HTTP/2 the post-handler arm can land after closeStream once a clear has nil'd the timer: 51-66 of 200 flushing responses got a stray RST_STREAM INTERNAL_ERROR on the closed stream after S (0 with loop 1's no-clear behavior), contradicting the amended Decision 2; n1 [adr] 550 B/s figure in Consequences (carried)."
}
```
