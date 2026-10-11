## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0222 implementation, round 3, model: claude-opus-5-5)

Round 3 reviews the commits after the round-2 report (`docs/reviews/adr-0222-implementation-claude-opus-5-5-2.md`), the
range `6c95d99d...de281726` on `feat/adr-0222-workload-backup-resources`:

- `07df1654`: the in-place load lifts its marker only after every member has loaded, through `ReleaseNamespaceMarker`
  and with no defer. The `Hold` interface of `services/backup` swaps `ReleaseNamespace` for `ReleaseNamespaceMarker`.
  The commit adds `TestRestoreStopKeepsNamespaceHeld`.
- `de281726`: the delete of an in-place Restore that is `Pending` or `Running` is refused at admission. The commit adds
  `TestRestoreInPlaceDeleteWaitsForEnd`.

The bar is the same as in round 2: ADR-0222 Decisions 7 and 8 and ADR-0206 Decisions 5 and 6. A half-written in-place
restore is never released silently, and a running Restore refuses the release. The repo-wide gate runs before the PR.

### 🔴 Blockers

None.

### 🟡 Majors

None. Round 2's Major 1 is resolved (see "Verified correct").

### Minor

- **A later in-place Restore lifts the hold that an earlier failed one left, whatever members each one covers**
  · attribution: `adr`. Decision 8 keeps one `<ns>.restore` marker per namespace, and Decision 7 says an in-place
  Restore "holds the namespace … lifts it". The second Restore's `HoldNamespace` therefore rewrites the marker that
  named the first, and its own `ReleaseNamespaceMarker` then lifts it. A scratch test (run through `go test -overlay`,
  not committed) showed this:
  - The first in-place Restore of `orders-files` failed on its second object, and its purge was refused. The log read
    `HeldIn(orders)=true marker=first bucket objects=1`.
  - A second in-place Restore that covers only the KVStore was then admitted, because the first had ended, and it
    succeeded. The log read `HeldIn(orders)=false markers=map[] orders-files objects=[a.csv]`.

  The Bucket that the first Restore half-wrote was released without `hold release --namespace`. This contradicts the
  example text "keeps the namespace held … check the store, then `funcdctl hold release --namespace`". A decision for
  the second addendum: either refuse an in-place Restore while the restore marker names another Restore, so that the
  release stays the only lever, or state that a completed in-place Restore releases the namespace.
- **Behaviour beyond ADR-0222's text** · attribution: `adr`, by ruling of the DR owner, to be recorded in the second
  addendum together with round 2's list. The fixer lists four consequences, and none of them breaks a Decision:
  - A failed in-place load stays held until it is released. Decision 7 does not say whether a failure lifts the hold,
    so keeping it is the fail-closed reading.
  - A stop leaves the Bucket or catalog objects that the cancelled purge did not remove. This is the same as a crash,
    which rounds 1 and 2 accepted with the hold as the fence. The addendum should say what Decision 7's "a failure
    empties what it wrote" means for an interrupted load, or have the next start empty the in-place targets.
  - A `Pending` in-place Restore cannot be cancelled. This narrows Decision 10's delete. A lever remains: deleting the
    Backup it waits for ends the Restore `Failed`.
  - A forced ResourceGroup or Namespace delete bypasses the admission, as ADR-0170's `force` does for every member.

### ✅ Verified correct (keep it)

- **Build, vet, lint, format**: `scripts/agent/d go build ./...` exit 0. `go vet` exit 0 over the four touched
  packages: `internal/controlplane`, `internal/controlplane/admission`, `internal/services/backup` and `pkg/funcd`.
  `go tool golangci-lint run` over the same packages: `0 issues.`, exit 0. `gofmt -l` on the changed files prints
  nothing.
- **Tests**: `go test -race -count=1` exits 0 over those packages and `cmd/funcdctl`: `pkg/funcd` 19.3 s,
  `internal/controlplane` 12.3 s, `cmd/funcdctl` 7.5 s, the others under 3 s.
- **The new tests are un-skipped, call `t.Parallel()` and pass under 2 s** (`-race -v`):
  `TestRestoreStopKeepsNamespaceHeld` 0.37 s and `TestRestoreInPlaceDeleteWaitsForEnd` 0.12 s. The stop test runs the
  real `Platform.Run`, stops it in the middle of a Bucket load, restarts it over the same store, KV driver, blob store
  and data directory, and asserts each of the following:
  - the namespace stays held under a marker that names the Restore;
  - after the restart the Restore is `Interrupted`, and the namespace is still held;
  - the release lifts the hold.
- **Mutants**, each applied through `go test -overlay` and each killed:
  - a failed load that lifts the hold (`restore.go:213`) fails `TestRestoreStopKeepsNamespaceHeld` ("a stop in the
    middle of the load keeps the namespace held");
  - an admitted delete of an active in-place Restore (`admission/backup.go:155`) fails
    `TestRestoreInPlaceDeleteWaitsForEnd`;
  - a completed load that never lifts the hold (`restore.go:218`) fails `TestScenarioRestoreInPlace` ("the namespace
    was held during the load, then released").
- **Round 2's Major 1 is resolved**: the round-2 scratch tests were rerun on this head through `go test -overlay`.
  - After a stop in the middle of a KVStore load, the log read `HeldIn(orders)=true` and the marker names `rewind`.
  - After a stop in the middle of a Bucket load, the log read `HeldIn(orders)=true` with `objects=[a.csv]`; the
    leftover object is fenced by the hold.
  - On a failure, a stop or a crash, the code keeps the marker whatever the emptying returned (`restore.go:210-222`).
    Only a completed load lifts it, and only while the marker still names this Restore.
- **Round 2's delete Minor is resolved**: the round-2 scratch test was rerun on this head while a load was blocked.
  - The developer's delete was refused ("in-place Restore rewind … has not ended: delete it once it has").
  - The admin's release was refused ("held by Restore rewind, which is Running"), and `HeldIn(orders)=true`.
  - A further in-place create was refused.

  The delete admission runs under the ADR-0147 namespace lock, and the store delete is conditional on the
  resourceVersion of `Old` (`handlers.go:351`). The admission returns `req.Old`, the convention that
  `links.go:150` and `resourcegroup.go:72` follow. A Restore into a new name, or an ended Restore, deletes as before.
- **No regression in what rounds 1 and 2 verified**: the following tests pass:
  - `TestScenarioRestoreInPlace` 0.26 s; a failed export still writes nothing and holds nothing;
  - `TestScenarioRestoreNewName` 0.20 s;
  - `TestScenarioRunInterrupted` 0.15 s;
  - `TestScenarioCatalogCheckpointVerified` 0.24 s;
  - `TestRestoreInterruptedHoldRelease` 0.22 s;
  - `TestRestoreInPlaceOnePerNamespace` 0.12 s;
  - `TestReleaseNamespaceWaitsForRestore` 0.15 s;
  - `TestScenarioPlatformRestoreHoldsNamespace` in `cmd/funcd`, 1.75 s.

  The rig refactor (`newPlatform`, `run`, `restart`) leaves every existing caller of `newWBRig` unchanged.
- **Tracking**: the range has no diff under `docs/adr` or `docs/feat`, and ADR-0222 keeps its `Implemented` stamp.

### Definition of Done

15 / 15 hold (ADR Review checklist 4, ADR Definition of done 1, generic implement-gate DoD 10). The generic item "real
behaviour" missed in round 2 now holds: a load that does not complete leaves the namespace held. `just ci` itself runs
in the PR gate; every check that it runs held on the touched packages.

### Model scorecard

To record: claude-opus-5-5 on ADR-0222 (implementation, round 3) → pass, 0/0/2, 0 model-attributed, DoD 15/15.

### Recommendation

Sign off on rounds 2 and 3. Both Minors go to the second addendum ADR, with no work for the builder:

- decide whether a later in-place Restore may lift a hold that an earlier one left;
- record the contract additions of rounds 2 and 3 and the four listed consequences.
