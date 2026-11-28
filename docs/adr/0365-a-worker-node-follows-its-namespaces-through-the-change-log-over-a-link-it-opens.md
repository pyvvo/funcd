# ADR-0365: A worker node follows its namespaces through the change log, over a link it opens

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, node-api, store
- **Realizes**: [FEAT-0002/F243](../feat/0002-feat-multi-node.md) (a node follows its namespaces)
- **Relates to**: [ADR-0363](0363-one-funcd-binary-runs-as-the-all-in-one-the-control-plane-alone-or-a-worker-node.md)
  (the roles) · [ADR-0364](0364-a-worker-node-joins-with-a-one-time-token-and-renews-its-certificate.md) (the join and
  admission) · [ADR-0202](0202-platform-store-snapshot-and-timeline.md) (the store's timeline) ·
  [ADR-0206](0206-restore-and-held-boot.md) (the hold after a restore) · [ADR-0012](0012-gateway-ingress-port.md) (an
  embedded driver that is the test driver too) ·
  [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model)

## Context & Need

A worker node (ADR-0363) runs the namespaces placed on it from its own store, so it needs a copy of their objects that
stays current, survives a restart of either side, and keeps serving while the control plane is away. Nodes sit behind
NAT or in cloud containers that take no inbound connection, so the control plane never dials a node (FEAT-0002). The
decider chose on 2026-11-16 a pull: the node takes a snapshot, then long-polls a change log the metastore keeps; on
2026-11-25 a port whose rules live in one handler; on 2026-11-26 a second driver on core NATS, with no JetStream.
Experiment 0031 then compared the two drivers (#1588).

Its purpose: the node agent (`internal/multinode/agent`) keeps the node's store a copy of the control plane's for its
namespaces, through the node API port (`internal/nodeapi`), which also carries the requests of the later multi-node
ADRs (F244, F245, F248 to F252).

## Scenarios

- `scenario: follow-a-change`: Given node vm1 that runs namespace e1, When an admin applies ConfigMap e1/probe on the
  control plane, Then vm1's store holds it within moments, with the control plane's uid, and no other node is woken.
- `scenario: resume-after-a-restart`: Given vm1 stopped after it followed e1, When three ConfigMaps are written in e1
  and vm1 starts again, Then it resumes after its saved revision and gets all three.
- `scenario: behind-the-floor`: Given vm1 stopped while the change log compacted past its revision, When it starts
  again, Then the feed refuses its revision (410, or 412 over NATS), and vm1 takes a snapshot that brings what it
  missed.
- `scenario: a-failed-write`: Given vm1's store refuses the write of a record twice, When the record comes, Then vm1
  saves no revision past it and takes a snapshot, which saves none either while it fails, until one brings the record.
- `scenario: a-record-it-cannot-decode`: Given a record of a kind vm1 does not know, When it comes, Then vm1 skips it
  and follows on, with no snapshot.
- `scenario: control-plane-away`: Given the control plane is stopped, When it comes back and a ConfigMap is written in
  e1, Then vm1 kept what it held meanwhile, and follows again from its revision.
- `scenario: control-plane-restored`: Given the control plane's metastore restored from a backup, When vm1 asks for its
  revision, Then it is refused, and vm1's platform starts again on the restored state before it runs anything.
- `scenario: over-nats`: Given `cluster.driver: nats` and a node that joined its `nats://` URL, When e1 changes, Then
  the node follows it as over HTTPS.

## Scope

- **In**: the node API port and its two drivers, the change log, the snapshot and the feed, the router that wakes the
  nodes, and the node's cursor.
- **Out**: the other requests the port carries, each its ADR's: write-back (F244), liveness (F245), placement (F246),
  hosts (F247), API tokens (F248), Secrets and keys (F249), node calls (F250), backups (F251), versions (F252); what a
  restored control plane does to its nodes (ADR-0206, F251).

## Constraints & Decision drivers

- The control plane never dials a node, so a node behind NAT works (FEAT-0002).
- Nothing is lost across a restart of either side, and a node keeps serving while the control plane is away (F243).
- One binary: no broker outside funcd (blueprint, *Single-binary process model*).
- A request's rules live once, whatever carries it (the decider, 2026-11-25).

## Alternatives considered

- **The control plane pushes to an API on each node** (the blueprint's first sketch): every node needs an inbound port.
  Rejected by the first constraint.
- **A watch stream the node opens**, as a kubelet's watch: a long-lived stream needs its own framing and resume through
  every proxy, while a long poll is one request per batch and resumes from the same revision. Rejected.
- **JetStream, or a queue per node**: the log would live twice, in the metastore and a stream, with its own retention
  and backup, while the metastore's transaction already orders the writes. Rejected (the decider, 2026-11-26).
- **A list on every poll**, with no log: each poll of each node costs a list of its namespaces. Rejected.
- **The store's own watch** and its replay ring (`store.WatchOptions.SinceResourceVersion`): it lives in memory, so a
  control plane's restart loses it, and a node could not resume. Rejected by the second constraint.
- **Each waiting feed reads the log at every write**: the cost of a write grows with the nodes that wait (#1586).
  Rejected for one router per control plane.

## Decision

1. **The port.** `internal/nodeapi` is the node API: `Handler` holds the rules of each request (`multinode.Handler`),
   `Server` is a driver's control-plane side, `Client` and `Connector` its node side. A node opens every connection.
   A driver calls `Admit` (ADR-0364) for each request of a joined node, then the request's method, and carries the
   errors' api/fault kinds. The bounds are the port's: a wait of `DefaultWait` 25 s, at most `MaxWait` 60 s, and bodies
   of `MaxJoinBody` 64 KiB, `MaxStatusBody` 1 MiB and `MaxObjectBody` 4 MiB. Every driver passes `nodeapicontract`.
2. **Two drivers.** `cluster.driver` picks the control plane's: `https`, the default, a long poll on the node API's own
   listener, `cluster.listenAddr`, under TLS 1.3 and mTLS; or `nats`, core NATS on a server funcd embeds, with no
   JetStream. A node takes the driver its control plane's URL names, `https://` or `nats://`. On NATS, a node's
   certificate sets what its connection may do: it publishes only under `funcd.node.<node>.<serial>.>` and hears only
   its inbox and `funcd.call.<node>.<serial>`, and the control plane answers only into the inbox of who asked. An answer
   over `chunkSize` 512 KiB comes in chunks under `maxPayload` 8 MiB. The server pings each link every `pingInterval`
   1 s and ends one that misses `maxPingsOut` 2, and a node dials again every `reconnectWait` 2 s.
3. **The change log.** A metastore with the cluster option (ADR-0363 Decision 3) writes one record per object write, in
   the write's transaction and at its revision, with the object as stored. It keeps the last `defaultChangeRetain`
   100,000 records and compacts every `changeCompactInterval` 1024 revisions. A revision below its floor, or of another
   timeline of the store (ADR-0202), is refused as `fault.PreconditionFailed`: 410 on HTTPS, 412 on NATS. A node's
   status write-back skips the log, and a node's own write carries the node as its origin. No backup holds the log.
4. **What a node gets.** `Snapshot` reads the log's head, then lists every object of the namespaces placed on the node,
   their Namespace objects included, and the cluster-scoped objects of `multinode.NodeKinds` that concern it, without
   the children the node owns; the node follows after that head. `Changes` answers the records after the node's
   revision, at most `feedLimit` 500: those of its namespaces and of the kinds it follows, never its own writes. A
   Namespace or an API token that does not concern the node reaches it as a bare delete of its name (F248). A feed
   waits up to its wait for a record that concerns the node (`nodeapi.WaitForChanges`). While the control plane is held
   after a restore (ADR-0206), both are 503.
5. **The router.** One reader per control plane follows the log from the head it reads before the node API serves, and
   wakes only the nodes a record concerns: the node its namespace is placed on, unless that node wrote it; every node
   for a Namespace or a kind they all follow; none for another cluster-scoped record. A failed read, a compaction past
   its position or a log that started again below it wakes every node, which then reads for itself.
6. **The node's side.** The agent keeps `cursor` under `cluster.dataDir`: the control plane's timeline, the revision,
   and the node store's own timeline. It applies each record to its store as the same object (uid, generation and
   creation time), then saves the cursor, so a batch cut short is applied again, and twice changes nothing. A node with
   no cursor of its store, or one a crash tore, takes a snapshot first. A refused revision sends the node to a
   snapshot, and so does a record it failed to write, after which it saves no cursor until the snapshot; a snapshot
   that fails to write an object saves none either, removes what the control plane no longer holds, and is taken
   again. A record the node cannot decode or validate
   (`fault.Invalid`), as from a newer release, is skipped, since no snapshot would bring it (F252). A snapshot of
   another timeline than the cursor's means the control plane was restored: the node's platform starts again and takes
   the restored state before it runs anything. A newly placed namespace comes from its own snapshot, and one that left
   goes from the node's store. While the control plane is away, the node serves from its store and asks again from
   `retryMin` 100 ms, doubled up to `retryMax` 5 s, as it takes a failed snapshot again.

## Design model

- **Spec**: [`docs/design/multinode/feed.fizz`](../design/multinode/feed.fizz) · state machine
  [`feed.md`](../design/multinode/feed.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `TheCursorNeverPassesWhatTheNodeApplied` | always | `scenario: resume-after-a-restart`, `scenario: a-failed-write` | `TestANodeResumesFromItsSavedRevision`, `TestAChangeTheNodeFailedToWriteComesWithASnapshot`, `TestAFailedSnapshotIsTakenAgain` |
  | `TheNodeConverges` | eventually always | `scenario: behind-the-floor`, `scenario: resume-after-a-restart` | `TestANodeBehindTheChangeLogsFloorTakesASnapshot`, `TestANodeResumesFromItsSavedRevision` |

- **Bounds**: two names, two writes, a log that keeps one record, batches of two, one crash and one failed write, in a
  batch or in a snapshot, 16 actions: the smallest scenario in which every mutant fails, and the first that every path
  completes in. Wider (three writes, 20 actions): pass, 21,966 states, 5.9 s.
- **Faults**: the node crashes once, at any step; one write fails, of a batch or of a snapshot.
- **Environment**: a write and its record are one transaction; the node's store keeps what it committed before a crash
  (each commit syncs); a feed's wait ends at its deadline, so a missed wake only delays a read. Modeled as atomic steps.
- **Mutants**: `save-before-apply`, `no-floor-check`, `list-before-head`, `no-reapply`, `skip-failed-apply` and
  `snapshot-saves-past-a-failed-write` fail `TheCursorNeverPassesWhatTheNodeApplied`; `no-resync` fails
  `TheNodeConverges`.
- **Result**: pass, 2,708 states, 1.0 s, FizzBee v0.5.4.
- **Left to tests**: which records concern a node (`TestAWriteWakesOnlyTheNodesItConcerns`,
  `TestANodeGetsTheAPITokensAndTheNamespacesThatConcernIt`); each driver's wire (`nodeapicontract`, and the NATS tests
  of permissions, chunks and silent links); a torn cursor, which reads as none (`TestATornCursorReadsAsNone`); a record
  the node cannot decode, skipped with no snapshot (`TestARecordTheNodeCannotDecodeIsSkipped`); a restore of the node's
  store or of its control plane, which ADR-0373's [`restore.fizz`](../design/multinode/restore.fizz) models.

## Behavior rows

- **multi-node.md**: "Following" (now with a failed write and `TestAChangeTheNodeFailedToWriteComesWithASnapshot`), "A
  write on the control plane", "An old revision", "A copy on a node", "The control plane away", "The node API's
  driver", "A node on NATS" and "A link that goes silent on NATS" cite this ADR's Decisions at acceptance.

## Temporary workarounds

None.

## Contracts

```text
cluster.driver: https | nats                              # the control plane's node API driver; "" is https
GET  /node/v1/snapshot?namespace=<ns>                     # HTTPS; every namespace of the node when ns is empty
GET  /node/v1/feed?since=<rev>&timeline=<t>&wait=<dur>    # 200 {"rev", "changes"}; 410 for a refused revision
funcd.node.<node>.<serial>.snapshot                       # NATS, the same requests; 412 for a refused revision
funcd.node.<node>.<serial>.feed
```

```go
// internal/nodeapi: the port; the requests of the other multi-node ADRs are left out here
type Handler interface {
	Admit(ctx context.Context, p Peer) error // ADR-0364
	Snapshot(ctx context.Context, node v1.ObjectName, ns v1.NamespaceName) (Snapshot, error)
	Changes(ctx context.Context, node v1.ObjectName, since uint64, timeline string) (Feed, error)
	Changed(node v1.ObjectName) <-chan struct{} // closed at the next record that concerns node
}

type Server interface {
	Serve(ctx context.Context, h Handler) error
	Addr() string
	Close() error
}

type Client interface {
	Snapshot(ctx context.Context, ns v1.NamespaceName) (Snapshot, error)
	Feed(ctx context.Context, since uint64, timeline string, wait time.Duration) (Feed, error)
	Close() error
}

type Connector interface {
	Join(ctx context.Context, server string, trusted func(caDER []byte) bool, in JoinRequest) (JoinResponse, []byte, error)
	Dial(id Identity) (Client, error)
}

type Schemes map[string]Connector // a node's Connector per URL scheme: "https", "nats"

type Snapshot struct {
	Rev      uint64         `json:"rev"`
	Timeline string         `json:"timeline"`
	Objects  []store.Change `json:"objects"`
}

type Feed struct {
	Rev     uint64         `json:"rev"` // the revision to resume after
	Changes []store.Change `json:"changes,omitempty"`
}

// internal/store
type ChangeLog interface {
	Changes(ctx context.Context, since uint64, limit int, keep func(Change) bool) ([]Change, uint64, error)
	Head(ctx context.Context) (uint64, error)
	ChangeSignal() <-chan struct{}
	Timeline() string
}

func WithChangeLog(retain int) Option                       // retain <= 0 keeps 100,000
func WithoutChangeLog(ctx context.Context) context.Context   // a node's status write-back
func WithOrigin(ctx context.Context, node v1.ObjectName) context.Context
func AsCopy(ctx context.Context) context.Context             // a copy keeps its original's uid and generation
```

| Part | Reads | Writes |
|---|---|---|
| control plane | `cluster.driver`, `cluster.listenAddr`; the change log | the change log, in each write's transaction |
| node agent | the snapshot, the feed; `cluster.dataDir/cursor` | its own store; `cluster.dataDir/cursor` |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `internal/nodeapi/nodeapi.go` (the port),
`nodeapicontract/contract.go`, `longpoll/` and `nats/` (the drivers); `internal/store/changelog.go`;
`internal/multinode/handler.go` (`Snapshot`, `Changes`, `Changed`) and `wake.go` (the router);
`internal/multinode/agent/agent.go` (`follow`, `syncAll`, `apply`, the cursor) and `copies.go` (`upsert`, `delete`).

| Scenario | Test |
|---|---|
| follow-a-change | `TestANodeJoinsFollowsItsNamespaceAndReportsBack`, `TestAWriteWakesOnlyTheNodesItConcerns`, `TestANodesCopyIsTheControlPlanesObject`, `TestChangeLogRecordsEachWriteInOrder` |
| resume-after-a-restart | `TestANodeResumesFromItsSavedRevision`, `TestTheChangeLogIsReadFromTheAskedRevision` |
| behind-the-floor | `TestANodeBehindTheChangeLogsFloorTakesASnapshot`, `TestChangeLogCompactsAndRefusesARevisionBelowItsFloor`, the contract's `a-gone-revision-fails-its-precondition` |
| a-failed-write | `TestAChangeTheNodeFailedToWriteComesWithASnapshot`, `TestAFailedSnapshotIsTakenAgain` |
| a-record-it-cannot-decode | `TestARecordTheNodeCannotDecodeIsSkipped` |
| control-plane-away | `TestANodeFollowsAgainOnceItsControlPlaneIsBack` |
| control-plane-restored | `TestANodeOutlivesTheRestoreOfItsControlPlane`, `TestScenarioRestoreTakesNewTimeline` |
| over-nats | `TestAWorkerNodeRunsTheNamespacePlacedOnItOverNATS`, `TestScenarioNATSPassesTheNodeAPIContract`, `TestANodePublishesUnderItsOwnNameOnly`, `TestAnAnswerGoesOnlyIntoTheInboxOfTheNodeThatAsked` |

Done when every test above passes, `just check-specs` passes the spec and its mutants, and `just ci` is green.

## Review checklist

- [ ] No code path on the control plane opens a connection to a node, on either driver.
- [ ] A write and its change-log record commit in one transaction, and a status write-back writes no record.
- [ ] A revision below the floor or of another timeline is `fault.PreconditionFailed`: 410 on HTTPS, 412 on NATS.
- [ ] `Snapshot` reads the head before it lists, and leaves out the children the node owns.
- [ ] A feed never carries a record of the node's own writes, nor one of a namespace placed on another node.
- [ ] The router reads the log once per record, whatever the number of nodes that wait.
- [ ] The agent saves its cursor only after the batch or the snapshot is applied, and not at all after a write it
      failed, but one of a record it cannot decode (`fault.Invalid`).
- [ ] Each oracle test carries its `oracle:` line, and the spec passes with its mutants failing.

## Consequences

- A node behind NAT, or in a container with no inbound port, follows its namespaces.
- A write reaches its node in about a millisecond on either driver, flat in the number of nodes that wait (experiment
  0031), and costs one read of the log.
- A node away for more than 100,000 writes, or one that failed to write a record, takes a snapshot of all its
  namespaces. While one write keeps failing, as on a full disk, the node gets changes only through snapshots, which
  the control plane serves it whole every 5 s at most, and saves no cursor until one is whole; each snapshot still
  removes what the control plane no longer holds.
- Each write of a control plane with the cluster option adds one record to its transaction. The log keeps the last
  100,000, and about a compaction interval more between compactions: one runs when a logged write lands on a multiple
  of 1024 revisions, which a status write-back can take instead.
- At acceptance, the blueprint's worker node no longer "exposes an API to the control plane": it dials the control
  plane, and its local API serves its workers and its own operator.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F243; feature issue #1480; tracker #1493; draft PR #1495; #1586 (the
  router); #1588 (the drivers compared).
- Experiment 0031 (`pyvvo/experiment`, private, at 56b5607): the HTTPS long poll and core NATS through the same port.
- [blueprint.md](../../blueprint.md), *Worker node* and *Single-binary process model*.
