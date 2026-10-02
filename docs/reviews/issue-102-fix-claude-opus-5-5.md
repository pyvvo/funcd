## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #102 fix, model: claude-opus-5-5)

Change: branch `fix/i102`, commit `a6291fc fix(eventing): mark blob EventSources and static Routes NotReady when their Bucket is deleted` (`git diff origin/main...HEAD`: `internal/eventing/eventing.go`, `internal/eventing/eventing_test.go`, `internal/route/reconcile.go`, `internal/route/reconcile_test.go`).

### 🟡 Minor

- **A static Route requeue re-evaluates the whole Route set** · attribution: model · evidence: `internal/route/reconcile.go:99-101` returns `RequeueAfter: bucketRecheckInterval` for every Route with a static backend, and each pass runs `evaluate` (one `List` of all Routes plus one `Get` per rule backend, `internal/route/reconcile.go:113-175`) and `routes.Set`. With N static Routes that is N full evaluations every 15 s, O(N × total backends) reads. The writes are harmless: the store coalesces unchanged updates (`internal/store/store.go:407-418`, ADR-0047), so no watch-event loop follows. At the target scale (about 100 workloads) the cost is small. Fix (optional): none needed now; a Bucket → dependents enqueue in the controller would remove the polling if the cost ever matters.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit a6291fc`, then restored the two test files from HEAD (the revert removes them) and ran `go test -run TestIssue102 ./internal/eventing/ ./internal/route/`: both fail with `"0s" is not positive` / "a Ready blob source requeues, else it stays Ready after its Bucket is deleted" and "a static Route requeues, else it stays Ready after its Bucket is deleted". That is the issue's cause: a Ready blob source or static Route never runs again after the Bucket goes away. Then `git reset --hard a6291fc`; the worktree is clean at that HEAD.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/eventing/ ./internal/route/` → ok/ok; `-v -run TestIssue102` shows both tests PASS, un-skipped.
- **Mutants (overlay), all killed.**
  1. NotFound path in `reconcileBlob` returns `controller.Result{}` → `TestScenarioBlobSourceMissingBucketNotReady` fails.
  2. `hasStaticBackend` tests `Backend.Static == nil` → `TestIssue102_StaticRouteNotReadyAfterBucketDeleted` fails.
  3. `if false && hasStaticBackend(rt)` → `TestIssue102_StaticRouteNotReadyAfterBucketDeleted` fails.
  The Ready-path requeue in `reconcileBlob` (`eventing.go:155`) is the line the revert check removed; the blob test fails on it.
- **Root cause, not symptom.** The issue names the cause: the controller opens one watch per kind and never resyncs, so no Bucket event reaches the EventSource or Route reconciler. The fix makes both reconcilers recheck their Bucket on a timer. This extends the existing `blobRetryInterval` NotReady requeue (now `bucketRecheckInterval`) to the Ready path, and matches the requeue-to-recheck idiom already used by `internal/site/reconcile.go` (`routeRequeue`) and `internal/services/catalog/reconcile.go`. The requeued pass finds the Bucket gone, sets `BucketNotFound`, deregisters the BlobWatcher (so the WARN-per-poll stops) or unprograms the static route. No retry, timeout or swallowed error hides anything. The controller has no cross-kind watch mechanism (`internal/controller/controller.go` `Register`/`watch` are per GVK), so the timer recheck is the smallest root-cause fix that needs no design decision. The admission alternative named in the issue (refuse a Bucket delete while an EventSource watches it) would not cover static Routes.
- **Scope.** Every hunk serves the issue: the requeue in both reconcilers, the rename and doc-comment update of the interval constant, the `hasStaticBackend` helper, and the two regression tests. No test was weakened or deleted.
- **Reuse.** No duplicated logic. `hasStaticBackend` is new and small; a grep for `.Static != nil` finds only inline per-rule checks (`api/types/v1alpha1/route.go`, `internal/dataplane/dataplane.go`), no Route-level helper to reuse. The two `bucketRecheckInterval` constants live in separate packages, each with its own reconciler, matching how each reconciler in the repo keeps its own requeue constant. The tests reuse the packages' existing harnesses (`newStore`, `createBucket`, `createBlobSource`, `stubLister`, `setup`, `seedBucket`, `seedStaticRoute`, `readyCond`).
- **Conventions.** ADR-0002 holds: `api/fault` errors, ctx-first, no new imports beyond `time`, imports at top level, comments state the reason (no Bucket event reaches the reconciler) without narration. `gofmt -l` reports nothing.
- **ADRs.** The fix delivers ADR-0119's Constraint "Missing bucket ⇒ NotReady … not Ready-but-silently-not-polling" and ADR-0120's static `BucketNotFound`. It contradicts no Decision or Contract, and the diff touches no ADR file.
- **Checks (touched packages).** `go build ./...` ok; `go test -race` ok for both packages; `go vet` ok; `golangci-lint run ./internal/eventing/... ./internal/route/...` → 0 issues. The e2e suite, Linux lint and the lanes are left to the group gate, as the review brief sets.
- **Shape.** Subject `fix(eventing): …`, body explains cause and fix, `Fixes #102`, `Co-Authored-By` trailer, one issue in one commit.

Observation, not a finding: a Route whose *function* backend is deleted may have the same stale-readiness pattern, unless the Function reconciler reprograms Routes. That is outside issue #102 (Bucket only) and was not tested here.

The issue's daemon repro (`funcdctl delete bucket` then `describe eventsource`) was not rerun. The brief excludes daemon and e2e runs; the unit tests drive the same reconcile path.

### Definition of Done
11 / 11 items hold. Item 8 was checked on the touched packages only (build, vet, lint, `-race` tests); e2e, Linux lint and lanes are deferred to the group gate.

### Model scorecard
Not recorded by this gate (per the brief). Ledger fields: claude-opus-5-5 on issue #102 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Sign off. The minor efficiency note needs no change now; revisit it only if Route counts grow large enough that the 15 s full re-evaluation matters.
