## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0354 implementation, round 2, model: claude-opus-5-5)

This report is the second round of ADR-0354's review. It covers the branch `feat/tunnel-edge` at 5364fcb4, rebased onto
main 1ccda7f5, which holds ADR-0344 and ADR-0345 (#1558). Round 1's report,
[adr-0354-implementation-claude-opus-5-5.md](adr-0354-implementation-claude-opus-5-5.md), found one Minor, which
eafac819 fixes. The reviewer did not write the work, and every check below was run, not read.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Round 1's finding

- **A doc comment on the wrong function** · fixed in eafac819. In `cmd/funcd/main.go`, `trustedProxies`' comment is now
  at `:1107-1108` above its function, and `limitsConfig`'s at `:1126`, right above `func limitsConfig`.

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
  under the coordinator's gate on 5364fcb4.
- **The reruns on 5364fcb4.** `go test ./internal/network/egress ./cmd/funcd ./pkg/funcd` passed, but for the routed
  flake: `cmd/funcd`, which holds `trustedProxies`, passed in 86.9 s. `just check-behavior` exited 0.
- **The behavior chapter** (a947dea0). The calls.md rows of this ADR moved unchanged into
  `docs/behavior/edge-behind-a-tunnel.md`, which the README and `scripts/behavior` list; security.md's row is unchanged.
- **Tracking.** This gate stamps the ADR `Implemented` and F272 `implemented`. The ADR's substance is unchanged: only
  its status line moves.

### Definition of Done

13 / 13 items hold, as in round 1: the 4 of the ADR's review checklist, and the 9 generic items that apply.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0354 (implementation, round 2) → pass, 0/0/0, 0 model-attributed, DoD 13/13. See
docs/reviews/model-scorecard.md.

### Recommendation

Implemented. The PR closes #1554 (`Fixes #1554`).
