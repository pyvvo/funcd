## Verdict: pass — 0 blockers, 0 majors, 3 minors  (issue #429 fix, model: claude-opus-5-5)

Change: branch `fix/i429`, commit 13a6608 `fix(funcdctl): delete resources a dev hot-reload no longer names`
(`cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_test.go`).

The watcher now carries the set of resources it applied (`watchHandlers` → `reloadChanged(..., applied *[]v1.Object, ...)`),
and after the Functions are re-applied, `pruneRemoved` (`cmd/funcdctl/dev.go:883-912`) deletes, last applied first,
each resource that the re-synthesized set no longer holds. It does not delete when the reload failed to apply.
A delete that the deletion protection refuses (ADR-0073: a KVStore or Bucket that still holds data) keeps the
resource with a warning, so the next reload retries it. This removes the cause the issue names
(`reloadChanged` applied and never deleted).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Two safety branches of `pruneRemoved` have no test** · attribution: model. Two overlay mutants survive
  `TestIssue429_|TestIssue320_|TestIssue135_|Reload` (`ok`, about 5 s each):
  M3 `if del {` → `if true {` (a reload whose apply failed still deletes, although a Function may still bind
  the removed resource), and M4 `return append(next, kept...)` → `return next` (a refused KVStore is forgotten,
  so the "next reload retries it" claim in the doc comment is not protected). The core behavior is tested
  (M1 and M2 fail). Fix (builder): add a sub-test that makes a reload apply fail and asserts the removed
  resource survives, and one that empties the protected store and asserts that a later reload deletes it.
- **Transport-swap boilerplate copied from a sibling test** · attribution: model. The six lines that wrap
  `http.DefaultClient.Transport` (`cmd/funcdctl/dev_test.go:521-529`) repeat
  `cmd/funcdctl/dev_phase2_test.go:197-205`, and `deleteStatus` is a second one-off `RoundTripper` beside
  `conflictOnFirstPut` (`dev_phase2_test.go:162`). The duplication is small, so this is Minor. Fix
  (builder, optional): a shared `swapDefaultTransport(t, func(next http.RoundTripper) http.RoundTripper)`
  test helper.
- **A `--persist` restart still does not prune** · attribution: issue (not scored). The issue's second
  root-cause bullet (`bootDev` only applies) is unchanged: a binding removed while the session is stopped
  leaves its object in the durable metastore, because `applied` starts from the boot's set. The issue's
  Expected behavior covers reloads only, and a boot-time prune needs a rule for which persisted objects
  `funcdctl dev` owns (it must not delete objects that a user applied with `funcdctl apply`). Recommendation:
  file a follow-up issue (or a `needs-adr` issue if an ownership marker is needed).

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `cmd/funcdctl/dev.go` checked out from `origin/main`
  with the test file kept → `TestIssue429_DevReloadDeletesRemovedResources/unused` fails on "the reload
  deletes the KVStore and ConfigMap no manifest names" and `/holds-keys` on "the reload deletes the KVStore
  no manifest names" (both `Condition never satisfied`). Worktree reset to 13a6608, clean.
- **Passes with the fix under `-race`** (`-tags dev`, the tag the dev files need): `unused` and
  `holds-keys` PASS. The neighboring reload tests `TestIssue135_…`, `TestIssue320_…` and
  `TestScenarioDevPersist…` also PASS.
- **Mutants M1 and M2 are killed**: M1 `len(errs) == loadErrs` → `len(errs) == loadErrs+1` (never delete)
  and M2 `if want[key(o)]` → `if !want[key(o)]` both fail `TestIssue429_`.
- **Data is never dropped**: the `holds-keys` sub-test sees the delete answered `409 Conflict` by the
  ADR-0073 deletion protection, and the key survives the reload. This answers the issue's open question
  (delete or report a data-bearing store) in a way that agrees with ADR-0073.
- **Root cause, not symptom**: the reload now converges the dev namespace on the manifests' desired state
  (ADR-0125 "watch files, re-apply on change"). Ordering is sound: removed resources are deleted after the
  Functions that stopped binding them are re-applied, in reverse apply order. A Conflict on the resource
  apply returns before the prune, and a load error on one manifest keeps that function's old resources in
  the set.
- **Scope**: two files; every hunk serves the issue; no test weakened or deleted.
- **Reuse**: no existing prune or diff-delete helper exists in `cmd/funcdctl`, `sdk` or `internal`;
  `pruneRemoved` uses the SDK's `Delete`, `api/fault` kinds and `slices.Reverse`.
- **Conventions**: ctx-first, `slog` only, `api/fault` error kinds, the `devNamespace` constant, top-level
  imports, block-style YAML in the test manifests, comments that state the why.
- **ADRs**: no ADR file touched; ADR-0125 and ADR-0073 are honored.
- **Checks (touched package)**: `go vet` and `go vet -tags dev` clean; `golangci-lint run` and
  `golangci-lint run --build-tags dev` → `0 issues.`; `go test -race` → `ok` both without and with
  `-tags dev`; no data race reported.
- **Shape**: `fix(funcdctl):` subject, `Fixes #429`, the attribution trailer, one issue in one commit.

### Definition of Done
10 / 11 items hold. Partial miss: item 4 (two of four mutants survive; see the first Minor, model).
Item 8 holds for the touched package (host build, vet, lint, race tests); the Linux lint, the e2e suite and
the lanes are left to the group gate.

### Model scorecard
Not recorded here (the orchestrator records it): claude-opus-5-5 on issue #429 (fix) → pass, 0/0/3,
2 model-attributed, DoD 10/11.

### Recommendation
Ship. Optionally, the builder adds the two missing sub-tests (failed-apply gate and retry of a refused
delete). File a follow-up issue for the `--persist` boot-time prune.
