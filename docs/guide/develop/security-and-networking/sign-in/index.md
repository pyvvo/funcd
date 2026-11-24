---
title: OIDC sign-in
description: Sign users in at the edge with an OpenID Connect provider before their requests reach a Route, a workload's address or a namespace, and read who signed in.
---

## Usage

An **OIDCProvider** names an OpenID Connect provider, such as Keycloak or the
[rauthy add-on](../../services/addons/index.md). A Route, a workload's own address or a namespace that names it signs
its callers in at [funcd's edge](../../../platform/security-and-networking/sign-in.md), before a request reaches the
code behind it.

The example puts a sign-in in front of the Function `devices-api`, with rauthy as the provider. It needs the
[dev build of funcdctl](../../../get-started/install.md), rauthy's plugin
[`funcdctl-rauthy`](https://github.com/pyvvo/funcd-addons/tree/main/rauthy#the-plugin) and Node.js on the `PATH`, a
running Docker, Git, curl and a browser. Clone both repositories into one folder and start the http-api example:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ git clone https://github.com/pyvvo/funcd
$ cd funcd-typescript
$ funcdctl dev examples/http-api --gport 8080 --cport 8081
```

In another terminal, from the folder of both clones, register rauthy with dev's admin token, and have its plugin write
the Secret that rauthy reads. The plugin prints the password of rauthy's first admin once:

```console
$ export FUNCD_SERVER=http://127.0.0.1:8081 FUNCD_TOKEN=funcd-dev-token
$ curl -fsSL https://github.com/pyvvo/funcd-addons/releases/latest/download/rauthy.yaml | funcdctl --token funcd-dev-admin-token apply -f -
applied Addon/rauthy
$ funcdctl rauthy keys --admin-email admin@login.localhost -n default
```

Save this IdentityProvider, which runs rauthy at `login.localhost`, as `identityprovider.yaml`:

::include[../../../../../api/types/v1alpha1/examples/sign-in/identityprovider.yaml#identityprovider]

Apply it, and repeat the second command until its phase is `Ready`:

```console
$ funcdctl apply -f identityprovider.yaml
applied IdentityProvider/login
$ funcdctl get identityprovider login -n default -o json
```

The OIDCProvider, and a Route that signs its callers in with it:

::include[../../../../../api/types/v1alpha1/examples/sign-in/oidcprovider.yaml#oidcprovider]

::include[../../../../../api/types/v1alpha1/examples/sign-in/oidcprovider.yaml#route]

- [`issuer`](spec.md#oidcproviderspec) is the URL that browsers use, written exactly as the provider's discovery
  document names it. `addon` has funcd itself reach the provider at the address of rauthy's engine.
- [`client.registration`](spec.md#oidcproviderspecclientregistration) has funcd register the edge's client at the
  provider, and keep its redirect URIs on the hosts that sign in with it.
- [`sessionSecret`](spec.md#oidcproviderspec) names a Secret whose `SESSION_KEY` is 32 random bytes in base64, as
  `openssl rand -base64 32` prints them. The file has a test key: whoever knows the key can forge a session.

Apply the [file](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/sign-in/oidcprovider.yaml), and
repeat the second command until the phase is `Ready`:

```console
$ funcdctl apply -f funcd/api/types/v1alpha1/examples/sign-in/oidcprovider.yaml
applied Secret/sso-session
applied OIDCProvider/sso
applied Route/devices
$ funcdctl get oidcprovider sso -n default -o json
```

A call with no session gets 401, and a page load goes to the provider:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/devices
401
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'Sec-Fetch-Mode: navigate' http://localhost:8080/devices
302
```

Open `http://localhost:8080/devices` in a browser and sign in with the email and the password that the plugin printed:
the page shows the devices, `[]`. Press Ctrl-C in the first terminal to stop; `funcdctl dev` then removes rauthy's
container and its data.

## Choose what asks for a sign-in

The mode `oidc` of an [`auth`](../networking/spec.md#routespecauth), with `provider` naming an OIDCProvider of the same
namespace, signs the callers in of:

- a Route, or one of its rules;
- the own address of a [Function](../../workloads/function/spec.md#functionspecingressauth), a
  [Site](../../workloads/site/spec.md#sitespecingressauth) or an add-on object, by its `ingress.auth`;
- a namespace, by its [`edgeDefaults.auth`](../../tenancy-and-limits/namespaces/spec.md#namespacespecedgedefaultsauth),
  which applies where nothing else does, but not to an add-on object's address.

A rule's own `auth` wins over its Route's, and both win over the namespace's. In `default`, the mode needs a host. The
rules of one host sign in with one provider: a Route that brings another is not served, with `ProviderConflict`, and its
paths answer 503.

`unauthenticated` sets what a call that is not signed in gets:

| `unauthenticated` | A call that is not signed in |
|---|---|
| `redirect`, the default | A `GET` or `HEAD` navigation, with `Sec-Fetch-Mode: navigate` or else `text/html` in `Accept`, gets 302 to the provider; any other call, and a call by name whose namespace `X-Funcd-Namespace` chose, gets 401 |
| `401` | 401 |
| `allow` | Passes, with no principal |

## Use a client made at the provider

At a provider without dynamic registration, its admin creates the client, a confidential one for the authorization
code flow, with one redirect URI per host that signs in, such as `https://app.example.com/.auth/callback`. A Secret
then has its `CLIENT_ID` and `CLIENT_SECRET`
([complete file](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/sign-in/credentials.yaml)):

::include[../../../../../api/types/v1alpha1/examples/sign-in/credentials.yaml#credentials]

Without `addon`, funcd reaches the provider over https only, at addresses that the internet reaches.

## Call with a token

A call also signs in with a token that the provider signed, such as an ID token or a JWT access token, in
`Authorization: Bearer`, when its `aud` is one of the provider's [`audiences`](spec.md#oidcproviderspec). Without
`audiences`, a token signs nobody in. Another token gets 401 with `WWW-Authenticate: Bearer error="invalid_token"`. The
code behind the edge gets the token too.

## Read who signed in

The edge lets every user pass whom the provider signs in. What each may do is up to the code behind it:

- An http app gets `X-Funcd-Principal`, the Base64 of a JSON object with `provider` and `claims`;
  `X-Funcd-Principal-Id`, the `sub` claim; `X-Funcd-Principal-Name`, the first of the `name`, `preferred_username` and
  `email` claims; and `X-Funcd-Id-Token` when the call has a session.
- A handler reads `ctx.principal`, with `provider`, `id`, `name` when the token has one, and `claims`. It is
  `undefined` in TypeScript and `None` in Python when the call is not signed in.

funcd removes these headers from every request that it did not sign in.

## End a session

`/.auth/logout` on a host clears funcd's session cookies there. The provider's own session stays, so the next page load
can sign the user in again without a password.

A session ends when the provider's ID token expires, after 30 minutes with rauthy's default. A page load then signs in
again; any other call gets 401 `sign in first`, so a form sent after that loses its input.

A call with a session and another method than `GET`, `HEAD` or `OPTIONS` must come from the host's own origin, or it
gets 403. An app that calls an API on another host sends such a call with a token.

## Rotate the client's secret

Raise [`client.registration.rotate`](spec.md#oidcproviderspecclientregistration) and apply. Once
`status.observedRotate` equals it, the provider refuses the old secret. No session ends.

## Access

A `developer` manages the OIDCProviders of its namespaces, and a `viewer` reads them. The edge asks neither
[RBAC nor Cedar](../security/index.md) about a signed-in user.

## Limits and quotas

- A sign-in must finish within 10 minutes, or the callback gets 401 `the sign-in's state expired: sign in again`.
- A session cookie has at most 4096 bytes. A provider whose ID tokens need more signs nobody in.
- Each of funcd's calls to a provider stops after 10 seconds or 1 MiB.

## Status

`funcdctl get oidcprovider <name> -n <namespace> -o json` prints the status: `clientId`, `callbacks`, the redirect URIs
that funcd last applied, and the conditions. While a provider cannot sign users in, its hosts answer 503.

| Condition | Reason | Meaning |
| --- | --- | --- |
| `Ready`, true | `ClientReady` | Users can sign in. |
| `Ready`, false | `SessionSecretMissing`, `SessionKeyInvalid` | The Secret of `sessionSecret` is missing, or its `SESSION_KEY` is not 32 bytes. |
| | `ClientSecretMissing` | The Secret of `credentialsSecret` lacks `CLIENT_ID` or `CLIENT_SECRET`. |
| | `AddonBehindProvider` | The add-on object that serves the provider asks for a sign-in with that same provider. |
| | `DiscoveryFailed` | funcd cannot read the provider's discovery document, or the document lacks what a sign-in needs. |
| | `IssuerMismatch` | The discovery document names another issuer. The message shows both; a trailing slash counts. |
| | `Unused` | No host signs its callers in with the provider, so funcd registers nothing. |
| | `RegistrationFailed` | The provider refused the registration or did not answer. The message has the HTTP status. |
| | `SecretNotOwned`, `SecretIssueFailed` | A Secret of the client's name exists that funcd did not write, or funcd could not write it. |
| `ClientSynced`, false | `RegistrationFailed`, `SecretIssueFailed`, `RotationRefused` or `Unused` | The client still signs users in, but its redirect URIs or its `rotate` are not applied yet. funcd tries again every `runtime.supervisionPeriod`, 10 seconds by default. |

A Route that names a missing provider is `Pending` with `ProviderNotFound`, and its paths answer 503. A Function or an
add-on object shows it in the condition `IngressReady`, a Site as `RouteNotReady`, and a Namespace in the condition
`EdgeDefaultsReady`.

## Changes and deletion

- A host that starts or stops naming the provider changes the client's redirect URIs at the next pass, within
  `runtime.supervisionPeriod`.
- A new `SESSION_KEY` or a new client ID ends every session of the provider.
- A client that the provider no longer knows is registered again.
- Deleting an OIDCProvider deletes the Secret that funcd wrote for it, and its hosts answer 503. The client stays at the
  provider; rauthy removes one that nobody used for 90 days.

## CLI errors

| Error | Action |
| --- | --- |
| `unknown command "rauthy" for "funcdctl"` | Put `funcdctl-rauthy` on the `PATH`. |
| `manifest is invalid (<kind> "<name>")`, followed by `spec.auth: mode oidc in namespace default needs a host, since default has no namespace host` | Set the Route's `host`, or the workload's `ingress.host`. |
| `manifest is invalid`, followed by `spec.auth.provider is required with mode oidc` | Name an OIDCProvider of the namespace. |
| `manifest is invalid`, followed by `spec.auth.provider and spec.auth.unauthenticated are only for mode oidc` | Set the mode `oidc`, or remove both. |
| `manifest is invalid`, followed by `spec.rules[0].path "/.auth/x" lies under /.auth, the edge's own path on every host` | Choose another path: `/.auth/` is funcd's on every host. |
| `manifest is invalid`, followed by `spec.client must set exactly one of registration and credentialsSecret` | Keep one of them. |

## Run it locally

Under `funcdctl dev`, the edge serves http, so its cookies are not `Secure`. rauthy accepts an http redirect URI on a
loopback host only: sign users in on `localhost` or `127.0.0.1`.

## Deploy it

Serve the edge over https. The redirect URIs use https when funcd terminates TLS
([`server.tls.enabled`](../../running-funcd/spec.md#funcdconfigyaml)), or when `server.edgePublicScheme` is `https`
behind a proxy that does; `server.edgePublicPort` is the port that browsers use when it is not 443.

In the example, set the IdentityProvider's `publicHost`, `rpId` and `ingress.host` and the OIDCProvider's `issuer` to
the provider's public host, `https` to `true`, and the Route's `host` to the app's. Write a `SESSION_KEY` of its own.

## Examples

- [rauthy](https://github.com/pyvvo/funcd-addons/tree/main/rauthy): the provider, which pyvvo/funcd-addons'
  `addon-rauthy-signin` lane signs a user in with.
- [http-api in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/http-api): the Function behind
  the sign-in.
