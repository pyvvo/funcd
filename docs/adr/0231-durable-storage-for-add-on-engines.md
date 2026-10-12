# ADR-0231: Durable storage for add-on engines

- **Status**: Accepted (2026-10-12, self-accepted under the decider's batch delegation; judged twice: the first round
  moved the memory-mode root to `cmd/funcd`, made runtimes that cannot mount refuse storage, tied the boot reclaim to the
  hold and its evidence, and fixed the chown rules; the second found no Blocker or Major)
- **Date**: 2026-10-12
- **Deciders**: green-0-rabbit
- **Tags**: provider, runtime, storage
- **Realizes**: [FEAT-0011/F127](../feat/0011-feat-sql.md) (Durable engine storage)
- **Relates to**: [ADR-0087](0087-add-on-provider-runtime.md) (the add-on runtime, extended here) ·
  [ADR-0032](0032-curated-runtime-images-container-execution.md) (bind mounts) ·
  [ADR-0142](0142-supervision-by-periodic-re-convergence.md) (re-creation by supervision) ·
  [ADR-0206](0206-restore-and-held-boot.md) (the hold and its orphan evidence) ·
  [ADR-0125](0125-funcdctl-dev-local-run.md) (the process-mode engine) · source: [design report](../reports/sql-provider-design.md)

## Context & Need

The add-on runtime (`internal/provider`) runs curated engine containers for a resource, and recreates a dead one on the
next pass. `ProviderSpec` (`internal/provider/spec.go:63`) carries no storage, and `workerSpec`
(`internal/provider/runtime.go:218`) sets no mount, so an engine loses everything it wrote whenever its container is
recreated. DuckDB never needed more, because its state lives in blob. The SQL engine (FEAT-0011) keeps its Raft log and
its database file on local disk.

This ADR gives an engine a host directory that the platform owns: created for it, mounted into every container it gets,
kept across re-creations, teardowns and daemon restarts, and removed only when the resource that owns the engine asks.
Its callers are the per-provider reconcilers.

## Scenarios

- `scenario: engine-data-survives-recreation` — Given an engine with storage and a file in its directory, When its
  container dies and the next `Converge` recreates it, Then the new container mounts the same directory and the file is
  there.
- `scenario: engine-data-survives-teardown` — Given that engine, When its owner calls `Teardown` and later `Converge`
  again, or a new runtime starts on the same storage root, Then the file is there.
- `scenario: reclaim-removes-the-directory` — Given a torn-down engine, When its owner calls `Reclaim`, Then the
  directory is gone and `List` no longer names it; `Reclaim` while a container of the engine exists is refused with
  `Conflict` and removes nothing.
- `scenario: storage-without-root-refused` — Given a runtime with no storage root, as on the process runtime, When
  `Converge` gets a spec with storage, Then it fails `Invalid` with the reason and creates no container.
- `scenario: memory-mode-storage-is-ephemeral` — Given `funcd` started with `storage.mode: memory` on containerd, When
  an engine with storage runs and the daemon exits, Then its directory lived under a temporary root that the exit
  removed.

## Scope

**In**: a storage field on `ProviderSpec`; the host directory's path, creation, ownership, mount and removal; the
storage root's configuration; the rule for runtimes that cannot mount.

**Out**: what an engine writes and how it is backed up or restored (each provider's ADR; FEAT-0011 F131 for SQL); disk
quotas; a directory shared by several replicas; storage for the process-mode dev engine (FEAT-0011 F133).

## Constraints & Decision drivers

- The engine runs as its image's user (`oci.WithImageConfig`, `internal/runtime/containerd/containerd_linux.go:1184`),
  often not root; the directory must be writable by that user and by nobody else on the host.
- Data must outlive a not-Ready hold: the catalog reconciler calls `Teardown` to stop an engine it holds
  (`internal/services/catalog/reconcile.go:278`).
- The process driver ignores mounts (`internal/runtime/runtime.go:74-76`); an engine there would write into a
  throwaway filesystem and report Ready. Refusing beats losing data silently.
- `provider.Runtime` already has fakes and a second implementation (`internal/catalog/devengine`); a new duty must not
  break them.
- Only `cmd/funcd` knows `storage.mode` and the execution runtime (`cmd/funcd/main.go:1329`); `pkg/funcd` takes paths.

## Alternatives considered

1. **A named volume managed by containerd**: no such concept in funcd's containerd driver, and the directory would sit
   outside the data directory that operators back up and inspect. Rejected.
2. **Remove the directory in `Teardown`**: simpler, but a held or failed engine would lose its data. Rejected.
3. **New methods on `provider.Runtime`**: breaks every fake of the port. Rejected for a separate interface that the
   engine runtime implements, found by a type assertion, as `kvsvc.PrefixManager` is (`pkg/funcd/funcd.go:971`); an
   owner whose runtime lacks it offers no storage.
4. **Open the directory to everyone (0777), as the boot directory is**: works without root, but any host user could
   read the data. Rejected for 0700 and a chown to the engine's user.

## Decision

1. **A storage field.** `ProviderSpec.Storage` (optional) names the mount path inside the engine and the uid and gid
   the engine's image runs as; the provider's own ADR pins them equal to its image's user. A spec with storage must have
   `Replicas == 1`, else `Converge` fails `Invalid`.
2. **One directory per engine.** The host directory is `<root>/<lower(owner kind)>/<namespace>/<name>`. Before creating
   a container, `Converge` creates it (0700, parents 0700) or checks the existing one with `Lstat`, refusing a symlink
   or a non-directory with `Invalid`, and sets the leaf's mode to 0700 and its owner to the spec's uid and gid, on the
   leaf only, never recursively. When the daemon is not root and the uid is not its own, `Converge` fails `Invalid`
   ("storage needs root or the daemon's uid"). The directory is bind-mounted read-write at the spec's path in every
   container of the engine; the containerd driver mounts every read-write bind `nosuid,nodev`.
3. **Kept until reclaimed.** Re-creation, `Teardown` and a daemon restart never touch it. `Volumes.Reclaim` removes it
   and refuses with `Conflict` while any container of the engine exists. `Volumes.List` names every engine of the
   runtime's owner kind that has a directory. The owner serializes `Converge` and `Reclaim` per engine, as its
   per-object reconciler already does.
4. **Owners' duties.** An owner reclaims on delete, and at boot reclaims the directories of resources that no longer
   exist, before its controller runs; both are skipped while the platform is held (ADR-0206). While held, the
   directories `List` names without a resource are restore evidence: the owner reports them in `hold status`, as
   ADR-0206 reports `bucketOrphans`.
5. **The root.** `cmd/funcd` sets it to `<storage.dataDir>/providers` on the containerd runtime. With
   `storage.mode: memory` it creates a temporary directory, passes it, and removes it on exit; a killed daemon
   leaves it, like its other temporary directories. On the process runtime it
   sets no root, so storage is refused (scenario `storage-without-root-refused`). Any other `provider.Runtime` that
   cannot honor `Storage` refuses it with `Invalid`; `devengine` does so until FEAT-0011 F133.
6. **Not in the platform backup.** An engine's directory is workload data; each provider's ADR decides its backup.

## Temporary workarounds

None.

## Contracts

```go
// internal/provider/spec.go

// StorageSpec asks for a durable host directory mounted into the engine (ADR-0231).
type StorageSpec struct {
	Path string // the mount path inside the engine, absolute
	UID  int    // the user the engine's image runs as
	GID  int
}

type ProviderSpec struct {
	// … the ADR-0087 fields, unchanged …
	Storage *StorageSpec // nil ⇒ no durable storage (the ADR-0087 behavior)
}

// internal/provider/runtime.go

type Deps struct {
	// … the ADR-0087 fields, unchanged …
	StorageRoot string // "" ⇒ no durable storage: a spec with Storage fails Invalid
}

// Volumes manages the durable directories of one owner kind's engines (ADR-0231). The runtime NewRuntime returns
// implements it; an owner gets it by a type assertion on its provider.Runtime.
type Volumes interface {
	// Reclaim removes ref's directory; Conflict while a container of ref exists; nil when there is none.
	Reclaim(ctx context.Context, ref ProviderRef) error
	// List returns the engines of the runtime's owner kind that have a directory, sorted.
	List(ctx context.Context) ([]ProviderRef, error)
}

// pkg/funcd/options.go

// WithProviderStorage sets the add-on engines' storage root (ADR-0231); "" keeps durable storage off.
func WithProviderStorage(root string) Option
```

| Consumes | Exposes |
|---|---|
| `runtime.WorkerSpec.Mounts` (ADR-0032) | `ProviderSpec.Storage`, `provider.Volumes` |
| `storage.dataDir`, `storage.mode`, the execution runtime (`cmd/funcd`) | the directory `<dataDir>/providers/<kind>/<ns>/<name>` |

| Call | Outcome |
|---|---|
| `Converge` with storage, root set, one replica | directory created or checked, 0700, owned by the spec's uid, mounted read-write `nosuid,nodev` at `Path` |
| `Converge` with storage and no root, `Replicas > 1`, a symlinked leaf, or a non-root daemon and a foreign uid | `Invalid` with the reason, no container created |
| `Teardown`, re-creation, daemon restart | directory kept |
| `Reclaim` with no container of the engine | directory removed; idempotent |
| `Reclaim` while a container exists | `Conflict`, nothing removed |

## Implementation plan

1. `internal/provider/spec.go`: `StorageSpec` and the field. `internal/provider/runtime.go`: `Deps.StorageRoot`, the
   checks, directory creation and the leaf-only chown in `Converge`, the mount in `workerSpec`, and `Reclaim` and
   `List` on the engine runtime. `internal/runtime/containerd/containerd_linux.go`: `nosuid,nodev` on read-write binds.
   `internal/catalog/devengine`: refuse `Storage` with `Invalid`.
2. `pkg/funcd`: `WithProviderStorage`, passed to every `provider.NewRuntime`. `cmd/funcd/main.go`: on the containerd
   path, the root `<dataDir>/providers`, or a temporary root in memory mode removed on exit; none on the process path.
3. Tests in `internal/provider` with the existing `fakeRuntime`: write a file into the host directory, then check it
   after retire and re-`Create`, after `Teardown`, and after a new `NewRuntime` on the same root; the mode, the symlink
   refusal, `Reclaim`'s `Conflict`, `List`. One `TestScenario<Name>` per scenario with `// scenario: <name>`;
   `memory-mode-storage-is-ephemeral` runs in `cmd/funcd` with fakes for the containerd path. The chown is checked only
   when the test runs as root.

**Definition of done**: the tests pass; the catalog's tests pass unchanged; the gate passes; no username or absolute
path in a changed file. The proof on a real container, a non-root engine writing through the mount, surviving a kill
and a daemon restart, is the smoke check and lane of FEAT-0011 F128 (ADR-0232).

## Review checklist

- [ ] A spec without storage produces the same `WorkerSpec` as before.
- [ ] The directory path is built from the owner kind, namespace and name only; both name parts are validated DNS
      labels, so no path escapes the root.
- [ ] The leaf is checked with `Lstat`; a symlink or non-directory is refused; the chown is on the leaf only.
- [ ] `Teardown` and `retire` never remove the directory; only `Reclaim` does, and it refuses while a container exists.
- [ ] Storage with no root, `Replicas > 1`, or a foreign uid on a non-root daemon fail `Invalid` before any container.
- [ ] The process path sets no root; `devengine` refuses `Storage`.
- [ ] Read-write binds carry `nosuid,nodev`.
- [ ] One test per scenario, named for it.

## Consequences

- An engine can keep a database, a log or a cache on local disk across restarts.
- Each owner must reclaim on delete and at boot, skip both while held, and report its leftovers in `hold status`, or the
  disk fills or restore evidence is lost.
- Every read-write bind, the function log directory included, gains `nosuid,nodev`.
- The directory is on one node, like the rest of V1's data.

## Open questions

- A resource deleted and recreated under the same name while the platform is held inherits the old directory: decided
  by the SQL database ADR (FEAT-0011 F128), with in-place restore (F131) in mind.
- The containerd shim's memory per engine: measured by the SQL database ADR.

## References

- [ADR-0087](0087-add-on-provider-runtime.md), [ADR-0032](0032-curated-runtime-images-container-execution.md),
  [ADR-0142](0142-supervision-by-periodic-re-convergence.md), [ADR-0206](0206-restore-and-held-boot.md).
- [SQL provider design report](../reports/sql-provider-design.md), "Gaps in today's code".
