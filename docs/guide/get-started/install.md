---
title: Install
description: Install funcdctl on macOS or Linux, and the funcd server on Linux.
icon: Download
---

These pages describe the latest release of funcd. funcd has two binaries: `funcdctl`, the CLI, runs on macOS and
Linux, and `funcd`, the server, runs on Linux.

## Install funcdctl

```sh
curl -fsSL https://pyvvo.github.io/funcd/install.sh | sh
```

The [install script](https://github.com/pyvvo/funcd/blob/main/scripts/install-funcdctl.sh) downloads the latest
release of funcdctl for the current system, in the dev build, which also runs Functions on the local machine with
`funcdctl dev`. It checks the download against the release's `SHA256SUMS` and installs it as
`/usr/local/bin/funcdctl`, through sudo when that folder is not writable. Check that it runs:

```sh
funcdctl --help
```

To install another release, set `FUNCDCTL_VERSION`:

<!-- x-release-please-start-version -->
```sh
curl -fsSL https://pyvvo.github.io/funcd/install.sh | FUNCDCTL_VERSION=v0.41.0 sh
```
<!-- x-release-please-end -->

A funcd server needs only the smaller build, `funcdctl-<version>-<os>-<arch>`, from the same
[release](https://github.com/pyvvo/funcd/releases/latest).

## Install the funcd server

funcd runs on Linux as one binary and a systemd unit. A release does not include the funcd binary. Build it from source
with Go, at the version that funcd's `go.mod` names:

```sh
git clone https://github.com/pyvvo/funcd && cd funcd
./scripts/build.sh
sudo install -m0755 dist/funcd /usr/local/bin/funcd
funcd version
```

Install the service next. `funcd install` sets up the containerd runtime, which isolates each Function. It also writes
and enables a systemd unit that runs funcd as root:

```sh
sudo funcd install
```

The process runtime runs Functions without isolation, for trusted code only, and uses another unit. The
[full install guide](https://github.com/pyvvo/funcd/blob/main/docs/install.md) covers it, along with access from
another machine, upgrades and removal.

## Check the server

A new funcd answers only on its own machine. Its API listens on `127.0.0.1:8080` and accepts the built-in token
`funcd-dev-token` there:

```sh
funcdctl --server http://localhost:8080 --token funcd-dev-token get functions -n default
```
