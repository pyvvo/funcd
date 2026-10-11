# Installing funcd

funcd ships as a **single binary** plus a **systemd unit** (ADR-0026). These instructions
cover Debian/Ubuntu and RHEL/Fedora; the steps are identical except where noted.

## 1. Get the binary

Either download a release binary, or build from source:

```sh
git clone https://github.com/pyvvo/funcd && cd funcd
./scripts/build.sh                 # → dist/funcd (pure-Go dev build)
sudo install -m0755 dist/funcd /usr/local/bin/funcd
funcd version                      # prints the stamped build identity
```

> **Release build.** The production binary is the same pure-Go static build (`CGO_ENABLED=0`) — ADR-0065
> made the metastore engine pure-Go Badger, so there is no cgo/`-tags slatedb` lane anymore.

## 2. Create the service user and state directory

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin funcd   # Debian/Ubuntu
# RHEL/Fedora: sudo useradd --system --no-create-home --shell /sbin/nologin funcd
```

`systemd` creates and owns `/var/lib/funcd` automatically via `StateDirectory=funcd`
(the Badger metastore writes there — `<dataDir>/store`, ADR-0065).

## 3. Install and enable the unit

```sh
sudo install -m0644 configs/systemd/funcd.service /etc/systemd/system/funcd.service
sudo systemctl daemon-reload
sudo systemctl enable --now funcd
systemctl status funcd
journalctl -u funcd -f             # structured slog output
```

The unit runs as the unprivileged `funcd` user, restarts on failure except safe mode's exit status 70 (ADR-0207,
`examples/restore-runbook.md`), and ships hardened
(`NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`, …). The function-worker lane
(F12/F13) later adds `CAP_NET_ADMIN` for netns wiring — see the comment in the unit.

> **Runtime.** The unit runs the default `runtime.mode: process`, which is not an isolation boundary (ADR-0011):
> every function runs as the `funcd` user and can read what that user can. Use it only for trusted functions;
> untrusted ones need `runtime.mode: containerd` (Linux, root). The daemon logs a warning at startup in process mode.

## 4. Drive it

Use the CLI (built alongside via `go build ./cmd/funcdctl`, ADR-0024):

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

## Uninstall

```sh
sudo systemctl disable --now funcd
sudo rm /etc/systemd/system/funcd.service /usr/local/bin/funcd
sudo systemctl daemon-reload
# state in /var/lib/funcd is left in place; remove it manually if desired
```
