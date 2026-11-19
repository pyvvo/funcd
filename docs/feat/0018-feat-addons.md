# FEAT-0018: Add-ons built outside funcd, and an edge that signs users in

- **Status**: Active: a living document; the tracking table updates as ADRs progress
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Defines**: an **add-ons epoch**: an admin registers an add-on built outside funcd's repository on a running funcd,
  and users declare objects of the kind it adds, as they declare a Lake or a SQL database. Beside it, the edge signs
  users in with any OpenID Connect provider, so an App that does not check tokens itself is still protected. The first
  add-on is rauthy as an OpenID Connect provider. Positioned alongside the other additive epochs (FEAT-0003 to
  FEAT-0017).

## Initial need

Every add-on is built inside funcd today: the SQL database and the Lake each have a kind, a reconciler, an embedded
image and a provider in this repository, and the provider list is fixed in `funcd.New` (`pkg/funcd/funcd.go`). An
engine that funcd does not ship, such as an identity provider, cannot become an add-on without a funcd release, and
nobody outside the repository can build one.

The first such engine is rauthy, an OpenID Connect provider kept in its own repository with funcd's look
(`pyvvo/funcd-rauthy`). An identity provider also shows the second gap: funcd has no way to sign users in. Its edge
checks only the static credentials of the server (`auth.credentials`), so every App must check its users' tokens
itself, as Azure Container Apps' built-in authentication spares its apps from doing.

## How this document works

This file captures **what** this capability set must contain and **why**. The **how** lives in ADRs (`docs/adr/`,
process in [ADR-0000](../adr/0000-adr-process.md)). The decisions were refined with the decider on 2026-11-19 in six
cards, before the first ADR was drafted, on facts checked on rauthy v0.37.2's code and image.

## Features

F258 comes first: every other add-on row needs it. F259 and F260 build on it. F261 is independent of the add-ons, and
F262 builds on F261.

| # | Feature | Builds on | ADR(s) | Status |
|---|---|---|---|---|
| F258 | **Add-ons built outside funcd**: an admin registers an add-on, an engine image built outside funcd's repository with the kind it adds, and users declare objects of that kind, checked against the add-on's own schema and run like funcd's own add-ons, from images the operator allows | — | [ADR-0332](../adr/0332-an-admin-registers-add-ons-built-outside-funcd.md) | reviewing |
| F259 | **An add-on's data in funcd's backups**: a BackupSchedule copies an add-on object's data from the engine's own consistent copies, without stopping it, and a Restore brings it back | F258 | [ADR-0333](../adr/0333-an-add-on-objects-data-in-funcds-backups.md) | accepted |
| F260 | **Add-ons under `funcdctl dev`**: dev runs an add-on's image on the local Docker or containerd, so an App that uses an add-on runs on a laptop as on a server | F258 | [ADR-0334](../adr/0334-funcdctl-dev-runs-add-on-images.md) | accepted |
| F261 | **An edge that signs users in**: a Route, a workload's address or a whole namespace asks its callers to sign in with an OpenID Connect provider, and the code behind it reads who they are from the request, with no sign-in code of its own | — | [ADR-0335](../adr/0335-the-edge-signs-users-in-with-an-openid-connect-provider.md) | accepted |
| F262 | **Sign-in clients registered by funcd**: funcd registers and updates the edge's client at a provider that allows it, registers it again when the provider loses it, and keeps its secret, so no client is created by hand | F261 | [ADR-0336](../adr/0336-funcd-registers-the-edges-sign-in-client.md) | reviewing |

## How it lands on funcd (high level)

An add-on is a declaration and an image: funcd runs the image with the add-on runtime its own Lake and SQL database
use, and an outside repository, `pyvvo/funcd-addons`, holds the add-ons. The edge sign-in is part of the edge, beside
the `open` and `authenticated` modes it has today, and works with any OpenID Connect provider, rauthy and Entra ID
alike.

## Out of scope

- An add-on whose code runs inside the funcd daemon: an add-on's engine always runs outside it.
- Add-ons registered by a namespace's developers: only an admin registers one.
- rauthy's users as funcd principals for its API: funcdctl and the API keep their credentials.
- A server-side store of the users' tokens behind the edge sign-in.

## Exit criterion

An admin registers the rauthy add-on from `pyvvo/funcd-addons` on a running funcd. A user declares an identity provider
object, whose data a BackupSchedule copies and a Restore brings back. An App behind a Route in `oidc` mode signs its
users in through it with no sign-in code, the client that sign-in uses is registered by funcd, and the same App runs
under `funcdctl dev`.
