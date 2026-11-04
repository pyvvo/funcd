---
name: behavior-registry
description: Keep docs/BEHAVIOR.md true: the one file that says, per process, what funcd does, how long it takes, what a user sees and who decided it (ADR-0313). Use when a default, a hard-coded value or a behavior changes, an ADR is accepted or superseded, a new package, kind, config section or provider arrives, `just check-behavior` fails, or a chapter needs writing or correcting. It records what was decided and asks about what was not; it never decides or fixes anything.
---

# Behavior registry

`docs/BEHAVIOR.md` has one chapter per process. It decides nothing: if it and an ADR disagree, the ADR wins and the file is
corrected, and code that differs from both is a surprise for the bug rule (CLAUDE.md, *Is it a bug?*). Paths are
repo-root-relative. Run tools through `scripts/agent/d`.

## When something changed

1. `scripts/agent/d just behavior-values` rewrites the generated blocks. Never hand-edit a block.
2. Read `git diff docs/BEHAVIOR.md`. For each value that changed, grep the written rows that cite it and correct them.
3. If a behavior changed, find its chapter and correct or add the row: what happens, the time, what a user sees, the status,
   the source. Remove a row the change made false. An ADR's *Behavior rows* section lists the rows it changes.
4. `scripts/agent/d just check-behavior`, then fix what it names:

   | It says | Do this |
   |---|---|
   | `writes the number ... inline` | Give the number a name at package level that says what it sets. A count's name must pass the name test (`nameTest` in `scripts/behavior/main.go`). Beside its config key, a number needs nothing. |
   | `package ... has no chapter`, `config section ... has no chapter` | Add it to `chapters()` under the process it belongs to, then answer *A new component*. |
   | `kind ... is on no chapter's Kinds line` | Add it to the chapter that owns its lifecycle. |
   | `now:` and `was:` | Run step 1. |
   | Anything with a line number | Correct that row: read what it cites first. |

5. An `uncovered` row goes to the decider as a question, with the evidence and no fix. The answer moves the row, and the PR or
   the issue records it. An answer that changes an Accepted ADR takes a superseding ADR.

## A new component

Answer each question with a row, or say why none is needed: how does it start? how does it stop? what happens to it and to a
call during a funcd restart, and after a crash? how is it backed up and restored? what happens when it hits a limit? Put its
states on the chapter's machine, or link its design spec.

## Where to look

| You need | Look in |
|---|---|
| Defaults | `internal/platform/config/config.go`, `examples/funcdconfig.yaml` |
| Hard-coded values | the generated blocks; by hand `images/runtime/duckdb/shim.py` and the pinned shims (`scripts/moddir.sh <module>`) |
| States and reasons | `api/types/v1alpha1`, each reconciler's reasons, `docs/design/**` |
| Errors | `api/fault`, and the message each step writes |
| The order of steps | `Run` and `Shutdown` in `pkg/funcd/funcd.go`, `pkg/funcd/providers.go`, `internal/lifecycle` |
| The why | an ADR's Scope, Alternatives, Consequences, Risks and Open questions, following each `Superseded in part by`; then issues (`gh issue list --state all --search`) and board cards (`python3 .claude/skills/project-management/driver.py list`, `show "<title>"`) |
| Proof | `TestScenario*`, `TestIssue*`, `e2e/*.venom.yml`, and the lanes' cases |

Find ADRs by content, not by title: `grep -l '<key or reason>' docs/adr/*`.

## A chapter

In order: a `Kinds:` line, a Mermaid `stateDiagram-v2`, a short note, one table, the generated block. About 50 lines, about 15
rows: the written part of the whole file stays under 800 lines.

- **The machine** tells the process in order, in the code's state names. Group states that behave alike. A process with no
  phase of its own, such as a startup, may use its steps as states. Link a design spec's generated machine instead of copying it.
- **The note** says in a few lines what the machine cannot, and links details. A word the code does not have is marked new here:
  `New words: Awake, Counted.`
- **The table** holds what someone could see and wonder about: a wait of about a second or more; a state, error or refusal a
  user sees; a cost, risk or limit an ADR accepted; a step nothing answers. Internal polls, buffers and log rates stay in the
  block. Follow the machine: ask at each state "what if a call, a stop or a crash arrives now?", and write down an answer
  nothing gives.

## A row

`| Where | What happens | Time | You see | Status | Source |`, with `|` inside a cell written `\|`.

- **Where**: the step or the trigger, in a few words.
- **What happens**: one short sentence of observable behavior, not the mechanism.
- **Time**: `` `name` N unit `` for a value (units `ms`, `s`, `min`, `h`, `KiB`, `MiB`, `GiB`, none for a count). The name is a
  config key or a name in a block, and the check compares the number. `none` when nothing waits.
- **You see**: the phase, reason, status code or message, word for word from the code.
- **Status**: one word.
- **Source**: the clause you read (`ADR-0161 Decision 4`), the test that pins it (`` `TestScenarioActivationTimeout` ``), a lane case.

| Status | When | Source must name |
|---|---|---|
| `decided` | an ADR or the decider chose it | an accepted ADR, or `decider <date>` with `#N` or `card "<title>"` |
| `accepted` | an ADR or the decider named it as a cost or a risk | the same |
| `deferred` | a card, an ADR's open question or an open issue holds it | `card "<title>"`, `ADR-NNNN Open questions` or `#N` |
| `uncovered` (written `**uncovered**`) | nothing speaks of it | what you read |

## The rules

- **Read, do not recall.** Open each ADR you cite, whole: costs live in Scope, Alternatives, Consequences, Risks and Open
  questions. Follow each `Superseded by` and `Superseded in part by`: cite what holds now. A row points at what to read and proves
  nothing alone, and the check proves only that a citation exists, so the reading is yours.
- **Check the code too.** Trace each row's behavior to the code that runs it. If the code does what no clause says, or differs
  from its ADR, the row says what the code does, with `uncovered`, and its source names both; tell the decider. Until the bug
  rule's two checks are done, write "surprising", never "bug", and propose no fix.
- **Mark what you invent.** A name, a state or a reason is traced to the code or marked new.
- **Security.** A possible vulnerability goes in no row: it is a private draft advisory (CLAUDE.md, *Issues*).
- **Identity.** No absolute path, local username or personal email.
