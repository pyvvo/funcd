---
name: brief
description: Brief the decider on one decision in plain short words so they can decide without reading the whole document. Refine mode takes a needs-adr card, an issue or an agent report before its ADR exists; acceptance mode takes a Proposed ADR. Use for "summarize ADR-NNNN / card 07", "TL;DR of the ADR", "should I accept ADR-NNNN?", "refine card 07", "let's refine the next decision", "next card", "brief ADR-NNNN". Follow-ups inside a brief: "explain this with an example", "any caveat?", "what do you choose?", "I don't get the number". It explains, recommends and asks; the decider decides. It does not judge (run `adr-judge` first if there is no verdict), write the ADR (`adr`) or fix defects (`/fix`).
---

# Brief the decider

The decider owns every design choice but has no time to read a full ADR or issue thread. A brief gives them the
decision in one read, on any screen, between other work. Two modes share one style:

| Mode | Input | Ends with |
|---|---|---|
| **refine** | a needs-adr card, an issue, an agent or judge report | numbered choice questions |
| **acceptance** | a Proposed ADR | a recommendation, then "Accept? Or change what?" |

## Ground it first (both modes)

- **Verify before presenting.** A refine brief rests on a reproduction on current main (a `go test -overlay` probe or
  a `file:line` code trace, which an independent agent then tries to refute); a read-only workflow can verify many
  cards at once. An acceptance brief rests on the ADR file as it is now and its last judge verdict. If a check is
  still running, say so in one line and what would change.
- **Never present invention as fact** (CLAUDE.md *Grounding*): a new field, key, reason or number is marked new.
- **Security:** CLAUDE.md *Issues* applies: a possible vulnerability goes into no public text and no shared decision
  record; it becomes a private draft advisory.

## Style (both modes)

- Short plain sentences, one idea each, the answer first. Technical terms are fine; do not explain them.
- **One small concrete example** in an ASCII or YAML block: real names from the code or the ADR, before and after,
  the measured numbers. Mermaid does not render in the chat.
- A table for a comparison of two or more things, or one line per item when short.
- No line-by-line detail, no contract text, no history of how it was found: the ADR holds the depth.
- At most four questions, numbered, each answerable by a choice; the decider replies "1. ok 2. B".
- Aim for one screen. Cut anything the decision does not turn on.

## Refine mode — one card per message

1. **Title:** `**Card NN (#issues): <the problem in plain words>**`
2. **Today:** one or two sentences, then the example.
3. **Cause:** one sentence with the `file:line`.
4. **Options:** `A`, `B`, `C`, one or two lines each: what it does and its cost; one marked **(recommended)**; the
   Accepted or Implemented ADRs it supersedes or refines (header links), in a few words each.
5. **Questions:** 1 to 4, the recommended answer stated.

After the decider chose: record the answers on the decision board artifact when in use, else as a comment on the
needs-adr issue (never for a security card). Then draft the ADR with the `adr` skill from Step 2, with the recorded
answers as the brainstorm, up to Proposed; `adr-judge` judges it.

## Acceptance mode — one ADR per message

Ready only if Status is Proposed, the last `adr-judge` verdict covers the current text, and no Blocker or Major is
open. Otherwise say what is missing and recommend running `adr-judge` first.

1. **Title:** `**ADR-NNNN: <what it decides, in plain words>**` and one line: status, judge rounds and the last
   verdict, the card or issues it answers.
2. **It decides:** three to five bullets, one line each.
3. **What changes for users and operators:** behavior, new config keys, kinds, fields and CLI flags (with
   defaults), new status reasons — one line each, with one example when the change is visible.
4. **Choices:** the decider's own choices (to recall) and any choice an agent made that the decider has not
   confirmed (to confirm). The second group must be empty before acceptance.
5. **Deferred / debt:** workarounds (exit criterion), open questions, new deps (license; not Apache/MIT/BSD needs
   the decider's approval), the losing alternatives in a few words, or none.
6. **Cost and risks:** one to three bullets, from the ADR's Consequences.
7. **Replaces:** the Accepted or Implemented ADRs it supersedes or refines (header links), in a few words each.
8. **Recommendation:** accept / accept after <one change> / not ready, plus a one-line reason.
9. **Question:** "Accept? Or change what?"

On an explicit accept naming the ADR-NNNN: run the `adr` skill's Step 3.3 and Step 4 (CLAUDE.md, the ADR Accepted
row). On a change: revise, re-judge if the change is substantive, and brief again.

## Follow-ups (both modes)

- **"Explain X with an example":** a smaller example, two or three named things, today versus proposed.
- **"What do you choose?":** one line per open question: the choice and its one-line reason.
- **"Any caveat?":** the real cost in one or two bullets, then how to reduce it (a mitigation is offered as an
  option, never added silently).
- **"I don't get the number":** what the number counts, what happens below and above it in a tiny example, whether
  it was measured or chosen; offer to make a chosen number configurable with a default.
- **An ambiguous reply** ("1.", one "ok" for two questions): take the reading that changes nothing irreversible,
  say how you read it in one line, and ask only what is still open. Never act on an ambiguous reply for anything
  public or irreversible (a merge, a push, an issue, a status change).

## Guard the decider's choices

- Bring back every choice an agent made on its own (a knob, a default, a new behavior) as a choice to confirm.
- If a revision reversed a decided value, restore it and say so; if an agent's finding argues against a decided
  value, present the finding and let the decider choose again.
- Hand a defect found on the way to `/fix` (security → private advisory; needs a decision → a card).

## Examples

Illustrative (from the ADR-0150 draft). Refine:

```
**Card 25 (#518): int64 contract range**
Today a contract field `format: int64` accepts 2^70 on both shims.
  9,007,199,254,740,993  → Node: becomes …992 (rounded by JSON.parse)   Python: exact
Cause: ajv-formats checks int64 with Number.isInteger only (funcd-typescript shim/src/contract.ts:31).
Options:
  A  the full 64-bit range — Node checks an already-rounded number
  B  ±(2^53−1) on both shims (recommended) — big IDs travel as strings
  C  an unchecked label — the contract promises a check that never runs
Questions: 1. A, B or C?  2. Python bounded the same way? (recommended: yes)
```

Acceptance:

```
**ADR-0150: int64 means the JSON safe-integer range on every runtime**
Proposed · judged once, no Blocker or Major · answers #518
It decides:
  - int64 accepts −(2^53−1) … 2^53−1 on Node and Python, input (422) and output (500)
  - Node uses Number.isSafeInteger; Python adds minimum/maximum like int32
  - a larger value travels as a string field
Changes: a call sending 2^60 in an int64 field now gets 422
Choices: yours — B, Python bounded too · to confirm — none
Deferred / debt: none
Cost: int64 no longer matches OpenAPI's meaning; this ADR is the reference
Replaces: nothing (refines ADR-0058's profile row)
Recommendation: accept — judged clean, no open choice
Accept? Or change what?
```
