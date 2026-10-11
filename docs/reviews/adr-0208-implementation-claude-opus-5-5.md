## Verdict: pass — 0 blockers, 0 majors, 3 minors (all `adr`)  (ADR-0208 implementation, model: claude-opus-5-5)

Scope: `git log 77c84f29..HEAD` on `feat/adr-0208-blob-store-backend` (7 commits, 37 files, +4586/-388). The base
includes the unmerged ADR-0207 (PR #920), which this review does not cover.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build (darwin) | `scripts/agent/d go build ./...` | exit 0 |
| vet (darwin) | `go vet` on blob, backup, config, testkit/s3stub, cmd/funcd | exit 0 |
| build (linux) | `GOOS=linux go build ./...` | exit 0 |
| vet (linux) | `GOOS=linux go vet` on the same packages | exit 0 |
| lint (darwin) | `go tool golangci-lint run` on the same packages | `0 issues.`, exit 0 |
| lint (linux) | `GOOS=linux <golangci-lint> run` on the same packages (the gate's form) | `0 issues.` |
| tests | `go test -race -count=1` on the same packages | all `ok`, exit 0 (blobmirror 2.9 s, gocloud 3.4 s, config 3.4 s, cmd/funcd 33.9 s) |
| e2e scenario | `go test -tags e2e -race -run '^TestScenarioRemoteStoreServes$' ./pkg/funcd/` | `--- PASS (0.40s)`, `t.Parallel()`, no raised timeout |
| fmt / tidy | `gofmt -l` on changed Go files; `go mod tidy -diff` | no output; exit 0; `go.mod`/`go.sum` untouched |
| tree | `git status --porcelain` | clean |

Not run here (the gate runs them): `just ci-full`, repo-wide `go test ./...`, the Lima lanes.

### Mutants (each run through `go test -overlay`, the work untouched)

| Mutant | Line | Killed by |
|---|---|---|
| M1: never reuse an epoch's complete upload | `internal/backup/blobmirror/mirror.go:310` (`lowest() != nil && false`) | `TestScenarioMirrorIncremental` — "should have 2 item(s), but has 4" |
| M2: drop the non-empty destination refusal | `internal/backup/blobmirror/restore.go:68` | `TestScenarioMirrorRestore` |
| M3: same bucket is only the provider | `internal/blob/gocloud/domains.go:55` | `TestCompareDomains` and `cmd/funcd` `TestScenarioTargetInsideStoreRefused` |

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **`blobmirror.Config.RetryInterval` is not in the Contract** · attribution: `adr` (DR answer Q9). The Contract's
  `Config` has no retry time, so after a failed run the newest manifest stays old and `Loop` would be due at once: a
  hot loop against a down target. The work adds `RetryInterval` from `backup.retryInterval` and sets due to
  min(run end + RetryInterval, run start + Interval) (`mirror.go` `Loop`), tested by `TestLoopRetriesAfterFailure`.
  Fix: DR's superseding addendum to ADR-0208.
- **`restore blob` writes the hold marker** · attribution: `adr` (DR answer Q15, on ADR-0206 Decision 5). ADR-0206
  names only `Begin`, `End` and `Own`; a blob-only restore would otherwise leave the next start unheld while every blob
  EventSource fires on the restored keys. `beginBlobHold` (`cmd/funcd/restore.go`) calls `hold.Write` after
  `hold.Begin` and removes a marker it wrote on an early failure; `TestRestoreBlobCommand` and
  `TestRestoreBlobAtCommand` check the marker. Fix: DR's addendum.
- **`--store-credentials-file` is declared by this ADR's work** · attribution: `adr` (DR answer Q5). ADR-0206 lists the
  flag but declares none; ADR-0208 calls it NEW. The work registers it on `restore blob` with default
  `blob.credentialsFile` (`newRestoreBlobCmd`). Fix: none needed in code; the ADR pair should agree in the addendum.

### ✅ Verified correct (keep it)
- **Contracts match**: `blob.Versioned` (`Versioning`, `RestoreAt(ctx, at, keep)`), `Versioning`, `RestoreReport`,
  `NoSign`, `gocloud.Overlap`/`CompareDomains`/`WalkFileKeys`, `config.CheckBlob() []Finding`, and the `blobmirror`
  surface (`Format`, `Config`, `Manifest`, `Object`, `Entry`, `Mirror`, `New`, `List`, `Restore`, `LifecycleRule`) have
  the ADR's names and signatures; only `s3://` buckets satisfy `Versioned` (`TestVersioningAnswers` checks `mem://` and
  `file://` do not).
- **Decision 1**: the eight keys with their env names and defaults (`config.go`, `blob.go`); `Load` derives `blob.dir`
  only in file mode without a target and makes a set one absolute, so `target`+`dir` stays visible (DR Q10);
  `CheckBlob` reads only the config, errors first, each naming its key; under `storage.mode: memory` one warning lists
  the ignored keys (`TestCheckBlobRows`, `TestScenarioBlobKeysCheckedLoad`, `TestScenarioBlobKeysChecked` through
  `serve`). `examples/funcdconfig.yaml` stays commented block-style YAML.
- **Decision 2**: `remoteStore` opens `blob.target` with `blob.credentialsFile`, reads versioning then the lock through
  the SDK client; off, suspended, 403 or 501 refuse with `fault.Invalid` naming `blob.allowUnversioned` (warning with
  it true); no lock warns once; `NoSuchBucket` refuses naming `blob.target`; a dropped connection is
  `fault.Unavailable` — ten cases in `TestScenarioStoreProtectionChecked`. The store is served through `blob.NoSign`
  (`TestRemoteStoreOpensNoLocalDir`, `TestNoSign` with `RangeReader` forwarding).
- **Decision 3**: endpoint normalization (case, default ports, trailing `/`, empty ⇒ AWS in region), `EvalSymlinks`
  through the nearest existing ancestor, `Dev` comparison; the daemon refuses `backup.target` and an enabled
  `kvstore.backup.target` inside the store and warns on the same provider, the local store compared as
  `FileURL(blob.dir)`.
- **Decision 4**: one run at a time (mutex), `Ready` before any request (`TestRunWaitsForReady` sees zero requests),
  one listing of `blob/`, n = highest + 1 across epochs, epoch e+1 past `rebaseline`, `IfNotExist` set from
  `Conditional`, parts before the `sha256-` key, index then manifest last, never `Get`/`Attributes`/`Delete` on the
  target (`requireBoxOnly`), the image removed on every path; the object id bytes as DR Q12 fixed them
  (`TestObjectIDStable` pins a golden id); 8 MiB parts with one part of memory (`TestLargeObjectPartedBoundedMemory`,
  re-upload after a crash past part 0); `LifecycleRule` ⌈(r+t)/24h⌉ logged at start (60 days at defaults).
- **Decision 5**: hard-link image at `<blob.dir>-frozen`, emptied first; checkpoint linked and MD5-checked before a new
  walk of its prefix; a gone key or changed checkpoint re-freezes the prefix; the fourth re-freeze fails with
  `fault.Conflict`; `EXDEV` reads live with one warning — `TestScenarioMirrorFrozenImage`,
  `TestScenarioCatalogCheckpointFirst`, `TestAttrsMismatchRefrozen`, `TestCheckpointChangedAfterListingRelistsPrefix`,
  `TestMissingCatalogKeyRefreezesPrefix`, `TestFreezeEXDEVReadsLive`. `WalkFileKeys` reuses the store's `fileWalker`
  rather than a second decoder.
- **Decision 6**: `Restore` calls `open` once (`TestRestoreOpensOnce`), checks every index and object part and
  `sha256-` key before the first write, refuses a non-empty destination with `fault.Conflict`, refuses an altered
  object with `fault.Invalid` and deletes what it wrote; an unsealed generation's user bytes come back as is, age header
  included (DR Q7). `RestoreAt` checks the window, plans every key first (a version over 5 GiB refuses before any write),
  copies from a `versionId` or adds a marker, removes no version, lists `NoVersion`, and a rerun completes a partial
  restore (`TestScenarioRemoteRestoreAt`, `TestRestoreAtRefusesLargeVersion`, `TestRestoreAtErrorKeepsDone`).
- **Daemon wiring**: `mirrorFor` runs only for a local file store with the platform backup on, takes `Ready` and
  `Conditional` from the platform target, the sealer's `Seal` and `Keys().Recipients`, `Recorder("blob")` and the hold
  inside `buildOptions` (DR §8 update); its closer waits for `Loop` and closes its bucket; a failed `buildOptions`
  closes it with the other opened drivers. `TestBlobMirrorAssembledWithHold` shows no generation while held and one
  after release. `Loop` records no cancel and nothing while held (`TestLoopSkipsWhileHeld`,
  `TestLoopCancelRecordsNothing`).
- **Every Scenario has a named, un-skipped, passing test**: remote-store-serves (`pkg/funcd`
  `TestScenarioRemoteStoreServes` + `cmd/funcd` `TestRemoteStoreOpensNoLocalDir`), store-protection-checked,
  blob-keys-checked, target-inside-store-refused, mirror-incremental, mirror-frozen-image, catalog-checkpoint-first,
  mirror-epoch-rolls, mirror-restore, remote-restore-at. Every unit the Test plan names exists and passes.
- **Reuse**: the S3 test stub moved to `internal/testkit/s3stub` and gained versioning, delete markers and
  `CopyObject`; ADR-0203 internals exported once (`backup.PartBytes`, `backup.TargetURL`) as DR Q14 chose; no new
  module.
- **Tracking**: the ADR diff is the status line only (`Accepted → Reviewing`), substance unchanged; `docs/feat` is
  untouched (DR owns the F111 row); the blueprint is not touched (its sync was due at acceptance, outside this work).
  Operator docs (`examples/backup-lifecycle.md` blob rule, store and box policies; `examples/restore-runbook.md`)
  follow the code.

### Definition of Done
13 / 13 hold (ADR Review checklist 3 + generic DoD 10; the ADR's own DoD items — `-race` tests, no new module, no
leak — are inside them). `just ci` itself is left to the gate; its sub-checks for the touched packages passed above.
Misses: none.

### Model scorecard
Not recorded here (the gate's caller records the ledger). Row to record: claude-opus-5-5 on ADR-0208
(implementation) → pass, 0/0/3, 0 model-attributed, DoD 13/13.

### Recommendation
Pass. The three minors are ADR gaps already answered by the DR session (Q5, Q9, Q15) and belong in DR's superseding
addendum; nothing loops back to the builder. The gate's `just ci-full` and Linux run remain the last checks before
stamping `Implemented`.

```json
{"adr": "0208", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "pass", "blockers": 0, "majors": 0, "minors": 3, "model_attributed": 0, "dod_passed": 13, "dod_total": 13, "report": "docs/reviews/adr-0208-implementation-claude-opus-5-5.md", "notes": "pass; 3 minors all adr: RetryInterval missing from blobmirror.Config (Q9), restore blob writes the hold marker beyond ADR-0206 D5 (Q15), --store-credentials-file declared here not by ADR-0206 (Q5); 3 mutants killed; e2e remote-store-serves 0.40s"}
```
