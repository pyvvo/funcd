## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #148 fix, model: claude-opus-5-5)

Change: branch `fix/148-eventsource-status`, commit 562c8e5 `fix(eventing): stop the EventSource reconciler
rewriting the spec and keeping a stale condition` (`internal/eventing/eventing.go`,
`internal/eventing/eventing_test.go`; +103/-6).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — no test covers a Ready blob source that becomes a timer source** · attribution: `model`.
  Mutant M3 removed `|| blobCond` from the timer branch's write guard in `Source.Reconcile`. The whole
  package still returned `ok`. The subtest `timer source drops the blob condition` switches only a
  NotReady blob source (missing Bucket, phase `Pending`) to a timer source, and there `Phase != Ready`
  alone triggers the write. The `|| blobCond` clause matters only when the blob source was Ready. A scratch
  probe (an overlay-added test, not committed) confirmed the gap: a Ready blob source switched to a timer
  source ends with no Ready condition under the fix, but under M3 it keeps
  `Ready=True reason=Watching observedGeneration=1` at generation 2. That is the stale-condition symptom of
  the issue. Fix: add a variant of the subtest whose Bucket exists before the switch.
- **Minor 2 — `normalizedBlob` has no observable effect** · attribution: `model`.
  `BlobWatcher.Register` (`internal/eventing/blobwatch.go`) stores only `Bucket` and each event's `Name`
  and `Prefix` in its `watchEntry`; it never reads `On`. Mutant M5 passed `es.Spec.Blob` to `Register`
  instead of `normalizedBlob(es)`, and the package still returned `ok`. So the helper clones the events on
  every reconcile (every 15s per blob source) to fill a field that nothing reads. Removing
  `es.Normalize()` alone would have fixed the issue. Keeping the helper is defensible: it keeps the
  ADR-0119 default in the one production call site of `Normalize`, ready for when `On` gains meaning. But
  its doc comment should then say that the V1 watcher ignores `On`; otherwise a reader assumes the
  defaulted copy changes what is watched.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reasons.** With the `origin/main`
  `eventing.go` overlaid (`go test -overlay`), all three subtests of `TestIssue148_StatusMatchesCurrentSpec`
  fail. `ready reconcile keeps the user's spec`: `expected: 1, actual: 2` — "a status write must not bump the
  generation" (facet (a) of the issue: generation 2 against observedGeneration 1). `spec edit refreshes the
  condition`: `expected: 2, actual: 1` — the Ready condition keeps the old observedGeneration after a user
  edit. `timer source drops the blob condition`: the source keeps
  `{Status:False ObservedGeneration:1 Reason:BucketNotFound}` with phase Ready (facet (b) of the issue).
- **It passes with the fix under `-race`.** `go test -race -count=1 ./internal/eventing/...` → `ok` for
  `internal/eventing` (1.393s) and both deadletter drivers; the three subtests pass un-skipped.
- **The root cause is fixed, not masked.** The issue names two causes, and both are removed.
  (1) `reconcileBlob` no longer calls `es.Normalize()` on the object it writes back, so `store.Update` sees
  no spec change and does not bump the generation; the default goes to a copy instead. (2) The timer branch
  now removes the blob-only Ready condition. The fix also adds `cur.ObservedGeneration != es.Generation` to
  the blob branch's write guard, so a user edit refreshes the condition. That serves the issue's expected
  behavior ("the status condition reflects the current generation"). No error is swallowed, no retry or
  timeout was added, and no test was skipped.
- **The fix stays quiescent (ADR-0047).** A second reconcile writes nothing: the first subtest reconciles
  twice and the generation stays 1. A scratch probe of the NotReady path (missing Bucket, empty `on`,
  two reconciles) ends at generation 1, observedGeneration 1 and resourceVersion 2, so the second pass is a
  no-op. Before the fix, that path also bumped the generation, because `Normalize` ran before
  `setBlobNotReady`.
- **Mutants on the key lines fail a test.** M1 (restore `es.Normalize()` in `reconcileBlob`) → the first
  subtest fails (generation 2). M2 (drop the `ObservedGeneration` check) → the second subtest fails. M4
  (drop the `slices.DeleteFunc` line) → the third subtest fails. M3 and M5 survive (Minors 1 and 2).
- **Scope.** Every hunk serves #148: the `Normalize` removal and the copy, the observedGeneration guard,
  the timer-branch condition cleanup, two honest doc-comment updates, and the regression test with two
  small helpers. No test was weakened or deleted. No other code reads the stored `on` (the only other
  reference is a watcher test fixture that sets it explicitly), and no living doc claims that the stored
  spec carries the default.
- **Reuse.** `normalizedBlob` reuses `EventSource.Normalize` instead of re-implementing the default loop.
  The shallow `slices.Clone` is enough, because `Normalize` only replaces an empty `On` and never writes
  into an existing one. `v1.Conditions` has only `Set` and `Get`, so `slices.DeleteFunc` (standard
  library) is the right way to remove a condition. The read-side default mirrors the existing
  `ExposureMode.Normalized()` precedent. The new test helpers (`getSource`, `newBlobSource`) reuse the
  package's `newStore`, `createBucket`, `createBlobSource`, `stubLister`, `capturePublisher` and `reqOf`;
  `internal/testkit` has no store-get helper to use instead.
- **Conventions.** ADR-0002 holds: ctx-first, `api/fault` errors, no new exported surface, and imports at
  the top level. The comments explain the why. `go vet` passes, `gofmt -l` is clean, and `golangci-lint`
  reports 0 issues on the host and with `GOOS=linux` for `internal/eventing/...`.
- **ADRs.** No ADR file was touched. ADR-0119 Decision §1 keeps `Validate` pure and puts the `on` default
  in a normalize step; that still holds, because `Normalize` is the step and the reconciler applies it to a
  copy. Note: the issue says ADR-0119 forbids defaulting in the reconciler, but the ADR says only "not in
  `Validate`". The Implemented ADR-0119 code also called `Normalize` in `reconcileBlob`, so keeping the call
  there contradicts nothing. ADR-0119 §2 (the sole reconciler, NotReady on a missing Bucket, requeue) and
  ADR-0108 (a pure `Validate`) are unchanged.
- **Shape.** The subject is `fix(eventing): …`, the body says `Fixes #148` and names the regression test,
  the attribution trailer is present, and the branch has one commit for one issue.

Not run here, by design: the daemon steps of the issue (`funcdctl apply`/`describe`), e2e, repo-wide tests
and the Lima lanes. The scratch probes exercise the real store (`store.New` over the memory engine) and the
real `eventing.Source` reconciler, which is the code path the daemon result comes from. The group gate runs
the rest.

### Recommendation

Pass. Both Minors are optional follow-ups: a Ready-bucket variant of the kind-change subtest, and either a
note in `normalizedBlob`'s doc comment that the V1 watcher ignores `On`, or removing the helper.
