# Fix review: issue #706, rework 1 (claude-opus-5-5)

- **Issue**: #706, containerd Stop leaks the bridge masquerade rules
- **Change**: commit 002611bf `fix(containerd): pin each worker's netns so its CNI DEL always runs in it`, on
  `fix/w15c-735-runtime` (PR #748), on top of 07440099
- **Producing model**: claude-opus-5-5
- **Verdict**: **changes-requested**: 0 Blocker, 1 Major, 2 Minor (all attributed to the model)
- **Definition of Done**: 10 of 11

## What the rework had to do

The branch's first #706 commit sends the DEL before the task is killed. That fixes Stop of a live worker. A worker
whose task exited on its own still leaked, because `/proc/<pid>/ns/net` is gone once the init process is reaped. The
rework pins the netns at Create with a bind mount, passes the pin to the ADD and to every DEL, unpins after the DEL,
and has the boot sweep clean pins that an earlier run left.

## Verification run

Linux tests ran in Docker `golang:1.26.4` as uid 1000, offline against a read-only module cache. Host checks ran
through `scripts/agent/d`.

| Check | Result |
|---|---|
| `TestIssue706_*` with the fix, `-race` (Linux) | the 3 tests PASS |
| Full revert (HEAD~1 `containerd_linux.go`, `netns_linux.go` removed) | build fails: `undefined: netnsPins`. The tests reference the new seam, so the full revert proves nothing about behavior. The behavioral revert is m1 |
| m1: Create passes `/proc/<pid>/ns/net` instead of the pin (behavioral revert) | **killed**: `ExitedWorkerCNIDelRunsInItsPinnedNetns` fails at cniid_linux_test.go:83 because the DEL's netns no longer exists. That is the issue's reason. `BootSweepReleasesLeftoverPins` fails too |
| m2: stop drops `d.netns.unpin(sb.netnsPath)` | **killed** (cniid_linux_test.go:87) |
| m3: discard sends `""` instead of the pin | **killed** (cniid_linux_test.go:108) |
| m4: SweepAll drops `d.netns.sweep(held)` | **killed** (cniid_linux_test.go:109) |
| m5: Create's failed-Setup path drops `d.netns.unpin(netnsPath)` | **survives**, see Minor 1 |
| Full `internal/runtime/containerd` package, `-race` (Linux) | ok |
| `go build ./...` (host) | ok |
| `go vet` on the package: host, `GOOS=linux`, and in the Linux container | clean |
| golangci-lint on the package: host and `GOOS=linux` | 0 issues |
| Probe (an overlay-only test, not committed): pins made by a driver whose mounts are recorded, then the boot sweep runs on a driver with the real `newNetnsPins` | the DEL names the pin, and `statfs` reports magic `794c7630` (overlayfs), not `NSFS_MAGIC` `6e736673`. This is a pin file with no mount behind it, which is the state after a reboot (Major 1) |

The real bind mount needs root, so neither the regression test nor the probe makes one. Both tests prove the case
through the `mount`/`unmount` seam instead. A real process stands in for the task's init: the test kills it before
Stop, and the DEL must still name a path that exists. This is a sound proof of the ordering and the path, but the
kernel's netns lifetime is not exercised. The Lima lane would cover that, and it was not run (by rule).

## Major

### 1. After a node reboot, the boot sweep's DEL names a stale pin file, and the host-local lease leaks (model)

The pins live at `<StateDir>/netns`. `StateDir` is relative to the data dir, which is `/var/lib/funcd` under the
installer's systemd unit (`cmd/funcd/install.go`), so it is on persistent disk. A reboot drops the bind mounts but
keeps the empty pin files. The private containerd keeps the leftover containers, so the boot sweep's `discard` finds a
pin through `pinned()`, which only `Lstat`s the path. It then sends the DEL with that path.

In containernetworking/plugins v1.9.1, the bridge plugin's `cmdDel` falls back to `ipamDel()` only for
`NSPathNotExistErr`. A path that exists but is not a netns gives `NSPathNotNSErr` ("unknown FS magic"), and `cmdDel`
returns that error before `ipamDel()` (bridge.go cmdDel; pkg/ns/ns_linux.go `IsNSorErr`). libcni stops the list at
that error. Before this commit, `discard` passed `""`, and the bridge plugin at least released the IP. Now:

- the worker's host-local lease outlives the sweep. A revisioned CNI ID that is never created again keeps its IP for
  good;
- a later ADD under the same CNI ID most likely fails while that lease is held (not run here). Create's failed-Setup
  DEL in the fresh pin would then release it, so the failure would be one-time.

The probe above reproduces the input to that path: after a simulated reboot, the boot sweep sends the DEL with a pin
whose filesystem is not nsfs.

This is the common case after a reboot or a power loss with workers running. The regression is in a path the commit
changed (`discard`).

**Fix direction**: either keep the pins on tmpfs (a `/run/...` dir, as CRI does with `/var/run/netns`), or make
`pinned()` accept only a live netns mount (`statfs` magic `NSFS_MAGIC`) and otherwise remove the file and return
`""`, as before. containerd's `pkg/netns` `NetNS.Closed()` (containerd v2.3.1, already a dependency) shows the upstream
handling: it treats an `NSPathNotNSErr` pin as stale, removes it and reports the netns as closed. Add a test with a pin
file that has no mount behind it.

## Minor

### 1. Create's failed-Setup path and the pin-failure path are untested with pins (model)

`TestCreate_FailedNetworkSetupRemovesCNIAttachment` and `TestCreate_FailedNetworkSetupKillsTask`
(`internal/runtime/containerd/createfail_linux_test.go`) build a driver that has a zero-value `netnsPins`, so they
still use the `/proc` path. Mutant m5 removes the unpin after a failed Setup, and the whole package still passes. No
test makes `mount` fail and checks that Create kills the task and deletes the container. Use `fakePins` in a failing
Setup test and assert the DEL path and the unpin. Add one case where `mount` returns an error.

### 2. Two sibling paths drop a pin without a DEL (model, check 13)

- `stop` returns early when `LoadContainer` reports NotFound (containerd_linux.go:604-607). It sends no DEL and does
  not unpin. The worker is marked released, so its pin, and with it the worker's netns (the veth, and the masquerade
  rules that stay installed), lasts until the next `SweepAll`. The missing DEL predates this commit. The netns that
  the pin keeps alive is new.
- `netnsPins.sweep` unpins every pin that no worker holds, but it sends no DEL, even though the file name is the CNI
  ID. The pins that only `sweep` reaches are those whose container is gone, which is the case above. A daemon that dies
  between a DEL and its unpin, the case the commit message names, still has its container, so `discard` already
  handles it.

In both places, send the DEL in the pin before unpinning it, so the "always runs in it" in the subject holds.

## Verified correct (keep)

- **Cause, not symptom.** Create, stop, the failed-Setup path and `discard` all send the DEL in a netns that outlives
  the task, which is the pinning that the issue's root cause names. The pin is taken right after `NewTask`, before the
  task starts, so the PID it reads cannot have been reused yet. This also removes the late-DEL PID-reuse hazard.
- **No duplicated cleanup.** A pin failure goes through the same task-kill and container-delete branch as a Setup
  failure (containerd_linux.go:478-483). `makeBootRoot` becomes `makeStateDir(stateDir, name)` and is reused for the
  0700 netns dir. No new module dependency: `golang.org/x/sys` was already direct.
- **The zero value pins nothing.** Drivers that tests build directly keep the `/proc` behavior, so no existing
  assertion changed or weakened. The `createdTasks.pid` seam defaults to pid 1.
- **Unmount.** `unpin` uses `MNT_DETACH`, then removes the file. It ignores any path outside the pin dir (`/proc`
  netns, `""`), so the `held` map and Stop's unpin are safe under the zero value.
- **ADRs.** ADR-0179 keeps `""` for the old-form DEL, and its Scope lists the masquerade leak of `discard` as Out, not
  as a decision. The old-form DEL is unchanged, so nothing contradicts ADR-0179, ADR-0011 or ADR-0160, and no ADR file
  changed.
- **Shape.** The subject is `fix(containerd):`. The commit has `Refs #706` (the branch's first #706 commit has
  `Fixes #706`) and the attribution trailer, and covers one issue. The comments explain why, without narration.

## Recommendation

Send it back to `/fix` for Major 1. After a reboot, the boot sweep must not hand the bridge plugin a pin file with no
mount behind it. Close both Minors in the same rework: test the failed-Setup and failed-pin paths with pins, and send
a DEL before a pin is dropped in `stop`'s NotFound path and in `sweep`. Everything else holds: the crashed-task case
is fixed and proven, and the checks are green on the host and on Linux.

## Ledger row

```json
{
  "issue": 706,
  "phase": "fix",
  "model": "claude-opus-5-5",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 1,
  "minors": 2,
  "model_attributed": 3,
  "dod_passed": 10,
  "dod_total": 11,
  "report": "docs/reviews/issue-706-fix-claude-opus-5-5-rework.md",
  "notes": "rework 1 (netns pin): crashed-task DEL fixed and proven, m1-m4 killed; Major(model) a stale pin file after a reboot makes the boot sweep DEL fail with NSPathNotNSErr and leak the host-local lease; Minor(model) failed-Setup and pin-failure paths untested with pins (m5 survives); Minor(model) stop NotFound path and sweep drop pins without a DEL"
}
```
