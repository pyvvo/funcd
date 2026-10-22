## Verdict: pass — 0 blockers, 0 majors open, 1 minor  (issue #1170 fix, model: claude-opus-5-5)

The branch is `fix/stress-flakes-1` (main f7e40c89). The work is in two commits:
- 130977e4 removes the 500 ms wall-clock bound from `TestConditionCheckDecodesEachDocumentOnce`
  (`internal/workflow/engine_test.go`) and keeps its 16 MiB allocation bound.
- d20c84cd, round 2, moves the schema's padding into a field that the check decodes. It also removes the same bound
  from `TestDefaultIsBoundOncePerPath` (`internal/expr/expr_test.go`).

## Round 2 (d20c84cd): pass

### Minor 1 — the sibling still runs under the evaluator's 100 ms deadline  ·  attribution: model
- **What the test still depends on.** `TestDefaultIsBoundOncePerPath` calls `EvalBool` with the default deadline,
  `evalTimeout` (100 ms, `internal/expr/eval.go:181`). The test's `Fatalf` on an error therefore still depends on the
  wall clock.
- **The removed bound could not fail first.** When an evaluation runs past 100 ms, `EvalBool` returns "evaluation
  exceeded its time limit". So the 500 ms check could not fail before the deadline did.
- **The margin.** Under `-race` on the review host, the real code takes 22–32 ms, a margin of 3 to 4.5. The failing
  workflow test had a margin of about 10 for each reference. The sibling passed all 5 stress runs, so no failure is
  proven, and the item is Minor.
- **The fix.** Set `x.timeout = time.Minute` after `mustCheck`, as `TestStringSearchIsLinear` does in the same file.
  Then only the allocation bound decides. `TestBudgetTimeCoversDocumentBinding` covers the deadline itself.
- **Round 1's part in it.** The round-1 fix named only the 500 ms check. The fixer then reported the deadline path for
  judgment.

### ✅ Verified correct (keep it)
- **Round 1's Blocker is fixed.** The schema's padding is a property's `default`, which `schemaNode` decodes.
  Overlay mutants on the committed test:
  - with only the schema cache off, the test fails at 203 MiB (`input.m`);
  - with only the document cache off, it fails at 1,199 MiB (both references);
  - with a 600 ms delay injected into `evalWhen`, it passes.
- **The sibling's guard holds.** A mutant replaces `!ctx.defaulted[ref]` with `true` in `internal/expr/check.go`.
  - The stock test fails it at line 641, through the 100 ms deadline.
  - With the deadline raised to a minute, the allocation bound fails it alone, at 201 MiB.

  So the allocation bound covers the property whichever check fires first.
- **The comments are now true.** Both say what the allocations measure and why the time bound went.
- **Checks.**
  - `go test -race -count=1` passes for `./internal/workflow/` (19.9 s) and `./internal/expr/` (2.1 s).
  - Both tests pass 3 runs out of 3 under `-race`, in 0.10 s and 0.03 s.
  - `go vet` passes on darwin and Linux.
  - `golangci-lint` reports 0 issues on darwin and Linux.
- **Shape.** d20c84cd is test-only and carries the trailer. `Fixes #1170` stays on 130977e4.

### Definition of Done
11 / 11. Item 1, a committed `TestIssue1170_` test, does not apply: the proof is an injection. Items 4, 6 and 12, the
misses of round 1, now hold.

### Model scorecard
Recorded: claude-opus-5-5 on issue #1170 (fix, round 2) → pass, 0/0/1, 1 model-attributed, DoD 11/11.
See docs/reviews/model-scorecard.md.

### Recommendation
Pass. Minor 1 is a one-line change and can go into this PR.

## Round 1 (130977e4): changes requested — 1 blocker, 1 major, both fixed in round 2

### 🔴 Blocker 1 — the schema half of the property lost its only guard  ·  attribution: model
- **The claim.** The test's comment says that each document **and schema** is decoded once per check.
- **Why this was false for the schema.** The schema's 250 KiB padding was a `description`. `schemaNode`
  (`internal/expr/schema.go:45-53`) does not decode that field.
- **The evidence.** A mutant disabled only the schema cache (`s.decoded`), by overlay.
  - The new test passed, also under `-race` (5.15 s).
  - Main's test failed it under `-race`: `input.m === 2 × 800 … allocated 3 MiB in 5.140082208s`.
  - The fixer's proof disabled both caches at once, so the document cache hid the gap.

### 🟡 Major 1 — an identical sibling kept the same 500 ms bound  ·  attribution: model
- `TestDefaultIsBoundOncePerPath` (`internal/expr/expr_test.go:631-646`) came from the same change (#643). It failed on
  `n > 16<<20 || took > 500*time.Millisecond`.
- The commit neither changed nor mentioned it.

### ✅ Verified correct in round 1
- **The injection proof.** With 600 ms injected into `evalWhen`:
  - main's test fails as CI did: `allocated 2 MiB in 617.469958ms`;
  - the new test passes.
- **Checks.** The `-race` package run, vet and lint passed on darwin and Linux.
- **Shape.** The subject is `test(workflow):`, with `Fixes #1170` and the trailer.

Round 1's ledger row: changes-requested, 1/1/0, 2 model-attributed, DoD 8/11.
