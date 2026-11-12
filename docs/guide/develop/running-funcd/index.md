---
title: funcd
description: Start the funcd server, point it at its configuration file, and find every key and command it takes.
---

## Usage

funcd is the server: one binary that runs the control plane, the data plane and the Functions. On Linux,
`sudo funcd install` sets it up as a systemd service, as [Install](../../get-started/install.md) shows.

funcd reads its settings from one file, `funcdconfig.yaml`: the file that `--config` names, else the one that
`FUNCD_CONFIG` names, else `./funcdconfig.yaml`, else `/etc/funcd/funcdconfig.yaml`. Most keys can also be set through
the variable that the [Spec](spec.md) tab lists beside them. `funcd --memory` keeps everything in memory, with nothing
on disk.

The Spec tab lists every key of the file with its type, default, limits and variable, then every `funcd` command with
its flags.
