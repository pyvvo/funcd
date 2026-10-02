# Fix review — issue #103 (claude-opus-5-5)

- **Issue**: #103 "A CatalogService that goes not-Ready keeps its external edge entry live"
- **Change**: branch `fix/i103`, commit 70f6ab3 `fix(catalog): retract the edge entry when a CatalogService goes not-Ready on a binding`
- **Files**: `internal/services/catalog/reconcile.go` (+7), `internal/services/catalog/reconcile_ingress_test.go` (+63)
- **Governing ADRs**: ADR-0138 (decision item 4, scenario catalog-external-teardown), ADR-0137, ADR-0121, ADR-0002
- **Verdict**: **pass**

## Summary

The BucketNotFound (`resolveBucketRefs` requeue) and BindingResolveFailed (`engineEnv` error) branches of
`Reconciler.Reconcile` returned before the post-Converge block, the only not-Ready path that cleared the
catalog's edge source. The fix calls the existing `syncIngressRoute(ctx, cs, "")` in both branches before
they write Pending/Ready=False. This is the cause named in the issue, fixed at the cause, through the
existing helper, and matches ADR-0138's "On delete / not-Ready the reconciler Sets the catalog source nil".

## Blockers

None.

## Majors

None.

## Minors

1. **The proxy keeps running on a binding not-Ready** (attribution: `issue`, not scored). The issue's
   Expected section adds, in parentheses, that the reconciler should also stop its proxy. The fix retracts
   only the edge entry. That matches the ADR-0138 contract (the edge source) and the pre-existing
   post-Converge not-Ready branch, which also leaves the ADR-0137 proxy and `status.endpoint` in place.
   Stopping the proxy on not-Ready is an ADR-0137 lifecycle question beyond this issue's edge-exposure
   scope. Recommend a follow-up issue if the proxy lifecycle on not-Ready should change.

## Verification (run)

| Check | Result |
|---|---|
| Pre-fix (overlay of `origin/main` `reconcile.go`), `-run TestIssue103_` | FAIL — both subtests (`bucket-deleted`, `secret-deleted`) fail on "a not-Ready catalog's edge entry is retracted": the issue's reason |
| `git revert --no-commit 70f6ab3` | also removes the test (`no tests to run`), so the overlay above is the meaningful revert check; worktree reset to 70f6ab3, clean |
| With fix, `-race -count=3 -run TestIssue103_` | PASS, 6/6 subtest runs |
| `go test -race ./internal/services/catalog/...` | ok |
| Mutant m1: drop the clear in the BucketNotFound branch | killed (`bucket-deleted` fails) |
| Mutant m2: drop the clear in the BindingResolveFailed branch | killed (`secret-deleted` fails) |
| Mutant m3: pass a non-empty proxy URL in the BindingResolveFailed clear | killed (`secret-deleted` fails) |
| `gofmt -l`, `go vet`, `golangci-lint` (touched package, host) | clean, 0 issues |
| Linux lint, e2e, lanes | not run here — the group gate runs them |

## Verified correct

- **Root cause, not symptom**: no timeout, retry or skip; both early-return branches now honor the
  not-Ready retraction contract.
- **Reuse**: the fix calls the existing `syncIngressRoute` (which already clears when `proxyURL == ""`) rather than
  re-implementing `routes.Set(src, nil)`. The test reuses `newReconciler` with a deps option,
  `recordingRoutes`, `fakeSecrets` (its `err` field), `seedCatalogBucket`, `mkCatalogService` and
  `reconcileLake`. The three lines of manager setup mirror `ingressReconciler`, but that helper does not
  take a secrets handle, so this is justified.
- **Ordering**: the route is cleared before the status update, so a status-update conflict retry never
  leaves the edge live. A route-clear error returns first, and the next reconcile retries it.
- **Scope**: every hunk serves the issue; no test was weakened or deleted; no ADR file was touched.
- **Conventions**: `fault`-wrapped error from the helper is propagated unchanged as in the post-Converge
  branch; imports at top level; one short ADR-citing comment; no YAML.
- **Shape**: `fix(catalog):` subject, `Fixes #103`, the attribution trailer, one issue per commit.

## Fix checklist

11 of 11 items hold. Item 8 was verified on the host for the touched package; Linux lint and e2e are deferred to the group gate.

## Recommendation

Pass. Optionally file a follow-up issue on the proxy and endpoint lifecycle when a CatalogService goes not-Ready (ADR-0137).
