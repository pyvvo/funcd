# Review: ADR-0203 implementation (claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0203 implementation, model: claude-opus-5-5)

Reviewed commit `a8c282e2` (`feat(backup)!: implement ADR-0203 — Backup format, targets and fencing`) against the
ADR's Decision, Contracts, Scenarios, Review checklist and Definition of done, and the ADR-0002 conventions.

### 🔴 Blocker

None.

### 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

- **Checks, run on the touched packages** (`scripts/agent/d`): `go build ./...` exit 0; `go vet` on
  `internal/backup/...`, `internal/blob/...`, `internal/platform/config/...` exit 0;
  `go test -race -count=1` on the same packages: `internal/backup`, `internal/blob`, `internal/blob/gocloud`,
  `internal/blob/s3gateway`, `internal/platform/config` all `ok`, exit 0; `golangci-lint run` on them: `0 issues.`
  The repo-wide `just ci`, the e2e suite and the Linux lint are left to the PR gate.
- **Every scenario has a named, un-skipped, passing test** in `internal/backup/backup_test.go`:
  `TestScenarioGenerationLayout` (parts in cut order with an 8 MiB split, manifest last, `at` in ADR-0196's form, a
  second create `fault.Conflict`), `TestScenarioFailedRunSkipsNumber` (n incomplete, n+1 written, no `DELETE` sent),
  `TestScenarioProbeOutcomes` (file and honoring S3 ready; ignoring and refusing S3 write nothing and name
  `backup.singleWriter`; `singleWriter: true` warns and writes), `TestScenarioDirectorySecondWriterRefused` (a real
  child process holds the lock; refused with and without `singleWriter`; B writes after A exits),
  `TestScenarioSecondPlatformRefused` (foreign timeline above last; a `daily` run's parts at the `hourly` n caught at
  the second listing; T1 puts only `gen/hourly/44-T1/refused`, then T2 is refused by it), `TestScenarioLadderClass`
  (weekly, daily, hourly; `daily: 0`; pins never count; a `verified` pin through `Write` is `fault.Invalid`),
  `TestScenarioPutAndListSuffice` (the stub's box mode refuses `Get`, `Attributes`, `Delete` and `gen/verified/`
  puts; only `PUT` and the list `GET` are sent), `TestScenarioManifestRecordsLineage` (`parent` T1/40, `Abandoned`
  = T1/41, T1/42), `TestScenarioTargetChecked` (`mem://`, `gs://`, `azblob://`, a credentials file beside `file://`,
  relative dir, `prefix` on a directory, retention bounds, the `kvstore.backup.` key prefix),
  `TestScenarioCredentialsFileSigns` (environment keys set, every request signed `Credential=AKIDFILE/`).
  `target-checked` is exercised at `backup.Open`; wiring `Open` into daemon start belongs to ADR-0205 (enabling),
  which the ADR's Scope puts out.
- **Units the test plan names** all exist and pass: `TestCreateIfAbsentIsAtomic` (16 goroutines on `mem://` and
  `file://` through the new `blobcontract.CreateIfAbsentIsAtomic`, also run by `RunContract`), `TestFileTempHidden`
  (a crash's temp unlisted by `List` and `ListAfter`, `Exists` on it `fault.Invalid`, reserved/escaped/root-leaving
  keys and attributes refused, no temp of its own left), `TestProbeOutcomes` (`Conditional` false only for refused +
  `singleWriter`; another failure re-probed), `TestS3ConditionalConflictRetried` (4 PUTs then `fault.Unavailable`;
  412 is `Conflict` only under `IfNotExist`), `TestLifecycleRules`, `TestRecordFraming` (every torn prefix is
  `fault.Invalid`).
- **Decision 1**: `file://` create is temp `<key>.<16 hex>.funcd-tmp` 0600, `fsync`, `os.Link`, `EEXIST` →
  `Conflict`, temp removed by `defer`; keys `fileWalkPrefix` would cut or that leave the root are `fault.Invalid`;
  the suffix is reserved in `checkKey` and skipped by the walk (`internal/blob/gocloud/gocloud.go`); directories use
  `dir_file_mode` parsed decimal as fileblob does. `FailedPrecondition` maps to `Conflict` only under `IfNotExist`.
- **Decision 4**: `flock(LOCK_EX|LOCK_NB)` on `<dir>/lock` held for the target's life, held elsewhere ⇒
  `fault.Conflict` naming `<KeyPrefix>target` before the probe (so it beats `singleWriter`); same-device warning;
  the two-goroutine probe on one random `probe/<32 hex>` key; `fence.other`/`atN` implement the last, first-n and
  second-listing rules exactly; a refused run with a complete generation puts only the `refused` mark.
- **Decisions 2, 3, 5, 6**: 10-digit n across classes from one listing of `gen/`; framing
  `uvarint ‖ key ‖ uvarint ‖ value`; manifest fields and YAML keys match the table (`Keys` embedded, `parent`
  omitempty); `ClassFor` on UTC ISO week and UTC day; `LifecycleRules` ⌈hourly/24⌉, daily, 7 × weekly, verified
  (none at 0), `probe/` 1; config keys and env names (`FUNCD_BACKUP_*`) and defaults 48/30/12/2 in `defaults()`;
  `examples/funcdconfig.yaml` and `examples/backup-lifecycle.md` (lifecycle JSON, box policy denying
  `gen/verified/`, restore/verify policy, directory prune commands) as the plan lists.
- **Contracts**: `PutOptions.IfNotExist`, `gocloud.OpenOptions`/`OpenWith` (`Open` = `OpenWith(…, OpenOptions{})`),
  and every `internal/backup` type and function match the ADR's signatures. The only test hook is
  `export_test.go`'s `SetClock`.
- **Tree vs the Implementation plan**: the files touched are exactly the listed ones plus the tests and the S3 stub;
  `go.mod` moves `smithy-go` from indirect to direct, as the ADR's consumes table states, with no new module and no
  `go.sum` change.
- **Conventions (ADR-0002)**: errors through `api/fault` with the config key named; `log/slog` only; ctx first; no
  `panic`, no `fmt.Print*`, no `any` in exported APIs; each `//nolint` carries its reason; imports at top level.
- **Tracking**: the ADR diff is the single status line `Accepted → Reviewing`; Context, Scenarios, Decision and
  Contracts are unchanged.

### Definition of Done

15 / 15 hold: the ADR's 3 Review-checklist items, its 2 Definition-of-done items (race tests green on the touched
packages; the repo-wide `just ci` runs once at the PR gate) and the 10 Scenarios. No misses.

### Model scorecard

To record: claude-opus-5-5 on ADR-0203 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 15/15.

### Recommendation

Sign off: the ADR moves `Reviewing → Implemented`. The tier's tracking PR records the ledger row and moves the
FEAT-0009/F109 row to `implemented`.
