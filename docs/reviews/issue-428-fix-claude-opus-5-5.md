## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #428 fix, model: claude-opus-5-5)

Change: `fix/i428`, commit f987e4b `fix(funcdctl): reload an edited workflow.yaml in funcdctl dev`
(`cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_phase3_test.go`).

The issue: `funcdctl dev workflow.yaml` resolved and applied the Workflow once at boot, and the watcher only
fingerprinted step functions, so an edit to the workflow file was never applied and nothing warned. The fix
threads a `devWorkflow` (path, boot object, file stamp) through `startDev` → `startDevWorkflow` → `bootDev` →
`watchHandlers`; on each poll `devWorkflow.reapply` checks the file stamp, re-runs `detectWorkflow` and
`resolveWorkflowPlan` (the same path as boot), warns `restart funcdctl dev …` for a step whose function is not
running, and re-applies the Workflow through `applyDesired`, clearing the stamp on a Conflict so the next poll retries.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **`devWorkflow.obj` is boot-only state that goes stale** · attribution: model · `cmd/funcdctl/dev.go`
  `devWorkflow` / `reapply`. `obj` is read only by `bootDev` and `startDevWorkflow` (the name in the banner);
  after the first reload it no longer matches what is applied, yet the type comment presents it as "the object
  bootDev applies" alongside live reload state. Harmless today; passing the boot Workflow separately (or updating
  `obj` on a successful reapply) would keep the struct honest.
- **`fileStamp` re-implements the size+mtime stamp of `devHandler.fingerprint`** · attribution: model ·
  `cmd/funcdctl/dev.go` `fileStamp` vs the `add` closure in `fingerprint`. Two lines of overlap, so trivial; a
  shared stamp helper used by both would keep the change-detection rule in one place.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit f987e4b`, test file kept at the fix,
  `go test -tags dev -run TestIssue428_ ./cmd/funcdctl/` → `--- FAIL: TestIssue428_DevReloadsEditedWorkflow
  (20.09s)`, `Condition never satisfied`, "the running dev session applies the edited Workflow". Worktree reset to
  f987e4b, clean.
- **Passes with the fix under -race**: `go test -tags dev -race -run 'TestIssue428_|TestScenarioDev'` → `ok`
  (7.2 s); the whole touched package `go test -tags dev -race ./cmd/funcdctl/` → `ok` (12.0 s), and untagged
  `go test -race ./cmd/funcdctl/` → `ok`. No data race.
- **User-visible behavior**: the regression test boots a real dev session (`startDevPath`, embedded platform,
  Node step function), edits `workflow.yaml` on disk to add steps `b` (existing function) and `c` (a manifest that
  is not running), and reads the applied Workflow back through the client: 3 steps, rewritten to `function.ref`
  `stepa`/`stepc`, plus the restart warning naming `function=stepc` in the slog output. That is the issue's own
  scenario end to end, and both outcomes the issue's Expected behavior allows (re-apply, and warn) are asserted.
- **Mutants (overlay, `-run TestIssue428_`), all killed**:
  1. warning loop disabled (`if false && !slices.ContainsFunc…`) → FAIL "warns that a restart is needed";
  2. reapply applies the stale boot object `w.obj` instead of the re-read one → FAIL "applies the edited Workflow";
  3. `resolveWorkflowPlan` skipped in reapply (no image→ref rewrite) → FAIL "applies the edited Workflow".
- **Cause, not symptom**: the watcher now covers the workflow file the issue names (dev.go resolve-once,
  apply-once, handlers-only watcher); reload reuses `detectWorkflow`, `resolveWorkflowPlan` and `applyDesired`
  rather than a parallel decode/apply path, and the boot stamp is taken before the file is read, so an edit racing
  the boot still reloads. A failed parse is reported once and retried on the next edit, matching `reloadChanged`.
- **Scope**: every hunk serves the issue; no test weakened or removed; no other file touched.
- **Reuse**: no new dependency or harness; the test reuses `devProject`, `startDevPath`, `requireNode` and
  `permissiveContract`. The test-local `lockedBuffer` has no reusable counterpart in the package (the
  `syncBuffer` in `internal/gateway` is unexported test code of another package).
- **Conventions**: `api/fault` errors (`fault.Invalidf`, `fault.Wrapf` keeping the kind), slog only, ctx-first,
  no `any`, top-level imports, comments state the why (ADR-0125 "watch files, re-apply on change"). `go vet`
  (with and without `-tags dev`) clean; `golangci-lint` (with and without `--build-tags dev`) `0 issues`.
- **ADRs**: conforms to ADR-0125 (boot sequence: watch files, re-apply on change); no ADR file edited.
- **Shape**: `fix(funcdctl):` subject, `Fixes #428`, attribution trailer, one issue in one commit.

### Definition of Done
11 / 11 items hold (fix checklist). Item 8 covers the touched package on the host (build, vet, lint, `-race`
tests, with and without the `dev` tag); Linux lint, the repo-wide set and e2e are left to the group gate by design.

### Model scorecard
Ledger fields (not recorded here): claude-opus-5-5 on issue #428 (fix) → pass, 0/0/2, 2 model-attributed,
DoD 11/11.

### Recommendation
Ship with the group. The two minors are optional polish for a later touch of `dev.go`; neither blocks.
