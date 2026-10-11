# ADR-0230: The full lane run shares one Lima VM, reset between lanes

- **Status**: Implemented (2026-10-11; the implementation review passed,
  docs/reviews/adr-0230-implementation-claude-opus-5-5.md; accepted 2026-10-11, decided by the decider, judged twice)
- **Amended in place**: 2026-10-11 — exceptionally edited after acceptance (immutability waived by the decider for
  this ADR only, as for ADR-0089 and ADR-0199) to correct three statements the implementation review found wrong:
  where the containerd root lives (Decision 4), how the template runs the scripts (Decision 2), and the 10 s stop
  (Constraints). The decision is unchanged.
- **Date**: 2026-10-11
- **Deciders**: green-0-rabbit
- **Tags**: testing, e2e, venom, lima, tooling
- **Realizes**: [FEAT-0001/F46](../feat/0001-feat-v1.1.md) (declarative e2e via Venom: the full run's shared VM)
- **Source**: the 2026-10-11 experiment, in which one VM ran all 8 Venom lanes green in 7 min 16 s including one
  62.5 s boot, against about 12 min with a VM per lane. Decider, 2026-10-11: one shared VM for a full lane run.
- **Relates to** (unchanged): [ADR-0077](0077-declarative-e2e-venom.md) (Venom lanes; its `no-helper-scripts`
  scenario covers per-lane invoke helpers, not the shared lane scripts here) · ADR-0054 (funcd's managed containerd)
  · ADR-0141 (the example modules the lanes stage).

## Context & Need

`just lima-example-all` runs each Venom lane through `just lima-example <name>`, which builds the images and the
binaries, then creates, boots, provisions, runs and deletes a VM of its own (`justfile:218-275`,
`scripts/lima-lane.yaml`). The boot takes about 60 s, more than most suites. The purpose: run the full set faster
while each lane still starts on a clean platform, and keep the single-lane run as it is.

## Scenarios

- `scenario: full-run-shares-one-vm` — Given the Venom lanes that `scripts/lane.py --venom-lanes` lists, When `just
  lima-example-all` runs, Then the images and binaries are built once for them, one VM boots once, every lane runs
  its suite on it in registry order, the run prints `PASSED:` and `FAILED:` lines, and the VM is deleted at the end.
- `scenario: lane-starts-clean` — Given a lane that leaves workers, firewall tables, IP leases, data and journal lines
  (egress, s3), When the reset and the leak check run after it, Then every count of Decision 4 is zero.
- `scenario: failed-lane-does-not-stop-the-run` — Given a lane whose suite fails, When the run goes on, Then the next
  lane runs after the reset, and `FAILED:` names the failed lane.
- `scenario: single-lane-unchanged` — `just lima-example <name>` still boots and deletes a VM of its own.

## Scope

**In**: `lima-example-all` for the Venom lanes; a reset and a leak check; the provision and readiness steps moved
into scripts shared by the per-lane template and the shared run; `nftables` in the lane VM; funcd's own `ctr` in the
suites.

**Out**: the single-lane run; optional lanes (run alone); the metastore smoke, which keeps its own VM; the image
build (Docker, unchanged); CI, which runs no lane.

## Constraints & Decision drivers

- Stopping funcd is not a reset (measured): its workers keep running, and mounts, network namespaces, the bridge,
  IP leases, iptables lines, the egress nftables tables, data and journal lines stay. The journal leak failed a
  suite: duckdb counted two 403 lines that the s3 lane had written.
- SIGKILL ends funcd in 0.02 s, and the reset discards the platform anyway. A graceful stop took 10 s only because
  the lanes' unit lacked the installed unit's `KillMode=mixed`, fixed with this ADR's implementation (about 0.25 s now).
- `scripts/lane-lock.sh` allows one `funcd-bench*` VM at a time (`:54`, `:102-107`), as today.
- The reset may wipe `/var/lib/funcd/data` only because every lane config sets `dataDir: /var/lib/funcd/data`; the
  default is `/var/lib/funcd` (`internal/platform/config/config.go:423`).
- The suites run `sudo ctr`, which resolves to the `ctr` that Lima's default containerd install (nerdctl-full)
  puts in `/usr/local/bin`, not funcd's own: a hidden dependency, fixed on the way.

## Alternatives considered

1. **A privileged container per lane on colima**: measured to start in 8–12 s, but overlay on the container's
   overlay root is refused, cgroup controllers need a kind-style boot unit, and it depends on Docker. Rejected by the
   decider: no Docker dependency for running lanes.
2. **Keep a VM per lane**: no reset to maintain, but about 60 s per lane. Rejected.
3. **Snapshot and restore the VM between lanes**: the vz driver has no cheap snapshot, and the reset takes under a
   second. Not chosen.

## Decision

1. **One VM per full run.** `lima-example-all` takes the lane lock, builds the images and the binaries once, deletes
   any leftover VM named `funcd-bench-shared`, removes `<deps>/lane.tgz`, boots `funcd-bench-shared` from the lane
   template, runs every Venom lane in registry order, then deletes the VM, also on failure or interrupt and
   explicitly before the metastore smoke, which `lane-lock.sh` would otherwise refuse.
2. **Shared scripts.** `scripts/lane-provision.sh boot` installs funcdctl and `apt-get`s python3-yaml and
   `nftables` (only the reset and the leak check use `nft`; funcd itself uses netlink); `scripts/lane-provision.sh
   lane` runs only when `/mnt/funcd-deps/lane.tgz` exists (stage `/opt/lane`, tokens, registry, start `funcd-demo`,
   push, apply). `scripts/lane-ready.sh` waits for the lane's `ready` target and exits 0 when `/opt/lane/LANE` is
   absent. The template runs `lane-provision.sh` with Lima's `file:` (Lima passes no argument, and with none the
   script runs `boot` then `lane`), and installs `lane-ready.sh` with a `mode: data` provision step that its probe
   runs (Lima refuses a probe `file:`). The shared VM gets `boot` from the template at start; the shared run copies
   the scripts into the deps mount and runs `limactl shell funcd-bench-shared sudo bash
   /mnt/funcd-deps/lane-provision.sh lane` per lane.
3. **A lane on the shared VM**: `lane.py <lane> <deps>` stages its `lane.tgz`, then the lane part of the provision,
   the readiness script and its suite run, then the reset and the leak check, which run once after the boot too.
4. **The reset** (`scripts/lane-reset.sh`, as root): `systemctl kill -s KILL funcd-demo` and `systemctl reset-failed
   funcd-demo`; kill the workers through `cgroup.kill` in `/sys/fs/cgroup/funcd-*/` (containerd's default
   `/<namespace>/<id>`) and remove those cgroups; unmount the task mounts under `/var/lib/funcd/`; remove the worker
   network namespaces pinned under `<dataDir>/run/netns` (`internal/runtime/containerd/containerd_linux.go:298`),
   the `funcd0` link, `/var/lib/cni` and the CNI iptables lines; delete the nftables tables `inet funcd_egress` and
   `bridge funcd_lateral`; rotate and vacuum the journal; remove `/var/lib/funcd/data`, `/opt/lane` and
   `/etc/funcd/tokens`. It keeps `/var/lib/funcd/bin`. The containerd root lives under the data dir
   (`<dataDir>/containerd`, `internal/platform/config/config.go:578-580`), so removing the data dir removes it too,
   and each lane's funcd starts on a fresh one. The leak check
   (`scripts/lane-leakcheck.sh`) then counts funcd cgroups, task mounts, pinned namespaces, the `funcd0` link, leases
   in `/var/lib/cni/networks/funcd`, funcd's nftables tables, the three removed paths and the `funcd-demo` journal
   lines; any non-zero count fails the lane that just ran.
5. **Failures.** A failed suite is recorded and the run goes on. A failed reset or leak check stops the run, after
   the boot too, and the lanes not run are listed. A rerun of one lane is a single-lane run on a fresh VM.
6. **Suites** call funcd's own `ctr` (`/var/lib/funcd/bin/ctr`) in `e2e/env-echo.venom.yml` and
   `e2e/duckdb.venom.yml`, on both kinds of VM.

## Temporary workarounds

None.

## Contracts

| Entry point | Behavior |
|---|---|
| `just lima-example-all` | Decision 1–5 for the Venom lanes; `PASSED:` and `FAILED:` lines as today (`scripts/agent/lanes.sh:33` reads `FAILED:`); exit 1 if a lane failed |
| `just lima-example <name>` | unchanged: a VM of its own, through the same provision and readiness scripts |
| `scripts/lane-reset.sh` | Decision 4's reset, idempotent, exit 0 on a clean VM |
| `scripts/lane-leakcheck.sh` | prints each count of Decision 4; exit 1 when any is not zero |
| `scripts/lane-provision.sh`, `scripts/lane-ready.sh` | Decision 2 |

| Consumes | Exposes |
|---|---|
| `scripts/lanes.yaml`, `scripts/lane.py`, `scripts/lane-lock.sh`, `limactl` (flake-pinned), the deps mount | the recipes and scripts above |

## Implementation plan

1. Move the provision and probe bodies of `scripts/lima-lane.yaml` into `scripts/lane-provision.sh` and
   `scripts/lane-ready.sh` as Decision 2 says; add `nftables`.
2. Add `scripts/lane-reset.sh` and `scripts/lane-leakcheck.sh`, written to Decision 4.
3. Rewrite the `lima-example-all` recipe as Decision 1, 3 and 5 say.
4. Point the suites' `ctr` calls at funcd's own `ctr` (Decision 6).
5. Tests: `scripts/lanes_test.go` checks that each new script parses (`bash -n`), that the template calls the shared
   scripts, and that every lane config sets `dataDir: /var/lib/funcd/data`. Proof: a full shared run with every lane
   green and its timing; egress then fn-to-fn with the leak check at zero; a forced failure (a suite edited in a
   scratch copy) that the run reports and survives; one single-lane run unchanged.

**Definition of done**: the full shared run is green and faster than per-lane VMs; the single-lane run is unchanged;
`go test ./scripts/...` passes; the gate passes; no username or absolute path in a changed file.

## Review checklist

- [ ] One VM, `funcd-bench-shared`, per full run, deleted on success, failure and interrupt and before the metastore
      smoke; a leftover one is deleted before the boot; the lane lock is held throughout.
- [ ] The per-lane template and the shared run call the same provision and readiness scripts; a boot without a lane
      provisions nothing lane-specific.
- [ ] The reset and the leak check cover every item of Decision 4; a leftover fails the lane that just ran.
- [ ] `PASSED:`/`FAILED:` unchanged; a failed reset or leak check stops the run; the suites use funcd's own `ctr`.

## Consequences

- A full run takes about 7 minutes instead of about 12.
- The lanes share a kernel and a VM: a leftover the reset does not know about could reach the next lane; the leak
  check catches the known kinds and fails the lane that left them.
- A rerun of a failed lane uses a fresh VM, so a failure caused by the shared VM shows up as a lane that passes alone.

## Open questions

1. The metastore smoke and the optional lanes on the shared VM: a later change, once measured.

## References

- ADR-0077; `scripts/lanes.yaml`; the `venom-e2e` skill, which sends cross-cutting lane changes to an ADR; the
  experiment's logs and scripts (not committed).
