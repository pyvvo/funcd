---
title: funcdctl
description: The funcd CLI, which applies resources, reads their state and logs, and runs Functions on the local machine.
---

## Usage

funcdctl is the CLI of funcd. It applies resources to a funcd server and reads their state and logs, as kubectl does
for Kubernetes, and it builds and pushes Function artifacts. It reaches the server that `--server` names and
authenticates with `--token`, or with `FUNCD_SERVER` and `FUNCD_TOKEN`:

```sh
funcdctl --server http://localhost:8080 --token funcd-dev-token get functions -n default
```

`funcd-dev-token` is the built-in token of a server that sets neither `auth.token` nor `auth.credentials`. Such a
server accepts it only on loopback, in the namespace `default`; the [server's
keys](../../running-funcd/spec.md#funcdconfigyaml) set other credentials.

funcdctl comes in two builds. The dev build, which the [install script](../../../get-started/install.md) installs, adds
`funcdctl dev`, which runs Functions on the local machine with no server; the smaller build, for a server, does not.
With `--containers`, or `dev.containers: true` in `funcdctl.config.yaml`, `funcdctl dev` runs the Node and Python
Functions and Jobs in containers on Docker or nerdctl, from images that the dev build carries, so the machine needs no
Node or Python. A folder's `node_modules`, and any other directory a build links to, is bound read-only into the
container instead of copied at each save when the engine sees it and no link in it leads out of it: an engine on a
Linux host sees any directory, and one in a VM, as on macOS, those under the home directory.
With [`--lan`](spec.md#funcdctl-dev), or `dev.lan: true` in `funcdctl.config.yaml`, `funcdctl dev` also serves its
gateway on every address of the machine, so that a phone or another computer on the same network can open the Functions,
Routes and Sites that it serves; `--lan=<IP address>` serves the gateway on that one address only. The gateway then
answers anyone on that network, with no authentication, while the control plane and the S3 endpoint stay on 127.0.0.1.
The banner then names each gateway URL on the IPv4 address of the machine's default route when dev started, and a note at
its bottom names 127.0.0.1 and the machine's other IPv4 addresses, on which the same URLs answer; a fixed `--gport` keeps
the URLs the same across restarts. A Route or a Site with a host answers only on that host, so its URL keeps the host.
With `liveReload: true`, a Site page opened on another device does not reload when the Site changes.
The [Spec](spec.md) tab lists every command with its flags, and marks the ones that only the dev build has.
