# ADR-0367: A WorkerNode is declared by name, and reads Ready while its node polls

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, node-api, status
- **Realizes**: [FEAT-0002/F245](../feat/0002-feat-multi-node.md) (node liveness and declared nodes)
- **Relates to**: [ADR-0364](0364-a-worker-node-joins-with-a-one-time-token-and-renews-its-certificate.md) (the join
  and admission, which records each contact) ·
  [ADR-0365](0365-a-worker-node-follows-its-namespaces-through-the-change-log-over-a-link-it-opens.md) (the node API) ·
  [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model)

## Context & Need

Operators need to see which worker nodes run, and automation needs to declare a node and its namespaces before the
machine exists (F245; F253 later builds on it). The decider chose on 2026-11-16 (#1482) a WorkerNode that an admin
declares by name, Pending until a node joins, then Ready while the node reports and not Ready when it stops. Since the
control plane never dials a node (ADR-0365), what it knows of a node is the node's own requests.

Its purpose: the control plane's `Liveness` turns each admitted request of a node into the WorkerNode's phase and Ready
condition, which `funcdctl get workernodes` shows, and which the join reads to keep a running node's name (ADR-0364).

## Scenarios

- `scenario: declared-before-its-machine`: Given an admin applies WorkerNode vm1, When no node has joined as vm1, Then
  it reads Pending, its Ready condition False `NotJoined`.
- `scenario: ready-while-polling`: Given vm1 joined, When it polls, Then it reads Ready (`Polling`), with its release,
  node API version and platform.
- `scenario: degraded-when-it-stops`: Given vm1 Ready, When it has not polled for 40 s, Then it reads Degraded, its
  Ready condition False `NotPolling`, within 5 s more; When it polls again, Then it reads Ready.
- `scenario: last-seen-paced`: Given vm1 polls every few seconds, When it does, Then its `lastSeen` is written at most
  every 30 s.
- `scenario: control-plane-restart`: Given vm1 Ready, When the control plane restarts and vm1 polls again within 40 s,
  Then vm1 never reads Degraded, and a join under its name is refused meanwhile.

## Scope

- **In**: the WorkerNode kind, its declaration, its phase, its Ready condition and the contact fields of its status.
- **Out**: the join and the certificate (ADR-0364); a Namespace's `spec.node`, and why a WorkerNode a Namespace names
  cannot be deleted (F246); a node's offline for its calls, no poll within 3 s (F250); the node's report of its blob
  store and its backup (F247, F251); provisioning a node's machine (F253).

## Constraints & Decision drivers

- The control plane never dials a node: liveness comes from the node's own requests (ADR-0365).
- The platform's phases have no NotReady: a node that stopped reads Degraded with its Ready condition False (the
  decider's rule 4 in #1495).
- A status write is no change-log record: no node follows WorkerNodes (ADR-0365 Decision 3).

## Alternatives considered

- **A heartbeat request of its own**: every node request already passes `Admit`, and a node long-polls at least every
  `DefaultWait` 25 s, so a heartbeat would only add traffic. Rejected.
- **A NotReady phase**, as Kubernetes nodes have: a phase the platform's other kinds do not have, for what Degraded with
  a False Ready condition already says. Rejected by the decider's rule 4.
- **A `lastSeen` written at each request**: one metastore write per node request. Rejected for `defaultSeenEvery`.
- **Liveness kept in memory alone, with no assumption at start**, as first built: a control plane's restart flipped
  every node to Degraded until its next poll. Rejected (Decision 3).

## Decision

1. **Declared nodes.** WorkerNode is a cluster-scoped kind with no spec: an admin applies it by name, a DNS-1123 label,
   and the control plane writes its status. It reads Pending, its Ready condition False `NotJoined`, until a node joins
   under that name (ADR-0364).
2. **Liveness.** Each request that `Admit` lets in (ADR-0364 Decision 3) records the node's contact: when, and the
   release, node API version and platform it sent. A WorkerNode reads Ready, its Ready condition True `Polling`, while
   its node's last contact is under `defaultReadyFor` 40 s old; Degraded, False `NotPolling`, once it is not and the
   node holds a certificate; Pending otherwise. Every `livenessTick` 5 s the control plane writes each status whose
   phase or condition changed, and `lastSeen` with what the node sent at most every `defaultSeenEvery` 30 s, outside
   the change log.
3. **A control plane's start.** The contacts live in memory. A control plane that starts takes each node whose
   WorkerNode reads Ready as polled at its start, and writes no `lastSeen` for it: a node that polls again within
   `defaultReadyFor` stays Ready, and its name stays held against a join (ADR-0364 Decision 2); one that does not reads
   Degraded then.

## Design model

- **Spec**: [`docs/design/multinode/liveness.fizz`](../design/multinode/liveness.fizz) · state machine
  [`liveness.md`](../design/multinode/liveness.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NoReadyLongAfterTheLastPoll` | always | `scenario: degraded-when-it-stops` | `TestAWorkerNodeReadsReadyWhileItsNodePolls`, `TestAControlPlaneThatStartsFlipsNoNodeThatPollsAgain` |
  | `NoDegradedWhilePolling` | always | `scenario: control-plane-restart` | `TestAControlPlaneThatStartsFlipsNoNodeThatPollsAgain` |

- **Bounds**: one node, a window of two ticks, four ticks of time, one stop, one restart, 8 actions: the smallest
  scenario in which every mutant fails. The full space is 226 states; wider (five ticks): pass, 346 states, 0.4 s.
- **Faults**: the node stops for good, once; the control plane restarts once and loses what it kept.
- **Environment**: a node's request and the record of its contact are one step (`Admit`); a sync reads the clock and
  writes the phase in one step (`writeNodeStatus` retries a conflict); a sync runs at least once per tick
  (`livenessTick`); a running node polls every tick, as a long poll comes back within its wait.
- **Mutants**: `no-assume` fails `NoDegradedWhilePolling`; `no-window` and `assume-degraded` fail
  `NoReadyLongAfterTheLastPoll`.
- **Result**: pass, 94 states, 0.3 s, FizzBee v0.5.4.
- **Left to tests**: the pacing of `lastSeen` (`TestALastSeenIsWrittenAtMostEverySeenEvery`); the contact a real node
  makes (`TestANodeJoinsFollowsItsNamespaceAndReportsBack`).

## Behavior rows

- **multi-node.md**: "Declaring a node" and "Liveness" (now with `TestAWorkerNodeReadsReadyWhileItsNodePolls`) cite
  Decisions 1 and 2 at acceptance; a row "A control plane's start" for Decision 3, with
  `TestAControlPlaneThatStartsFlipsNoNodeThatPollsAgain`.

## Temporary workarounds

None.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: WorkerNode
metadata:
  name: vm1
status:
  phase: Ready
  conditions:
    - type: Ready
      status: "True"
      reason: Polling
  version: v0.50.0
  nodeAPIVersion: 1
  platform: linux/arm64
  lastSeen: 2026-11-27T18:00:00.000Z
```

```go
// internal/multinode
const (
	ReasonNotJoined  = "NotJoined"
	ReasonPolling    = "Polling"
	ReasonNotPolling = "NotPolling"
)

func NewLiveness(st store.Store, log *slog.Logger, now func() time.Time, readyFor, seenEvery time.Duration) *Liveness
func (l *Liveness) Seen(node v1.ObjectName, version string, api int, platform string) // a contact, from Admit
func (l *Liveness) Ready(node v1.ObjectName) bool                                     // the join's guard
func (l *Liveness) Run(ctx context.Context)                                           // the start, then a sync per tick
func (l *Liveness) Sync(ctx context.Context) error
```

| Part | Reads | Writes |
|---|---|---|
| control plane | each admitted node request; the WorkerNodes at its start | each WorkerNode's `status.phase`, Ready condition, `lastSeen`, `version`, `nodeAPIVersion` and `platform` |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `api/types/v1alpha1/workernode.go` (the kind),
`internal/multinode/liveness.go` (`Liveness`), `handler.go` (`Admit` records the contact), `cluster.go` (runs it).
This ADR's Decision 3 changed the built code: `Liveness.Run` starts with `assumeReady`.

| Scenario | Test |
|---|---|
| declared-before-its-machine | `TestAWorkerNodeReadsReadyWhileItsNodePolls` |
| ready-while-polling | `TestAWorkerNodeReadsReadyWhileItsNodePolls`, `TestANodeJoinsFollowsItsNamespaceAndReportsBack` |
| degraded-when-it-stops | `TestAWorkerNodeReadsReadyWhileItsNodePolls` |
| last-seen-paced | `TestALastSeenIsWrittenAtMostEverySeenEvery` |
| control-plane-restart | `TestAControlPlaneThatStartsFlipsNoNodeThatPollsAgain` |

Done when every test above passes, `just check-specs` passes the spec and its mutants, and `just ci` is green.

## Review checklist

- [ ] A contact is recorded only for a request `Admit` let in, on either driver.
- [ ] A status is written only where its phase or condition changed, or its `lastSeen` is `defaultSeenEvery` old, and
      never into the change log.
- [ ] A control plane that starts assumes a contact only for a WorkerNode that reads Ready, and writes no `lastSeen`
      from it.
- [ ] Each oracle test carries its `oracle:` line, and the spec passes with its mutants failing.

## Consequences

- An operator sees which nodes run, and automation can declare a node before its machine exists.
- A node that stops reads Degraded 40 to 45 s after its last request.
- A control plane's restart flips no node that polls again within 40 s; a node that stopped just before the restart
  reads Ready up to 40 s after the start.
- A node whose own store keeps refusing writes still reads Ready, since it polls; its log says what fails (routed from
  the ADR-0365 judge).

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F245; feature issue #1482; tracker #1493; draft PR #1495.
