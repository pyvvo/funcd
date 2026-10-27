## Verdict: changes requested — 1 blocker, 0 majors, 2 minors  (ADR-0292 implementation, model: claude-opus-5-5)

This review covers 31bd5849 on `feat/adr-0291-workload-limits` against ADR-0292's Decisions, Contracts, Scenarios,
Design model, Review checklist and done-when line. ADR-0292's work is be278212, 68cd587f and 2e903323, with the
pins of 08be33d3, on top of the acceptance commit 4c0f382d. The checks ran in a worktree detached at 31bd5849.
`scripts/agent/gate.sh` and the Lima lanes run once on the branch, in the integration session, and did not run here.

### Checks run
- `go build ./...`: ok.
- `go test -race -count=1` on the 28 packages the branch touches: 27 pass. `pkg/funcd` fails on
  `TestScenarioResourcesChangeRestartsEngine` (Blocker 1), and only on that test.
- `go vet` on the touched packages, plain, `-tags dev`, `-tags e2e` and `GOOS=linux`: ok.
- `golangci-lint run` on the touched packages, `--build-tags dev` for `internal/lake/devengine`: 0 issues.
  `gofmt -l` on the changed Go files lists nothing.
- `just check-hygiene check-specs`: ok. `quota-usage.fizz` passes, with its oracle markers in
  `internal/quota/quota_test.go`.
- Four mutants, run with `go test -overlay`:
  - `blob.Quoted` charging every key segment, not only a capped prefix, fails `TestQuotedAsksOnlyCappedSubjects`.
  - `deletes` dropping its statement-end check fails `TestDeletesOnly`.
  - `Admit` skipping the fresh measure when bytes were settled or deleted fails `TestScenarioOverwriteNotRefused`,
    `TestDeleteMakesRoomAtOnce` and `TestDeleteDuringAMeasureCounts`.
  - The Site's `QuotaExceeded` branch removed (`internal/site/reconcile.go:384-386`) **survives** (Minor 1).

### 🔴 Blocker 1 — ADR-0232's scenario test still asserts the teardown this ADR removed  ·  attribution: model
`TestScenarioResourcesChangeRestartsEngine` (`pkg/funcd/sql_internal_test.go:279-297`) fails 3 of 3 with `-race`, at
:291, `expected: 1 actual: 0` ("one change makes exactly one teardown"). Decision 6 removed the SQL reconciler's
per-generation teardown (`internal/services/sql/reconcile.go:150`). The test's fake `sqlRuntime` counts only
`Teardown` calls, and it does not model the provider runtime's replacement by spec fingerprint (#1195).

With origin/main's `internal/services/sql/reconcile.go` overlaid, the test passes, so the branch causes the failure.
The builder updated the unit-level sibling (`internal/services/sql/sql_test.go`,
`TestGenerationChangeConvergesTheNewBound`) but missed this one, and `just ci-full` fails on it.

Fix (builder): assert ADR-0292's mechanism, as the sibling does. Converge gets the new bound, the directory and the
data stay, and the reconciler makes no teardown.

### Minor
- **The Site's `QuotaExceeded` reason has no test** (`model`). Decision 5 says a Site at its Bucket's quota is
  `NotReady`, reason `QuotaExceeded`, and the scenario `bucket-total-refused` names a Site's build.
  `TestScenarioBucketTotalRefused` shows only the shared view's 507. The three branches that set the reason are
  untested (`internal/site/reconcile.go:384-386`, `basehref.go:60`, `runtimeconfig.go:58`): the mutant above passes
  `internal/site` and every Site, Quota and Bucket test in `pkg/funcd`.
- **`blob.Quoted` takes a nil Checker as is** (`model`). Decision 1 and the checklist make `quota.None` every
  enforcement point's default. The KV and SQL facades default a nil `Quota` to it, and `Quoted` does not. Every
  caller passes a Checker today.

### Routed (not this work's)
- **The changelog's note on the move** · home: elsewhere · destination: the PR's squash message (the one-PR batch
  mode's `BEGIN_COMMIT_OVERRIDE`). The ADR's Negative promises a changelog line for a breaking change: a per-item cap
  at the spec's root is now a 422. be278212 carries neither a `!` nor a `BREAKING CHANGE:` footer, which is what
  release-please reads.

### ✅ Verified correct (keep)
- **The decision point** (`internal/quota/quota.go`):
  - `Book` follows the design model's state. Usage is the last measure plus the settled and reserved bytes; a landed
    measure keeps what settled while it ran (`during`); a delete during a measure is carried over (`freedMid`).
  - A refusal waits on one shared fresh measure only when bytes were settled or deleted since the last one.
  - The credit is read outside the lock and read again when a measure landed meanwhile (`credit`'s epoch loop).
  - A newly capped subject is measured before its next decision (`fresh`), and a stored write of unknown size starts
    a measure or one more after the running one (`again`).
  - A failed first refresh is 503, and a failed later one keeps the last values. Subjects are kept as copies.
  - `quota.None` carries its `gochecknoglobals` reason.
- **Sources and enforcement points**:
  - `blob.Quoted` sits inside `blob.Capped` in `s3BucketFor`, so a 413 comes first. It asks only capped subjects
    (a declared prefix with a size, then the Bucket), releases the prefix's reservation when the Bucket refuses,
    reports deletes, and refuses a presigned PUT under a cap.
  - Every Bucket path goes through that one view: the S3 gateway, `ctx.blob`, Sites, the static handler and the
    watcher.
  - The KV facade counts the stored key and the value and credits the old pair. `Usager` is on both engines and in
    the contract suite (`RunUsage`).
  - The SQL facade asks only on Execute, with `quota.Unknown` and `Deletes` from `deletesOnly`, and measures
    `(page_count - freelist_count) * page_size`.
  - `deletesOnly` skips literals, quoted identifiers and comments, and treats `WITH … DELETE` and a second
    statement as writes.
- **The refusal**: `fault.InsufficientStorage` maps to 507 with `urn:funcd:problem:insufficient-storage`. The gateway
  answers `QuotaExceeded` with 507, and the message names the subject, its usage and its limit.
- **The quota block**:
  - `SizeQuota`, `BucketQuota` and `KVStoreQuota` match the Contracts. Both forms or a bad size is a 422.
  - A root per-item cap is refused at the edge (`TestScenarioPerItemCapMoved`), and the 413 under `quota` still holds
    (`TestIssue109_…`, `site_status_test.go`).
  - Every reader of a per-item cap reads `spec.quota`. The fixtures and the pinned language repos' examples (v0.23.0,
    v0.18.0) put their caps under `quota:`.
- **The engine keeps running**: the SQL reconciler no longer tears down per generation, and the engine's spec carries
  no quota, so the provider runtime's fingerprint does not change with it (`TestScenarioQuotaChangeKeepsTheEngine`).
- **Tests and lanes**: one named test per scenario. The kv-counter, sql and duckdb lanes each add a case
  (`e2e/kv-counter.venom.yml:90`, `e2e/sql.venom.yml:435`, `e2e/duckdb.venom.yml:647`).
- **Tracking**: the ADR at `Reviewing` with its substance unchanged since acceptance, F207 at `reviewing`. The
  back-links in ADR-0073, ADR-0080 and ADR-0232 were added at acceptance, and the blueprint names the quota block.

### Definition of done
8 of 9. That is the four Review checklist items, plus the done-when line's per-change checks: the tests with
`-race`, build, vet, lint, and `just check-hygiene check-specs`. Miss: the tests with `-race` (Blocker 1, `model`).
`scripts/agent/gate.sh` runs once on the branch in the integration session.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0292 (implementation) → changes-requested, 1/0/2, 3 model-attributed, DoD 8/9. See
docs/reviews/model-scorecard.md.

### Recommendation
Fix Blocker 1 and re-review ADR-0292. The two Minors can ride along. Nothing here touches the ADR.
