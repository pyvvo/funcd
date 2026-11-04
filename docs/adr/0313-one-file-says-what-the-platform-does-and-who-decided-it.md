# ADR-0313: One file says what the platform does and who decided it

- **Status**: Proposed 2026-11-04, judged two rounds, every Major folded
- **Date**: 2026-11-04
- **Deciders**: green-0-rabbit
- **Tags**: meta, process, behavior-registry, documentation
- **Realizes**: [FEAT-0001/F227](../feat/0001-feat-v1.1.md) (one place that says what the platform does)
- **Supersedes (in part)**: [ADR-0000](0000-adr-process.md) — its *Template* gains a *Behavior rows* section; its
  *Workflow gates* 2 to 5 gain a behavior-row step · [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md)
  — what an amendment carries (its Decision 7) gains *Behavior rows*
- **Relates to**: ADR-0272 (the precedent: a generated document a check keeps true; the specs' machines this file links)
  · [ADR-0002](0002-source-code-conventions-and-patterns.md) (the code conventions Decision 7 adds to)

## Context & Need

A session that meets a surprising wait has nowhere to look up whether it was decided. The answer is spread over 313
ADRs, the open issues, the board's cards, about 170 config keys and about 330 hard-coded values. The bug rule (#1392)
makes a session read them before it writes "bug", because 13 of 125 closed `kind/bug` issues were decided behavior
(#1226), but that read has no index, so each surprise costs a long dive. On 2026-11-04, a call held 30 s and refused
while a Lake came back took four ADRs and a code read to explain: ADR-0161 chose the hold; ADR-0162, ADR-0185 and
ADR-0192 accepted the `Pending` under a Lake gate; and one case, a gate clearing under a held call with `replicas: 0`,
nothing speaks of.

**Purpose.** One file that says, per process, what happens, how long it takes, what a user sees and who decided it. A
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
- `scenario: new-component`: Given a new package, config section or kind that no chapter names, or a written chapter
  with no machine or no table, When the check runs, Then it fails naming it.
- `scenario: row-does-not-hold`: Given a row cites an ADR that is missing, wholly superseded or not accepted, a missing
  test or status reason, or `` `name` N unit `` with a name that does not exist or a number that differs, or its status
  breaks Decision 5, When the check runs, Then it fails naming the line.
- `scenario: parallel-prs`: Given two PRs each add a value to one chapter's block, When they merge, Then no hand edit is
  needed: the rows merge cleanly, or `just behavior-values` clears a conflict inside the block.
- `scenario: surprise-looked-up`: Given a session sees a 503 after 30 s on a Function, When it follows the bug rule,
  Then step 2 sends it first to the Functions chapter, where a row names `invoke.activationTimeout`, its status and what
  to read next.
- `scenario: adr-states-its-rows`: Given an ADR numbered above 0313 with no *Behavior rows* section, When the check
  runs, Then it fails naming the file.

## Scope

**In**: the file, its generated blocks, how a value is shaped in code, the check, the `/behavior-registry` skill, the gate
steps, the bug rule's first read, the Function machine's home. **Out**: deciding or fixing anything a row shows (an `uncovered` row is a question answered outside this ADR);
every error message (a row cites a kind or a reason where it needs one); comparing a machine or a row's meaning with the
code, and generating values from a shim (Open questions).

## Constraints & Decision drivers

- The decider's picks: one `.md` with chapters by process, a Mermaid machine first, only what matters in the written part,
  values generated from the config and the code, no data file, a skill that says where to look, values shaped by name.
- No new dependency. The check runs in `just ci` in seconds, with the same result on macOS and Linux: it parses every
  file whatever its build tags, and a generated row holds no host-derived value and only project-root-relative paths.
- Generated text is regenerated, never hand-edited (CLAUDE.md, strict rule 5).
- A possible vulnerability never becomes a row: it stays in its private advisory (CLAUDE.md, *Issues*).

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Flows: a machine and rows per process | a step nothing answers shows as a gap, as the 30 s case did | written rows can go stale | chosen by the decider |
| Values only | all generated, nothing stale | no rule and no gap: the 30 s is one row of about 490 | kept as the folded blocks, not alone |
| One row per rule, no flow | easy to search | a gap appears only if someone thought to write it | rejected |
| A folder, one file per chapter | short files | the decider: "one .md is enough" | rejected by the decider |
| YAML or JSON rows and a renderer | parseable | one more source; the table holds the same fields and the check reads it | rejected |
| Values typed into rows, or a scan that writes no file | a shorter file | typed values drift; scanned values are not where a session reads | rejected by the decider |
| A skill with no check | no CI step | relies on someone remembering to run it | rejected |
| Line numbers or totals in a block | a jump to the line | a moved line fails a PR; two PRs always conflict on a total | rejected |

How a value is shaped in code:

| Option | For | Against | Outcome |
|---|---|---|---|
| The const's name | about 100 edits; names already mostly fit | a count with an unusual name is missed | chosen by the decider |
| No edits: guess an inline number's meaning from the code around it | no code change | a name is a guess; the first rows read `bootBackoff()` 10s and `bootBackoff()` 5m | rejected |
| A comment on each const | exact; it could carry the meaning | about 260 comments; a forgotten one on a count goes unseen | rejected by the decider |
| One file per package | the file is the marker | about 260 moves; a value leaves the code that uses it | rejected by the decider |
| A type (`limit.Count`) | checked by the compiler | a conversion at each use | rejected by the decider |
| Every value a config key | one source, read by reflection | about 300 new keys threaded through every package | rejected |
| The `mnd` linter on plain inline numbers | sees every one | about 350 hits, mostly arithmetic | not now |

## Decision

1. **The file.** `docs/BEHAVIOR.md`, called the behavior registry (bare "registry" means the OCI registry in the code).
   It says what the platform does now and decides nothing. If it and an ADR disagree, the ADR wins and the file is
   corrected; code that differs from both is a surprise for the bug rule.
2. **Chapters**, one per process, in this order: Platform lifecycle, Controller, Admission, Security, Workers, Functions,
   Calls, Services, Eventing, Apps, Disaster recovery, Health, Limits, funcdctl dev. Every package, config section and
   kind has one. A new component joins one or adds one.
3. **A chapter's shape**, in order: a `Kinds:` line; a Mermaid state machine; a short note; one table; the generated
   block. A machine uses the code's state names, and a word the code does not have is marked new in the note. A design
   spec's generated machine is linked, not copied. The Functions chapter's machine replaces the copies in `blueprint.md`
   and `docs/PROJECT-SUMMARY.md`, which link to it. That comes last, after the decider's answers on its edges: each edge
   added or dropped against the blueprint's machine gets a row with its status and source.
4. **What the table holds**: a wait of about a second or more; a state, error or refusal a user sees; a cost, risk or
   limit an ADR accepted; a step nothing answers. Internal polls, buffers and log rates stay in the block. One row per
   step: `Where | What happens | Time | You see | Status | Source`. The written part stays under 800 lines, a chosen
   number.
5. **Status**, one word per row:

   | Word | It means | Source must name | Bug rule's verdict |
   |---|---|---|---|
   | `decided` | an ADR or the decider chose it | an accepted ADR, or `decider <date>` with its record (`#N` or `card "<title>"`) | `accepted` |
   | `accepted` | an ADR or the decider named it as a cost or a risk | the same | `accepted` |
   | `deferred` | a card, an ADR's open question or an open issue holds it | `card "<title>"`, `ADR-NNNN Open questions` or `#N` | `deferred`; an open `kind/bug` is `bug`, already filed |
   | `uncovered` | nothing speaks of it | what was read | `uncovered` |

   An `uncovered` row is a question for the decider, with no fix until answered. The answer moves the row, and its PR or
   issue records it; an answer that changes an Accepted ADR still takes a superseding ADR. A row points at what to read
   and proves nothing alone, so a session still reads what it cites.
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
8. **The check.** `just check-behavior` regenerates the blocks in memory and fails on: a block that differs; an inline
   number; a package, config section or kind with no chapter, or a heading with no entry; a written chapter with no
   `Kinds:` line, machine or table; a row that breaks `scenario: row-does-not-hold`; a row without six cells; the written
   part over its cap; a chapter still `Not written yet`, once all are written; an ADR numbered above 0313 with no
   *Behavior rows* section. It runs in `just ci`, and in CI's light path when a docs-only PR touches the file or an ADR.
9. **The skill.** `.claude/skills/behavior-registry/SKILL.md` runs when a value or a behavior changes, an ADR is accepted
   or superseded, or a new package, kind, config section or provider arrives. For a new component it asks how it starts,
   stops, survives a funcd restart and a crash, is backed up and restored, and hits a limit. It says where to look:
   defaults in `internal/platform/config/config.go` and `examples/funcdconfig.yaml`; hard-coded values in the blocks, and
   by hand in `images/runtime/duckdb/shim.py` and the pinned shims; states in `api/types/v1alpha1`, each reconciler's
   reasons and `docs/design/**`; errors in `api/fault` and each step's message; the order of steps in `Run` and `Shutdown`
   of `pkg/funcd/funcd.go`, `pkg/funcd/providers.go` and `internal/lifecycle`; the why in an ADR's Scope, Alternatives,
   Consequences and Risks, following each `Superseded in part by`, then issues and board cards; proof in `TestScenario*`
   and `e2e/*.venom.yml`.
10. **The gates.** The template's *Behavior rows* follows *Design model*: the rows an ADR adds, changes or removes, by
    chapter, or `None` and why. Each gate gains one step:
    - draft (`adr`): fill the section;
    - judge (`adr-judge`): a Decision that changes what a user sees, with `None`, or a row it changes and does not list,
      is a Major;
    - implement (`adr-impl`): the rows land in the implementation PR, with `just behavior-values`;
    - review (`adr-impl-review`): the file's rows match the section, and `just check-behavior` passes;
    - fix (`/fix`, `/fix-review`): a fix that changes what a row says, or closes an issue a row cites, updates the row in
      the same commit.
11. **The bug rule.** Step 2 of *Is it a bug?* starts with the process's chapter. A dive that finds no row ends by adding
    one, or, in a gate or campaign that edits nothing, by reporting it. An issue's *Design check* gains no line: a row's
    ADR goes on its `ADRs:` line. CLAUDE.md lists the file as a reference that decides nothing, and its propagation table
    gains a column for it.

## Design model

None: this ADR changes a process and a generated document, with no runtime actors or timers.

## Behavior rows

None: it changes no behavior of the platform. It creates the file.

## Temporary workarounds

A chapter may read `Not written yet` until the implementation's chapters step. Exit: the last chapter is written and the
check then refuses the phrase, before the review gate stamps `Implemented`.

## Contracts

````
## <Chapter>
Kinds: <its kinds>.   (or: Kinds: none of its own.)
```mermaid
stateDiagram-v2
```
| Where | What happens | Time | You see | Status | Source |
<!-- generated values, do not edit: just behavior-values -->
<details>
<summary><Chapter>: values</summary>

| Value | Default | From |
| `<key or name>` | <value> | config, or `<file>` |

</details>
<!-- end of generated values -->
````

| Command | Does | Exit |
|---|---|---|
| `go run ./scripts/behavior` · `just behavior-values` | rewrite each chapter's block | 1 on a package or section with no chapter |
| `go run ./scripts/behavior -check` · `just check-behavior` | Decision 8, writing nothing | 1 on any failure |

- `internal/platform/config`: `func Defaults() Config`, every key's default before a file or a variable applies. A key
  empty there reads its documented default from `examples/funcdconfig.yaml`.
- `chapters()` in `scripts/behavior/main.go` gives each chapter its packages (the longest prefix wins) and config
  sections. Skipped: tests, generated files, benches, any non-test file that imports `testing` (a driver's contract
  suite), and `e2e`, `examples`, `scripts`, `docs`, `images`, `bench`, `tests`, `internal/testkit`,
  `internal/controlplane/cmd`, `api/cron`, `api/lintfixture`.
- **A value**: a package-level `const` or `var` whose expression holds only numbers, names, units, shifts and
  conversions, and which holds a time unit (a duration), holds a literal shift (a size), or has a name that passes the
  name test (a count, or a value derived from another name, shown as its expression).
- **Name test**, case-insensitive: the name starts with `default`, `max`, `min`, `limit`, `grace`, `timeout`, `interval`,
  `retry`, `retries`, `attempts` or `budget`, or ends with `max`, `limit`, `timeout`, `interval`, `grace`, `wait`,
  `period`, `budget`, `attempts`, `retries`, `bytes`, `size`, `ttl`, `page`, `batch`, `concurrency`, `conns` or `buf`.
- **An inline number**, in a function body: `N * time.Unit` or `N << M` with literals, wherever it stands, or a bare
  `time.Second`, `time.Minute` or `time.Hour` passed as an argument, set as a field, assigned, returned or compared. A
  bare unit is allowed in arithmetic (`d / time.Second`), in a conversion, or as the unit given to `Round`, `Truncate`,
  `ceilUnits` or `ceilDiv`. Any number beside its key is allowed.
- **A cited number**: `` `name` N unit ``, where the name is a config key or a value. The check compares this form only.
- **A cited ADR**: superseded when its Status starts with `Superseded` or it has a `- **Superseded by**` line; accepted
  when its Status starts with `Accepted`, `Reviewing` or `Implemented`.
- **Output**: `docs/BEHAVIOR.md:<line>: <what does not hold>`, or `<file>: <func>() writes the number <expr> inline: give
  it a name at package level`, with `now: <row>` and `was: <row>` for a block that differs. The last line is always
  ``run the /behavior-registry skill: regenerate with `just behavior-values`, then check that each written row still
  holds``.

## Implementation plan

1. `internal/platform/config/config.go`: `Defaults()`. `scripts/behavior/main.go`, whose run takes the chapters, the keys
   and the kinds as inputs so a test brings its own. `justfile`: `behavior-values`, `check-behavior`, and `check-behavior`
   in `ci`. `.github/workflows/ci.yml`: the `design` filter gains `docs/BEHAVIOR.md` and `docs/adr/**`; the light path
   runs `just check-hygiene check-specs check-behavior`, with the Go cache; its comments follow.
2. Name the inline numbers, in one commit that changes no value: today 53 `N * time.Unit`, about 15 bare units and 34
   literal shifts, in 40 files. Rename the plain constants the name test should catch: about six, such as `engineStarts`; most of
   the others are ports, flags, lengths and format versions. The check starts refusing an inline number in this commit.
3. `.claude/skills/behavior-registry/SKILL.md`; Decision 10's step in the `adr`, `adr-judge`, `adr-impl`,
   `adr-impl-review`, `fix` and `fix-review` skills, and the section in the `adr` skill's template and partial-supersession
   lists; the bug rule in `.claude/CLAUDE.md` and its mirror `.github/copilot-instructions.md` (step 2, and an open issue in the
   `deferred` verdict), and where it is restated:
   the `fix-batch` and `issue-management` skills, `.claude/workflows/fix-batch.js`, `.claude/workflows/shim-fix.js`;
   CLAUDE.md's layer, pipeline and propagation tables, strict rule 5's list, the code conventions and *Before you finish*;
   `docs/method/README.md`; `README.md`; at acceptance, the back-links in ADR-0000 and ADR-0272.
4. Write the 14 chapters with the skill, marking each word the code does not have. Each `uncovered` row goes to the
   decider as a question, with no fix. Then the check refuses `Not written yet`.
5. After the decider's answers, replace the Function machine: `blueprint.md` and `docs/PROJECT-SUMMARY.md` link to the
   chapter; `.claude/skills/project-summary/summary-shape.md`, that skill's line on copied diagrams, the bug rule's mention of
   the blueprint's state machine and the row at the chapter's `Terminating` follow.
6. Tests in `scripts/behavior/main_test.go`, on small fixture trees: one `TestScenario<Name>` per Scenario, each item of
   Decision 8, the value shapes and the name test. `TestScenarioSurpriseLookedUp` reads the real file, and that the bug
   rule names it.
7. Done when `just ci-full` and CI pass; the lanes run only as the merge queue's rule for the touched paths requires.

## Review checklist

- [ ] `just check-behavior` passes; a Linux run gives the same bytes as a macOS run.
- [ ] Each Scenario has its named test, passing; each fails when the check it tests is removed.
- [ ] The commit that names the numbers changes no value: each new constant equals the literal it replaces.
- [ ] Every chapter has its machine and table, no `Not written yet` remains, each word the code does not have is marked
      new, and each row's citations were read, not only found by the check.
- [ ] `blueprint.md` and `docs/PROJECT-SUMMARY.md` link to the chapter and hold no copy of the machine; each edge the
      blueprint's copy had and the chapter lacks, or the reverse, has a row with its status and source.
- [ ] The six skills carry Decision 10's step; the bug rule and its restatements start at the file; the CLAUDE.md mirror
      is byte-identical.
- [ ] No absolute path or local username in the file; no security finding in any row.

## Consequences

- **(+)** A session finds in one read whether a behavior was decided and what to read next.
- **(+)** Drawing three chapters showed what no list did, none of it checked yet: six edges the blueprint's Function
  machine lacks, a `Terminating` phase no production code writes, a gate clearing under a held call.
- **(−)** The check proves a citation exists, not what it says, and does not compare a machine with the code. A wrong
  row citing a real ADR passes until someone reads it, and a bare `30 s` in a cell is not compared at all.
- **(−)** A row that cites an issue goes stale when that issue closes, unless the closing fix updates it: the check cannot
  see GitHub.
- **(−)** A PR that adds a value also regenerates a block: one more step, which the check names. Naming the numbers is a
  wide diff with no behavior change. A docs-only PR that touches an ADR now runs CI's light path.
- **(−)** About 490 generated rows, folded, with noise: a name that passes the test and is no limit. A value defined as
  both a config default and a named fallback shows twice.
- **(−)** Some numbers stay unseen: about 350 plain numbers written inline (`mnd`'s count on production code), a number
  held in a string (`heldRetryAfter = "30"`), a count with an unusual name, and a shim's values, read by hand. A wait or
  limit hidden there is found by reading the code.

## Open questions

- Generating values from `images/runtime/duckdb/shim.py` and the pinned shims: a later ADR, if a shim's wait surprises
  someone.
- Comparing a machine with the phases and reasons the code writes: a later ADR, if the rows drift.
- Parsing the Mermaid in CI: no parser is in the flake, and the first three machines were checked by hand with Mermaid
  11.4.1. A pinned tool, if a broken machine lands.

## References

- #1226 (13 of 125 closed bugs were decided behavior), #1392 (the bug rule), #1391 (config keys the rows cite)
- ADR-0272 (generated documents, partial supersession); ADR-0161, ADR-0162, ADR-0185, ADR-0192 (the 30 s case)
- Precedents for a computed document: `docs/PROJECT-SUMMARY.md` with `state.py`, `v1-plan.json` with `plan_waves.py`,
  `scripts/specs.py`
- A prototype on 2026-11-04: about 1 s per run; 17 changes each failed the check and named the skill; a moved line and a
  function with no value passed. Its inline-number refusal is not built yet: those counts come from a scan of the code
