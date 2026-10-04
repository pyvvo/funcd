---
name: brief
description: Brief the decider on one decision in plain short words, so they can decide without reading the whole document — either (refine) a needs-adr decision card, an issue or an agent report, before its ADR exists, with one simple example, options, one recommendation and at most four choice questions; or (acceptance) a Proposed ADR, with what it decides, what changes, the choices still to confirm, its cost and the judge result, ending in "accept, or change what?". Use for "refine card 07", "next card", "brief ADR-0156", "is ADR-0150 ready to accept?", "explain this with an example", "what do you choose?", "any caveat?", "I don't get the number". It explains and asks; the decider decides. Writing the ADR is the `adr` skill; judging it is `adr-judge`; fixing a defect found on the way is `/fix`.
---

# Brief the decider

The decider owns every design choice but has no time to read a full ADR or issue thread. A brief gives them the
decision in one read, on any screen, between other work. Two modes share one style:

| Mode | Input | Ends with |
|---|---|---|
| **refine** | a needs-adr card, an issue, an agent or judge report | numbered choice questions |
| **acceptance** | a Proposed ADR | "accept, or change what?" |

## Ground it first (both modes)

- **Verify before presenting.** A refine brief rests on a reproduction on current main (an overlay probe or a code
  trace with `file:line`) re-checked by a skeptic; a read-only workflow can verify many cards at once. An acceptance
  brief rests on the ADR file as it is now and its last judge verdict. If a check is still running, say so in one
  line and what would change.
- **Never present invention as fact** (CLAUDE.md *Grounding*): a new field, key, reason or number is marked new.
- **Security:** a possible vulnerability is said only in the private conversation; public text (issues, PRs,
  commits) describes the change neutrally, and a released vulnerability goes to a private advisory.

## Style (both modes)

- Short plain sentences, one idea each, the answer first. Technical terms are fine; do not explain them.
- **One small concrete example** in an ASCII or YAML block: real names (`hello`, `orders`, `report`; bucket
  `photos`, prefix `in/`), before and after, the measured numbers. Mermaid does not render in the chat.
- A table for any comparison of two or more things.
- No line-by-line detail, no contract text, no history of how it was found: the ADR holds the depth.
- At most four questions, numbered, each answerable by a choice; the decider replies "1. ok 2. B".
- Aim for one screen. Cut anything the decision does not turn on.

## Refine mode — one card per message

1. **Title:** `**Card NN (#issues): <the problem in plain words>**`
2. **Today:** one or two sentences, then the example.
3. **Cause:** one sentence with the `file:line`.
4. **Options:** `A`, `B`, `C`, one or two lines each: what it does and its cost; one marked **(recommended)**; the
   Implemented ADR it changes, in a few words.
5. **Questions:** 1 to 4, the recommended answer stated.

After the decider chose: record it where the session tracks decisions (the decision board, when one is in use),
then draft the ADR (the `adr` skill, or a draft → independent `adr-judge` → revise loop) up to Proposed.

## Acceptance mode — one ADR per message

1. **Title:** `**ADR-NNNN: <what it decides, in plain words>**` and one line: status, judge rounds and the last
   verdict, the card or issues it closes.
2. **It decides:** three to five bullets, one line each.
3. **What changes for users and operators:** behavior, new config keys with defaults, new status reasons — one line
   each, with one example when the change is visible (a status block, a config line).
4. **Choices** — a table: the decider's own choices (to recall) and any choice an agent made that the decider has
   not confirmed (to confirm). The second group must be empty before acceptance.
5. **Cost and risks:** one to three bullets, from the ADR's Consequences.
6. **Replaces:** the Implemented ADR clauses it supersedes, in a few words each.
7. **Question:** "Accept? Or change what?"

On "accept": set Accepted with the date and propagate in the same session as CLAUDE.md requires (the feat row,
the blueprint if refined, the back-links, the board card) — the `adr` skill's acceptance steps. On a change: revise,
re-judge if the change is substantive, and brief again.

## Follow-ups (both modes)

- **"Explain X with an example":** a smaller example, two or three named things, today versus proposed.
- **"What do you choose?":** one line per open question: the choice and its one-line reason.
- **"Any caveat?":** the real cost in one or two bullets, then how to reduce it (a mitigation is offered as an
  option, never added silently).
- **"I don't get the number":** what the number counts, what happens below and above it in a tiny example, whether
  it was measured or chosen; offer to make a chosen number configurable with a default.
- **An ambiguous reply** ("1.", one "ok" for two questions): take the reading that changes nothing irreversible,
  say how you read it in one line, and ask only what is still open. Never act on an ambiguous reply for anything
  public (a merge, a push, an issue).

## Guard the decider's choices

- Bring back every choice an agent made on its own (a knob, a default, a new behavior) as a choice to confirm.
- If a revision reversed a decided value, restore it and say so; if an agent's finding argues against a decided
  value, present the finding and let the decider choose again.
- Defects found on the way are fixed directly (test first, independent review, their own PR) unless the fix needs
  a decision; then it becomes a card.

## Examples

Refine:

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
Proposed · judged once, no Blocker or Major · closes #518
It decides:
  - int64 accepts −(2^53−1) … 2^53−1 on Node and Python, input (422) and output (500)
  - Node uses Number.isSafeInteger; Python adds minimum/maximum like int32
  - a larger value travels as a string field
Changes: a call sending 2^60 in an int64 field now gets 422
Choices: yours — B, Python bounded too · to confirm — none
Cost: int64 no longer matches OpenAPI's meaning; this ADR is the reference
Replaces: nothing (refines ADR-0058's profile row)
Accept? Or change what?
```
