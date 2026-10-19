# ADR-0252: The scenarios of ADR-0199 and ADR-0213 that the resource-shape refactor changes

- **Status**: Accepted (2026-10-19), after two judge rounds, under `adr-batch` with acceptance delegated by the
  decider: the judge added `app-shared-writer-refused`, `app-config-change-rolls` and `app-secret-declared`.
- **Date**: 2026-10-19
- **Deciders**: green-0-rabbit
- **Tags**: app, stores, config, secrets, prune
- **Realizes**: [FEAT-0001/F159 and F161](../feat/0001-feat-v1.1.md) (#1068, #1071, tracker #1072)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0252` back-link:
  - [ADR-0199](0199-app-resource.md)'s scenarios `app-prune-dropped-part`, `app-store-in-use-kept` (its first half) and
    `app-shared-writer-refused` (its Site and two-Workflows cases);
  - [ADR-0213](0213-app-config-and-secret-declarations.md)'s scenarios `app-config-change-rolls` (its last case) and
    `app-secret-declared` (its environment assertions).
- **Relates to**: [ADR-0248](0248-stores-at-the-root.md) Decision 2 and [ADR-0249](0249-ctx-secret-reads-from-funcd.md)
  Decision 3, which should have listed these scenarios; both are frozen, so this ADR records the change.

## Context & Need

ADR-0248 Decision 2 has an App's parts bind only what the App declares, and carry no root entries of their own; ADR-0249
takes secrets out of the worker's environment. Five scenarios of two Implemented ADRs assume otherwise
(`docs/adr/0199-app-resource.md:52-53`, `:60-64`; `docs/adr/0213-app-config-and-secret-declarations.md:49-53`,
`:67-68`), so their tests fail.

**Purpose**: give each of those scenarios its form under the new rules, so the tests prove the rules instead of the
old behavior.

## Scenarios

- **scenario: app-prune-dropped-part** — *When* the App drops Route `todo-api`, store `todo-store` and `todo-api`'s
  binding to it in one apply, *Then* once no part is Pending the Route is deleted, and `todo-store` stays with its
  data and marker.
- **scenario: app-store-in-use-kept** — *Given* Function `audit`, outside the App, binding `todo-cache` by `ref`,
  *When* the App drops `todo-cache` and `todo-api`'s binding to it, *Then* the store stays `Pruning` `InUse` naming
  `Function/audit`, the App `Ready`; once `audit` unbinds it, it goes with its keys.
- **scenario: app-drop-bound-store-refused** — *When* the App drops `todo-store` while `todo-api` still binds it,
  *Then* apply refuses it, naming `todo-api`'s binding.
- **scenario: app-shared-writer-refused** — a Site part or a Workflow part that declares a store of its own is refused
  as a part's own root entry, naming it; the function named like a step (`todo-plan-due`) is refused as before.
- **scenario: app-config-change-rolls**, last case — a ConfigMap the App does not define, declared by `ref`, named by
  `todo-api` and changed by hand ⇒ the App renames nothing and writes nothing.
- **scenario: app-secret-declared**, under ADR-0249 — once the key is added, `todo-api` reads `STRIPE_API_KEY` through
  `ctx.secret` and its worker's environment holds no such key; the value changed outside the App ⇒ the next read
  returns it, and no object gets a new `resourceVersion` from the App.

## Scope

**In**: the six scenarios above. **Out**: ADR-0199's and ADR-0213's other rules, which stand.

## Constraints & Decision drivers

- ADR-0248 Decision 2 and ADR-0249 hold with no exception, in every pass.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. Restate the scenarios under the new rules** | One rule; the old protections stay where they still apply | Five tests change | **Chosen** |
| B. Treat the stored spec's stores as declared during a rollout | Two scenarios stand | A loophole in ADR-0248 Decision 2 | Rejected |

## Decision

1. **Dropping a store.** An App drops a store together with the bindings to it. Dropping a store that one of its parts
   still binds is refused at apply, naming the binding. ADR-0199's `InUse` keeps a dropped store that something outside
   the App still binds, naming the binder.
2. **A part's stores.** A part declares no store of its own: the App declares a Site's bucket and a shared KV store at
   its root. ADR-0199's shared-writer refusal for those two cases becomes the refusal of a part's own root entry.
3. **A hand-made ConfigMap** a part binds is declared by `ref` at the App's root; the App never renames or writes
   it.
4. **A declared Secret**, under ADR-0249, reaches a part only through `ctx.secret`, read at call time.

## Temporary workarounds

None.

## Contracts

None: no type, reason or admission changes.

## Implementation plan

- In the ADR-0246 + ADR-0248 funcd PR: the tests of `app-prune-dropped-part`, `app-store-in-use-kept` and
  `app-shared-writer-refused` (`pkg/funcd/app_e2e_test.go`) and of `app-config-change-rolls`
  (`pkg/funcd/app_config_e2e_test.go`) change as above, keeping their names; `TestScenarioAppDropBoundStoreRefused`
  is added.
- In ADR-0249's funcd PR: the test of `app-secret-declared` changes as above.
- Docs: the back-links on ADR-0199 and ADR-0213, and ADR-0252 on the F159 row.

## Review checklist

- [ ] Each of the six scenarios has its named test, which passes.
- [ ] ADR-0199 and ADR-0213 carry the back-links.

## Consequences

- **Positive**: the tests prove the new rules; the old protections stay where they still apply.
- **Negative**: dropping a store takes the bindings' removal in the same apply; a hand-made ConfigMap needs a `ref`.
- **Risks accepted**: none.

## Open questions

None.

## References

- #1068, tracker #1072; the scenarios at `docs/adr/0199-app-resource.md:52-53`, `:60-64` and
  `docs/adr/0213-app-config-and-secret-declarations.md:49-53`, `:67-68`.
