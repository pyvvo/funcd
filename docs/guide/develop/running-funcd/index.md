---
title: funcd
description: Start the funcd server, give it its settings in a file or in variables, and find every key and command it takes.
---

## Usage

funcd is the server: one binary that runs the control plane, the data plane and the Functions. With no configuration
file, it starts with every key at its default. `funcd --memory` keeps the platform's data in memory, so that it
is lost when funcd stops, which suits a test or a first try. funcd still writes some working files, such as its
sockets, under `storage.dataDir`.

With no `auth` keys, funcd accepts the built-in token `funcd-dev-token`, in the namespace `default`, and only on
loopback. To reach funcd from another machine, set `server.listenAddr` and `auth.credentials`, which the
[Spec](spec.md#funcdconfigyaml) tab describes.

## The configuration file

funcd reads its settings from one file, `funcdconfig.yaml`: the file that `--config` names, else the one that
`FUNCD_CONFIG` names, else `./funcdconfig.yaml`, else `/etc/funcd/funcdconfig.yaml`. The file is optional and sets
only the keys that differ from their defaults; a key that funcd does not know is an error. This file keeps the data on
disk, the KV data included, and runs each Function in containerd:

```yaml
apiVersion: funcd.io/v1alpha1
kind: FuncdConfig
server:
  listenAddr: 127.0.0.1:8080
  dataPlaneAddr: 127.0.0.1:8081
storage:
  mode: file
  dataDir: /var/lib/funcd/data
kvstore:
  engine: badger
runtime:
  mode: containerd
```

`storage.mode: file` keeps funcd's own data on disk, but the KV data lives in memory, and is lost when funcd stops,
unless `kvstore.engine` is `badger`.

[`examples/funcdconfig.yaml`](https://github.com/pyvvo/funcd/blob/main/examples/funcdconfig.yaml) shows every key
with its default, and the [Spec](spec.md) tab lists each one with its type, limits and variable.

Behind a proxy or a tunnel, `server.trustedProxies` names the hops whose word the edge takes for a request's client
address, which the rate limit's `clientIP` key, the access log and the `X-Forwarded-For` a backend gets use. This entry
trusts the engines of a tunnel add-on of kind `Tunnel`, which set `X-Real-IP` to the visitor, and this one a CDN's range,
whose `X-Forwarded-For` the edge walks from the right:

```yaml
server:
  trustedProxies:
    - addonKinds:
        - Tunnel
      header: X-Real-IP
    - cidrs:
        - 203.0.113.0/24
```

The first entry that matches the connection's peer decides, and with none the peer is the client. A backend gets the
client address alone in `X-Forwarded-For`, and none of the caller's own `X-Forwarded-For`, `Forwarded` or `X-Real-IP`.
No header sets the edge's scheme or host.

## Keys from the environment

Every key but `apiVersion`, `kind`, `auth.credentials` and `server.trustedProxies` also has a variable, which the Spec
tab names beside it:
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
