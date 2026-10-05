## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0157 implementation, model: claude-opus-5-5, re-review)

Work: one commit `86e8d98a` on `origin/main` (`git diff origin/main...HEAD`: 8 files, all in `internal/eventing/`,
+1239 / -125). No `go.mod`, `go.sum`, `api/`, `pkg/` or `docs/` change. Compared with the commit reviewed in loop 1
(`b15d2c14`), only three eventing files changed (+70 / -9): `blobwatch.go`, `blobwatch_test.go`, `run_test.go`.

### Loop-1 findings

| Loop-1 finding | Status | Evidence |
|---|---|---|
| Minor 1 — a failing `Save` that runs during a `Register` prune or a `Deregister` re-adds the event to the failing set | **resolved** | `noteSave(k, e, err)` now returns early unless `w.watches[k]` still equals `e`, inside the same `w.mu` section (`internal/eventing/blobwatch.go:351-356`); `saveIfLive` passes `e` (`:324`). New test `TestBlobWatcherSaveOutcomeOnlyForLiveEntry` (`internal/eventing/blobwatch_test.go:519`) runs both cases (event dropped by `Register`, source deregistered) through a `Save` that changes the registry and then fails (`hookSave`, `:509`). Mutant m1 below shows the test kills the old behavior. |
| Minor 2 — the NotFound-branch `Purge` error return (`eventing.go:101` in the ADR, now `:112`) was not pinned by a test | **resolved** | New test `TestReconcileDeletedSourceReturnsPurgeError` (`internal/eventing/run_test.go:63`) reconciles a source missing from the store over a `failKV` watermark and asserts an `Unavailable` error and zero active timers. The shared setup moved into `newFailPurgeSource` (`:52`); `TestReconcileTimerPurgeErrorKeepsTimer` builds the same objects as before and keeps all its assertions. Mutant m2 below, the same mutation that survived in loop 1, is now killed. |

### Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet ./internal/eventing/ ./pkg/funcd/` | exit 0 |
| `GOOS=linux go vet ./internal/eventing/ ./pkg/funcd/` | exit 0 |
| `gofmt -l internal/eventing` | no output, exit 0 |
| `golangci-lint run ./internal/eventing/...` (host) | `0 issues.`, exit 0 |
| `golangci-lint run` with `GOOS=linux` (host-built binary) on `./internal/eventing/...` and `./pkg/funcd/...` | `0 issues.` each, exit 0 |
| `go test -race -count=1 -v ./internal/eventing/` (darwin) | `ok` (1.6 s); all 35 top-level tests pass, including the 11 scenario tests and the two new tests |
| Linux, in Docker (`golang:1.26.4`, worktree piped in as a tar): `go vet ./internal/eventing/` and `go test -race -count=1 -v ./internal/eventing/` | vet exit 0; `ok` (1.1 s), the same 35 tests pass |
| Linux, `go test -race -count=5` of the out-of-order, save-outcome, purge-during-poll and deleted-source purge-error tests | `ok` (1.8 s), no flake |

Not run, by the brief: e2e, `go test ./...`, `just ci` and `just lima-example s3` (the per-PR gate runs them).
`internal/eventing` has no build tags, so the Docker run covers its Linux behavior, including the `file://`
out-of-order subtest on a Linux file system.

### Mutants (overlay files built from the branch files; `go test -race -overlay`)

| Mutant | Line | Result |
|---|---|---|
| m1: the live-entry check in `noteSave` disabled (`false && (!ok \|\| live != e)`) | `internal/eventing/blobwatch.go:354` | killed by `TestBlobWatcherSaveOutcomeOnlyForLiveEntry` (both subtests: `"arrived"` reported as failing) |
| m2: the NotFound branch discards the `Purge` error and returns nil | `internal/eventing/eventing.go:112` | killed by `TestReconcileDeletedSourceReturnsPurgeError` ("An error is expected but got nil") |
| m3: prune disabled (`if false && !listed[key]`) | `internal/eventing/blobwatch.go:300` | killed by `TestScenarioDeletedObjectPruned` (still killed after the rework) |

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

None. The rework changes only what the two loop-1 Minors asked for and introduces no new defect. One behavior
was checked and is correct: when a `Register` re-points an event during its `Save`, `noteSave` now skips the
outcome; a failure recorded earlier for that event stays until the next poll of the new entry, which saves the
replaced record and records the new outcome. Decision 9 allows this, because the condition follows the next poll.

### ✅ Verified correct (keep it)

- **The two fixes are minimal and stay where the problem is.** The `noteSave` check uses the same live-entry
  comparison as `matches`, inside the `w.mu` section that writes the failing set, so neither `Register` nor
  `Deregister` needs to take the record lock. The new test makes the fault happen during the `Save` call itself,
  instead of relying on timing, so it is deterministic under `-race`.
- **Everything verified in loop 1 still holds on this commit.** Spot-checked again: `Register` stamps the epoch
  and drops the failing entries of pruned events (`blobwatch.go:119-139`); `Purge` holds `recordMu`, then `mu`,
  while it drops the entries and increments the epoch, then calls `Delete` (`:150-158`); `pollOne` returns on a
  `List` error before it touches the record, checks the live entry before each `Publish`, prunes against the full
  listing, and saves only on a change through `saveIfLive` (`:242-326`); the NotFound branch and the non-blob
  branch return the `Purge` error last (`eventing.go:107-140`); `setBlobNotReady` removes `SeenListSaved`
  (`:187`). A grep for `Cursor{`, `KeysAtMax`, `MaxModTime`, `isNew(` and `advance(` finds no match in
  `internal/eventing`. Every direct `BlobWatcher.Register` caller passes a constant UID (`testUID`, or `uid-2`
  in the new-UID subtest).
- **Contracts.** `SeenList`, `SourceRef`, the four-method `Watermark`, `WatchHooks`, and the `Register` /
  `Deregister` / `Purge` / `SetHooks` signatures match the ADR exactly (unchanged since loop 1).
- **Scenarios.** All 11 scenario tests named in the Implementation plan pass under `-race` on darwin and Linux,
  together with the ADR-0119 tests, `TestBlobWatcherTieBreak`, `TestBlobWatcherPurgeDuringPoll`,
  `TestBlobWatcherInvalidUTF8KeySkipped` and the `Watermark` contract tests. No existing assertion was weakened.
- **Scope and conventions.** The change stays inside `internal/eventing/`; errors go through `api/fault`; logging
  uses only `slog`; there is no `panic`; `ctx` is the first parameter; imports are at the top of each file (the new
  `errors` import in `blobwatch_test.go` included). The commit message lists both new tests and keeps `Fixes #34`
  and `Fixes #57`.

### Definition of Done

6 / 8 items hold (5 Review-checklist items + 3 Implementation-plan "Done" items: one test per scenario, `just ci`
and `just lima-example s3` green, the `Fixes` lines).

- Checklist 1-4 hold, now without caveats: the loop-1 caveats on item 3 (the `:101` `Purge` error) and item 4
  (no stale `SeenListSaved` False) are resolved, and a mutant test now covers each.
- Checklist 5 holds except `just lima-example s3`, which this review did not run (`env`: excluded by the brief;
  the per-PR gate runs it).
- Done: one passing test per scenario holds; the `Fixes #34` and `Fixes #57` lines hold (commit message; no PR is
  open yet); `just ci` and the Lima lane are not verified here (`env`, the per-PR gate).
- Tracking: the ADR still reads `Accepted`, and the F83 row has not changed. In this campaign, one docs PR per wave
  makes those moves, so this is not counted against the model. This review did not stamp the ADR or edit any doc.

### Model scorecard

To record: claude-opus-5-5 on ADR-0157 (implementation, re-review) → pass, 0/0/0, DoD 6/8 (the 2 misses are
`env` items this review did not run). The ledger row is below; the wave's ledger PR records it.

### Recommendation

Pass. Both loop-1 Minors are fixed, and a test that kills the matching mutant pins each fix. The per-PR gate still
owes `just ci` (with e2e) and `just lima-example s3`; then the wave's docs PR moves ADR-0157 to `Reviewing` and
then `Implemented`, moves the F83 row, and adds the ADR-0119 back-link.

```json
{
  "date": "2026-10-05",
  "adr": "0157",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 0,
  "model_attributed": 0,
  "dod_passed": 6,
  "dod_total": 8,
  "report": "docs/reviews/adr-0157-implementation-claude-opus-5-5-2.md",
  "notes": "re-review pass; both loop-1 minors resolved: noteSave now records a Save outcome only while the entry is still live (TestBlobWatcherSaveOutcomeOnlyForLiveEntry), and the NotFound-branch Purge error is pinned (TestReconcileDeletedSourceReturnsPurgeError); host and Linux build/vet/lint green; eventing -race green on darwin and in Linux Docker (5x repeat of the race-sensitive tests clean); 3/3 overlay mutants killed (noteSave live check, NotFound Purge-error return, prune); just ci and the s3 Lima lane not run here (env, per-PR gate); ADR/feat stamping deferred to the wave docs PR."
}
```
