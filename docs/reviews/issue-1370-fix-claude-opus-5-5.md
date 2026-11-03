# Issue #1370 Fix Review — a KVStore's quota is never enforced on the default in-memory KV driver

**Verdict**: **pass**. The regression test fails on the pre-fix code for the reported reason (a 9-byte write to a
KVStore capped at 8 bytes is admitted) and passes with the fix under `-race`. The revert and three mutants of the fix's
key lines each fail it. A scratch probe that follows the issue's own steps through a Function's local API gets 507 with
the fix, where main stored the value. The change removes the cause that the issue names and touches nothing else. There
are no findings. One observation, about the daemon's path, predates this fix.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #1370 · ADR-0292 (Decision 2: the feature that owns a store kind registers its quota
`Source`, and a KVStore's usage comes through `kvstore.Usager`; Contracts: `Usager` on both KV engines) · ADR-0002 ·
`CLAUDE.md` style rules

The change is one commit, `1cca7d6e` (`fix(funcd): a KVStore's quota caps the default in-memory KV driver too`), on
`fix/1370-kv-quota`, based on main `1cfa7f30`. It touches `pkg/funcd/funcd.go` (+4 −3) and
`pkg/funcd/quota_internal_test.go` (+30).

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1370 fix, model: claude-opus-5-5)

### ✅ Verified correct (keep it)
- **The regression test fails without the fix.** With main's `pkg/funcd/funcd.go` as a `go test -overlay`,
  `TestIssue1370_EveryStoreKindIsCappedOnTheDefaultDrivers` fails on its KVStore case only, for the issue's reason:
  `a write of 9 bytes to a KVStore capped at 8: <nil>`, expected `insufficient_storage`, actual `""`. The Bucket and
  SQLDatabase cases pass there, as they should: those two sources were already registered on main.
- **It passes with the fix**: `-race -count=3` passes 3/3 for all three subtests, in about 0.15 s each. Nothing is
  skipped.
- **The user-visible behavior is fixed.** A scratch overlay test (not committed) follows the issue's steps on a running
  platform: a KVStore capped at 8 bytes and bound by a Function, a 9-byte `PUT` through the Function's local API socket,
  then a `GET` of the same key. On main, the default driver answers the `PUT` with 204 and the `GET` with 200, so the
  value was stored past the cap. With the fix, under `-race`, both the default driver and an injected in-memory driver
  answer the `PUT` with 507 and the `GET` with 404.
- **Mutants**: each overlay mutant fails the regression test's KVStore case:
  - m1 removes the `Register` call;
  - m2 negates the `kvstore.Usager` condition;
  - m3 registers the source under `KindBucket` (the Bucket case fails too).
- **Cause, not symptom.** The issue names the order in `buildControlPlane`: the KVStore source was registered only when
  `c.kvStore` implemented `kvstore.Usager`, but on the default path `c.kvStore` was still nil at that point, and
  `kvmemory.New()` was set about 80 lines later. The fix moves the same lines after the default
  (`pkg/funcd/funcd.go:722-728`), so the check sees the driver that the platform will use. The move is safe: the `Book`
  reads its sources lazily, at a subject's first write (`internal/quota/quota.go:144-158`), nothing writes during `New`,
  and every other reader of `c.kvStore` already comes after the default (`funcd.go:769`, `:801`, `:1187`, `:1452`,
  `:2017`, `:2570-2573`). For an injected driver, which is the daemon's path, the default is skipped and the same check
  runs, so nothing changes there. The memory driver's `Usage` (`internal/kvstore/memory/memory.go:72-83`) counts the
  keys and values under the prefix, as the Badger driver does, and passes the shared `kvstorecontract.RunUsage`
  (`internal/kvstore/memory/memory_test.go:44`). So `funcdctl dev` without a KV directory now meters a KVStore the same
  way the daemon does.
- **Scope**: the change has two hunks, and both serve the issue. No test was weakened or deleted. The test's Bucket and
  SQLDatabase rows pin the two sibling registrations on the same default platform: an overlay mutant that drops the
  SQLDatabase source fails only the SQLDatabase row, and one that drops the Bucket source fails only the Bucket row.
- **Reuse**: the fix adds no code; it moves existing lines. The test reuses the package's `tmeta` helper
  (`pkg/funcd/hold_internal_test.go:58`), seeds objects through `p.cfg.store.Create` as `quotaGatewayWith` does, and
  asks `p.checker`, which is the same `Checker` that the KV facade receives (`funcd.go:769`), instead of building a new
  harness. The gate's bloat audit on the branch reports 0 hard flags, no cross-package clones, no masking patterns, no
  lint deltas and no new dependencies.
- **Conventions**: the new comment says why the order matters and cites ADR-0292 and the issue, as other comments in
  `funcd.go` do (`issue #94`, `issue #708`). The test is named `TestIssue1370_…`, calls `t.Parallel()`, keeps its
  imports at the top level, and shuts the platform down in `t.Cleanup`, which is the package's usual form. The
  SQLDatabase row expects a 503: with no engine running, `sql.Facade.Usage` fails at once
  (`internal/services/sql/facade.go:172-174`), while a missing source would admit the write, so the row fails if the
  source is dropped. The row is deterministic, because `New` alone starts no reconciler and therefore no engine.
- **ADRs**: the fix realizes ADR-0292 Decision 2 on the default driver. Its Contracts require `Usager` on both KV
  engines, and only the registration order kept the memory engine's `Usage` from being used. No ADR file changed. The
  blueprint already says that every store's size is capped by its `spec.quota` ("Quotas & limits"); the fix makes the
  default platform match it.
- **Siblings**: the Bucket source's guard `c.blob != nil` (`funcd.go:639`) always holds, because `validate` requires a
  blob (`funcd.go:477`) and `InMemory` opens an in-memory bucket (`pkg/funcd/presets.go:24-31`). The SQLDatabase source
  is registered unconditionally, right after its facade (`funcd.go:839`). No other code builds a `quota.Book`. The other
  optional dependencies in `buildControlPlane` do not share the cause: `c.authorizer` is defaulted (`:642`) before its
  first use, and `c.lakeProvider` is never defaulted; its `nil` checks (`:1247-1249`) deliberately mean "no Lake
  runtime injected". Only the KVStore source had the cause.
- **Checks** (through `scripts/agent/d`): `go test -race -count=1 ./pkg/funcd/` passes (43.3 s). `go vet ./pkg/funcd/`
  and `go vet -tags e2e ./pkg/funcd/` pass. `golangci-lint run ./pkg/funcd/...` reports `0 issues.`. The repo-wide
  gate (`scripts/agent/gate.sh`) was still running on `1cca7d6e` when this review was written: its `audit` step had
  passed, and `ci-full` was in its e2e suite. Its result goes into the PR.
- **Shape**: the subject is `fix(funcd): …`, the body has `Fixes #1370` and the `Co-Authored-By` trailer, and the commit
  covers one issue.

### Observation (outside this fix, not scored)
- **No Go test covers the daemon's KVStore quota wiring.** A fourth mutant registers the source only when the driver
  was defaulted, so an injected driver, such as the daemon's Badger driver, loses its quota. That mutant passes the
  regression test and the whole `pkg/funcd` unit suite, and no e2e test sets a KVStore size quota. Only the kv-counter
  Lima lane's `kv-total-refused` case (`e2e/kv-counter.venom.yml:88-109`) covers that path, and CI does not run the
  lanes. The gap predates this fix: before it, no Go test covered the default path either, which is how #1370 went
  unnoticed. One more row in the regression test, a platform built with `WithKVStore(kvmemory.New())`, would pin the
  daemon's path in about 0.15 s: under that mutant, the probe's injected-driver case answers the `PUT` with 204 and the
  `GET` with 200, and fails. ADR-0307 lists quota among the shared hooks that a later ADR will move, so this wiring is
  about to change.

### Definition of Done
11 / 12 items hold: the regression test is present; it fails before the fix for the reported reason; it passes under
`-race`; the revert and the mutants fail it; the root cause is fixed; the scope is clean; no ADR is contradicted or
edited, and the living docs stay true; the conventions hold; the change reuses what exists; the commit has the right
shape; the issue's one case is fixed and tested, and no sibling has the same cause. Item 8 is pending (env): build,
vet, lint and tests are green on the host for `pkg/funcd`, but the gate's Linux build, vet and lint, its Linux tests,
the e2e suite and the stress run had not finished at review time. No lane covers the changed default path; the
kv-counter lane covers the unchanged daemon path. Misses: none.

### Model scorecard
Recorded with this report: claude-opus-5-5 on issue #1370 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/12 (item 8
pending: the gate).

### Recommendation
Sign off. The injected-driver row from the observation is cheap and fits this branch, but it does not block the merge.
