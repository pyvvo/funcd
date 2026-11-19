---
title: Plugins
description: A funcdctl plugin is an executable named funcdctl-<name> on PATH, which funcdctl runs as a command of its own.
---

## Usage

A plugin adds a command to funcdctl without a new release, as a kubectl plugin does for kubectl. `funcdctl <name>`
runs the executable `funcdctl-<name>` that PATH finds, and passes it every word after the name.

This plugin lists the Functions of a namespace, `default` when none is given:

```sh
#!/bin/sh
exec "$FUNCDCTL" get functions -n "${1:-default}"
```

Save it as `funcdctl-fns` in a folder on PATH, make it executable, and run it:

```console
$ sudo install -m 0755 funcdctl-fns /usr/local/bin/
$ funcdctl fns team-a
$ funcdctl plugin list
NAME   PATH
fns    /usr/local/bin/funcdctl-fns
```

A plugin can be written in any language. It reaches funcd through funcdctl, as above, or through the API at
`FUNCD_SERVER` with `FUNCD_TOKEN`.

## What a plugin receives

The plugin inherits funcdctl's environment, with these variables set:

| Variable | Value |
|---|---|
| `FUNCDCTL` | the path of the funcdctl that ran the plugin |
| `FUNCDCTL_VERSION` | funcdctl's version, `dev` for a build with none |
| `FUNCD_SERVER` | `--server`, else `$FUNCD_SERVER`, else `http://localhost:8080` |
| `FUNCD_TOKEN` | `--token`, else `$FUNCD_TOKEN`; unset when neither gives a token |

The flags before the plugin's name are funcdctl's, so `funcdctl --server http://funcd:8080 fns team-a` hands the
plugin that server. Every word after the name is the plugin's, flags included.

## Rules

- **The name** is a plain word: a lowercase letter, then lowercase letters, digits and dashes.
- **funcdctl's own commands win.** A plugin named after a command, an alias, `help` or `completion` never runs, and
  `funcdctl plugin list` warns about it.
- **The first on PATH wins.** `funcdctl plugin list` warns about each later plugin of the same name.
- **The plugin replaces funcdctl's process.** Its exit code, its input and output and the signals it gets are its own.
  When it cannot start, funcdctl prints the error and exits 1.
- **Nothing runs from the working directory.** A relative entry of PATH, such as `.`, is skipped, and a name with a
  slash is never looked up as a path.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `unknown command "fns"` | No executable `funcdctl-fns` is on PATH, or PATH reaches it only through a relative entry. Put it in an absolute PATH folder and make it executable. |
| `funcdctl plugin list` warns that a plugin never runs | A built-in command or an earlier PATH entry takes its name. Rename the plugin, or reorder PATH. |
| The plugin talks to the wrong server | Flags before the plugin's name set `FUNCD_SERVER` and `FUNCD_TOKEN`; flags after it are the plugin's own. |
