# ADR-0315 implementation review — claude-opus-5-5 (round 3)

## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0315 implementation, model: claude-opus-5-5)

This round checks one thing, the Blocker of [round 2](adr-0315-implementation-claude-opus-5-5-2.md): a Scenario's sentence changed in the Accepted ADR. That sentence is back as it
was accepted, so the Blocker is closed, and every finding of [round 1](adr-0315-implementation-claude-opus-5-5.md) was closed in round 2. The ADR is stamped `Implemented` and F229
`implemented`. The pass carries one condition: `scripts/agent/gate.sh` and the `egress` lane must run green over the batch before the merge. By this run's instruction I ran
neither, nor `just ci` or the e2e suite. A later stage cherry-picks and squashes these commits, so these three reports name a commit by what it is.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### Round 2's Blocker: closed

The way out taken was the second one: the accepted sentence is back, and no change to the Scenario is left to rule on. Diffs of `docs/adr/0315-what-a-domain-rule-allows.md`:

| Compared | Differences |
|---|---|
| The text as first accepted, against the ADR commit | One line, the link to ADR-0238 |
| The ADR commit, against the implementation commit | One line, the status line's `Accepted` → `Reviewing` |
| The text as first accepted, against the implementation commit | Those two lines and nothing else |

- **The scenario** reads as accepted again, on lines 63-64: "When a worker sends a lookup on `:53`, over UDP or TCP, or opens an external connection, Then nothing leaves the node."
- **The link** now points to `docs/adr/0238-container-bridge-address.md`, which exists. It is the correction round 1 routed, and it is no substance.
- **So the tracking rule holds:** the ADR's Context, Scenarios, Decision, Contracts and Alternatives are as accepted.

### Routed (not this work's)

- **`docs/PROJECT-SUMMARY.md`**: the later stage. **Two comments in the pinned `funcd-typescript` v0.24.0**: that repository, at its next release. Both are as round 1 left them.

### ✅ Verified correct (keep it)

- **Nothing else moved since round 2.** Round 2's tree and this one differ in one file and one line, that sentence, so I reran no test: the code, the tests, the docs and the lane steps are
  those rounds 1 and 2 read and ran. Their evidence stands.
- **The stamp is in place.** The ADR's status line reads `Implemented` with its earlier history kept, and F229 reads `implemented`. After those two edits, `just check-hygiene` and
  `just check-behavior` each exit 0.
- **Feature issue.** The ADR's References cite none, so the PR closes none.

### Definition of Done

14 of 16 hold and 2 are pending. They hold: the six lines of the ADR's Review checklist, and eight applicable generic items (no port here has drivers, so the contract suite item does not
apply). Pending, for the batch's gate and lane pass: the ADR's *Done* line (`just ci` and the `egress` lane) and the generic full suite.

### Model scorecard

To record, by the stage that writes the ledger: claude-opus-5-5 on ADR-0315 (implementation, round 3) → pass, 0 blockers, 0 majors, 0 minors, 0 model-attributed, DoD 14/16. See
docs/reviews/model-scorecard.md once it is written.

### Recommendation

Merge with the batch once its gate and `egress` lane pass. The ADR is `Implemented` now, so it is frozen: if either run finds a fault in this work, the fix goes in the code, and any change to
what the ADR says goes in a superseding ADR.
