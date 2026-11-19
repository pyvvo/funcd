---
title: Add-ons
description: Register an add-on built outside funcd, such as rauthy, declare its objects, back them up and sign users in through it.
---

## Usage

An add-on is an engine built outside funcd that an admin registers on a running funcd. Its users then declare objects
of the kind it adds, as they declare a Lake: funcd runs one engine per object, serves it at the object's host, and
copies its data into the namespace's backups. The add-ons funcd ships live in
[pyvvo/funcd-addons](https://github.com/pyvvo/funcd-addons). This page uses rauthy, an OpenID Connect provider of
kind `IdentityProvider`.

An admin registers it from a release, and an operator lists its registry in `addons.registries`:

```sh
funcdctl apply -f https://github.com/pyvvo/funcd-addons/releases/download/v0.1.0/rauthy.yaml
```

A user writes the Secret its objects bind with its plugin, then declares an identity provider:

```sh
funcdctl rauthy keys --admin-email admin@example.com -n default
funcdctl apply -f identityprovider.yaml
funcdctl get identityproviders -n default
```

```yaml
apiVersion: funcd.io/v1alpha1
kind: IdentityProvider
metadata:
  name: login
  namespace: default
spec:
  config:
    publicHost: login.example.com
    rpId: login.example.com
    https: true
  secrets:
    - rauthy-keys
  ingress:
    host: login.example.com
```

funcd checks `spec.config` against the add-on's schema when the object is applied, and the object turns `Ready` once
its engine answers on its readiness path.

## Back it up

A BackupSchedule copies an add-on object when its scope names it under `addons`: each Backup copies the newest copy
that the engine wrote of its own data. A Restore of an add-on object is in place only. funcd restarts the engine with
the copy, then again without it, and holds the namespace meanwhile.

## Sign users in through it

An `OIDCProvider` whose `addon` names the IdentityProvider has funcd register the edge's sign-in client there. A Route
whose `auth.mode` is `oidc` then signs its callers in, and the code behind it reads who they are from the
`X-Funcd-Principal` headers.

## Build one

[Build an add-on](build.md) shows what an engine must do and how rauthy's Addon and its funcdctl plugin are written.
