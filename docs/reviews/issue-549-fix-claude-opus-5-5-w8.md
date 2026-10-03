## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #549 fix, model: claude-opus-5-5)

Change: branch `fix/w8-i549`, commit 0c0b466 `refactor(function): return a revisionPass from convergeRevision`,
one file (`internal/function/function.go`, +77/-62). Issue kind: task; the target is its "Done when" section and the
person's decision (a refactor with no behavior change; one result struct, named options; the internal/function
tests pass unchanged).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **Two surviving mutants on the moved triage lines; no `planReplicas` table test** · attribution: model ·
  The issue's Details say the triage switch moves into a pure `planReplicas` so "it can then get a table test".
  The commit makes it pure but adds no test. Overlay mutants:
  - m1 `case opts.untried:` → `case false && opts.untried:` in `planReplicas`: `go test ./internal/function/` ok,
    `go test ./pkg/funcd/` ok — survives.
  - m2 `scaleDown: true` → `scaleDown: false` at the `convergeSolo` call site: both packages ok — survives.
  Both gaps predate the change (the existing tests are unchanged and the lines are moved verbatim), and the
  "Done when" does not require the table test, so this does not block. Fix: add a table test for `planReplicas`
  (missing / Created / running / untried terminal / Stopped before and after the period / serving crash /
  tried Failed not serving) and a scale-down case for `convergeSolo`.

### ✅ Verified correct (keep it)

- **Done when, item 1**: `convergeRevision` now returns `(revisionPass, error)` and takes `opts convergeOpts`; no
  positional bool remains. Audit config (`scripts/agent/audit.golangci.yml`) reports cognitive complexity 38 for
  `(*Reconciler).convergeRevision` — under the 46 the issue asks for (the commit message's 50 → 38 matches).
- **Done when, item 2**: no `_test.go` file changed (`git diff --name-only origin/main...HEAD -- '*_test.go'` is
  empty); `go test -race -count=1 ./internal/function/...` → `ok` (10.2 s).
- **No behavior change** (read hunk by hunk): the triage switch moves into `planReplicas` verbatim with
  `untried`/`serving` read from `opts` and `r.supervisionPeriod` passed as `period`; `time.Now()` is still taken at
  the same point. Every error return maps `0, time.Time{}, nil, err` → `revisionPass{}, err`. Call sites keep their
  argument values: `convergeSolo` (`serving, untried, true`), `switchSolo` staging (`true, false, false` →
  `{serving: true}`; the NotFound path still yields running from `runningReplicas` and a zero retryAt), and
  `switchSolo` current (`false, untried, true` → `{untried, scaleDown: true}`). The retryAt merge and verdict fields
  are unchanged in meaning.
- **Mutant m3** (`convergeOpts{serving: true}` → `convergeOpts{}` in `switchSolo`) is killed by
  `TestScenarioServingWorkerCrashDuringSwitchIsReplaced` and `TestServingRevisionKeepsItsReplicasDuringASwitch`.
- **Checks (touched package)**: `go build ./...` ok; `go vet ./internal/function/` ok; `golangci-lint run
  ./internal/function/...` → 0 issues; race tests green. Repo-wide and Linux checks are left to the group gate.
- **Scope**: one file; every hunk serves the issue. No defect found on the way.
- **Reuse**: `convergeOpts`, `revisionPass` and `planReplicas` are new names with no collision in the module; the
  triage logic is moved, not duplicated.
- **Conventions**: ctx-first unchanged, `api/fault` wrapping unchanged, no `any`, no new import, doc comments name
  ADR-0142 and the issues they carry over (#73, #358) without narration.
- **ADRs**: ADR-0142's per-replica table and ADR-0143's revisions behave as before; no ADR file edited.
- **Shape**: `refactor(function):` fits a task (no bug fixed), `Fixes #549`, attribution trailer, one commit.

### Definition of Done

7 / 8 applicable items hold (items 1–3 do not apply: a task with no `TestIssue549` test; its "Done when" was
verified directly). Miss: item 4 — two mutants on the moved lines survive (model, Minor, pre-existing gap).

### Model scorecard

Ledger fields (not recorded here): claude-opus-5-5 on issue #549 (fix) → pass, 0/0/1, 1 model-attributed, DoD 7/8.

### Recommendation

Ship. Optionally add the `planReplicas` table test the issue suggested, in a follow-up, to close the two
surviving mutants.
