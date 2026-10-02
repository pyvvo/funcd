# Fix review — issue #372 (model: claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #372 fix, model: claude-opus-5-5)

Change: branch `fix/i372`, commit `80df7be` — `fix(catalog): stop serving a CatalogService held not-Ready by a missing bucket or binding`.
Files: `internal/services/catalog/reconcile.go`, `internal/services/catalog/catalog.go` (doc comment),
`internal/catalog/gateway/manager.go`, `internal/services/catalog/reconcile_test.go`.

The issue: the `BucketNotFound` and `BindingResolveFailed` gates wrote `Pending` and retracted the edge entry,
but left the engine running, the node-private PEP proxy forwarding with the engine token, and `status.endpoint`
at the proxy URL. The fix routes both gates through one `holdNotReady` helper that retracts the edge entry,
suspends the proxy (new `Manager.Suspend`: 503 for every request, target and token dropped, listener kept),
clears `status.endpoint`, and tears the engine down.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### 🟡 Minor 1 — `ReconcilerDeps.Proxy` comment names only one of the two gates  ·  attribution: model

`internal/services/catalog/catalog.go:75` says the reconciler "Suspends it while a binding is missing". It also
suspends on a missing Bucket (`BucketNotFound`). Cosmetic; "while the catalog is held not-Ready" would be exact.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix.** `git revert --no-commit 80df7be` with the fix's test file kept:
  `TestIssue372_NotReadyGateStopsServing` fails in both subtests (`bucket-deleted`, `secret-deleted`) with
  "a not-Ready catalog publishes no endpoint" — the pre-fix gate leaves the proxy URL published. Reset to
  `80df7be`: passes under `-race`. Worktree left clean at `80df7be`.
- **The test covers every part of the issue, not only the first assertion.** Overlay mutants, each killed:
  1. drop `r.proxy.Suspend(...)` in `holdNotReady` → fails "the proxy consumers hold no longer forwards" (the
     proxy still reached the engine);
  2. replace `r.prov.Teardown(...)` with `error(nil)` → fails "the engine is stopped";
  3. drop `mp.upstream, mp.engineToken = "", ""` in `Manager.Suspend` → recovery fails (503 instead of 200
     after the binding returns), because `Ensure` sees an unchanged target and does not retarget.
- **Cause, not symptom.** The issue names the two gates at `reconcile.go` that called only `syncIngressRoute`;
  both now stop the engine, the proxy and the endpoint. No timeout, retry or swallowed error.
- **#59 respected.** `Suspend` keeps the listener, so the URL already injected into Functions is the same when
  the catalog is Ready again; the test asserts the same `proxyURL` and a 200 through it after the rebind.
- **Clearing `status.endpoint` is safe for consumers.** `internal/function/catalog.go:60` already requires
  `Phase == Ready` and a non-empty endpoint, so a not-Ready catalog was already unbindable; the endpoint clear
  only makes the status truthful.
- **Teardown on every gated requeue is safe.** `provider.Teardown` (`internal/provider/runtime.go:164`) is
  documented and coded idempotent (a missing engine is fine); the delete path already relies on that.
- **Reuse, no duplication.** `holdNotReady` replaces two copy-pasted gate blocks; `Suspend` reuses the existing
  `retargetable.set` handler slot rather than adding a new mechanism; `http.Error` from the standard library is
  used as in `proxy.go`. No new dependency, type or harness; the test reuses `newReconciler`, `fakeProvider`,
  `fakeSecrets`, `seedCatalogBucket`, `mkCatalogService` and `reconcileOnce`.
- **Conventions (ADR-0002).** ctx-first, `const op`, errors through `api/fault` (`fault.Wrapf` with the
  wrapped kind), no `any`, no new logging, top-level imports, comments explain the why (ADR-0057, ADR-0138,
  #59, #372) without narration. Naming follows the package (`syncIngressRoute`, `retryOnConflict`).
- **ADRs.** The fix brings the code in line with the comment's stated ADR-0057 fail-closed posture; it does not
  contradict ADR-0137 (proxy fronts the engine only while Ready) or ADR-0138 (edge retracted when not Ready).
  ADR-0087's "no scale-to-zero" pins replicas for a serving catalog; stopping a gated, not-Ready engine is the
  same posture the delete path and the issue's expected behavior call for. No ADR file is touched.
- **Scope.** Every hunk serves the issue; no test was weakened or deleted.
- **Checks (touched packages).** `go build ./...` ok; `go test -race -count=1 ./internal/services/catalog/
  ./internal/catalog/gateway/` ok; `go vet` ok; `golangci-lint run` on both packages: 0 issues; `gofmt -l` clean.
- **Shape.** `fix(catalog):` subject, `Fixes #372`, attribution trailer, one issue in one commit.

### Definition of Done

| # | Item | Result |
|---|---|---|
| 1 | `TestIssue372_…` reproduces the issue | ✅ |
| 2 | Fails on the pre-fix code, for the reported reason | ✅ |
| 3 | Passes with the fix, un-skipped, `-race` | ✅ |
| 4 | Reverting or mutating the key lines fails a test | ✅ (3/3 mutants killed) |
| 5 | Root cause fixed, not masked | ✅ |
| 6 | Only the issue's scope; no test weakened | ✅ |
| 7 | No ADR contradicted or edited; living docs true | ✅ |
| 8 | Build, vet, lint, tests green | ✅ host, touched packages (Linux lint, e2e and lanes run at the group gate) |
| 9 | Conventions hold | ✅ (one cosmetic comment nit, Minor 1) |
| 10 | Reuses what exists | ✅ |
| 11 | Commit shape | ✅ |

11 of 11.

### Model scorecard

claude-opus-5-5: pass, 0 blockers, 0 majors, 1 minor (model-attributed: 1), DoD 11/11.

### Recommendation

Pass. Optionally reword the `ReconcilerDeps.Proxy` comment to cover the Bucket gate when next touching the file;
it does not block the PR. Hand back to `/fix` Step 8.
