## Verdict: changes requested — 0 blockers, 1 major, 4 minors  (ADR-0262 implementation, model: claude-opus-5-5)

This report reviews branch `feat/adr-0262-function-volumes` at 40306cac (base 621f7f25), PR #1192, together with
ADR-0266 and ADR-0268 (their own reports beside this one). ADR-0262 realizes FEAT-0014/F181; the PR closes feature
issue #1187.

### 🟡 Major 1 — an emptyDir that does not fit sets `Ready=False` `StartFailed`, not `VolumeCapacity`  ·  attribution: model
- The ADR: Decision 5 says "a worker whose `emptyDir` does not fit is not created and the Function is `Ready=False`
  with that reason"; scenario `capacity-not-overcommitted` says "leaves its worker unstarted with `Ready=False`
  `VolumeCapacity`".
- The code: `workerVolumes` (`internal/function/volumes.go:140`) returns the manager's `ErrCapacity` error to
  `workerSpec`. The caller in `internal/function/function.go` keeps it as `startErr`, and the verdict sets
  `Reason: "StartFailed"` (`internal/function/function.go:1172`). `volumeGate`, which maps `ErrCapacity` and
  `ErrBudget` to their reasons, runs only for `local` volumes in `ensureVolumes`. The text `VolumeCapacity` appears
  only inside the message.
- The test: `TestScenarioCapacityNotOvercommitted` (`internal/volume/volume_test.go:39`) checks the manager's
  `Reserve` error. No test checks the Function's Ready reason for the emptyDir half, so the gap is untested.
- Fix (builder): map an emptyDir booking error from worker create to the `VolumeCapacity` or `VolumeBudget` Ready
  reason, and extend the scenario test to assert the condition at the reconciler level.

### Minor
- **DataMissing clears on any volume Restore into the Function** · model · `volumesRestoredSince`
  (`internal/function/volumes.go:422`, match at `:433`) accepts a Succeeded Restore of any Function-volume member into
  `fn`. Decision 12 says the condition clears on a Restore "into that volume", meaning the volume that was lost. With
  two `local` volumes, restoring the other one clears the condition.
- **The allocator is rebuilt from the metas only** · model · `volume.New` (`internal/volume/volume.go:163`) books
  only volumes that have a meta. Decision 5 also names the stored Functions' `local` entries. If the daemon stops
  between the admission booking and the first `Reserve`, nothing holds that booking after the restart, and the next
  `Reserve` can fail on capacity. The window is small, and the Function then waits with `VolumeCapacity` instead of
  overcommitting.
- **The emptyDir leaf is named by a random token** · model · `workerToken` (`internal/function/volumes.go:150`). The
  path is `<name>.empty/<token>`, not `<name>.empty/<worker id>` as in Decision 3. The behavior is the same, and the
  token is a DNS label.
- **The lane's steps carry no scenario names** · model · in `e2e/volumes.venom.yml`, steps V1 to V8 prove
  `only-its-own-volume`, `write-past-size-fails`, `local-survives` and `emptydir-goes-with-worker` on real containerd,
  but no step names its scenario. Each scenario still has its own `TestScenario<Name>`.

### ✅ Verified correct (keep it)
- **Targeted tests pass.** `scripts/agent/d go test -count=1 ./api/... ./internal/volume/... ./internal/function/
  ./internal/controlplane/... ./internal/backup/workload/ ./internal/services/backup/ ./cmd/funcd/ ./pkg/funcd/ -run
  'Volume|Scenario|ByteSize|DeleteVolume|DataMissing|EnsureVolumes'` exited 0.
- **Lint is clean.** golangci-lint on the touched packages reported `0 issues.` and exited 0.
- **The root quota tests pass.** `just test-volumes-root` exited 0, with `TestScenarioWritePastSizeFails` and
  `TestImageGrowsAndRefusesShrink` PASS as root in the Linux VM.
- **The earlier gates passed.** On this branch, the repo-wide gate and `just lima-example volumes` (eight steps)
  passed, as the caller reported. They were not re-run here.
- **Every scenario has a named test.** All 12 scenarios have one `TestScenario<Name>` with its `// scenario:` tag:
  VolumePathInEnv, OnlyItsOwnVolume, WritePastSizeFails, LocalSurvives, EmptyDirGoesWithWorker,
  CapacityNotOvercommitted, NamespaceBudget, OneReplica, RetainedNotTakenOver, VolumesOff, DevMeasures and
  VolumeBackupRestore.
- **The image matches Decision 3.** `quotafs.Open` preallocates the image, runs `mkfs.ext4 -O quota,project -m 0`,
  mounts it `loop,nosuid,nodev,prjquota`, grows it with `e2fsck` and `resize2fs`, and refuses to shrink it. In memory
  mode the image lives in a temporary directory that the closer removes.
- **The host side follows no link (Decision 4).** `ensureLeaf` uses `Lstat` and refuses a symlink or a
  non-directory, and it changes the owner of the leaf only (`Lchown`). `usage` walks with `WalkDir`. `Tar` and
  `Untar` go through `os.Root`, and `Untar` checks each entry with `fs.ValidPath` and writes only into an empty leaf
  (`Conflict` otherwise).
- **Only the leaf is mounted.** Each volume is one bind mount at `/var/funcd/volumes/<name>`, `rw,nosuid,nodev`. The
  `.meta` file sits beside the leaf, so no mount includes it.
- **The allocator holds one lock.** `Book` and `Reserve` share `m.mu`. `TestConcurrentBookingsNeverOvercommit` runs
  20 concurrent bookings against 10 units. Unbacked bookings expire after `BookingTTL`, and a changed size is refused
  `Invalid`.
- **Ownership follows Decision 7.** `Reserve` refuses another UID with `StoreNotOwned`. `Adopt` refuses `Conflict`
  while a live Function holds the volume. A volume dropped from the spec follows its deletion policy.
- **The hold rule holds (Decision 8).** `volumesGone` and `ReclaimVolumes` skip their work while the platform is held,
  and `VolumeOrphans` feeds `hold status`. `DeleteVolume`, the API route and `funcdctl volume delete` refuse a held
  volume with `Conflict`.
- **Volumes are off without a manager.** The admission refuses `VolumesOff` when there is no manager (the process
  runtime, or no capacity). The admission books `local` sizes. `replicas > 1` with a `local` volume is refused, and
  the error names both fields.
- **Pooling changes only for volumes.** `KeyOf` adds `volume/<fn>/<name>` to the owned data, so a Function with a
  volume shares no worker. With no volumes, the access hash is unchanged.
- **The backup member works.** The scope form is `<function>/<name>` with no repeats, and an App's empty scope
  includes its Functions' `local` volumes. A restore member (`kind: Function`, `volume`, `as` or `inPlace`) loads only
  into an empty volume.
- **The ADR's substance is unchanged.** The diff of `docs/adr/0262-*.md` shows only the `Accepted → Reviewing` status
  move and the `Superseded in part by` back-link to ADR-0266. The F181 row is at `reviewing`.

### Definition of Done
8 of 9 Review-checklist items hold, and all 3 DoD items hold (12 items in all, 11 hold). The miss is the scenario's
emptyDir Ready reason (Major 1, model). The checklist items "the sum never passes capacity or budget under concurrent
applies" and "one test per scenario" hold.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0262 (implementation) → changes-requested, 0/1/4, 5 model-attributed, DoD 11/12. See
docs/reviews/model-scorecard.md.

### Recommendation
Loop back to `adr-impl` for Major 1: the worker-create emptyDir booking error should set the Ready reason, with a
reconciler-level test. ADR-0262 stays `Reviewing` and F181 stays `reviewing`. The Minors are optional.
