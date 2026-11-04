# ADR-0312 implementation review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors, 3 minors (ADR-0312 implementation, model: claude-opus-5-5)

Reviewed `48319cf3` on main `5a347faa`: three code commits, the status commit `2925a2c6` and one later fix, on top of the
acceptance commit `bd73f1b5`, built by a session other than this one. ADR-0311's code and its stamp sit below them and were
reviewed already. The ADR's substance is unchanged: since acceptance, only its status line moved. The three Minors need no rework
before the merge, though the first and third are small fixes worth making.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **A stale comment** · attribution: model · `pkg/funcd/binding_providers.go:248-249` still says the Bucket kind holds "the
  catalogs of its Lakes, which its purge takes too (ADR-0293 Decision 5, #1383)". The kind holds only the catalogs of its other
  prefixes and purges nothing now. Fix: reword it to ADR-0312's rule.
- **`StopEngine` removes `CatalogSaved`, which nothing asked for** · attribution: model · `markRestoring`
  (`internal/services/lake/reconcile.go`) deletes the condition, and `TestIssue1384_StopEngineKeepsTheLakeEngineless` pins it.
  Decision 9 does not say so, the ADR's Consequences say "`CatalogSaved` can read `False` until its first save", and the builder's
  list of differences leaves it out. The behavior is sound: a report about a replaced catalog would be stale, and removing it stops
  the condition reading `True` with an old time over an empty store. I would keep it. It is recorded here because it differs from
  the frozen text and was not stated.
- **No test restores a whole copy elsewhere in one Restore** · attribution: model · `examples/workload-backup.md` tells a user to
  name "one member for the Bucket and one for each Lake", which means a Bucket `as` X with a Lake `asLocation` on X. No Go test or
  lane case has both in one Restore: the scenario's test and block B run them apart, and the positive case of
  `TestRestore_RefusesALakeOnABucketInPlace` restores the Bucket `as` another Bucket than the Lake's. A mutant of Decision 6 that
  also refuses this form survived every test (`d6-as-too`). The code is right: my own probe of it passes in both member orders.
  Fix: one case in that test and one in `TestScenarioBucketRestoredAsCarriesNoLake`.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **Checks, run by me on the touched packages.**
  - On `2925a2c6`, on `api/types/v1alpha1`, `internal/backup/workload`, `internal/services/backup`, `internal/services/lake`,
    `internal/lake/gateway`, `internal/dataplane`, `internal/blob/s3gateway` and `pkg/funcd`: `go build ./...`, `go vet` and
    `golangci-lint` with no tag and with dev, e2e and integration (0 issues), and `go test -race`, all exit 0. `GOOS=linux go vet`
    on the four packages that changed most, exit 0.
  - Also on `2925a2c6`: the new tests under `-race -count=8` (`-count=3` in `pkg/funcd`), the e2e-tagged Lake rigs under `-race`,
    `just check-specs` (16 specs, 0 failing), `just check-hygiene` and `go test -run Lane ./scripts/...`, all exit 0. The slowest new
    scenario takes 0.54 s, and each calls `t.Parallel()`.
  - On `48319cf3`, on `internal/backup/workload`, `internal/services/backup` and `pkg/funcd`: the same build, vet, lint, `-race`
    and e2e-tagged runs, its two new tests under `-race -count=5`, and `just check-specs`, all exit 0.
- **Lanes, not run by me.**
  - `just lima-example app-todo`: 63 of 63 with real assertions, D18, B1 to B13, D8 and Q5 included, on `f5d94b3c` (block B on
    `b1ed582b`) with funcd-functions `47da482`, run by the lane's session. I read its report and its block B: each case reads real
    rows through a Function that runs SQL on the Lake.
  - `just lima-example duckdb`: 26 of 26, run by the builder.
  - The gate and the 13 lanes run once on the PR's tip, which adds block B and the funcd-functions bump to this code.
- **Decision 9, read line by line and by mutants.**
  - `StopEngine` marks first, waits until `passing` is 0, suspends the proxy, retries its status write up to three times, then
    tears down whatever that write answered. It returns only after `Teardown`, and the driver's stop waits for the task to die.
  - `Reconcile` takes `beginPass` as its first step and `defer`s `endPass`, so a pass counts until it returns, not until `Converge`
    returns. A marked pass returns `RequeueAfter` and touches nothing. `Converge` has one caller, so that also blocks supervision
    (ADR-0142).
  - The edge entry stays. `TestIssue1384_ARestoreLoadsALakeWithNoEngine` proves it through the real edge: 503 with
    `Retry-After: 30` during the load.
  - For Decision 9, `restore.go` is unchanged: the hold comes first, then `Prepare`, the load, the lift, and the release in its
    `defer`.
  - `TestIssue1384_StopEngineReturnsOnceTheEngineIsGone` has the store that answers Conflict once and the provider that blocks, as
    the plan asks, and `TestIssue1384_StopEngineWaitsForAPassCreatingAnEngine` holds its pass in `Ensure`.
- **The gone-Lake rule.** `lakePrefixes` takes `gone`: false in `Resolve`, true for a Bucket's `IsEmpty`, `Clear` and `Load`, so a
  copy holds a deleted Lake's objects and catalogs, and a Restore leaves its prefix. `TestScenarioGoneLakeKeepsItsPrefix` runs a
  rewind from a copy made with the Lake and from one made without it, then with the owner cleared.
- **The walk past a Lake prefix.** The builder found that a key under a Lake prefix sorting past the seek target looped `IsEmpty`
  and `Clear` forever, and fixed it with `max(seek, it.Key)`. The fix is right: `after` now grows on every pass, so the walk ends, and
  it skips nothing, since `it.Key` is the first key listed. With the fix removed, `TestBucket_IsEmptyAndClearActOutsideItsLakePrefixes`
  hangs until its timeout (my mutant `walk-seek-stuck`). I also ran the walk on the file store: it returns `a.txt`, `gold-two/y`,
  `gold2/z` and `zz/q`, and skips `gold/`.
- **The refused saves.** `issue1233-failing-saves-show` and `issue1385-a-refused-save-runs-a-statement-once` swap only their two
  apply steps for `sudo chattr -R +i` and `-i`, each with its own step output, and keep every other assertion (`git diff` of
  `e2e/duckdb.venom.yml`). `lake-saves-refused.yaml` and its `scripts/lanes.yaml` entry are gone with no reference left.
  `scripts/lane-reset.sh` clears the flag before its `rm` and has no `set -e`, so a missing directory does not stop it. The builder's
  run of the lane is the proof on Linux. I checked only that a file store on an immutable directory refuses a Put, keeps the old
  object and recovers with no restart, on macOS with `chflags`.
- **#1383.** `bucketCatalog` and the put by key, `purge` and the `gc` import are gone from `internal/backup/workload`.
  `TestKinds_RoundTripLeavesTheStoresAsTheyWere`, `TestKinds_AFailedRestoreDeletesOnlyWhatItWrote` and the three `TestIssue1383_…`
  keep every assertion and pass, and they fail when a Bucket's `Clear` takes a Lake's files or catalog. `Fixes #1383` and `Fixes #1384`
  are in their commits.
- **Decisions 1 to 8 and 10.** I read `Resolve`, `lakePrefixes`, `buckets.go` and `lakes.go` against each decision. `Resolve` reads
  each Lake once, so what a Bucket's copy skips and what the Lake's member copies agree. `Write` reads no store. A format 1 copy on
  the rig ends `Failed`, naming both formats, with no hold and no store changed.
- **The fix after the stamp, `48319cf3`.** A Restore fails a member whose manifest lists another number of files than its kind
  writes, before it empties or writes that member: one for a KVStore, a SQLDatabase and a volume, one or two for a Bucket, none to
  two for a Lake. Those ranges match each kind's `Write`. A refused member is not cleared, earlier members are cleared as in any
  failed load, and a part count below zero reads no part. Its one line in `restore.go` belongs to this fix, not to ADR-0312's
  decisions, so the ADR's Scope still holds for them. `TestRestore_RefusesAMemberWithAnotherFileCount` covers every kind.
- **Mutants.** The builder ran 44, all caught. I ran 70 of my own: 56 on `2925a2c6`, covering the two specs' nineteen, the plan's list
  and my own spot checks (a marked pass that writes `Ready`, a Decision 7 that ignores the owner's kind, a catalogs file that takes a
  Lake's catalog, prefixes read once at the plan), and 14 on `48319cf3`. 69 were caught, and the one survivor is the third Minor.
- **Specs.** `just check-specs` passes with no `no oracle test yet` for either spec. I reran both: 11,992 and 121 states, every mutant
  failing its named assertion, and the wider runs pass (33,224 and 240 states). Each of the seven assertions has its oracle marker.
- **Tests.** Every test the ADR names exists and passes, none skipped. The only assertion lines removed from existing tests are in
  the three the plan names. Every other edit to an existing test is setup: the owner of a Lake's prefix, or the signatures.
- **Conventions (ADR-0002).** No `any` in the new signatures, `api/fault` errors, no `panic`, `log/slog` only, `go.mod` unchanged. The
  commits use `feat!` for the two breaking changes.
- **Tracking.** The ADR was at Reviewing and F226 at reviewing. The back-links on ADR-0311, ADR-0222 and ADR-0293, `blueprint.md:241`
  and F225's row are done.

### The builder's four stated differences

All four are acceptable.

1. **`ReconcilerDeps.Proxy` is the interface `lake.ProxyManager`.** The plan's "holds its pass in `Ensure`" needs a seam, and the
   Contracts do not name this field. The only production value is the `*Manager` that `build` has just made, so no typed nil can pass
   `r.proxy != nil`, and `lake.go` no longer imports the gateway.
2. **A marked pass writes nothing, and `StopEngine` retries its write three times.** Decision 9's "at most writing" allows it. If all
   three lose, the status stays `Ready` during the load, with the proxy suspended and the hold on, and the first pass after the release
   corrects it.
3. **`stop-returns-early` has three code forms.** My mutants of all three are caught, one of them by a test the ADR does not name,
   `TestIssue1384_StopEngineStopsTheEngineWhateverItsStatusWriteAnswers`.
4. **Plan item 5 still calls `chattr +i` a candidate.** The ADR is frozen, so it stays. The lane proved it, and this report records
   that.

### Definition of Done

12 of 12 hold: the ADR's 3 checklist lines and 9 applicable generic items (no port has drivers here, so the contract suite item does
not apply). The full suite here is the touched packages; the gate runs on the PR's tip.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0312 (implementation): pass, 0 blockers, 0 majors, 3 minors, 3 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation

Merge after the gate and the 13 lanes pass on the PR's tip. The first and third Minors are worth one small commit before then.
