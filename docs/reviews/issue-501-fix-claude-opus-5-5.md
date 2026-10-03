## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #501 fix, model: claude-opus-5-5)

Change: `fix/i501`, commit 4b98516 `fix(funcdctl): delete the renamed Workflow and removed step Functions on a dev reload`
(`cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_phase3_test.go`).

### 🟡 Minor 1 — a removed step's synthesized resources are pruned only on the next function edit  ·  attribution: model
- Evidence: `devWorkflow.reapply` (`cmd/funcdctl/dev.go`) now drops the removed step's handler from `hs` and prunes its
  Function and the old Workflow. The KVStore / Bucket / ConfigMap / Secret / CatalogService that only the removed
  step's manifest declared stay in the watcher's `applied` set. `reloadChanged` prunes them only when some
  remaining function's files change, because it returns early when `len(changed) == 0`.
- Effect: after a step is removed, its backing resources stay on the dev control plane until the next source edit of
  another function. No data is lost, and the issue's two reported cases (rename, removed-step Function) are fixed.
- Fix (follow-up, optional): after a reapply that dropped handlers, re-synthesize the resources over the remaining
  handlers and prune the watcher's `applied` set, or record this as a separate issue.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With `git revert --no-commit 4b98516` and the new test file kept,
  `go test -tags dev -run TestIssue501_ ./cmd/funcdctl/` fails at `dev_phase3_test.go:193` with "Condition never
  satisfied — the reload deletes the Workflow of the old name and the Function of the removed step". After
  `git reset --hard 4b98516`, the worktree is clean at that HEAD.
- **Passes with the fix under -race.** `TestIssue501_…`, `TestIssue428_DevReloadsEditedWorkflow` and
  `TestIssue429_DevReloadDeletesRemovedResources` pass with `-race -tags dev`. The full `-tags dev -race` package
  run of `cmd/funcdctl` passes, and so does the untagged `-race` run.
- **Mutants (3/3 killed):**
  - M1: replace the `w.applied = pruneRemoved(…)` call in `reapply` with a no-op. Result: fails at :193.
  - M2: discard the returned handlers in `watchHandlers` (`_, werr = wf.reapply(…)`). Result: a nil-pointer panic in
    `fingerprint`, because `slices.DeleteFunc` zeroes the tail in place. The reassignment is therefore required, and
    the test catches its loss.
  - M3: leave `wf.obj` out of `wf.applied` in `bootDev`. Result: fails at :193 because the old-name Workflow survives.
- **Cause, not symptom.** The issue names three root-cause lines: reapply never deletes, the watcher's `applied` holds
  only `resObjs`, and removed steps are not handled. The fix addresses each one: the workflow tracks its own
  `applied` set (step Functions and the Workflow), prunes it after a successful apply, and drops the handlers of
  removed steps, so an edit to a removed step's files no longer re-applies its Function. The test's last block
  proves this.
- **Deletion order is safe.** `pruneRemoved` deletes the last-applied object first, so the old Workflow is deleted
  before the step Functions it referenced. The prune runs only after the new Workflow was applied, so the current
  Workflow never references a deleted Function. The namespace is safe because `resolveWorkflowPlan` forces
  `devNamespace`, which is the namespace `pruneRemoved` deletes in.
- **The slice is not shared.** `handlers` from `bootDev` is passed only to `watchHandlers`, so the in-place
  `slices.DeleteFunc` affects no other holder.
- **Reuse.** The fix reuses `pruneRemoved` (the #429 prune, which keeps the ADR-0073 data-refusal warning),
  `synthesizeFunction` and `applyDesired`. It adds no new helper. The test reuses `devProject`, `startDevPath`,
  `requireNode` and `permissiveContract`.
- **Conventions.** Errors use `api/fault`, signatures put ctx first, logging uses only slog, and all imports are at top
  level. The test YAML is block style. The only comment is one "why" comment about the reload-pass ordering. The
  doc comments on `devWorkflow` and `reapply` were updated to match the new behavior.
- **Scope.** Two files changed. Every hunk serves #501, and no test was weakened or removed.
- **ADRs.** The change is consistent with ADR-0125 ("watch files, re-apply on change"; the manifests are the
  session's desired state). No ADR file was touched.
- **Checks on the touched package.** `go vet` (untagged and `-tags dev`): clean. `golangci-lint run` (untagged and
  `--build-tags dev`): 0 issues. `gofmt -l`: clean.
- **Shape.** The subject is `fix(funcdctl): …`, the body has `Fixes #501` and the attribution trailer, and the commit
  covers one issue.

### Definition of Done
11 / 11 items hold. Item 8 was checked on the host only (build, vet, lint and the package's tests). Linux lint, e2e
and the lanes are left to the group gate, as instructed.

### Model scorecard
Not recorded here. The ledger fields are returned to the orchestrator: claude-opus-5-5 on issue #501 (fix) → pass,
0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Sign off and proceed to the PR. Track Minor 1 (resources of a removed step are pruned lazily) as a follow-up, or fold
it into a later fix. It does not block this one.
