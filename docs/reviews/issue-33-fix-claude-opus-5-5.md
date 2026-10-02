# Issue #33 Fix Review — graceful shutdown loses open function log and trace segments

**Verdict**: **pass**. Both regression tests fail on the pre-fix code for the reported reason and pass with the fix
under `-race`. Four mutants of the fix's key lines each fail a test. A real-Node probe that follows the issue's own
steps goes from 0 of 22,801 records persisted at `Run` return (3/3 rounds) to 22,801 of 22,801 (3/3). The change
removes both causes the issue names, touches nothing else, and conforms to ADR-0081. There is one Minor, and it does
not block sign-off.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #33 · ADR-0081 (Decision "no loss on freeze/teardown", the Sink contract) · ADR-0101
(traces on the same channel) · ADR-0002 · `CLAUDE.md` style rules

The fix is one commit, `57718bd`, on `fix/202-graceful-shutdown`. The three later commits on that branch belong to
other issues and touch none of the same files. The commit touches `internal/funclog/sink.go`,
`internal/funclog/tracesink.go`, `pkg/funcd/funcd.go`, a new `pkg/funcd/logroutes.go`, and two new test files:
`internal/funclog/shutdown_test.go` and `pkg/funcd/logroutes_internal_test.go`.

## Verdict: pass — 0 blockers, 0 majors  (issue #33 fix, model: claude-opus-5-5)

### Minor
- **Minor · model — no test covers the socket half-close on a real socket.** For the containerd driver, `drain`
  depends on one claim: `CloseRead` on a worker's log socket wakes the blocked `Route`, and the socket still returns
  the queued bytes before EOF. The comment says this holds "on Linux", and Linux `AF_UNIX` stream sockets do behave
  this way. But the only test that covers the path is `TestIssue33_ShutdownDrainsLogRoutes`, which uses a fake
  `heldChannel`. On a real socket the claim is untested, and on a BSD-family kernel `SHUT_RD` can discard queued
  data. A wrong claim here would bring the loss back on the containerd path only, and no test would fail. This
  review did not run the containerd Lima lane, because a later stage owns it. **Fix**: add a small `linux`-tagged
  test that writes to one end of a real `net.UnixConn` pair, calls `CloseRead` on the other end, and reads every
  byte and then EOF. As an alternative, cover the path in the containerd funclog lane.

### Observation (not scored, outside the issue's scope)
- The containerd driver's `Close` leaves each instance's log listener and accept loop running. A connection that a
  shim opens after `drain` takes its snapshot gets a Route that `drain` does not wait for. Its records can reach
  the sink after `Close`. The window is narrow, because the daemon exits right after this point. ADR-0081 already
  accepts that a hard teardown may lose the last fd tail. This review does not count it as a finding.

### ✅ Verified correct (keep it)
- **The tests fail without the fix, for the issue's reason.** I reverted `57718bd` with `git revert --no-commit`,
  restored the two new test files, and ran `go test -race -run TestIssue33 ./internal/funclog/ ./pkg/funcd/`. The
  revert did not conflict.
  - `TestIssue33_CloseWaitsForInFlightFlush/logs` and `/traces` failed with `"[]" should have 1 item(s), but has 0`
    and the message "Close returned while a Flush was still putting its segment". This is the issue's first cause:
    a Flush takes the segment out of the map before its Put.
  - `TestIssue33_ShutdownDrainsLogRoutes` failed with `expected: 200, actual: 0` and the message "(200 were Put
    after it)". This is the issue's second cause: the Routes run fire-and-forget, so the records are Put after
    `blob.Close`.
- **The tests pass with the fix.** At HEAD `d1614a16`, `go test -race -count=5 -run TestIssue33` passed all
  5 runs of both tests in `internal/funclog` and `pkg/funcd`. Neither test is skipped.
- **The user-visible behavior is fixed.** I wrote a scratch e2e probe, kept outside the repo and added with
  `-overlay`. It follows the issue's step 1: a real Node `log-burst` function with `items: 20000`, a funclog seal
  age of 1 h, `Run` cancelled right after the 200 response, and a blob wrapper whose `Close` does nothing, so the
  records can be counted. The daemon listened only on ports 30330 and 30331.
  - Pre-fix (the pre-fix `sink.go`, `tracesink.go` and `funcd.go` overlaid, `logroutes.go` removed): `emitted=22801
    persisted-at-run-return=0` in 3/3 rounds. Three seconds later all 22,801 records were present, which is the
    late Put that the issue describes.
  - Fixed: `emitted=22801 persisted-at-run-return=22801` in 3/3 rounds.
- **The fix removes the cause, not the symptom.** `BlobSink` and `BlobTraceSink` now hold `flushing` (an RWMutex)
  for reading from the moment a Flush takes its segment until its Put returns. `Close` holds it for writing, so
  `Close` cannot return while a taken segment is still in flight. Shutdown now waits for each capture Route to read
  its channel to the end and flush before the sinks close. This also closes the issue's second facet, where lines
  appended after `Close` went into a segment that nothing flushed. No timeout was raised, no retry was added, and no
  error is swallowed. The wait uses Run's existing `shutdownTimeout` context, so a stuck channel delays shutdown by
  at most that bound and cannot block it.
- **No deadlock.** `Close` calls the unexported `flush`, so it never takes the read lock again while it holds the
  write lock. The age-flusher and the size-triggered Flush in `Append` take only the read lock, and `Close` stops the
  age-flusher before it takes the write lock. The e2e suite and the `-race` runs passed without a hang.
- **The mutants are killed.** Each mutant was applied with `-overlay`:
  - `drain` without the `CloseRead` call fails `ShutdownDrainsLogRoutes` 3/3, on the 10 s guard "Run did not return
    after ctx cancel".
  - `drain` that half-closes the channels but does not wait fails it 20/20 (`expected: 200, actual: 0`).
  - `BlobSink.Close` without `flushing.Lock()` fails `CloseWaitsForInFlightFlush/logs` 3/3.
  - `BlobTraceSink.Close` without `flushing.Lock()` fails `CloseWaitsForInFlightFlush/traces` 3/3.
- **Scope.** Every hunk serves the issue. Both Close fixes keep the same shape (the issue names the trace sink as
  affected). No test was weakened or deleted, and no other caller changed.
- **Reuse.** The funclog test reuses the package's `memBucket`, `defaultRes` and `serverSpan` helpers. `logRoutes`
  keeps each Route's reader because `drain` must half-close it, so a `sync.WaitGroup` alone would not do the job. I
  found no existing join-with-half-close helper in `internal/platform`, `internal/testkit` or the touched packages.
  The test-only wrappers (`gatedBucket`, `closeOrderBucket`, `heldChannel`, `captureRuntime`) are specific to these
  tests and duplicate no existing harness.
- **Conventions.** ADR-0002 holds: the change adds no `any` to an exported signature and no new logger. The
  contexts are passed first, and the import graph keeps `pkg/funcd` above `internal/funclog`. Imports are at the top
  level, and the comments explain why the code is shaped this way, not what each line does. The naming follows the
  surrounding code (`flush`/`Flush`, `start`/`drain`).
- **ADRs.** The fix makes the shutdown path match ADR-0081's "no loss on freeze/teardown" and the existing Close and
  Shutdown doc comments, which it updates to describe the new wait. The commit edits no ADR file.
- **Checks** (all through `nix develop -c`): `gofmt -l` on the touched packages printed nothing. `go build ./...`
  and `GOOS=linux go build ./...` passed. `go vet` passed on the host and on Linux. `golangci-lint` reported
  `0 issues.` on the host and on Linux. `just check-hygiene` reported `hygiene: clean`. `go test -race -count=1
  ./internal/funclog/... ./pkg/funcd/... ./internal/runtime/...` reported `ok` for all nine packages. The e2e suite,
  `go test -tags e2e -count=1 ./pkg/funcd/...`, reported `ok` (105 s). This review did not run a Lima lane (a later
  stage owns it), and the fix does not touch `internal/runtime/containerd`.
- **Commit shape.** The subject is `fix(funclog): …`. The body names the cause and both regression tests, and it
  ends with `Fixes #33` and the `Co-Authored-By` trailer. The commit fixes one issue.

## Recommendation
Sign off. The Minor can be fixed in a follow-up commit on this branch: a `linux`-tagged real-socket test for the
half-close claim, or a containerd funclog lane assertion. It goes back to `/fix` only if the group wants it before
the PR.
