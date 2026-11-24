## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0353 implementation, round 2, model: claude-opus-5-5)

This report is the second round of ADR-0353's review. It covers the branch `feat/tunnel-edge` at 5364fcb4, rebased onto
main 1ccda7f5, which holds ADR-0344 and ADR-0345 (#1558). Round 1's report,
[adr-0353-implementation-claude-opus-5-5.md](adr-0353-implementation-claude-opus-5-5.md), found one Minor, which
5364fcb4 fixes. The reviewer did not write the work, and every check below was run, not read.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Round 1's finding

- **The gateway scenario's named test was not in `pkg/funcd`** · fixed in 5364fcb4.
  - `TestScenarioGatewayNeverDialsTheEdge` (`pkg/funcd/bridge_pin_internal_test.go`) now runs on a platform bound on
    every address, with an EgressPolicy whose `cidrs` rule holds the node's address and the bridge address.
  - It asks the gateway that the platform built, through the real PDP: both addresses are refused at the data plane's
    port, and the node's address is let through at another port.
  - The egress package's unit test of the refusal is now `TestDecideRefusesTheEdgesAddresses`, and the egress.md row
    cites both tests.

### Routed (not this work's)

- **A load flake in `pkg/funcd`** · elsewhere: the flake tracker (#1142), which does not list it yet.
  - The failure: `TestBackup_GoesOnWhenItsListingFails/pages_with_no_key` failed twice in full runs of
    `go test ./pkg/funcd`, while the coordinator's gate ran on the same host. Both times its wait for "Backup b1 reaches
    Succeeded" timed out.
  - It passed three runs alone. Main 1ccda7f5's own full run of `pkg/funcd`, under the same load, failed on another
    test, `TestShutdownReleasesTheLoopbackFeedPort`.
  - Why it is not this work's: neither the rebase nor any commit of the branch changes a backup file.

### ✅ Verified correct (keep it)

- **Mutants of the changed lines, killed.** Each was one edit, applied with `go test -overlay` outside the tree:
  - the platform never filling its edge set: `TestScenarioGatewayNeverDialsTheEdge`,
    `TestTheEdgeListensOnTheBridgeWhereTheGatewayRuns`;
  - the gateway's edge refusal skipped: `TestScenarioGatewayNeverDialsTheEdge`, `TestDecideRefusesTheEdgesAddresses`;
  - the refusal of every node address under a wildcard bind skipped: `TestScenarioGatewayNeverDialsTheEdge`,
    `TestDecideRefusesTheEdgesAddresses`.
- **`egress.Deps.Decide` is not scope creep.**
  - It adds no behavior: it forwards to `decideConnect`, which `Serve` already runs for each redirected connection
    (`internal/network/egress/gateway.go`).
  - Its only caller is the scenario test. `Serve`'s own path cannot run there, since it reads `SO_ORIGINAL_DST` on
    Linux.
  - The executor's new `gatewayDeps` is the value the gateway is built from. Its edge set is the same slice, which
    `edgeBound` fills.
  - The package is internal, and the codebase already exports a function for its tests
    (`internal/controlplane/controlplane.go:447`).
- **The rebase.** `git range-diff de9a0081..03aa1e38 1ccda7f5..dbf69650` shows the four ADR commits and the three
  implementation commits unchanged, so round 1's evidence on 03aa1e38 holds for this code. Main's two newer commits are
  under the coordinator's gate on 5364fcb4.
- **The reruns on 5364fcb4.**
  - `go test ./internal/network/egress ./cmd/funcd ./pkg/funcd` passed, but for the routed flake.
  - The two gateway tests and the listener test each passed five runs under `-race`.
  - `just check-behavior` exited 0.
- **The behavior chapter** (a947dea0). The calls.md rows of this ADR moved unchanged into
  `docs/behavior/edge-behind-a-tunnel.md`, which the README and `scripts/behavior` list. That brings calls.md's written
  part back under its cap.
- **Tracking.** This gate stamps the ADR `Implemented` and F273 `implemented`. The ADR's substance is unchanged: only
  its status line moves.

### Definition of Done

13 / 13 items hold: the 4 of the ADR's review checklist, and the 9 generic items that apply. The gateway scenario's
named test is now in `pkg/funcd`, as the plan asks.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0353 (implementation, round 2) → pass, 0/0/0, 0 model-attributed, DoD 13/13. See
docs/reviews/model-scorecard.md.

### Recommendation

Implemented. The PR closes #1553 (`Fixes #1553`).
