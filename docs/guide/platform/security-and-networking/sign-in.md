---
title: OIDC sign-in
description: How funcd's edge signs users in with an OpenID Connect provider before a request reaches the code behind it.
---

![A browser's page load with no session goes to the provider, where the user signs in. The provider sends the browser to the edge's callback with a code, which the edge exchanges for an ID token. The edge sets its session cookie, and the next request reaches the code behind it with the principal's headers](../../assets/sign-in.drawio.svg)

A Route, a workload's own address or a namespace can ask the edge to sign its callers in with an OpenID Connect
provider. An OIDCProvider names the provider, the client that the edge signs users in as, and the key that seals its
cookies. The edge finds the provider's endpoints and signing keys in its discovery document, so any provider that
follows the standard can serve: one that runs elsewhere, or one that runs as an add-on.

## A sign-in

A page load with no session goes to the provider, and any other call is refused, so a program never gets a login page.
The provider sends the browser back to a path that the edge answers itself on every host. There the edge exchanges the
code for an ID token, checks the token, and seals it into a cookie that only the edge can open. funcd stores no session:
the edge can restart and lose none. A session ends with its ID token, and the next page load signs in again.

The code behind the edge takes no part in the sign-in. It gets the user's ID, name and claims in headers that only the
edge sets: the edge removes them from every other request, so a caller cannot forge them. A request that changes
something passes with a session only when it comes from the host's own origin.

A program signs in with a token instead, one that the provider signed for an audience that the OIDCProvider lists.

## The edge's client

A sign-in needs a client at the provider whose redirect URIs are the hosts that sign in. The provider's admin makes it,
or funcd registers it where the provider offers dynamic registration. funcd then changes its redirect URIs as hosts come
and go, rotates its secret on demand, and registers it again when the provider no longer knows it.

funcd reaches a provider that runs elsewhere over https only, and only at addresses that the internet reaches, so an
OIDCProvider cannot point funcd at a service of its own network. It reaches a provider that runs as an add-on at its
engine's own address, which only funcd reaches.
