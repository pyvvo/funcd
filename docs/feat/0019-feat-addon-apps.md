# FEAT-0019: Add-ons that ship as App templates, with Functions that configure their engines

- **Status**: Active: a living document; the tracking table updates as ADRs progress
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Defines**: an **add-on apps epoch**: an add-on ships as one App template that carries its Addon, the objects of
  its kind and the Functions that configure its engine, so an add-on built outside funcd keeps its engine's own
  settings, such as rauthy's password policy, and repairs them when they drift. Follows FEAT-0018, which made add-ons
  an engine and a manifest.

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
chart carries its CRDs; a Function reaches an add-on's engine through funcd, as it reaches a SQL database; the
settings live in the App's values.

## Features

F263 comes first: an App that holds the add-on's objects is what F264's Functions bind.

| # | Feature | Builds on | ADR(s) | Status |
|---|---|---|---|---|
| F263 | **An add-on ships as an App template**: an App template carries its Addon and the objects of the add-on's kind; an admin's deploy registers the Addon before the App, as written, a developer's deploy only checks that it is registered, the App requires the Addon and owns its objects, and the Addon outlives every App | FEAT-0018 F258 | — | idea |
| F264 | **A Function calls an add-on object's engine**: a Function binds an add-on object of its namespace and calls the engine through funcd, as it calls a SQL database, so an add-on's App applies its engine's settings after each revision and repairs them on a timer | F263 | — | idea |

## How it lands on funcd (high level)

Nothing new to package: an add-on's App template is an App template with one more file, its Addon, and the App gains a
section for the add-on's objects and a requirement on its Addon. The engine call is one more route of the worker-node
local API, beside the routes that `ctx.sql` (ADR-0233), `ctx.kv` and `ctx.blob` use. rauthy, in `pyvvo/funcd-addons`,
is the worked example.

## Out of scope

- A kind without an engine, such as a settings kind of its own: the settings live in the App's values.
- Add-ons registered by a namespace's developers: only an admin registers one, as in FEAT-0018.
- A Function that reaches an engine's address directly, or an engine of another namespace.
- Templating the Addon with an App's values: one Addon serves every namespace.

## Exit criterion

An admin deploys rauthy's App template from `pyvvo/funcd-addons` once, which registers its Addon. A developer deploys
the same template in another namespace with a password policy in its values: rauthy enforces it after the deploy, a
change made in rauthy's own UI is undone within one timer period, and a new value reaches rauthy with the next revision.
Deleting the developer's App removes its objects and Functions, and the Addon stays.
