# ADR-0372: The control plane reaches a node through calls it polls for, and a check on a node's state runs there

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, node-api, cli
- **Realizes**: [FEAT-0002/F250](../feat/0002-feat-multi-node.md) (node commands across machines)
- **Relates to**: [ADR-0365](0365-a-worker-node-follows-its-namespaces-through-the-change-log-over-a-link-it-opens.md)
  (the node API port) · [ADR-0364](0364-a-worker-node-joins-with-a-one-time-token-and-renews-its-certificate.md)
  (admission) · [ADR-0368](0368-a-namespace-is-placed-whole-on-one-node-by-its-spec-node.md) (placement) ·
  [ADR-0206](0206-restore-and-held-boot.md) (holds and quarantine) ·
  [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model)

## Context & Need

Some commands need the state of the node that runs a namespace, or an action on it: its logs and run logs, its dead
letters and their replay, an App's hook retry, a volume delete, its holds and quarantine, its backup status; so do
five checks before a write (F250). `funcdctl` must behave the same wherever a namespace runs, and a node that is
offline must answer at once instead of hanging. The control plane never dials a node (ADR-0365). The decider chose on
2026-11-16 (D9, #1487) calls that the node picks up: offline means no poll within 3 s, a node gets 10 s to answer, and
a node answers only the calls about itself and the namespaces it runs.

Its purpose: the control plane's API sends each such command to the node of its namespace, the node answers from its
own parts, and the checks that read a node's state run on that node.

## Scenarios

- `scenario: logs-from-the-node`: Given Function f1 of e1 on vm1, When `funcdctl logs f1 -n e1`, or a read of a run's
  logs, runs against the control plane, Then the lines come from vm1's logs.
- `scenario: a-command-on-the-node`: Given a dead letter of e1, When an admin replays it on the control plane, Then
  vm1 replays it.
- `scenario: offline-at-once`: Given vm1 stopped, When a log read of e1 is asked, Then it is 503 `worker node vm1 of
  namespace e1 is offline`, at once.
- `scenario: a-node-that-does-not-answer`: Given vm1 took a call, When it does not answer within 10 s, Then the call is
  503 `did not answer within 10s`, and a later answer to it is refused.
- `scenario: only-its-calls`: Given vm1 runs e1 and not e2, When a call about e2 reaches vm1, or a hold release or a
  quarantine return that names e2, Then vm1 answers 403.
- `scenario: a-check-on-the-node`: Given SQLDatabase db1 of e1 on vm1, protected from deletion, When an admin deletes it
  on the control plane, Then vm1's own check refuses it, as on one host.
- `scenario: no-check-needed`: Given vm1 offline, When an admin applies a Function of e1 that declares no volume, or a
  Restore of e1 that is not in place, Then the write passes.
- `scenario: needs-an-offline-node`: Given vm1 offline, When an admin deletes kv1, Then the write is 503 at once.
- `scenario: a-nodes-own-hold`: Given vm1 held after its restore, When an admin asks for the hold of vm1 by its name,
  Then vm1 answers with its own.
- `scenario: an-ended-certificate`: Given vm1 on NATS whose certificate the control plane no longer takes, with its
  link open, When a call is sent after its first refused request, Then the call fails at once as offline.
- `scenario: a-renewed-certificate`: Given vm1 on NATS that renewed its certificate, When a late request under the old
  one is refused, Then calls still go to vm1 under the new one.

## Scope

- **In**: node calls, their delivery on both drivers, the node's answers, and the checks before a write that run on a
  node.
- **Out**: what each command does on one host (its own ADR); the node's own report into its WorkerNode (F245, F251);
  a BackupSchedule's target checked against a node's blob store (F251).

## Constraints & Decision drivers

- The control plane never dials a node (ADR-0365).
- A command acts where its namespace runs, and answers as on one host (F250).
- An offline node answers at once; no call hangs past a bound (F250, D9).
- A node acts only for itself and the namespaces it runs.

## Alternatives considered

- **The control plane dials the node for each command**: a node behind NAT has no inbound port. Rejected.
- **Commands copied into the node's objects** (a desired state the feed carries): a log read or a check before a write
  needs an answer now, not a reconciled object. Rejected.
- **A call that waits for an offline node to come back**: a person's command would hang. Rejected for an answer at
  once.
- **Every check before a write run on the node**: a write in a namespace whose node is away would fail even when it
  needs nothing of the node. Rejected for the five checks that read its state.

## Decision

1. **Node calls.** The commands that need a namespace's node, its logs and run logs, its dead letters (list, read,
   discard and replay), an App's hook retry, a volume delete, a namespace's hold release and quarantine return, and
   those about a node itself, its hold's status and release and its backup's status, become a `nodeapi.Call` that the
   control plane's `Server.Call` sends to the node. The API answers as the node's own would: the answer carries the
   lines or the body, or the error and its api/fault kind.
2. **Delivery.** On HTTPS, the control plane queues each call; the node long-polls its queue (`GET /node/v1/calls`,
   every queued call at once) and posts each answer (`POST /node/v1/calls/{id}`), which only the node the call went to
   may post. A node with no poll open and none within `callOffline` 3 s is offline. On NATS, a call is a request on
   `funcd.call.<node>.<serial>`, the serial of the certificate the node's last admitted request came with; a request
   refused as unauthorized withdraws that serial while it is still the node's, since a NATS reply passes no admission,
   and no serial or no subscriber is offline. On either driver a call to an offline node fails at once, 503 `worker
   node <node> of namespace <ns> is offline`, or `worker node <node> is offline` for a call about the node itself; a
   node gets `CallTimeout` 10 s to answer, else 503 `did not answer within 10s`, and an answer after that is refused on
   HTTPS and reaches no caller on NATS.
3. **The node's side.** The agent long-polls its calls and answers each in its own goroutine, from its local parts. It
   answers only a call about itself or about a namespace placed on it: any other is 403.
4. **Checks before a write.** The five admissions that read a node's state, the deletion protection of a KVStore, a
   Bucket and a SQLDatabase, the booking of a Function's or a Job's volumes (only when it declares one) and the
   in-place restore guard (only for an in-place Restore), run on the namespace's node: an `admit` call carries the
   request, and the node runs its own copy of the check (`multinode.NodeState`). The others, the quotas among them,
   read the metastore only and stay on the control plane. A write that needs an offline node's check is 503 at once;
   one that needs none goes through while the node is away.

## Design model

- **Spec**: [`docs/design/multinode/calls.fizz`](../design/multinode/calls.fizz) · state machine
  [`calls.md`](../design/multinode/calls.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NoCallWaitsPastItsTimeout` | always | `scenario: a-node-that-does-not-answer` | `TestACallThatTimesOutRefusesItsLateAnswer` |
  | `NoAnswerAfterItsCallEnded` | always | `scenario: a-node-that-does-not-answer` | `TestACallThatTimesOutRefusesItsLateAnswer` |
  | `NoCallWaitsForAnOfflineNode` | always | `scenario: offline-at-once`, `scenario: needs-an-offline-node` | `TestCallsFailAtOnceForAnOfflineNode` |

- **Bounds**: one call, one node that may stop, an offline window of one tick shorter than a timeout of two (as 3 s is
  to 10 s), three ticks of time, 6 actions: the smallest scenario in which every mutant fails (at 5 actions `no-drop`
  passes, at two ticks `no-timeout` does). Wider (a second call, queued while the first is taken, so one poll takes
  both and a timeout drops one of two; 8 actions): pass, 1,479 states, 0.5 s.
- **Faults**: the node stops for good, once, before or after it takes a call.
- **Environment**: a send, a poll, a drop and an answer are each one step (the queue's lock); a poll that is open
  counts as one within the window; a caller's timer fires at its deadline, before time moves on; NATS has the same
  shape, with no responders as the offline answer.
- **Mutants**: `no-timeout` fails `NoCallWaitsPastItsTimeout`; `no-drop` (a call that timed out stays waiting for its
  answer) fails `NoAnswerAfterItsCallEnded`; `no-offline-check` fails `NoCallWaitsForAnOfflineNode`.
- **Result**: pass, 145 states, 0.3 s, FizzBee v0.5.4.
- **Left to tests**: who may answer (`TestCallsFailAtOnceForAnOfflineNode`,
  `TestAnAnswerGoesOnlyIntoTheInboxOfTheNodeThatAsked`); a node's 403 for another namespace
  (`TestANodeAnswersNoCallAboutANamespaceThatDoesNotRunOnIt`) and the checks on the node
  (`TestTheChecksBeforeAWriteRunOnTheNodeThatHoldsTheState`); NATS's offline answers
  (`TestANodeThatLeavesIsOfflineAtOnce`, `TestANodeWhoseLinkGoesSilentIsOfflineOnceTheLinkEnds`,
  `TestANodeWhoseCertificateEndedGetsNoCall`) and its serial guard
  (`TestALateRequestUnderAReplacedCertificateKeepsTheNodesCalls`). NATS's timeout has no test of its own: its
  `CallTimeout` is a constant there, and the long poll's timeout test covers the rule.

## Behavior rows

- **multi-node.md**: "A node call" (once "A log read of a node's namespace", to which this ADR added a call's timeout),
  "A node command", "A check before a write", "A namespace's hold", "A call to a node on NATS" (which this ADR changed:
  a refused certificate gets no call), and the reads through a node's queue in "A node's own hold and backup" (ADR-0373
  holds the rest of that row) cite this ADR's Decisions at acceptance.

## Temporary workarounds

None.

## Contracts

```text
GET  /node/v1/calls?wait=<dur>          # HTTPS: the node's queued calls; NATS: funcd.call.<node>.<serial>
POST /node/v1/calls/{id}                # the node's answer; NATS: the reply to the call's request
```

```go
// internal/nodeapi
const CallTimeout = 10 * time.Second

type Call struct {
	ID        string           `json:"id"`
	Kind      string           `json:"kind"` // one of the CallLogs ... CallBackupStatus constants

	Namespace v1.NamespaceName `json:"namespace,omitempty"` // empty in a call about the node itself
	Run       v1.ObjectName    `json:"run,omitempty"`
	Name      string           `json:"name,omitempty"`
	Volume    v1.ObjectName    `json:"volume,omitempty"`
	Body      json.RawMessage  `json:"body,omitempty"`
	Query     logread.Query    `json:"query"`
}

type Answer struct {
	Lines []logread.Line  `json:"lines"`
	Body  json.RawMessage `json:"body,omitempty"`
	Error string          `json:"error,omitempty"`
	Fault fault.Kind      `json:"fault,omitempty"`
}

type Server interface {
	Call(ctx context.Context, node v1.ObjectName, c Call) (Answer, error) // Unavailable at once for an offline node
}

type Client interface {
	Calls(ctx context.Context, wait time.Duration) ([]Call, error)
	Answer(ctx context.Context, id string, a Answer) error
}
```

| Part | Reads | Writes |
|---|---|---|
| control plane | the namespace's placement; the node's answers | nothing but what each command writes on one host |
| node agent | its calls; its local parts | its answers; what each command writes on its node |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `internal/nodeapi/nodeapi.go` (`Call`, `Answer`, the call
kinds), `longpoll/calls.go` (the queue), `nats/nats.go` (`Call`, the serials), `internal/multinode/calls.go`
(`LogRouter`, `RunLogRouter`), `commands.go` (the command routers, `Nodes`), `admit.go` (`NodeState`, `Forward`), and
`internal/multinode/agent/report.go` (`serveCalls`, `answer`, `run`, `admit`). This ADR changed the built code: on NATS
a refused request withdraws its certificate from the calls, and the long poll's call timeout is a field its test
shortens.

| Scenario | Test |
|---|---|
| logs-from-the-node | `TestANodeJoinsFollowsItsNamespaceAndReportsBack`, the contract's `a-call-reaches-the-node-that-polls` |
| a-command-on-the-node | `TestTheNodeCommandsRunOnTheNodeThatRunsTheNamespace` |
| offline-at-once | `TestCallsFailAtOnceForAnOfflineNode`, `TestANodeThatLeavesIsOfflineAtOnce` |
| a-node-that-does-not-answer | `TestACallThatTimesOutRefusesItsLateAnswer` |
| only-its-calls | `TestANodeAnswersNoCallAboutANamespaceThatDoesNotRunOnIt` |
| a-check-on-the-node | `TestTheChecksBeforeAWriteRunOnTheNodeThatHoldsTheState` |
| no-check-needed | `TestAWriteThatNeedsAnOfflineNodeIsRefusedAtOnce` |
| needs-an-offline-node | `TestAWriteThatNeedsAnOfflineNodeIsRefusedAtOnce` |
| a-nodes-own-hold | `TestANodesOwnHoldAndBackupAnswerByItsName`, `TestANamespacesHoldIsReleasedOnTheNodeThatRunsIt` |
| an-ended-certificate | `TestANodeWhoseCertificateEndedGetsNoCall` |
| a-renewed-certificate | `TestALateRequestUnderAReplacedCertificateKeepsTheNodesCalls` |

Done when every test above passes, `just check-specs` passes the spec and its mutants, and `just ci` is green.

## Review checklist

- [ ] No command that needs a node's state is answered from the control plane's own store for a namespace a node runs.
- [ ] A call to an offline node fails at once, and every call ends within `CallTimeout`.
- [ ] On the long poll only the node a call went to may answer it, and on NATS a request refused as unauthorized
  withdraws its serial while it is still the node's.
- [ ] The agent answers 403 to a call about a namespace not placed on it, and to a hold release or a quarantine return
  that names one.
- [ ] Only the five node-state checks run on a node, each only when it reads the node's state.
- [ ] Each oracle test carries its `oracle:` line, and the spec passes with its mutants failing.

## Consequences

- `funcdctl` behaves the same wherever a namespace runs, and an offline node answers at once.
- On HTTPS a node keeps a second long poll open, for its calls, beside its feed.
- A write that needs an offline node's check waits for that node, by being refused until it is back.
- On NATS, a node whose certificate ended gets calls until its next request is refused, at most one long-poll wait,
  25 s.
- A call that timed out may still run on the node, which runs each call to its end: a replay, a discard, a volume
  delete or a hold release answered 503 `did not answer within 10s` can still complete, and a person who retries it
  runs it again.
- A hold release across nodes is not atomic: the control plane releases its own namespaces first, then each node's in
  turn, and stops at the first error with the earlier releases kept.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F250; feature issue #1487; tracker #1493; draft PR #1495.
- The ADR-0364 judge's routed finding: calls on NATS to a node whose certificate ended (Decision 2).
