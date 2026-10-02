## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #68 fix, model: claude-opus-5-5)

Fix under review: commit `24f9a2b` "fix(function): reclaim a pool worker once its last member is gone" on
`fix/205-pooling` (reviewed at group HEAD `2500327`). Touched: `internal/function/function.go`,
`internal/function/pool.go`, `internal/function/pool_test.go`.

### 🟡 Minor
- **The reclaimed pool's manifest file is left behind** · attribution: model · evidence:
  `reclaimOrphanPools` (`internal/function/pool.go`) retires the worker (stop + remove) and forgets the
  signature, but the per-key manifest that `writePoolManifest` wrote under the OS temp dir
  (`funcd-pool/<pool-name>.json`) is not deleted. The file lists the removed handlers' artifact paths. It is
  bounded per key, but worker ids are user-chosen, so churn over many ids leaves one file each. ADR-0047's
  "no per-resource residue" intent (cited by the issue) applies. Fix: remove the manifest file when an orphan
  pool is retired (owner: `/fix`, may ride with a follow-up). Not blocking: the process, the port and the
  loaded handlers, which are what the issue reports, are gone.

### Out of scope (recorded, not scored)
- `writePoolManifest` names the file from `poolInstanceName(key)`, which does not encode `key.Namespace`.
  Two namespaces with the same runtime and worker id share one manifest path, so a rebuild in one namespace
  can overwrite the other's manifest. This predates the fix and is not caused by it; it is worth a separate
  issue.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** `git revert --no-commit 24f9a2b`
  conflicted only in `internal/function/pool_test.go` (later commits of the group appended tests there), so
  the test file was kept at HEAD and the two non-test files were reverted. Both subtests of
  `TestIssue68_PoolReclaimedWithItsLastMember` then fail: `deleted` with
  `Should be empty, but was map[:map[0:running]]` / "the pool is removed with its last member", and
  `left the key` with the same running pool / "the pool is removed when its last member runs solo".
- **It passes with the fix**, after `git reset --hard 2500327`: `go test -race -count=3 -run TestIssue68_`
  passes 3/3, un-skipped.
- **The user-visible behavior is fixed** (e2e probe in the scratch area, using the real process driver and
  the Node pool host through `pooling_e2e_test.go`'s `poolHarness`): pools `lonely` (one member), `pair` (two
  members) and `moving` (its only member clears `spec.pooling.worker`) were all running. After the three
  deletes and the re-apply, **with the fix** no `__pool__` instance remains and the former member serves solo
  (HTTP 200). **With the pre-fix files overlaid**, all three pools are still running 10 s later. This covers
  the issue's steps and the drop-the-worker variant that the issue inferred.
- **Cause, not symptom.** The issue names two causes: `teardown` matches only the Function's own instance name,
  and `ensurePool` (the only place a pool is reclaimed) runs only from a surviving member. The fix adds
  the missing driver: on the delete path, and on the first pass of a new generation (the only way a
  member leaves a key: a change to `spec.pooling.worker` or `spec.runtime`), it lists the namespace's
  instances and Functions and retires every `__pool__` worker whose key no Function declares. No timeout,
  retry or skipped test is involved.
- **Ordering and concurrency.** It lists instances before Functions, so a pool created in between already
  has its member stored, as the doc comment states. The Function controller runs one worker
  (`controller.New` with the default `Workers`), so this pass cannot race a member's `ensurePool`. A pool
  whose members are only unadmitted (PoolFull) or unplaceable stays declared, and `ensurePool` still owns it.
- **Mutants: all 4 killed** (overlays on the key lines, full `internal/function` package):
  dropping the `__pool__` prefix guard → 7+ solo tests fail (it would retire solo workers);
  dropping the `declared[...]` guard → `TestIssue68` fails (m2's live pool is retired);
  disabling the new-generation call → `TestIssue68/left the key` fails;
  removing the delete-path call → `TestIssue68/deleted` fails.
- **Scope.** Every hunk serves #68. It extracts the `poolInstancePrefix` constant, which
  `poolInstanceName` now uses. No test was weakened or deleted.
- **Reuse.** It reuses `retire` (stop + remove, the same helper solo teardown uses), `poolKeyFor`,
  `poolInstanceName`, `runtime.List` and `store.List`. `forgetPoolSigOf` is new, but it is needed because a
  key cannot be recovered from an instance name without parsing it. No dependency was added.
- **Conventions.** Errors are `api/fault` wrapped with an `op`, ctx comes first, the types are typed
  (`v1.NamespaceName`, `v1.ObjectName`), and imports stay at the top level. Comments carry the *why*
  (ADR-0046 Decision 6, the list ordering) and do not narrate.
- **ADRs.** No ADR file was touched. The change matches ADR-0046 Decision 6: a pool with no admitted member
  has a desired count of 0, and a pool with no declaring member is removed outright, because no wake can
  ever target it. It is consistent with ADR-0047's no-residue intent apart from the minor above.
- **Checks (touched packages):** `gofmt -l internal/function` is clean. `go build ./...` passes on the host
  and with `GOOS=linux`. `go vet ./internal/function/ ./pkg/funcd/` passes. golangci-lint on
  `./internal/function/...` reports 0 issues on the host and 0 with `GOOS=linux`. These pass under `-race`:
  `go test -race ./internal/function/... ./internal/pooling/... ./internal/controller/...`. The e2e suite
  `go test -tags e2e ./pkg/funcd/...` passes (112 s), and `just check-hygiene` is clean. The Lima lanes are
  left to the group stage.
- **Shape.** The subject is `fix(function): …`, the body says `Fixes #68` and names the regression test, the
  `Co-Authored-By` trailer is present, and the commit covers one issue.

### Definition of Done
11 / 11 items hold (fix checklist). The Lima lane for item 8 is deferred to the group's full check run, as
the batch instructions require. Every lane in scope is green.

### Model scorecard
Not recorded here (batch mode). Ledger fields: claude-opus-5-5 on issue #68 (fix) → pass, 0/0/1,
1 model-attributed, DoD 11/11.

### Recommendation
Sign off. In a follow-up, delete the pool manifest file when an orphan pool is retired. Also file the
cross-namespace manifest-path collision as its own issue.
