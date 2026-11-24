---
paths:
  - "docs/adr/**"
  - "docs/feat/**"
  - "docs/design/**"
  - "api/types/**"
  - "internal/**"
  - "cmd/**"
---

# One mechanism per job

A design or a change that needs a schema, a check, a reference, an owner rule, a schedule, a retry, a quota, an
expression or a version uses the component that already does that job. Two components for one job drift: each changes
without the other, and nothing compares them.

1. **Ask who reads it.** A schema, a limit or a rule lives with the component that reads the values, and only there.
   When only a Function reads them, its contract is the schema.
2. **Search before proposing.** Read how the shipped kinds do the job (`api/types/v1alpha1`, their reconcilers,
   `docs/behavior/`, the ADRs), and take what you find as the default. Cite it. A question that a shipped kind answers
   is not a question for the decider.
3. **A second mechanism is a decision.** *Alternatives considered* names the existing component, what it lacks, and why
   extending it lost. "None exists" says what was searched.

| The job | What already does it |
|---|---|
| Describe and check what code takes and answers | its contract, which funcd checks on every call (ADR-0058, ADR-0303) |
| Say that a side takes nothing | void, `{"type": "null"}` (ADR-0090), never `{}`, which takes anything |
| Name a Function, or own one made from an image | a Workflow step's `function.ref` and `function.image` (ADR-0094, ADR-0096) |
| Run on a schedule | an EventSource timer and a Sensor (ADR-0108, ADR-0109), in ADR-0211's cron grammar (`api/cron`) |
| Decide who may read, write or call | Cedar (ADR-0136, ADR-0177); RBAC for the API's own objects |
| Own what another object wrote | `internal/ownedstore` and the owner references (ADR-0170) |
| Compute a value in a manifest | the `${{ }}` engine, `internal/expr` (ADR-0095) |
| Bound what a namespace creates | an admission under the namespace lock (ADR-0147) |
| Keep the version that ran | a Revision, pinned by digest (ADR-0035, ADR-0172) |

How it drifted here:

| Pull request | The second mechanism | What it cost |
|---|---|---|
| [#1347](https://github.com/pyvvo/funcd/pull/1347), [#1348](https://github.com/pyvvo/funcd/pull/1348) | A contract's rules in ajv and in fastjsonschema, one per shim | One contract took different data on Node and on Python: `uuid`, `date-time`, `\d` (ADR-0301's table, #1339). Now funcd alone checks |
| [#1433](https://github.com/pyvvo/funcd/pull/1433) | Secret reads asked RBAC, while KV, Blob, SQL, Lake, calls and egress asked Cedar | The check never denied, so no Policy could stop one Function from reading one Secret (ADR-0321) |
| [#1566](https://github.com/pyvvo/funcd/pull/1566) | `variables: {}` for a module that takes none, where void existed | A run's input could set a variable that the same Decision keeps out of every input |
| [#1516](https://github.com/pyvvo/funcd/pull/1516), [#1558](https://github.com/pyvvo/funcd/pull/1558) | An Addon's schema for objects that only a Function reads, beside that Function's `{}` contract | A field that the Addon adds is `Ready` and never applied by an older Function (reproduced on 2026-11-24). Being replaced by the contract |
| [funcd-addons#15](https://github.com/pyvvo/funcd-addons/pull/15) | A cron parser in the InfraStack reconciler | ADR-0211's grammar in a second language, kept in step by hand |
