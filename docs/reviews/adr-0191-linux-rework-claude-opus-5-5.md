# Review: Linux rework of the write-stall change (ADR-0191)

- Branch: `feat/edge-response-write-deadline`, commit `b4ba565d` (amended single commit; the pre-rework commit is `9084dfb3`).
- Verdict: **pass**. The stated cause is right, the fix changes only test conditions, no assertion was loosened, and a
  broken mechanism still fails tests on Linux. Two documentation follow-ups (below) do not block.

## What changed

`git diff 9084dfb3 b4ba565d`: tests only, plus the new helper `internal/testkit/sendbuf` (27 lines).
`internal/gateway/writestall.go` and `pkg/funcd/funcd.go` are unchanged, so `mechanism_changed: false` holds.

- `internal/testkit/sendbuf`: a listener that sets `SO_SNDBUF` to 64 KiB on each accepted connection. Used by
  `serveListener` (dataplane) and `stallServer` (gateway). With `StartTLS`, httptest wraps this listener, so the TLS
  case gets the fixed buffer too.
- The stalled client's read buffer is now 64 KiB instead of 4 KiB, in `stalledClient` and in the e2e test.
- The static asset is now `bigBody` (32 MiB) instead of 4 MiB.
- Not changed: `requireCut` (10 s read deadline, no timeout allowed, `n < body`), `requireWhole`, the `TestWriteStallLiveClientNotCut`
  assertions (`NoError`, `> 3 S`, 30 s cap), `TestWriteStallCutsStalledWrite` (still a 4 KiB read buffer), and every sleep and S value.

## Evidence

All Linux runs used Docker through colima (Linux 6.8, go1.26.4 linux/arm64, `--user 1000:1000`) on a detached worktree
of this branch, removed after the runs.

| Run | Platform | Result |
|---|---|---|
| Old tests (`9084dfb3` test files via `-overlay`) | Linux | FAIL, as in CI: `TestScenarioStalledStaticAssetIsCut` (12.60 s), `TestScenarioStalledReaderIsCut` by name and by route, each at `writestall_test.go:102` with "the stalled connection is still open: ... read tcp ...: i/o timeout"; `TestWriteStallLiveClientNotCut` (0.31 s) at `:176` with "write tcp ...: i/o timeout" |
| New tests, `-race -count=3` | Linux | ok: dataplane 16.5 s, gateway 8.4 s; 48/48 PASS (16 tests or subtests × 3) |
| New tests, `-race -count=3` | macOS | ok: dataplane 9.5 s, gateway 7.0 s; 48/48 PASS |
| `go vet` (also `-tags e2e ./pkg/funcd/`), `golangci-lint` on sendbuf, gateway, dataplane and pkg/funcd | macOS | clean, 0 issues |

### Cause attribution (extra run: new tests with the `sendbuf` line removed, `-count=2`, Linux)

- Dataplane `TestScenarioStalledReaderIsCut` (all three subtests) and `TestScenarioStalledStaticAssetIsCut`: **PASS**
  with the server send buffer autotuned. The 64 KiB client read buffer alone fixes the "still open" failure. This
  confirms that the slow drain after the cut, caused by the 4 KiB read buffer, was the cause, and not a cut that failed to happen.
  This is also the exact condition of the e2e test `TestScenarioStalledReaderIsCutRealChain`: the production listener with an
  autotuned send buffer and a 64 KiB read buffer. The e2e test was not run here because it needs the shim rig.
- Gateway `TestWriteStallLiveClientNotCut`: **FAIL** both times at 0.31 s with "write tcp ...: i/o timeout". The fixed
  send buffer is what fixes it. This matches the report: on Linux the writer wakes only after about a third of an autotuned
  multi-MiB buffer drains.

### Mutants (overlay on `internal/gateway/writestall.go`, Linux, `-race -count=1`)

| Mutant | Failing tests | Caught |
|---|---|---|
| `WriteStall` returns `next` | `TestScenarioStalledReaderIsCut` (by_name, by_route, http2), `TestScenarioStalledStaticAssetIsCut`, `TestWriteStallCutsStalledWrite`, `TestWriteStallArmSequence` | yes, by both scenario and unit tests |
| deadline never cleared (`clear()` does nothing) | `TestScenarioQuietStreamNotCut/http2`, `TestWriteStallArmSequence` | yes |
| one deadline per `Write` (`stallChunk = 32 MiB`) | `TestWriteStallLiveClientNotCut` (0.31 s), `TestWriteStallArmSequence` | yes; the 64 KiB send buffer keeps the live-client test sensitive to per-piece deadlines |

The quiet-stream http1 subtest passes under "never cleared". This is expected: on HTTP/1.1 a deadline matters only during a write,
and `arm()` refreshes it before each write. The http2 subtest covers that case.

## Findings

1. **Minor (documentation sync, not optional).** The ADR-0191 draft (`docs/adr/0191-response-write-stall-timeout.md`,
   not yet published) was `Proposed`, so it was still editable, and it disagreed with the tests:
   - The scenario `stalled-static-asset-is-cut` says "a static Route serving a 4 MiB asset". The test serves 32 MiB.
   - Implementation step 1 says "A on a raw `net.Conn` with a 4 KiB read buffer". The test uses 64 KiB and fixes the server send buffer at 64 KiB.
   - Step 2 (`TestWriteStallLiveClientNotCut`) does not mention the fixed 64 KiB server send buffer.

   Apply the three wording edits the rework proposed before acceptance. A scenario's *Given* that differs from its test is
   a conformance gap at the implementation-review gate.
2. **Note (coverage scope; no action required in the code).** `TestWriteStallLiveClientNotCut` now proves the per-piece
   deadline with a 64 KiB send buffer, not with the Linux default. The extra run shows that with an autotuned loopback send
   buffer, a client reading about 2 MiB/s is cut at S = 300 ms. In production, on Linux, a steady reader is therefore
   cut when it frees less than about a third of the connection's send buffer within S (60 s). For loopback peers, the
   buffer can reach the `tcp_wmem` maximum. The ADR's Consequences mention this only in general terms ("depends on socket buffers"). Consider
   stating the one-third rule there while the ADR is still Proposed.
3. **Nit.** The `stalledClient` comment "would hold the drain after the cut to the sender's zero-window probes" reads
   awkwardly. "would limit the drain after the cut to the sender's zero-window probes" says the same thing more clearly.

## Checks on the work itself

- The commit diff has no absolute OS path, local username or personal email (grepped). The work tree was left clean;
  nothing in the work was edited.
- The `sendbuf.Listener` type assertion `c.(*net.TCPConn)` is unchecked. This is fine for the httptest TCP listeners that
  use it. The package is test support, and only `_test.go` files import it.

_Follow-up: findings 1 and 2 were applied to the ADR before its acceptance._
