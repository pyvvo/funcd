# ADR-0374: A node may run an older release, never a newer one, and a field it is too old for is refused

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, node-api, upgrades
- **Realizes**: [FEAT-0002/F252](../feat/0002-feat-multi-node.md) (nodes on older releases)
- **Relates to**: [ADR-0365](0365-a-worker-node-follows-its-namespaces-through-the-change-log-over-a-link-it-opens.md)
  (the node API and a record a node cannot decode) ·
  [ADR-0364](0364-a-worker-node-joins-with-a-one-time-token-and-renews-its-certificate.md) (the join and admission) ·
  [ADR-0367](0367-a-workernode-is-declared-by-name-and-reads-ready-while-its-node-polls.md) (the version a node
  reports)

## Context & Need

funcd ships releases every few days, so the nodes of a platform cannot all upgrade at once (F252). A node on an older
release must keep working with a newer control plane, within the node API versions the control plane serves; a node
on a newer release than its control plane could rely on what the control plane does not have. A new spec field that an
older node does not know would be dropped on that node without a word. The decider chose on 2026-11-16 (D12, #1489):
an older node runs, a newer one is refused, and the control plane refuses a spec that a namespace's node is too old to
run.

Its purpose: the node API's version and the releases are checked at each request, and the spec fields a release adds
are checked at each write against the node that would run them.

## Scenarios

- `scenario: an-older-node`: Given a control plane on a release and vm1 on an older one that speaks a served node API
  version, When vm1 joins and follows, Then it runs its namespaces.
- `scenario: a-newer-node`: Given a control plane on v0.40.0, When a node on a newer release joins or makes a request,
  Then it is refused (409 `newer than the control plane's`).
- `scenario: a-branch-build`: Given a node or a control plane on a build whose release does not order, When the node
  joins, Then it passes.
- `scenario: another-node-api`: Given a node that speaks a node API version the control plane does not serve, When it
  makes any request, Then it is refused: 426 on HTTPS, 409 `upgrade or downgrade the node` on NATS.
- `scenario: a-field-too-new`: Given vm1 speaks node API version 1 and a field needs 2, When an admin sets that field
  in a namespace on vm1, Then the write is 400 `upgrade the node first`; on a node that has not joined, it passes.
- `scenario: a-new-field-is-named`: Given a release that adds a spec field, When its tests run, Then they fail until
  the field names the node API version it needs, or is marked as one an older node may ignore.

## Scope

- **In**: the node API version, the release order between a node and its control plane, and the spec fields a node is
  too old for.
- **Out**: how a release is cut and numbered; the upgrade of the control plane's own stores (each store's ADR); a
  record of a kind a node does not know, which it skips (ADR-0365 Decision 6).

## Constraints & Decision drivers

- Nodes upgrade one at a time, after the control plane (F252).
- A node never runs what its control plane does not have (D12).
- No spec a node cannot run reaches it unnoticed (F252).

## Alternatives considered

- **Every node on the control plane's release**: a fleet would upgrade at once, at each release. Rejected.
- **A newer node allowed**: it could write back what an older control plane cannot read or check. Rejected.
- **Older nodes drop what they do not know**, as JSON decoding does: a person's new field would silently do nothing on
  that node. Rejected for a check at the write.
- **One node API version per release**: every release would break every older node. Rejected for a version that
  changes only when the interface does.

## Decision

1. **The node API's version.** A node sends, with every request, the node API version it speaks (`APIVersion`, 1),
   its release and its platform (`Funcd-Node-Api`, `Funcd-Node-Version` and `Funcd-Node-Platform`, on both drivers).
   A control plane serves `SupportedAPIVersions`: its own version, and the previous one once there is one. Any other
   is refused before the request, 426 on HTTPS and 409 `upgrade or downgrade the node` on NATS.
2. **Never a newer node.** A node whose release is newer than its control plane's is 409 `newer than the control
   plane's: upgrade the control plane first`, at its join and at every request (`Admit`, ADR-0364). Releases that do
   not order, such as a build of a branch, pass. So the control plane upgrades first, then the nodes.
3. **A field a node is too old for.** A spec field that a release adds needs a node API version of its own, named in
   `FieldVersions` by kind and JSON path (a list's items and a map's values, as `*`), unless an older node may ignore
   it; a test holds the spec fields against a list of the known ones, and fails on a new one that neither names a
   version nor is added to that list. A write that sets such a field in a namespace whose node speaks an older version
   is 400 `upgrade the node first` (the admission `node-too-old`). A node that has not joined has no known version, so
   writes to its namespaces are not checked; a namespace on the control plane's own node has no node to check.

## Design model

None: the decision adds no state, timer or actor. Each check compares two versions at one request or one write, and a
node's version comes with each of its requests (ADR-0367).

## Behavior rows

- **multi-node.md**: "A node on a newer release", "A field a node is too old for" and the rows on the node API's
  version cite this ADR's Decisions at acceptance.

## Temporary workarounds

None.

## Contracts

```text
Funcd-Node-Api: 1                  # every request of a node, on both drivers
Funcd-Node-Version: v0.49.2
Funcd-Node-Platform: linux/arm64
426 this control plane serves node API versions [1]                  # HTTPS; NATS: 409 ... upgrade or downgrade the node
409 this node runs funcd v0.51.0, newer than the control plane's v0.50.0: upgrade the control plane first
400 worker node vm1 speaks node API version 1, and spec.retry needs version 2: upgrade the node first
```

```go
// internal/nodeapi
const APIVersion = 1

var SupportedAPIVersions = []int{APIVersion}

func Supported(v int) bool

// internal/multinode
type FieldVersions map[v1.Kind]map[string]int // the node API version each new spec field needs, by kind and path
```

| Part | Reads | Writes |
|---|---|---|
| each driver | a request's node API version | refuses or passes |
| `Admit`, the join | the node's release and the control plane's | refuses or passes |
| admission `node-too-old` | the write, its namespace's node, `status.nodeAPIVersion` | refuses or passes |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `internal/nodeapi/nodeapi.go` (`APIVersion`,
`SupportedAPIVersions`), `longpoll/longpoll.go` and `nats/nats.go` (the headers and the refusal), `internal/multinode/
versions.go` (`newerRelease`, `FieldVersions`, `nodeTooOld`), `internal/multinode/fields_test.go` and its golden list
`internal/multinode/testdata/node-api-fields.txt`, and `internal/platform/version` (`Compare`).

| Scenario | Test |
|---|---|
| an-older-node | `TestANodeJoinsFollowsItsNamespaceAndReportsBack`, `TestCompareVersions` |
| a-newer-node | `TestANodeOnANewerReleaseThanTheControlPlaneIsRefused` |
| a-branch-build | `TestCompareVersions` |
| another-node-api | `TestANodeOnAnotherNodeAPIVersionGets426`, `TestANodeOnAnotherNodeAPIVersionIsRefused` |
| a-field-too-new | `TestASpecFieldANodeIsTooOldForIsRefused`, `TestAFieldIsFoundInListsAndMapsToo` |
| a-new-field-is-named | `TestANewSpecFieldNamesTheNodeAPIVersionItNeeds` |

Done when every test above passes and `just ci` is green.

## Review checklist

- [ ] Every request of a node carries its node API version, release and platform, on both drivers.
- [ ] A node API version outside `SupportedAPIVersions` is refused before the request runs.
- [ ] A newer node release is refused at the join and in `Admit`; an unordered one passes.
- [ ] A new spec field names its node API version in `FieldVersions`, or is in the known list.
- [ ] `node-too-old` checks only a joined node, and only the fields `FieldVersions` names.

## Consequences

- The control plane upgrades first, and the nodes follow one by one, each while its namespaces keep running.
- A field a namespace's node is too old for cannot be used there until the node upgrades.
- The node API keeps the previous version alongside its own, so a node may lag the control plane by one interface
  change.
- A record of a kind an older node does not know is skipped there (ADR-0365 Decision 6), so an object of a newer kind
  does not run on that node.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F252; feature issue #1489; tracker #1493; draft PR #1495.
