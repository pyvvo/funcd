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
| Who may act | `internal/auth/rbac`, `cmd/funcd/credentials.go` |
| Deletes and owners | `internal/gc`, `internal/ownedstore` |
| The unit and the CLI | `cmd/funcd/install.go`, `configs/systemd/funcd.service`, `cmd/funcdctl` |
| The order of steps | `Run` and `Shutdown` in `pkg/funcd/funcd.go`, `pkg/funcd/providers.go`, `internal/lifecycle` |
| The why | an ADR's Scope, Alternatives, Consequences, Risks and Open questions, following each `Superseded in part by`; then issues (`gh issue list --state all --search`) and board cards (`python3 .claude/skills/project-management/driver.py list`, `show "<title>"`) |
| Proof | `TestScenario*`, `TestIssue*`, `e2e/*.venom.yml`, and the lanes' cases |

Find the ADRs in this order: the code's own comments (`grep -rhno 'ADR-[0-9]*' <package> | sort -u`), a key or a reason
(`grep -l '<key or reason>' docs/adr/*`), the feat rows in `docs/feat/`. A common word finds too much. Read whole each ADR you
cite; for the others, the header and the Decision are enough to know you need not cite them.

## A chapter

In order: a `Kinds:` line, a Mermaid `stateDiagram-v2`, a short note, one table, the generated block. About 50 lines, about 15
rows: the written part of the whole file stays under 800 lines. A step that belongs to a neighbor's chapter gets at most one row,
saying what a caller sees here, and the neighbor holds the rest. A kind nothing acts on still sits on a `Kinds:` line: one row says so.

- **The machine** tells the process in order, in the code's state names. Group states that behave alike. A process with no
  phase of its own, such as a startup, may use its steps as states. Link a design spec's generated machine instead of copying it.
  Use one word per state, and write each transition on one line: `A --> B : what makes it move`.
- **The note** says in a few lines what the machine cannot, and links details. A word on the machine that the code does not have
  is marked new here on one line, or the check refuses it: `New words: Awake, Counted.`
- **The table** holds what someone could see and wonder about: a wait of about a second or more; a state, error or refusal a
  user sees; a cost, risk or limit an ADR accepted; a step nothing answers. Internal polls, buffers and log rates stay in the
  block. Follow the machine: ask at each state "what if a call, a stop or a crash arrives now?", and write down an answer
  nothing gives.

## A row

`| Where | What happens | Time | You see | Status | Source |`, with `|` inside a cell written `\|`. A row stays under 400
characters, or the check refuses it: it holds one fact someone could ask about, not all there is to say. If it holds two facts,
cut one or split it. A row that mixes a decision and a cost is two rows, each with its own status.

- **Where**: the step or the trigger, in a few words.
- **What happens**: one short sentence of observable behavior, not the mechanism.
- **Time**: `` `name` N unit `` for a value (units `ms`, `s`, `min`, `h`, `KiB`, `MiB`, `GiB`, none for a count; `` `key` 0 `` for
  a limit that is off). The name is a config key or a name in a block, and the check compares the number. A number with no name, such
  as a shim's or a library's, is written plain and says where it comes from: `1 s (the shim)`. `none` when nothing waits.
- **You see**: the phase, the reason, the status code or the message a user reads, word for word from the code and without its `op:`
  prefix. Name the one or two someone would search for: the code holds the full list. A library's word goes without backticks, which
  the check keeps for funcd's own names.
- **Status**: one word.
- **Source**: the clause you read (`ADR-0161 Decision 4`) and the test that pins it (`` `TestScenarioActivationTimeout` ``) or a
  lane case. One clause and one test are usually enough.

| Status | When | Source must name |
|---|---|---|
| `decided` | an ADR or the decider chose it | an accepted ADR, or `decider <date>` with `#N` or `card "<title>"` |
| `accepted` | an ADR or the decider named it as a cost or a risk; or no ADR speaks of it, and a test pins it or a closed issue's fix chose it | the same, or the test, or `#N` |
| `deferred` | a card, an ADR's open question or an open issue holds it | `card "<title>"`, `ADR-NNNN Open questions` or `#N` |
| `uncovered` (written `**uncovered**`) | nothing speaks of it | what you read |
| `differs` (written `**differs**`) | an ADR or the blueprint says otherwise, and the code does this | the clause and the code that differs |

An ADR accepted a cost and a card proposes to change it: `accepted`, citing both. A card is named by a few words of its title, as
`driver.py show` finds it. A later ADR that made a sentence stale with no `Superseded in part by` line: cite the later one and tell the
decider. Evidence you ran yourself goes in the source as `ran it`.

## The rules

- **Read, do not recall.** Open each ADR you cite, whole: costs live in Scope, Alternatives, Consequences, Risks and Open
  questions. Follow each `Superseded by` and `Superseded in part by`: cite what holds now. A row points at what to read and proves
  nothing alone, and the check proves only that a citation exists, so the reading is yours.
- **Check the code too.** Trace each row's behavior to the code that runs it. If the code does what no clause says, the row says
  what the code does, with `uncovered`. If it differs from its ADR, with `differs`. Either way tell the decider. An `uncovered` row is a
  question; a `differs` row is a surprise the bug rule's two checks turn into an issue or a superseding ADR, and the issue then moves it
  to `deferred`. Until those checks are done, write "surprising", never "bug", and propose no fix.
- **Read beyond the first hits before you write `uncovered`.** Of 32 rows first written `uncovered`, a second reader moved 3 to `decided`,
  11 to `differs` and found 2 false.
- **Mark what you invent.** A name, a state or a reason is traced to the code or marked new.
- **Security.** A possible vulnerability goes in no row: it is a private draft advisory (CLAUDE.md, *Issues*).
- **Identity.** No absolute path, local username or personal email.
