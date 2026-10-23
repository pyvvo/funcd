# ADR-0268: Only the platform sets a volume's project id — a seccomp filter on every worker

- **Status**: Reviewing (2026-10-23), implemented, the volumes lane green; Accepted 2026-10-23, under the decider's "go ahead until completion" of 2026-10-23; Proposed
  2026-10-23 for a private finding, judged twice: round 1 one Blocker (a second ioctl) and three Majors, round 2 accept
- **Date**: 2026-10-23
- **Deciders**: green-0-rabbit
- **Tags**: runtime, containerd, seccomp, volume, quota
- **Realizes**: [FEAT-0014/F181](../feat/0014-feat-volumes.md)
- **Relates to**: [ADR-0262](0262-function-owned-volumes.md) (Decision 3's project quotas, whose ids this keeps the
  platform's) · [ADR-0011](0011-runtime-sandbox-port.md) (the worker's OCI defaults, which gain this filter) ·
  ADR-0264, in the DR session's work (the next change to the same OCI options)

## Context & Need

ADR-0262 limits a volume with an ext4 project quota: funcd gives each leaf a project id and a hard block limit. The
limit holds only while every file in the leaf keeps that id and the leaf keeps its inheritance flag, so only the
platform may set either.

## Scenarios

- `scenario: worker-cannot-set-project-id` — Given a worker with a volume, When it asks the kernel to set a file's
  project id, or the leaf's flags, Then the call fails with EPERM, and a write past the volume's size still fails.
- `scenario: high-bits-do-not-pass` — When the request carries the same command with its upper 32 bits set, Then it
  fails with EPERM too.
- `scenario: other-ioctls-pass` — Given a worker, When it makes any other ioctl (a terminal's, a socket's), Then the
  filter lets it through.

## Scope

**In**: one seccomp filter on every containerd worker. **Out**: the process runtime, which isolates nothing
(ADR-0262 Decision 10); any other system-call policy.

## Constraints & Decision drivers

- The kernel reads only the low 32 bits of an ioctl command, so a filter must compare under a 32-bit mask.
- Kernels from 6.17 have a second path, the `file_setattr` system call, which a libseccomp that does not know it would
  leave open.
- Nothing else a worker does may change, but setting a file's flags, which a seccomp rule cannot tell apart from
  clearing the inheritance flag.

## Alternatives considered

1. **Workers in a user namespace**: the kernel refuses a project-id change from one, but idmapped mounts and the
   rootfs change for every worker. Rejected for now.
2. **One loop image per volume** (ADR-0262 Alternative 2): no shared image to fill, but a loop device and a mount per
   volume. Rejected; the filter keeps one image.
3. **A filter on workers with a volume only**: one filter fewer, but two OCI shapes to keep. Rejected: every worker.

## Decision

1. **The filter.** Every containerd worker's OCI spec carries a seccomp filter with default action allow, the native and
   compat architectures (amd64: x86_64, x86, x32; arm64: aarch64, arm; any other: the native one only), and rules each answering `SCMP_ACT_ERRNO` with
   `ErrnoRet` EPERM: `ioctl` whose second argument, masked to 32 bits, is `FS_IOC_FSSETXATTR` (`0x401C5820`),
   `FS_IOC_SETFLAGS` (`0x40086602`) or its 32-bit form (`0x40046602`); and `file_setattr`.
2. **The leaf.** The platform alone sets a leaf's project id and flags, from the host (ADR-0262 Decision 3).
3. **The newer kernel.** On a kernel from 6.17 the daemon refuses at start to turn volumes on (see Temporary
   workarounds).
4. **Order.** This filter lands with ADR-0262's first release, and before ADR-0264's changes to the same OCI options.

## Temporary workarounds

- **Volumes refuse a kernel from 6.17**: the daemon fails at start when `volumes.capacityBytes` (or `volumes.capacity`)
  is set on such a kernel. Exit criterion: the `volumes` lane runs on a kernel from 6.17 and its probe gets EPERM from
  `file_setattr`.

## Contracts

```go
// internal/runtime/containerd/containerd_linux.go

// withProjectIDFilter is the seccomp filter of ADR-0268. Each ioctl rule is, literally:
//
//	{Names: ["ioctl"], Action: SCMP_ACT_ERRNO, ErrnoRet: EPERM,
//	 Args: [{Index: 1, Op: SCMP_CMP_MASKED_EQ, Value: 0xFFFFFFFF, ValueTwo: <command>}]}
//
// Value is the mask and ValueTwo the datum, as crun passes them to libseccomp.
func withProjectIDFilter(ctx context.Context, c oci.Client, ctr *containers.Container, s *specs.Spec) error
```

| Consumes | Exposes |
|---|---|
| `github.com/opencontainers/runtime-spec` (`LinuxSeccomp`, already a dependency) | the filter in every worker's OCI spec |

## Implementation plan

1. `ociOpts` appends `withProjectIDFilter`. `cmd/funcd` refuses volumes on a kernel from 6.17 (`uname` release).
2. Tests: a unit test of the spec's filter (`TestScenarioHighBitsDoNotPass` checks the mask); the `volumes` Lima lane
   runs a probe inside a real worker for `worker-cannot-set-project-id` and `other-ioctls-pass`.

**Definition of done**: the tests and the lane pass; the gate passes; no username or absolute path in a changed file.

## Review checklist

- [ ] Each ioctl rule compares under the mask `0xFFFFFFFF` (Value) with the command as ValueTwo, never the full argument.
- [ ] The three commands and `file_setattr` answer EPERM; the compat architectures are listed.
- [ ] The default action is allow; only the two rules answer EPERM.
- [ ] A worker without a volume starts and serves as before.
- [ ] One test per scenario, named for it.

## Consequences

- A volume's quota holds against its own worker.
- A worker can no longer set any file's flags (`chattr`, on any filesystem) or project id.
- Every worker carries a seccomp filter; a later system-call policy extends it.

## Open questions

None.

## References

- ADR-0262, ADR-0011; Linux `ioctl_fsfs`, `seccomp(2)`; FEAT-0014 F181, feature issue
  [#1187](https://github.com/pyvvo/funcd/issues/1187).
