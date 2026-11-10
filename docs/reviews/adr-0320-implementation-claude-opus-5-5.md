## Verdict: changes requested — 0 blockers, 2 majors, 2 minors  (ADR-0320 implementation, model: claude-opus-5-5)

This report reviews branch `feat/kv-conditional-writes` on `origin/main` 7586d793: the acceptance commit 0437d6d3 and the
implementation commit 6c2582f4. The reviewer is a subagent that did not write the work. The drivers, the gateway's rule, the
timeline, the sweep, the local API, the backups, the quarantine and both shims do what the ADR says, and every check below exits
0. Two findings stop the stamp. A winning write can get no version when it shares a txn with a write whose key reads absent at
the readback. And no test pins two rules that the design model's own mutants guard: the expiry's rounding up, and the sweep's
check that a key is not live again. The ADR stays `Reviewing` and F234 stays `reviewing`. By the decider's instruction this review
ran no `scripts/agent/gate.sh`, no `just ci`, no e2e suite and no lane; the items that need them are pending, not findings.

### 🔴 Blockers

None.

### 🟡 Majors

**1. A winner batched behind a write whose key reads absent gets no version** · attribution: `model` · home: here.

`release` reads one version for the whole txn. `readVersion` (`internal/kvstore/badger/badger.go:384-410`) takes the first
versioned write that stored (`:386`), reads its key with `txn.Get` in a new `View`, and gives that commit version to every
versioned winner of the txn. When that key reads absent, `n` stays 0 and no winner gets a version (`:405`): each one gets
`Result{Done: true, Version: ""}`. Badger's `Get` reads a key whose expiry has passed as absent, and the comment at `:369` names a
DropPrefix as a second case.

Reproduced on the branch with `go test -overlay` scratch tests, not committed:
- In one gateway batch, a write with `Write{ExpiresAt}` at the current second, then a put-if-absent of another key: the
  put-if-absent returns `{Done:true Version:}`. The first write is what the quarantine's copy writes
  (`internal/services/kv/quarantine.go:231`) for a key whose expiry passes between its `GetEntry` and its commit.
- Through the public `PutIf`: one goroutine writes such copies while 16 goroutines each put-if-absent 200 distinct keys. 177 of
  the 3200 winning claims return an empty version.

In production the first write needs a key that reads absent right after its commit: a quarantine copy (a boot reclaim's move, or
an admin's return) of a key at the edge of its expiry, or a write that waits in the gateway's queue and commit for longer than its
TTL. The race is narrow, but its cost falls on winners that did nothing wrong. The local API then answers 201 or 204 with no
`ETag` (`internal/workernode/local/kv.go:150-163`), and both shims return `""` for the win. A caller that tests the result for
truth takes its win for a loss. No caller can renew or release with the version it was given, and a caller without `kv::read`
cannot read the version back. The claim then stands until its TTL ends, or for ever when it has none.

It breaks Decision 3 ("A winner's version is read back from its key after the commit"), Decision 6 (each win answers "with the
new version's `ETag`") and the `Result` contract (`internal/kvstore/kvstore.go:64-65`: when the write is Done, Version "is then
the key's new version"). Nothing accepts it: ADR-0320 read in full (its Consequences and Open questions included), the spec's
*Left to tests* list, the behavior rows, the issues (searched for the readback and the conditional write's `ETag`) and the
board's cards say nothing of a winner without a version.

Fix (builder): read the version from an entry that Badger does not hide (an all-versions read, as `newest` does, finds an
expired entry's version), and fall back to another winner's key when one has no entry; then add a gateway test that batches a
copy whose expiry has passed before a claim.

**2. No test pins the two rules that the spec's `floor-expiry` and `sweep-by-name` mutants guard** · attribution: `model` · home:
here.

The ADR's *Done* says that reverting each Decision's key line fails a test. For these two lines it does not: each mutant below,
run as an overlay, passes every test of `./internal/kvstore/...`, `./internal/services/kv/`, `./internal/workernode/local/` and
`./internal/backup/workload/`. In the spec the first fails `OneHolder` and the second fails `SweepSparesALiveKey`.

- **The expiry's rounding up** (Decision 4; `kvstore.ExpiresAt`, `internal/kvstore/kvstore.go:116-126`). Without `sec++`
  (`:123`) a key expires at the floor second, so a 1 s key written at x.9 lives 0.1 s: the Alternatives' `floor-expiry`. The
  `expiry` subtest asserts `ExpiresAt >= floor(before + 1 s)` (`internal/kvstore/kvstorecontract/contract.go:355`), which the
  floor also meets. Scenario `ttl-expires-a-key`'s "lives at least 1 s" therefore has no test that sees it break. Smallest
  change: a table test of `ExpiresAt` at a second's boundary and just after it.
- **The sweep keeps a key that is live again** (Decision 5, "It deletes a key with no live entry"; `reapable` in `applyReap`,
  `internal/kvstore/badger/badger.go:296-324`). A reap that deletes the key whatever its newest entry, the spec's
  `sweep-by-name`, passes. The `sweep` subtest's renewed key (`contract.go:405-413`) never reaches the sweep, because its renewal
  already dropped its index key (`badger.go:264`). The case that needs the check is a lease that expired and was claimed again
  before the sweep: the old index key is then due and names a live key. A scratch test of that case keeps the new holder's key
  on the real code and deletes it on the mutant, which would let a third claimant take a held key. The ADR names
  `TestConditionalContract` and the Badger driver's `conditional` run as the oracles of `SweepSparesALiveKey`; the memory
  driver's run checks it, but the Badger run does not reach it. Smallest change: that case in `RunConditional`'s `sweep`.

### Minors

**1. Four more stated rules pass every test when broken** · attribution: `model` · home: here. Each mutant ran as an overlay over
the packages above; the fourth ran over `./internal/platform/config/` and `./cmd/funcd/`. The code holds each rule, but no test
would see it break.
1. Decision 4 and the last clause of scenario `renewal-moves-the-expiry`, "one with no TTL leaves it none": a compare-and-set
   without a TTL that keeps the key's expiry (three lines before `badger.go:264`) passes.
2. Decision 6, "a lost write is charged nothing": `settle(err == nil)` in place of `settle(err == nil && wrote)`
   (`internal/services/kv/kv.go:189`) passes. `TestSweepFreesQuotaAtOnce` says that it checks this
   (`internal/services/kv/conditional_test.go:22`), but its lost put-if-absent would replace a value of the same size, so a
   charge changes nothing. A lost compare-and-set of an absent key, then a write that fits only if the loser was not charged,
   would see it.
3. Decision 8, "A load leaves out a record whose expiry passed": removing the skip from the Badger driver's `LoadPrefix`
   (`badger.go:659-661`) passes, because the expired record is absent to `List` anyway. What differs is the record's index key,
   its change record and the sweep's later delete, and a test can count those.
4. The Contracts' "`kvstore.sweepInterval` ... at least 100ms": removing the bound check
   (`internal/platform/config/kvsweep.go:30-33`) passes. Other duration keys have such a test (`cmd/funcd/duration_test.go`).

**2. Three wording slips** · attribution: `model` · home: here.
- `pkg/funcd/kv_sweep_internal_test.go:37` says that the provider runs the sweep "from its Start"; it runs from `Run`, the
  provider's Runner.
- `internal/services/kv/sweep.go:26` and `internal/services/kv/conditional_test.go:67` cite ADR-0290 Decision 4 for the wait for
  a held namespace. ADR-0320 Decision 5 cites ADR-0222 Decision 8, and ADR-0290 Decision 4 only lists the per-namespace runners
  as unchanged.
- The new `e2e/README.md` row (`:21`) cites no ADR, while every other row cites the ADRs that its suite proves.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **Commands, each exit 0**, all through `scripts/agent/d`:
  - `go build ./...`; `go vet` on the touched packages, and `go vet -tags e2e ./pkg/funcd/`;
  - `go test -race -count=1 ./api/fault/ ./internal/backup/workload/ ./internal/kvstore/... ./internal/platform/config/
    ./internal/services/kv/ ./internal/snapshot/... ./internal/workernode/local/ ./pkg/funcd/ ./cmd/funcd/`;
  - `go tool golangci-lint run` on the same packages and `./scripts/...`, and with `--build-tags e2e` on `./pkg/funcd/...`: 0
    issues each;
  - `just check-specs`: 17 specs, 0 failing. `kv-claims.fizz` passes, each of its seven mutants fails the assertion it names,
    and `kv-claims.md` is current; `quarantine.fizz` passes with its new note. Each of the six assertions has its oracle marker;
  - `just check-behavior`, `just check-hygiene`, `gofmt -l` on the changed Go files (no file listed), `go mod verify` and
    `go mod tidy -diff`;
  - the lane guards, `go test ./scripts/ ./internal/testkit/langmod/` and `go test -run 'Example|Lane' ./pkg/funcd/`.
- **Revert checks: 20 of 26 overlay mutants are killed**, each by the tests named:
  - no txn split: `TestGatewayLostConditionIsItsOwnResult` and `TestGatewayVersionNamesOneWrite`; a condition never checked: the
    Badger driver's `conditional` run; no readback at all: both, and the races;
  - a DR export that keeps the timeline: `TestRestoreTakesANewTimeline`; a version without the key: `TestVersionIsAnOpaqueHash`;
  - a sweep blind to compaction, and a sweep that writes no delete record: `TestSweepDeletesWhatCompactionRemoved`; a delete of
    an expired value with no record: `TestDeleteOfAnExpiredKeyWritesItsRecord`;
  - a plain write, a versioned write or a DropPrefix that keeps the index key: `TestEveryWriteDropsTheIndexKeyItReplaces` and
    `TestRenewalKeepsOneIndexEntry`;
  - no quota report: `TestSweepFreesQuotaAtOnce`; the hold ignored: `TestSweepWaitsForAHeldNamespace`; a Runner that runs
    nothing: `TestKVProviderSweepsExpiredKeys`;
  - 409 in place of 412: the three local API scenario tests; `?ttl` ignored: `TestScenarioKVTTLOverLocalAPI`;
  - a change record without the expiry: `TestCDCRecordCarriesExpiry`; an export without it: `TestKVStores_CarryEachKeysExpiry`;
    `Format` 2: `TestReadManifest_RefusesAnotherFormat`; a quarantine copy without it: `TestQuarantineKeepsAnExpiry`.

  The six that survive are Major 2 and Minor 1.
- **The gateway's rule** (`update`, `badger.go:229-246`). A txn ends before a request for a key that it already holds when either
  request is versioned, so a versioned write reads the committed key. A lost condition stages nothing and is its own result. A
  request that Badger refuses still fails alone, and a split batch commits in order.
- **The index and the sweep.** A write over an expired entry leaves that entry's index key to the sweep, which drops it and keeps
  the key (the scratch test of Major 2 passes on the real code). The index therefore holds one key per live key with an expiry,
  plus the due ones. The sweep deletes through the gateway, publishes a delete, writes the delete record when a value or no entry
  is left, tells the quota, and waits for a held namespace; a quarantine key waits only for the platform's hold (`Hold.HeldIn`).
- **Contracts.** `Entry`, `Write`, `Result`, `Conditional`, `Sweeper`, `ExpiresAt`, `NewTimeline`, `Version`, the facade's verbs,
  `RunSweep`, `EncodeKV` and `DecodeKV`, `Format` 3, `snapshot.Record.ExpiresAt`, `fault.PreconditionFailed` (412),
  `CDC.OnWrite` with the expiry, `changeRecord.ExpiresAt`, the config key and the provider's `Run` match the ADR's text. The local
  API's statuses and its eleven 400 refusals match Decision 6, and a PUT's refusal comes before its body is read. The extra
  exported helpers (`Write.ExpiryOf`, `Expired` and `CheckWrite`) live in the internal port package.
- **Shims.** funcd-python v0.20.0 and funcd-typescript v0.25.0 are released, and their PRs (pyvvo/funcd-python#120 and
  pyvvo/funcd-typescript#101) merged with every check green. Their verbs, their loss values and their docs match Decision 10 and
  the Contracts' table.
- **The real platform.** Experiment 0018's run at b3fb383 used this code: the code in its `funcd.patch` equals the implementation
  commit's, but for one doc comment and one test line's alignment, and it pins the shims through its own patches instead of
  `go.mod`. On a Linux guest with a real funcd, its `kv-claim` lane passed both cases, and the e2e scenario passed. The decider's
  own lane run is in progress.
- **Scenarios.** Each of the 14 scenarios has its named, un-skipped test with its `scenario:` marker, or, for
  `two-functions-race-a-claim`, the lane. The one changed assertion of an older test
  (`TestScenarioBaseDriverHasNoDurabilitySideEffects`) now names the timeline key and still refuses every CDC or backup key. The
  e2e scenario calls `t.Parallel()` and uses a 1 s TTL.
- **Tracking.** The implementation commit changes only the ADR's status line, and F234 reads `reviewing`. The eight
  `Superseded in part by` lines and the blueprint's KV lines came with the acceptance commit. The seven rows of the ADR's
  *Behavior rows* section are in `docs/behavior/services.md`, each `decided` and citing its Decision and test. Only `go.mod` and
  `go.sum` name the shims' versions, and the change adds no dependency.

### Definition of Done

16 of 19 hold: seven of the eight Review-checklist items and nine of the ten generic items. Misses: checklist item 7, the local
API's `ETag`, through Major 1 (`model`); the ADR's *Done* line, whose "reverting each Decision's key line fails a test" fails
through Major 2 and Minor 1 (`model`), and whose gate part is pending. Pending: the generic full suite (`just ci`), which
`scripts/agent/gate.sh` runs after this review.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0320 (implementation) → changes-requested, 0/2/2, 4 model-attributed, DoD 16/19. See
docs/reviews/model-scorecard.md.

### Recommendation

Fix the readback of Major 1 with its test, add the tests of Major 2 and Minor 1, and correct the three wording slips. A second
round then reruns these mutants and the readback probe, and can stamp `Implemented`. The PR carries `Fixes #1424`.
