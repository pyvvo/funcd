## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #147 fix, model: claude-opus-5-5)

Change: branch `fix/i147`, commit `1d32689` — `fix(kv): keep KVStore status.bindings current when Functions bind or unbind`.
Files: `internal/controller/controller.go`, `internal/controller/controller_test.go`,
`internal/services/kv/reconcile.go`, `pkg/funcd/funcd.go`, `pkg/funcd/kvstore_bindings_test.go` (new).

Checklist: 11 of 11 items apply and hold (item 8 scoped to the touched packages; Linux lint, e2e and the
lanes are left to the group gate).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor 1 — every Function event fans out to every KVStore in the namespace  ·  attribution: model
`KVReconciler.MapFunction` (`internal/services/kv/reconcile.go:160`) enqueues all KVStores in the
Function's namespace on every Function Added/Modified/Deleted event, including the Function reconciler's
own status write-backs. Each KVStore reconcile then lists every Function in the namespace
(`countBindings`) and every key under the store prefix (`reclaimOrphanTables`). The queue deduplicates
requests, and the store coalesces byte-identical updates (`internal/store/store_test.go`
"noop-write-coalesced"), so this cannot loop. It is still a cost that grows with
Functions × KVStores × keys, and the commit message does not mention it. The namespace-wide mapping is
justified, because a Modified event carries no prior spec and an unbind no longer names the store.
A later refinement could cache each Function's last-seen `spec.kv` stores in the mapper and enqueue only
the union of old and new stores. This does not block.

### Minor 2 — the engine's exported surface grows beyond ADR-0015's Contracts  ·  attribution: adr
ADR-0015 (Implemented) lists the engine API as `New`, `Register` and `Run`, with "one watch per
registered gvk". The fix adds `Controller.Watches(gvk, MapFunc)` and the `MapFunc` type. The addition
does not contradict the Decision: own-kind enqueue stays uniform, the engine still never inspects object
internals (the MapFunc does), and deletes are not special-cased. A second Reconciler cannot be registered
for the Function gvk (`Register` overwrites it), so an engine seam is the clean fix, and `RequeueAfter`
polling would only hide the gap. No ADR file was edited. Record the seam so the next ADR that touches the
engine documents it. Not scored.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** The three non-test files were
  overlaid with their pre-fix versions (the merge-base, `HEAD~1`; `origin/main` has since moved one commit
  ahead and its `funcd.go` does not build against this tree). Then
  `go test -overlay … -run TestIssue147_ ./pkg/funcd/` failed with
  `Condition never satisfied … r1 + r2 bind s 3 times; got 0`. This matches the issue's
  "bindings=0 (expected 3)".
- **The requested `git revert --no-commit 1d32689` check is uninformative.** The revert also deletes the
  new test file, so the run reports `[no tests to run]`. The overlay check above is the real revert
  evidence. After the check the worktree was `git reset --hard` back to `1d32689`, and it is clean.
- **It passes with the fix under `-race`.** `go test -race -count=1 -run TestIssue147_ ./pkg/funcd/`
  printed `ok`. The test is not skipped. It covers the issue's two transitions: binding (0 to 3) and an
  unbind (3 to 1).
- **Mutants, all killed:**
  - M1: `MapFunction` returns nil, which makes `TestIssue147_` fail.
  - M2: `forward` drops the mapped requests, which makes `TestIssue147_` and `TestWatchesEnqueuesMappedRequests` fail.
  - M3: `Run` opens no watch for a mapper-only gvk, which makes `TestWatchesEnqueuesMappedRequests` fail.
    `TestIssue147_` passes under M3, which is correct, because Function already has its own Reconciler
    and therefore its own watch.
- **The root cause is fixed, not masked.** The issue named the missing Function→KVStore mapping
  (`controller.go` enqueued only the changed object's own GVK). The fix adds that mapping. It adds no
  timeout, retry or polling.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted. The own-kind enqueue is now
  gated on a registered Reconciler, which is equivalent for every existing kind, because a gvk was watched
  only when it was registered.
- **Reuse.** No mapping or secondary-watch mechanism existed in `internal/controller` or elsewhere. The
  recount reuses the existing `countBindings`. The names mirror the controller-runtime
  `Watches`/map-func idiom. No new dependency was added.
- **Conventions.** The engine stays ctx-first and logs only through slog
  (`WarnContext` on a list error, then nil, so a watch never dies on a mapper failure). There is no `any`
  in a signature, the typed `v1.GroupVersionKind`/`ObjectName` are used, and imports are at the top level.
  Comments are short and explain why. The test uses `p.cfg.store` like `pkg/funcd/funcd_test.go`.
  `gofmt -l` reported nothing on the touched packages.
- **ADRs.** No ADR conflicts with the change. ADR-0073 Decision 7 ("the reconciler sets
  status.tables/bindings") is now true when Functions change. No Accepted or Implemented ADR file was touched.
- **Checks, touched packages:** `go test -race -count=1 ./internal/controller/ ./internal/services/kv/`
  printed ok/ok. `go test -race -run 'KV|Issue147|KVStore' ./pkg/funcd/` printed ok. `go vet` on the
  three packages passed. `golangci-lint run` on the three packages reported `0 issues.`
- **Shape.** The subject is `fix(kv): …`, the body has `Fixes #147` and the attribution trailer, and the
  branch holds one commit for one issue.

### Recommendation
Pass. Rebase onto the current `origin/main` before the PR, because the branch is one commit behind.
Minor 1 is an optional efficiency follow-up, and Minor 2 goes to the next ADR that touches the
controller engine.
