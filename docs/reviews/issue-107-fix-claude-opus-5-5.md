## Verdict: pass — 0 blockers, 0 majors, 3 minors  (issue #107 fix, model: claude-opus-5-5)

Change: `f85b188 fix(route): keep every Route's status in step with the edge table and its backends`
(`internal/route/reconcile.go`, `internal/route/reconcile_test.go`; 122+/27-).

The issue's root cause is that `Reconcile` evaluated every Route and reprogrammed the edge table, but wrote
only the requested Route's status and never requeued, and no watch enqueues a Route on a Function, Bucket
or sibling-Route change. The fix removes both halves of that cause. Every evaluated Route whose Ready
condition or phase changed is now written. A live Route is requeued after 2 s while a backend is missing
(the ADR-0121 bounded requeue) and after 10 s otherwise, so a deleted backend is noticed.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minors
- **A reason-only status change is untested** · attribution: model. Mutant M4 changed `applyStatus`'s
  change detection from `cur != prev` to `cur.Status != prev.Status`. The whole `internal/route` package
  still passed (`ok github.com/pyvvo/funcd/internal/route 0.273s`). A transition that keeps
  `Status=False`/`Phase=Pending` and changes only the reason, for example `RouteConflict → BackendNotFound`
  or `HostRequired → RouteConflict`, would then never be persisted, and no test would notice. The fix's
  code is correct. To close the gap, add a test step that moves a NotReady Route from one reason to another.
- **`resyncPeriod` repeats `controller.SupervisionPeriod`** · attribution: model (trivial).
  `internal/route/reconcile.go` defines `resyncPeriod = 10 * time.Second`, and
  `internal/controller/controller.go` already exports `SupervisionPeriod = 10 * time.Second` as the
  steady-state requeue. The two have different meanings (ADR-0142 ties `SupervisionPeriod` to reconcilers
  that own running instances), so a local constant is defensible. The comment could name the precedent,
  or the code could reuse the exported value.
- **Each steady-state resync costs O(N) per Route** · attribution: adr (not scored). Every live Route
  requeues every 10 s, and each reconcile lists and evaluates the whole Route set: one backend `Get` per
  rule plus a full `Aggregator.Set` → `Program`. For N Routes this is about N²/10 s of store reads. That
  is acceptable at the target scale (around 100 Routes). The cheaper design is a cross-kind enqueue (a
  Function/Bucket event enqueues the Routes that reference it), which the controller does not have
  (`controller.Run` opens one watch per registered kind). Adding it is a design decision, not part of this fix.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit f85b188` was applied with the
  new test kept, and `TestIssue107_RouteStatusFollowsEvaluation` FAILED on facet (a):
  `expected: "True" actual: "False" — r-b serves /x, so it is Ready (reason "RouteConflict")`. This is the
  issue's "loser serves traffic but reports RouteConflict" symptom. The tree was then reset to `f85b188`
  and left clean.
- **Passes with the fix under -race.** `go test -race -count=1 ./internal/route/` → `ok` (4.6 s), with
  every test un-skipped.
- **Mutants (3 of 4 killed).** M1 `resyncPeriod` requeue → `Result{}` was killed ("a Ready Route is
  requeued"). M2 `backendRequeue` requeue → `Result{}` was killed ("a Route waiting on its backend is
  requeued"). M3 writing only the requested Route's status was killed (facet (a)). M4 survived; see Minors.
- **Cause, not symptom.** No timeout was lengthened and no error was swallowed. The status write now
  covers the whole evaluated set, which is the set that `routes.Set` programs, so status and the edge table
  come from one evaluation. The requeue is the ADR-0121 accept-and-requeue mechanism
  (`controller.Result{RequeueAfter}`, 2 s while unresolved). The static arm (`BucketNotFound`) is covered
  by the same flag and by its own test assertion.
- **Write loop terminates.** `applyStatus` reports a change only when the Ready condition or phase differ.
  `Conditions.Set` keeps `LastTransitionTime` when the status is unchanged, so a no-op pass writes
  nothing, and the watch events that the sibling writes cause settle after one more pass.
- **Facets (b), (c) and (d) are asserted.** The displaced Route goes `RouteConflict`, a late backend
  converges and is routable, and a deleted Function makes the Route `BackendNotFound` and unprogrammed.
- **Scope.** Both hunks serve the issue. No test was weakened or deleted, and no other file or ADR was touched.
- **ADRs.** The fix conforms to ADR-0110 Decision 3/4 (first claimant Ready, the rest RouteConflict;
  route-not-ready-missing-backend) and to the ADR-0121 bounded requeue. No Accepted or Implemented ADR
  is contradicted or edited.
- **Conventions.** Errors are wrapped with `api/fault` and `op` and now name `ns/name`. There is no `any`
  in signatures, imports are at the top level, and comments explain the why (why the requeue exists),
  not the what. The test reuses the package's `setup`/`seedRoute`/`seedFunction`/`seedStaticRoute`/
  `readyCond`/`reconcile` helpers. The one new helper, `deleteObject`, has no existing equivalent in the package.
- **Checks (touched package).** `go vet ./internal/route/` passed, `golangci-lint run ./internal/route/`
  reported `0 issues.`, and `gofmt -l` was clean. Linux lint and e2e were left to the group gate, as instructed.
- **Shape.** The subject is `fix(route): …`, the body has `Fixes #107`, the commit carries the attribution
  trailer, and the commit covers one issue.

### Definition of Done
11 / 11 items hold. Item 4 holds with one surviving mutant, recorded as a Minor test gap. Item 8 holds for
the touched package; Linux lint and e2e were left to the group gate by instruction. Item 11 was checked on
the commit; the PR does not exist yet.

### Model scorecard
Not recorded here; the ledger fields are returned to the caller: claude-opus-5-5 on issue #107 (fix) →
pass, 0/0/3, 2 model-attributed, DoD 11/11.

### Recommendation
Pass. Hand back to `/fix` Step 8 to open the PR. Optionally, add a reason-only transition step to the
regression test (M4) and name or reuse `controller.SupervisionPeriod`. Route the cross-kind enqueue idea
to `/adr` or to the board if the resync cost ever matters.
