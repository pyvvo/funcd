# ADR-0315 implementation review — claude-opus-5-5 (round 2)

## Verdict: changes requested — 1 blocker, 0 majors, 0 minors (ADR-0315 implementation, model: claude-opus-5-5)

Reviewed the branch's two commits, the ADR commit and the implementation commit, which are round 1's two commits, amended; nothing is pushed or merged. Every
finding of round 1 is closed, and the code and its tests stand. One thing holds the stamp: a sentence of a Scenario in the Accepted ADR
changed in place, and no ruling on it is on record. That is the freeze rule, and no finding against the code or the new wording. The ADR
stays `Reviewing` and F229 stays `reviewing`. By this run's instruction I ran no `scripts/agent/gate.sh`, `just ci`, e2e suite or lane.

### 🔴 Blockers

**1. A Scenario of the Accepted ADR changed in place, with no ruling on record · attribution: `adr` (process, not the code) · home: here, by the
freeze rule.**

The text as first accepted, which round 1 read, and the ADR as it reads now differ in two lines (`docs/adr/0315-what-a-domain-rule-allows.md`). One is the
link round 1 routed, and it decides nothing. The other is scenario `no-forwarder-nothing-out`, lines 63-64:

- Was: "When a worker sends a lookup on `:53`, over UDP or TCP, or opens an external connection, Then nothing leaves the node."
- Now: "When a worker sends a lookup on `:53` to an outside address, over UDP or TCP, or opens an external connection, Then none of them leaves
  the node."

The rule is plain. `.claude/CLAUDE.md` says none of the Scenarios changes after acceptance, and `.claude/skills/adr-impl-review/SKILL.md` Step 2 says any
change to them is itself a Blocker. This repository has met the same case twice:

- `docs/reviews/adr-0005-implementation-deepseek-v4-pro-3.md` gave it this Blocker over clean code, on an ADR still in flight. Round 4 passed once the
  decider's ruling stood in the ADR's header.
- ADR-0290 carries the record in its header: `Amended in place: 2026-10-27, by the decider on #1302`.

Here the commits were amended, so a `git diff` from the ADR commit to the implementation commit shows only the status line, and the usual check cannot see the change. The header
records nothing. Its status line delegates acceptance to the session driving this work, which says who accepted the ADR, not who may change it
afterwards. This review learned of the change from that session's message, which is no ruling by the decider, and it cannot tell whether the
delegation reaches this far.

What is not wrong:

- The new sentence reads as the ADR's Scope does, which leaves the `input` chain's policy out.
- The Decision and the Contracts are unchanged, and so is the code: round 1's rig and `TestScenarioNoForwarderNothingOut` hold under both wordings.

Either way out closes this with no code change and no repeat of the evidence below:

1. **Keep the new sentence, with the ruling on record.** The ADR's header gets a line in ADR-0290's shape, saying who ruled, what changed and why. Whose
   ruling that may be is the decider's to say.
2. **Put the accepted sentence back,** and keep the link's correction. This needs nobody. A later change to the sentence is a short partial supersession
   (ADR-0272 Decision 7).

### Round 1's findings

| Round 1 | Status | Evidence on the amended implementation commit |
|---|---|---|
| Major: the helper of `TestIssue368_ServeCancelledEarlyReleasesListeners` reads the old name | Closed | `internal/network/egress/forwarder_test.go:553`. Same mutant, 30 runs each: 30 of 30 fail on `a DNS server goroutine outlived Serve`, with and without `-race` (was 18 and 29). With no mutant 0 of 30 fail, both ways. |
| Minor 1: the fallback bind | Closed | `TestBuildEgressBindsTheBridgeAddress` runs with a bridge address and with none (`pkg/funcd/own_principal_internal_test.go:119`). `cmp.Or(c.bridgeAddress, "0.0.0.0")` now fails it. |
| Minor 2: the line's count, the shared allowance, the comment | Closed | Two subtests of `TestScenarioRefusedLookupAudited` (`forwarder_test.go:413`, `:426`). `"more", 0*more` and `key := src` now fail them. The `refusalLog` comment (`internal/network/egress/forwarder.go:250-252`) reads as `note` does. |
| Minor 3: the TTL, the token by principal | Closed | `TestRecordKeepsTheAnswersTTL` (`forwarder_test.go:287`) and `TestWildcardTokenFollowsWhoARuleCovers` (`:265`) fail on their mutants. The second also holds that a Function a rule does not name gets no token. |
| Minor 4: `dnsResolver` read only with a forwarder | Closed | `TestDNSResolverIsReadOnlyWithAForwarder` (`cmd/funcd/pacing_test.go:74`) fails on its mutant, and holds that a bad value stops the start with a forwarder. |
| Minor 5: scenario markers | Closed | `forwarder_test.go:203`, `:233` name `lookup-refusals` and `lookup-follows-who-a-rule-covers`. |
| Minor 6: the folded row | Closed | `docs/behavior/security.md:58` is its own row and cites `TestScenarioLookupNameOutsideHostCharacters`. |

The six mutants ran against whole packages; each fails in the test named above and in no other.

### Routed (not this work's)

- **The ADR-0238 link** in the header is corrected. It is a link, not substance, and its home was the session holding acceptance.
- **`docs/PROJECT-SUMMARY.md`**: the later stage. **Two comments in the pinned `funcd-typescript` v0.24.0**: that repository, at its next release. Both are as round 1 left them.

### ✅ Verified correct (keep it)

- **Commands, each exit 0.** Through `scripts/agent/d`:
  - `go test -race -count=1 ./internal/network/egress/ ./pkg/funcd/ ./cmd/funcd/`;
  - `go vet` on those packages, for darwin and for linux/arm64;
  - `go tool golangci-lint run` on them (0 issues);
  - `just check-behavior`;
  - in the `funcd-linux-tests` VM through `scripts/linux-exec.sh`: `./internal/network/egress/` whole, `TestBuildEgressBindsTheBridgeAddress` with both subtests,
    `TestStoreDomainRules`, and `TestDNSResolverIsReadOnlyWithAForwarder`.
- **What can have changed.** A `git diff` from round 1's implementation commit to the amended one touches six files: the ADR's two lines, one behavior row, the `refusalLog` comment and three test files. The only
  production change is that comment, and `e2e/` is untouched. So I did not repeat the lane steps, the rig, the revert checks or the Linux rule-plan tests, and round 1's
  evidence for them stands.
- **The new tests have the house shape.** They call `t.Parallel()` where the package's tests do, poll instead of sleeping, and add no dependency. No test was weakened: the only
  lines removed from the tests are the helper's old string, the two old markers, and the bind test's body, now inside its subtests with its assertions intact.

### Definition of Done

13 of 16 hold, 2 are pending and 1 misses. They hold: the six lines of the ADR's Review checklist, and seven applicable generic items. Pending, for the batch's gate and lane
pass: the ADR's *Done* line and the generic full suite. The miss is the generic tracking item, whose ADR substance is the Blocker above.

### Model scorecard

To record, by the stage that writes the ledger: claude-opus-5-5 on ADR-0315 (implementation, round 2) → changes-requested, 1 blocker, 0 majors, 0 minors, 0 model-attributed,
DoD 13/16. See docs/reviews/model-scorecard.md once it is written.

### Recommendation

Take one of the two ways out above, then one short check diffs the ADR and makes the stamp; nothing else needs rerunning before the batch's gate and lane pass.
