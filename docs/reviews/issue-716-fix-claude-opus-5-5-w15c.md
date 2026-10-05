## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #716 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i716`, one commit `3fbc6e49` "fix(catalog): suspend the catalog proxy while the engine is
not Ready". Files: `internal/services/catalog/reconcile.go` (+4 code lines), `internal/services/catalog/catalog.go`
(one doc-comment line), `internal/services/catalog/reconcile_test.go` (+80, the regression test).

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Proof on current main (the decided "prove first" rule).** Overlay of the current `origin/main` (`a394c6f1`)
  `reconcile.go` and `catalog.go`, the test kept:
  `go test -overlay revert.json -run TestIssue716 ./internal/services/catalog/` → `FAIL`; both cases fail for the
  issue's reason: `expected: 503 actual: 200 — the proxy consumers hold no longer forwards` in `engine-moved` and
  `converge-failed`. Each case of the issue (engine moved and not probed Ready; `Converge` error) has its own
  sub-test and its own failing proof.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/services/catalog/ ./internal/catalog/gateway/`
  → `ok` / `ok`; `-race -count=10 -run TestIssue716` → 10/10 passes. The #372 and #104 regression tests stay green.
- **Root cause, not symptom.** The engine-not-Ready `else` branch (`reconcile.go:146-166`) now calls
  `r.proxy.Suspend(cs.Namespace, cs.Name)` after retracting the edge entry, the same call `holdNotReady` makes for the
  gate branches (#372). `Manager.Suspend` (`internal/catalog/gateway/manager.go:254-261`) installs the 503 handler and
  clears `upstream`/`engineToken`, so the next Ready pass's `Ensure` sees a changed upstream and retargets the same
  listener: the URL is kept (#59 posture), which the test asserts (`consumers keep the URL they were injected with`).
  No retry, timeout or swallowed error.
- **Mutants (overlay, restored).**
  1. Suspend only on a `Converge` error (`if r.proxy != nil && cerr != nil`) → `TestIssue716/engine-moved` FAILs.
  2. `Manager.Suspend` keeps `upstream`/`engineToken` (the line that lets `Ensure` retarget removed) → both
     `TestIssue716` cases and both `TestIssue372` cases FAIL (recovery to 200 on the same URL is asserted).
  3. The revert overlay above (Suspend call absent) → both cases FAIL.
- **Scope.** Every hunk serves the issue; the `catalog.go` doc comment on `ReconcilerDeps.Proxy` was corrected from "a
  missing binding Suspends it" to "a not-Ready pass Suspends it", which keeps it true. No test weakened or deleted.
- **Reuse.** The fix reuses `Manager.Suspend`; the test reuses the package helpers (`seedCatalogBucket`, `fakeSecrets`,
  `fakeProvider`, `newReconciler`, `mkCatalogService`, `reconcileOnce`) and `cataloggw.NewManager`, the same harness
  shape as `TestIssue372_NotReadyGateStopsServing`. No new helper, type or dependency.
- **Conventions.** ctx-first, nil-safe proxy guard as in `holdNotReady`, `fault` wrapping untouched, comments explain
  the why and cite the issue, no comment bloat, table-driven test named `TestIssue716_…`.
- **ADRs.** Consistent with ADR-0091 (fail closed for catalog consumers) and ADR-0137 (the PEP proxy); ADR-0162 lists
  this branch under Scope Out, so nothing decided is contradicted. No ADR file touched.
- **Checks (touched packages).** `go vet ./internal/services/catalog/` clean; `golangci-lint run
  ./internal/services/catalog/...` → `0 issues.` Repo-wide, Linux and e2e checks are the group gate's.
- **Siblings.** All other not-Ready exits of the reconciler already suspend (`holdNotReady` for `BucketNotFound` and
  `BindingResolveFailed`); the delete path Releases. No other caller of the catalog proxy with the same gap.
- **Shape.** `fix(catalog):` subject, `Fixes #716`, attribution trailer, one issue in one commit.

Note for the integrator (not a finding): the branch base is `6baf945a`, behind current `origin/main`; the overlay
proof was run against current `origin/main` and the change applies to it.

### Definition of Done
12 / 12 items hold (item 8 checked for the touched packages; repo-wide, Linux and e2e run in the group gate).

### Model scorecard
Ledger fields: claude-opus-5-5 on issue #716 (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12.

### Recommendation
Ship with the group: hand back to `/fix` Step 8 for integration.
