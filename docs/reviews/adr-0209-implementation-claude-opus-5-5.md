## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0209 implementation, model: claude-opus-5-5)

Scope: `git log 548c1f08..HEAD` on `feat/adr-0209-kv-backup-common-format` (7 commits, 28 files, +3138/-816). The base
carries the unmerged ADR-0207 and ADR-0208; only this range is reviewed.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build darwin | `scripts/agent/d go build ./...` | exit 0 |
| build linux | `GOOS=linux scripts/agent/d go build ./...` | exit 0 |
| vet darwin / linux | `go vet ./cmd/funcd/ ./internal/kvstore/... ./internal/platform/config/ ./internal/snapshot/...` | exit 0 / exit 0 |
| lint darwin | `go tool golangci-lint run` (same packages) | `0 issues.` exit 0 |
| lint linux | `GOOS=linux <golangci-lint> run` (same packages) | `0 issues.` exit 0 |
| tests | `go test -race -count=1` (same packages) | `ok cmd/funcd 34.9s`, `ok internal/kvstore/badger 23.5s`, `ok internal/kvstore/memory`, `ok internal/platform/config`, `ok internal/snapshot`; exit 0 |
| scenario timings | `go test -v -run 'TestScenario…'` | every scenario 0.12-0.89 s; all but one call `t.Parallel()` (the serial one sets `version.Version`, says so) |
| box rule | grep `backup.go` for bucket `Get`/`Attributes`/`Delete` | none (only Badger `txn.Get`/`txn.Delete`); writer uses `Put` (`IfNotExist`), `List`, `ListAfter` |
| tree | `docs/feat`, `blueprint.md`, `docs/reviews`, `go.mod`, `go.sum` | untouched |
| ADR | `git diff 548c1f08...HEAD -- docs/adr` | only the status line `Accepted → Reviewing`; substance unchanged |
| reuse | `internal/testkit/s3stub`, `backup.TargetURL` | both used as is (`kvbackup_scenarios_test.go`, `chain_test.go`, `fake_bucket_test.go` `TestMain`; `main.go` `kvBackup`); no copied stub or helper. `fakeBucket` is the pre-existing ADR-0195 fake, moved and extended |

Not run here by instruction (the gate runs them once): `just ci`/`just ci-full`, the e2e suite, repo-wide `go test`, Lima lanes.

### Mutants (run with `go test -overlay`, the work untouched)

| # | Mutation | Killed by |
|---|---|---|
| M1 | `backup.go` Ship: drop the `first` hook, so `next+1` is not stored before the segment's first `Put` | `TestScenarioFailedShipSkipsNumber` FAIL |
| M2 | `chain.go` walk: ignore `since > head` (no chain break) | `TestChainWalk` FAIL |
| M3 | `restore_kv.go`: remove `restore.CheckVersion` before `RestoreDir` | `TestScenarioNewerChainRefusedCommand` FAIL |

### 🟡 Major

None.

### Minor

- **`restore list` depends on a second target and cannot reach a KV-only target**. Attribution: model.
  `restore_kv.go` `kvPoints` opens `kvstore.backup.target` with the same `--credentials-file` whenever it differs
  from `backup.target`, even with `kvstore.backup.enabled: false`. If that target cannot be opened or listed, the whole
  `restore list` fails, platform rows included. A KV-only config (no `backup.target`) also fails earlier in `f.open`
  ("no --from and backup.target is empty"), unless `--from` is given. Fix: list KV rows only when the KV export is
  configured, report a KV listing error as a row or a warning, and let a KV-only config list its `kv/` target.
- **The seam's `Restore` reads the chains twice**. Attribution: model. `chain.go` `(*exporter).Restore` calls
  `readChains` and then `ListPoints`, which calls `readChains` again: two `List` calls and two `Get` calls per
  manifest. It is the test-only seam (Decision 6), so the cost is small. Fix: compute the points from the one `chains`
  map.
- **`restore kv` writes the hold marker (`hold.Write` after `hold.Begin`)**. Attribution: adr (ADR-0206 Decision 5,
  per the DR session's decision of 2026-10-10). Decision 6 names only `hold.Begin`/`Own`/`End`, but
  `beginRestoreHold` also writes the marker, so the next start is held, as `restore blob` does. This is correct
  behaviour. ADR-0206 Decision 5 leaves the rule out, and a later ADR should state it. It does not count against the
  model.

### ✅ Verified correct (keep it)

- **Decision 1 / `ResolveKVBackup`** (`internal/platform/config/kvbackup.go`): the target and encryption groups are
  each taken whole from their lead key. The refusals are covered: `credentialsFile`/`singleWriter` without a KV
  target, a KV target equal to `backup.target`, no target in either block, and no recipients or `none` in either
  block. Each is `fault.Invalid` and names the key. They become `CheckBackup` rows (`checkKVBackup`), together with the
  `chunkBytes` limit of 8 MiB and the check that `rebaseline` stays below ⌈hourly/24⌉ days. `backup.encryption.*` is no
  longer reported as ignored while the KV export inherits it. An inherited target shares the platform `Target`
  (`kvBackupDeps.target`); an own target gets its own `backup.Open` with `KeyPrefix: "kvstore.backup."`. The bucket
  opens through `backup.TargetURL` + `gocloud.OpenWith`, and the sealer is built with `NoSecrets`. The per-key env
  tags are right (`envSeparator:","` for recipients). A typed-nil `*hold.Hold` is avoided.
- **Decisions 2-3** (`backup.go`): the paths are `kv/base/<class>/<n>/` and `kv/inc/<n>/<m>/` with 10-digit numbers.
  Parts and then `manifest.yaml` are written `IfNotExist` (conditional from `Ready`). The chunk default and maximum
  are 8 MiB, and `NewBackup` refuses a larger value. The cursor record is a 24-byte `cursor‖n‖next`. `next+1` is
  stored before the first part, and the cursor is set after the manifest. An absent, 8-byte or unknown-chain record
  makes the Ship re-baseline (`TestCursorRecordForms`). The re-baseline lists `kv/base/` once, then calls `ListAfter`
  (limit 1) repeatedly for a higher `kv/inc/` chain, and numbers the new chain above every chain held
  (`TestScenarioExpiredBaseNumbersAbove`). The due time comes from the newest complete manifest's `ModTime`. The old
  `manifest.json`/`prune` code is gone and the legacy objects are never read (`TestScenarioUpgradeStartsNewChain`).
- **Decision 4 / reader** (`chain.go`): `ChainManifest` matches the Contract. The walk loads complete segments in
  order, re-loads a re-shipped range, and breaks the chain at `since > head`. A higher `format` is `fault.Invalid` and
  names both values. `Funcd` is the semver-highest stamp on the walk. `RestoreDir` checks everything before it writes:
  `Conflict` for a non-empty directory, `NotFound` for a missing base, segment or part, and `Invalid` for the format.
  It opens the recipients once per manifest. A size or SHA-256 mismatch is `fault.Invalid`. The record pass runs and
  the 8-byte cursor is stored. Any error empties the directory.
- **Decision 5**: `LifecycleRules` gives 2, 2, 30 and 84 days at the defaults (`TestKVLifecycleRules`), and the rules
  are logged at start (`TestKVBackupAssembledWithHold` asserts `kv/inc/:2`). Ladder classes are covered by
  `TestScenarioLadderClasses` (Mon/Tue/Wed/Wed ⇒ weekly/daily/daily/hourly).
- **Decision 6**: every run awaits `Ready`. A refusal reaches the log and `Record`, and the error names
  `kvstore.backup.singleWriter` (`TestScenarioKVProbeFailureStopsBackup`). While held, a run writes nothing and records
  nothing; Ships recheck each interval and the re-baseline waits `RebaselineRetry` (`TestRunBackupSkipsWhileHeld`, plus
  the assembly case in `cmd/funcd`). `restore kv` covers: memory KV ⇒ `Invalid`, `CheckVersion` before writing, the
  hold sequence `Begin`→`RestoreDir`→`Own(kvstore.dataDir)`→`End`, undo on error, `list` KV rows with `newer`, and
  `inspect --kv`. `Recorder("kv")` is wired from the runner, which ADR-0205 builds even when only the KV export is on.
- **Decision 7**: `SnapshotPrefix` exists on both drivers. Badger uses one `View` with one prefix iterator
  (`snapbadger.SnapshotPrefix`, the existing #806 reader generalized rather than copied). Memory copies under one read
  lock. The shared contract `kvstorecontract.RunSnapshotPrefix` checks the refusal before any emit, the
  prefix-stripped keys in order, the absence of `a/s10/`, `a/s1x`, deleted keys and reserved keys, and, with
  concurrent writers, that each writer's keys form an unbroken run (one snapshot).
- **Scenarios**: all 12 have named, un-skipped, passing tests, plus the ADR's units: `TestResolveKVBackup`,
  `TestCursorRecordForms`, `TestChainWalk`, `TestKVLifecycleRules`, `TestSnapshotPrefixContract` (both drivers) and
  `TestRunBackupSkipsWhileHeld`. `TestScenarioPutAndListSuffice` runs on the s3stub box policy and asserts only
  `PUT`/list requests and no key written twice. `TestScenarioInheritsPlatformTarget` checks the signing key id per
  request and opens the base with each identity. ADR-0195's tests moved to the new layout, and
  `TestScenarioDaemonBackupEnabledRequiresTarget` asserts the group rule.
- **Docs**: `examples/funcdconfig.yaml` has the new keys in block style, with `chunkBytes` set to 8388608.
  `examples/backup-lifecycle.md` adds the `kv/` rules (JSON and `find`), the box policy for a KV-only target, how to
  remove the legacy objects, and the upgrade refusals. `examples/restore-runbook.md` adds `restore kv`.

### Definition of Done

8 / 9 items hold here (ADR Review checklist 4 + ADR DoD 5). All four checklist items hold. Of the DoD, these hold:
`-race` tests pass on the touched packages, `backup.go` makes no `Get`/`Attributes`/`Delete` call on the box, no new
module, and no leak. `scripts/agent/d just ci` was not run here by instruction; the PR gate runs it once (not a miss).

### Model scorecard

To record (not written by this run): claude-opus-5-5 on ADR-0209 (implementation) → pass, 0/0/3, 2 model-attributed,
DoD 8/9 (the ninth item is the gate's `just ci`).

### Recommendation

Pass. The two model Minors are cleanups and can wait for a follow-up. The adr-attributed hold-marker rule belongs in
a later ADR that amends ADR-0206 Decision 5. Run the PR gate (`just ci-full`, Linux checks, the audit) before the
`Implemented` stamp.

```json
{"adr": "0209", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "pass", "blockers": 0, "majors": 0, "minors": 3, "model_attributed": 2, "dod_passed": 8, "dod_total": 9, "report": "docs/reviews/adr-0209-implementation-claude-opus-5-5.md", "notes": "pass; 3 minors: restore list fails on/needs a separate KV target [model]; seam Restore reads chains twice [model]; restore kv writes the hold marker, outside ADR-0206 D5 [adr]; 3/3 mutants killed; just ci left to the gate"}
```
