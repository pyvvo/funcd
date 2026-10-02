## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #193 fix, model: claude-opus-5-5)

Change: branch `fix/i193`, commit 5c3efd2 `fix(funcdctl): reject an unknown -o on get, workflow runs/describe and dlq list`
(5 files, +53/-8, all in `cmd/funcdctl`).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **Two regression cases do not reproduce the "table with exit 0" symptom on their own** · attribution: model ·
  `TestIssue193_UnknownOutputIsRejected` runs `workflow describe run1` against a run that does not exist, and
  `eventing dlq list` against a test server that answers 404. Without the guard, both already return an error
  (`store.Get: WorkflowRun "run1" not found`, `sdk: 404 page not found`), so the test catches the missing guard
  only through the `fault.Invalid` and "unknown output" assertions, not through the reported behavior (the
  table printed with exit 0). The test still discriminates: mutants M2 and M3 below fail it. The cases would be
  more faithful if they ran against an existing run and a served DLQ endpoint. This is non-blocking.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 5c3efd2` with the test file kept →
  `go test ./cmd/funcdctl -run TestIssue193 -count=1`: `FAIL` — "An error is expected but got nil" on
  `[get function fn1 -n team-a -o yaml]`. This is the issue's symptom: `-o yaml` succeeds and prints the table.
- **Passes with the fix under -race.** After `git reset --hard 5c3efd2`: `go test -race ./cmd/funcdctl -run TestIssue193 -count=1` → `ok`.
  The full package under `-race` → `ok`. The worktree was left clean at 5c3efd2.
- **Mutants (overlay, `-run 'TestIssue193|Logs'`): all 3 killed.**
  - M1 `checkOutput` accepts everything (`if true || …`) → FAIL on the `get … -o yaml` case.
  - M2 the `workflow describe` guard also allows the given value → FAIL on `workflow describe … -o garbage`.
  - M3 the `eventing dlq list` guard also allows the given value → FAIL on `eventing dlq list … -o yaml`.
- **Root cause, not symptom.** The issue names `output == "json"` with no validation of other values. Every
  `-o` flag in the repo has a guard now. A grep of `"output", "o"` finds six flags, all in `cmd/funcdctl`:
  `get`, `logs`, `workflow logs`, `workflow runs`, `workflow describe` and `eventing dlq list`. The guard runs
  before `sdkClient()`, so an invalid `-o` makes no API call and prints nothing (the test asserts an empty
  output).
- **Reuse, no duplication.** The new `checkOutput` helper replaces the two hand-written `switch` blocks in
  `logs.go` and `workflow.go`, and keeps their exact message (`unknown output %q (want: wide or json)`). It uses
  the standard library (`slices.Contains`, `strings.Join`) and `fault.Invalidf`. No existing helper did this
  job: a search of `cmd/funcdctl`, `internal/platform` and `api/fault` found none.
- **Scope.** Every hunk serves the issue. The `logs` and `workflow logs` refactor serves it directly: one rule
  for all `-o` verbs. No test was weakened or deleted.
- **Conventions.** The error is a `fault.Invalid`, the same as the logs verbs. The import is at the top level.
  The doc comment on the helper is one line long and gives the reason. No `any` appears in a signature.
- **ADRs.** The change is consistent with ADR-0024 (`get … [-o json]`, a table by default) and ADR-0100
  (`-o json` on `workflow describe`). No ADR file was edited.
- **Checks (touched package).** `go vet ./cmd/funcdctl` → ok. `golangci-lint run ./cmd/funcdctl/...` →
  `0 issues.` `gofmt -l cmd/funcdctl` → clean. `go test -race ./cmd/funcdctl` → ok.
- **Shape.** Subject `fix(funcdctl): …`, `Fixes #193`, the Co-Authored-By trailer, one commit for one issue.

Out of scope here: the e2e suite, repo-wide tests and the Linux lint. The group gate runs them.

### Definition of Done

10 / 10 applicable items hold (1–7, 9–11). Item 8 was checked for the touched package only (host build, vet,
lint and race tests). The Linux lint and e2e are deferred to the group gate and are not counted.

### Model scorecard

Not recorded here: the ledger fields were returned to the orchestrator. Values: claude-opus-5-5 on issue #193
(fix) → pass, 0/0/1, 1 model-attributed, DoD 10/10.

### Recommendation

Ready to ship. If the fixer reworks the test, it can optionally make the `workflow describe` and `dlq list`
cases run against an existing run and a served DLQ endpoint. This is not required for sign-off.
