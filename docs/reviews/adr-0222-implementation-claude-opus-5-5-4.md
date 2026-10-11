## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0222 implementation, round 4, model: claude-opus-5-5)

Round 4 reviews the one commit after the round-3 report (`docs/reviews/adr-0222-implementation-claude-opus-5-5-3.md`),
the range `c5f7df7c...f8d8a47e` on `feat/adr-0222-workload-backup-resources`. Commit `f8d8a47e` makes these changes:

- The restore-in-place admission refuses a new in-place Restore with `fault.Conflict` while the namespace carries a
  restore marker. The message names the marker's Restore and the lever, `hold release --namespace`. The constructor is
  now `NewRestoreInPlaceAdmission(authz, r, h)`.
- `hold.HoldNamespace` never replaces a standing marker. A load that meets one fails with the new reason
  `NamespaceHeld` before it writes anything.
- The commit adds `TestRestoreInPlaceRefusedWhileMarkerStands` and extends `TestReleaseNamespaceMarker`.

The bar is the same as in rounds 2 and 3: ADR-0222 Decisions 7 and 8 and ADR-0206 Decisions 5 and 6.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **No committed test covers the load-time refusal** · attribution: `model` · The commit states that "a load that
  meets one fails with the new reason NamespaceHeld before it writes anything" (`internal/services/backup/restore.go:205-210`).
  A mutant that lets the load carry on past a refused `HoldNamespace` survives the full suites of
  `internal/services/backup` and `pkg/funcd`: both report `ok`, because the admission stops every committed case
  first. The behaviour itself is correct. A scratch test (run through `go test -overlay`, not committed) seeded an
  in-place Restore past the admission while a marker naming `first` stood. The log read `reason=NamespaceHeld
  HeldIn(orders)=true marker=first orders-state=map[t/1:changed]`: nothing was written and the marker was kept. The
  same scratch test fails under that mutant. Fix: commit a test of this shape, a Restore stored directly while a
  marker stands, which asserts `NamespaceHeld`, the store unchanged, and the marker kept.
- **Behaviour beyond ADR-0222's text** · attribution: `adr`, by ruling of the DR owner, for the second addendum. None
  of the three listed consequences breaks a Decision:
  - No in-place Restore of a namespace is admitted while its restore marker stands, until `hold release --namespace`.
    Decision 7's admin may still run one after the release. Decision 8's release is the only lever, which fails
    closed.
  - `HoldNamespace` refuses to replace a standing marker. The Contracts give only its signature, so this adds a
    precondition. A load that meets a standing marker fails with `NamespaceHeld`, a tenth reason next to the
    Contracts' nine.
  - Restore run's `data` marker does not block the admission. This conforms to Decision 8 ("Restores skip `HeldIn`").

  One case to record: a marker that stays after a `Succeeded` load, because its lift failed with an I/O error and the
  failure was logged, also blocks the admission. The refusal message then says the load "did not complete". This
  fails closed, and the release lifts the marker.

### ✅ Verified correct (keep it)

- **Build, vet, lint, format**: `scripts/agent/d go build ./...` exit 0. `go vet` exit 0 over the six touched
  packages: `cmd/funcdctl`, `internal/controlplane`, `internal/controlplane/admission`, `internal/platform/hold`,
  `internal/services/backup` and `pkg/funcd`. `go tool golangci-lint run` over the same packages: `0 issues.`, exit 0.
  `gofmt -l` on the changed files prints nothing.
- **Tests**: `go test -race -count=1` over those packages exits 0: `pkg/funcd` 19.5 s, `internal/controlplane`
  12.4 s, `cmd/funcdctl` 7.6 s, the others under 4 s.
- **The new and extended tests are un-skipped, call `t.Parallel()` and pass under 2 s** (`-race -v`):
  `TestRestoreInPlaceRefusedWhileMarkerStands` 0.30 s and `TestReleaseNamespaceMarker` 0.04 s.
- **Mutants**, each applied through `go test -overlay`:
  - `HoldNamespace` that replaces a standing marker (`hold/namespace.go:63`) fails `TestReleaseNamespaceMarker`;
  - an admission that ignores a standing restore marker (`admission/backup.go:186`) fails
    `TestRestoreInPlaceRefusedWhileMarkerStands`;
  - the third mutant, on the load-time refusal, survives (see the model Minor).
- **The round-3 gap is closed**: the round-3 scratch test was rerun on this head. After the failed Bucket load the log
  read `HeldIn(orders)=true marker=first bucket objects=1`. The KVStore-only in-place Restore is now refused: "namespace
  "orders" is held by in-place Restore "first", whose load did not complete: check the store, then funcdctl hold
  release --namespace orders". The half-written Bucket stays fenced until the release. In the committed test, the
  release admits the second Restore, which then succeeds and lifts the hold.
- **Earlier rounds hold**: the round-2 and round-3 scratch tests were rerun on this head.
  - A stop in the middle of a KVStore or Bucket load keeps the namespace held: the log read `HeldIn(orders)=true`, the
    marker names `rewind`, and `objects=[a.csv]` remains fenced.
  - While `rewind` runs, the second in-place create, the delete and the release are each refused.
- **The order of the checks is right**: the admission refuses a Restore that has not ended before it checks the
  marker. A running load therefore gets the "has not ended" message, and only an ended load that left a marker gets
  the release message. `MarkNamespaces`, the offline write of restore run, does not go through `HoldNamespace` and
  still writes its `data` markers. The only caller of `HoldNamespace` in production is the in-place load.
- **No regression in what rounds 1 to 3 verified**: the following tests pass:
  - `TestScenarioRestoreInPlace` 0.26 s;
  - `TestScenarioRestoreNewName` 0.20 s;
  - `TestScenarioRunInterrupted` 0.16 s;
  - `TestRestoreStopKeepsNamespaceHeld` 0.37 s;
  - `TestRestoreInterruptedHoldRelease` 0.23 s;
  - `TestRestoreInPlaceOnePerNamespace` 0.12 s;
  - `TestRestoreInPlaceDeleteWaitsForEnd` 0.12 s;
  - `TestReleaseNamespaceWaitsForRestore` 0.15 s;
  - `TestScenarioCLIApplyWorkloadBackup` 0.12 s, with a nil hold;
  - `TestScenarioPlatformRestoreHoldsNamespace` in `cmd/funcd`, 1.74 s.
- **Tracking**: the range has no diff under `docs/adr` or `docs/feat`, and ADR-0222 keeps its `Implemented` stamp.

### Definition of Done

15 / 15 hold (ADR Review checklist 4, ADR Definition of done 1, generic implement-gate DoD 10). The untested
load-time refusal is a defence behind the admission, not a Scenario, so it misses no DoD item. `just ci` itself runs
in the PR gate; every check that it runs held on the touched packages.

### Model scorecard

To record: claude-opus-5-5 on ADR-0222 (implementation, round 4) → pass, 0/0/2, 1 model-attributed, DoD 15/15.

### Recommendation

Sign off. Two follow-ups, neither blocking:

- The builder adds the load-time `NamespaceHeld` test (the model Minor).
- The second addendum ADR records the three consequences and the new reason.
