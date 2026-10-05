## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #708 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i708`, commit 751f1796 `fix(kv): reclaim a deleted KVStore's data after a crash or a stop`.
Files: `internal/services/kv/reconcile.go` (new `Reconciler.ReclaimDeleted`), `pkg/funcd/funcd.go` (one call in
`Run`, before the Route load, the controller and the control-plane `Serve`), and two tests
(`internal/services/kv/reconcile_test.go`, `pkg/funcd/kvstore_reclaim_test.go`).

The decision for this issue was "prove first": the regression test must fail on current `origin/main` with no fix,
for the stated reason, with one proof per case. Both cases (crash and graceful stop) have their own subtest, and
both fail on current main.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **`kvRestartDir` duplicates `shortDataDir`** · attribution: model · `pkg/funcd/kvstore_reclaim_test.go:105-111`
  has the same body as `shortDataDir` in `pkg/funcd/shim_regression_e2e_test.go:129`. That file carries the
  `//go:build e2e` tag, so the non-e2e test cannot call it as it stands. A better fix is to move `shortDataDir`
  into an untagged `_test.go` file of `pkg/funcd` and reuse it here. This is trivial duplication, so it is a Minor.
- **The start sweep lists every KV key into memory** · attribution: model · `ReclaimDeleted` calls
  `r.kv.List(ctx, "")`, and the Badger driver returns every key as a sorted `[]string`
  (`internal/kvstore/badger/badger.go:301-326`). The sweep only needs the distinct `<ns>/<store>/` prefixes, but
  startup memory grows with the total number of keys. The `PrefixManager` port has only `List`, so this is
  acceptable at the current target scale. It is worth a note if KV volumes grow. No change is required for this
  fix.

### ✅ Verified correct (keep it)

- **Proof on current main (DoD 1, 2).** `origin/main` is a394c6f1 and the branch base is d4cfc2b7. Between them,
  no file changed in `pkg/funcd/funcd.go`, `internal/services/kv/`, `internal/gc` or `internal/controller`. In a
  scratch worktree of current `origin/main`, with only `kvstore_reclaim_test.go` copied in, both subtests fail
  for the reason the issue states:
  ```
  --- FAIL: .../crash_after_the_collector_deletes_the_store
        Messages: the re-created store reads "old-incarnation", the data of the deleted store
  --- FAIL: .../stop_while_the_delete's_reconcile_runs
        Messages: the re-created store reads "old-incarnation", the data of the deleted store
  FAIL github.com/pyvvo/funcd/pkg/funcd
  ```
  The overlay revert check on the branch, with both changed non-test files replaced by their `origin/main`
  versions, gives the same two failures. The kv unit test `TestIssue708_ReclaimDeletedDropsOnlyDeletedStores`
  does not compile under the overlay (`r.ReclaimDeleted undefined`), because it tests the new method directly.
  The worktree was removed and left clean.
- **The test passes with the fix under `-race` (DoD 3).** `go test -race -count=10 -run TestIssue708` gives
  `ok` for both packages. The full `-race` run of `./internal/services/kv/` and `./pkg/funcd/` also gives `ok`.
  No subtest is skipped.
- **Mutants (DoD 4).** Each mutant was applied as an overlay of `reconcile.go`, and each one fails a test:
  1. Drop the DNS-label guard (`ns.Validate()`/`name.Validate()`): `TestIssue708_ReclaimDeletedDropsOnlyDeletedStores`
     fails, because the `_eventing/blobwatch/` watermark prefix would be dropped.
  2. Invert the NotFound check, so the sweep drops live stores and keeps deleted ones: the unit test fails, and
     `TestIssue708_DeletedKVStoreDataReclaimedOnRestart` fails at platform level.
  3. Remove `checked[sp] = true`: the unit test fails, because the prefix is dropped once per key.
- **Cause, not symptom (DoD 5).** The issue's cause is that the reclaim happens only as an in-memory, NotFound-time
  side effect, with no durable fallback. The fix adds that fallback. The sweep runs at every start and compares
  the durable KV prefixes with the durable metastore, so neither a crash nor a dropped queued or delayed key can
  lose a reclaim. It runs after `MarkKVStoresOnce` and before the controller, the collector and
  `p.httpServer.Serve`, so no store with the same name can be created in the meantime. The `Workflow`
  re-materialization runs only under the controller, which starts after the sweep. There is no retry, timeout or
  swallowed error that hides the defect: a sweep failure is logged as a warning, and the next start retries,
  which is crash-only behaviour (ADR-0028).
- **Key layout safety.** Tenant keys are `<ns>/<store>/<table>/<key>` (`internal/services/kv/kv.go:67-69`). The only
  other user of the KV engine is the eventing watermark, `_eventing/blobwatch/` (`internal/eventing/watermark.go:43`).
  Its first segment is not a DNS label, so the sweep skips it. The Badger `Reserved` (`\x00`) keys never appear
  in `List`. The facade, the watermark, the reconciler and the admission prober are the only consumers of
  `c.kvStore` in `funcd.go`.
- **Scope (DoD 6).** Every hunk serves #708. No test was weakened or deleted.
- **ADRs (DoD 7).** No ADR file was touched. The fix implements ADR-0170's "also across a crash" and
  "no collection lost to a crash" without changing its decision. It stays within ADR-0170's accepted
  re-apply timing risk, and it does not reintroduce the rejected alternative A as the only mechanism.
  ADR-0072/0073 `DropPrefix` semantics are unchanged.
- **Checks (DoD 8, touched packages).** `go vet` passed for `./internal/services/kv/` and `./pkg/funcd/`.
  `golangci-lint` reported `0 issues.` for both. The `-race` tests passed. The Linux lint and the e2e suite are
  left to the group gate, as instructed.
- **Conventions (DoD 9).** Errors are wrapped with `fault.Wrapf` and keep their kind. `ctx` is the first
  parameter. The code uses typed `v1.NamespaceName` and `v1.ObjectName` with their own `Validate`, and
  `errors.Join` for per-store errors. Logging uses `slog`. Imports are at top level, and the comments explain the
  why without narrating the code. The new code reuses the existing `storePrefix` helper and the `PrefixManager`
  port.
- **Reuse (DoD 10).** It reuses `storePrefix`, the existing `PrefixManager` (`List` and `DropPrefix`), the test
  double `recPrefixManager` and the `mkKVStore` builder in the kv tests. The platform wiring mirrors the existing
  `routeReconciler` start step. The only gap is the duplicated helper described in the first Minor.
- **Shape (DoD 11).** The subject is `fix(kv):`, the body has `Fixes #708` and the attribution trailer, and the
  branch has one commit for one issue.
- **Every case and the siblings (DoD 12).** The crash case and the graceful-stop case each have their own
  subtest and their own failing proof. Sibling NotFound handlers were checked:
  - `services/identity` does nothing on NotFound.
  - `services/catalog` tears down only runtime state (routes, proxies and the provider engine), which is rebuilt
    at start, not durable data.
  No sibling has the same durable-leftover cause.

### Recommendation

Pass. The fix can go to the group integrator. The two Minors are optional follow-ups (untag and reuse
`shortDataDir`; a prefix-only listing if KV volumes grow) and do not block.
