---
name: fizzbee
description: Write, check and read the FizzBee design model of a funcd decision (ADR-0272) — a spec under docs/design that explores every interleaving of a design before code exists, with mutant controls and a generated Mermaid state machine. Use when an ADR adds or changes a lifecycle, a stop or crash path, racing actors, a timer/TTL/lease/retry, a capacity reservation, event or call delivery, a conditional write, or two rules that must agree; when the judge reruns a spec; when a fix changes a modeled behavior; or when asked to "model", "spec", "model-check" or "draw the state machine of" a part of the platform. Not for API shapes, names, formats or library choices.
---

# FizzBee design models

A design model is a FizzBee spec of a decision's actors, steps and faults, with the properties the decision promises.
FizzBee explores every interleaving within the spec's bounds and prints the run that breaks a property. The rules are
ADR-0272's; this skill is how to apply them. Run everything through the dev shell (`scripts/agent/d …`): the flake
pins FizzBee v0.5.4 and puts `fizz` on the PATH.

## When an ADR needs one

When its Decision adds or changes any of these (otherwise its *Design model* section reads `None` and says why):

| Trigger | funcd examples | First assertion to write |
|---|---|---|
| steps a stop, crash or timeout can cut | Lake stop (#1198, #1217), restore order, upgrade hooks | nothing acknowledged is lost at the end |
| two or more actors racing on the same state | drain against hand-out, scale-to-zero wake against reclaim, renewal | one owner/writer; no call to a retired worker |
| time rules: TTL, lease, period, retry, backoff | booking TTL (#1208), key renewal (ADR-0270), lane lock | nothing expires while in use; cleanup eventually happens |
| reservations of a limited capacity | volume booking, reservation, reclaim | admitted means it gets its space; freed space returns |
| delivery and retries of events or calls | eventing, workflow steps and dead letters | no event lost; a retry applies once |
| conditional writes | the S3 gateway's `If-None-Match` | exactly one winner per key |
| two rules that must agree | admission against the reconciler (#1199) | whatever admission accepts, the reconciler can honor |

A modeled mechanism reused unchanged needs no new model: link its spec.

## Write the spec

`docs/design/<part>/<topic>.fizz`, `<part>` the platform part in lowercase (`function`, `lake`, `volume`,
`eventing`). Model the **design**, not the code: one action per step the design names, one assertion per promise.
The first spec, `docs/design/function/key-renewal.fizz`, shows every convention below.

```python
---
deadlock_detection: false          # only when an end with no action enabled is intended (a stop)
options:
  max_concurrent_actions: 3        # always set: the actors racing through yield points (the default 2 hid #1217)
  max_actions: 14                  # always set: the smallest scenario in which every mutant fails
  crash_on_yield: false            # say which faults are modeled; true (FizzBee's default) crashes at yield points
---
# diagram: phase                   # at least one; a global, or one role's Worker.phase
# mutant upload-pre-copy: AckedCommitsAreStored

MUTANT = ""                        # check-specs runs each mutant with --preinit-hook 'MUTANT = "<name>"'
```

- **State**: globals for shared state; a `role` for an actor with its own lifecycle (`self.phase`), created one per
  step in a loop (`workers.append(Worker(id=i))`), with an `id` param to find the others.
- **Steps**: `atomic action` is one step; a plain action yields after each statement, so other actions interleave
  there; `atomic:` blocks group statements inside one. A stop or crash cuts at those yield points. A step that only
  reads (a check with no write) makes no interleaving point: when a race depends on its timing, record it
  (`committing.append(c)` in `docs/design/lake/stop.fizz`).
- **Choices**: `p = oneof [...]` into a local, then assign it; `oneof` straight into a field assigns nothing.
- **Waits and timeouts**: an action guarded by `require <what it waits for>`; the timeout is a second action guarded
  by `require <still waiting>` (`StopClosed` against `StopAbort` in the Lake stop).
- **Time**: a `fair` `Tick` action that advances a clock, guarded so time passes only when nothing urgent may run, and
  bounded (`age < E`), so the state space stays finite.
- **Faults**: `crash_on_yield` (durable fields survive, `@state(ephemeral=[...])` fields reset); a call to another
  role's function is an RPC that FizzBee may lose on the way in or out. Read another role's fields directly when the
  design shares state, and call functions only to model real messages. Crash and message-loss recipes are FizzBee's
  (references 14-01 to 14-04); no funcd spec uses them yet: try them on a copy first.
- **Assertions**: `always`, `transition`, `exists`; progress as `always eventually` or `eventually always`, with the
  actions it relies on marked `fair` (there is no bare `eventually`). A wait or retry with no bound in modeled time
  gets a progress assertion: it catches the over-correction that never lets go (#1208's control). A wait a modeled
  clock bounds (key renewal's `HOLD_END`) is checked by a safety assertion on that clock; name the timing it assumes
  under *Left to tests*.
- **Mutants**: `if MUTANT != "<name>": require <guard>` around each guard the Decision adds or relies on, and a
  `# mutant <name>: <assertion>` line. A mutant that passes means the assertion checks nothing.

## Check, draw, read

```bash
scripts/agent/d just check-specs       # every spec: pass, bounds, state budget, mutants, diagrams, oracle markers
scripts/agent/d just spec-diagrams     # rewrite each <topic>.md after an edit, then commit it
scripts/agent/d python3 scripts/specs.py check docs/design/lake/stop.fizz   # one spec
```

For a trace by hand, copy the spec to a scratch directory (`fizz` writes `<spec>.json` beside it) and run
`fizz --output-dir out <spec>.fizz`, with `--preinit-hook 'MUTANT = "<name>"'` for a mutant. A failure prints the
steps from `Init` to the broken state: each block names the action or thread that ran and the state after it. Retell
it as the issue's story before you trust it (FizzBee's VERIFICATION_GUIDE). A `deadlock` verdict at an intended end
means `deadlock_detection: false` is missing.

Keep a run within the 100,000-state budget: fewer items, fewer actions, atomic steps where the design has no yield.
Run the wider bound, one more item than the race needs, once on a copy (edit its front matter) and record it in the
ADR's result line, not in `just ci`.

## The ADR's Design model section

```markdown
## Design model

- **Spec**: [`docs/design/lake/stop.fizz`](../design/lake/stop.fizz) · state machine [`stop.md`](../design/lake/stop.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `AckedCommitsAreStored` | always | `scenario: stop-keeps-acknowledged-commits` | `TestScenario…` |
- **Bounds**: 2 commits, 3 concurrent actions, 14 actions: the smallest race that broke ADR-0271 (2 hid it).
- **Faults**: graceful stop; crash on yield off (a SIGKILL's loss is #998's trade-off).
- **Mutants**: `upload-pre-copy` (ADR-0271's timeout) fails `AckedCommitsAreStored`.
- **Result**: pass, <n> states, <s> s, FizzBee v0.5.4; wider (<bounds>): pass, <s> s.
- **Left to tests**: <facts of the code or platform the model cannot hold, and the test that holds each>.
```

The implementation's oracle test carries `// oracle: docs/design/<part>/<topic>.fizz#<Assertion>` (Python: `#`)
above it; the review greps for it. A fix that changes a modeled behavior updates the spec and its diagram in the same
commit.

## Patterns

FizzBee's references at v0.5.4 (`examples/references/` in fizzbee-io/fizzbee) hold a spec per pattern: a lease or
distributed lock (13-01-03), optimistic locking and compare-and-swap (13-01-04, 13-03-03), two-phase commit and saga
(13-02-01, 13-02-02), producer-consumer and message queue (13-04-01, 13-04-02), idempotent operations (13-05-02), a
write-ahead log (13-07-01). Start from the closest one; its `GOTCHAS.md`, `PERFORMANCE_GUIDE.md` and
`VERIFICATION_GUIDE.md` are worth a read before a first spec. Do not run `fizz install-skills`: it fetches FizzBee's
unpinned main branch into the global skills.

## Limits of v0.5.4 and their workarounds

| Limit | Workaround |
|---|---|
| a top-level `func` is undefined inside an `if` condition | inline the expression |
| a call inside a larger expression is refused | `x = fn()` as its own statement first |
| `self.x = other.fn()` crashes | assign to a local, then to the field |
| `x.field = oneof [...]` assigns nothing; `oneof` inside a literal does not parse | choose into a local first |
| one step creating several roles panics | append them one per step in a loop |
| an unknown front-matter key is refused | checker directives are comments |
| no tuple unpacking in `for` | index the tuple |
| an intended end reads as a deadlock | `deadlock_detection: false` |
| `max_actions` can hide an unbounded spec | bound counters and queues in the spec itself |
| a step that only reads makes no interleaving point | record the step in the state when its timing matters |

## What a model cannot catch

Facts of the code or the platform the spec does not encode: two SQLite libraries blind to each other's locks, ext4's
overhead, a FIFO in a backup. Those stay with real-engine and e2e tests; the section's *Left to tests* names them.
