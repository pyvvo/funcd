---
title: Build an add-on
description: Package an engine built outside funcd as an add-on, with rauthy and its funcdctl plugin as the example.
---

## Usage

An add-on is a set of images and an `Addon` manifest. The manifest lists the runtimes the add-on ships: one entry may
run the kind the add-on adds, and tells funcd how to run, serve and back up one engine per object of that kind; other
entries are runtimes that Functions and Jobs name. funcd runs no code of the add-on in its own process.
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
([OIDC sign-in](../../security-and-networking/sign-in/index.md)) speaks OpenID Connect to any
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

On a server, only funcd reaches an engine's port, and the engine reaches nothing outward unless egress control is on
and an EgressPolicy names its object by kind and name, as
[Networking](../../security-and-networking/networking/index.md#allow-a-functions-outbound-connections) shows. Under
`funcdctl dev`, the engine runs on the local Docker or containerd network.

## The Addon

rauthy's [`addon.yaml`](https://github.com/pyvvo/funcd-addons/blob/main/rauthy/app/addon.yaml) ships one runtime,
`rauthy`, whose `workloads` hold `Engine` alone: one engine of its image runs per object of its kind. Its `image` and
its `attributes`, part by part:

- `kind: IdentityProvider` is the kind users declare, at `/apis/funcd.io/v1alpha1/namespaces/<ns>/identityproviders`.
  funcdctl takes it as `IdentityProvider`, `identityprovider` or `identityproviders`. A lower-case word that ends in
  s is read as a plural, so a kind that ends in s, such as `Redis`, is typed with its capital letter or as its
  plural, `rediss`.
- The entry's `image` is pinned by digest, from a registry the server's `addons.registries` lists.
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
Addon gives such a setting a kind of its own in its engine entry's `settings`. An object of a settings kind runs no engine: it names its
target, an object of the Addon's `kind` in the same namespace, and the kind's Function applies it to that target's
engine.

```yaml
settings:
  - kind: RauthyPasswordPolicy
    target: spec.config.provider
    resync: 5m
    function:
      image: ghcr.io/example/rauthy-settings@sha256:b8390e254d5f99832bdcfa0567d144725d1d96907c9da01dc5917f71c3989cba
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
- `target` is a string field under `spec.config` that the Function's contract requires. A replace of the Addon cannot
  change it.
- `function` names the kind's Function in one of two ways:
  - `image` is a Function's artifact, pinned by digest, from a registry that `addons.registries` lists. funcd creates
    the Function, and its contract is the kind's schema: a write whose `spec.config` breaks it gets a 422 that names
    the property.
  - `ref` is an existing Function of the target's namespace. funcd creates nothing, and no schema checks a write.
- `constraints` are checked when an object is written, under the namespace's lock, and a refusal is 400:
  - `count.perTarget` and `count.perNamespace` bound the objects of the kind;
  - `unique` lists fields whose value no two objects of the namespace share;
  - `immutable` lists fields that a replace cannot change;
  - each `rules` entry is a `${{ }}` condition over `object`, `oldObject` on a replace, and `others`, the namespace's
    other objects of the kind, of which a rule can read the length. Only a kind with an `image` takes rules, since its
    contract types what they read.

A settings object takes no `bindings`, `ingress`, `resources` or `deletion`. Settings objects count toward
`addons.maxSettingsPerNamespace`, 200 by default, and not toward the engines' `addons.maxObjectsPerNamespace`.

### The kind's Function

From an `image`, funcd creates one Function for a target once an object of the kind names it, and names it after both,
such as `login-rauthypasswordpolicy`. It runs the artifact's export `handle` on the runtime that the artifact declares,
binds the target under the alias `self` and the target's Secrets, of which it can read every key, and has no address.
It scales to zero after 5 minutes with no call. A kind whose Function needs another binding, such as a KVStore, names a
`ref` instead. With a `ref`, the namespace holds the Function, which binds the target in `spec.bindings.addon`. A target
may name its own Function for a kind in `spec.functions`, in place of either.

funcd calls the Function with each target's full list, as a CloudEvent of type `funcd.addon.settings` whose data holds
`kind`, `cause` (`start`, `change`, `retry` or `resync`), `target` (its `kind`, its `name` and the `alias` that binds
it) and `objects` (each one's `name`, `uid`, `generation` and `config`). The Function applies the whole list through
`ctx.addon.fetch`, idempotently, and answers `{"results": [...]}`, each result an object's `name`, `ok`, and a `reason`
and a `message` when it is not ok, in at most 1 MiB. A call past `addons.settingsTimeout`, 30 seconds by default,
fails, and funcd calls again after a backoff. For an empty list, the Function puts the engine's default back, and
throws when it cannot: once a call with an empty list ended well, funcd removes a Function that it created from an
`image`. rauthy's is
[`settings/rauthy-settings.mjs`](https://github.com/pyvvo/funcd-addons/blob/main/rauthy/settings/rauthy-settings.mjs).

The contract of an `image` describes this call, and what it says of one object's `config` is the kind's schema:

```yaml
runtime: nodejs22
input:
  type: object
  additionalProperties: false
  properties:
    kind:
      type: string
    cause:
      type: string
    target:
      type: object
      additionalProperties: false
      properties:
        kind:
          type: string
        name:
          type: string
        alias:
          type: string
    objects:
      type: array
      items:
        type: object
        additionalProperties: false
        properties:
          name:
            type: string
          uid:
            type: string
          generation:
            type: integer
          config:
            type: object
            additionalProperties: false
            properties:
              provider:
                type: string
              lengthMin:
                type: integer
                minimum: 8
              lengthMax:
                type: integer
                maximum: 128
            required:
              - provider
              - lengthMin
              - lengthMax
  required:
    - objects
output: {}
```

The contract of a Function that serves several kinds is a union of their calls, discriminated by `kind`, and each kind's
schema is its member's. funcd reads the contract when the Addon is registered. Until it can, the Addon is not `Ready`,
with `ArtifactNotReady`, and its objects are stored and wait with that reason.

## Engineless kinds

An add-on whose kind manages something outside funcd, such as a cloud stack that a Job applies, runs no engine: its
Addon's kind entry serves `Engineless` alone, with no image, and names the Function that reconciles the kind's objects:

::include[../../../../../api/types/v1alpha1/examples/addons/engineless.yaml#addon]

- `kind` is the kind that users declare.
- `function` names the Function that reconciles the objects, as a settings kind's does. From an `image`, funcd creates
  one for each object, such as `bunny-edge-infrastack`, which binds the Secrets and the Jobs of the object's
  `bindings`, and its contract is the kind's schema; a `ref` is an existing Function of each object's namespace. An
  object may name its own for its kind in `spec.functions`.
- `resync`, from 1s to 24h, 10m when unset, is how often funcd sends each list again with no change.
- An image of the entry, `config`, `settings` or an engine's attribute such as `port` is refused, and so is another
  workload beside `Engineless`. An Addon's kind keeps its workload: a replace cannot move it between `Engine` and
  `Engineless`.

### The engineless kind's Function

funcd groups a namespace's objects of the kind by their Function, one object for a Function it created from an `image`,
and calls it with each group's full list, sorted by name, as a CloudEvent of type `funcd.addon.objects` whose source is
`funcd://<namespace>/addon/<function>`, and whose data holds `kind`, `cause` and `objects`, each one's `name`, `uid`,
`generation`, `deleting` and `config`:

```json
{"kind": "InfraStack", "cause": "change",
 "objects": [{"name": "bunny-edge", "uid": "9c2e41aa", "generation": 3, "deleting": false,
              "config": {"variables": {"zone": "funcd-tunnel"}}}]}
```

The cause is `start`, `change`, `retry`, `resync` or `run`, the last once a JobRun of the namespace ends. The Function
must accept a cause it does not know. It binds no object: it applies each list idempotently through what it binds, such
as the Jobs it starts, reads which run ended from its own state, and answers as a settings kind's Function does,
`{"results": [...]}` in at most 1 MiB, each result an object's `name`, `ok`, and a `reason` and a `message` when it is
not ok. An object sent with `deleting: true` is one to destroy: only an ok to it lets the object go. An object that
left the list was retained, and the Function forgets it, leaving what it manages in place. A call past
`addons.settingsTimeout` fails, and funcd calls again after a backoff. InfraStack's is
[`reconciler/infrastack-reconciler.mjs`](https://github.com/pyvvo/funcd-addons/blob/main/infrastack/reconciler/infrastack-reconciler.mjs).

## Test it under funcdctl dev

`funcdctl dev` runs an add-on's engine on the local Docker or containerd, and a Function or Job on the add-on's
runtimes there too. It pulls the `image` of a kind's Function from its registry, as a server does. Its admin token
registers an Addon:

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
