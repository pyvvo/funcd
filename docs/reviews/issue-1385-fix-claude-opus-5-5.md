## Verdict: changes requested — 1 blocker, 0 majors, 1 minor  (issue #1385 fix, model: claude-opus-5-5)

This report reviews commit `487f1f1d`, `fix(lake): a refused catalog save no longer makes a client run its statement
twice`, on `origin/main` `0db817a6`. It touches `internal/lake/gateway/proxy.go`, `internal/lake/gateway/barrier_test.go`,
`images/runtime/duckdb/engine_test.py` and `e2e/duckdb.venom.yml`.

The fix works on the path it names, a client that reaches the Lake proxy directly. But it moves the same double commit
onto a Lake's ingress, where main gave a clean 503.

### 🔴 Blocker 1 — The fix moves the double commit onto a Lake's ingress  ·  attribution: model

A client outside the node reaches a Lake through the data plane's `serveUpstream` (`internal/dataplane/dataplane.go:327`,
ADR-0138), then the Lake proxy. `serveUpstream` builds its own `httputil.ReverseProxy` (`:347`) with no `FlushInterval`.

- **On main**, the Lake proxy broke the connection with no byte sent. The data plane's `RoundTrip` failed, and its
  `ErrorHandler` answered a whole 503.
- **With the fix**, the Lake proxy's headers have gone out, so the data plane has already called `WriteHeader`. It buffers
  the small answer, its body read then fails, and `http.ErrAbortHandler` drops the buffer. The outside client gets an
  empty reply, which is the defect of #1385.

Evidence, with a real Quack client and a real engine (`funcd/runtime-duckdb`), the real `dataplane.Handler` and the real
`gateway.NewLakeProxy`, the ingress at path `/` (the default for an ingress with a host, `v1.IngressPath`), saves refused,
one `INSERT` on a kept-alive connection:

| Code | Client reaches the proxy directly | Client comes through the ingress |
|---|---|---|
| main `0db817a6` | 2 rows, `Server returned nothing` | 1 row, `Request returned HTTP 503` |
| this fix | 1 row, `Transferred a partial file` | **2 rows, `Server returned nothing`** |
| this fix plus `proxy.FlushInterval = -1` in `serveUpstream` | 1 row, `Transferred a partial file` | 1 row, `Transferred a partial file` |

A raw client with a stub engine gives the same picture by answer size, through the ingress with the fix: 9, 1000, 2047 and
2048 bytes get an empty reply; 4096 bytes and more get a partial answer. The answers of one real `INSERT` were 19, 83 and
98 bytes, so a real statement is in the empty range.

**The fix, for `/fix`:** set `proxy.FlushInterval = -1` in `serveUpstream`, and add a `TestIssue1385_…` test in
`internal/dataplane`: a stub upstream that sends its headers and all but the last byte and then breaks, read by a raw client
on a kept-alive connection. Only the Lake reaches this code: `internal/services/lake/reconcile.go:381` is the only place
that sets a `CompiledRule.Upstream`. I tried the line with a Go overlay, and every test of `internal/dataplane` still
passes. The comment at `dataplane.go:323` calls this path "Streaming-native", which is true only with that line.

### Minor

- **The issue understates the outcome** · attribution: issue · recorded, not scored. Its table shows the client getting an
  error. When only the first save is refused, the re-sent statement's save succeeds, and the client sees **no error at all
  and two rows** (real client, stand-in proxy). The duplicate is silent in that case, which raises the severity of the
  Blocker above.

### ✅ Verified correct (keep it)

**The regression test.** Without the fix (`go test -overlay` with main's `proxy.go`),
`TestIssue1385_ARefusedSaveBreaksAnAnswerThatBegan` fails with `unexpected EOF` and the message "the client got an empty
reply". With the fix it passes 20 runs of 20 under `-race`.

**Mutants**, against the whole of `internal/lake/gateway`:

| Mutant | Result |
|---|---|
| `FlushInterval` left unset, as on main | `TestIssue1385_…` fails |
| `FlushInterval` so long it never fires | `TestIssue1385_…` fails |
| `FlushInterval` 1 ms | `TestIssue1385_…` fails |
| `FlushInterval` 50 ms | `TestIssue1385_…` fails |
| a failed release drops the bytes it holds (`return 0, rerr`) | `TestIssue1385_…` and `TestHeldTailGivesTheLastByteAfterTheRelease` fail |
| `FlushInterval = -1` without a barrier too | survives; it is harmless and not a key line, so this is no test gap |

**The cause is right, and nothing contradicts it.** A real Quack client against a stand-in proxy that counts the POSTs
carrying the `INSERT`:

| How the failed answer ends | POSTs | Rows | Client's error |
|---|---|---|---|
| reused connection, nothing, then a close | 2 | 2 | `Server returned nothing` |
| reused connection, nothing, then a TCP reset | 2 | 2 | `Failure when receiving data from the peer` |
| fresh connection, nothing, then a close | 1 | 1 | `Server returned nothing` |
| reused connection, headers, then a close | 1 | 1 | `Transferred a partial file` |
| reused connection, all but the last byte, then a close | 1 | 1 | `Transferred a partial file` |
| reused connection, all but the last byte, then a TCP reset | 1 | 1 | `Failure when receiving data from the peer` |

The re-send needs a reused connection and no byte received, headers included. That fits curl's retry of a reused connection
and the issue's inference, and the error texts are curl's; I did not read curl's source. Main's Go proxy with the same client
and engine gives 2 rows, so the issue reproduces end to end. Headers alone are enough to stop the re-send.

**What the flush changes on the Lake proxy:**

- **The empty-answer 503.** Unchanged: `hold` fails in `ModifyResponse`, before `copyResponse` starts the flush timer, so
  the 503 goes out whole. `TestScenarioRefusedSaveIsAnError/an_empty_answer` passes.
- **Early completion.** None. With the save call held, answers of 1, 2 and 9 bytes reach the client as headers with their
  `Content-Length` and no body byte, and each completes only after the save answers. When the engine's last read returns
  its bytes with EOF, `heldTail` makes the save call first, so before the save only the headers are out. The real-engine
  fact that one held byte holds the answer stays pinned.
- **A refused save, by size.** At 1, 2, 3, 100, 2047–2049, 4095–4097 bytes, 32 KiB and 200 KiB, known length and chunked,
  every reply on the direct path has its headers and all but the last byte.
- **Large answers.** No meaningful cost on a shared host, main against the fix: 16 MiB about 3.9–4.0 GB/s
  both; 64 KiB 406–453 and 374–458 MB/s; 1 KiB 131–141 and 144–148 µs per call, which a real save call outweighs.
- **Scope of the setting.** It applies only with a `SaveURL`, so a Lake with `lake.saveInterval` is untouched.

**An empty reply on the direct path, after the fix:**

- **Two bytes or more:** not possible on a failed save. By then `heldTail` has handed out at least one byte, and
  `maxLatencyWriter.Write` flushes it before `copyBuffer` returns the error.
- **One byte:** only the timer's flush (`time.AfterFunc(-1)` in `copyResponse`) sends the headers, so this is not
  deterministic by construction. I measured 0 empty replies in 1600 rounds, half at the default `GOMAXPROCS` and half at 1,
  half with a refused save and half with the save endpoint closed. A real Quack answer is not one byte (the smallest seen is
  19), and headers alone stop the re-send. No finding.
- **Other paths (read, not run):** an engine failing before its headers, a failed learning call, and `Suspend` or
  `Release` all answer a whole 503. A stopping proxy (`closeProxy`) can drop an in-flight request with no byte, but the
  re-send then meets a closed listener, so nothing runs twice.

**Siblings.** The only other `ReverseProxy` builders are `internal/gateway/embedded/embedded.go:55` and
`internal/activator/activator.go:502`, which both set `FlushInterval = -1`. `internal/workernode/local/invoker.go` runs the
data plane's handler into a recorder and fails the invoke on an abort, so no client sees an empty reply. `heldTail` and
`saveBarrier` exist only in the Lake proxy. `serveUpstream` is the one sibling, in Blocker 1.

**Scope.** Every hunk serves the issue. No test was weakened: `TestTheBarrierRestsOnTwoQuackFacts` became `…ThreeQuackFacts`
and gained a fact, and no document names the old class. The lane case asserts both the client's error and the row count, so
it can tell a re-send from a plain failure (read, not run).

**ADRs.** No ADR file was edited, and the fix contradicts none. It makes the code do what ADR-0279 Decision 3 already says:
forward an answer but its last bytes, and break the connection after the headers went out.

**Reuse and conventions.** `FlushInterval = -1` is the idiom of `embedded.go` and `activator.go`. The test reuses
`barrierFront` and `newSaveStub`, and the real-engine test extends `_BarrierProxy`. The YAML is block style, the one comment
gives the why, and imports are at the top.

**Shape.** `fix(lake):`, `Fixes #1385` and the attribution trailer are there, in one commit for one issue.

**Checks run here.** `go test -race -count=1` on `internal/lake/gateway` and `internal/dataplane`: ok. `go vet` with no tag
and with `dev`, `e2e` and `integration`: clean. `golangci-lint` on `internal/lake/gateway`: 0 issues. `gofmt`: clean. The lane
guards (`go test -run Lane` on `cmd/funcd`, `internal/testkit/langmod` and `scripts`): ok.

### Not run here

`scripts/agent/gate.sh`, the duckdb lane, `go test ./...` and the e2e suite belong to the session that owns the branch, and
their results were not in hand when this report was written. The real-engine suite passed 21 tests there and was not rerun;
the probes above run on its harness.

### Definition of Done

10 of 12 hold:

1. A `TestIssue1385_…` test reproduces the behavior: yes.
2. It fails on the pre-fix code for the reported reason: yes.
3. It passes with the fix under `-race`: yes.
4. Mutating the key lines fails a test: yes.
5. The cause the issue names is fixed, not masked: yes, on the Lake proxy.
6. Scope, with no test weakened: yes.
7. No ADR contradicted or edited: yes.
8. Build, vet, lint, tests, e2e and the lane: **not known yet**. The touched packages are green; the gate and the lane are
   pending.
9. Conventions: yes.
10. Reuse: yes.
11. Commit shape: yes.
12. Every case fixed and tested, no sibling left: **no**, see Blocker 1.

### Recommendation

Back to `/fix` for Blocker 1, one line and one test in `internal/dataplane`, then a second review (`-2`). A lane case through
an ingress would need a Lake at path `/`: a Quack client whose URI held a path got `Not Found` from the route under that path.
Whether to add one is the decider's choice.
