# Issue #101 Fix Review — a DLQ sweep over one Badger txn evicts nothing

**Verdict**: **pass**. The regression test fails on the pre-fix code with the issue's own error ("Txn is too big to fit
into one request") and passes with the fix under `-race`. Three mutants of the fix's key lines each fail a test. The
change removes the cause the issue names, touches nothing else, and conforms to ADR-0118 and ADR-0002. There are no
findings.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #101 · ADR-0118 Decision 5 (Retention) and the `retention-evicts` scenario · ADR-0002 ·
`CLAUDE.md` style rules

The fix is one commit, `25338d1` (`fix(eventing): evict a DLQ backlog larger than one Badger txn`), on the group
branch `fix/200-badger-txn-size`. Its parent is the kvstore fix for another issue of the same group, which this review
does not cover. The commit touches `internal/eventing/deadletter/badger/badger.go` (`SweepExpired`) and
`internal/eventing/deadletter/badger/badger_test.go` (one new test).

## Verdict: pass — 0 blockers, 0 majors  (issue #101 fix, model: claude-opus-5-5)

### 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix.** In a detached review worktree I ran `git revert --no-commit 25338d1`
  (it applied cleanly, with no conflict against the sibling commit), restored the new test file, and ran
  `go test -count=1 -run TestIssue101_ ./internal/eventing/deadletter/badger/`. Both subtests fail at
  `badger_test.go:57` with `SweepExpired: deadletter.badger: evicting swept dead letters: Txn is too big to fit into
  one request`. That is the error and the cause the issue reports, for both the TTL case and the cap case.
- **It passes with the fix.** After `git reset --hard 25338d1`,
  `go test -race -count=1 -run 'TestIssue101_|TestBadgerContract' ./internal/eventing/deadletter/...` passes
  (`TestIssue101_SweepEvictsPastTxnLimit` 7.6 s, `TestBadgerContract` passes). Nothing is skipped.
- **The user-visible behavior is fixed.** The regression test is the issue's own probe: the on-disk driver
  (`Config{Dir}`, the production profile with a 16 MiB memtable), 30000 entries in `default`, a TTL-only sweep over
  2-hour-old entries with a 1-hour TTL, and a cap-only sweep with cap 1000. It asserts the evicted count, the entries
  left, and that the 1000 survivors are the newest. Because the store state after a failed pre-fix sweep is the same
  backlog the test builds, the same code path also clears a store that the old code left stuck, including after a
  restart.
- **The cause is fixed, not the symptom.** The old code deleted every evictable key in one `db.Update`, which hits
  Badger's per-txn entry limit. The fix deletes through a Badger `WriteBatch`, which commits each time the txn limit is
  reached and then flushes the rest. It adds no retry, timeout or swallowed error: a `Delete` or `Flush` error still
  returns a wrapped `fault.Internal`. The loss of atomicity does not matter for this operation. The old txn held only
  writes, so it had no conflict detection either, and a partial eviction after a failed chunk is completed by the next
  sweep. I read the Badger v4.9.2 `WriteBatch` source: `defer wb.Cancel()` after a successful `Flush` is safe (a second
  `throttle.Finish` returns the first result), and the store does not open Badger in managed mode.
- **Mutants** (each built with `go test -overlay`, each killed):
  - `wb.Flush()` removed: the regression test fails with 3788 entries left, and the contract test fails ("the past-TTL
    entry must be evicted").
  - The delete loop over only half of `toDelete`: the regression test fails with 15000 and 15500 entries left.
  - `wb.Delete` on a wrong key: the regression test fails with 30000 entries left, and the contract test fails.
- **Scope.** Both hunks serve the issue. No existing test was changed or removed, and no ADR or living doc was touched.
- **Reuse.** `WriteBatch` is the Badger library's own bulk-write feature, and the `NewWriteBatch` / `defer Cancel` /
  `Flush` idiom matches the existing use in `bench/badger/scenarios.go`. The change adds no helper, type or
  dependency. The sibling kvstore fix handles `ErrTxnTooBig` differently because each co-batched KV write must succeed
  or fail on its own; a DLQ sweep has no such need, so a `WriteBatch` is the simpler and correct tool here.
- **Conventions.** Errors use `api/fault` with the package's existing `op` and message. The new comment states the
  reason for the `WriteBatch`, not what the code does. The test uses top-level imports, the package's external test
  package, and the `TestIssue<N>_…` name. It contains no YAML.
- **ADRs.** The fix keeps ADR-0118 Decision 5 as written: `SweepExpired` still sweeps all namespaces with a global TTL
  and a per-namespace cap, and it returns the evicted count. The fix makes the `retention-evicts` scenario hold for a
  backlog of any size.
- **Checks** (all through `nix develop -c`): `gofmt -l internal/eventing/` prints nothing; `go build ./...` and
  `GOOS=linux go build ./...` pass; `go vet` (host and Linux) on `./internal/eventing/...` and `./pkg/funcd/...` passes;
  `golangci-lint` (host and Linux) on the same packages reports `0 issues`; `just check-hygiene` reports clean;
  `go test -race -count=1 ./internal/eventing/...` passes; and `go test -tags e2e -count=1 ./pkg/funcd/...` passes
  (112 s). No Lima lane covers this path.
- **Shape.** The commit has a `fix(eventing):` subject, `Fixes #101`, the `Co-Authored-By` trailer, and covers one
  issue. Its body names the cause, the fix and the regression test.

## Recommendation

Pass. Hand back to `/fix` for the group PR. The new test adds about 8 s under `-race` to the fast lane, because it
must write more entries than one Badger txn holds; that cost is inherent to the reproduction.
