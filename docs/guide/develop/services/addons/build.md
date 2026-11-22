---
title: Build an add-on
description: Package an engine built outside funcd as an add-on, with rauthy and its funcdctl plugin as the example.
---

## Usage

An add-on is an engine image and an `Addon` manifest. The manifest names the kind the add-on adds and tells funcd how
to run, serve and back up one engine per object of that kind. funcd runs no code of the add-on in its own process.
[rauthy](https://github.com/pyvvo/funcd-addons/tree/main/rauthy) in
[pyvvo/funcd-addons](https://github.com/pyvvo/funcd-addons) is the example on this page: its Addon adds the kind
`IdentityProvider`, and its funcdctl plugin, `funcdctl-rauthy`, writes the Secret each object binds.

## How an add-on fits in

funcd's engine runtime serves two sorts of kind:

- **funcd's own kinds**, such as Lake and SQLDatabase. funcd holds their API and their integration, the bindings that
  workloads use, and runs DuckDB and rqlite as their engines.
- **An add-on's own kind.** The add-on defines the API: its Addon's `kind` and `config` schema. funcd runs, fences,
  serves and backs up one engine per object, and the engine does the rest. In Kubernetes terms, this is a custom
  resource with its operator, where funcd's add-on reconciler is a generic operator that the manifest drives.

An add-on cannot serve one of funcd's own kinds, as a CSI driver serves a Kubernetes volume; that is a funcd decision
still to make.

A funcd feature that speaks a standard protocol can still use an add-on's engine as its server. The edge's sign-in
([Sign users in](../../security-and-networking/security/index.md#sign-users-in)) speaks OpenID Connect to any
provider, so rauthy is to it what Dex is to the Kubernetes API server's OIDC authentication: a trusted provider, not a
driver. An `OIDCProvider` names the object with `spec.addon`, and funcd reaches the engine at its own address. Two of
rauthy's settings serve the sign-in rather than its own kind: `ENABLE_DYN_CLIENT_REG` lets funcd register the edge's
client, and `privatePaths` keeps that registration endpoint off the edge.

## What the engine must do

| The engine | rauthy |
|---|---|
| Serves HTTP on one port, under one base path that the edge keeps | port 8080, `/auth/v1` |
| Answers 200 on a readiness path once it serves | `/auth/v1/ping` |
| Keeps its data in one folder, as one user and group | `/app/data`, 10001:10001 |
| Takes its settings from environment variables | `PUB_URL`, `ENC_KEYS`, … |
| Writes consistent copies of its data into a folder of its own, for backups | hiqlite's `HQL_BACKUP_CRON` copies |
| Loads a copy at its start when one variable names it, for restores | `HQL_BACKUP_RESTORE=file:<path>` |
| Stops cleanly on SIGTERM within the stop grace | rauthy, with hiqlite's crash marker removed |

On a server, an engine reaches nothing outward: funcd fences its network, and only funcd reaches its port. Under
`funcdctl dev`, the engine runs on the local Docker or containerd network.

## The Addon

rauthy's [`addon.yaml`](https://github.com/pyvvo/funcd-addons/blob/main/rauthy/app/addon.yaml), part by part:

- `kind: IdentityProvider` is the kind users declare, at `/apis/funcd.io/v1alpha1/namespaces/<ns>/identityproviders`.
  funcdctl takes it as `IdentityProvider`, `identityprovider` or `identityproviders`. A lower-case word that ends in
  s is read as a plural, so a kind that ends in s, such as `Redis`, is typed with its capital letter or as its
  plural, `rediss`.
- `image` is pinned by digest, from a registry the server's `addons.registries` lists.
- `port`, `basePath`, `readiness.path` and `storage` describe the engine, as the table above does.
- `privatePaths` lists paths under `basePath` that the edge never serves. rauthy's client registration,
  `/auth/v1/clients_dyn`, is one: funcd registers clients itself, at the engine's own address.
- `config` is the JSON Schema of each object's `spec.config`, checked when the object is applied.
- `env` sets the engine's environment. A value is a literal or one `${{ }}` expression over `config`, checked against
  the schema when the Addon is applied. rauthy derives three settings from `config.https`; quote a value that holds
  `: `:

  ```yaml
  env:
    PUB_URL: ${{ config.publicHost }}
    COOKIE_MODE: '${{ config.https ? "host" : "danger-insecure" }}'
  ```

- `secretKeys` lists the keys of an object's Secrets that reach the engine; no other key does.
- `snapshot.path` is the folder of the engine's own copies: a Backup copies the newest one, unopened.
- `restore.path` and `restore.env` are where funcd puts a copy back and the environment of the start that loads it,
  with `${{ restore.file }}` the copy's path. They lie under `storage.path`, apart from `snapshot.path`.

## Settings kinds

Some engines keep a setting behind their own API, often for their whole instance, such as rauthy's password policy. An
Addon gives such a setting a kind of its own in `settings`. An object of a settings kind runs no engine: it names its
target, an object of the Addon's `kind` in the same namespace, and the kind's Function applies it to that target's
engine.

```yaml
settings:
  - kind: RauthyPasswordPolicy
    target: spec.config.provider
    resync: 5m
    function:
      ref: rauthy-settings
    schema:
      type: object
      additionalProperties: false
      properties:
        provider:
          type: string
        lengthMin:
          type: integer
        lengthMax:
          type: integer
      required:
        - provider
        - lengthMin
        - lengthMax
    constraints:
      count:
        perTarget: 1
      immutable:
        - spec.config.provider
      rules:
        - rule: ${{ object.spec.config.lengthMin <= object.spec.config.lengthMax }}
          message: lengthMin must not exceed lengthMax
```

- `kind` is served at its plural, as the Addon's own kind is, and is unique across the platform.
- `target` is a string field under `spec.config` that the schema requires.
- `function.ref` names the kind's Function in the target's namespace.
- `schema` checks each object's `spec.config`, with a 422 naming the property.
- `constraints` are checked when an object is written, under the namespace's lock, and a refusal is 400:
  - `count.perTarget` and `count.perNamespace` bound the objects of the kind;
  - `unique` lists fields whose value no two objects of the namespace share;
  - `immutable` lists fields that a replace cannot change;
  - each `rules` entry is a `${{ }}` condition over `object`, `oldObject` on a replace, and `others`, the namespace's
    other objects of the kind, of which a rule can read the length.

A settings object takes no `bindings`, `ingress`, `resources` or `deletion`. Settings objects count toward
`addons.maxSettingsPerNamespace`, 200 by default, and not toward the engines' `addons.maxObjectsPerNamespace`.

### The kind's Function

The kind's Function is a Function of the target's namespace that binds the target in `spec.bindings.addon`; funcd
creates nothing for it, and a target may name its own in `spec.functions`. funcd calls it with each target's full
list, as a CloudEvent of type `funcd.addon.settings` whose data holds `kind`, `cause` (`start`, `change`, `retry` or
`resync`), `target` (its `kind`, its `name` and the `alias` that binds it) and `objects` (each one's `name`, `uid`,
`generation` and `config`). The Function applies the whole list through `ctx.addon.fetch`, idempotently, puts the
engine's default back for an empty list, and answers `{"results": [...]}`, each result an object's `name`, `ok`, and a
`reason` and a `message` when it is not ok, in at most 1 MiB. A call past `addons.settingsTimeout`, 30 seconds by
default, fails, and funcd calls again after a backoff. rauthy's is
[`settings/rauthy-settings.mjs`](https://github.com/pyvvo/funcd-addons/blob/main/rauthy/settings/rauthy-settings.mjs).

## Test it under funcdctl dev

`funcdctl dev` runs an add-on's engine on the local Docker or containerd, and its admin token registers an Addon:

```sh
funcdctl --token funcd-dev-admin-token apply -f rauthy/app/addon.yaml
funcdctl rauthy keys --admin-email admin@login.localhost
funcdctl apply -f identityprovider.yaml
funcdctl get identityproviders -n default
```

funcd-addons' `just e2e` runs each add-on this way in CI: registration, the engine, the edge, a backup and a restore,
and a sign-in through rauthy.

## Give it a funcdctl plugin

A [funcdctl plugin](../../tools/funcdctl/plugins.md) is where an add-on's own chores go. rauthy's needs keys that a
shell cannot make, an argon2id password hash among them, so `funcdctl rauthy keys` generates them and pipes the
Secret to `$FUNCDCTL apply -f -`. `funcdctl rauthy urls` reads an IdentityProvider and prints its issuer, account and
admin URLs. Each funcd-addons release builds the plugins for Linux and macOS and attaches them.

## Publish it

An add-on of funcd's lives in its own folder of funcd-addons: `app/`, its App template, which holds `addon.yaml`,
`app.yaml` and `resources/`; `settings/`, the code of its settings kinds' Function; `examples/`, `README.md` and
`plugin/funcdctl-<addon>/`. Its CI checks every manifest (block style, an image pinned by digest, known kinds), tests
the plugin and runs the e2e; release-please tags each release, which attaches `<addon>.yaml`.
