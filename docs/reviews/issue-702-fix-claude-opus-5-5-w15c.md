## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #702 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i702`, one commit `7d80205e fix(kvstore): keep the KV change feed in a stream so no change is lost`
(6 files, +121/−6). Base: current `origin/main` (a394c6f1). No touched file changed on `origin/main` since the
branch's merge base, so the overlay below is the current main code.

The fix makes `cdc.Tail` declare a `KV_CDC` stream over the sink subject before it drains the outbox
(`internal/kvstore/badger/cdc.go`). With a stream covering the subject, the bus `Publish` waits for the JetStream ack, so the cursor
moves only past changes that a stream keeps. `bus.StreamConfig` gains `MaxAge`, and the NATS driver passes it through
(`internal/bus/bus.go`, `internal/bus/nats/nats.go`). If the stream cannot be declared, the tailer stops with the outbox intact,
and `RunCDC` retries.

### 🟡 Minor

- **Minor 1: the retention-to-MaxAge wiring in `Tail` is not tested.** Attribution: model. Evidence: mutant m3
  (`MaxAge: c.retention` changed to `MaxAge: 0` in `cdc.go` `Tail`) survives every `TestIssue*`/`TestCDC*` test in
  `internal/kvstore/badger` (`ok … 1.894s`). `TestIssue702_EnsureStreamSetsMaxAge` covers only the bus side. Without this
  wiring, the `KV_CDC` stream would keep every change forever, and `kvstore.cdc.retention` would have no effect. Fix: in
  `TestIssue702_LateConsumerReceivesEveryChange`, or in a small sibling test, assert that the `KV_CDC` stream's MaxAge equals the configured or default
  retention.
- **Minor 2: the meaning of `kvstore.cdc.retention` moved from the outbox to the stream.** Attribution: adr. ADR-0068
  Decision 6 comments the key as "TTL window for delivered _cdc/ entries (or min-cursor GC)". The shipped code
  already used min-cursor GC and never read the field. The fix makes the key the stream's age window and
  rewords the comment in `examples/funcdconfig.yaml`. This keeps the ADR's intent: Decision 4 describes a bounded window
  after which a consumer that was down re-syncs from a snapshot. The issue also proposed this remedy. No Decision or Contract is broken, and the
  min-cursor GC scenario (`cdc-retention-bounds-log`) still holds. This is recorded so that the follow-up consumer-attach ADR
  named in the issue can state the key's meaning explicitly. It is not a fix-blocker.

### ✅ Verified correct (keep it)

- **Prove-first on current main (the person's decision).** Overlay of the `origin/main` `cdc.go`, `bus.go` and `nats.go`,
  with the branch's tests kept:
  - `TestIssue702_LateConsumerReceivesEveryChange` fails on both `storage=0` and `storage=1`. The tailer drains, gc
    empties the outbox, and then no stream holds the 5 changes (the test's message: "no stream holds the 5 changes the tailer
    delivered and reclaimed"). This is the issue's loss: the changes were counted as delivered, reclaimed, and kept nowhere.
  - `TestIssue702_TailerStopsWhenTheStreamCannotBeDeclared` fails with `Should be empty, but was [1 2 3]`. The pre-fix
    tailer publishes to an uncovered subject.
  - `internal/bus/nats` with the `origin/main` `nats.go` overlay: `TestIssue702_EnsureStreamSetsMaxAge` fails with
    `stream MaxAge = 0s, want 1h0m0s`.
- **With the fix, under `-race`**: all three `TestIssue702_*` tests pass, without skips. Both storages are exercised on the real
  embedded NATS bus, not the `fakeBus`. The issue's own gap, "no test runs the real sink", is closed.
- **Mutants**: m1 (the stream subject changed to `c.subject + ".m"`) fails `LateConsumerReceivesEveryChange`.
  m2 (the `EnsureStream` error ignored) fails `TailerStopsWhenTheStreamCannotBeDeclared`. m3 survives (Minor 1).
- **Cause, not symptom**: the issue's root cause (no stream covers the sink subject, so `Publish` takes the core path, which
  returns nil with no subscriber) is removed at the source. The fix also fails closed, as the issue asks: no publish
  happens before the declare succeeds, and an error returns to `RunCDC`'s existing retry. It adds no timeout, retry or
  swallowed error.
- **Idempotence**: `EnsureStream` uses `CreateOrUpdateStream` and clears the coverage memo, so it is safe on every
  `RunCDC` retry pass and on restart, including a changed sink subject or retention.
- **Scope**: every hunk serves the issue. The `examples/funcdconfig.yaml` comment change follows the new meaning of the key. No test
  was weakened. The `fakeBus.EnsureStream` change only adds an error injection.
- **Reuse**: the fix reuses the existing `bus.Bus.EnsureStream` port (the remedy the issue named), the existing
  `CreateOrUpdateStream` driver path, the `RunCDC` retry loop, and the test helpers `putN`, `countPrefix` and `OpenWithSeamsFor`.
  It adds no new helper, type or dependency. `MaxAge` is an additive field on the existing port struct and maps 1:1 to
  `jetstream.StreamConfig.MaxAge`.
- **Conventions**: errors pass through the port's `mapErr`/`fault`, functions take ctx first, and no `any` appears. Imports are at the top level. The
  doc comments explain why (core publish returns nil with no listener) without narration.
- **ADRs**: no ADR file edited. The fix conforms to ADR-0068 (durable feed, zero loss across consumer death) and
  ADR-0008 (the uncovered subject stays core pub/sub, and the bus port is unchanged apart from an additive field).
- **Siblings**: outside tests, `EnsureStream` is called only by the bus itself and now by the CDC tailer. No other
  production publisher depends on durability over an uncovered subject.
- **Checks (touched packages)**: `go test -race ./internal/bus/... ./internal/kvstore/badger/` ok.
  `go vet` ok. `golangci-lint run` reports 0 issues. The repo-wide set, the Linux lint and e2e are left to the group gate.
- **Shape**: `fix(kvstore):` subject, `Fixes #702`, attribution trailer, one issue in one commit. The worktree is left clean.

### Definition of Done
12 / 12 items hold. Item 4 holds on the key lines (the declare and its error path). The survivor on the
MaxAge value is Minor 1. Item 8 covers the host checks of the touched packages, and the group gate runs the rest.

### Model scorecard
Ledger fields (not recorded here; the batch ledger PR records them): claude-opus-5-5 on issue #702 (fix) gets the verdict pass,
with 0 blockers, 0 majors and 2 minors; 1 minor is model-attributed. DoD 12/12.

### Recommendation
Pass. The fix can go to the group gate as it is. Minor 1, an assertion on the `KV_CDC` stream's MaxAge, is a small test follow-up
that the fixer can fold in. Minor 2 belongs to the follow-up ADR on how a consumer attaches to the feed.
