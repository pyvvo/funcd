# Installing funcd

funcd ships as a **single binary** plus a **systemd unit** (ADR-0026). These instructions
cover Debian/Ubuntu and RHEL/Fedora; the steps are identical except where noted.

There are two ways to run it:

- **The containerd runtime**, the isolation boundary for untrusted functions: `funcd install` lays down crun and a
  private containerd and writes one systemd unit that runs funcd as root (ADR-0054, ADR-0056). See step 2.
- **The process runtime**, for trusted functions only: the unit in `configs/systemd/` runs funcd as an unprivileged
  user (ADR-0011). See step 3.

## 1. Get the binary

Build the daemon from source; a release attaches only the `funcdctl` binaries:

```sh
git clone https://github.com/pyvvo/funcd && cd funcd
./scripts/build.sh                 # → dist/funcd (pure-Go dev build)
sudo install -m0755 dist/funcd /usr/local/bin/funcd
funcd version                      # prints the stamped build identity
```

> **Release build.** The production binary is the same pure-Go static build (`CGO_ENABLED=0`) — ADR-0065
> made the metastore engine pure-Go Badger, so there is no cgo/`-tags slatedb` lane anymore.

## 2. The containerd runtime: `funcd install`

```sh
sudo funcd install --print         # prints the version-stamped funcd.service and changes nothing
sudo funcd install                 # lays down crun and the private containerd, writes and enables funcd.service
```

`funcd install` is idempotent. It writes one unit, `/etc/systemd/system/funcd.service`: funcd is the service and
containerd its managed child, with no separate containerd unit. The unit runs as root, which the private containerd
needs for cgroups, network namespaces and mounts; it restarts on failure except safe mode's exit status 70
(ADR-0207). `sudo funcd uninstall` removes the service again. Then go to step 4.

## 3. The process runtime: the bundled unit

### Create the service user and state directory

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin funcd   # Debian/Ubuntu
# RHEL/Fedora: sudo useradd --system --no-create-home --shell /sbin/nologin funcd
```

`systemd` creates and owns `/var/lib/funcd` automatically via `StateDirectory=funcd`
(the Badger metastore writes there — `<dataDir>/store`, ADR-0065).

### Install and enable the unit

```sh
sudo install -m0644 configs/systemd/funcd.service /etc/systemd/system/funcd.service
sudo systemctl daemon-reload
sudo systemctl enable --now funcd
systemctl status funcd
journalctl -u funcd -f             # structured slog output
```

The unit runs as the unprivileged `funcd` user, restarts on failure except safe mode's exit status 70 (ADR-0207,
`examples/restore-runbook.md`), and ships hardened
(`NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`, …). It has no capability for network namespaces: the
containerd runtime runs from the unit of step 2.

> **Runtime.** The unit runs the default `runtime.mode: process`, which is not an isolation boundary (ADR-0011):
> every function runs as the `funcd` user and can read what that user can. Use it only for trusted functions;
> untrusted ones need `runtime.mode: containerd` (Linux, root). The daemon logs a warning at startup in process mode.

## 4. Drive it

Use the CLI (ADR-0024): download `funcdctl-<version>-<os>-<arch>` from a release and check it against the release's
`SHA256SUMS`, or build it with `go build ./cmd/funcdctl`:

```sh
funcdctl --server http://localhost:8080 get functions -n default
```

## Upgrade

Run the upgrade with the installed binary, as root (ADR-0207):

```sh
sudo funcd upgrade ./funcd-<new version>
```

It runs `<new binary> version` and refuses an older release, checks that `--unit` (default `funcd.service`) runs this
binary, and stops the unit. It then writes a `pre-upgrade` backup generation of the metastore, run state and event
store, which this version can restore (the platform backup must be on: `backup.target`). It installs the new binary,
keeping the old one at `<path>.previous`, records the upgrade in `<dataDir>/safemode.json` and starts the unit. It
prints the generation as `<timeline>/<n>`: the way back (`examples/restore-runbook.md`, "Roll back an upgrade").

- A failure before the swap starts the old binary again; a held platform is refused until `funcdctl hold release`.
- `--config` must name the daemon's config: the unit may set `FUNCD_DATA_DIR`, and this shell may not.
- `--unit ""` leaves stopping and starting funcd to you; a funcd still running is then refused.
- `--no-snapshot` upgrades without a generation, so without a way back.
- funcd deletes no pin: it lists those older than the newest `backup.retention.preUpgrade` (3) with their prune
  command (`examples/backup-lifecycle.md`).

From the release with ADR-0294, a worker on the containerd runtime has a read-only root: a Function writes to `/tmp`
(a tmpfs counted in its memory), to `$HOME`, which is `/tmp` unless its spec sets `HOME`, and to its volumes. Code that
wrote elsewhere fails with "read-only file system". The default `lake.catalogMaxSize` is 256MiB: a Lake whose catalog
is larger needs the setting, else its saves are refused, and it does not start once its catalog no longer fits its
scratch (`lake.scratchSize`).

From the release with ADR-0300, the event store's directory is `<dataDir>/eventstore` and its key
`eventing.eventstore.dataDir`; the old key fails to load. Upgrade to it in four steps, so the installed binary still
snapshots the store from `deadletter`:

```sh
sudo systemctl stop funcd
sudo funcd upgrade --unit "" ./funcd-<new version>
sudo mv <dataDir>/deadletter <dataDir>/eventstore    # and rename eventing.deadletter.dataDir if the config sets it
sudo systemctl start funcd
```

A start without the move begins on an empty event store: its dead letters are gone, every blob EventSource fires again
for each object under its prefixes, and the SQL change feed publishes again any group its engine resends.

## A full data disk

funcd keeps `storage.minFreeSize` (2GiB by default) free on the filesystem of `storage.dataDir` (ADR-0296). Below it,
workload writes (a KV put, a Bucket upload, a SQL write, a Lake commit or save) get 507, and funcd logs once when the
free space falls below the setting and once when it is back above. Reads, deletes and funcd's own writes go on. Once
space is freed, writes are admitted again within a second.

A start of funcd needs about 640 MiB free for its stores (ADR-0297). Before it opens them, it removes the Lake scratch
images an earlier run left under `<dataDir>/scratch`, then logs the free space every 10 seconds until that much is free;
the wait counts no failed start in safe mode. A Lake engine of an earlier run that still runs keeps its scratch until
funcd stops it: if the start waits for that space, end the engine's processes, which run in the cgroup
`/sys/fs/cgroup/funcd-<namespace>/<engine container>` (`echo 1 | sudo tee <that cgroup>/cgroup.kill`), or free other
space.

## Uninstall

After `funcd install`, run `sudo funcd uninstall`. After the bundled unit:

```sh
sudo systemctl disable --now funcd
sudo rm /etc/systemd/system/funcd.service /usr/local/bin/funcd
sudo systemctl daemon-reload
# state in /var/lib/funcd is left in place; remove it manually if desired
```
