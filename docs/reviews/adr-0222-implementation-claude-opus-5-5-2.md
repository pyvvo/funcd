## Verdict: changes requested — 0 blockers, 1 major, 2 minors  (ADR-0222 implementation, round 2, model: claude-opus-5-5)

Round 2 reviews the commits after the round-1 report (`docs/reviews/adr-0222-implementation-claude-opus-5-5.md`), the
range `2e65274b...4b321182` on `feat/adr-0222-workload-backup-resources`: `4f471c82` (`hold release --namespace` lifts
the restore marker of an ended in-place Restore, refuses while it is `Pending` or `Running`; `hold.Marker.Restore`,
`Evidence.Namespaces`, `ReleaseNamespaceMarker`), `4b321182` (one active in-place Restore per namespace at admission,
`Restore.Ended`) and `bc50ef43` (an edge test off the deprecated `http2.ConfigureTransport`). The bar is ADR-0222
Decisions 7 and 8 and ADR-0206 Decisions 5 and 6: a half-written in-place restore is never released silently, and a
running Restore refuses the release. The repo-wide gate (`just ci-full`, e2e, Linux lint, lanes) runs before the PR.

### 🔴 Blockers

None.

### 🟡 Major 1 — A graceful stop during an in-place load lifts the namespace hold over a half-written store  ·  attribution: `model`

The round-1 Minor is resolved for a crash only. On a graceful stop the hold is lifted at shutdown, which is the
opposite of what the new text in `examples/workload-backup.md:111-113` says ("One that funcd stopped during the load
ends `Failed` (`Interrupted`) and keeps the namespace held: `funcdctl hold status` names it").

- Path: shutdown cancels the controller context and then waits for the runs (`pkg/funcd/funcd.go:1481-1484`). The load
  fails on the cancelled context, `load` empties with that same context (`internal/services/backup/restore.go:215`), and
  the deferred `ReleaseNamespace(ns, restore)` (`restore.go:206-210`) lifts the marker whatever the emptying returned.
  `Reconcile` then skips `finish` (`restore.go:88-90`), so the Restore stays `Running` until the next start marks it
  `Interrupted`. At that point `hold status` lists no marker and `hold release --namespace` answers "not held".
- On a Bucket or catalog member, the emptying runs `BucketPurger.Purge` or `ListAfter` with the cancelled context
  (`restore.go:356`, `funcd.go:2192`). A backend that honours the context, as S3 does, refuses the emptying, so the
  objects already loaded stay. This contradicts Decision 7 ("a failure empties what it wrote"), and the hold is lifted
  over the half-written store. The same unconditional lift follows any failure whose emptying errs.
- Evidence: three scratch tests ran through `go test -overlay` on the real `Platform.Run` path; they were not
  committed, and the worktree stayed clean. The rig's cleanup cancelled the platform in the middle of an in-place load.
  - KVStore member, with a `LoadPrefix` that blocks until the context is cancelled: during the load the marker was
    `restore … rewind`. After the stop the log read `HeldIn(orders)=false markers=map[]`, `Restore phase after stop:
    "Running"`, `orders-state keys after stop: []`.
  - Bucket member, with a Backup that holds `a.csv` and `b.csv`, a second `Put` that blocks, and a blob that returns
    `ctx.Err()` once the context is cancelled: the log read `HeldIn(orders)=false markers=map[] objects=[a.csv]`.
- Fix (builder): keep the restore marker when the load ends on a cancelled context or when the emptying fails. The
  next start then marks the Restore `Interrupted`, and `hold release --namespace` is the lever, as for a crash. A
  second option is to empty under `context.WithoutCancel` with a bound, and to lift only after a clean empty. Add a
  test that stops the platform in the middle of an in-place load and asserts that `HeldIn` is true and that the
  marker names the Restore.

### Minor

- **Deleting a Pending or Running in-place Restore bypasses both fixes** · attribution: `adr` · Decision 10 lets a
  developer delete a Restore, and nothing stops a load when its record is deleted. Both fixes read "not ended" from
  the stored record: the release treats a Restore that is gone as releasable (`internal/controlplane/hold.go:268`),
  and the admission lists the stored Restores (`internal/controlplane/admission/backup.go:169`). The deleted
  Restore's deferred lift also uses the unconditional `ReleaseNamespace`, not `ReleaseNamespaceMarker`. A scratch
  test with a blocked KV load showed the following:
  - while `rewind` was running, a second in-place create was refused, as intended;
  - the developer's delete of `rewind` returned `err=<nil>`;
  - the admin's `ReleaseNamespaceHold` returned `err=<nil> HeldIn(orders)=false` while the load still ran;
  - a third in-place Restore was admitted (`err=<nil>`).

  To decide in the second addendum: refuse a delete of a Restore that has not ended, or cancel the load on delete and
  keep the marker. In either case the deferred lift should use `ReleaseNamespaceMarker`.
- **Contract additions beyond ADR-0222's text** · attribution: `adr`, by ruling of the DR owner, to be recorded in
  the second addendum ADR:
  - `hold.Marker.Restore`;
  - `hold.NamespaceHold` and `Evidence.Namespaces` (`hold status` lists each namespace marker, a restore marker with
    its Restore's phase);
  - `(*Hold).NamespaceMarkers` and `ReleaseNamespaceMarker` (compare and lift);
  - the release of `--namespace` also lifts the restore marker of an ended or deleted Restore, and refuses the whole
    namespace, its `data` marker included, while that Restore is `Pending` or `Running`;
  - one active in-place Restore per namespace at admission (`fault.Conflict`) under the ADR-0147 lock;
    `NewRestoreInPlaceAdmission(authz, r)`;
  - `Restore.Ended`.

### ✅ Verified correct (keep it)

- **Build, vet, lint, format**:
  - `scripts/agent/d go build ./...` exit 0.
  - `go vet` exit 0 over the nine touched packages: `api/types/v1alpha1`, `cmd/funcdctl`, `internal/controlplane`,
    `internal/controlplane/admission`, `internal/platform/hold`, `internal/services/backup`, `pkg/funcd`, `pkg/sdk`
    and `internal/edge/tls/static`.
  - `go tool golangci-lint run` over the same packages: `0 issues.`, exit 0.
  - `gofmt -l` on the changed Go files prints nothing, and `go mod tidy -diff` exits 0.
- **Tests**: `go test -race -count=1` over the nine packages exits 0, every package `ok` (`pkg/funcd` 21.0 s,
  `internal/controlplane` 13.8 s, `cmd/funcdctl` 6.2 s, the others under 5 s). The OpenAPI drift test in
  `internal/controlplane/api_test.go` passes on the regenerated `api/openapi/funcd.v1alpha1.yaml`.
- **The new tests are un-skipped, call `t.Parallel()` and pass under 2 s** (`-race -v`):
  - `TestReleaseNamespaceMarker` 0.03 s;
  - `TestReleaseNamespaceWaitsForRestore` 0.13 s;
  - `TestRestoreInPlaceOnePerNamespace` 0.10 s;
  - `TestRestoreInterruptedHoldRelease` 0.21 s, on the rig's short `os.MkdirTemp` data dir.
- **Mutants**, each applied through `go test -overlay` and each killed:
  - the release that ignores a running Restore (`hold.go:268` → `false && …`) fails
    `TestReleaseNamespaceWaitsForRestore`;
  - the admission without the check that the other Restore has ended (`admission/backup.go:169`) fails
    `TestRestoreInPlaceOnePerNamespace`;
  - `ReleaseNamespaceMarker` without its compare (`hold/namespace.go:101`) fails `TestReleaseNamespaceMarker`.
- **The crash path of the round-1 Minor is resolved, and it fails closed**:
  - A `Running` in-place Restore that is found at the start ends `Failed` (`Interrupted`) and keeps its marker.
  - `hold status` names the Restore and its phase.
  - The release refuses with `fault.Conflict`, naming the Restore, while the Restore is `Pending` or `Running`, and
    keeps both markers. It lifts both markers once the Restore has ended.
  - The lift succeeds only while the marker still names that Restore, which closes the window in which another
    Restore rewrites the marker. The release can be run again after a partial failure.
- **One active in-place Restore per namespace**: the admission reads the namespace's Restores under the ADR-0147
  namespace lock (`ReadsNamespace`, `handlers.go:51`), so two concurrent creates are serialized. A Restore into a new
  name is not affected. `Ended` treats every phase except `Succeeded` and `Failed` as active, which fails closed.
- **No regression in what round 1 verified**: `TestScenarioRestoreInPlace` 0.25 s, `TestScenarioRestoreNewName`
  0.20 s, `TestScenarioRunInterrupted` 0.15 s, `TestScenarioCLIApplyWorkloadBackup` 0.12 s (it uses the new admission
  signature) and `TestScenarioPlatformRestoreHoldsNamespace` in `cmd/funcd` 1.75 s all pass.
  `restore run`'s `data` markers and their release work as before when no restore marker is present.
- **`bc50ef43`**: a test-only change. `TestScenarioALPNNegotiatesH2` now enables HTTP/2 through
  `http.Transport.Protocols` and passes; staticcheck is clean.
- **Tracking**: the range has no diff under `docs/adr` or `docs/feat`, and ADR-0222 keeps its round-1 `Implemented`
  stamp.

### Definition of Done

14 / 15 hold (ADR Review checklist 4, ADR Definition of done 1, generic implement-gate DoD 10). One item misses: the
generic item "real behaviour: bodies do the work the Contracts describe". On the stop path, Decision 7's "a failure
empties what it wrote" does not hold, and the hold is lifted anyway (Major 1, `model`). `just ci` itself runs in the PR
gate; every check that it runs held on the touched packages.

### Model scorecard

To record: claude-opus-5-5 on ADR-0222 (implementation, round 2) → changes-requested, 0/1/2, 1 model-attributed,
DoD 14/15.

### Recommendation

Back to the builder for Major 1: keep the restore marker when a load ends on a cancelled context or on a failed
emptying, add the stop test, and keep the example text true. The two `adr` Minors go to the second addendum ADR:
decide what deleting a Restore that has not ended does, and record the listed contract additions. The ADR stays as it
is.
