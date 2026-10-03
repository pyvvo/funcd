## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #502 fix, model: claude-opus-5-5)

Change: branch `fix/i502`, commit cc1c93a `fix(funcdctl): prune stale dev objects on a --persist restart`
(`cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_phase2_test.go`; 99 insertions, 4 deletions).

The fix answers the issue's open question with its second option: every object `funcdctl dev`
synthesizes carries the tag `managed-by: funcdctl-dev` (`setMeta`, and `resolveWorkflowPlan` for the
Workflow). Over a durable metastore (`plan.storeDir != ""`), `bootDev` lists the tagged objects after
applying, deletes those the manifests no longer name through the existing reload prune
(`pruneRemoved`), and seeds the watcher's `applied` set with the ones that could not be deleted.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Objects persisted by a pre-fix session are never pruned or reported** · attribution: model ·
  `cmd/funcdctl/dev.go` `pruneStale` keeps only objects carrying `devManagedTag`. A `.funcd-dev/`
  metastore written before this change holds untagged objects; after the upgrade the first boot
  re-applies the still-named ones (they gain the tag), but a removed binding's object stays untagged
  and is kept silently. The issue's stated minimum was a boot warning about persisted objects the
  manifests no longer name. A one-time gap for a dev tool; a warning for untagged leftovers, or a note
  in the commit/PR, would close it.
- **The pruned kinds are a hand-kept list** · attribution: model · `pruneStale` lists
  `KVStore, Bucket, Secret, CatalogService, ConfigMap, Role, RolesAssignment, Function, Workflow`
  separately from what `synthesizeResources`/`resolveWorkflowPlan` emit. A kind added to the synthesis
  later is not pruned unless this list is updated too. Not a defect today (the list matches every
  `setMeta` call site); a comment tying the two together, or a test that checks the synthesized kinds
  are a subset, would guard it.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit cc1c93a`
  with the new test file kept, then
  `go test -tags dev -race -run TestIssue502 ./cmd/funcdctl/` →
  `--- FAIL: TestIssue502_DevPersistRestartPrunesRemovedResources (10.51s)`,
  `Condition never satisfied`, `the boot deletes the Function "back" no manifest names`. Worktree reset
  to cc1c93a, clean.
- **Passes with the fix under -race**, un-skipped (a runtime shim was available):
  `--- PASS: TestIssue502_DevPersistRestartPrunesRemovedResources (0.62s)`. Note the test file is
  `//go:build dev`; it runs in the `just test` dev-tag pass (justfile line 44), not in a plain
  `go test`.
- **The user-visible behavior**: the test drives the issue's own sequence through `startDev` — boot a
  `--persist` session with a `settings` ConfigMap binding and a second manifest, stop, remove the
  binding and the manifest, boot again — and asserts the Function `back`, its RolesAssignment and the
  ConfigMap `settings` are gone, while a ConfigMap the user applied over the client stays.
- **Mutants (overlay, each killed):**
  1. `setMeta` stops stamping the tag → FAIL, `the boot deletes the Function "back" no manifest names`.
  2. the tag filter in `pruneStale` becomes `if true` (prune every listed object) → FAIL,
     `the boot keeps an object the user applied`.
  3. `KindConfigMap` dropped from the pruned kinds → FAIL,
     `the boot deletes the ConfigMap "settings" no manifest names`.
- **Cause, not symptom**: the issue names two causes — the boot only applies, and the watcher's
  `applied` set starts from this boot's resources. Both are removed: the boot now deletes stale
  owned objects, and `applied = slices.Concat(resObjs, stale)` hands the undeletable ones to the reload
  prune so a later reload retries them.
- **ADR-0073 deletion protection preserved**: deletion goes through `pruneRemoved`, which keeps an
  object whose Delete fails (a KVStore/Bucket holding data), logs a `slog` warning and returns it.
- **Delete order**: kinds are listed in apply order and `pruneRemoved` walks `prev` last-first, so a
  stale Workflow is deleted before its Functions and a Function before what it binds.
- **User objects are safe**: only tagged objects are candidates; the test's untagged user ConfigMap
  survives, and mutant 2 proves the filter is load-bearing.
- **Scope**: every hunk serves the issue; no test was weakened or deleted. The ephemeral (memory
  metastore) path is unchanged apart from the harmless tag.
- **Reuse**: the boot prune reuses `pruneRemoved` (the PR #480 reload prune) rather than a second
  delete loop; `setMeta` is the single stamping point for synthesized resources. `sdk.Client.List` has
  no tag selector (`pkg/sdk/sdk.go:154`), so the client-side tag filter reinvents nothing.
- **Conventions (ADR-0002, CLAUDE.md)**: `fault` wrapping with `fault.KindOf`, ctx-first, `slog` only,
  `v1.Tags` (a `map[string]string`, allowed by ADR-0002), no `any` in signatures, no inline imports,
  doc comments state the why and cite ADR-0125.
- **ADRs**: no ADR file touched; consistent with ADR-0125 (the manifests are the session's desired
  state) and ADR-0073.
- **Checks (touched package only)**: `go test -race ./cmd/funcdctl/` ok (7.2 s);
  `go test -tags dev -race ./cmd/funcdctl/` ok (33.7 s); `go vet` with and without `-tags dev` clean;
  `golangci-lint run` and `golangci-lint run --build-tags dev` on `./cmd/funcdctl/` → `0 issues.`;
  `gofmt -l` clean. Repo-wide, Linux lint and e2e are left to the group gate.
- **Commit shape**: `fix(funcdctl):` subject, body names the regression test, `Fixes #502`, the
  attribution trailer, one issue in one commit.

### Definition of Done

11 / 11 items hold (fix checklist). Item 8 is verified for the touched package on the host; the
repo-wide set, Linux lint and e2e run at the group gate. Item 11 is verified on the commit; the PR is
not opened yet.

### Model scorecard

To record: claude-opus-5-5 on issue #502 (fix) → pass, 0/0/2, 2 model-attributed, DoD 11/11.
