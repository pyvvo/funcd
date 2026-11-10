## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0320 implementation, round 2, model: claude-opus-5-5)

This second round reviews commit d711baa0 on branch `feat/kv-conditional-writes`: the rework of the four findings of
[round 1](adr-0320-implementation-claude-opus-5-5.md). The reviewer is the same subagent, which did not write the work. Each
finding is resolved with a test that fails when its rule breaks, and the rework brings no new problem. The ADR is stamped
`Implemented` and F234 `implemented`. By the decider's instruction, `scripts/agent/gate.sh` and the app-todo lane run after this
stamp; this review ran neither of them, nor `just ci`, the e2e suite or a lane.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Round 1's findings, as reworked

- **Major 1, a winner without a version: resolved.** `readVersion` (`internal/kvstore/badger/badger.go:398-424`) now reads the
  txn's commit version through `newestVersion` (`:328-338`), an all-versions read that also finds an expired entry. It reads it
  from the first winner's key that still holds an entry. `TestGatewayAWinnerBehindAnExpiredCopyGetsItsVersion` batches a claim
  behind a copy whose expiry has passed, then writes such a copy alone, and each one gets its version. Round 1's readback, run
  as an overlay, fails that test with an empty version. Round 1's two probes now pass: in one batch the claim gets its version,
  and through the public `PutIf` none of the 3200 winning claims returns an empty version.
- **Major 2, the two rules that the spec's mutants guard: resolved.** `TestExpiresAtRoundsUpToTheNextSecond`
  (`internal/kvstore/expiry_test.go`) kills `floor-expiry`. The contract's `sweep` subtest now claims an expired lease again
  before the sweep (`internal/kvstore/kvstorecontract/contract.go`), so the Badger driver's `conditional` run kills
  `sweep-by-name`.
- **Minor 1, four more rules that no test pinned: resolved.**
  - The contract's `each-write-sets-its-own-expiry` kills a compare-and-set that keeps the key's expiry.
  - `TestSweepFreesQuotaAtOnce` now counts the store's measures. A charged lost write would make the next refusal measure
    again, so the test kills that charge.
  - `TestLoadLeavesOutAnExpiredRecord` kills a load that writes an expired record.
  - `TestKVSweepInterval` kills a `kvstore.sweepInterval` with no floor.
- **Minor 2, three wording slips: resolved.** The sweep test now says `Run`, `sweep.go` and `conditional_test.go` cite ADR-0222
  Decision 8, and the `e2e/README.md` row cites ADR-0320.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **Commands, each exit 0**, all through `scripts/agent/d`, on d711baa0:
  - `go build ./...`; `go vet` on the reworked packages, and `go vet -tags e2e ./pkg/funcd/`; `gofmt -l` (no file listed);
    `go mod tidy -diff`;
  - `go test -race -count=1 ./internal/kvstore/... ./internal/platform/config/ ./internal/services/kv/
    ./internal/workernode/local/ ./internal/backup/workload/ ./pkg/funcd/`;
  - the new and changed tests, five times each under `-race`;
  - `go tool golangci-lint run` on the reworked packages, and with `--build-tags e2e` on `./pkg/funcd/...`: 0 issues each;
  - `just check-specs`: 17 specs, 0 failing. `kv-claims.fizz` passes, each of its seven mutants fails the assertion it names,
    and its diagram is current. Also `just check-behavior` and `just check-hygiene`.
- **Revert checks: 27 of 28 overlay mutants are killed.** These are round 1's 26 mutants, each now killed, and two new mutants
  of the readback. The one that survives removes the fallback to another winner's key. That fallback serves only one case: a
  native DropPrefix that removes the first winner's key between the commit and the readback. No test can put a drop there
  without a seam, and the code reads correct.
- **The rework's scope.** d711baa0 changes only the files in its message, adds no dependency and leaves the ADR untouched. The
  gateway benchmark (`BenchmarkGatewayWrites`) shows no difference between the old and the new readback beyond the noise from one
  run to the next.
- **Round 1's other checks still hold.** Its *Verified correct* list stands, because the rework touches only the readback and
  tests.
- **The real platform.** The decider reports that all 15 Lima lanes, `kv-claim` included, and the app-todo lane passed on
  6c2582f4. The rework changes only the readback after a commit, which the unit tests above cover. The gate's e2e suite runs on
  the final code.

### Definition of Done

17 of 19 hold: the eight Review-checklist items and nine of the ten generic items. Two are pending for `scripts/agent/gate.sh`,
which the decider runs after this stamp. One is the ADR's *Done* line: its other parts now hold (every test passes under
`-race`, each spec mutant fails, each Decision's key line has its test, the `kv-claim` lane passed, and both shims pass). The
other is the generic full suite.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0320 (implementation, round 2) → pass, 0/0/0, 0 model-attributed, DoD 17/19. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamped `Implemented` (2026-11-10), with F234 `implemented`. The gate and the app-todo lane follow on the final code. The PR
carries `Fixes #1424`.
