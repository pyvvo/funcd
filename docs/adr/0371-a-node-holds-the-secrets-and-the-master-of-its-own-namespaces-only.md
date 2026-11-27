# ADR-0371: A node holds the Secrets and the master of its own namespaces only

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, security, secrets
- **Realizes**: [FEAT-0002/F249](../feat/0002-feat-multi-node.md) (Secrets and keys on nodes)
- **Relates to**: [ADR-0365](0365-a-worker-node-follows-its-namespaces-through-the-change-log-over-a-link-it-opens.md)
  (the feed that carries the Secrets) · [ADR-0327](0327-a-function-writes-the-secrets-it-owns.md) (a workload's Secret
  write) · [ADR-0270](0270-derived-s3-key-pairs-rotate.md) (S3 pairs derived from a master) ·
  [ADR-0366](0366-a-worker-node-writes-back-the-status-of-what-it-runs-and-the-objects-it-makes.md) (what a node writes
  back)

## Context & Need

A node's workers read their Secrets and the credentials funcd derives for them (S3 pairs, SQL engine passwords, lake and
add-on tokens) on the node, also while the control plane is away. An all-in-one derives them all from its own master;
a node that held that master could derive every namespace's. The decider chose on 2026-11-16 (D8, #1486) a master per
namespace, derived on the control plane, of which a node holds only its own namespaces', and a node's Secrets
encrypted at rest with a key of its own.

Its purpose: a leaked node exposes only the namespaces it runs, and a namespace keeps its credentials on any node.

## Scenarios

- `scenario: only-its-masters`: Given vm1 runs e1 and not e2, When vm1 asks for e2's master, Then it is refused (403),
  and vm1 keeps e1's alone.
- `scenario: the-master-first`: Given e1 placed on vm1, When vm1 takes its snapshot, Then it takes e1's master before
  e1's objects, and a Function of e1 waits Pending `NamespaceKeyMissing` while the master is missing.
- `scenario: secrets-encrypted-on-the-node`: Given a node whose config names no `secrets.encryptionKeyFile`, When it
  starts, Then it makes `cluster.dataDir/secrets.key` and keeps its Secrets encrypted with it.
- `scenario: a-sealed-record`: Given a Secret written on the control plane, When its change-log record is stored, Then
  it is sealed at rest, and the node's feed serves it decrypted over the node's link.
- `scenario: a-workload-writes-a-secret`: Given a workload of e1 on vm1 that may write Secret s, When it writes a key,
  Then the control plane makes the write as for its own workloads, and the workload reads it back on vm1; while the
  control plane is away, the write is 503.

## Scope

- **In**: the masters per namespace, where a node keeps them, its Secrets key, and a workload's Secret write on a node.
- **Out**: what each credential is and how it rotates (ADR-0270 and the services' ADRs); who may read or write a Secret
  (ADR-0321, ADR-0327); the backup of a node's keys (F251).

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
   the `Key` request, for a namespace placed on it only (403 otherwise), before that namespace's objects in a snapshot,
   and again after each feed answer while one is missing. It keeps it whole in `cluster.dataDir/keys/<ns>.json`, mode
   0600, and removes the file when the namespace leaves it. The node derives the namespace's credentials from it,
   never from its own master; an all-in-one derives every namespace's from its own master, as before. A namespace keeps
   its master on any node, since its UID does not change.
2. **Waiting for it.** A Function whose namespace's master the node lacks waits Pending, reason `NamespaceKeyMissing`;
   a SQL or Lake engine waits by failing its reconcile until the master arrives.
3. **Secrets on a node.** A node holds the Secrets of its namespaces only, which its feed brings (ADR-0365 Decision 4):
   a Secret's change-log record is sealed at rest by the control plane's encryptor and served decrypted over the
   node's mTLS link. The node's store encrypts them with `secrets.encryptionKeyFile`, or, when that is unset, with
   `cluster.dataDir/secrets.key`, 32 random bytes in mode 0600, made at the node's first start.
4. **A workload's Secret write.** It goes to the control plane, which holds the Secret, and is checked and made there
   as a write of its own workloads is (ADR-0327); the node then waits up to `secretEcho` 2 s for its copy to show it,
   so the workload reads its own write. While the control plane is away, the write is 503.

## Design model

None: a master is one value per namespace, taken before the namespace's objects and again after each feed answer while
it is missing, and a Function's wait for it is an existing Pending state; a Secret's write is one request to the control
plane, and its echo through the feed is ADR-0365's.

## Behavior rows

- **multi-node.md**: "A namespace's master" and its rows on a node's Secrets key and a workload's Secret write cite
  this ADR's Decisions at acceptance.

## Temporary workarounds

None.

## Contracts

```text
cluster.dataDir/keys/<ns>.json      # a namespace's master on its node, 0600
cluster.dataDir/secrets.key         # the node's Secrets key when secrets.encryptionKeyFile is unset, 0600
GET /node/v1/keys?namespace=<ns>    # HTTPS; NATS funcd.node.<node>.<serial>.key; 403 for a namespace not placed there
POST /node/v1/secrets               # a workload's Secret write; NATS .secret
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
| node agent | `cluster.dataDir/keys` | `cluster.dataDir/keys/<ns>.json`; `cluster.dataDir/secrets.key` at its first start |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `internal/multinode/keys.go` (`NamespaceKeys`),
`handler.go` (`Key`, `Secret`), `internal/multinode/agent/keys.go` and `secrets.go`, `internal/platform/nsmaster`
(which master a namespace's credentials take), `internal/function/workerenv.go` (`NamespaceKeyMissing`),
`cmd/funcd/cluster.go` (`secretsKey`), and the services that derive credentials (S3 gateway, SQL, Lake, add-ons).

| Scenario | Test |
|---|---|
| only-its-masters | `TestANodeKeepsTheMastersOfItsNamespacesOnly` |
| the-master-first | `TestAWorkerNodeHoldsAFunctionUntilItsNamespacesMasterArrives`, `TestANodeKeepsTheMastersOfItsNamespacesOnly` |
| secrets-encrypted-on-the-node | `TestAWorkerNodeKeepsItsSecretsEncryptedWithAKeyOfItsOwn` |
| a-sealed-record | `TestChangeLogServesASealedKindDecrypted` |
| a-workload-writes-a-secret | `TestAWorkloadsSecretWriteGoesToTheControlPlane` |

Done when every test above passes and `just ci` is green.

## Review checklist

- [ ] `Key` answers only for a namespace placed on the asking node.
- [ ] A node derives a namespace's credentials from that namespace's master alone.
- [ ] A master's file is written whole in mode 0600, and removed when its namespace leaves the node.
- [ ] A node's Secrets are encrypted at rest, with its own key when its config names none.
- [ ] A workload's Secret write on a node is made by the control plane, through the same checks as its own.

## Consequences

- A leaked node exposes only the Secrets and the credentials of the namespaces it runs.
- A namespace moves with its credentials unchanged, since its master follows its UID.
- Losing a node's `secrets.key` with its store loses nothing, since the control plane holds every Secret; losing the
  key alone stops the node at its start, as the Secrets it holds no longer decode, and it joins again with a new store.
- A workload on a node writes no Secret while the control plane is away.
- Risk accepted (#1495): a node keeps its namespaces' masters and its own Secrets key as plain 0600 files under
  `cluster.dataDir`, as funcd keeps `master.key`, so whoever reads that directory holds those namespaces' credentials.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F249; feature issue #1486; tracker #1493; draft PR #1495 (the closed
  finding on decision 8: no credential derived today outlives a start, so a move changes nothing a caller holds).
