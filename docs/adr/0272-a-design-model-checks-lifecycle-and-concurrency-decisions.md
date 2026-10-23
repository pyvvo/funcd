# ADR-0272: A design model checks every lifecycle and concurrency decision

- **Status**: Accepted 2026-10-23 by the decider, with the triggers, the state budget, `just ci` running every spec and
  FizzBee's own skills left out; Proposed 2026-10-23, judged three rounds, every Major folded
- **Date**: 2026-10-23
- **Deciders**: green-0-rabbit
- **Tags**: meta, process, design-model, fizzbee, model-checking
- **Realizes**: [FEAT-0001/F185](../feat/0001-feat-v1.1.md) (design models checked by a model checker)
- **Supersedes (in part)**: [ADR-0000](0000-adr-process.md) — its *Template* gains a *Design model* section; its
  *Workflow gates* 2 to 5 gain a design-model step; partial supersession gets a written form (Decision 7)
- **Relates to**: [ADR-0025](0025-testing-strategy-and-e2e-harness.md) (the test taxonomy: a model comes before
  the tests and becomes their oracle) · [ADR-0077](0077-declarative-e2e-venom.md) (a test tool pinned in the flake) ·
  [ADR-0270](0270-derived-s3-key-pairs-rotate.md) (the first spec) · [ADR-0271](0271-the-lake-catalog-is-a-duckdb-file.md)
  and #1217 (the first amendment)

## Context & Need

Fix campaigns keep finding defects in designs, not in code: an order of steps a stop can cut, a timer that drops
what is still in use, two rules that disagree. #1198 (a stop's snapshot taken before an acknowledged commit), #1199
(`replicas` admitted, `minReplicas` not), #1208 (an expired booking dropped while its Function is stored) and
ADR-0270's review round 5 (an unbounded hold lets a key pair expire) each passed review and tests on fakes, then cost
a campaign round. ADR-0271's own fix for #1198 shipped with one more such gap (#1217).

Experiment 0007 (`pyvvo/experiment`, private) wrote each design as a FizzBee spec of 10 to 63 lines without
comments. FizzBee explores every interleaving within a spec's bounds and checks its assertions. It found all four
defects in under a second each, with the issue's story as its trace; it passed each fixed design; liveness caught an
over-correction of #1208; and it found #1217 in ADR-0271 as merged, later proven on the real engine in 3 runs of 3.
With FizzBee's default of two concurrent actions, #1217 stayed hidden: the bounds decide what is checked.

A *design model* is such a spec: a small executable description of a decision's actors, steps and faults, with the
properties the decision promises. The decider and the judge see every interleaving before any code exists, and its
assertions become the oracles of the implementation's tests. AWS model-checks its protocols at design time and uses
the model as the later test oracle (Brooker and Desai, CACM 2025).

ADR-0185, 0192, 0227 and 0249 already replace parts of Accepted or Implemented ADRs (`Supersedes (in part)`, with a
`Superseded in part by` back-link), but no rule describes it: each restates a whole template, and CLAUDE.md allows
only `Superseded by`. #1217 needs a one-item change of ADR-0271.

## Scenarios

- `scenario: specs-checked`: Given the specs under `docs/design/`, When `just check-specs` runs, Then each spec
  passes within its bounds and the state budget, each mutant fails with the assertion it names, each diagram equals
  its regeneration, each oracle marker names an assertion that exists, and the run prints each spec's verdict, states
  and seconds and exits 0.
- `scenario: broken-assertion`: Given a spec edit that lets the design break an assertion, When `just check-specs`
  runs, Then it exits non-zero and prints the assertion and FizzBee's trace.
- `scenario: toothless-mutant`: Given a mutant that no longer fails, When `just check-specs` runs, Then it exits
  non-zero naming the mutant: the guard it skips protects nothing the spec asserts.
- `scenario: diagram-drift`: Given a spec edit that changes its states or transitions, When its diagram was not
  regenerated, Then `just check-specs` exits non-zero naming the diagram, and `just spec-diagrams` rewrites it.

## Scope

**In**: when an ADR needs a design model, the spec and its checks, the generated diagram, the pinned tool, the steps
the ADR and fix gates add, the written form of a partial supersession, the `fizzbee` skill. **Out**: model-based
testing of Go code against a spec (FizzBee's MBT; a later ADR); performance models; specs for every existing design
(an ADR adds its part's spec when it changes that part).

## Constraints & Decision drivers

- A spec must be quick to write and to read, by the decider and by agents; a check must take seconds.
- Apache-2.0/MIT-compatible tools only, pinned in the flake (CLAUDE.md; Venom's precedent, ADR-0077).
- Generated artifacts are regenerated, never hand-edited (CLAUDE.md, strict rule 5).
- An Accepted or Implemented ADR stays frozen; only its forward status moves and a back-link may touch it.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| FizzBee | Python-like specs; crash at yield points, message loss, roles, liveness with fairness; found 4 of 4 known defects and #1217 | young (v0.5.4), one main maintainer; not in nixpkgs; rough edges (Consequences) | chosen |
| TLA+ / PlusCal (TLC) | the reference, mature, wide use | a mathematical notation slower to write and review; a Java toolchain | rejected |
| P (p-org/P) | state machines and runtime monitors, used at AWS | a .NET toolchain; heavier for 20-line specs | rejected |
| Quint | TLA+'s semantics in a typed syntax | still TLA+'s model of state; a Node and JVM toolchain | rejected |
| Property-based tests only (`rapid`, approved) | checks the real code | needs the code first; explores one implementation, not the design | kept for code, not instead |
| Review and tests on fakes (today) | no new tool | missed the five defects above | rejected |

## Decision

1. **When.** An ADR needs a design model when its Decision adds or changes any of these; otherwise its Design model
   section reads `None` and says why (a rename, an API shape, a format, a library choice, or a modeled mechanism
   reused unchanged, whose spec it links):

   | Trigger | funcd examples |
   |---|---|
   | steps a stop, crash or timeout can cut | the Lake stop (#1198, #1217); a restore's order; upgrade hooks |
   | two or more actors racing on the same state | a reconcile pass against calls and timers: drain, hand-out, scale-to-zero wake against reclaim, key renewal |
   | time rules: TTL, lease, period, retry, backoff | the booking TTL (#1208); key renewal (ADR-0270); the lane lock; crash-loop backoff |
   | reservations of a limited capacity | volume booking, reservation and reclaim |
   | delivery and retries of events or calls | eventing, workflow steps and their dead letters |
   | conditional writes | the S3 gateway's `If-None-Match` |
   | two rules that must agree | admission against the reconciler (#1199) |

2. **The spec.** A FizzBee spec of the design, not of the code, at `docs/design/<part>/<topic>.fizz`, where `<part>`
   is the platform part in lowercase (`function`, `lake`, `volume`, `eventing`); it lives on: each ADR that changes
   the design updates it. Assertions state what the decision promises: `always`, `transition` or `exists`; a wait or
   retry with no bound in modeled time also gets a progress assertion, `always eventually` or `eventually always`,
   with `fair` actions (v0.5.4 has no bare `eventually`), while a wait a modeled clock bounds is checked by a safety
   assertion on that clock. Faults are modeled where the design meets them: `crash_on_yield`, message loss, a timeout
   as a guarded action. The front matter always sets `max_actions` and `max_concurrent_actions` to the smallest
   scenario in which every mutant fails: as many concurrent actions as actors racing through yield points (a spec
   whose actions are all atomic says so). A run explores at most 100,000 unique states, a count, so the check does not
   depend on the host; a wider run, one more item than the race needs, is recorded in the ADR, not in CI. Comment
   lines drive the checks; each spec has at least one `# diagram:` line:
   - `# diagram: <var>[, <var>]` — the variables the state machine follows: globals, or one role's `<Role>.<field>`.
   - `# mutant <name>: <assertion>` — run with `MUTANT = "<name>"` (FizzBee's `--preinit-hook`), the spec, which
     declares `MUTANT = ""` and skips one guard when it matches, must fail `<assertion>` (or `liveness`).
3. **The diagram.** `<topic>.md` beside the spec holds one Mermaid state machine per `# diagram:` line, computed from
   the states FizzBee explored: each state maps to the named variables' values; each transition those values see is an
   edge labeled with the action that takes it; a self-loop marks an action that runs whole where it starts (in one
   step, or after its choices) and leaves the values as they were; the initial states and the true ends (not the
   bound's) are marked. FizzBee v0.5.4 draws none: it writes Graphviz files, and its web explorer draws one trace as a
   sequence diagram.
4. **The tool.** FizzBee v0.5.4 (Apache-2.0) is pinned in `flake.nix` by the release digests GitHub publishes. The
   dev shell keeps its Go checker and its pure-Python parser (10 MB; the Linux tarball also bundles a 500 MB Python)
   behind a `fizz` command that parses on the dev shell's Python. `scripts/specs.py` (stdlib only) runs every spec in a
   scratch directory; `just check-specs` runs it and `just ci` includes it; CI's fast lane also runs when
   `docs/design/**` changes. A FizzBee upgrade is a pin change whose PR reruns every spec. FizzBee's own Claude skills
   (`fizz install-skills`) are not installed: they fetch its unpinned main branch.
5. **The gates.** The four ADR gates and the fix pipeline each gain one step:
   - draft (`adr`): check the triggers; write or update the part's spec; run `just check-specs`; fill the section;
   - judge (`adr-judge`): rerun the spec; a trigger with no model is a Blocker; an assertion that protects no
     Scenario at risk, a guard the Decision adds with no mutant, bounds not justified as in Decision 2, or a wait or
     retry with no bound in modeled time and no progress assertion is a Major;
   - implement (`adr-impl`): each assertion's oracle test, named in the section, carries the comment
     `oracle: docs/design/<part>/<topic>.fizz#<Assertion>`;
   - review (`adr-impl-review`): each assertion of a spec the ADR changed has an oracle marker, and `just check-specs`
     passes;
   - fix (`/fix`, `/fix-review`): a fix that changes a modeled behavior updates the spec in the same commit, and the
     review checks it.
6. **The section.** The template's *Design model* follows *Decision*: the spec and diagram links; a table of the
   assertions, their kind, the Scenario each protects and its oracle test; the bounds and why; the faults modeled;
   the mutants; the result (verdict, states, seconds, FizzBee version) and any wider run; what is left to tests.
7. **Partial supersession** (an amendment). An ADR may replace named parts, numbered Decision items or named
   subsections, of one Accepted or Implemented ADR. Its header names them in `Supersedes (in part)`; its title may say
   `(amends ADR-XXXX)`. It carries Context, Scenarios (new or changed), Alternatives considered, Decision (the
   replacing parts, numbered as in the parent), Design model, Contracts (the delta), Implementation plan, Review
   checklist, Consequences and References; Temporary workarounds and Open questions read `None` or carry the change;
   Scope and Constraints may read `Unchanged (ADR-XXXX)`. It has its own feat row and passes every gate. At its
   acceptance the parent gains, below its Status line, the header line that 143 partly superseded ADRs already
   carry, `- **Superseded in part by**: [ADR-NNNN](<file>) (<date>) — <parts>`; with the forward status moves, the
   only touch an Accepted or Implemented ADR takes.
8. **The skill.** `.claude/skills/fizzbee/SKILL.md`: the triggers; a spec cookbook (durable and ephemeral state, waits
   as guarded actions, time as a fair tick, bounds, mutants, liveness controls; crash and message-loss recipes from
   FizzBee's documentation, not yet tried on a funcd spec); FizzBee's patterns mapped to the triggers (lease,
   compare-and-swap, saga, queue, two-phase commit); the limits of v0.5.4 with their workarounds; the commands. The
   `adr`, `adr-judge`, `adr-impl`, `adr-impl-review`, `fix` and `fix-review` skills point to it.

## Design model

None: this ADR changes a process, with no runtime actors or timers.

## Temporary workarounds

None.

## Contracts

```
docs/design/<part>/<topic>.fizz      a FizzBee spec; front matter sets max_actions and max_concurrent_actions
  # diagram: Worker.phase            one state machine per line, at least one
  # mutant unbounded-hold: NoWorkerServesAnExpiredPair
  MUTANT = ""
docs/design/<part>/<topic>.md        generated by scripts/specs.py; never edited
// oracle: docs/design/<part>/<topic>.fizz#<Assertion>   (above the oracle test, in Go, Python or TypeScript)
```

| Command | Does | Exit |
|---|---|---|
| `fizz [flags] <spec.fizz>` | parse the spec into `<spec>.json` beside it, then model-check it | FizzBee's |
| `scripts/specs.py check [spec...]` | the checks of `scenario: specs-checked` over the given specs, or all of `docs/design` | 1 on any miss |
| `scripts/specs.py diagrams [spec...]` | rewrite each spec's `.md` | 1 when a spec does not pass |
| `just check-specs` · `just spec-diagrams` | `specs.py check` · `specs.py diagrams` over `docs/design` | as above |

`FIZZ=<path>` overrides the `fizz` that `specs.py` runs; `SPECS_MAX_STATES` the budget. Output lines:
`<spec>: pass (<n> states, <s> s)` · `<spec>: FAILS <assertion|liveness|deadlock|error> (<s> s)` and the trace ·
`<spec> mutant <name>: fails <assertion>` · `<spec> mutant <name>: PASSES, so the guard it skips protects nothing
<spec> asserts` · `<spec> mutant <name>: fails <got>, not <expected>` · `<spec>: names mutants but declares no MUTANT
= ""` · `<spec>: sets no <bound>; a spec names its bounds` · `<spec>: over the <n>-state budget; …` · `<spec>: names
no variable to draw (# diagram: <var>)` · ``<topic>.md: current`` or ``STALE, run `just spec-diagrams` `` ·
`<spec>#<Assertion>: no oracle test yet` (a note) · `<file>:<line>: oracle marker names no assertion
<spec>#<Assertion>` · `<n> specs, <m> failing[: <specs>]`.

## Implementation plan

1. `flake.nix`: the `fizzbee` package, in `packages` and the dev shell; `scripts/specs.py`; `justfile`:
   `check-specs`, `spec-diagrams`, and `check-specs` in `ci`; `.github/workflows/ci.yml`: a `design` filter
   (`docs/design/**`) that also starts the fast lane; `.gitignore`: what `fizz` writes beside a spec run in place.
2. The first spec: ADR-0270's bounded hold, `docs/design/function/key-renewal.fizz`, with review round 5's unbounded
   hold as its mutant, its diagram, and the oracle marker on `TestScenarioBrokenReplicaDoesNotBlockRenewal`.
3. `.claude/skills/fizzbee/SKILL.md`; the steps of Decision 5 in the six skills and the template section of Decision 6
   in the `adr` skill; `.claude/CLAUDE.md` (the pipeline tables, Decision 7's back-link); `docs/method/README.md`; at
   acceptance, ADR-0000's back-link.
4. Tests: `scripts/specs_test.go` runs the dev shell's FizzBee on `scripts/testdata/specs/gate.fizz` (a gate that opens
   once): `TestScenarioSpecsChecked`, `TestScenarioBrokenAssertion`, `TestScenarioToothlessMutant`,
   `TestScenarioDiagramDrift`, and the diagram's exact text, a spec that names no bounds, the state budget, a mutant
   that fails another assertion, and a dangling oracle marker.
5. Done when `just ci-full` and CI pass.

## Review checklist

- [ ] Each `docs/design` spec has its diagram and its oracle markers, and `just check-specs` passes (every marker
  names an existing assertion).
- [ ] Each mutant line names a guard the spec skips under `MUTANT`, and its run fails the named assertion.
- [ ] The scenario tests pass, and fail when `specs.py` skips the mutant, diagram or bounds check.
- [ ] The six skills carry Decision 5's step; the `adr` skill carries Decision 6's section and Decision 7's form.
- [ ] FizzBee comes only from the flake, by its published digest.

## Consequences

- **(+)** Design defects of the kinds above surface at the judge gate, with a trace, before any code.
- **(+)** Each assertion is the oracle of a named test; each diagram shows the design at a glance.
- **(−)** A triggered ADR costs a spec: tens of lines and up to an hour of drafting.
- **(−)** A spec checks only what it models; facts of the code or the platform (two SQLite libraries blind to each
  other's locks, ext4's overhead) stay with the tests.
- **(−)** The Linux release tarball is 352 MB; the Nix store fetches it once per pin change.
- **(−)** FizzBee is young. Experiment 0007 met two limits of v0.5.4: a top-level `func` is undefined inside an
  `if`, and an intended end reads as a deadlock unless `deadlock_detection: false`. Writing the first specs met six
  more: `x.field = oneof [...]` assigns nothing; `oneof` inside a literal does not parse; one step creating several
  roles panics; unknown front-matter keys are refused; a step that only reads makes no interleaving point; and a call
  to another role's function is a message FizzBee may lose (its documented model). The skill gives each workaround.

## Open questions

- Checking Go code against a spec (FizzBee's model-based testing): a later ADR, once a part has a stable spec.

## References

- [FizzBee](https://fizzbee.io/) and [fizzbee-io/fizzbee](https://github.com/fizzbee-io/fizzbee) v0.5.4 (Apache-2.0):
  the language, its fault model, `proto/graph.proto` (the state graph the diagram reads), its design patterns
- M. Brooker and A. Desai, [Systems Correctness Practices at Amazon Web Services](https://cacm.acm.org/practice/systems-correctness-practices-at-amazon-web-services/), CACM, 2025
- Experiment 0007 (`pyvvo/experiment`, private): `0007-fizzbee-design-bugs`, its specs, results and diagram script
- #1198, #1199, #1208, #1217; ADR-0270's review record (round 5)
