# FEAT-0019: Add-ons that ship as App templates, with Functions that configure their engines

- **Status**: Active: a living document; the tracking table updates as ADRs progress
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Defines**: an **add-on apps epoch**: an add-on ships as one App template that carries its Addon, the objects of
  its kind and the Functions that configure its engine, so an add-on built outside funcd keeps its engine's own
  settings, such as rauthy's password policy, and repairs them when they drift. An Addon's changes are revisions that
  each object pins, and an Addon may add settings kinds, whose objects a Function applies. Follows
  FEAT-0018, which made add-ons an engine and a manifest.

## Initial need

An add-on is an engine image and an `Addon` manifest (FEAT-0018, ADR-0332). funcd gives the engine its object's
`config` only as environment variables at its start, and replaces the engine when that spec changes
(`internal/provider/runtime.go:89-90`). Many engines keep settings at runtime instead: rauthy sets its password policy
only through its API (`PUT /auth/v1/password_policy`), with an admin session or an API key. So funcd can neither set
such a setting nor repair it when someone changes it in the engine's own UI.

funcd's App already ships everything that work needs: Functions, Sensors and timers (ADR-0199), typed values
(ADR-0217), a template pushed as one artifact (ADR-0218), hooks after each revision (ADR-0214), Secret declarations
(ADR-0213) and requirements (ADR-0219). Two gaps keep it from carrying an add-on: an App holds no Addon and no object
of an add-on's kind, and no workload reaches an add-on's engine (ADR-0332 Decision 3).

## How this document works

This file captures **what** this capability set must contain and **why**. The **how** lives in ADRs (`docs/adr/`,
process in [ADR-0000](../adr/0000-adr-process.md)). The decider refined the decisions on 2026-11-19, before the first
ADR was drafted: reuse the App framework rather than a new add-on template; the template carries its Addon as a Helm
chart carries its CRDs; a Function reaches an add-on's engine through funcd, as it reaches a SQL database. On
2026-11-21 the decider added two rows: an Addon's versions are revisions that each object pins, as a Function's are,
so a newer Addon moves no engine by itself; and a setting the engine keeps, such as the password policy, is an object
of a settings kind that the Addon adds, applied by the kind's Function and usable without an App, as experiment 0026
measured.

## Features

F263 comes first: an App that holds the add-on's objects is what F264's Functions bind. F265 follows F263, whose
deploy updates an Addon. F266 builds on F264: the kind's Function reaches the engine through F264's binding.

| # | Feature | Builds on | ADR(s) | Status |
|---|---|---|---|---|
| F263 | **An add-on ships as an App template**: an App template carries its Addon and the objects of the add-on's kind; an admin's deploy registers the Addon before the App, as written, a developer's deploy only checks that its kind is served, the App owns its objects, whose kind must be registered, and the Addon outlives every App | FEAT-0018 F258 | [ADR-0337](../adr/0337-an-app-template-carries-its-add-on.md) | implemented |
| F264 | **A Function calls an add-on object's engine**: a Function binds an add-on object of its namespace and calls the engine through funcd, with the shim's client or any HTTP SDK, as it calls a SQL database or a Bucket | F263 | [ADR-0338](../adr/0338-a-function-calls-an-add-on-objects-engine.md) | implemented |
| F265 | **An add-on's versions are revisions**: each change of an Addon is a revision, each object runs the revision it pins, a newer revision moves no object, and an edit of the object or a new revision of its App moves it; a revision no object and no kept App revision uses is removed | F263 | [ADR-0339](../adr/0339-an-add-ons-versions-are-revisions-each-object-pins.md) | implemented |
| F266 | **An add-on adds settings kinds that a Function applies**: an Addon declares kinds whose objects run no engine, each naming its target object, checked at admission against a schema and built-in constraints; funcd sends a target's full list of them to the kind's Function, or to one the target names for itself, which applies them through the engine, repairs drift at each resync and rolls a deleted one back | F264 | [ADR-0340](../adr/0340-an-addon-declares-settings-kinds.md), [ADR-0341](../adr/0341-a-function-applies-a-targets-settings.md) | implemented |
| F275 | **An add-on's kinds bring their Function**: a settings kind, and a kind that runs no engine, names its Function's image in the Addon, as a Workflow step does, so funcd runs the same code in every namespace with nothing to deploy there; the Function's contract is the kind's schema, so an object's fields and the code that reads them ship together, and a kind may still name a Function of the namespace | F266, FEAT-0012 F270 | [ADR-0358](../adr/0358-an-add-on-kinds-function-comes-from-an-image-and-its-contract-is-the-kinds-schema.md) | adr |

## How it lands on funcd (high level)

Nothing new to package: an add-on's App template is an App template with one more file, its Addon, and the App gains a
section for the add-on's objects. The engine call is the add-on provider's own facet of the worker-node local API,
beside those that `ctx.sql` (ADR-0233), `ctx.kv` and `ctx.blob` use. A settings kind is one more kind an Addon serves,
and its Function is a Function of the namespace. rauthy, in `pyvvo/funcd-addons`, is the worked example.

## Out of scope

- Add-ons registered by a namespace's developers: only an admin registers one, as in FEAT-0018.
- A Function that reaches an engine's address directly, or an engine of another namespace.
- Templating the Addon with an App's values: one Addon serves every namespace.

## Exit criterion

An admin deploys rauthy's App template from `pyvvo/funcd-addons` once, which registers its Addon. A developer deploys
the same template in another namespace with a password policy in its values: rauthy enforces it after the deploy, a
change made in rauthy's own UI is undone within one resync, and a new value reaches rauthy with the next revision. A
second developer applies a `RauthyPasswordPolicy` by hand, with no App, and rauthy enforces it too. An admin's newer
Addon moves no engine until an App's revision or an edit moves it. Deleting the developer's App removes its objects and
Functions, and the Addon stays.
