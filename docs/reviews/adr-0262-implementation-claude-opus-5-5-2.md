## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0262 implementation round 2, model: claude-opus-5-5)

This report re-reviews branch `feat/adr-0262-function-volumes` at 87fab687, PR #1192, after
[round 1](adr-0262-implementation-claude-opus-5-5.md) found one Major and four Minors. The PR closes feature issue
#1187.

### Minor
- **Boot bookings expire while the platform is held** · attribution: model · `bookStoredVolumes`
  (`internal/function/volumes.go`) books each stored Function's `local` entries at boot, even while the platform is
  held. These bookings have no leaf, so `dropStale` (`internal/volume/volume.go`) drops them after `BookingTTL` (1
  minute) unless a `Reserve` backs them. A hold that lasts longer gives up that space until the Function's next pass.
  The space is never overcommitted: the pass then gates the Function `VolumeCapacity`. Non-blocking.

### ✅ Verified correct (keep it)
- **Round-1 Major fixed.** `startReason` maps `ErrCapacity` and `ErrBudget` to the Ready reasons `VolumeCapacity` and
  `VolumeBudget` at the `Ready=False` site in `finish`. `TestScenarioEmptyDirOverCapacityIsNotReady`
  (`internal/function/volume_capacity_test.go`) drives a real Reconcile. It asserts the reason and that no worker is
  created.
- **Round-1 Minor 1 fixed.** DataMissing names the lost volumes, and only Succeeded Restores that cover every one of
  them clear it (`TestDataMissingAfterLoss`).
- **Round-1 Minor 2 fixed.** At boot, the allocator books the stored Functions' `local` entries.
- **Round-1 Minor 3 accepted as it is.** The per-create token is the worker's id for its emptyDir leaf. It is a DNS
  label, unique per worker create, so it fits Decision 3's `<worker id>`.
- **Round-1 Minor 4 fixed.** The volumes suite's header maps its steps to the scenarios they prove.
- **ADR-0268 Minor fixed.** The F181 row links ADR-0268.
- **Targeted tests pass.** `scripts/agent/d go test -count=1 -race ./internal/function/ ./internal/volume/... -run
  'EmptyDirOverCapacity|DataMissing|Volume'` exited 0. A wider run, `-run 'Scenario|Concurrent|Reclaim|DataMissing'`
  on `internal/volume`, `internal/function`, `pkg/funcd` and `cmd/funcd`, exited 0.
- **Lint is clean.** golangci-lint on `internal/function` and `internal/volume` reported `0 issues.`
- **Everything else from round 1 still holds.** The fix touches no path that round 1 verified.

### Definition of Done
12/12 (9 Review-checklist + 3 DoD items) hold.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0262 (implementation, round 2) → pass, 0/0/1, 1 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation
Stamped `Implemented`, with F181 moved to `implemented`. The PR closes #1187.
