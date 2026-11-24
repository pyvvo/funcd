## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0352 implementation, round 2, model: claude-opus-5-5)

This report is the second round of ADR-0352's review. It covers the branch `feat/tunnel-edge` at 5364fcb4, rebased onto
main 1ccda7f5, which holds ADR-0344 and ADR-0345 (#1558). Round 1's report,
[adr-0352-implementation-claude-opus-5-5.md](adr-0352-implementation-claude-opus-5-5.md), found one Minor, which
f3649d4f fixes. The reviewer did not write the work, and every check below was run, not read.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Round 1's finding

- **The guide's sentence on `funcdctl dev`** · fixed in f3649d4f. The page now reads "Without it on containerd, a
  Function's workers connect anywhere and an add-on engine nowhere. Under `funcdctl dev`, workers and engines both
  connect anywhere" (`docs/guide/develop/security-and-networking/networking/index.md:144-145`), which matches ADR-0334
  Decision 2 and `docs/behavior/funcdctl-dev.md:49`.

### Routed (not this work's)

- **A load flake in `pkg/funcd`** · elsewhere: the flake tracker (#1142), which does not list it yet.
  - The failure: `TestBackup_GoesOnWhenItsListingFails/pages_with_no_key` failed twice in full runs of
    `go test ./pkg/funcd`, while the coordinator's gate ran on the same host. Both times its wait for "Backup b1 reaches
    Succeeded" timed out.
  - It passed three runs alone. Main 1ccda7f5's own full run of `pkg/funcd`, under the same load, failed on another
    test, `TestShutdownReleasesTheLoopbackFeedPort`.
  - Why it is not this work's: neither the rebase nor any commit of the branch changes a backup file.

### ✅ Verified correct (keep it)

- **The rebase.** `git range-diff de9a0081..03aa1e38 1ccda7f5..dbf69650` shows the four ADR commits and the three
  implementation commits unchanged, so round 1's evidence on 03aa1e38 holds for this code. Main's two newer commits are
  under the coordinator's gate on 5364fcb4, which runs the repository-wide checks.
- **The reruns on 5364fcb4.**
  - `go test ./internal/network/egress ./cmd/funcd ./pkg/funcd` passed, but for the routed flake.
  - `TestScenarioGatewayNeverDialsTheEdge` and `TestTheEdgeListensOnTheBridgeWhereTheGatewayRuns` passed five runs
    under `-race`, and `TestDecideRefusesTheEdgesAddresses` passed five as well.
  - `just check-behavior` exited 0.
- **The behavior chapter** (a947dea0). The twelve tunnel rows moved out of calls.md unchanged into
  `docs/behavior/edge-behind-a-tunnel.md`. The README's chapter table and `scripts/behavior` both list the new chapter.
  The egress.md and addons.md rows of this ADR are unchanged.
- **Tracking.** This gate stamps the ADR `Implemented` and F274 `implemented`. The ADR's substance is unchanged: only
  its status line moves.

### Definition of Done

14 / 14 items hold, as in round 1: the 5 of the ADR's review checklist, and the 9 generic items that apply.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0352 (implementation, round 2) → pass, 0/0/0, 0 model-attributed, DoD 14/14. See
docs/reviews/model-scorecard.md.

### Recommendation

Implemented. The PR closes #1552 (`Fixes #1552`).
