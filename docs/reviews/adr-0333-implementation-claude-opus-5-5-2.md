# ADR-0333 implementation review — claude-opus-5-5 (round 2)

## Verdict: pass — 0 blockers, 0 majors, 1 minor (ADR-0333 implementation, model: claude-opus-5-5)

Round 2 reviews commit bab8d26d of `feat/addons`. 041797d9 fixes round 1's two Majors and its Minor. The rest of the
range is ADR-0334's dev runtime and the edge's work, and it touches ADR-0333's code only in `reconcile.go` and
`addon_provider.go`, without changing this ADR's paths. Each fix holds: its test passes, a mutant that undoes it fails
that test, and round 1's overlay tests now show the right behavior. One Minor remains, a fourth kind comparison that
round 1 did not name.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **A Restore of a Backup taken before a kind's case changed fails once the object was written again** · attribution:
  model. `addonOf` (`internal/backup/workload/addons.go:105`) still compares the stored object's kind with the member's
  exactly. The API sets an object's `spec.kind` from its Addon at each write (`internal/controlplane/addon.go:443`), so
  after the Addon registers the kind again as `Identityprovider`, one update of the object stores it under that kind. A
  Backup taken before lists the member as `IdentityProvider/login`, and the Restore's plan needs the member named as the
  Backup lists it. An overlay test, added for this review and not kept, gave the Addon and the stored object the kind
  `Identityprovider` and asked `Destination` for the member `IdentityProvider/login`. It failed with "the add-on object
  login is of another kind", which the plan reports as `MemberNotFound`. No spelling of the kind passes both checks, so
  that Backup cannot be restored in place. The failure is loud, and a Backup taken after the update restores. Round 1's
  Major 2 named three places and missed this fourth comparison. Fix: use `v1.SameAddonKind` there too, with a test.

### Round 1's findings: closed

- **Major 1, the clock** (041797d9). `Newest` applies no age rule at a quiet of 0 (`internal/provider/files.go:64-65`),
  so the copy counts while it is there, whatever its time says. The Backup's quiet of 2 s keeps its rule.
  `TestRestoreKeepsTheMarkForACopyDatedAhead` passes, and a mutant that brings the age rule back at a quiet of 0 fails
  it. Round 1's end-to-end overlay test, adapted to the fix: with the placed copy dated one hour ahead and the restore
  start stalled, the Restore stayed `Running`, with the mark `Loading` and the copy in place. Once the start was
  unstalled, the Restore succeeded, the write made after the Backup answered 404, `restore.path` was empty, and the
  hold lifted.
- **Major 2, the plural** (041797d9). `v1.SameAddonKind` compares two kinds by plural. The backup member kind's
  `addonOf`, the group scope (through keys by plural), the reconciler's `addonOf` and `MapAddon` use it.
  `TestAddons_AnAddonOfTheSamePluralKeepsItsObjects` passes. A mutant whose `SameAddonKind` compares exactly fails it
  and `TestReconcileFindsTheAddonByPlural`. The comparison that remains exact is the Minor above.
- **The Minor, the hold until an engine without `restore.env` is Ready** (041797d9).
  `TestAddons_LoadPlacesTheCopyAndWaitsForTheMarkToGo` now asserts that the load has not ended at `Loaded`, and round
  1's surviving mutant, a wait that ends at `Loaded`, fails it.

### Routed (not this work's)

- **422 or 400 for `as`** (choice 7) · attribution: adr · the stamp proceeds. Round 1 held the `Implemented` stamp for
  the decider's ruling, but ADR-0313 already answers the question. It rejected treating such rows as questions for the
  decider, since "the experiment's 11 such rows are no question for the decider: a clause already answers". Here the
  clause is ADR-0148's: malformed input stays 400. ADR-0291 and ADR-0292 were stamped `Implemented` with the same
  `differs` row (`docs/behavior/admission.md:43`, `docs/behavior/limits.md:49`). The row stays `differs`, and the
  ADR's 422 stays an `adr` finding, so Review-checklist item 3 counts as a miss attributed to the ADR. Nothing waits on
  it.
- **Choice 3's restart question** · home: the decider · status: uncovered. Unchanged: an object deleted after a
  restart and before its first pass is quarantined, even under `deletion: delete`. The quarantine keeps its data.
- **Failures in the whole-package `pkg/funcd` run** · attribution: env.
  `TestBackup_GoesOnWhenItsListingFails/pages_with_no_key` and two subtests of
  `TestRestore_RefusesAMemberWithAnotherFileCount` (a KVStore and a Bucket, in place) hit their waits' deadlines in a
  `-race` run that took 234 s, against 149 s in round 1. Alone, every subtest of both tests passed three times out of
  three. bab8d26d's worker lookup in the target refusal is skipped for loopback addresses, which these tests' S3 stubs
  use, so it adds nothing to their connections.
- **The lane `addon-rauthy-restore`** · home: elsewhere, the final gate. It is still pending: at bab8d26d, `go.mod`
  does not pin `github.com/pyvvo/funcd-addons`, from which the lane stages rauthy's Addon. The lane YAML guards pass.
  The coordinator reports that funcd-addons' own e2e under `funcdctl dev` passed 31 of 31 checks on Docker, among them
  the backup, the in-place restore and `deletion: delete`. I did not run it, and it is not the containerd lane that the
  ADR names.
- **The dev runtime's `AddonFiles` drivers** · home: elsewhere, the ADR-0334 review. `internal/provider/docker`, and
  `internal/provider/nerdctl`, which embeds it, implement this ADR's port by comparing two listings `quiet` apart, so
  at a quiet of 0 they list twice in a row and count a copy that is there, with no clock. Their conformance to the port is ADR-0334's
  to review.

### The builder's seven choices

Choices 1, 2, 3 and 6 are as round 1 found them, and choice 4 is closed (Major 1 above). Choice 5: the refusal now
also reads, for every address that is not loopback, the address of each add-on engine's worker from the worker's
creation on (bab8d26d, through the helper of the egress gateway's index). An error from that lookup refuses the
connection. `TestBackupEnginesRefuseACreatedEngine` pins a created engine whose address no status names yet, and a
mutant without the worker lookup fails it. Choice 7 is routed above.

### ✅ Verified correct (keep it)

- **Checks.** `go build ./...` exits 0. `go vet` on the eleven touched packages exits 0, and on `cmd/funcdctl` with
  `-tags dev` too. `golangci-lint` reports 0 issues on them. Under `-race`, the tests of `api/types/v1alpha1`,
  `internal/backup/workload`, `internal/provider/...` (`docker` and `nerdctl` included), `internal/services/addon`,
  `internal/services/backup`, `internal/blob/gocloud`, `internal/controlplane/...` and `internal/platform/hold` pass,
  `cmd/funcdctl -run Hold` passes with and without `-tags dev`, and `pkg/funcd` passes except for the routed
  timeouts. `just check-behavior`, `just check-specs` and `just check-hygiene` exit 0. `restore-start.fizz` passes
  (69 states, 0.5 s), its five mutants fail as the ADR names, and `restore-start.md` is current.
- **Scenarios.** The nine scenario tests call `t.Parallel()` and pass under `-race`:
  `TestScenarioAddonRestoreInPlaceOnly` (0.82 s), `TestScenarioAddonBackupNoSnapshot` (0.89 s),
  `TestScenarioAddonBackupMember` (0.91 s), `TestScenarioAddonDeletion` (1.00 s), `TestScenarioAddonBackupGroupScope`
  (1.03 s), `TestScenarioAddonRestoreInPlace` (1.08 s), `TestScenarioAddonRestoreNoRollback` (1.15 s),
  `TestScenarioAddonRestoreInterrupted` (1.71 s) and `TestScenarioAddonRestoreTimeout` (3.06 s, with a
  `restore.timeout` of 2 s). `TestAddonBackupTargetIsNeverAnEngine` passes too (1.00 s).
- **Mutants.** Each of the four fails a test: round 1's wait that ends at `Loaded`
  (`TestAddons_LoadPlacesTheCopyAndWaitsForTheMarkToGo`), the age rule at a quiet of 0
  (`TestRestoreKeepsTheMarkForACopyDatedAhead`), an exact `SameAddonKind`
  (`TestAddons_AnAddonOfTheSamePluralKeepsItsObjects` and `TestReconcileFindsTheAddonByPlural`), and the refusal
  without the worker lookup (`TestBackupEnginesRefuseACreatedEngine`).
- **The other commits of the range.** ADR-0334's b8758e10 holds an object `Pending`, with `RuntimeUnavailable`, when
  its runtime can run no engine (`internal/services/addon/reconcile.go:92-95`), and its merge adds `AnyRegistry` to the
  Addon rules. Neither changes this ADR's paths. The edge's commits touch none of its files.
- **Tracking.** ADR-0333 is unchanged since round 1: since its acceptance, only its status line moved. F259 reads
  `reviewing`. The rows of `docs/behavior/addons.md` and `docs/behavior/disaster-recovery.md` are unchanged and still
  describe the code. The feature issue is #1499, for the PR to close.

### Definition of Done

16 of 19 items hold: six of the seven Review-checklist items (all but item 3), the check part of the Implementation
plan's done line (`just check-specs` and `just check-behavior`), and the nine applicable generic items. The miss is
item 3, the 422, attributed to the ADR. Two items are pending the final gate: `just ci` and the lane case.

### Model scorecard

To record, with the batch's ledger rows: claude-opus-5-5 on ADR-0333 (implementation, round 2) → pass, 0/0/1,
1 model-attributed, DoD 16/19. See docs/reviews/model-scorecard.md.

### Recommendation

Pass. The Minor is a one-line fix with a test, which the builder can fold into this PR before the stamp. The integrator
stamps `Implemented` once the final gate has run the lane case.
