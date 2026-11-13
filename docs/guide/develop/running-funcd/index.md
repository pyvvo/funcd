---
title: funcd
description: Start the funcd server, give it its settings in a file or in variables, and find every key and command it takes.
---

## Usage

funcd is the server: one binary that runs the control plane, the data plane and the Functions. With no configuration
file, it starts with every key at its default. `funcd --memory` keeps everything in memory, with nothing on disk,
which suits a test or a first try.

## The configuration file

funcd reads its settings from one file, `funcdconfig.yaml`: the file that `--config` names, else the one that
`FUNCD_CONFIG` names, else `./funcdconfig.yaml`, else `/etc/funcd/funcdconfig.yaml`. The file is optional and sets
only the keys that differ from their defaults; a key that funcd does not know is an error. This file keeps the data on
disk and runs each Function in containerd:

```yaml
apiVersion: funcd.io/v1alpha1
kind: FuncdConfig
server:
  listenAddr: 127.0.0.1:8080
  dataPlaneAddr: 127.0.0.1:8081
storage:
  mode: file
  dataDir: /var/lib/funcd/data
runtime:
  mode: containerd
```

[`examples/funcdconfig.yaml`](https://github.com/pyvvo/funcd/blob/main/examples/funcdconfig.yaml) shows every key
with its default, and the [Spec](spec.md) tab lists each one with its type, limits and variable.

## Keys from the environment

Every key but `apiVersion`, `kind` and `auth.credentials` also has a variable, which the Spec tab names beside it:
`FUNCD_LISTEN_ADDR` sets `server.listenAddr`, for example. A variable that is set wins over the file, and a flag wins
over both:

```text
flag > FUNCD_* variable > funcdconfig.yaml > default
```

## As a service

On Linux, `sudo funcd install` installs funcd as a single systemd service, which manages containerd as its child, and
`sudo funcd uninstall` removes it again. [Install](../../get-started/install.md) walks through it.

## Other commands

| Command | What it does |
| --- | --- |
| `funcd version` | Prints the build identity |
| `funcd install` | Installs funcd as a single systemd service |
| `funcd uninstall` | Removes the service that `funcd install` wrote |
| `funcd restore` | Lists, inspects and restores the platform's backup generations, offline; the restored platform starts held, as [Disaster recovery](../../platform/disaster-recovery.md) explains |
| `funcd bench` | Runs the benchmark and sustainability harness |

The Spec tab lists every command with its flags.
