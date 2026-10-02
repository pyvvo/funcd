## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #31 fix, model: claude-opus-5-5)

Fix under review: commit `d2be391` `fix(kvstore): commit each co-batched KV write on its own merits` on
branch `fix/200-badger-txn-size` (group tracker #200). Only this commit was reviewed; `25338d1` (#101) is
out of scope. Touched files: `internal/kvstore/badger/badger.go`,
`internal/kvstore/badger/gateway_internal_test.go` (new).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The `maxKeyBytes` facet of the root cause is left open, and the commit does not say so** ·
  attribution: `model` · The issue's Summary and Root cause name a second facet:
  `KVStoreSpec.Validate` (`api/types/v1alpha1/kvstore.go:94`) only rejects a negative `maxKeyBytes`, so a
  store may declare a cap above Badger's 65000-byte key limit, and such a key returns a 500 whose detail
  carries a hex dump of the internal `<ns>/<store>/<table>/` key. The fix removes the cross-caller part
  (the key now fails only its own write; verified below), which is what the issue's Expected behavior
  asks for. A key the store declares valid still fails with an Internal 500 and the hex dump. Closing the
  bound may need a decision, because it ties a spec field to one engine's limit (the memory engine has
  none), in the same way as #169 for `maxValueBytes`. Fix (builder): name the open facet in the commit
  body or PR and either file a follow-up issue or route it to `/adr`. Not a DoD miss: the defect the
  issue reports (one caller's write failing another's) is fixed at its cause.

### ✅ Verified correct (keep it)

- **The regression tests fail without the fix, for the issue's reason.** `git revert --no-commit d2be391`
  with the test file restored from HEAD:
  `TestIssue31_OverBudgetBatchCommitsEveryWrite` → `Received unexpected error: Txn is too big to fit into
  one request` (`write 0`), and `TestIssue31_RejectedWriteFailsAlone` → bystander `errs[0]` got `Key with
  size 70004 exceeded 65000 limit`. Package `FAIL`.
- **They pass with the fix, under `-race`.** After `git reset --hard 25338d1`:
  `go test -race -count=5 -run TestIssue31_ ./internal/kvstore/badger/` → 5/5 PASS each, `ok`.
- **The user-visible behavior is fixed** (a probe through the public `Open` path with production defaults,
  sync writes on, concurrent `Put`s, 3 rounds each, added by overlay and not committed):

  | Scenario | pre-fix (overlay of `d2be391^`) | with fix |
  |---|---|---|
  | 8 concurrent 1000 KiB puts | 7/8 failed, every round | 0/8 |
  | 64 concurrent 64 KiB puts | 45, 61, 63 failed | 0 |
  | 64 concurrent 128 KiB puts | 44, 43, 42 failed | 0 |
  | 70000-byte key + 300 concurrent 1-byte puts to another namespace | 255/300 bystanders failed, every round | 0/300 (the big key alone fails) |

- **Cause, not symptom.** The old `flush` staged up to 256 writes in one `db.Update` and sent its one error
  to every waiter. The new `commit`/`update` pair records how many leading requests staged before the
  error; that prefix commits together, the overflowing request starts the next txn, and a request that
  fails in an empty txn fails alone. No retry, timeout or swallowed error: a commit-time error still
  reaches every waiter of that txn (`n == len(batch)` branch).
- **Mutants (all killed):** m1 `if n > 0` → `if false` (prefix never committed): both TestIssue31 fail.
  m2 drop `applied++`: both fail. m4 deliver the error to all remaining requests instead of continuing:
  both fail. m3 skip the CDC `OnWrite` in `apply`: `TestScenarioCDCLogEntryAtomicWithWrite`,
  `TestScenarioCDCSurvivesConsumerRestart`, `TestScenarioCDCRetentionBoundsLog` fail.
- **The CDC outbox property holds across a split batch** (ADR-0068). A probe with CDC wired and the same
  8 × 900 KiB batch plus a delete of `k0`: exactly 9 `_cdc/` entries, in write order, with monotonic seqs
  and gaps from discarded txns (`[3 4 7 8 11 12 13 14 15]`). The gaps are the documented lease behavior
  (`cdc.go`: "gaps on crash are fine"), and `pending` reads `seq > cursor`, so they are harmless.
- **Order and last-write-wins** are kept: the split commits in queue order; the test asserts that the later
  rewrite of `ns/s/k0` wins.
- **ADRs.** ADR-0066 (Implemented) and ADR-0073 Decision 5 are respected: one single writer, greedy drain,
  0 conflicts, group commit. The only change is that a batch Badger would reject as one txn now becomes
  several txns; the normal path is still one txn per batch. No ADR file was touched (`git diff` on
  `docs/adr` for the commit is empty). The living docs (blueprint, PROJECT-SUMMARY) say "group commit"
  without "one txn", so they stay true.
- **Reuse.** A Badger `WriteBatch` (the tool `25338d1` uses for the DLQ sweep) would not fit here: it
  commits at arbitrary points, so it could separate a write from its `_cdc/` entry and cannot report a
  per-request error. No txn-splitting helper exists elsewhere (`ErrTxnTooBig` and `NewWriteBatch` have no
  other non-test users). The test helper builds the driver by hand because `startDriver` uses an
  unbuffered channel and starts the gateway at once, so requests cannot be queued as one batch first.
- **Scope.** Every hunk serves the issue; no test was weakened or deleted. The CDC-outbox remark moved from
  the gateway's doc comment to `apply`, where the hook now lives.
- **Conventions.** Errors still reach callers as `fault.Internalf("kvbadger.write", …)` through `submit`;
  no `any`, no new imports in production code; doc comments state the why; test imports are at top level.
- **Checks** (all through `nix develop -c`): `gofmt -l internal/kvstore/badger` empty; `go build ./...`
  ok (host and `GOOS=linux`); `go vet ./internal/kvstore/...` ok (host and Linux); `golangci-lint run
  ./internal/kvstore/...` 0 issues (host and Linux); `go test -race ./internal/kvstore/...
  ./internal/services/kv/...` ok; `go test -race ./internal/...` and `go test ./...` with no failing
  package; `just check-hygiene` clean. The `pkg/funcd` e2e suite does not use the Badger KV driver (only
  `cmd/funcd` and `cmd/funcdctl` import it), so it does not cover this path; the Lima lanes were out of
  scope for this stage.
- **Shape.** Subject `fix(kvstore): …`, `Fixes #31`, the `Co-Authored-By` trailer, one issue per commit,
  and both regression test names in the body.

Observation, not counted against this fix (`env`, unrelated code): one `go test -race ./internal/...` run
printed a `DATA RACE` report in `internal/blob/s3gateway` (inside the third-party fasthttp
`Server.ShutdownWithContext`, reached from `s3gateway.(*Server).Close`); the rerun was clean. No issue
tracks it yet.

### Definition of Done

11 / 11 items hold (the fix checklist; e2e and lane items apply only where they cover the path, and the
e2e suite does not). No misses.

### Model scorecard

To record: claude-opus-5-5 on issue #31 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11. The ledger
row is written by a later stage.

### Recommendation

Ship it. Before the PR, the builder should name the open `maxKeyBytes` bound and key-dump facet in the PR
body and point it at a follow-up issue or `/adr`, so that `Fixes #31` does not close it without a trace.
