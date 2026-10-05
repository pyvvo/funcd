# ADR-0189 implementation review (claude-opus-5-5, loop 1)

- **ADR**: [ADR-0189](../adr/0189-workflow-run-pins-child-tree.md): a workflow run pins its child tree at start
- **Work**: branch `feat/adr-0189-run-pins-child-tree`, commit 596cc967 on origin/main (one commit, 8 files, +758/-22, all in `internal/workflow/`)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**. There are no Blockers or Majors, and three Minors, all attributed to the model.
- **Preflight brief**: the brief records no drift that is specific to ADR-0189. The change stays inside the files the ADR names. `drive_test.go` is the one addition, and its change is a test adaptation (see Verified correct).

## Verification run (captured)

| Check | Command (via `scripts/agent/d`) | Result |
|---|---|---|
| Build darwin | `go build ./...` | exit 0 |
| Build linux | `GOOS=linux go build ./...` | exit 0 |
| Vet darwin and linux | `go vet ./internal/workflow/...` | exit 0, both |
| Tests | `go test -race -count=1 ./internal/workflow/...` | `ok internal/workflow 23.7s`, `ok runstate/badger`, exit 0 |
| Lint darwin | `go tool golangci-lint run ./internal/workflow/...` | 0 issues |
| Lint linux | host-built golangci-lint binary with `GOOS=linux` | 0 issues (`GOOS=linux go tool golangci-lint` builds a linux binary that cannot run on the host, which is an `env` artifact) |
| Prove-first | origin/main versions of `engine.go`, `reconcile_run.go`, `subworkflow.go`, `runstate.go` and the old test files through `-overlay`, plus `TestIssue766_…` and its helpers | **FAIL as the ADR predicts**: `phase="Succeeded" … dispatched=[x]`, child `p-1.e` revision `oci:a@sha256:oci:a` under a spec that names `oci:b`. HEAD: PASS |
| Tree | `git diff --name-only origin/main...HEAD -- docs/` | empty. The ADR status was not touched, as the brief asks |

### Overlay mutants (`go test -overlay`, scenario and unit tests of ADR-0189)

| # | Mutation | Killed by |
|---|---|---|
| m1 | `start` skips `pinTree` (pins nil, so runs use the live fallback) | TestIssue766, EditedChildWaitsAtParentStart, WaitingRunNamesChildCause, GrandchildGatesTheTree, ChildEditAfterStartIgnored, ChildPinnedOnce, the 3 replay scenarios, TestReplayTreeGate |
| m2 | `runChild` ignores `ChildPins` and always calls `resolveChild` | ChildEditAfterStartIgnored, TestPinTree, ReplayRunsTheSourcePin, ResumeRunsThePin (nil-resolver panic) |
| m3 | `childDrift` always returns nil | ReplayChildDriftGated |
| m4 | `restamp` re-stamps no fresh child | ReplayChildDriftGated (allow-drift revision), TestReplayRestampsNeverRunChild |
| m5 | `pinTree` skips `when:`-guarded `workflow:` steps | **survived** (see n1) |

Four of five mutants were killed. The survivor is a test gap. The code is correct on that line.

## Contracts and Decisions

- `runstate.ChildPin` and `Record.ChildPins` match the Contracts block field for field, JSON tags included (`internal/workflow/runstate/runstate.go`).
- `StartOptions.ChildPins` is copied onto the record by `execute` (`engine.go`). `replay` gains `childImages` after `current`. The public `Replay` passes nil and keeps its signature.
- The signatures of `freshChildren`, `childOptions`, `subtree`, `treeWait`, `pinTree` and `replayTree` are as specified. `replayTree` uses `freshRefs`, which `freshChildren` wraps, so the gate and the re-stamp cover the same set of children.
- D1 and D3: the walk is in spec order and depth first. It starts with the root in the seen set, so the root is never pinned and a cycle ends the walk. Each child is pinned once (TestPinTree covers a diamond and a cycle to the root). A run back to the root fails with `"back" has no pin` (`fault.Invalid`).
- D2: absent gives `WorkflowNotFound`. Otherwise the reason is `WorkflowNotReady`, and the message names the child, the step and the Workflow that reference it, and the cause (`notReadyCause`). The top-level messages are unchanged byte for byte after the `notReadyCause` refactor.
- D4: with non-nil pins, `runChild` neither reads the store nor needs a resolver. TestScenarioResumeRunsThePin runs with no `Children` dependency. A child record gets `subtree(...)`. An empty non-nil subtree keeps descendants on the pin path, so they fail on a missing pin and do not fall back to a live read.
- D5: Resume rebuilds from the record. A replay copies the source pins and never calls `pinTree`. `replayTree` gates only after the top-level gate and captures images after every fresh child is `ready`. `childDrift` returns `DigestDrift` and names the child, the step and the calling step, unless `--allow-drift` is set. A source that is absent, not terminal, has an unknown `from`, a `from` that is the onFailure handler, or nil pins skips the gate.
- Temporary workaround: a nil-`ChildPins` record keeps the `resolveChild` path. The `e.children == nil` guard now applies only to that path.

## Findings

### Minor

- **n1 [model]: no test isolates "a skipped branch's child is still gated".** The code holds this property, because `pinTree` walks every step whatever its `when:`. But mutant m5, in which `pinTree` skips `when:` steps, passes every test. In `TestScenarioChildPinnedOnce` the skipped `e3` calls `enrich`, which `e1` already pins. The test matches the scenario text, but no test guards the Review-checklist clause. A unit case would close the gap: a child reached only through a false `when:` step makes the run wait.
- **n2 [model]: `replayTree` gates on a source that the replay will reject because the Workflow does not match.** `replayTree` (`reconcile_run.go`) skips an absent or non-terminal source but does not check `src.Workflow` against `run.Spec.Workflow`. `replay` rejects that mismatch with `SeedInvalid` (`engine.go`). A replay that names the wrong Workflow therefore waits on the source's children instead of failing at once. If one of those children was deleted, it waits with `WorkflowNotFound` indefinitely. Decision 5 says that a source the replay rejects skips `replayTree`. The impact is low because the path is an operator error.
- **n3 [model]: a stale comment in the replay drift gate.** `engine.go:398` still says "Ref/builtin/workflow steps are not gated (documented workarounds)". The `childDrift` call two lines below now gates `workflow:` steps of a source that has pins.

## Verified correct (keep)

- The prove-first test reproduces #766 exactly on origin/main code: the run succeeds, dispatches `x`, and records the child under the old image.
- All nine scenarios have a `TestScenario<Name>` test, and each one passes under `-race`. The unit tests cover `pinTree` (diamond, absent, stale, flat, cycle), `replayTree` (rejected source, unknown `from`, unpinned source, absent grandchild), `subtree`, `childOptions` (missing pin is `Invalid`), the re-stamp of a child that never ran, and the nil-pins fallback (TestIssue444).
- No test was weakened. `TestIssue444` now seeds a record without pins, which is the only path where a run can still meet an absent child at step time. The comment in the test says so. `collisionStore` and two `drive_test.go` tests seed their child Workflow, which a fresh run now needs in order to pin. Their assertions are unchanged.
- No new condition reason was added. `WorkflowNotFound` and `WorkflowNotReady` already exist on main, and `DigestDrift` is reused.
- The re-stamp is scoped correctly: `restamp` copies the map before it re-stamps, so the source record is never mutated. The drift message names the child, its step and the calling step.
- The change is small and in the right place. A shared `readyChild` serves both gates, and `pinnedRefs` serves the gate, `subtree` and `childDrift`, so there are no parallel walkers.

## Review checklist and Definition of Done (7/7)

- [x] `TestIssue766_…` fails on unfixed code for the step-1 reason and passes with the fix.
- [x] `runChild` neither reads the store nor needs a resolver when pins are non-nil (m2 is killed).
- [x] There is one pin per child. A skipped branch's child is gated by the code, although no test guards it (n1).
- [x] The wait message names the child, the first step and Workflow that reference it, and the cause.
- [x] A replay copies the pins without `pinTree`, gates before it captures, re-stamps fresh subtrees, and fails on drift with `DigestDrift` unless `--allow-drift` is set (m3 and m4 are killed).
- [x] Every scenario has a named passing test. No test was weakened, and no new reason was added.
- [x] Done: `-race` tests, vet and golangci-lint are green on the touched packages, also for Linux.

## Recommendation

The verdict is **pass**. n1 to n3 are small follow-ups for the builder, and none of them blocks the change. The wave's docs PR stamps the ADR and the feat row.

```json
{
  "date": "2026-10-05",
  "adr": "0189",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 3,
  "model_attributed": 3,
  "dod_passed": 7,
  "dod_total": 7,
  "report": "docs/reviews/adr-0189-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (596cc967): TestIssue766 fails on origin/main code (run Succeeded, child stamped oci:a under spec oci:b), passes on HEAD; all Contracts + 6 Decisions hold; build/vet/lint clean also Linux; internal/workflow -race ok; 9/9 scenario tests pass; mutants 4/5 killed (pinTree gate, runChild pin path, childDrift, restamp). n1 [model] no test isolates the skipped-branch child gate (pinTree skipping when: steps survived); n2 [model] replayTree gates on a source the replay rejects for a Workflow mismatch (waits, WorkflowNotFound forever if a source child is deleted); n3 [model] stale engine.go:398 comment says workflow steps are not drift-gated."
}
```
