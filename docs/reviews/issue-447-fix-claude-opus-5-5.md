# Fix review — issue #447 (a run failed at start by the payload cap or record size reports StepFailed)

- **Change**: branch `fix/i447`, commit e1070c7 `fix(workflow): name the cause of a run that fails at start on the payload cap or record size`
- **Producing model**: claude-opus-5-5
- **Governing ADRs**: ADR-0094 (engine core, run failure reasons), ADR-0099 (sub-workflows), ADR-0002
- **Verdict**: **pass**

## Summary

`failureReason` fell back to `StepFailed` for any cause without one of its tokens. The two run-start
failures carried no token: the payload cap (#181) and a first run record over the run store's value limit
(#306). The fix follows the existing token pattern (`(InputSchemaMismatch)` in `engine.go`,
`(SubworkflowDepthExceeded)` in `subworkflow.go`). The engine messages now carry `(PayloadLimitExceeded)`
and `(RunRecordTooLarge)`, and `failureReason` maps both tokens. A record that is never stored fails
through `failUnrecorded` with reason `RunRecordTooLarge`. This removes the cause the issue names. The issue
left the reason names open, and neither name collides with a shipped name in the repository.

## Verification (run, not eyeballed)

| Check | Result |
|---|---|
| Revert the fix (`git revert --no-commit e1070c7`, test file kept) | `TestIssue447_RunStartFailureReasonNamesCause` FAILS in all 3 subtests with `Reason:StepFailed` and a message that names the payload limit or the record size. This is the issue's reason. |
| With the fix, `go test -race -run TestIssue447_ ./internal/workflow/` | PASS (payload_cap, record_input, record_spec) |
| Mutant 1: drop `"PayloadLimitExceeded"` from `failureReason` | killed (payload_cap) |
| Mutant 2: the `failUnrecorded` reason back to `failureReason(err.Error())` | killed (record_spec) |
| Mutant 3: drop `(RunRecordTooLarge)` from the engine's record-refusal message | killed (record_input) |
| `go test -race ./internal/workflow/...` | ok (workflow, runstate/badger) |
| `go vet`, `golangci-lint` on `internal/workflow/...` | clean (0 issues) |
| Worktree after review | at e1070c7, clean |

User-visible path: the test drives the real `RunReconciler.Reconcile` with a real engine and an in-memory
Badger run store. It covers the two record paths: the record kept without its input (mirror) and the
record that is never stored (`failUnrecorded`). It asserts phase Failed, `Ready=False` with the named
reason, and that no step was dispatched.

## Blockers

None.

## Majors

None.

## Minors

None.

## Observations (not scored)

- `reasonToken` matches tokens by substring, so a parent run whose sub-workflow child failed with one of
  the new tokens inherits that reason through the child's message. This is the same behavior that the
  existing tokens have (`InputSchemaMismatch`, `SubworkflowDepthExceeded`). The fix did not introduce
  this behavior and it is outside the issue's scope.

## ✅ Verified correct

- The fix removes the root cause (no token, so the fallback applied). It does not mask it.
- Scope: 3 files. Every hunk serves the issue. No test was weakened or deleted.
- Reuse: the fix extends the existing `reasonToken` and `failureReason` mechanism and the existing token
  convention in messages. The test reuses `newStore`, `seedWorkflow`, `seedRun`, `newFake` and
  `wbadger.New` in-memory, with the same idiom as the 21 sibling tests.
- Conventions: `api/fault` errors keep their kinds (`Invalid`, `PayloadTooLarge`). The comment states the
  why and is not bloated. The test has no YAML and only top-level imports.
- ADRs: no Accepted or Implemented ADR is edited or contradicted. ADR-0094 does not define a closed set of
  run failure reasons, and no living doc lists the reasons in a way that needs an update.
- Commit shape: a `fix(workflow):` subject, `Fixes #447`, the attribution trailer, and one issue per commit.

## Recommendation

Pass. Hand back to `/fix` Step 8. Checklist: 11 of 11 hold. Item 8 was checked only for the touched
packages. The group gate runs the repo-wide, Linux-lint and e2e checks.
