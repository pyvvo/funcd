# ADR-0371: A node holds the Secrets and the master of its own namespaces only

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, security, secrets
- **Realizes**: [FEAT-0002/F249](../feat/0002-feat-multi-node.md) (Secrets and keys on nodes)
- **Relates to**: [ADR-0365](0365-a-worker-node-follows-its-namespaces-through-the-change-log-over-a-link-it-opens.md)
  (the feed that carries the Secrets) · [ADR-0022](0022-secrets-service.md) (the Secrets encryptor) ·
  [ADR-0327](0327-a-function-writes-the-secrets-it-owns.md) (a workload's Secret write) ·
  [ADR-0270](0270-derived-s3-key-pairs-rotate.md) (S3 pairs derived from a master) ·
  [ADR-0366](0366-a-worker-node-writes-back-the-status-of-what-it-runs-and-the-objects-it-makes.md) (what a node writes
  back) · [ADR-0373](0373-each-process-backs-up-the-stores-it-holds-and-a-node-under-a-prefix-of-its-own.md) (a node's
  backup, which names no key)

## Context & Need

A node's workers read their Secrets and the credentials funcd derives for them (S3 pairs, SQL engine passwords and
change-feed tokens, lake tokens and add-on proxy tokens) on the node, also while the control plane is away. An all-in-one derives them all from
its own master; a node that held that master could derive every namespace's. The decider chose on 2026-11-16 (D8,
#1486) a master per namespace, derived on the control plane, of which a node holds only its own namespaces', and a
node's Secrets encrypted at rest with a key of its own.

Its purpose: a leaked node exposes only the namespaces it runs, and a namespace keeps its credentials on any node.

## Scenarios

- `scenario: only-its-masters`: Given vm1 runs e1 and vm2 runs e2, When vm1 asks for e2's master, Then it is refused
  (403), and vm1 keeps e1's alone.
- `scenario: waiting-for-the-master`: Given e1 placed on vm1, whose master is not there yet, When a Function of e1
  starts on vm1, Then it waits Pending `NamespaceKeyMissing` until the master arrives.
- `scenario: credentials-from-the-namespaces-master`: Given vm1 holds e1's master, When a workload of e1 on vm1 takes
  its S3 pair, lake token, add-on proxy token or SQL engine password, Then each derives from e1's master, and verifies
  under it alone.
- `scenario: a-namespace-leaves`: Given vm1 runs e1, When e1 leaves vm1, Then vm1 removes e1's master; a node that
  stopped during that removal removes it at its next start.
- `scenario: secrets-encrypted-on-the-node`: Given a node whose config names no `secrets.encryptionKeyFile`, When it
  starts, Then it makes `cluster.dataDir/secrets.key`, which its store's encryptor takes, and keeps its Secrets
  encrypted with it.
- `scenario: a-sealed-record`: Given a control plane with `secrets.encryptionKeyFile`, When a Secret is written there,
  Then its change-log record is sealed at rest, and the node's feed serves it decrypted over the node's link.
- `scenario: a-workload-writes-a-secret`: Given a workload of e1 on vm1 that may write Secret s, When it writes a key,
  Then the control plane makes the write as for its own workloads, and the workload reads it back on vm1; while the
  control plane is away, the write is 503 `secrets.control-plane-away`.

## Scope

- **In**: the masters per namespace, where a node keeps them, its Secrets key, and a workload's Secret write on a node.
- **Out**: what each credential is and how it rotates (ADR-0270 and the services' ADRs); who may read or write a Secret
  (ADR-0321, ADR-0327); a node's backup, which names no key (ADR-0373).

## Constraints & Decision drivers

- A leaked node exposes only the namespaces it runs (F249).
- A namespace keeps its credentials when it moves (F249).
- A node derives its credentials while the control plane is away (F243).
- No new secret format: a master is a file like funcd's own `master.key`.

## Alternatives considered

- **Every node holds the control plane's master**: one leaked node exposes every namespace. Rejected.
- **A master drawn at random per namespace and kept in the metastore**: a store to back up and escrow, for what an
  HMAC of the control plane's master and the namespace's UID gives the same everywhere. Rejected.
- **The node asks the control plane for each credential**: no credential while the control plane is away. Rejected.
- **Secrets on a node kept in the clear, since the control plane holds them all**: a node's disk would hold them in
  the clear. Rejected for a key of the node's own.

## Decision

1. **A master per namespace.** The control plane derives each namespace's master as the HMAC-SHA256 of its own master
   over `funcd-namespace-key/v<version>/<uid>`, the namespace's UID, at `NamespaceKeyVersion` 1. A node takes it with
   the `Key` request, for a namespace placed on it only (403 otherwise), when the namespace is placed on it, and again
   after each feed answer while one is missing. It keeps it whole in `cluster.dataDir/keys/<ns>.json`, a 0600 file as
   `master.key` is, and removes the file when the namespace leaves it; at its start, before anything runs, it removes
   the master of every namespace whose local Namespace it does not hold, so a node that stopped during a removal keeps
   none. The node derives the namespace's S3 pairs, SQL engine passwords and change-feed tokens, lake tokens and add-on
   proxy tokens from it, never from its own master; an all-in-one derives every namespace's from its own master, as before. A namespace
   keeps its master on any node, since its UID does not change.
2. **Waiting for it.** A Function whose namespace's master the node lacks waits Pending, reason `NamespaceKeyMissing`,
   and a Job with S3, Lake or add-on bindings is not Ready, with the same reason; a JobRun that starts meanwhile fails
   its start with that reason's message. A SQL or Lake engine waits by failing its reconcile until the master arrives.
3. **Secrets on a node.** A node holds the Secrets of its namespaces only, which its feed brings (ADR-0365 Decision 4).
   When the control plane encrypts Secrets (`secrets.encryptionKeyFile`, ADR-0022), a Secret's change-log record is
   sealed at rest by the same encryptor, and the feed serves it decrypted over the node's mTLS link. The node's store
   encrypts them with `secrets.encryptionKeyFile`, or, when that is unset, with `cluster.dataDir/secrets.key`, 32
   random bytes in a 0600 file, made at the node's first start, which the store's encryptor takes as its key.
4. **A workload's Secret write.** It goes to the control plane, which holds the Secret, and is checked and made there
   as a write of its own workloads is (ADR-0327). The node then waits up to `secretEcho` 2 s for its copy to show it,
   so the workload reads its own write; past that, the feed brings it. A refusal of the write keeps its own reason; a
   node request that fails, with the control plane away or refusing the node, is 503 `secrets.control-plane-away`.

## Design model

- **Spec**: [`docs/design/multinode/keys.fizz`](../design/multinode/keys.fizz) · state machine
  [`keys.md`](../design/multinode/keys.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NoMasterWithoutItsNamespace` | always | `scenario: a-namespace-leaves` | `TestANodeThatStoppedWhileANamespaceLeftDropsItsMasterAtItsStart` |
  | `TheMasterOfANamespaceThatLeftGoes` | eventually always | `scenario: a-namespace-leaves` | `TestANodeKeepsTheMastersOfItsNamespacesOnly` |

- **Bounds**: one namespace that leaves, one stop, 6 actions: the smallest scenario in which every mutant fails (with
  no stop, `no-sweep` passes; at 5 actions, a path is cut short of its end). Wider (two stops, 9 actions): pass, 33
  states, 0.2 s.
- **Faults**: the node stops at any step of a removal, its store, its files and its cursor kept.
- **Environment**: the node's disk keeps what was written before a stop; the control plane's `Key` answers a node only
  for a namespace placed on it, so a node that asks again for a master it removed gets none.
- **Mutants**: `no-sweep` (a node that starts keeps every master on its disk) and `no-drop` (a removal keeps the
  master's file) fail `NoMasterWithoutItsNamespace`.
- **Result**: pass, 19 states, 0.2 s, FizzBee v0.5.4.
- **Left to tests**: the 403 of `Key` (`TestANodeKeepsTheMastersOfItsNamespacesOnly`); the master's derivation and its
  use by each service (`scenario: credentials-from-the-namespaces-master`); the wait of a Function
  (`TestAWorkerNodeHoldsAFunctionUntilItsNamespacesMasterArrives`); the seal at rest
  (`TestChangeLogServesASealedKindDecrypted`); a Secret write and its echo
  (`TestAWorkloadsSecretWriteGoesToTheControlPlane`), one request and ADR-0365's feed.

## Behavior rows

- **multi-node.md**: "A namespace's master", "Secrets on a node" and "A workload's Secret write on a node" cite this
  ADR's Decisions at acceptance.

## Temporary workarounds

None.

## Contracts

```text
cluster.dataDir/keys/<ns>.json      # a namespace's master on its node, 0600
cluster.dataDir/secrets.key         # the node's Secrets key when secrets.encryptionKeyFile is unset, 0600
GET /node/v1/keys?namespace=<ns>    # HTTPS; NATS funcd.node.<node>.<serial>.key; 403 for a namespace not placed there
POST /node/v1/secrets               # a workload's Secret write; NATS .secret
503 secrets.control-plane-away      # a workload's Secret write whose node request fails
```

```go
// internal/nodeapi
type NamespaceKey struct {
	Namespace v1.NamespaceName `json:"namespace"`
	Version   int              `json:"version"`
	Key       []byte           `json:"key"`
}

// internal/multinode
const NamespaceKeyVersion = 1

func NewNamespaceKeys(st store.Store, master []byte) *NamespaceKeys
func (k *NamespaceKeys) Of(ctx context.Context, ns v1.NamespaceName) (nodeapi.NamespaceKey, error)
```

| Part | Reads | Writes |
|---|---|---|
| control plane | its master; the Namespace's UID | nothing for a key; the Secret, on a workload's write |
| node agent | `cluster.dataDir/keys`; its local Namespaces, at its start | `cluster.dataDir/keys/<ns>.json`; `cluster.dataDir/secrets.key` at its first start |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `internal/multinode/keys.go` (`NamespaceKeys`),
`handler.go` (`Key`, `Secret`), `internal/multinode/agent/keys.go`, `children.go` (`Prepare`) and `secrets.go`,
`internal/platform/nsmaster` (which master a namespace's credentials take), `internal/function/workerenv.go`
(`NamespaceKeyMissing`), `cmd/funcd/cluster.go` (`secretsKey`), and the services that derive credentials (S3 gateway,
SQL, Lake, add-ons).

| Scenario | Test |
|---|---|
| only-its-masters | `TestANodeKeepsTheMastersOfItsNamespacesOnly` |
| waiting-for-the-master | `TestAWorkerNodeHoldsAFunctionUntilItsNamespacesMasterArrives` |
| credentials-from-the-namespaces-master | `TestAWorkerNodeDerivesEachNamespacesPairsFromItsOwnMaster`, `TestALakeTokenVerifiesOnlyUnderItsNamespacesMaster`, `TestAProxyTokenNamesItsCallerOnlyUnderItsNamespacesMaster`, `TestAWorkerNodeDerivesTheEnginePasswordFromTheNamespacesMaster` |
| a-namespace-leaves | `TestANodeKeepsTheMastersOfItsNamespacesOnly`, `TestANodeThatStoppedWhileANamespaceLeftDropsItsMasterAtItsStart` |
| secrets-encrypted-on-the-node | `TestAWorkerNodeKeepsItsSecretsEncryptedWithAKeyOfItsOwn` |
| a-sealed-record | `TestChangeLogServesASealedKindDecrypted` |
| a-workload-writes-a-secret | `TestAWorkloadsSecretWriteGoesToTheControlPlane` |

Done when every test above passes, `just check-specs` passes, and `just ci` is green.

## Review checklist

- [ ] `Key` answers only for a namespace placed on the asking node.
- [ ] A node derives a namespace's credentials from that namespace's master alone.
- [ ] A master's file is written whole in mode 0600, and removed when its namespace leaves the node.
- [ ] A node's start removes the master of every namespace whose local Namespace it does not hold, before anything runs.
- [ ] A node's Secrets are encrypted at rest, with its own key when its config names none.
- [ ] A workload's Secret write on a node is made by the control plane, through the same checks as its own.

## Consequences

- A leaked node exposes only the Secrets and the credentials of the namespaces it runs.
- A namespace moves with its credentials unchanged, since its master follows its UID.
- Losing a node's `secrets.key` with its store loses nothing, since the control plane holds every Secret; losing the
  key alone stops the node at its start, as the Secrets it holds no longer decode, and it joins again with a new store.
- A workload on a node writes no Secret while the control plane is away.
- A control plane with no `secrets.encryptionKeyFile` keeps its Secrets and their change-log records in the clear, as
  ADR-0022 allows.
- Risk accepted (#1495): a node keeps its namespaces' masters and its own Secrets key as plain 0600 files under
  `cluster.dataDir`, as funcd keeps `master.key`, so whoever reads that directory holds those namespaces' credentials.
- Risk accepted (#1495): a node names the workload of each Secret write it sends, so a compromised node can write any
  Secret key that a workload of its namespaces may write.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F249; feature issue #1486; tracker #1493; draft PR #1495 (the closed
  finding on decision 8: no credential derived today outlives a start, so a move changes nothing a caller holds).
