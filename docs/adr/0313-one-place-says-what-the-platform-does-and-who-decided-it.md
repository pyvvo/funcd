# ADR-0313: One place says what the platform does and who decided it

- **Status**: Proposed 2026-11-04, judged three rounds; an experiment built the whole registry, and this text folds in what it found
- **Date**: 2026-11-04
- **Deciders**: green-0-rabbit
- **Tags**: meta, process, behavior-registry, documentation
- **Realizes**: [FEAT-0001/F227](../feat/0001-feat-v1.1.md) (one place that says what the platform does)
- **Supersedes (in part)**: [ADR-0000](0000-adr-process.md) — its *Template* gains a *Behavior rows* section; its
  *Workflow gates* 2 to 5 gain a behavior-row step · [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md)
  — what an amendment carries (its Decision 7) gains *Behavior rows*
- **Relates to**: ADR-0272 (a generated document a check keeps true) · [ADR-0002](0002-source-code-conventions-and-patterns.md) (the conventions Decision 7 adds to)

## Context & Need

A session that meets a surprising wait has nowhere to look up whether it was decided. The answer is spread over 313
ADRs, the open issues, the board's cards, about 170 config keys and about 360 hard-coded values. The bug rule (#1392)
makes a session read them before it writes "bug", because 13 of 125 closed `kind/bug` issues were decided behavior
(#1226), but that read has no index, so each surprise costs a long dive: on 2026-11-04, a call held 30 s while a Lake came
back took four ADRs and a code read to explain, and one case, a gate clearing under a held call, nothing speaks of.

**Purpose.** One place that says, per process, what happens, how long it takes, what a user sees and who decided it. A
session or a person reads it before calling something a bug; an ADR author updates it; the decider reads its
`uncovered` rows as questions. It decides nothing.

## Scenarios

- `scenario: value-changed`: Given a config default or a named constant changes, When `just check-behavior` runs, Then
  it exits non-zero, prints the row as it was and as it is now and names `/behavior-registry`; `just behavior-values`
  then changes only the generated blocks, and the check passes or names each written row that cites the old number.
- `scenario: line-moved`: Given a value moves to another line, or a function with no value is added, When the check
  runs, Then it passes.
- `scenario: inline-number`: Given a function calls `context.WithTimeout(ctx, 30*time.Second)` or
  `io.LimitReader(r, 1<<20)`, When the check runs, Then it fails naming the file and the function. A number beside its
  config key passes, and when it differs from the key's default a row `<key> (fallback)` shows it.
- `scenario: new-component`: Given a new package, config section or kind that no chapter names, or a chapter with no file,
  no machine or no table, When the check runs, Then it fails naming it.
- `scenario: row-does-not-hold`: Given a row cites an ADR that is missing, wholly superseded or not accepted, a missing
  test, status reason or link, or `` `name` N unit `` with a name that does not exist or a number that differs, or its
  status breaks Decision 5, or it passes 400 characters, or a machine's state is not in the code and not marked new, When
  the check runs, Then it fails naming the line.
- `scenario: parallel-prs`: Given two PRs each add a value to one chapter's block, When they merge, Then no hand edit is
  needed: the rows merge cleanly, or `just behavior-values` clears a conflict inside the block.
- `scenario: surprise-looked-up`: Given a session sees a 503 after 30 s on a Function, When it follows the bug rule, Then
  step 2 greps what it saw, and each row it hits names `invoke.activationTimeout`, its status and what to read next.
- `scenario: adr-states-its-rows`: Given an ADR numbered above 0313 with no *Behavior rows* section, When the check
  runs, Then it fails naming the file.

## Scope

**In**: the chapters, their generated blocks, how a value is shaped in code, the check, the `/behavior-registry` skill, the gate
steps, the bug rule's first read, the Function machine's home. **Out**: deciding or fixing anything a row shows (an `uncovered` row is a question answered outside this ADR);
every error message (a row cites a kind or a reason where it needs one); comparing a machine or a row's meaning with the
code, and generating values from a shim (Open questions).

## Constraints & Decision drivers

- The decider's picks: chapters by process, one file each, a Mermaid machine first, only what matters in the written part,
  values generated from the config and the code, no data file, a skill that says where to look, values shaped by name.
- No new dependency. The check runs in `just ci` in seconds, with the same result on macOS and Linux: it parses every
  file whatever its build tags, and a generated row holds no host-derived value and only project-root-relative paths.
- A possible vulnerability never becomes a row: it stays in its private advisory (CLAUDE.md, *Issues*).

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Flows: a machine and rows per process | a step nothing answers shows as a gap, as the 30 s case did | written rows can go stale | chosen by the decider |
| Values only | all generated, nothing stale | no rule and no gap: the 30 s is one row of about 530 | kept as the folded blocks, not alone |
| One file with chapters | one place to read | about 45k tokens whole, where a chapter is about 3k | the decider's first pick, reversed on that measure |
| YAML or JSON rows and a renderer | parseable | one more source; the table holds the same fields and the check reads it | rejected |
| Values typed into rows, or a scan that writes no file | a shorter file | typed values drift; scanned values are not where a session reads | rejected by the decider |
| Four status words, `uncovered` also for code that differs from its ADR | one word fewer | the experiment's 11 such rows are no question for the decider: a clause already answers | rejected |

How a value is shaped in code:

| Option | For | Against | Outcome |
|---|---|---|---|
| The const's name | about 100 edits; names already mostly fit | a count with an unusual name is missed | chosen by the decider |
| No edits: guess an inline number's meaning from the code around it | no code change | a name is a guess; the first rows read `bootBackoff()` 10s and `bootBackoff()` 5m | rejected |
| A comment on each const | exact; it could carry the meaning | about 260 comments; a forgotten one on a count goes unseen | rejected by the decider |
| One file per package | the file is the marker | about 260 moves; a value leaves the code that uses it | rejected by the decider |
| A type (`limit.Count`) | checked by the compiler | a conversion at each use | rejected by the decider |
| The `mnd` linter on plain inline numbers | sees every one | about 350 hits, mostly arithmetic | not now |

## Decision

1. **The place.** `docs/behavior/`, called the behavior registry (bare "registry" means the OCI registry in the code): an
   index, `README.md`, and one file per chapter. It says what the platform does now and decides nothing. A row that misstates
   an ADR is corrected; where the code differs from the ADR, the row says what the code does, with `differs`.
2. **Chapters**, one per process, in this order: Platform lifecycle, Controller, Admission, Security, Workers, Functions,
   Calls, Services, Eventing, Apps, Disaster recovery, Health, Limits, funcdctl dev. Every package, config section and
   kind has one. A new component joins one or adds one.
3. **A chapter's shape**, in order: its title; a `Kinds:` line; a Mermaid state machine; a short note; one table; the generated
   block. A machine uses the code's state names, and a word the code does not have is marked on the note's `New words:`
   line. A design spec's generated machine is linked, not copied. The Functions chapter's machine replaces the copies in
   `blueprint.md` and `docs/PROJECT-SUMMARY.md`, which link to it; each edge added or dropped against the blueprint's machine
   gets a row with its status and source, so the answers move rows, not the machine.
4. **What the table holds**: a wait of about a second or more; a state, error or refusal a user sees; a cost, risk or
   limit an ADR accepted; a step nothing answers. Internal polls, buffers and log rates stay in the block. One row per
   fact, at most 400 bytes: `Where | What happens | Time | You see | Status | Source`. A row that mixes a decision and a cost
   is two rows. A chapter's written part stays under 16 KiB, about 4k tokens, a chosen number: a session reads one chapter.
5. **Status**, one word per row:

   | Word | It means | Source must name | Bug rule's verdict |
   |---|---|---|---|
   | `decided` | an ADR or the decider chose it | an accepted ADR, or `decider <date>` with its record (`#N` or `card "<title>"`) | `accepted` |
   | `accepted` | an ADR or the decider named it as a cost or a risk, left it out on purpose or rejected its fix; or no clause speaks of it and a test pins it | the same, or the test | `accepted` |
   | `deferred` | a card, an ADR's open question or an open issue holds it | `card "<title>"`, `ADR-NNNN Open questions` or `#N` | `deferred`; an open `kind/bug` is `bug`, already filed |
   | `uncovered` | nothing speaks of it | what was read | `uncovered`; `bug` if no sound design could want it |
   | `differs` | an ADR or the blueprint says otherwise, and the code does this | that clause | `bug` once its two checks hold; not checked yet until then |

   A clause that reaches a case only by its letter, with no sign its author weighed the case, decides nothing: `uncovered`,
   citing it. A cost an ADR accepted, with a card that proposes a change, is `accepted`, citing both. An `uncovered` row is a
   question for the decider, with no fix until answered; it is written only after a read beyond the first hits. The answer
   moves the row, and its PR or issue records it; one that changes an Accepted ADR still takes a superseding ADR. A `differs`
   row goes through the bug rule: an issue moves it to `deferred`; a clause that turns out to decide it, or a superseding ADR
   that keeps the behavior, to `decided`.
6. **The generated block** holds every config key with its default and every package-level value in production Go
   (Contracts), with its file. It holds no line numbers and no totals, so a moved line changes nothing and two PRs do not
   conflict on a count. It is rewritten whole, so `just behavior-values` clears a conflict inside it.
7. **How a value is shaped in code.** A number that changes what the platform does has a name at package level. A
   duration or a size is found by its value, a count by its name:

   ```go
   const heldIdleTimeout = 5 * time.Minute   // a duration: holds a time unit
   const maxSegment = 64 << 20               // a size: holds a shift
   const maxCopyMembers = 1024               // a count: its name passes the name test
   context.WithTimeout(ctx, 30*time.Second)  // refused: name it
   io.LimitReader(r, 1<<20)                  // refused: name it
   parseDuration("sql.callTimeout", raw, 30*time.Second, ...)  // allowed: beside its config key
   ```

   The check refuses what it can see: an inline duration or size. A number beside its config key, a string literal naming
   the key in the same call or composite literal, is that key's hard-coded fallback: it has no row while it equals the
   key's default, and a row `<key> (fallback)` when it differs.
8. **The check.** `just check-behavior` regenerates the blocks in memory and fails on: a block that differs by a byte; an
   inline number; a package, config section or kind with no chapter; a chapter with no
   title, `Kinds:` line, machine or table, or with them out of order; a chapter with no file, a file with no chapter, or a
   chapter the index does not list; a row that breaks `scenario: row-does-not-hold`; a row without six cells or outside the one
   table; a written part over its cap; an ADR numbered above 0313 with no *Behavior rows* section. It runs in `just ci`, and
   in CI's light path when a docs-only PR touches a chapter or an ADR.
9. **The skill.** `.claude/skills/behavior-registry/SKILL.md` runs when a value or a behavior changes, an ADR is accepted
   or superseded, or a new package, kind, config section or provider arrives. For a new component it asks how it starts,
   stops, survives a funcd restart and a crash, is backed up and restored, and hits a limit. It says where to look for
   defaults, hard-coded values, states, errors, who may act, deletes, the order of steps, the unit and the CLI, the why
   (an ADR's Scope, Alternatives, Consequences and Risks, following each `Superseded in part by`, then issues and cards) and
   proof, how to find the ADRs that govern a package, and how to write a row.
10. **The gates.** The template's *Behavior rows* follows *Design model*: the rows an ADR adds, changes or removes, by
    chapter, or `None` and why. Each gate gains one step:
    - draft (`adr`): fill the section;
    - accept (`adr`): rows citing what it wholly supersedes move to it, `differs` until implemented;
    - judge (`adr-judge`): a Decision that changes what a user sees, with `None`, or a row it changes and does not list,
      is a Major;
    - implement (`adr-impl`): the rows land in the implementation PR, with `just behavior-values`;
    - review (`adr-impl-review`): the file's rows match the section, and `just check-behavior` passes;
    - file or fix (`issue-management`, `/fix`, `/fix-review`): filing a `differs` row moves it to `deferred`; a fix that changes
      what a row says, or closes an issue a row cites, updates the row in the same commit.
11. **The bug rule.** Step 2 of *Is it a bug?* starts with a grep of what it saw across the chapters and reads every row it
    hits: the status tells them apart. A row whose cited clause reads as the row says ends the dive, with no issue and no fix;
    a session about to file or fix does the rest of step 2. A dive that finds no row ends by adding one, or, in a gate or
    campaign that edits nothing, by reporting it. An issue's *Design check* gains no line: a row's ADR goes on its `ADRs:`
    line. CLAUDE.md lists the place as a reference that decides nothing, and its propagation table gains a column for it.

## Design model

None: this ADR changes a process and a generated document, with no runtime actors or timers.

## Behavior rows

None: it changes no behavior of the platform. It creates the chapters.

## Temporary workarounds

None: all 14 chapters land together.

## Contracts

````
docs/behavior/<chapter, lower case with dashes>.md:
# <Chapter>
Kinds: <its kinds>.   (or: Kinds: none of its own.)
```mermaid
stateDiagram-v2
```
<a short note>. New words: <each word on the machine the code does not have, on one line>.
| Where | What happens | Time | You see | Status | Source |
<!-- generated values, do not edit: just behavior-values -->
<details> <summary><Chapter>: values</summary> | Value | Default | From | `<key or name>` | <value> | config, or `<file>` | </details>
<!-- end of generated values -->
````

| Command | Does | Exit |
|---|---|---|
| `go run ./scripts/behavior` · `just behavior-values` | rewrite each chapter's block | 1 on a package or section with no chapter |
| `go run ./scripts/behavior -check` · `just check-behavior` | Decision 8, writing nothing | 1 on any failure |

- `internal/platform/config`: `func Defaults() Config`, every key's default before a file or a variable applies. A key
  empty there reads its documented default from `examples/funcdconfig.yaml`.
- `chapters()` in `scripts/behavior/main.go` gives each chapter its packages (the longest prefix wins) and config
  sections; a row may cite a value from any chapter's block. Skipped: tests, generated files, benches, any non-test file that imports `testing` (a driver's contract
  suite), and `e2e`, `examples`, `scripts`, `docs`, `images`, `bench`, `tests`, `internal/testkit`,
  `internal/controlplane/cmd`, `api/cron`, `api/lintfixture`.
- **A value**: a package-level `const` or `var` whose expression holds only numbers, names, units, shifts and
  conversions, and which holds a time unit (a duration), holds a literal shift or `N * 1024` (a size), or has a name that
  passes the name test (a count, or a value derived from another name, shown as its expression). A string holding a number
  with an optional unit (`"256Mi"`, `"30"`) counts when its name passes the test.
- **Name test**, case-insensitive: the name starts with `default`, `max`, `min`, `limit`, `grace`, `timeout`, `interval`,
  `retry`, `retries`, `attempts` or `budget`, or ends with `max`, `limit`, `timeout`, `interval`, `grace`, `wait`,
  `period`, `after`, `budget`, `attempts`, `retries`, `bytes`, `size`, `ttl`, `page`, `batch`, `concurrency`, `conns` or
  `buf`.
- **An inline number**, in a function body: `N * time.Unit`, `N << M` or `N * 1024` with literals, wherever it stands, or
  a bare `time.Second`, `time.Minute` or `time.Hour` passed as an argument, set as a field, assigned, returned or compared.
  A bare unit is allowed in arithmetic (`d / time.Second`), in a conversion, or as the unit given to `Round`, `Truncate`,
  `ceilUnits` or `ceilDiv`. Any number beside its key is allowed.
- **A cited number**: `` `name` N unit ``. A config key or a value is compared, a name only the code holds is not, and any
  other name fails. **A cited word**: a backticked CamelCase word, a backticked capitalized word in *Where* or *You see*, and a
  machine's state, unless marked new, must be a string literal or an identifier in production code, a value or a test. **A
  link**: a relative link must resolve from its file, and a bare `#` link is refused.
- **A cited ADR**: superseded when its Status starts with `Superseded` or it has a `- **Superseded by**` line; accepted
  when its Status starts with `Accepted`, `Reviewing` or `Implemented`.
- **Output**: `docs/behavior/<file>:<line>: <what does not hold>`, or `<file>: <func>() writes the number <expr> inline: give
  it a name at package level`, with `now: <row>` and `was: <row>` for a block that differs. The last line always names
  the skill: ``run the /behavior-registry skill: regenerate with `just behavior-values`, then check that each written row still holds``.

## Implementation plan

The local branch `exp/behavior-registry` holds the tool, its tests, the named numbers, the skill and the 14 chapters. Left: step
1's `ci.yml`, step 2's renames, step 3 but the skill, step 4's edge rows and its questions, step 5.

1. `internal/platform/config/config.go`: `Defaults()`. `scripts/behavior/main.go`, whose run takes the chapters, the keys
   and the kinds as inputs so a test brings its own. `justfile`: `behavior-values`, `check-behavior`, and `check-behavior`
   in `ci`. `.github/workflows/ci.yml`: the `design` filter gains `docs/behavior/**` and `docs/adr/**`; the light path
   runs `just check-hygiene check-specs check-behavior`, with the Go cache; its comments follow.
2. Name the inline numbers, in one commit that changes no value: 102 sites in 42 files, each compared with its literal.
   Rename the plain constants the name test should catch: about six, such as `engineStarts`.
3. `.claude/skills/behavior-registry/SKILL.md`; Decision 10's step in the `adr`, `adr-judge`, `adr-impl`,
   `adr-impl-review`, `fix` and `fix-review` skills, and the section in the `adr` skill's template and partial-supersession
   lists; the bug rule in `.claude/CLAUDE.md` and its mirror `.github/copilot-instructions.md` (step 2, and an open issue in the
   `deferred` verdict), and its restatements: `fix`, `fix-review`, `issue-management`, `.claude/workflows/fix-batch.js`, `shim-fix.js`;
   CLAUDE.md's layer, pipeline and propagation tables, strict rule 5's list, the code conventions and *Before you finish*;
   `docs/method/README.md`; `README.md`; at acceptance, the back-links in ADR-0000 and ADR-0272.
4. The 14 chapters, written with the skill, and a row per edge the Functions chapter adds or drops against the blueprint's
   machine. Each `uncovered` row goes to the decider as a question, each `differs` row through the bug rule's two checks; no fix
   in this PR.
5. Replace the Function machine: `blueprint.md` and `docs/PROJECT-SUMMARY.md` link to the chapter; `.claude/skills/project-summary/summary-shape.md`, that skill's line on copied diagrams, the bug rule's mention of
   the blueprint's state machine and the row at the chapter's `Terminating` follow.
6. Tests in `scripts/behavior/main_test.go`, on small fixture trees: one `TestScenario<Name>` per Scenario, each item of
   Decision 8, the value shapes and the name test. `TestScenarioSurpriseLookedUp` reads the real file and the bug rule.
7. Done when `just ci-full` and CI pass; the lanes run only as the merge queue's rule for the touched paths requires.

## Review checklist

- [ ] `just check-behavior` passes; a Linux run gives the same bytes as a macOS run.
- [ ] Each Scenario has its named test, passing; each fails when the check it tests is removed.
- [ ] The commit that names the numbers changes no value: each new constant equals the literal it replaces.
- [ ] A random 40 rows' citations were re-read, not only found by the check; a docs-only PR with a broken row fails CI's
      light path, run once.
- [ ] `blueprint.md` and `docs/PROJECT-SUMMARY.md` link to the chapter and hold no copy of the machine; each edge the
      blueprint's copy had and the chapter lacks, or the reverse, has a row with its status and source.
- [ ] The six skills carry Decision 10's step; the bug rule and its restatements start at the file; the CLAUDE.md mirror
      is byte-identical.
- [ ] No absolute path or local username in a chapter; no security finding in any row.

## Consequences

Measured on the experiment's text as one file, with a list of 14 past surprises written before its new chapters:

- **(+)** A reader of the registry alone reached the right verdict on 13 of 14, at about 12k tokens each, a saving Decision 11
  lets a session keep; a read that goes on to the ADRs took about 46k, and one with no registry 41k. The miss read "no row",
  which sends a reader on to the issues.
- **(+)** Writing it showed what no list did: 20 questions and 11 places where the code differs from its ADR.
- **(−)** It is a first stop, not a shorter dive: on the same 7 surprises, the registry with the ADRs took 314k tokens and
  none 285k. Both got all 7.
- **(−)** The check proves a citation exists, not what it says, and does not compare a machine with the code. An audit of 40
  random rows found 28 that hold, 12 imprecise, mostly a citation that does not pin the fact, and 0 wrong, which bounds the
  wrong rate near 7%: up to about 18 of the 242 rows no one re-read. A bare `30 s` in a cell is not compared at all.
- **(−)** A first-pass `uncovered` is often wrong: of 32, a second reader kept 16, and moved 11 to `differs` and 3 to
  `decided`; 2 were false. So a question for the decider costs two reads.
- **(−)** A row that cites an issue goes stale when that issue closes, unless the closing fix updates it.
- **(−)** A PR that adds a value also regenerates a block, and a docs-only PR that touches an ADR now runs CI's light path.
- **(−)** Writing the first chapters cost about 7.6 M tokens; a later ADR pays only for its own rows. About 530 generated rows,
  folded, with noise: a name that passes the test and is no limit, and a value both a config default and a named fallback.
- **(−)** Some numbers stay unseen: about 350 plain inline numbers (`mnd`'s count), a count or string with an unusual name,
  and a shim's values, read by hand.

## Open questions

- Generating values from `images/runtime/duckdb/shim.py` and the pinned shims: a later ADR, if a shim's wait surprises
  someone.
- Comparing a machine with the phases and reasons the code writes: a later ADR, if the rows drift.
- Parsing the Mermaid in CI: no parser is in the flake; the 14 machines were checked by hand with Mermaid 11.4.1.

## References

- #1226 (13 of 125 closed bugs were decided behavior), #1392 (the bug rule), #1391 (config keys the rows cite)
- ADR-0272 (generated documents, partial supersession); ADR-0161, ADR-0162, ADR-0185, ADR-0192 (the 30 s case)
- Precedents for a computed document: `docs/PROJECT-SUMMARY.md` with `state.py`, `scripts/specs.py`
- The experiment of 2026-11-04: 11 simulated PRs did what the Scenarios say, in under a second each; each of 10 removed
  checks failed its test; macOS and Linux gave the same output; a conflict inside a block cleared by regenerating
