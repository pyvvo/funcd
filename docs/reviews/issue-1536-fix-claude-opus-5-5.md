## Verdict: pass — 0 blockers, 0 majors, 3 minors  (issue #1536 fix, model: claude-opus-5-5)

This report reviews commit c66152a6 `fix(addons): an App's part takes its Addon's first revision when its App was
stamped before it`, one commit on origin/main f352341c (`git diff origin/main...HEAD`:
`internal/services/addon/revisions.go`, `internal/services/addon/revisions_test.go`). Issue: "An App's add-on part
never runs when the App is stamped before its Addon".

### Minor
- **The fix's "no move by itself" half is not tested** · attribution: model · evidence: two mutants on the lines the
  fix wrote survive. M1 drops `&& o.Status.AddonRevision == ""` from `unpinned`
  (`internal/services/addon/revisions.go:242`). M2 turns `>` into `>=` in `moves` (line 241). Under each,
  `go test ./internal/services/addon/` passes. Under M1, the add-on tests of `internal/app` and `pkg/funcd` pass too
  (`-run Addon`). A scratch probe, not committed, adds three steps to the regression test: after the part pins
  `rauthy-1`, the Addon's spec changes, its reconciler stamps `rauthy-2`, and the part passes once more. The probe
  passes with the fix. Under M1 and under M2 it fails with `["web-auth-1", "rauthy-2"]`: the part follows the Addon by
  itself, which ADR-0339 forbids (nothing moves by itself, Decisions 2 and 3; the model's `NoMoveByItself`). The commit
  body's "moves stay forward only" rests on that guard. Fix: add those steps to
  `TestIssue1536_PartTakesTheAddonsFirstRevision`, and assert that the part keeps `rauthy-1` and that its engine keeps
  `rauthy-1`'s spec.
- **The corner the fix decides has no behavior row** · attribution: model · evidence: the issue's Design check found
  no row for an App revision stamped before its Addon's first revision, and this review's search found none either.
  The diff touches no file under `docs/behavior/`. CLAUDE.md (*Is it a bug?*, check 2) says that a dive that finds no
  row ends by adding one. The row "Which revision an object runs" (`docs/behavior/addons.md:31`) says that a part pins
  its App revision's record once applied. It does not say that a part whose App revision records none pins the
  Addon's current revision once the Addon has one. Fix: name that case in the row, cite
  `TestIssue1536_PartTakesTheAddonsFirstRevision`, and run `just check-behavior`.
- **The `revRig` type comment now describes only `newRevRig`'s rig** · attribution: model · evidence:
  `internal/services/addon/revisions_test.go:23-25` says that a revRig has the revisions `rauthy-1` to `rauthy-3` and
  the AppRevisions `holder-1` and `holder-2`. The new `newUnstampedRig` (lines 50-65) returns a revRig with no
  revision and no AppRevision. Fix: move that sentence to `newRevRig`'s doc comment. This one is cosmetic.

### Routed (not this work's)
- **The design model starts with the Addon stamped** · home: elsewhere · destination: the decider, at the next change
  of ADR-0339's design. In `docs/design/addon/revisions.fizz`, `Init` sets `current = 1` (line 49), and every App
  revision records a revision of 1 or more. So `p_pin` is never 0 once `p_app` is set, and the model cannot reach the
  fix's new branch. The fix changes no modeled behavior, so it owes no spec update (ADR-0272 Decision 5). The oracle
  markers and their tests are unchanged and pass. The issue's corner lies outside the model's bounds. A model whose
  `Init` has no revision would check this path. Its `NoMoveByItself` part clause (line 197) would then need the
  `before.p_pin != 0` exemption that the standalone clause has (line 195), because a first pin is not a move. Nothing
  stays here.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** With origin/main's `revisions.go` as an
  overlay (`go test -race -overlay`), `TestIssue1536_PartTakesTheAddonsFirstRevision` fails at `revisions_test.go:207`:
  expected `["web-auth-1", "rauthy-1"]`, actual `["web-auth-1", ""]`, "the Addon's first revision". That is the
  issue's state: the part follows `web-auth-1` and pins nothing after `rauthy-1` is stamped. The issue reproduces on
  origin/main f352341c.
- **It passes with the fix** under `-race`: `--- PASS` (0.01 s), and `-race -count=20` is ok.
- **Mutant M3** (no fallback to the current revision on line 248) fails the regression test with "the Addon's first
  revision".
- **Cause, not symptom.** The issue names the cause: in `followApp`, only a move set `addonRevision`. The fix adds one
  condition. A part that follows the App's latest Applied revision and pins nothing takes
  `cmp.Or(record, current)`, the same expression a move uses. `moves` is the exact negation of the old early return,
  so moves are unchanged. The fix adds no timeout or retry, swallows no error and skips no test.
- **The user-visible path.** The Addon's status update reaches the part. The controller enqueues every change of a
  watched kind (`internal/controller/controller.go:165`), and `MapAddon` maps an Addon change to the objects of its
  kinds (`internal/services/addon/addon.go:180`, wired at `pkg/funcd/addon_provider.go:260`). So the pass that pins
  the revision runs after the Addon's first stamp. The CI flake did not reproduce locally: the six `pkg/funcd` tests
  that wait for an add-on App to be Ready ran 180 times on the pre-fix overlay and 180 times with the fix, and every
  run passed (22.5 s and 22.2 s). Each kind has its own watch (`internal/controller/controller.go:121`), so the order
  cannot be forced cheaply. The reconciler-level test is the issue's own reproduction.
- **It is a bug** (the two checks). ADR-0339, read whole: Decision 3 keeps a part Pending until its first pin, and
  Decision 2 holds an object only while its Addon has no revision. No Scope, Alternative, Consequence or Open question
  accepts a part that never pins. ADR-0337 Decision 2 has `funcdctl app deploy` wait for the Addon's revision
  (`cmd/funcdctl/app.go:628`). That is an order the CLI keeps, not a rule that accepts a hang for other writers. The
  rows "Which revision an object runs" (`addons.md`) and "An App's revision with add-on parts" (`apps.md`) do not speak
  of the case. Issue searches (the field, the hold message, the test name, `followApp`) find only #1536, and no board
  card covers it. No test pinned the old behavior. The verdict `bug` holds.
- **ADR conformance.** The fix completes the rule that the stamp already states (`internal/app/revision.go:111`: a kind
  whose Addon has no revision yet gets no record, and its part pins the current revision). It matches the standalone
  rule (`revisions.go:186`), which pins the current revision while the pin is empty. The App revision does not move,
  so nothing steps back. The pass reads the current revision and pins it in one step of the one worker (Decision 6).
  No ADR file was edited.
- **Siblings.** The standalone path and dev's rule (`revisions.go:184-187`) already pin while the pin is empty.
  `internal/app`'s readiness rule (`status.go:71`) and its hook moves (`revision.go:158`) set no pin. No sibling has
  the same cause.
- **Scope and reuse.** Both files serve the issue. `newUnstampedRig` takes `newRevRig`'s setup unchanged, so nothing
  is copied. `record` writes no record for an empty `rev`, and its other callers get the same records as before. No
  existing assertion changed, and the oracle markers are untouched.
- **Conventions.** The change adds no exported name, number or dependency. The doc comment names the issue, as many
  comments in `internal/` do. The test follows the `TestIssue<N>_…` name, calls `t.Parallel()` and uses the file's
  pair assertions.
- **Checks.** `go test -race -count=1 ./internal/services/addon/` is ok. `go vet` is ok on the host and with
  `GOOS=linux`. `go tool golangci-lint run ./internal/services/addon/...` reports 0 issues. The repo-wide gate has not
  run on the branch yet; it runs once per PR.
- **Shape.** One commit. `fix(addons):` uses the scope of `feat(addons)` (48704723). The body has `Fixes #1536` and the
  attribution trailer.

### Definition of Done
12 / 13 items hold. Miss: item 4 (model), because mutants M1 and M2 survive (Minor 1). Item 13 applies, because a
spec models the part's pass, and it holds: the fix changes no modeled behavior (Routed 1).

### Model scorecard
Recorded: claude-opus-5-5 on #1536 (fix) → pass, 0/0/3, 3 model-attributed, DoD 12/13. See
docs/reviews/model-scorecard.md.

### Recommendation
Ready for the PR. Before it, add the probe's steps to the regression test to kill M1 and M2, name the case in the
addons.md row, and fix the `revRig` comment. Then run the gate once.
