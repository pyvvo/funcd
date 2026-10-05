## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0154 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0154-child-run-record-names`, one commit `27ebc2ee` on `origin/main` `cdd21033`
(merge base = current main). Diff: 7 files, +264/-17; the only production change is
`internal/workflow/subworkflow.go` (one line in `runChild` plus the `childRunName` helper).

### 🟡 Major / Minor

- **Minor 1 — Implementation plan step 6 (release note) is not in the commit** · attribution: `model`.
  The commit body ends with `Fixes #117` but has no `Release note:` line. The repo's precedent puts upgrade
  guidance in the commit body as `Release note: …` (an earlier fix commit on main does exactly that), and
  release-please builds `CHANGELOG.md` from commit messages. Without it, the Decision 4 guidance ("after an
  upgrade, do not create a WorkflowRun named `<parent>-<step>` until the old run records have expired, or clear
  the run state") is lost. Fix: add the line to the commit body, or to the PR body that becomes the squash
  message.
- **Minor 2 — `TestScenarioChildExpiryKeepsUserRun` never shows that `p.sub` existed before the sweep** ·
  attribution: `model`. `internal/workflow/reconcile_run_test.go` (the new test) asserts
  `rstate.Get(…, "p.sub")` is NotFound after `SweepExpired`, but does not assert the record was there first, so
  the "record p.sub is deleted" clause can pass vacuously. Evidence: overlay mutant M3 (separator `_`) leaves this
  test green (it fails 10 other tests, so the format is still pinned elsewhere). Fix: `getRecord(t, rstate,
  "p.sub")` before the sweep.

### ✅ Verified correct (keep it)

- **Build / vet / lint, captured exit codes**: `go build ./...` exit 0; `GOOS=linux go build ./...` exit 0;
  `go vet ./internal/workflow/... ./api/types/v1alpha1/` exit 0 (darwin and Linux); golangci-lint on the same
  packages: 0 issues, exit 0 (darwin, and Linux with the host-built linter as `scripts/agent/gate.sh` runs it).
- **Tests**: the 8 scenario tests pass under `-race` (each named in the commit body); the touched packages
  `./internal/workflow/... ./api/types/v1alpha1/` pass under `-race -count=1` (exit 0). The seven
  `internal/workflow` scenario tests plus `TestCrashRecoveryResumesRun` are stable at `-race -count=10`.
- **Overlay mutants (`go test -overlay`), all killed**:
  - M1 `childRunName` → `parent + "-" + step` (the pre-ADR form): 12/12 selected tests fail, including all seven
    engine and reconciler scenarios. The reconciler scenarios fail on behavior, not only on the literal lookup:
    `p-sub` reports childwf's steps (A), record `p-sub` is overwritten (B), the sweep deletes WorkflowRun `p-sub`.
  - M2 drop the `spec.replay.run` DNS-label check in `WorkflowRun.Validate`: `TestScenarioChildNameNeverAdmitted`
    fails.
  - M3 separator `_`: 10/12 fail. The tests pin the literal dotted format, as plan step 3 asks.
- **Contracts**: `childRunName` and its doc comment match the ADR's Contracts block verbatim
  (`internal/workflow/subworkflow.go:66-72`); `runChild` calls `childRunName(parent.Name, n.name)`
  (`subworkflow.go:50`). It is the only builder: no other `Name + "-"` run-name construction in
  `internal/workflow` production code, and no `"-"` left in `subworkflow.go`.
- **Review checklist**:
  1. `childRunName` is the sole builder; no `"-" +` in `subworkflow.go` — holds (grep).
  2. No new admission, engine or reconciler check; `foreignRecord`, `started`, `deleteRun` unchanged — holds (the
     production diff is `subworkflow.go` only).
  3. No code validates, parses or truncates `runstate.Record.Name` as an `ObjectName` — holds (grep over
     `internal/workflow`, `internal/controlplane`, `cmd/funcdctl`: no hit).
  4. A WorkflowRun name and `spec.replay.run` still reject `.` — holds; the admission test also keeps a positive
     control (`p-sub` with replay source `p-sub` is admitted), and M2 proves it guards the replay rule.
  5. One named, passing test per scenario with literal dotted names; the three old tests use `run-p.sub` —
     holds (`subworkflow_test.go` lines 102-103, 312, 383); the vacuous precondition is Minor 2.
  6. ADR-0099 and ADR-0107 carry only the back-link; F70 row updated — holds on `origin/main` (back-links
     present; F70 reads "child record names: accepted").
- **Scenario fidelity**: each test sits in the file the plan names, under a `// scenario: <name>` comment, on
  the harness the plan names (`seedWorkflow`/`seedRun`/`reconcileRun` + `fakeChildren`; `childEngine`; `crashAt`
  extended with a `child` field to keep `p.sub` at the child's first dispatch, then `Resume` on a fresh store).
  The restart test checks the re-dispatch order `p.sub/c1, p.sub/c2` and that only records `p` and `p.sub`
  exist; the replay test checks `src.sub` is deep-equal before and after.
- **Scope**: the two ADR-0146 drive tests (`drive_test.go`: `run-p.call`, `run-m.call`) were updated because
  they hard-code a child name and landed after the ADR was written; the commit body says so. That is the
  necessary follow-through, not scope creep. No dependency, no new check, no keyspace.
- **ADR substance unchanged**: the branch touches no file under `docs/`.
- **Conventions**: test-only `crashAt` extension is minimal; ignoring `badger.New`/`New` errors in tests follows
  the existing harness (`engine_test.go:493`, `:565`); imports at top level.

### Definition of Done

7 / 8 items hold (ADR Review checklist 6 + ADR Definition of done + Implementation plan step 6). Miss: the release
note (Minor 1, `model`). Repo-wide `just ci-full` and the clean-tree check are left to `scripts/agent/gate.sh`
once per PR, as the plan says (not run here by instruction). Status tracking: the ADR still reads `Accepted` and
the F70 row "accepted"; the wave's docs PR moves both, per the orchestration, so this is not a finding against
the model.

### Model scorecard

To record: claude-opus-5-5 on ADR-0154 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 7/8 (ledger row
below; the wave's docs PR writes it).

### Recommendation

Pass. Before merge, add the `Release note:` line (commit or PR body) and, optionally, the pre-sweep `getRecord`
on `p.sub`; neither blocks. The PR must carry `Fixes #117` (the commit already does).

```json
{
  "date": "2026-10-05",
  "adr": "0154",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 2,
  "dod_passed": 7,
  "dod_total": 8,
  "report": "docs/reviews/adr-0154-implementation-claude-opus-5-5.md",
  "notes": "Contracts verbatim (childRunName sole builder, one production line); build+vet+lint darwin and Linux green, touched pkgs -race ok, 8/8 scenarios pass (stable at -count=10); 3/3 overlay mutants killed (- separator, replay.run DNS check, _ separator); release note of plan step 6 missing from commit body (model); expiry test never asserts p.sub existed before the sweep (model)"
}
```
