# Installing funcd

funcd ships as a **single binary** plus a **systemd unit** (ADR-0026). These instructions
cover Debian/Ubuntu and RHEL/Fedora; the steps are identical except where noted.

## 1. Get the binary

Either download a release binary, or build from source:

```sh
git clone https://github.com/green-0-rabbit/funcd && cd funcd
./scripts/build.sh                 # → dist/funcd (pure-Go dev build)
sudo install -m0755 dist/funcd /usr/local/bin/funcd
funcd version                      # prints the stamped build identity
```

> **Release build (slatedb engine).** The production single binary static-links the slatedb
> native engine (cgo, ADR-0006) and is no longer a pure-Go static binary (~24 MB). Build it with
> `just slatedb-lib` then `CGO_ENABLED=1 go build -tags slatedb -o dist/funcd ./cmd/funcd`
> (see the release note in `scripts/build.sh`).

## 2. Create the service user and state directory

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin funcd   # Debian/Ubuntu
# RHEL/Fedora: sudo useradd --system --no-create-home --shell /sbin/nologin funcd
```

`systemd` creates and owns `/var/lib/funcd` automatically via `StateDirectory=funcd`
(the slatedb file backend writes there — `file:///var/lib/funcd`, ADR-0006).

## 3. Install and enable the unit

```sh
sudo install -m0644 configs/systemd/funcd.service /etc/systemd/system/funcd.service
sudo systemctl daemon-reload
sudo systemctl enable --now funcd
systemctl status funcd
journalctl -u funcd -f             # structured slog output
```

The unit runs as the unprivileged `funcd` user, restarts on failure, and ships hardened
(`NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`, …). The function-worker lane
(F12/F13) later adds `CAP_NET_ADMIN` for netns wiring — see the comment in the unit.

## 4. Drive it

Use the CLI (built alongside via `go build ./cmd/funcdcli`, ADR-0024):

```sh
funcdcli --server http://localhost:8080 get functions -n default
```

## Uninstall

```sh
sudo systemctl disable --now funcd
sudo rm /etc/systemd/system/funcd.service /usr/local/bin/funcd
sudo systemctl daemon-reload
# state in /var/lib/funcd is left in place; remove it manually if desired
```
