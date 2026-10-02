## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #104 fix, model: claude-opus-5-5)

Change: `fix/i104`, commit 1c507fb `fix(catalog): report a CatalogService not Ready when its engine cannot restart`
(`internal/services/catalog/reconcile.go` +7/-1, `internal/services/catalog/reconcile_test.go` +56).

### 🟡 Major / Minor
- **Minor — the `EngineConvergeFailed` condition carries no Message** · attribution: model ·
  `internal/services/catalog/reconcile.go:117` sets `st = provider.ProviderStatus{Reason: "EngineConvergeFailed"}`,
  so the Ready condition says *that* the engine could not be recreated but not *why*; the cause (for example
  "pull image: not found") is only in the controller log. The sibling not-Ready branches in the same function put
  the error in the condition (`BucketNotFound` with `Message: refMsg` at line 75, `BindingResolveFailed` with
  `Message: berr.Error()` at line 89), and so does `internal/services/identity/reconcile.go:82`. Fix: carry
  `cerr.Error()` into the condition's Message on this path. Not blocking: the issue's defect (stale Ready and a
  live-looking endpoint) is fixed without it.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** Reverted 1c507fb's code with the test file
  kept from HEAD: `TestIssue104_ConvergeErrorMarksCatalogNotReady` fails at `reconcile_test.go:382` with
  `expected: "Pending" actual: "Ready"` — "an engine that cannot be recreated is not Ready". That is the stale
  Ready the issue reports.
- **Passes with the fix under `-race`**: `go test -race -count=1 ./internal/services/catalog/` → `ok`; the test
  itself → `--- PASS`. The worktree was reset to 1c507fb and is clean.
- **Mutants (3/3 killed)**:
  - M1, drop the post-write `if cerr != nil { return … }` → FAIL ("a failed converge still fails the pass, so the
    controller backs off"). The backoff is kept.
  - M2, drop the `st = …EngineConvergeFailed` assignment → FAIL (reason assertion).
  - M3, set `Ready: true` in the substituted status → FAIL ("an engine that cannot be recreated is not Ready").
- **Root cause, not symptom.** The issue names the early `return fault.Wrapf(cerr…)` before the status write-back.
  The fix routes the error through the existing not-Ready branch: Phase=Pending, Ready=False, an endpoint that is
  no longer the proxy URL, and `syncIngressRoute(ctx, cs, "")` retracting the edge route. It then still returns the
  wrapped error after the write, so the controller's backoff is unchanged. No timeout, retry or swallowed error was
  added. The consumer gate (`internal/function/catalog.go:60`, `Phase != Ready || Endpoint == ""` → requeue) now
  stops admitting consumers to the dead engine.
- **Recovery is tested**: the test's third pass (Converge succeeds again) asserts Ready on the *same* proxy URL,
  in line with ADR-0137/ADR-0142 (the proxy keeps its URL across an engine move).
- **Scope**: both hunks serve the issue. The test-only `err` field on `fakeProvider` is the smallest extension of
  the existing fake. No test was weakened or deleted.
- **Reuse**: no new helper, type or dependency. The fix reuses the existing not-Ready branch, `syncIngressRoute`,
  `retryOnConflict` and the existing test harness (`newReconciler`, `newRecordingRoutes`, `seedCatalogBucket`,
  `cataloggw.NewManager`, `lakeRouteSource`).
- **Conventions**: `api/fault` wrapping is kept with the original kind and message, the import is top-level
  (`errors`, test only), and the code has one *why* comment that cites the issue. `go vet` is clean,
  `golangci-lint run ./internal/services/catalog/...` → `0 issues.`, and `gofmt -l` is empty.
- **ADRs**: no ADR file was touched. ADR-0142 (Implemented) asks that a replacement that cannot boot be visible.
  The fix uses the CatalogService's existing not-Ready vocabulary (`Pending` + a Ready=False reason, as the other
  branches do; CatalogService has no `Degraded` phase), so it contradicts no Decision or Contract.
- **Shape**: `fix(catalog):` subject, `Fixes #104`, the Co-Authored-By trailer, and one issue in one commit.

### Definition of Done
10 / 10 applicable items hold. Item 8 was checked for the touched package only (tests with -race, vet and lint on
the host). Linux lint, e2e and the Lima lane are left to the group gate, as this review's scope sets out.

### Model scorecard
Ledger fields: claude-opus-5-5 on issue #104 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/10.
(Not recorded by this run; the orchestrator records it.)

### Recommendation
Pass. Optionally fold the converge error into the condition Message (Minor) before the PR; nothing else blocks.
