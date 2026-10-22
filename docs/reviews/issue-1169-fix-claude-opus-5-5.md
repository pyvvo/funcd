## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #1169 fix, model: claude-opus-5-5)

Commit 42dc65bf on `fix/stress-flakes-1` (main f7e40c89) changes `internal/dataplane/writestall_test.go`:
- `TestScenarioStalledReaderIsCut` and `TestScenarioStalledStaticAssetIsCut` run the write-stall guard at a new
  `cutStall` of 2 s, and wait `cutStall + 2 s` before client B's request;
- `edgeChain` takes the limit as a parameter, and the streams that must not be cut keep `stall` (300 ms).

### Minor 1 — ADR-0191 states S as 300 ms for these tests  ·  attribution: adr
- **What the ADR says.** ADR-0191's Scenarios section (`docs/adr/0191-response-write-stall-timeout.md:50`) says "S is
  the stall limit: … 300 ms in the `internal/dataplane` tests". Its plan, step 3, passes
  `gateway.WriteStall(300 * time.Millisecond)`.
- **The deviation.** The two cut scenarios now run at S = 2 s. Their substance holds with that S: B calls at S + 2 s,
  B gets the whole body, A is cut, A's upstream returns and the call count is 0.
- **Why it is recorded, not scored.** The ADR's test value is the source of the flake. A reader must take no data
  for S, and 300 ms is shorter than the reader's worst pause under `-race`. The ADR is Implemented and frozen, and a
  test parameter does not need a superseding ADR.

### Minor 2 — a sibling with the same construct in `internal/gateway`, not mentioned  ·  attribution: model
- **The sibling.** `TestWriteStallLiveClientNotCut` (`internal/gateway/writestall_test.go:166`) has a client that reads
  4 MiB through 64 KiB send and receive buffers, at one read per S/10, with S = 300 ms. A pause of 300 ms in its loop
  cuts it, as it cut B.
- **Why it is not counted as the same cause.**
  - In the stress run 39791754697, it passed all 5 runs.
  - Its package runs no parallel 32 MiB pipelines, which are the load that starved B.
  - Raising its S lengthens the test, because the write must outlast 3 S at the client's pace.
- **The fix.** Name it under #1142, so a later failure of it is recognized at once.

### ✅ Verified correct (keep it)
- **The injection proof, reproduced.** B was paused 400 ms after its first MiB, in `getBody`, by overlay:
  - with main's test file, all four cases fail with CI's `unexpected EOF` (by name, by route, http2, static asset);
  - with the changed file, all four pass (4.5 s each, in parallel).
- **The cut is still tested.** A mutant passes `gateway.WriteStall(0)` in `edgeChain`, so there is no guard. All four
  cut cases then fail, because B gets no 200 while A holds the only in-flight slot.
- **Cause, not symptom.**
  - The guard cut a reading client that took no data for S. That is its designed behavior.
  - The test's S was shorter than B's worst pause under `-race`.
  - Production keeps `gateway.WriteStallTimeout` (60 s). No assertion was loosened, and B's 32 MiB stays as the
    scenario requires.
- **Siblings in the file.** All four B readers move to `cutStall`, including the two that did not fail in the stress
  run. The not-cut streams send small events through the 64 KiB buffer, so their writes do not block on a client
  pause. They correctly keep 300 ms.
- **Reuse.** `edgeChain` gains a parameter, and no new helper was added.
- **Checks.**
  - `go test -race -count=1 ./internal/dataplane/` passes (7.2 s).
  - `go vet` passes on darwin and Linux.
  - `golangci-lint` reports 0 issues on darwin and Linux.
- **Shape.** The subject is `test(dataplane):` (test-only), with `Fixes #1169` and the trailer.

### Definition of Done
11 / 11. Item 1, a committed `TestIssue1169_` test, does not apply: the proof is an injection, and I reproduced it.

### Model scorecard
Recorded: claude-opus-5-5 on issue #1169 (fix) → pass, 0/0/2, 1 model-attributed (Minor 2), DoD 11/11.
See docs/reviews/model-scorecard.md.

### Recommendation
Pass. Note the gateway test under #1142 (Minor 2).
