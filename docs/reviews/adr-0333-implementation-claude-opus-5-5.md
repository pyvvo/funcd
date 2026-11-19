# ADR-0333 implementation review — claude-opus-5-5

## Verdict: changes requested — 0 blockers, 2 majors, 1 minor (ADR-0333 implementation, model: claude-opus-5-5)

The work is ADR-0333 at commit 689b6b6c of `feat/addons`: e15e8f19, merged in fa7a6e2b, and 5614d18f, read with the
integration commits 74b31a0e and d4f07fc9 where they touch the same code. Each of the nine scenarios has a named test,
and every one passes under `-race`. The design model passes with every mutant failing as named, and the touched
packages build, vet, lint and test clean. Two defects break the ADR's guarantees on uncommon paths: a Restore can
report success without loading its copy when the copy's modification time is ahead of the clock, and a Backup leaves
out an add-on object whose Addon registers the kind again in another case. Each fix is a few lines with a test.

### 🔴 Blockers

None.

### 🟡 Majors

#### Major 1 — whether the copy is still there depends on the clock · attribution: model

`hasCopy` (`internal/services/addon/restore.go:82-95`) asks `Newest(..., restore.path, 0)` whether the copy is still in
`restore.path`. `EngineFiles.Newest` skips each file with `now.Sub(fi.ModTime()) < quiet`
(`internal/provider/files.go:64`). With a quiet of 0, it skips a file whose modification time is ahead of the
reconciler's clock, so such a copy reads as gone. That happens after a backward clock step between the copy's write
and a pass, such as an NTP step, or on a filesystem whose timestamps come from a clock ahead of the host's, such as a
network filesystem's server.

Then `restoreStep` turns the mark `Loaded` with the copy still in place, no engine starts with `restore.env`, and the
next Ready engine clears the mark. Two overlay tests, added to the packages for this review and not kept, show it:

- In `internal/services/addon`, the rig's Restore placed its copy, dated one hour ahead. After one pass,
  `status.restore` was nil, no engine had started since the mark, `restore.path` still held `backup_1.json`, and the
  engine's items were empty.
- In `pkg/funcd`, the same date on the placed copy, with the restore start stalled, ended with the Restore
  `Succeeded` and the namespace hold lifted. A write made after the Backup still answered 200 with its value, and the
  copy stayed in `restore.path`.

This breaks Decision 4 ("sets `Loaded` only once the copy is gone"), Review-checklist item 4, the design model's
`TheCopyIsLoaded`, and the scenario `addon-restore-in-place`'s "`restore.path` is empty". The mark names the copy
(`status.restore.file`), so the check needs no clock. Fix: make `Newest` apply no age rule when quiet is 0 or less
(one condition at `files.go:64`), or check for the named file. Pin it with a test whose copy is dated ahead of the
clock. This is the builder's choice 4.

#### Major 2 — the backup member kind matches an object's Addon by exact kind · attribution: model

ADR-0332 Decision 1 compares add-on kinds without case, and the API finds an object's Addon by plural
(`internal/controlplane/addon.go:373` and `:383`). It already did so at e15e8f19's parent, 986f53b5. d4f07fc9 moved
the reconciler and its Addon watch to the plural match (`internal/services/addon/reconcile.go:155`, `addon.go:166`),
so that "an Addon that registers the kind again in another case runs the objects stored under the first". The backup
side was written before d4f07fc9 and merged after it, and it still matches exactly, in three places:

- `internal/backup/workload/addons.go:113` (`addonOf`, which `Write` and `Destination` use);
- `internal/backup/workload/workload.go:301` and `:309` (the resource-group scope).

An Addon's kind cannot change, so an admin who fixes a kind's casing deletes the Addon and registers it again. After
that, the object runs Ready under the new Addon, but:

- a resource-group Backup leaves it out, with no failure;
- a Backup that names it fails `NoSnapshot`, with a message that no Addon registers its kind;
- an in-place Restore of it fails.

An overlay test in `internal/backup/workload` gave the Addon the kind `Identityprovider` and kept the stored object's
`IdentityProvider`. The group scope resolved no member, and `Destination` failed with "no Addon registers the kind
IdentityProvider". The merge exposed the gap, but the gap is this change's own, since the API already matched by plural
at its base. Fix: compare `v1.AddonPlural` in the three places, as d4f07fc9 did, with a test.

### Minors

- **No test pins the hold until an engine without `restore.env` is Ready** · attribution: model. A mutant whose wait
  ends when the mark turns `Loaded` (`addonDestination.loaded`, `internal/backup/workload/addons.go:294`) passes every
  test of `internal/backup/workload`, `internal/services/addon` and the `pkg/funcd` scenarios.
  `TestAddons_LoadPlacesTheCopyAndWaitsForTheMarkToGo` writes `Loaded` and removes the mark with no check between them.
  `TestScenarioAddonRestoreInPlace` reads the engine after the Restore succeeded, and the fake engine reaches that state
  within milliseconds with or without the mutant. The code itself is right: Decision 3's wait ends only once
  `status.restore` is gone. Fix: after the test writes `Loaded`, assert that the load has not ended, as it already does
  after `Loading`.

### Routed (not this work's)

- **422 or 400 for `as` on an add-on object's member** (choice 7) · home: elsewhere, the decider · attribution: adr.
  Decision 3, the scenario `addon-restore-in-place-only` and Review-checklist item 3 say 422. The refusal is
  `RestoreMember.validate`'s `fault.Invalid`, which `api/fault` maps to 400. ADR-0148 decides that malformed input stays
  400, with one status map, and ADR-0333 does not supersede it. The registry row says `differs` and cites ADR-0148, as
  the rows of ADR-0291, ADR-0332 and ADR-0335 do. Checklist item 3 cannot hold as written without breaking ADR-0148, so
  the `Implemented` stamp waits for the decider's ruling. It is the same question that those rows ask.
- **A failure in the whole-package `pkg/funcd` run** · attribution: env.
  `TestBackup_GoesOnWhenItsListingFails/pages_with_no_key` hit its wait's deadline in the `-race` run of the whole
  package. Alone, it passed three times out of three, in 2.1 to 2.2 s. With the target refusal switched off by an
  overlay, it took 2.3 to 3.2 s. The failure comes from the host's load, not from this change.
- **The lane `addon-rauthy-restore`** · home: elsewhere, the final gate. It is written (`scripts/lanes.yaml`,
  `e2e/addon-rauthy-restore.venom.yml` and its fixtures), and the lane YAML guards pass (`go test -run Lane
  ./scripts/...`). It stages rauthy's Addon from `github.com/pyvvo/funcd-addons`, which `go.mod` does not pin yet, so
  it has not run. Its two real-platform steps are pending: hiqlite's restore at a start after a graceful replace, and
  its normal start over its crash marker after funcd's kill. The Addon in that repository must declare `snapshot` and
  `restore`.
- **The deletion policy after a restart** (choice 3) · home: the decider · status: uncovered. The reconciler keeps an
  object's policy in memory. An object deleted after funcd restarts and before its first pass is therefore quarantined,
  even under `deletion: delete`. This keeps the data, and no clause speaks of the case. The registry row "Deleting an
  add-on object" could name it.

### The builder's seven choices

1. **`Serves` gives `Addons` every kind whose `Kind.Validate()` fails.** `Kind.Validate()` lists funcd's stored kinds.
   It leaves out the two authorization-only kinds `S3Identity` and `NetDestination`, but neither reaches `Serves`: a
   Restore's members pass `RestoreMember.validate`, which takes a kind that is not funcd's only through `IsAddonKind`,
   and a Backup's member kinds come from stored objects. So the predicate matches the ADR's "every kind funcd does not
   define" wherever it is called. `TestServes` pins it.
2. **The Restore's name through `workload.WithRestore`.** `RestoreReconciler.load` sets it first
   (`internal/services/backup/restore.go:168`), and `plan`, the only caller of `Destination`, runs after it. So the
   `Internal` refusal is a closed guard that no path reaches today. The context value keeps ADR-0311's `Destination`
   contract, which ADR-0333 does not supersede, unchanged.
3. **The UID and the deletion policy in memory.** An object deleted and created again between two passes has its old
   engine stopped and its storage reclaimed by the old policy first (`TestAnObjectCreatedAgainStartsEmpty`). The SQL
   database reconciler does not detect that case at all. While the platform is held, nothing moves and the new object
   keeps the old storage, which ADR-0231 Decision 4 and the runbook ("apply, while held, the objects whose data stays")
   decide. The uncovered point is routed above.
4. **`Newest(..., quiet 0)` as the check for the copy.** Major 1.
5. **The backup target refusal.** It applies ADR-0332 Decision 3: a backup target's S3 client acts for the schedule's
   owner, and no funcd proxy that acts for a workload connects to an add-on engine's address. The refusal runs in the
   dialer's `ControlContext`, after name resolution, at each connection. A refused dial is not retried
   (`RetryableError()` is false). The option is set before the static key's `cappedAnswers` wraps the client, so the
   answer cap stays. `TestEnginesTellAnEngineAddress` pins the loopback rule (address and port) and the bridge rule
   (the whole address, IPv4-mapped forms included), and `TestAddonBackupTargetIsNeverAnEngine` pins the refusal end to
   end. The rule is ADR-0332's, placed in this commit, which the ADR-0332 review may want to know.
6. **`hold.Evidence.AddonOrphans`.** ADR-0231 Decision 4, on which Decision 5 builds, requires it: "While held, the
   directories `List` names without a resource are restore evidence: the owner reports them in `hold status`".
   `TestEveryReclaimHasHoldEvidence` enforces it for every boot reclaim, and the registry row cites both ADRs. It is an
   obligation of the ADRs, not scope creep.
7. **400 for `as`.** Routed above, attribution adr.

### ✅ Verified correct (keep it)

- **Checks.** `go build ./...` exits 0. `go vet` on the eleven touched packages exits 0, and on `cmd/funcdctl` with
  `-tags dev` too. `golangci-lint` reports 0 issues on them. Under `-race`, the tests of `api/types/v1alpha1`,
  `internal/backup/workload`, `internal/provider`, `internal/services/addon`, `internal/services/backup`,
  `internal/blob/gocloud`, `internal/controlplane/...` and `internal/platform/hold` pass, `cmd/funcdctl -run Hold`
  passes with and without `-tags dev`, and `pkg/funcd` passes except for the routed timeout. `just check-behavior`,
  `just check-specs` and `just check-hygiene` exit 0.
- **Scenarios.** Each has one named test that calls `t.Parallel()` and passes under `-race`:
  `TestScenarioAddonBackupMember` (0.55 s), `TestScenarioAddonBackupNoSnapshot` (0.53 s),
  `TestScenarioAddonBackupGroupScope` (0.55 s), `TestScenarioAddonRestoreInPlace` (0.63 s),
  `TestScenarioAddonRestoreNoRollback` (0.72 s), `TestScenarioAddonRestoreTimeout` (2.64 s, with a `restore.timeout` of
  2 s), `TestScenarioAddonRestoreInterrupted` (0.93 s), `TestScenarioAddonRestoreInPlaceOnly` (0.51 s) and
  `TestScenarioAddonDeletion` (0.59 s). No timeout was raised.
- **Design model.** `restore-start.fizz` passes (69 states, 0.4 s). `lift-early`, `clear-first`, `loaded-first`,
  `never-clear` and `keep-mark` fail as the ADR names, and `restore-start.md` is current. Each assertion has an oracle
  marker on a passing test: `TheCopyIsLoaded` and `TheRestoreEnds` on `TestScenarioAddonRestoreInPlace`,
  `NoAckedWriteRolledBack` on `TestScenarioAddonRestoreNoRollback`, and `TheEngineRunsWithoutRestoreEnv` on
  `TestScenarioAddonRestoreInterrupted`.
- **Mutants.** A mutant that removes the copy before the engine with `restore.env` is Ready (`restoreMoves`) fails two
  unit tests and four scenarios. A mutant that keeps the copy when the Restore has ended (`restoreStep`) fails
  `TestRestoreThatEndedIsCleared` and `TestScenarioAddonRestoreInterrupted`. The third mutant is the Minor.
- **The reconciler's moves** follow Decision 4 and the model. The provider runtime retires an engine whose fingerprint
  changed and reports the next one not Ready on the pass that starts it (`internal/provider/runtime.go:182-257`), so
  `Ready` always means an engine of the current env. The copy goes only once an engine with `restore.env` is Ready,
  `Loaded` follows, the engine starts again without `restore.env`, and the mark goes once that engine is Ready. The
  mark of a Restore that ended or is gone goes, the copy first, under the kept hold. The containerd stop sends SIGTERM,
  waits the grace, then sends SIGKILL.
- **The load** (`internal/backup/workload/addons.go:183-202`) empties `restore.path`, writes the copy owned by the
  storage's uid and gid, marks `Loading`, and waits on a watch under `context.WithTimeoutCause`. Past the timeout it
  fails `RestoreTimedOut`. A stop's cancellation stays a plain error, so the next start ends the Restore `Interrupted`.
  Only a completed load releases the hold. The load's own clear removes the copy, then only its own mark.
- **File access** (`internal/provider/files.go`). Every call opens an `os.Root` on `<root>/addonobject/<ns>/<name>`. A
  folder must pass `fs.ValidPath`, and a file name must pass `IsAddonFile`. `Newest` takes regular files only, so a
  link is no candidate, and it checks with `os.SameFile` that the file it opened is the one it chose.
  `TestEngineFilesStayInsideTheStorage` pins that a link out of the storage is refused.
- **Validation** (`api/types/v1alpha1/addon.go`). `snapshot.path` and `restore.path` need storage, and each lies
  strictly under `storage.path`, apart from the other. The keys of `restore.env` are env names, and its expressions read
  only `config` and `restore.file` (`CheckRestoreEnv`, at admission). `restore.timeout` is 1 s to 1 h, 10 min unset.
  `spec.deletion` is `retain`, `delete` or unset.
- **Deletion** follows Decision 5. `delete` reclaims the storage once the engine is gone. `retain` renames it into
  `<dataDir>/providers/.quarantine/addonobject/<ns>/<name>/<since>`. The boot's `Reclaim` quarantines the storage of
  every object no longer stored, whatever its policy. Nothing moves while the platform is held. The quarantine is in
  `quarantineStores`, for the sweep and `hold status`.
- **Contracts** match the ADR: `AddonSnapshot`, `AddonRestore`, `BackupScope.Addons`, `AddonObjectStatus.Restore`,
  `AddonRestoreState`, `AddonFiles`, `Addons` and `Serves`, the reasons `NoSnapshot` and `RestoreTimedOut`, and
  `addonSnapshotQuiet` of 2 s.
- **Tree.** The files match the Implementation plan, and each extra one is explained: the target refusal (ADR-0332
  Decision 3), the hold evidence (ADR-0231 Decision 4), the fake engine that the plan's tests call for, the lane, the
  OpenAPI golden and the guide pages. e15e8f19 adds no dependency, and ADR-0002's conventions hold: no `any` in the new
  APIs, `api/fault` errors, the context first, and no panic or print.
- **Behavior rows** match the ADR's section. The in-place row says `differs` (routed above), and two more rows record
  what the Decisions imply: the boot's quarantine and the target refusal.
- **Tracking.** ADR-0333 reads `Reviewing`, with its substance as accepted: only its status line changed since
  955563f3. F259 reads `reviewing`. ADR-0311 carries the `Superseded in part by` line. The feature issue is #1499, for
  the PR to close.

### Definition of Done

14 of 19 items hold: four of the seven Review-checklist items (2, 5, 6 and 7), the check part of the Implementation
plan's done line (`just check-specs` and `just check-behavior`), and the nine applicable generic items. Misses:
checklist item 1 (Major 2: a group scope leaves out an object whose Addon declares a snapshot) and item 4 (Major 1),
both attributed to the model, and item 3, the 422, attributed to the ADR. Two items are pending the final gate:
`just ci` and the lane case.

### Model scorecard

To record, with the batch's ledger rows: claude-opus-5-5 on ADR-0333 (implementation) → changes-requested, 0/2/1,
3 model-attributed, DoD 14/19. See docs/reviews/model-scorecard.md.

### Recommendation

The builder fixes Majors 1 and 2 and the Minor in this round, each with its test. The 422 waits for the decider's
ruling, with the rows of ADR-0332 and ADR-0335. The final gate then runs the lane case.
