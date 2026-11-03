## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1385 fix, re-review, model: claude-opus-5-5)

This report reviews commit `339c6c5b`, `fix(lake): a refused catalog save no longer makes a client run its statement
twice`, on `origin/main` `0db817a6`. It replaces `487f1f1d`, which the first review
(`docs/reviews/issue-1385-fix-claude-opus-5-5.md`) sent back for one Blocker. `git diff 487f1f1d 339c6c5b` touches only
`internal/dataplane/dataplane.go` (3 lines in) and `internal/dataplane/upstream_test.go` (35 lines in).

The Blocker is gone, and nothing the first review marked correct has regressed. The new line changes two other things on
the ingress, and neither is a defect for a real Lake's client (see below). The pass holds if the gate and the duckdb lane
are green on this commit: they belong to the session that owns the branch, and their results were not in hand.

### ✅ The Blocker is gone

A real Quack client and a real engine (`funcd/runtime-duckdb`), the real `dataplane.Handler` and the real
`gateway.NewLakeProxy`, the ingress at path `/`, saves refused, one `INSERT` on a kept-alive connection. The first review
tried the third row with an overlay; here it is this commit:

| Code | Client reaches the proxy directly | Client comes through the ingress |
|---|---|---|
| main `0db817a6` | 2 rows, `Server returned nothing` | 1 row, `Request returned HTTP 503` |
| first version `487f1f1d` | 1 row, `Transferred a partial file` | 2 rows, `Server returned nothing` |
| this commit `339c6c5b` | 1 row, `Transferred a partial file` | 1 row, `Transferred a partial file` |

- **Behind the production chain.** With every wrapper on, in the order of `pkg/funcd/funcd.go` and with compression on, the
  same client gave 1 row in 15 runs of 15: five each through the chain, the bare handler and the direct path.
- **By size.** A raw client through the ingress at 9, 1000, 2047, 2048, 4096, 8192 and 204800 bytes gets status 200, all but
  the last byte, and `unexpected EOF`. The first review's empty range is gone. A real Lake proxy behind the chain, from 2
  bytes to 200 KiB, never gave an empty reply and kept the edge's header. A gzip client gets a partial gzip stream there.
- **One byte.** Only the timer's flush sends the headers here, as on the direct path, so this is not deterministic by
  construction. Through the ingress I measured 0 empty replies in 400 rounds at the default `GOMAXPROCS` and 0 in 400 at 1.
  The smallest real answer seen is 19 bytes, and headers alone stop the re-send. No finding.

### ✅ The new test

Without the line (`go test -overlay` with main's `dataplane.go`), `TestIssue1385_ABrokenUpstreamAnswerReachesTheCallerBegun`
fails with the message "the caller got an empty reply". With the line it passes 20 runs of 20 under `-race`.

**Mutants of the line**, against the whole of `internal/dataplane`:

| Mutant | Result |
|---|---|
| `FlushInterval` left at 0, as on main | only the new test fails |
| `FlushInterval` 1 h | only the new test fails |
| `FlushInterval` 50 ms | only the new test fails |
| `FlushInterval` 1 ms | only the new test fails |

The test reads with `http.Post`, not the raw kept-alive client that the first review suggested. That is enough: `http.Post`
returns an error on an empty reply, and this hop owes only the beginning of the answer. The re-send itself is pinned by the
real-engine test `test_client_sends_a_statement_again_after_an_empty_reply`.

### ✅ What the line changes on the ingress

**The same on main and on this commit** (status, headers, bytes, and the connection kept alive). Behind the chain with a gzip
client, the framing in item 2 below is the one difference:

- **The 503s.** The Lake proxy's 503 for an empty answer with a refused save (142 bytes of problem+json), and its 503 with the
  engine down (143 bytes), 100 rounds each through the handler and through the chain. The data plane's own 503 with nothing to
  dial (142 bytes), through the handler.
- **A 502.** An upstream's 502 with `Retry-After`, 100 rounds through the handler and through the chain. The data plane answers
  no 502 of its own: `serveUpstream` answers 503. The comment on `TestScenarioDataPlaneUpstreamUnreachable` still calls this
  "the 502 path"; its assertion accepts any 5xx. This wording predates the fix.
- **The kept header.** An upstream that also sets `X-Request-Id` and `Vary`: `X-Request-Id: edge-id` alone in 300 rounds of 300,
  and both `Vary` values behind the chain. `KeepEdgeHeaders` acts in `ModifyResponse`, and the data plane's refusals go through
  `ErrorHandler`, both before `copyResponse` starts the flush timer.
- **Other answers.** 200 with a length, with a big length and empty, 204, 304, HEAD and 404, 30 rounds each through the handler.
  An answer with a `Content-Type` at 2 bytes and at 1 MiB, 300 and 20 rounds, through the handler, through the chain and through
  the Lake proxy.
- **The tests of `internal/dataplane`**, unedited, pass under `-race`, among them `TestIssue417_…`, `TestIssue720_…`,
  `TestScenarioEdgeUpstreamBurstReusesConnections` and `TestUpstreamFailureProblemHidesUpstreamAddress`.

**An answer through the ingress still waits for its save.** With the save call held, answers of 1, 2 and 9 bytes reach the
caller as headers with their `Content-Length` and no body byte, and an answer of 5000 bytes as 3976 bytes. Each completes,
whole, only after the save answers.

**Two things differ. Neither is a finding:**

1. **An answer with a known length and no `Content-Type` can lose the sniffed type.** net/http sniffs on the first write, so a
   flush that comes first sends the headers without a type. The same flush on the Lake proxy, from the first version, does this
   too. Main always sniffs. This commit sent no type as follows:

   | Answer | Ingress, bare | Ingress, chain | Lake proxy alone |
   |---|---|---|---|
   | 2 bytes | 288 of 300 | 286 of 300 | 300 of 300 |
   | 5000 bytes | 26 of 200 | 16 of 200 | 10 of 200 |
   | 1 MiB | 3 of 20 | 2 of 20 | 0 of 20 |
   | a 500 of 14 bytes | 195 of 200 | 194 of 200 | 200 of 200 |

   Main already has this race on a chunked answer: a 30-round run on main showed both forms there. No real Lake answer reaches
   it. In a real engine's session with a failing statement, 13 answers of 13 carry `application/vnd.duckdb`; `GET /` carries
   `text/plain`; its 404s have no body; and the Lake proxy's and the data plane's own answers are problem+json.

2. **A compressed answer's framing.** With compression on and a gzip client, an answer whose compressed form fits net/http's
   buffer had a computed `Content-Length` on main and is chunked on this commit. The status, the other headers, the decoded
   bytes and the kept-alive connection are the same, from 2 bytes to 1 MiB and on the refusals above. `shape.go` already treats
   the gzipped length as unknown, and an answer that outgrows the 2 KiB buffer is chunked on main as well (read, not run). The
   real client completes its statements through the chain with compression on; I did not check whether it asks for gzip.

**Cost.** The line applies to every Lake on the ingress, also one with `lake.saveInterval`, whose proxy does not flush. Main
against this commit on a shared host: 1 KiB 102 and 98 µs per call, 64 KiB 519 and 591 MB/s, 16 MiB 999 and 1090 MB/s. No cost
is visible. A run of the chain under `-race` found no data race between the timer's flush and the wrappers.

### ✅ Unchanged from the first review, rerun on this commit

**The regression test on the Lake proxy.** With main's `proxy.go`, `TestIssue1385_ARefusedSaveBreaksAnAnswerThatBegan` fails with
"the client got an empty reply". With the fix it passes 20 runs of 20 under `-race`. Its mutants fail it as they did: `FlushInterval`
unset, 1 h, 1 ms and 50 ms. A failed release that drops the bytes it holds (`return 0, rerr`) fails it and
`TestHeldTailGivesTheLastByteAfterTheRelease`.

**The cause.** A real Quack client against a stand-in proxy that counts the POSTs carrying the `INSERT`. The two rows with only
the first save refused are new to this table:

| How the failed answer ends | POSTs | Rows | Client's error |
|---|---|---|---|
| reused connection, nothing, then a close | 2 | 2 | `Server returned nothing` |
| reused connection, nothing, then a TCP reset | 2 | 2 | `Failure when receiving data from the peer` |
| reused connection, nothing, only the first save refused | 2 | 2 | none |
| fresh connection, nothing, then a close | 1 | 1 | `Server returned nothing` |
| reused connection, headers, then a close | 1 | 1 | `Transferred a partial file` |
| reused connection, all but the last byte, then a close | 1 | 1 | `Transferred a partial file` |
| reused connection, all but the last byte, then a TCP reset | 1 | 1 | `Failure when receiving data from the peer` |
| reused connection, headers, only the first save refused | 1 | 1 | `Transferred a partial file` |

The last row settles the first review's Minor on the fix's side: where main can duplicate a row silently, the client now sees an
error and one row. `TestTheBarrierRestsOnThreeQuackFacts` passes on the real engine, and the answers of one real `INSERT` are
still 19, 83 and 98 bytes, small enough for a write buffer to hide.

**The flush on the Lake proxy.** `proxy.go` has not changed since the first review. On this commit `TestScenarioRefusedSaveIsAnError`
passes. A refused save by size leaves every reply with its headers. Small answers show 0 body bytes before the save answers,
and then arrive whole. The one-byte race gave 0 empty replies in 400 rounds for each of three cases, at the default `GOMAXPROCS`
and at 1.

**Siblings.** The four `ReverseProxy` builders outside the tests all set `-1` now: `internal/activator/activator.go:502`,
`internal/gateway/embedded/embedded.go:55`, `internal/lake/gateway/proxy.go:75` and `internal/dataplane/dataplane.go:352`. Only
the Lake reaches `serveUpstream`: `internal/services/lake/reconcile.go:381` is the one place that sets a `CompiledRule.Upstream`.

**Scope.** Every hunk serves the issue, and the two new hunks add lines only. No test was weakened.

**ADRs.** No ADR file was edited, and the fix contradicts none. ADR-0279 Decision 3 now holds on the ingress as well. ADR-0138
says nothing about flushing, and the comment at `dataplane.go:323` that calls this path "Streaming-native" is now true.

**Reuse and conventions.** `FlushInterval = -1` is the idiom of the three siblings. The test repeats the inline setup of its file,
which has no helper for it (`router.New()` appears 11 times). The new comment gives the why, and nothing else was added.

**Shape.** `fix(lake):`, `Fixes #1385` and the attribution trailer are there, in one commit for one issue, and the body now names
the proxy in front of a Lake's ingress and both Go tests.

**Checks run here.** `go test -race -count=1` on `internal/dataplane` and `internal/lake/gateway`: ok. On `internal/activator`,
`internal/edge/...` and `internal/gateway`: no failure. `go vet` with no tag and with `dev`, `e2e` and `integration`: clean. The
Linux build and vet: ok. `golangci-lint` on the touched packages: 0 issues. `gofmt`: clean. The lane guards (`go test -run Lane` on
`cmd/funcd`, `internal/testkit/langmod` and `scripts`): ok.

### Not run here

`scripts/agent/gate.sh`, the Lima lanes, `go test ./...` and the e2e suite belong to the session that owns the branch, and their
results were not in hand when this report was written. The real-engine suite was not rerun beyond
`TestTheBarrierRestsOnThreeQuackFacts`; the probes above run on its harness. The lane case
`issue1385-a-refused-save-runs-a-statement-once` was read, not run, and it is unchanged since the first review.

### Definition of Done

11 of 12 hold:

1. A `TestIssue1385_…` test reproduces the behavior on each path: yes.
2. Each fails on the pre-fix code for the reported reason: yes.
3. Each passes with the fix under `-race`: yes.
4. Mutating the key lines fails a test: yes.
5. The cause the issue names is fixed, not masked: yes, on both paths.
6. Scope, with no test weakened: yes.
7. No ADR contradicted or edited: yes.
8. Build, vet, lint, tests, e2e and the lane: **not known yet**. The touched packages are green; the gate and the lane are pending.
9. Conventions: yes.
10. Reuse: yes.
11. Commit shape: yes.
12. Every case fixed and tested, no sibling left: yes.

### Recommendation

Hand back to `/fix` Step 8 once the gate and the duckdb lane are green on this commit. No rework is needed. Two points for the
owner, neither scored:

- **The lane case and ADR-0312.** `issue1385-a-refused-save-runs-a-statement-once` applies `e2e/fixtures/lake-saves-refused.yaml`,
  as `issue1233-failing-saves-show` does. The Proposed ADR-0312 changes that fixture, and its plan names only the issue1233 case.
  Whichever lands second must keep both cases working.
- **A lane case through an ingress** remains the decider's choice, as the first review said. The committed tests pin each hop
  apart, and only this review's probes ran both hops with a real client.
