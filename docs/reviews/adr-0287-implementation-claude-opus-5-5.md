## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0287 implementation, model: claude-opus-5-5)

This review covers 219039a5 on `adr-0287-backup-sync`, the implementation on top of the docs commit 37f1f86a, against
ADR-0287's Decisions, Contracts, Scenarios, Review checklist and its done-when line. The checks below ran in a worktree
detached at 219039a5. The gate, the e2e suite and the lanes run once on the batch branch and did not run here.

### Checks run
- `go build ./...`: ok.
- `go vet` and `GOOS=linux go vet` on `api/types/v1alpha1`, `internal/backup/workload`, `internal/restore`,
  `internal/services/backup`, `internal/testkit/s3stub`, `cmd/funcd` and `pkg/funcd`: ok.
- `golangci-lint run` on the same packages: 0 issues.
- `go test -race -count=1` on the first six packages: ok (`cmd/funcd` 64.5 s). The seven scenario tests in `pkg/funcd`
  under `-race`, `TestScenarioRunInterrupted` included: all pass.
- `scripts/specs.py check docs/design/backup/target-sync.fizz`: pass, 1,848 states; the six mutants fail as the ADR
  names them; the diagram is current.
- Two mutants, run with `go test -overlay`: the slot logic without `!pending` (`schedule.go`) fails
  `TestScenarioUnreachableTargetRetried`, and the sync without its `c.Whole` check (`sync.go`) fails
  `TestScenarioIncompleteCopyNotRecorded`.

### Routed (not this work's)
None.

### ✅ Verified correct (keep)
- **Decision 1, the mark:** `holdNamespaces` sets `TargetSynced` False, `PlatformRestored`, on each schedule it lists,
  in the same loop that holds its namespace (`internal/restore/run.go:238-260`); `TestRestoreRun_MarksBackupSchedules`
  reads it back.
- **Decision 2, no slot while pending:** `Reconcile` computes `syncPending` from the stored status, starts the sync,
  and takes no slot while it is pending (`schedule.go:52-60`), held or not. The pass writes its status with the object's
  version, so a sync that wrote meanwhile turns the pass's write into a Conflict and a requeue: it never overwrites the
  sync's `TargetSynced` True.
- **Decision 3, what it lists:** `SlotCopies` lists after every key of `L`'s slot (`<L minutes>~`, which sorts after
  the `-<uid>/` of `L`'s directory), keeps a directory only when `ParseBackupSlotName(schedule, name)` parses and the
  slot is after `L`, and sorts the result by slot (`internal/backup/workload/copies.go:50-92`). `ParseBackupSlotName`
  refuses `a-1-<n>` for schedule `a`, leading zeros and negatives (`api/types/v1alpha1/backup.go:199-209`).
  `TestScenarioOnlyOwnSlots` covers a shared target with a Backup by hand and an export. Nothing reads an object:
  `ListCopy` and `SlotCopies` only list.
- **Decision 4, the record:** `sync` creates only whole copies, oldest first, with `startTime` the slot, `expireTime`
  the slot plus the schedule's ttl (never later than the original's, whose creation is after its slot),
  `completionTime` the manifest's modification time, `members` from the kind directories with a Lake's last attempt,
  the schedule's `resourceGroup` and no ownerReference. An expired copy gets no record, a Conflict counts as done, and
  `lastScheduleTime` and `TargetSynced` go in one status write after the records (`sync.go:48-102`, `:120-140`).
  `TestScenarioRestoreFindsNewerBackups` checks each field, then restores the newest record.
  `TestScenarioGoneBackupStaysGone` covers the three gone records, a second sync and the resumed slots.
- **Decision 5, a run the records lost:** `recoverCopy` lists the record's own directory with its schedule's
  credential. A whole copy makes the record `Succeeded`, `FoundOnTarget`, with its `startTime` and `expireTime` kept. A
  failed listing while the schedule's sync is pending leaves a `Running` record for the next pass (`unlisted`). Any other
  failure, or no manifest, goes on as before: `Interrupted` for a `Running` record, the copy for a `Pending` one
  (`sync.go:156-183`, `run.go:41-47`, `:75-90`, `:129-137`). `TestScenarioFinishedRunKept` covers both records and a target that
  answers only after the first listing; `TestScenarioRunInterrupted` still passes.
- **Decision 6, status and logs:** True with "found <n> Backups, newest <name>"; False, `ListFailed`, with the error,
  retried at most once per `Period` (`startSync`), and a Warn only on turning False. `TestScenarioUnreachableTargetRetried`
  shows no slot while the target refuses, even after `hold release --namespace`, then the records and one catch-up.
- **The builder's choices hold:**
  - the sync runs in a goroutine claimed like a Backup run, so a listing that waits on the target holds no controller
    worker;
  - the Backup reconciler requeues a `Running` record every `Period` while it looks at it, so an `unlisted` record gets
    another pass;
  - the new tests run their schedules in UTC; the fold behavior is ADR-0211's, not a defect.
- **Contracts:** `ConditionTargetSynced`, the three reasons, `ParseBackupSlotName`, `Copy`, `ListCopy` and
  `SlotCopies` match the ADR's signatures. `FailList` in the S3 stub answers 403, which a client does not retry.
- **Tracking:** the ADR's substance is unchanged since its acceptance commit (only the status line moved); F202 is at
  `reviewing`; the runbook and the workload backup guide wait for `TargetSynced` True before the owner restores.
- **Tests:** each of the five assertions has an oracle marker on its scenario test; the unit tests the plan names
  (`TestParseBackupSlotName`, `TestFindSlotsSkipsOtherSchedules`, `TestListCopyMembers`, the mark in restore run)
  exist and pass. The scenario tests live in `pkg/funcd/target_sync_internal_test.go`, beside the rig, rather than in
  the file the plan names; nothing depends on the file.

### Definition of done
6 of 6: the five Review checklist items and the done-when line (the touched packages' tests under `-race`, vet, lint
and `just check-specs`).

### Recommendation
Stamp ADR-0287 `Implemented` and F202 `implemented`.
