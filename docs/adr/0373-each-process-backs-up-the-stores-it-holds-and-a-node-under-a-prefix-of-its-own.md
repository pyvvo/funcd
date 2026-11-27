# ADR-0373: Each process backs up the stores it holds, and a node under a prefix of its own

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, backup, disaster-recovery
- **Realizes**: [FEAT-0002/F251](../feat/0002-feat-multi-node.md) (backups per process)
- **Relates to**: [ADR-0206](0206-restore-and-held-boot.md) (the platform backup, the held boot and the quarantine's
  return) · [ADR-0288](0288-a-boot-reclaim-quarantines-data-no-object-names.md) (data no object names) ·
  [ADR-0365](0365-a-worker-node-follows-its-namespaces-through-the-change-log-over-a-link-it-opens.md) (a node's new or
  restored store) · [ADR-0371](0371-a-node-holds-the-secrets-and-the-master-of-its-own-namespaces-only.md) (the
  masters a node's credentials derive from) ·
  [ADR-0372](0372-the-control-plane-reaches-a-node-through-calls-it-polls-for-and-a-check-on-a-nodes-state-runs-there.md)
  (a node's hold and backup read by its name) ·
  [ADR-0364](0364-a-worker-node-joins-with-a-one-time-token-and-renews-its-certificate.md) (the CA)

## Context & Need

With nodes, the stores of one platform live on several machines: the control plane holds the metastore and the
masters, and each node the data of its namespaces. No backup can cut stores on different machines at one moment, and a
lost node must come back without the others (F251). The decider chose on 2026-11-16 (D10, #1488) a backup per process:
an all-in-one as today, the control plane its metastore and key escrow, and each node its own stores on its own
schedule, with workload backups unchanged and a replaced node restored from its node's backup.

Its purpose: every process runs the platform backup over what it holds, on one shared target, and every restore,
of the control plane or of a node, brings that process back without taking the others with it.

## Scenarios

- `scenario: each-its-own`: Given a control plane and node vm1 that share `backup.target`, When both back up, Then
  vm1's generations go under `nodes/vm1/` and the control plane's at the target's root.
- `scenario: a-replaced-node`: Given vm1's machine is lost, When a new machine joins as vm1 and restores, Then it comes
  back from `nodes/vm1/`, held with what changed on the control plane since, and its Secrets come from the control
  plane.
- `scenario: a-lost-metastore`: Given vm1's metastore is emptied while it is stopped, When it starts, Then it takes its
  namespaces whole before anything runs, and its data stays.
- `scenario: a-restored-control-plane`: Given the control plane restored from a backup, When vm1 follows it again after
  the release, Then vm1 starts again on the restored state, and data no object names goes into its quarantine.
- `scenario: what-ended-stays-ended`: Given a run that ended on the control plane, When its node is restored to a point
  before that end, Then the run stays ended there; a run the node's restore paused shows paused until a person
  resumes it.
- `scenario: the-backup-shows`: Given vm1 backs up, When an admin reads WorkerNode vm1, or asks for vm1's backup by its
  name, Then each shows the node's own backup.
- `scenario: a-schedule-into-the-nodes-store`: Given vm1 reports its blob store's address, When an admin applies a
  BackupSchedule of a namespace on vm1 whose target is inside that store, Then it is refused.

## Scope

- **In**: which process backs up what, where a node writes, how a node's backup shows, and what a node does after a
  restore of itself or of its control plane.
- **Out**: the platform backup and the restore themselves (ADR-0206, ADR-0288 and the DR ADRs); workload backups
  (BackupSchedule), unchanged; moving a namespace's data (ADR-0368, by workload backups).

## Constraints & Decision drivers

- No backup cuts stores on different machines at one moment (F251).
- A lost node comes back without the others (F251).
- A node's backup names no key and holds no Secret: the control plane holds both (ADR-0371).
- One target for a whole platform, with no fencing between its processes.

## Alternatives considered

- **One backup of the whole platform, taken from the control plane**: it would read every node's stores over the link,
  and still cut them at different moments. Rejected.
- **A target per node, in each node's config**: an operator configures one target per machine. Rejected for a prefix
  under the shared target.
- **The node's masters in its backup**: a leaked backup would hold the credentials of its namespaces. Rejected; the
  control plane holds them and sends them again (ADR-0371).
- **The cluster CA in the control plane's backup**: a leaked backup would hold the key that signs every node, for a
  restore that a new token per node also covers. Rejected (Decision 5).

## Decision

1. **Each process backs up what it holds.** The platform backup (ADR-0206) runs in each process over its own stores:
   an all-in-one as today; the control plane its metastore, with the escrow of its master; a worker node its own
   stores on its own schedule, its metastore copy without Secrets, naming no key, since its credentials derive from
   its namespaces' masters, which the control plane holds (`envelope.FromNodeConfig`). Workload backups do not change.
2. **One target, a prefix per node.** A node writes under `nodes/<name>/` of `backup.target` and of
   `kvstore.backup.target`: the `prefix` parameter of an `s3://` target, a subdirectory of a `file://` one, which the
   node makes when the target itself exists. A node that backs up unsealed (`backup.encryption.none`) sets
   `secrets.encryptionKeyFile`, as every process must.
3. **A node's backup shows on the control plane.** The node reports its platform backup in its WorkerNode's
   `status.backup` (held, RPO risk, last success, next run, last failure), and its blob store's address in
   `status.blobStore`, at its start and every `selfEvery` 5 min when they changed. An admin reads a node's hold and
   backup by its name through a node call (ADR-0372). A BackupSchedule's target inside the blob store that its
   namespace's node reports is refused; one of a node that reported none is not compared.
4. **Restores.** A machine joined again under a node's name restores that node from its prefix. A node whose store is
   new or restored takes its namespaces whole, with the children it made that the control plane holds, before
   anything runs, and waits for the control plane (ADR-0365 Decision 6). A node whose control plane was restored
   follows nothing while the control plane is held; after the release its platform starts again on the restored
   state, and data no object names goes into its quarantine (ADR-0288). What ended on the control plane stays ended
   there, and a run that its node's restore paused shows paused there until a person resumes it (ADR-0206 Decision 7).
5. **The cluster CA stays out of the backup.** It lives under `cluster.dataDir`, on the control plane's machine. A
   control plane restored without that directory has a new CA, so every node joins again with a new token (ADR-0364).

## Design model

None: each process runs the platform backup of ADR-0206 unchanged over its own stores, a node with a prefix; a
restore's held boot and quarantine are ADR-0206's and ADR-0288's, with their models; a node's part after a restore is
one call (`agent.Prepare`) before its platform starts its loops, and the feed's snapshot rules (ADR-0365, with its
model).

## Behavior rows

- **multi-node.md**: "A node's own hold and backup", "A BackupSchedule's target", "A node whose store is new or
  restored", "A restored control plane" and "A restored node's runs" cite this ADR's Decisions at acceptance.

## Temporary workarounds

None.

## Contracts

```yaml
# the node's own backup, as WorkerNode vm1's status shows it
status:
  blobStore: file:///var/lib/funcd/blobs
  backup:
    held: false
    rpoRisk: false
    lastSuccessTime: 2026-11-27T18:00:00.000Z
    nextRunTime: 2026-11-27T19:00:00.000Z
```

```text
backup.target: s3://backups?prefix=prod/        # the control plane writes at prod/, node vm1 at prod/nodes/vm1/
backup.target: file:///mnt/backups              # node vm1 writes in /mnt/backups/nodes/vm1
```

| Part | Reads | Writes |
|---|---|---|
| control plane | its metastore and master | its generations at the target's root |
| worker node | its own stores | its generations under `nodes/<name>/`; `status.backup` and `status.blobStore` of its WorkerNode |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `cmd/funcd/cluster.go` (`nodeBackups`, `makeTargets`,
`nodeTarget`), `internal/backup/envelope/config.go` (`FromNodeConfig`), `internal/multinode/agent/report.go`
(`reportSelf`) and `children.go` (`Prepare`, `adoptChildren`), `internal/multinode/commands.go` (`Nodes`, `NodeBackup`),
`internal/multinode/admit.go` (`outsideNodeStore`), `internal/multinode/copies.go` (`KeepDecisions`, `KeepPause`).

| Scenario | Test |
|---|---|
| each-its-own | `TestAWorkerNodeBacksUpUnderItsOwnPrefix`, `TestAJoinedNodesUpgradeWritesUnderItsOwnPrefix` |
| a-replaced-node | `TestAReplacedNodeComesBackFromItsBackup`, `TestAJoinedNodeRestoresFromItsOwnPrefix` |
| a-lost-metastore | `TestANodeThatLostItsMetastoreKeepsItsData`, `TestANodeWithANewStoreTakesItsNamespacesAgain` |
| a-restored-control-plane | `TestARestoredControlPlaneLosesNoDataOfItsNodes`, `TestANodeOutlivesTheRestoreOfItsControlPlane` |
| what-ended-stays-ended | `TestWhatEndedStaysEndedAfterItsNodesRestore`, `TestARunTheRestoreOfItsNodePausedWaitsForAPerson` |
| the-backup-shows | `TestANodeReportsItsBackupInItsWorkerNode`, `TestANodesOwnHoldAndBackupAnswerByItsName` |
| a-schedule-into-the-nodes-store | `TestABackupSchedulesTargetStaysOutsideItsNodesBlobStore` |

Done when every test above passes and `just ci` is green.

## Review checklist

- [ ] A node's backup goes under `nodes/<name>/` of both targets, and names no key and holds no Secret.
- [ ] The control plane's backup holds its metastore and the escrow of its master, and no node's stores.
- [ ] A node reports `status.backup` and `status.blobStore` at its start and when they change.
- [ ] A node with a new or restored store takes its namespaces whole before its platform starts its loops.
- [ ] A BackupSchedule's target inside its node's reported blob store is refused.

## Consequences

- A lost node comes back from its own prefix, without the control plane or the other nodes restoring.
- A restore of the control plane and one of a node are each their own point in time; the node takes the control
  plane's state at its next start, with data no object names in its quarantine.
- One target serves a whole platform; each node fills its own part of it.
- Risk accepted: a control plane whose machine is lost with its `cluster.dataDir` comes back with a new CA, and every
  node joins again, with a new token each.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F251; feature issue #1488; tracker #1493; draft PR #1495.
- The ADR-0364 judge's routed finding: the backup of the CA key and `cluster.dataDir` (Decision 5).
