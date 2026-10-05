# Fix review: issue #706, rework 2 (claude-opus-5-5)

- **Issue**: #706, containerd Stop leaks the bridge masquerade rules
- **Change**: commit ecd9af7a `fix(containerd): pin each worker's netns so its CNI DEL always runs in it`, on
  `fix/w15c-735-runtime` (PR #748), on top of 07440099. It amends 002611bf, which rework 1 reviewed.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**: 0 Blocker, 0 Major, 2 Minor (both attributed to the model)
- **Definition of Done**: 11 of 11

## What the rework had to do

Rework 1 returned one Major and two Minors:

- **Major 1**: after a reboot, the boot sweep sent the DEL with a pin file that had no netns mounted on it. The bridge
  plugin fails such a DEL before it frees the IP, so the host-local lease leaked.
- **Minor 1**: the failed-Setup path and the failed-pin path had no test with pins.
- **Minor 2**: `stop`'s container-NotFound path and the pin sweep dropped a pin without a DEL.

This commit adds an `isNetns` seam to `netnsPins`. In production it calls `statfs` and checks for `NSFS_MAGIC`.
`pinned()` now removes a pin file that has no netns mounted on it and returns `""`. A new `driver.release(ctx, cniID)`
sends the DEL in the pin, or with `""` when no live pin is left, and then unpins. `discard`, the NotFound path of
`stop`, and `SweepAll` (through the new `unheld(held)`) all call `release`. Three new tests cover the reboot, the gone
container and the failed Create.

## Verification run

Linux tests ran in Docker `golang:1.26.4` as uid 1000, offline, against a read-only module cache. Host checks ran
through `scripts/agent/d`. Mutants are overlay replacements of one line each.

| Check | Result |
|---|---|
| The six `TestIssue706_*` tests with the fix, `-race` (Linux) | all PASS, including both subtests of `FailedCreateDropsItsPin` |
| Full revert (HEAD~1 `containerd_linux.go`, `netns_linux.go` removed) | build fails, because the tests reference the new seam. The behavioral revert is k5 |
| k5: Create passes `/proc/<pid>/ns/net` instead of the pin (behavioral revert) | **killed**: `ExitedWorkerCNIDelRunsInItsPinnedNetns` fails at cniid_linux_test.go:83, because the DEL's netns no longer exists. That is the issue's reason. Both boot-sweep tests fail too |
| k1 (Major 1): `pinned()` skips the `isNetns` check | **killed**: `BootSweepAfterRebootSendsNoStalePin` (cniid_linux_test.go:131) |
| k2 (Minor 2): `stop`'s NotFound path drops `d.release` | **killed**: `StopOfGoneContainerReleasesItsPin` (cniid_linux_test.go:150) |
| k3 (Minor 2): `SweepAll` unpins an unheld pin without a DEL (the rework-1 behavior) | **killed**: `BootSweepReleasesLeftoverPins` (cniid_linux_test.go:108) and `BootSweepAfterRebootSendsNoStalePin` |
| k4 (Minor 1): Create's failed-Setup path drops `d.netns.unpin(netnsPath)` (rework 1's surviving m5) | **killed**: `FailedCreateDropsItsPin/setup_fails` (createfail_linux_test.go:150) |
| k6: `release` sends `""` instead of the pin | **killed**: `BootSweepReleasesLeftoverPins` and `StopOfGoneContainerReleasesItsPin` |
| k7: the production `statfs` check inverted (`!=` instead of `==` against `NSFS_MAGIC`) | **survives**, see Minor 1 |
| Probe (an overlay-only test, not committed) with the real `newNetnsPins`, as uid 1000 | `isNetns("/proc/self/ns/net")` is true and a plain file is false. `pinned()` returns `""` for the plain file and removes it. A real `pin()` fails with "operation not permitted" and leaves no file behind |
| Full `internal/runtime/containerd` package, `-race` (Linux) | ok |
| `go build ./...` (host) | ok |
| `go vet` on the package: host, `GOOS=linux`, and in the Linux container | clean |
| golangci-lint on the package: host and `GOOS=linux` | 0 issues |

The real bind mount needs root, so the tests prove the mount and unmount through the `fakePins` seam. A real process
stands in for the task's init: the test kills it before Stop, and the DEL must still name a path that exists. This is a
sound proof of the ordering and the path. The kernel's netns lifetime under a real bind mount is not exercised. The
Lima lane would cover that, and it was not run (by rule). The probe covers the production `statfs` check and the
cleanup after a failed real mount, as a non-root user.

## Minor

### 1. The production `statfs` check has no test (model)

`newNetnsPins` checks for `NSFS_MAGIC` at netns_linux.go:28-31. Every test replaces `isNetns` through `fakePins`, so
k7, which inverts the check, passes the whole package. With k7, every live pin would look stale: `discard` and the
boot sweep would send their DEL with `""`, and the masquerade leak would come back for leftover workers. The check
needs no root to test, as the probe shows: `/proc/self/ns/net` is a netns and a plain file in `t.TempDir()` is not.
Add a short test of the real `newNetnsPins(dir).isNetns` for both cases.

### 2. The amended commit message no longer describes the whole change (model)

The commit was amended with `--no-edit`, so the message is still the one from rework 1. It says that `SweepAll`
"drops the pins no worker holds". `SweepAll` now sends their DEL first. The message also does not say that a pin file
with no netns behind it (after a reboot) is removed and that its DEL is sent with no netns. It names only two of the
five new tests. Update the body when the group is integrated, so `git log` explains why `pinned()` checks the
filesystem type.

## Verified correct (keep)

- **Major 1 is fixed at its cause.** `pinned()` accepts a pin only when a netns is mounted on it (netns_linux.go:67),
  and otherwise removes the file and returns `""`. This matches the upstream handling of a stale pin (containerd's
  `pkg/netns` `Closed()`). After a reboot the bridge plugin gets `""` again and frees the IP. Both k1 and the probe
  confirm it.
- **Every path that drops a pin sends its DEL first (check 13).** A live Stop, an exited Stop, a Stop of a gone
  container (containerd_linux.go:605), a failed Setup, `discard` (reclaim, Sweep and the boot sweep), and the unheld
  pins in `SweepAll` (containerd_linux.go:955) all go through a DEL before the unpin. A failed pin sets nothing up and
  still kills the task and deletes the container. Each of these cases has a test, and the mutants k2, k3, k4 and k6
  fail them.
- **One helper, no duplication.** `release` replaces the separate `Remove` and `unpin` pairs in `discard`, `stop` and
  `SweepAll`. `makeStateDir` is reused for the 0700 pin dir (ADR-0160). No new module dependency: `golang.org/x/sys`
  was already direct. containerd's `pkg/netns` was not reused, which is justified: it imports
  `containernetworking/plugins`, which is not in `go.mod`, and it names pins at random, while the boot sweep needs the
  CNI ID in the file name to send the DEL of an orphan pin.
- **Conservative sweep.** `SweepAll` releases unheld pins only when every namespace sweep succeeded. A container that
  `discard` kept may still run in its pin, so the early return is the safe choice.
- **The zero value pins nothing.** Drivers that tests build directly keep the `/proc` behavior, and `unheld` on an
  empty dir returns nothing. No existing assertion changed or weakened.
- **ADRs.** No ADR file changed. ADR-0179's old-form DEL still uses `""`, and ADR-0167's boot sweep and ADR-0160's
  0700 state dirs hold.
- **Shape.** The subject is `fix(containerd):`. The commit has `Refs #706` (the branch's first #706 commit has
  `Fixes #706`) and the attribution trailer, and covers one issue. The comments explain why, without narration.

## Recommendation

Pass. The rework closes rework 1's Major and both Minors. The crashed-task case, the reboot case and every sibling
path that drops a pin are fixed and proven, and the checks are green on the host and on Linux. Two cheap follow-ups
can ride the integration: a non-root test of the real `statfs` check (Minor 1) and a commit body that describes the
reboot handling (Minor 2).

## Ledger row

```json
{
  "issue": 706,
  "phase": "fix",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 2,
  "dod_passed": 11,
  "dod_total": 11,
  "report": "docs/reviews/issue-706-fix-claude-opus-5-5-rework2.md",
  "notes": "rework 2 (netns pin): reboot stale pin, gone-container Stop, orphan-pin DEL and failed-Create pin paths fixed; k1-k6 killed incl. behavioral revert k5 for the issue's reason; Minor(model) production statfs NSFS_MAGIC check untested (k7 survives, probe shows it correct as non-root); Minor(model) amended --no-edit message no longer describes the reboot handling"
}
```
