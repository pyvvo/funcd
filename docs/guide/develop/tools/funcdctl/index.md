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

funcdctl comes in two builds. The dev build, which the [install script](../../../get-started/install.md) installs, adds
`funcdctl dev`, which runs Functions on the local machine with no server; the smaller build, for a server, does not.
The [Spec](spec.md) tab lists every command with its flags, and marks the ones that only the dev build has.
