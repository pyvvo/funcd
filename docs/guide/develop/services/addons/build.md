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

An engine reaches nothing outward: funcd fences its network, and only funcd reaches its port.

## The Addon

rauthy's [`addon.yaml`](https://github.com/pyvvo/funcd-addons/blob/main/rauthy/addon.yaml), part by part:

- `kind: IdentityProvider` is the kind users declare, at `/apis/funcd.io/v1alpha1/namespaces/<ns>/identityproviders`.
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

## Test it under funcdctl dev

`funcdctl dev` runs an add-on's engine on the local Docker or containerd, and its admin token registers an Addon:

```sh
funcdctl --token funcd-dev-admin-token apply -f rauthy/addon.yaml
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

An add-on of funcd's lives in its own folder of funcd-addons: `addon.yaml`, `examples/`, `README.md` and
`plugin/funcdctl-<addon>/`. Its CI checks every manifest (block style, an image pinned by digest, known kinds), tests
the plugin and runs the e2e; release-please tags each release, which attaches `<addon>.yaml`.
