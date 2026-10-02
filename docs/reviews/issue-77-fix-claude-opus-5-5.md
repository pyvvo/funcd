## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #77 fix, model: claude-opus-5-5)

Change: branch `fix/i77`, commit 2f1ae21 `fix(function): bring up a Function or CatalogService once its
ConfigMap or Secret is applied` (5 files, +109/-2).

The issue: the Function binding gate (gate 3c) and the CatalogService `engineEnv` gate fail closed on a
missing ConfigMap or Secret with `RequeueAfter=0`. No reconciler maps a ConfigMap or Secret event back to
the objects that bind it, and the store coalesces an unchanged re-apply (ADR-0047). The object therefore
stays Failed or Pending after its binding is applied. The fix requeues both gates every 2 s when the
resolve error is `fault.NotFound`. Status stays fail-closed until the binding resolves, which matches the
ADR-0121 data-reference gate and the `CatalogNotReady` precedent.

### 🟡 Minor

- **The NotFound guard is not tested.** · attribution: model · Two overlay mutants that replace the guard
  with `if true` (requeue on every resolve failure) survive. One is in `internal/function/function.go` and
  one is in `internal/services/catalog/reconcile.go`. The full `internal/function` and
  `internal/services/catalog` suites print `ok` with each mutant. The commit message claims that a PDP
  deny, an unconfigured resolver and the pooled gate keep the no-requeue behaviour, but no test asserts
  it. The damage is small: a permanent failure would be retried every 2 s and would stay fail-closed.
  Fix: assert `RequeueAfter == 0` in an existing Forbidden case, for example
  `TestScenarioUnauthorizedSecretFailsMaterialization`, and in a CatalogService Forbidden case.

### ✅ Verified correct (keep it)

- **Revert check.** Command: `git revert --no-commit 2f1ae21`, with the two test files kept at HEAD,
  then `go test -run TestIssue77`. All four subtests FAIL with `"0s" is not positive — a missing binding
  requeues the Function` (or `the CatalogService`). That is the issue's reason, `requeueAfter=0`. After
  `git reset --hard 2f1ae21`, the worktree is clean at HEAD.
- **With the fix.** `go test -race -count=1` passes for `./internal/function/`,
  `./internal/services/catalog/` and `./cmd/funcdctl/`. `go vet` passes, golangci-lint reports
  `0 issues`, and `gofmt -l` is empty for the touched packages.
- **Mutant M1.** Changing `== fault.NotFound` to `== fault.Conflict` in `function.go` makes
  `TestIssue77_MissingBindingRecoversWhenApplied` FAIL. The requeue path is guarded. The catalog
  zero-requeue revert is also killed by the revert check.
- **Cause, not symptom.** The fix removes the cause the issue names: the no-requeue fail-closed path. It
  adds no timeout, swallows no error and skips no test. `fault.KindOf` uses `errors.As`, and it reaches
  through `errors.Join(fault.Wrapf(gerr, fault.KindOf(gerr), …), ErrConfig/ErrSecret)` in
  `internal/envresolve/envresolve.go`. The `secrets` resolver keeps the store's NotFound kind
  (`internal/secrets/secrets.go`), so the guard matches both missing-ConfigMap and missing-Secret.
- **ADRs.** The fix keeps ADR-0093 Decision 5: binding validity stays fail-closed at reconcile. The
  existing `TestScenarioConfigMissingFailsClosedReconcile` and `TestScenarioMissingSecretFails` still
  assert `PhaseFailed` with no worker. The fix also keeps ADR-0057 fail-closed injection and follows the
  ADR-0121 accept-and-requeue pattern. No ADR file was edited, and the diff touches no `docs/` path.
- **Scope.** Every hunk serves the issue. The `cmd/funcdctl/dev.go` comment was the only stale "does not
  requeue" claim, and the fix updated it. A grep finds no other stale claim in the code, the feat docs or
  the blueprint. No test was weakened or deleted.
- **Reuse.** The fix uses the existing `gateFailure.requeue` field and `gateFailed` (`earliest(requeue,
  drainAfter)`). It uses the same `2 * time.Second` literal as the sibling gates in both files, and no
  shared constant exists. The tests reuse `newHarness`, `h.reconcile`, `h.getFn`, `h.running`,
  `seedCatalogBucket`, `newReconciler`, `mkCatalogService` and `reconcileOnce`. They use the real
  `secrets.NewResolver` with `rbac.New()` against the harness store. `newSecretHarness` cannot be used,
  because it builds its own store, which the real resolver needs.
- **Conventions.** The code follows ADR-0002: `api/fault` kinds, ctx-first and no `any`. Imports are at
  the top level, and each gate has one short comment that explains why.
- **Commit shape.** The subject is a conventional `fix(function):`. The commit has `Fixes #77` and the
  attribution trailer, and it covers one issue.

### Definition of Done

10 of 11 items hold. Miss: item 4 is only partly met, because the NotFound discrimination survives the
mutants (Minor, model). Item 8 was checked here for the touched packages: race tests, vet, host lint and
gofmt. The group gate runs the Linux lint, the e2e suite (including the issue's
`TestChaos_config_secret_E2EFunctionBefore*` probes) and the lanes.

### Model scorecard

Not recorded here. The workflow records the ledger row for claude-opus-5-5 on issue 77 (fix) → pass,
0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation

Pass. The fix is minimal, removes the root cause and follows its precedent. Optionally add a
`RequeueAfter == 0` assertion to a Forbidden case, so that the NotFound-only requeue is pinned.
