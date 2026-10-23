# ADR-0262: Function-owned volumes — a size-limited directory only its Function can reach

- **Status**: Reviewing (2026-10-23), implemented; Accepted 2026-10-23, by the decider with the agent choices as proposed; Proposed 2026-10-23 from the
  decider's six refine cards, judged twice: round 1 found three Majors (the booking at apply, `ref` and ADR-0248, the
  restore member), round 2 one (the booking's lifecycle), all folded
- **Superseded in part by**: [ADR-0266](0266-volume-sizes-with-units.md) (2026-10-23) — the size fields of Decisions 1, 3
  and 5: `size`, `volumeBudget` and `volumes.capacity` beside their byte forms.
- **Date**: 2026-10-23
- **Deciders**: green-0-rabbit
- **Tags**: function, storage, volume, runtime, quota, backup
- **Realizes**: [FEAT-0014/F181](../feat/0014-feat-volumes.md)
- **Relates to**: reuses the host-directory rules of [ADR-0231](0231-durable-storage-for-add-on-engines.md) and the
  root-entry ownership of [ADR-0248](0248-stores-at-the-root.md), both unchanged ·
  [ADR-0032](0032-curated-runtime-images-container-execution.md) (bind mounts) ·
  [ADR-0143](0143-redeploy-by-revision-switch.md) (the redeploy overlap) ·
  [ADR-0046](0046-pooling-placement-policy.md) (pool access hash) · [ADR-0234](0234-sql-backup-and-restore.md) (the
  member-kind pattern) · [ADR-0206](0206-restore-and-held-boot.md) (the hold)

## Context & Need

A Function has no directory of its own. Its worker's root filesystem is a containerd snapshot deleted with the
container (`internal/runtime/containerd/containerd_linux.go:488`, `:689`), the spec has no volume field
(`api/types/v1alpha1/function.go:51`), and durable host directories exist only for add-on engines (ADR-0231). Code
that needs a file path (a model cache, a SQLite file) cannot keep it across a crash, a redeploy or a scale to zero.

This ADR lets a Function declare volumes: `emptyDir`, one per worker and gone with it, and `local`, durable and owned
like a store. Each has a hard size; no Function can reach another's volume or the volume root.

## Scenarios

- `scenario: volume-path-in-env` — Given Function `cart` with volume `state`, When its worker starts, Then
  `FUNCD_VOLUME_STATE` names a writable directory, `/var/funcd/volumes/state` in a container.
- `scenario: only-its-own-volume` — Given `cart` and `thumbs` in one namespace, each with a volume, When `cart`'s code
  walks up from its volume path, Then it finds neither `thumbs`'s volume nor the volume root.
- `scenario: write-past-size-fails` — Given `state` with `sizeBytes: 1048576`, When the code writes 2 MiB into it,
  Then the write fails and the earlier data stays readable.
- `scenario: local-survives` — Given a file in `cart`'s `local` volume, When the worker crashes, the Function is
  redeployed, or it scales to zero and wakes, Then the file is there.
- `scenario: emptydir-goes-with-worker` — Given a file in an `emptyDir`, When its worker is replaced, Then the new
  worker's `emptyDir` is empty and the old one's space is free.
- `scenario: capacity-not-overcommitted` — Given `volumes.capacityBytes` of 10 GiB and 9 GiB already reserved, When a
  Function declares a 2 GiB `local` volume, Then apply refuses it with reason `VolumeCapacity`; an `emptyDir` that
  does not fit leaves its worker unstarted with `Ready=False` `VolumeCapacity`.
- `scenario: namespace-budget` — Given namespace `shop` with `volumeBudgetBytes` 4 GiB and 3 GiB reserved, When a
  Function there declares 2 GiB, Then apply refuses it with reason `VolumeBudget`.
- `scenario: one-replica` — When a Function declares a `local` volume and `replicas: 2`, Then apply refuses it,
  naming `spec.replicas` and `spec.volumes[0]`.
- `scenario: retained-not-taken-over` — Given a retained `local` volume of a deleted `cart`, When `cart` is applied
  again with `name: state`, Then it is `Ready=False` `StoreNotOwned`; with `ref: state` it binds the old data.
- `scenario: volumes-off` — Given a daemon without `volumes.capacityBytes`, or on the process runtime, When a Function
  with a volume is applied, Then apply refuses it with reason `VolumesOff`.
- `scenario: dev-measures` — Given `funcdctl dev` and a volume past its size, When the next measurement runs, Then
  dev logs a warning naming the Function, the volume and both sizes, and the write itself succeeded.
- `scenario: volume-backup-restore` — Given a schedule whose scope names `cart/state`, When it runs and the host is
  later lost, Then `cart` comes back `Ready` with an empty `state` and `DataMissing=True`, and a `Restore` of that
  member fills `state` and clears the condition.

## Scope

**In**: the `spec.volumes` entry; the volume filesystem, its quotas and reservations; the namespace budget; the
path env var and the mount; the process-runtime rule; deletion, `ref` and the hold; the `Volume` backup member.

**Out**: a Bucket prefix as a directory (FEAT-0014 F182); sharing one volume between live Functions; more than one
node; resizing a volume in place; limits on what a worker writes outside its volumes.

## Constraints & Decision drivers

- **Isolation.** A Function must not reach a sibling volume or the root, on the containerd runtime. A process-runtime
  worker is a plain host process and can read whatever its OS user can, so the rule cannot hold there.
- **A hard limit from day one.** A measured limit lets a function write gigabytes between two checks.
- **No setup on a default host.** A stock Ubuntu ext4 has no project quotas, and turning them on needs the
  filesystem unmounted.
- **One principal per volume**: no Cedar check exists on file I/O, which goes straight to the kernel.
- The redeploy boots the new revision beside the old one (ADR-0143); a `local` volume sees both for that window.

## Alternatives considered

1. **A `path` field (ACA's `mountPath`)**: the process runtime cannot mount it, so portable code reads an env var
   anyway. Rejected for the env var alone.
2. **One loop image per volume**: a hard limit without quotas, but a loop device, a mount and an `mkfs` per volume, and
   per worker start for an `emptyDir`. Rejected for one image with project quotas.
3. **Project quotas on the data directory's own filesystem**: no image, but only after the operator reformats or
   remounts it. Rejected.
4. **A measured limit, stopping the Function past it**: works everywhere, but does not stop a malicious writer. Kept
   for `funcdctl dev` only, as a warning.
5. **Overcommit**: more volumes fit, but one Function can fill the image under the others. Rejected.
6. **Stop-then-start redeploys for `local`**: one writer always, at the cost of an outage per redeploy. Rejected; the
   code locks its files.
7. **No backup**: simpler, but host loss loses the data. Rejected for a workload-backup member.

## Decision

1. **The entry.** `spec.volumes[]` declares `name` (a DNS label, unique in the list) or `ref`, `type` (`emptyDir` or
   `local`), `sizeBytes` (required, > 0) and, for `local`, `deletion` (`retain`, the default, or `delete`;
   ADR-0248). `emptyDir` takes neither `ref` nor `deletion`; a `ref` entry takes no `deletion`.
2. **The path.** The code reads `FUNCD_VOLUME_<NAME>`, the name upper-cased as `FUNCD_LAKE_<ALIAS>_URL` is
   (`internal/function/lake.go:77`). In a container it is `/var/funcd/volumes/<name>`, a bind of the volume's leaf
   directory only, read-write `nosuid,nodev` (ADR-0231 Decision 2); in `funcdctl dev` it is the host directory.
3. **The volume filesystem.** On the containerd runtime, `volumes.capacityBytes` turns volumes on. At startup funcd
   creates, once, `<dataDir>/volumes.img` preallocated to that size, formats it ext4 with project quotas, and mounts
   it at `<dataDir>/volumes` with `prjquota`; later starts check and mount it. A `local` volume is
   `<ns>/<function>/<name>`, an `emptyDir` `<ns>/<function>/<name>.empty/<worker id>`; each leaf gets its own project
   id and a hard block limit of `sizeBytes`. A larger `capacityBytes` than the image grows it at startup; a smaller
   one fails startup `Invalid`. Under `storage.mode: memory` the image lives in a temporary directory removed on exit,
   as ADR-0231 Decision 5's root does.
4. **The host side never follows a link.** Path parts are validated DNS labels; each leaf is checked with `Lstat`
   (a symlink or non-directory is refused `Invalid`); only the leaf is chowned, to the runtime image's user; cleaning
   and measuring walk without following links.
5. **Reservations.** A `local` volume reserves `sizeBytes` from apply until it is removed; an `emptyDir` from its
   worker's create until the worker is removed, so replicas and the redeploy overlap count. The total never passes
   `capacityBytes`, nor a namespace's optional `spec.volumeBudgetBytes`. Apply refuses a `local` volume that does not
   fit (`VolumeCapacity`, `VolumeBudget`); a worker whose `emptyDir` does not fit is not created and the Function is
   `Ready=False` with that reason. One in-process allocator holds every booking under one lock: the `function-volumes`
   admission books a `local` size (no leaf, no quota) and refuses what does not fit; the reconcile pass's `Reserve`
   creates the leaf and its quota against that booking; worker create books and reserves an `emptyDir` at once. At
   startup the allocator is rebuilt from the stored Functions' `local` entries and the retained metas; a booking with
   no stored Function is dropped at the next pass; a changed `sizeBytes` on an existing `local` entry is refused
   `Invalid`.
6. **One writer.** A `local` volume requires `replicas` ≤ 1; apply names both fields otherwise. During a redeploy the
   old and new revisions hold it at once until the old one stops; the code locks its files (POSIX locks, SQLite's
   own, work on the image).
7. **Ownership.** A `local` volume's host metadata, `<ns>/<function>/<name>.meta` beside the leaf and outside every
   mount, records the owner's UID, the type, the size and the project id. `name` creates it with that UID and never
   takes over a volume with another UID (`StoreNotOwned`). `ref` adopts a retained volume only while no live Function
   holds it (else `Conflict`) and stamps the new UID: unlike an ADR-0248 `ref` store, which is never rewritten, so
   that a volume keeps one holder. `deletion: delete` removes the volume with its Function.
8. **Retained volumes.** A retained volume keeps its reservation until a `ref` binds it or an operator removes it
   (`funcdctl volume delete <ns>/<function>/<name>`, `DELETE /v1/namespaces/{ns}/volumes/{function}/{name}`, which
   refuses `Conflict` while a live Function holds it). The owner reclaims on delete and, at boot, the `emptyDir`
   leftovers; both are skipped while the platform is held, and retained `local` volumes without a Function are listed
   in `hold status` as restore evidence (ADR-0231 Decision 4, ADR-0206).
9. **Pooling.** Volumes join the pool access hash (`internal/pooling/pooling.go:75`), so a Function with a volume never
   shares a worker with another Function.
10. **Process runtime.** A `funcd` daemon on the process runtime, or without `volumes.capacityBytes`, refuses a
    Function with a volume at apply (`VolumesOff`). `funcdctl dev` gives each volume a plain directory under its dev
    data directory, measures every `volumes.measureInterval` (default 30 s) and logs a warning past `sizeBytes`.
11. **Backup.** `BackupScope.volumes` lists `<function>/<name>` of `local` volumes; an App's empty scope includes its
    Functions' `local` volumes. A backup writes a tar of the leaf, read live and file by file without following
    links, sealed like the other members: the copy is not a point in time. Both parts are DNS labels, without repeats,
    under ADR-0234's one-form rule. A `RestoreMember` names it as `kind: Function`, `name: <function>`,
    `volume: <name>`, with `as` (another Function's empty volume of that name) or `inPlace`; it writes only into an
    empty volume (else `Conflict`). Volumes are not in the platform export.
12. **Data missing.** A Function that was Ready, with a `local` volume absent at its first pass, gets an empty volume
    and `DataMissing=True`, cleared only by a Succeeded `Restore` into that volume (ADR-0234 Decision 6). Memory mode
    never sets it.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1/function.go
type VolumeType string

const (
	VolumeEmptyDir VolumeType = "emptyDir"
	VolumeLocal    VolumeType = "local"
)

type VolumeEntry struct {
	Name      ObjectName     `json:"name,omitempty"`
	Ref       ObjectName     `json:"ref,omitempty"`
	Type      VolumeType     `json:"type"`
	SizeBytes int64          `json:"sizeBytes" minimum:"1"`
	Deletion  DeletionPolicy `json:"deletion,omitempty"`
}

// FunctionSpec gains:
//	Volumes []VolumeEntry `json:"volumes,omitempty"`

// api/types/v1alpha1/namespace.go — NamespaceSpec gains:
//	VolumeBudgetBytes int64 `json:"volumeBudgetBytes,omitempty" minimum:"0"` // 0 ⇒ only volumes.capacityBytes

// api/types/v1alpha1/backup.go — BackupScope gains:
//	Volumes []string `json:"volumes,omitempty"` // "<function>/<name>"
// RestoreMember gains:
//	Volume ObjectName `json:"volume,omitempty"` // with Kind Function: the member is that Function's local volume

// internal/volume/volume.go (NEW)
type ID struct {
	Namespace v1.NamespaceName
	Function  v1.ObjectName
	Name      v1.ObjectName
	Worker    string // "" for a local volume
}

type Manager interface {
	// Book records sizeBytes for id under the allocator's lock, with no leaf; VolumeCapacity or VolumeBudget
	// (fault.Conflict) when it does not fit. Idempotent for the same id and size.
	Book(ctx context.Context, id ID, sizeBytes int64) error
	// Reserve creates or checks id's leaf and sets its quota against its booking (booking it first for an emptyDir);
	// StoreNotOwned when the meta names another UID.
	Reserve(ctx context.Context, id ID, owner v1.UID, sizeBytes int64) (hostPath string, err error)
	// Adopt stamps owner on a retained local volume; Conflict while a live Function holds it.
	Adopt(ctx context.Context, id ID, owner v1.UID) error
	Release(ctx context.Context, id ID) error                // removes the leaf and frees its booking
	Retained(ctx context.Context) ([]ID, error)              // local volumes with no live owner
	Tar(ctx context.Context, id ID) (io.ReadCloser, error)   // the backup member stream
	Untar(ctx context.Context, id ID, r io.Reader) error     // Conflict when the leaf is not empty
}

// pkg/funcd/options.go
func WithFunctionVolumes(m volume.Manager) Option // nil ⇒ volumes off (VolumesOff at apply)
```

| Consumes | Exposes |
|---|---|
| `volumes.capacityBytes`, `volumes.measureInterval`, `storage.dataDir`, the execution runtime (`cmd/funcd`) | `spec.volumes`, `FUNCD_VOLUME_<NAME>`, `/var/funcd/volumes/<name>` |
| `runtime.WorkerSpec.Mounts` (ADR-0032), `mkfs.ext4` on the host | reasons `VolumeCapacity`, `VolumeBudget`, `VolumesOff`; condition `DataMissing` |
| ADR-0248 markers, the pool access hash, `workload.Sources` | `BackupScope.volumes`, `RestoreMember.volume`, `funcdctl volume delete`, `DELETE /v1/namespaces/{ns}/volumes/{function}/{name}` |

## Implementation plan

1. `api/types/v1alpha1`: the types above, `Validate` (names, sizes, `emptyDir` without `ref`/`deletion`, `ref` without `deletion`),
   OpenAPI.
2. `internal/volume` (NEW): the image (create, preallocate, `mkfs.ext4 -O quota,project`, mount `prjquota`, grow),
   project ids and limits through `quotactl` and `FS_IOC_FSSETXATTR` (`golang.org/x/sys/unix`, already a dependency),
   the meta files, the allocator, `Tar`/`Untar`; a directory-only `Manager` for `funcdctl dev` that measures.
3. `internal/controlplane/admission`: `function-volumes` (off, replicas, and `Book` for capacity and budget).
4. `internal/function`: `Reserve` or `Adopt` in the reconcile pass, the mounts and env in `workerSpec` (both branches), release on
   worker removal and Function delete, `DataMissing`; `internal/pooling`: volumes in `AccessHashOf`.
5. `internal/backup/workload`: `Sources.Volume`; the restore path; `hold status` lists retained volumes.
6. `cmd/funcd`: the manager on containerd with `volumes.capacityBytes`, none otherwise; `cmd/funcdctl`: dev's manager
   and `volume delete`.
7. Tests: one `TestScenario<Name>` per scenario with `// scenario: <name>`. Validation, reservations and ownership
   run anywhere with a directory manager; the image, quota and isolation scenarios run as root in a Lima lane.

**Definition of done**: the tests pass, including the Lima lane run as root; the gate passes; no username or
absolute path in a changed file.

## Review checklist

- [ ] A Function without volumes produces the same `WorkerSpec` and access hash as before.
- [ ] Only the leaf is bind-mounted; the meta file and the parents are never mounted.
- [ ] No host-side walk, chown, removal or tar follows a symlink; a symlinked leaf is refused.
- [ ] The sum of reservations never passes `capacityBytes` or a namespace budget, under concurrent applies.
- [ ] `replicas > 1` with a `local` volume is refused at apply, naming both fields.
- [ ] A volume with another UID in its meta is never taken over; `ref` binds only a volume with no live holder.
- [ ] Reclaim and boot cleanup are skipped while held; retained volumes appear in `hold status`.
- [ ] A daemon on the process runtime, or without `capacityBytes`, refuses volumes at apply.
- [ ] One test per scenario, named for it.

## Consequences

- Functions get scratch and durable directories with a hard size, on one node.
- The image reserves `capacityBytes` on the host disk up front; growing it needs a restart.
- The host needs `mkfs.ext4`; the quota and isolation paths are tested only as root.
- A `local` volume pins its Function to one node and to one replica; its code must lock across a redeploy.
- A volume backup is not a point in time; data that needs one belongs in a SQLDatabase or a KV store.

## Open questions

- A Bucket prefix as a directory, with Cedar enforcing access through the S3 gateway: FEAT-0014 F182's ADR.
- Moving ADR-0231's engine directories onto the same image and quotas: a later ADR, if engines need a limit.

## References

- Feature issue [#1187](https://github.com/pyvvo/funcd/issues/1187) (epoch tracker #1188); the PR that implements
  this ADR closes #1187.
- [FEAT-0014](../feat/0014-feat-volumes.md); ADR-0231, ADR-0248, ADR-0143, ADR-0234, ADR-0206, ADR-0046, ADR-0032.
- Linux `quotactl(2)`, `ioctl_fsfs` project ids (`FS_IOC_FSSETXATTR`), ext4 `project` quota feature (kernel ≥ 4.5).
