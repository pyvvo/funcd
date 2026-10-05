## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0191 implementation, model: claude-opus-5-5)

Work: branch `feat/edge-response-write-deadline`, one commit `a492b15a` on origin/main (`git diff origin/main...HEAD`):
`internal/gateway/writestall.go` (+85), `internal/gateway/writestall_test.go` (+176),
`internal/dataplane/writestall_test.go` (+331), `pkg/funcd/funcd.go` (+5/-3, wiring and the chain-order comment),
`pkg/funcd/writestall_e2e_test.go` (+40, tag `e2e`). Every file is one the ADR names. The review did not edit the work, stamp the ADR or change any doc.

### Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` (darwin) / `GOOS=linux go build ./...` | exit 0 / exit 0 |
| `go vet` gateway, dataplane, pkg/funcd + `-tags e2e ./pkg/funcd/` (darwin and `GOOS=linux`) | exit 0 (all four) |
| `golangci-lint run` on the three packages (darwin, and the host binary run with `GOOS=linux`) | `0 issues.`, exit 0 (both) |
| `go test -race -count=1 ./internal/gateway/ ./internal/dataplane/` | ok 1.5 s / ok 7.4 s |
| `go test -race -count=1 ./pkg/funcd/` (non-e2e) | ok 13.8 s |
| `go test -race -count=3` on the 5 scenario tests + 5 `WriteStall` unit tests | ok / ok (no flake in 3 runs) |
| `go.mod` / `go.sum` | unchanged |
| `WriteTimeout` in non-test Go code | none |
| e2e `TestScenarioStalledReaderFreesSlotRealChain` | compiled (vet `-tags e2e`, both OSes); not run (no e2e in this review; it runs once in the gate) |

Overlay mutants of `internal/gateway/writestall.go` (`go test -overlay`; the tree was not changed):

| Mutant | Killed by |
|---|---|
| m1 `WriteStall` always returns `next` (revert check 1; equals main's listener chain) | `TestWriteStallArmSequence` (expected 1, actual 0), `TestWriteStallCutsStalledWrite` (not cut in 10 s), `TestScenarioStalledReaderFreesSlot` by name / by route / http2 (B got **503 `edge.limit: in-flight concurrency ceiling reached`**, the ADR's prove-first reason), `TestScenarioStalledStaticAssetFreesSlot` |
| m2 `Hijack` does not set `hijacked` (revert check 2) | `TestWriteStallArmSequence` ("Should be zero, but was 1") |
| m3 one arm per `Write` call instead of per 32 KiB piece | `TestWriteStallArmSequence` (expected 33, actual 2); `TestWriteStallLiveClientNotCut` still passes (see Minor 2) |
| m4 no arm after the handler returns (deferred arm removed) | `TestWriteStallArmSequence` (expected 35, actual 34) |

### 🟡 Major

None.

### Minor

- **Minor 1: the ADR's slowest-client figure and its live-client test parameters do not match the mechanism.** Attribution: **adr** (not scored).
  The ADR's Consequences say a client must accept each 32 KiB piece within 60 s, "about 550 B/s at the slowest".
  Plan step 2 specifies `TestWriteStallLiveClientNotCut` as a 1 MiB `Write` to a client that reads 32 KiB every S/2,
  which is twice that rate. With the ADR's parameters (applied to the test as an overlay), the write was cut in
  5 of 5 runs (`--- FAIL ... (0.45s)`). The kernel wakes a writer that is blocked on a full send buffer only after
  the reader has drained a large share of the buffers, so the real minimum rate depends on the socket buffer sizes
  and is not a fixed 32 KiB per 60 s. The model found this, changed the client to read 128 KiB every S/4, and
  explained the change in a comment in the test (`internal/gateway/writestall_test.go:130-132`). That departure is
  justified. Impact at 60 s on real links (small autotuned buffers) is probably small, but the stated guarantee is
  not accurate. Owner: the decider, as a note for a superseding ADR if the slow-client bound matters. The Accepted
  ADR stays as it is.
- **Minor 2: `TestWriteStallLiveClientNotCut` does not prove that re-arming per piece keeps a live client alive.** Attribution: **model**.
  At the model's read rate, the 1 MiB body finishes within one S. Under mutant m3 (one deadline per `Write`), the
  test still passes in 0.23 s. In one run it passed in under 10 ms, which means the kernel buffered the whole body
  before any read. When the model changed the read rate, it did not enlarge the body so that the write would last
  several S. The property is still pinned elsewhere: m3 fails `TestWriteStallArmSequence`, and re-arming per
  `Flush` for a live stream is covered by `TestScenarioSlowLiveStreamNotCut`. Fix (builder): use a body that takes
  several S at the chosen read rate (for example 4 MiB at 128 KiB every S/4, about 2.3 s), so that m3 fails this
  test.

### Note (not scored)

- Decision 2 says the deadline armed after the handler "stays on the idle keep-alive connection". With the pinned
  Go 1.26.4, net/http clears the connection's write deadline right after `w.finishRequest()` in `conn.serve`. The
  armed deadline therefore bounds the final flush, which is what the ADR intends, and then it is cleared. A probe test
  (added as an overlay and then discarded) sent a second request with `Expect: 100-continue` on a reused
  connection, and its handler read the body 3 S after the deadline set for the first response. It got
  `100 Continue` and then `200 "hello"`, with and without `WriteStall`. So a deadline left over from an earlier
  response does not break the `100 Continue` that net/http writes for the next request. The real behavior is better
  than the ADR describes, and nothing needs to change.

### ✅ Verified correct (keep it)

- **Contracts**: `WriteStallTimeout = 60 * time.Second`; `stallChunk = 32 << 10`. `WriteStall(d)` returns `next`
  itself for `d <= 0` (`require.Same` for 0 and -1 s). The `stallWriter` fields are `ResponseWriter, d, hijacked`,
  and its methods are `WriteHeader`, `Write` (returns the bytes written so far and the first error), `Flush` (arms,
  then calls `ResponseController.Flush`), `Hijack` (through `ResponseController`, sets `hijacked` only on success)
  and `Unwrap`. This is the same method set as `commitWriter` (`internal/gateway/middleware.go`). Arm errors are
  ignored (`TestWriteStallNoDeadlineSupport`: a recorder gets the full 100 KiB body and `Flushed`).
- **Decision 1–2**: the deadline is armed before `WriteHeader`, before each ≤ 32 KiB piece and before `Flush`,
  and once more in a `defer`, so also during an `ErrAbortHandler` panic unwind (unit test asserts 1 arm). The
  `WriteStall` deadline is set 60 s ahead in production (`WithinDuration` check). **Decision 3**: nothing is armed
  after `Hijack`, and the code adds no clear. **Decision 4**: `gateway.WriteStall(gateway.WriteStallTimeout)` is
  the outermost middleware of `dpHandler` (`pkg/funcd/funcd.go`, the listener chain); the internal chain line is
  unchanged. **Decision 5**: no change to `limit.go`, `activator.go` or `calltracker.go`; the stalled-reader
  scenario proves each release (B 200 with the full 32 MiB body, A's connection closed or its HTTP/2 stream reset,
  the upstream handler returned, `CallTracker.Idle` true). **Decision 6**: no config key, metric or log line.
- **Scenarios**: all five tests exist by name and pass: `TestScenarioStalledReaderFreesSlot` (by name, by Route,
  HTTP/2 over TLS), `TestScenarioStalledStaticAssetFreesSlot`, `TestScenarioSlowLiveStreamNotCut` (16 events),
  `TestScenarioQuietStreamNotCut`, `TestScenarioUpgradeTunnelNotCut` (a 64 KiB response first on the same keep-alive
  connection, then 8 frames every S/2; it uses an echo upstream written in the test file, which the ADR allows while
  ADR-0181 is not on main).
- **Prove first**: the listener chain without `WriteStall` (m1) gives B the ADR's 503 detail on all three variants
  of the stalled-reader scenario.
- **Reuse**: `listenerChain` composes `outer`, `Recover`, `RequestID` and `limit.Chain` in the listener's order.
  The tests reuse existing package helpers (`fakeEndpoints`, `noScaler`, `seedFunction` in `dataplane_test.go`)
  instead of the names in the ADR plan (`readyEndpoints`, `spyScaler`, `seedFn`; `seedFn` does not exist). The e2e
  test reuses `newShimRig` and `nodeFn`. These are acceptable departures with no duplicated code.
- **Held publication**: no advisory id or security wording appears in the code, the comments, the test names or the
  commit message. The doc comment of `WriteStallTimeout` leaves out the Contract's "(ADR-0191)" reference to the held
  ADR, which matches the checklist rule "describe the behavior only".
- **Brief**: the change stays inside the files that the ADR names, so the integrator's rebases stay mechanical.

### Definition of Done

11 / 11 hold (5 Review-checklist items + 6 DoD items; the at-acceptance doc edits are held for publication and are
done by the wave's docs PR, so they are not counted). For "step 1 tests failed on unfixed main", this review
verified the dataplane test (m1, 503 reason). It did not run the e2e half, because no e2e runs in this review.

### Recommendation

Pass. Only Minor 2 goes back to the builder, and it is optional: make the live-client test's body large enough to
kill m3. Minor 1 is a note for the decider about the ADR's slow-client figure; it is not a defect in the
implementation.

```json
{
  "date": "2026-10-05",
  "adr": "0191",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 1,
  "dod_passed": 11,
  "dod_total": 11,
  "report": "docs/reviews/adr-0191-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (a492b15a); all Contracts + Decisions 1-6 hold; WriteStall outermost on the listener chain, internal chain untouched; build/vet/lint clean also Linux; gateway+dataplane+pkg/funcd -race ok, scenario tests -count=3 ok; 5/5 scenario tests pass (e2e compiled, not run); mutants 4/4 killed (no-op WriteStall gives the 503 prove-first reason, hijacked flag, per-piece arm, deferred arm). n1 [adr] the 550 B/s slowest-client figure and the plan's 32 KiB per S/2 live-client test do not hold (cut 5/5); the model's 128 KiB per S/4 departure is justified; n2 [model] TestWriteStallLiveClientNotCut's 1 MiB body ends within one S, so the per-piece mutant survives it (still killed by ArmSequence)."
}
```
