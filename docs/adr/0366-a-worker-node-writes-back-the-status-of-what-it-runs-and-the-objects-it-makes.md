# ADR-0366: A worker node writes back the status of what it runs and the objects it makes

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, node-api
- **Realizes**: [FEAT-0002/F244](../feat/0002-feat-multi-node.md) (a node reports back)
- **Relates to**: [ADR-0365](0365-a-worker-node-follows-its-namespaces-through-the-change-log-over-a-link-it-opens.md)
  (the node API and the snapshot) ·
  [ADR-0347](0347-a-job-runs-its-handler-once-per-jobrun-in-a-worker-of-its-own.md) (who started a JobRun) ·
  [ADR-0348](0348-a-run-waits-for-a-persons-approval-of-its-preview-unless-its-job-says-bypass.md) (a person's
  approval) · [ADR-0206](0206-restore-and-held-boot.md) (what ended stays ended) ·
  [ADR-0345](0345-a-function-reconciles-an-engineless-kind-and-a-deletion-waits-for-its-destroy.md) (a held
  deletion) · [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model)

## Context & Need

A worker node runs the reconcilers of the namespaces placed on it (ADR-0363 Decision 4), so the status of what they
run, the children they make and the runs they start appear first in the node's own store. The control plane must show
each node's truth, while spec stays with the people and CI who wrote it, and no node overwrites what a person wrote
(F244). The decider chose on 2026-11-16 (D4, D5) a write-back of status alone for the objects people write, validated
on the control plane, and of whole children of the kinds the node's reconcilers make.

Its purpose: the agent's report loop and the node API's `Status`, `PutObject`, `DeleteObject` and `Children` requests
(ADR-0365) keep the control plane's copy of a node's namespaces true to the node.

## Scenarios

- `scenario: status-back`: Given Function f1 of e1, which vm1 runs, When f1 turns Ready on vm1, Then it reads Ready on
  the control plane, with the spec a person wrote.
- `scenario: a-late-status`: Given a status write of KVStore kv1 in flight, When a person deletes kv1 and makes it
  again under its name, Then the late write changes nothing of the new kv1 (409), and vm1 writes the new one's status
  once it holds it.
- `scenario: a-persons-decision`: Given JobRun r1, which a person approved on the control plane, When vm1 reports r1's
  status without the approval, Then r1 keeps it.
- `scenario: a-child-back`: Given vm1's reconciler makes Revision f1-00002, When it does, Then the control plane holds
  it whole, under the same uid.
- `scenario: a-run-back`: Given vm1 starts a JobRun with no owner, When it does, Then the control plane holds it whole,
  and takes only its status after that; one under a name another run holds there gives way to that run.
- `scenario: only-its-kinds`: Given vm1, When it writes back a Policy, or sends a ConfigMap a person wrote as a child
  it owns, Then the control plane refuses it (403) and the person's spec stays.
- `scenario: a-delete-back`: Given vm1 deleted child f1-00001 and the control plane did not take the delete, When the
  next pass comes, Then vm1 sends it again until it is taken; at its next start, vm1 deletes there each child it no
  longer holds.
- `scenario: the-runs-before-a-snapshot`: Given vm1 holds a run the control plane does not, When a snapshot is due and
  the run's write back fails, Then vm1 takes no snapshot until the run is on the control plane, and keeps the run.
- `scenario: a-run-during-a-snapshot`: Given vm1 takes a snapshot, When its reconciler starts a run after vm1 wrote
  back what it had made, Then the run stays on vm1 and reaches the control plane after the snapshot.
- `scenario: after-a-snapshot`: Given vm1 took a snapshot, When it is done, Then vm1 writes back every status and child
  again.
- `scenario: a-run-deleted-while-behind`: Given a run vm1 made and wrote back, When a person deletes it on the control
  plane while vm1 is stopped behind the change log, Then vm1 writes it back at its start, and the control plane takes it
  again (accepted: Consequences).

## Scope

- **In**: what a node writes back and how the control plane checks it; the node's write-back loop; what a snapshot
  does to the objects a node made.
- **Out**: what a node follows (ADR-0365); its liveness and its report of itself (F245); Secret writes (F249); the
  checks before a write that read a node's state (F250); a restored node's runs (ADR-0206, F251).

## Constraints & Decision drivers

- Spec stays with people and CI: a node changes only the status of an object a person wrote (F244).
- The control plane validates every node write (F244).
- No write of a node reaches another object of the same name.
- A run a node made reaches the control plane before the node takes a snapshot, so none is lost (the decider's rule 24
  in #1495).

## Alternatives considered

- **The node writes whole objects, as a client of the API does**: it could rewrite a person's spec. Rejected by the
  first constraint.
- **The control plane pulls the status from each node**: it would dial the node (ADR-0365). Rejected.
- **A status by name, the last write winning**: a late write of a deleted object lands on the one made again under its
  name. Rejected for the uid check.
- **A snapshot's prune removes every object the control plane lacks**, as first built: a run made during the snapshot,
  or one whose write back failed just before, was lost. Rejected (Decision 5).
- **A snapshot never prunes what a node made**: a run a person deleted on the control plane while its node was behind
  the change log would stay on the node for good, even when the node remembers it wrote it back. Rejected; the prune
  removes such a run while the node remembers, and a node that does not (a restart, a run changed since) writes it back
  (Consequences).
- **A record of deleted runs on the control plane**, so that a node never writes one back: it would end the case
  above, at the cost of a store of deletes and its retention. Left for the decider (Consequences).

## Decision

1. **What a node writes.** The status of each object of its namespaces, and whole objects of two sorts: a child it
   owns, of the kinds its reconcilers make (`childKinds`), and an object it makes with no owner (`madeKinds`: Backup,
   WorkflowRun, Invocation and JobRun). Nothing else: no Policy, Role, RolesAssignment or Identity, and no spec of an
   object a person wrote (`multinode.WrittenBack`).
2. **Status.** `Status` writes the status of the object of the uid the node names, in a namespace placed on the node:
   another uid under the name is 409 `created again`, and a namespace placed elsewhere 403. The status becomes the
   node's, but for what the control plane records there itself (`KeepDecisions`): who started a JobRun and its Job's
   UID (ADR-0347 Decision 3), a person's decision on a preview (ADR-0348 Decisions 3 and 5), and an end it recorded
   (ADR-0206 Decision 7). The result passes the kind's validation, or the write is refused. A status write writes no
   change-log record (ADR-0365 Decision 3).
3. **Whole objects.** `PutObject` creates or replaces a child the node owns, under the node's uid. An object of a
   `madeKinds` kind with no owner is created once, whole, when the control plane does not hold it, and after that only
   its status changes, with the pause of a WorkflowRun its node's restore paused (ADR-0206 Decision 7); this holds for a
   run a person made through the API too. One under a name another object holds is 409, and the node takes that object
   through its feed. An object of any other kind that a person wrote is 403, whatever the node sends. `DeleteObject`
   deletes, by uid and on the version it read, an object the node writes back whole, or one whose deletion the control
   plane holds until the node ends it (ADR-0345 Decision 6); an object of another uid under the name stays. These
   writes carry the node as their origin, so its feed leaves them out. A child is the node's, as its reconciler derives
   it, and the API ignores the ownerReferences a person sends (ADR-0048), so only an edit reaches one from the control
   plane: the feed brings the edit to the node's copy, where it lasts until the node's reconciler writes the child
   again, while a snapshot leaves children out, and the node's next write replaces the edit.
4. **The node's side.** The agent watches every namespaced kind of its store and writes each change back once: the
   status, or the whole object, unless what it wrote last, kept in memory, has the same content but for the
   resourceVersion. A pass every `resyncEvery` 30 s writes
   again what failed and sends again each delete the control plane did not take. At its start and after a snapshot it
   deletes on the control plane each child the control plane holds and the node no longer does (`Children`); after a
   snapshot it writes back everything again.
5. **Before a snapshot.** On a store that is neither new nor restored (ADR-0365 Decision 6), the node writes back
   every object it made with no owner and does not remember writing, and takes the snapshot only once each went through
   or the control plane refused it (400, 403, 409 or 413); any other failure leaves the snapshot to its next try. The
   snapshot's prune then removes what the control plane does not hold, but the objects the node made since that write
   back, which the next pass sends.

## Design model

- **Spec**: [`docs/design/multinode/writeback.fizz`](../design/multinode/writeback.fizz) · state machine
  [`writeback.md`](../design/multinode/writeback.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NoStatusOnAnotherObject` | always | `scenario: a-late-status` | `TestANodesLateWriteChangesNothingOfAnotherObject` |
  | `APersonsDecisionStays` | always | `scenario: a-persons-decision` | `TestAPersonsDecisionOnARunReachesItsNode` |
  | `NoRunIsLost` | always | `scenario: the-runs-before-a-snapshot`, `scenario: a-run-during-a-snapshot` | `TestASnapshotWaitsForTheRunsTheNodeMade`, `TestARunMadeDuringASnapshotStays`, `TestARunTheNodeMadeOutlivesASnapshot` |
  | `ADeletedRunCanComeBack` | exists | `scenario: a-run-deleted-while-behind` | `TestARunAPersonDeletedWhileItsNodeWasBehindComesBack` |
  | `TheControlPlaneConverges` | eventually always | `scenario: status-back`, `scenario: a-delete-back` | `TestADeleteTheControlPlaneDidNotTakeIsSentAgain`, `TestANodeJoinsFollowsItsNamespaceAndReportsBack` |

- **Bounds**: one object, which a person approves and makes again once, with one status; one run, which a person may
  delete; one node restart; one snapshot; one failed write back; 20 actions: the smallest scenario in which every
  mutant fails, and the first that every path completes in. Wider (two failed writes, 24 actions): pass, 55,696
  states, 22.9 s.
- **Faults**: one write back fails: of a status, of a run, of a delete, or before the snapshot; the node restarts once
  and forgets what it wrote back.
- **Environment**: the control plane's status write is conditional on the version it read (`Status` retries a
  conflict), one step here; the feed brings the control plane's object, and a person's delete of the run, to the node
  in one step; the node forgets what it wrote back of a copy it removes (its watch's delete event), and sends the
  delete of a run it writes whole.
- **Mutants**: `no-uid-check` fails `NoStatusOnAnotherObject`; `no-keep-decisions` fails `APersonsDecisionStays`;
  `snapshot-despite-a-failed-push` and `prune-what-came-since` fail `NoRunIsLost`; `no-resend` fails
  `TheControlPlaneConverges`. `ADeletedRunCanComeBack` records an accepted cost, so no mutant guards it: a record of
  deletes would end it, and the spec would then fail it.
- **Result**: pass, 36,526 states, 14.5 s, FizzBee v0.5.4.
- **Left to tests**: the kinds a node writes back (`TestANodeWritesBackOnlyTheKindsItsReconcilersMake`,
  `TestTheObjectsANodeMakesReachTheControlPlaneAndNoOthers`); a person's object sent as a child
  (`TestANodesLateWriteChangesNothingOfAnotherObject`); a run under a name another holds
  (`TestARunThatANodeStartsReachesTheControlPlane`); the children a node no longer holds
  (`TestAChildTheNodeNoLongerHoldsGoesFromTheControlPlane`); the writes after a snapshot
  (`TestANodeReportsEverythingAgainAfterASnapshot`).

## Behavior rows

- **multi-node.md**: "Status and children back", "A node's late write", "A delete the control plane did not take", "A
  person's decision on a JobRun" and "A snapshot on a running node" (now with `TestASnapshotWaitsForTheRunsTheNodeMade`
  and `TestARunMadeDuringASnapshotStays`) cite this ADR's Decisions at acceptance; a row "A run deleted while its node
  was behind" for the accepted cost, with `TestARunAPersonDeletedWhileItsNodeWasBehindComesBack`.

## Temporary workarounds

None.

## Contracts

```text
PUT    /node/v1/status                                     # HTTPS; NATS funcd.node.<node>.<serial>.status
PUT    /node/v1/objects                                    # NATS .putObject
DELETE /node/v1/objects?kind=&namespace=&name=&uid=        # NATS .deleteObject
GET    /node/v1/children                                   # NATS .children
```

```go
// internal/nodeapi: the write-back requests of the port (ADR-0365)
type Handler interface {
	Status(ctx context.Context, node v1.ObjectName, in StatusWrite) error
	PutObject(ctx context.Context, node v1.ObjectName, in ObjectWrite) error
	DeleteObject(ctx context.Context, node v1.ObjectName, in ObjectRef) error
	Children(ctx context.Context, node v1.ObjectName) ([]store.Change, error)
}

type StatusWrite struct {
	Kind      v1.Kind          `json:"kind"`
	Namespace v1.NamespaceName `json:"namespace"`
	Name      v1.ObjectName    `json:"name"`
	UID       v1.UID           `json:"uid"`
	Status    json.RawMessage  `json:"status"`
}

type ObjectWrite struct {
	Kind   v1.Kind         `json:"kind"`
	Object json.RawMessage `json:"object"`
}

type ObjectRef struct {
	Kind      v1.Kind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
	UID       v1.UID
}

// internal/multinode
func WrittenBack(kind v1.Kind, owned bool) bool // whole (childKinds when owned, madeKinds when not), else status
func KeepDecisions(to, from v1.Object)          // what the control plane records in a status, from its own copy
func KeepPause(to, from v1.Object)              // a WorkflowRun's pause from the later spec
```

| Part | Reads | Writes |
|---|---|---|
| node agent | its store's watch; a pass every `resyncEvery` | `Status`, `PutObject`, `DeleteObject`; reads `Children` |
| control plane | the node's write; its own copy | the copy's status, with no change-log record; a whole child or run, with the node as origin |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `internal/multinode/writeback.go` (`childKinds`,
`madeKinds`, `WrittenBack`), `handler.go` (`Status`, `PutObject`, `DeleteObject`, `Children`), `copies.go`
(`KeepDecisions`, `KeepPause`); `internal/multinode/agent/report.go` (`report`, `push`, `pushMade`, the deletes),
`children.go` (`dropChildren`), `agent.go` (`syncAll`) and `copies.go` (`prune`). This ADR changed the built code:
`pushMade` returns the keys it holds and an error on a write the control plane did not refuse, `syncAll` takes no
snapshot after such an error, `prune` spares what the node made since, and `DeleteObject` deletes on the version it
read.

| Scenario | Test |
|---|---|
| status-back | `TestANodeJoinsFollowsItsNamespaceAndReportsBack`, `TestAFunctionRunsOnTheWorkerNodeItsNamespaceIsPlacedOn` |
| a-late-status | `TestANodesLateWriteChangesNothingOfAnotherObject` |
| a-persons-decision | `TestAPersonsDecisionOnARunReachesItsNode` |
| a-child-back | `TestTheObjectsANodeMakesReachTheControlPlaneAndNoOthers`, `TestANodeWritesBackOnlyTheKindsItsReconcilersMake` |
| a-run-back | `TestARunThatANodeStartsReachesTheControlPlane`, `TestAJobRunsOnTheWorkerNodeItsNamespaceIsPlacedOn` |
| only-its-kinds | `TestTheObjectsANodeMakesReachTheControlPlaneAndNoOthers`, `TestANodesLateWriteChangesNothingOfAnotherObject` |
| a-delete-back | `TestADeleteTheControlPlaneDidNotTakeIsSentAgain`, `TestAChildTheNodeNoLongerHoldsGoesFromTheControlPlane` |
| the-runs-before-a-snapshot | `TestASnapshotWaitsForTheRunsTheNodeMade`, `TestARunTheNodeMadeOutlivesASnapshot` |
| a-run-during-a-snapshot | `TestARunMadeDuringASnapshotStays` |
| after-a-snapshot | `TestANodeReportsEverythingAgainAfterASnapshot` |
| a-run-deleted-while-behind | `TestARunAPersonDeletedWhileItsNodeWasBehindComesBack` |

Done when every test above passes, `just check-specs` passes the spec and its mutants, and `just ci` is green.

## Review checklist

- [ ] A node's write reaches the control plane only through `Status`, `PutObject` and `DeleteObject`, each checking
      that the namespace is placed on the node.
- [ ] `Status` and `DeleteObject` act on the object of the uid the node names, and a status write keeps what
      `KeepDecisions` keeps and writes no change-log record.
- [ ] `PutObject` refuses a kind `WrittenBack` does not name and an object of another kind that a person wrote, and
      creates a run once; `DeleteObject` deletes on the version it read.
- [ ] A failed write back is sent again at the next pass, and a delete until the control plane takes it.
- [ ] `syncAll` takes no snapshot while `pushMade` returns an error, and `prune` spares an object made since.
- [ ] Each oracle test carries its `oracle:` line, and the spec passes with its mutants failing.

## Consequences

- The control plane shows each node's status and children, and a person's spec and decisions stay theirs.
- A write back that failed reaches the control plane within `resyncEvery` 30 s of the link's return.
- A snapshot waits until the runs the node made reach the control plane, so a node whose link fails takes none.
- A node writes back every status and child again at its start and after each snapshot, since what it wrote back is
  kept in memory.
- A child edited on the control plane takes the node's version again at the node's next write of it.
- A run the control plane refused (400, 403 or 413) leaves the node at its next snapshot; one under a taken name (409)
  gives way to the other.
- Risk accepted: a run a person deleted on the control plane while its node was behind the change log comes back when
  the node writes it back without remembering it did (after a restart, or a change of the run since); it must be
  deleted again. No run lost (the decider's rule 24) was chosen over this, and a record of deletes would end it.
- Risk accepted (the decider's D5, as #1495 states it): a node's writes back are checked by the kind's validation
  alone, with no admission and no quota, and the owner a child names is not checked to exist. So a compromised node can
  add objects of the written-back kinds in the namespaces it runs, past their quotas, and start JobRuns there under any
  `startedBy`; it cannot write a Policy, a Role, a RolesAssignment or an Identity.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F244; feature issue #1481; tracker #1493; draft PR #1495.
- The ADR-0365 judge's routed finding: the node's own children leave the snapshot, and their writes leave the feed by
  origin (Decision 3).
