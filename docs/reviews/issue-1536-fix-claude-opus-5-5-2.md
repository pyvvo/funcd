## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1536 fix, re-review, model: claude-opus-5-5)

This report re-reviews branch `fix/1536-part-takes-the-addons-first-revision` at 4944f48b, `test(addons): a first pin
through an App revision with no record does not follow the Addon by itself`. That commit sits on top of c66152a6,
which the first review (`docs/reviews/issue-1536-fix-claude-opus-5-5.md`) passed with three Minors. The round's diff
(`git diff c66152a6 4944f48b`) touches `docs/behavior/addons.md` and `internal/services/addon/revisions_test.go`.
`revisions.go` is unchanged.

### Round 1 findings, all closed
- **Minor: the fix's "no move by itself" half was not tested.** `TestIssue1536_PartTakesTheAddonsFirstRevision` now
  runs the first review's probe. After the part pins `rauthy-1`, the Addon's spec changes, its reconciler stamps
  `rauthy-2`, and the part passes once more. The test then requires `["web-auth-1", "rauthy-1"]` and an engine that
  keeps `rauthy-1`'s spec.
  - M1 (drop `&& o.Status.AddonRevision == ""` from `unpinned`) now fails it at `revisions_test.go:217`, with actual
    `["web-auth-1", "rauthy-2"]` and the message "the pin does not follow the Addon by itself".
  - M2 (`>` to `>=` in `moves`) fails at the same line, the same way.
- **Minor: the corner had no behavior row.** `docs/behavior/addons.md` has a new row after "Which revision an object
  runs": "A part whose App revision records no revision". It reads `accepted`, with the source ADR-0339 Decision 3 and
  `TestIssue1536_PartTakesTheAddonsFirstRevision`.
  - The registry's table allows `accepted` when no clause speaks of a behavior and a test pins it, with the test as
    its source (`.claude/skills/behavior-registry/SKILL.md`). No clause speaks of this case, and the test now pins
    both the first pin and that the pin stays.
  - The row is 289 bytes. A separate row was the right choice: the row "Which revision an object runs" is 390 bytes,
    and `scripts/behavior/main.go:35` caps a row at 400.
  - `scripts/agent/d just check-behavior` exits 0, and it checks that the cited test exists.
- **Minor: the `revRig` type comment described only `newRevRig`'s rig.** The type comment now describes the rig itself:
  the Addon `rauthy` over a memory store, with its reconciler and a fake runtime. `newRevRig` carries the revisions
  sentence.

### Routed (not this work's)
- **The design model starts with the Addon stamped.** This is unchanged from the first review, and it is still a
  question for the decider at the next change of ADR-0339's design. The fix still changes no modeled behavior.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** With origin/main's `revisions.go` as an overlay,
  it fails at `revisions_test.go:208` with actual `["web-auth-1", ""]`, "the Addon's first revision". With the fix it
  passes under `-race` (0.01 s), and `-race -count=20` is ok.
- **Mutant M3** (no fallback to the current revision) fails the test at line 208, as in the first review.
- **Scope.** The round adds 9 test lines, rewrites two comments and adds one row. No product code changed and no
  assertion was weakened. The other tests in the file are untouched.
- **Checks.** `go test -race -count=1 ./internal/services/addon/` is ok. `go vet` is ok on the host and with
  `GOOS=linux`. `go tool golangci-lint run ./internal/services/addon/...` reports 0 issues. `just check-behavior` exits
  0.
- **Everything the first review verified still holds.** That covers the cause, the user-visible path through
  `MapAddon`, the two checks behind the verdict `bug`, ADR-0339 conformance, siblings, reuse and conventions. The
  product code and its wiring are unchanged since c66152a6.
- **Shape.** The round's commit is a conventional `test(addons):` with the attribution trailer. The first commit keeps
  `Fixes #1536`. The queue squashes the PR, so the PR body must carry `Fixes #1536`.

### Definition of Done
13 / 13 items hold. Item 4 now holds: the revert, M1, M2 and M3 each fail the regression test.

### Model scorecard
Recorded: claude-opus-5-5 on #1536 (fix, re-review) → pass, 0/0/0, 0 model-attributed, DoD 13/13. See
docs/reviews/model-scorecard.md.

### Recommendation
Ready for the PR. Run the gate once on the branch, title the PR with the fix's subject, and put `Fixes #1536` in its
body.
