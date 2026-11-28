# ADR-0370: An API token is kept as a hash, and each node checks the callers of its namespaces

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, security, auth
- **Realizes**: [FEAT-0002/F248](../feat/0002-feat-multi-node.md) (tokens checked on the node)
- **Supersedes (in part)**: [ADR-0171](0171-static-credential-list.md): its first Temporary workaround and its Scope
  Out items issuance, rotation, revocation without restart and hashing tokens at rest; its Decision 6, a restart
  applies a change, still holds for the config's list
- **Relates to**: [ADR-0365](0365-a-worker-node-follows-its-namespaces-through-the-change-log-over-a-link-it-opens.md)
  (the feed that carries the tokens) · [ADR-0171](0171-static-credential-list.md) (`auth.credentials`) ·
  [ADR-0153](0153-catalog-token-carries-its-owner.md) (a token that names its record) ·
  [ADR-0364](0364-a-worker-node-joins-with-a-one-time-token-and-renews-its-certificate.md) (a join token kept as a hash)

## Context & Need

API tokens live in `auth.credentials` today, so a new token or a revoked one needs a restart, and none expires. With
nodes, each node's API and edge must check the callers of its namespaces itself, also while the control plane is away
(F248). The decider chose on 2026-11-16 (D7, #1485) tokens declared as objects, kept only as hashes with an expiry,
that change with no restart, and that each node gets for the namespaces it runs.

Its purpose: the APIToken kind and `apitoken.Credentials` let an admin mint, rotate and revoke a caller's token, and let
every process check it against its own store.

## Scenarios

- `scenario: mint-and-use`: Given an admin declares APIToken ci, a developer of namespace e1, When the admin runs
  `funcdctl apitoken mint ci`, Then the token is shown once and its calls to e1 pass, on the control plane and on e1's
  node.
- `scenario: mint-again`: Given token ci in use, When the admin mints ci again, Then the old token is 401 and the new
  one passes, with no restart.
- `scenario: expired`: Given a token minted for a minute, When it is used at its end or after, Then it is 401
  `invalid credential`, as every refused credential is.
- `scenario: deleted`: Given token ci, When the admin deletes APIToken ci, Then the token is 401.
- `scenario: only-its-tokens`: Given vm1 runs e1 and vm2 runs e2, When an APIToken spans only e2, Then vm1 does not
  hold it, and a token that stops spanning e1 leaves vm1.
- `scenario: control-plane-away`: Given vm1 holds token ci, When the control plane is stopped, Then vm1 still checks ci
  for e1's callers.
- `scenario: config-first`: Given `auth.credentials` names a token, When it is used, Then the config's entry decides,
  before any stored token.

## Scope

- **In**: the APIToken kind, minting, the check, and which node holds which token.
- **Out**: the roles and what each may do ([ADR-0018](0018-api-server-authn-rbac-admission.md),
  [ADR-0171](0171-static-credential-list.md)); OIDC and Identities; the node's own certificate (ADR-0364).

## Constraints & Decision drivers

- No secret at rest: a token is kept as the hash of a 256-bit secret (D7).
- A change takes no restart, and every token ends (F248).
- A node checks callers while the control plane is away (F248), and holds no token of a namespace it does not run.

## Alternatives considered

- **Tokens in each node's config**: every change is a config push and a restart on every node. Rejected.
- **A node asks the control plane at each call**: a node could not check callers while the control plane is away.
  Rejected.
- **Signed tokens (JWT)** that need no lookup: a revoked one works until it ends, since a node checks only the
  signature. Rejected.
- **A token's secret kept encrypted**: a leaked store would still expose the secrets to whoever holds the key.
  Rejected; a hash needs no key.

## Decision

1. **The kind.** APIToken is cluster-scoped: `spec.role`, admin, developer or viewer, and `spec.namespaces`, none for an
   admin and at least one for the others, as an `auth.credentials` entry says them; its name is a DNS-1123 label. Its
   status, which only the minting writes, holds `tokenHash`, the SHA-256 of the secret, `issuedAt` and `expiresAt`.
2. **Minting.** `funcdctl apitoken mint <name> [--ttl]` (`POST /apis/funcd.io/v1alpha1/apitokens/{name}/token`, an
   admin action) mints `funcd.<name>.<secret>` with a 256-bit secret, shown once. It lives `defaultTTL` 720 h when no
   TTL or 0 is asked, else the TTL asked, from `minTTL` 1 s to `maxTTL` 8760 h; another is 400. A new mint replaces the
   hash, so the token before it ends.
3. **The check.** Every process's API and edge resolve a `funcd.` token by one read of the APIToken its name names,
   compare the hash in constant time and check its end, which refuses it at the instant of `expiresAt`. A caller of an
   unknown name, a wrong secret, a deleted APIToken, one never minted or one past its end gets 401 `invalid
   credential`, as for every refused credential (ADR-0018). `auth.credentials` is checked first, and stays as the first
   admin's credential.
4. **On a node.** A node follows the APITokens that concern it (ADR-0365 Decision 4): an admin's, and each one whose
   namespaces include one placed on the node. One that stops concerning it reaches it as a bare delete of its name, and
   a node drops the tokens that span none of its namespaces when a namespace leaves it. The node checks its own
   namespaces' callers against its own store, also while the control plane is away.

## Design model

None: the decision adds no state, timer or actor. A token's end is a value compared at each read, and nothing fires
when it passes; a mint is one conditional write of the APIToken's status, the last one winning, with no use-up race as
a join token has (ADR-0364's model). A token reaches a node through the feed, reused unchanged
([`feed.fizz`](../design/multinode/feed.fizz), ADR-0365).

## Behavior rows

- **security.md**: "An API token kept as a hash" and "An API token on a worker node" cite this ADR's Decisions at
  acceptance; both now show 401 `invalid credential`, and the second says a node follows the tokens that concern it.

## Temporary workarounds

None.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: APIToken
metadata:
  name: ci
spec:
  role: developer
  namespaces:
    - e1
status:
  tokenHash: <sha-256 of the secret, hex>
  issuedAt: 2026-11-27T18:00:00.000Z
  expiresAt: 2026-12-27T18:00:00.000Z
```

```text
funcdctl apitoken mint <name> [--ttl <duration>]          # prints the token alone
POST /apis/funcd.io/v1alpha1/apitokens/{name}/token       # admin; {"ttl": "720h"} -> {"token", "expiresAt"}
Authorization: Bearer funcd.<name>.<secret>
```

```go
// internal/auth/apitoken
func NewMinter(st store.Store, now func() time.Time) *Minter
func (m *Minter) MintAPIToken(ctx context.Context, name v1.ObjectName, ttl time.Duration) (string, time.Time, error)
func NewCredentials(st store.Store, now func() time.Time) Credentials // a middleware.CredentialStore
func (c Credentials) Lookup(ctx context.Context, token string) (auth.Identity, error)
```

| Part | Reads | Writes |
|---|---|---|
| control plane | the APIToken at a mint | its status: `tokenHash`, `issuedAt`, `expiresAt` |
| every API and edge | `auth.credentials`, then the APIToken a token names, in its own store | nothing |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `api/types/v1alpha1/apitoken.go` (the kind),
`internal/auth/apitoken/apitoken.go` (`Minter`, `Credentials`), `internal/controlplane/apitoken.go` (the route),
`cmd/funcdctl/apitoken.go`, `internal/multinode/handler.go` (`concerns`, `forNode`) and
`internal/multinode/agent/agent.go` (`dropTokens`).

| Scenario | Test |
|---|---|
| mint-and-use | `TestAnAPITokenKeptAsAHashWorksUntilItIsMintedAgainOrDeleted`, `TestANodeChecksTheAPITokensOfTheControlPlaneItself` |
| mint-again | `TestAnAPITokenWorksUntilItExpiresOrIsMintedAgain`, `TestAnAPITokenKeptAsAHashWorksUntilItIsMintedAgainOrDeleted` |
| expired | `TestAnAPITokenWorksUntilItExpiresOrIsMintedAgain` |
| deleted | `TestAnAPITokenKeptAsAHashWorksUntilItIsMintedAgainOrDeleted` |
| only-its-tokens | `TestANodeGetsTheAPITokensAndTheNamespacesThatConcernIt` |
| control-plane-away | `TestANodeChecksATokenWhileItsControlPlaneIsAway` |
| config-first | `TestTheConfigsCredentialsComeBeforeTheStoredTokens`, `TestTheConfigsCredentialsComeFirst` |

Done when every test above passes and `just ci` is green.

## Review checklist

- [ ] The store holds a token's hash and end only; the secret is shown once and never logged.
- [ ] The hash is compared in constant time, and an expired token is refused at the instant of `expiresAt`.
- [ ] A node's feed and snapshot carry an APIToken only to a node it concerns, and a bare delete to one it stopped
      concerning.
- [ ] `auth.credentials` is checked before the stored tokens.

## Consequences

- Tokens rotate and end with no restart, and a leaked store exposes no usable token.
- A node checks its callers while the control plane is away; a token minted or deleted meanwhile reaches the node when
  it follows again, so until then the node goes by the hash it holds.
- Every node holds the hashes of every admin token, which span every namespace, and the name of every APIToken, since
  one that does not concern it comes as a bare delete of its name (ADR-0365 Decision 4).
- A node judges a token's end by its own clock.
- A metastore restored from a backup taken before a revocation or a new mint brings the old hash back, until its end;
  the hold after a restore applies first (ADR-0206).
- This closes ADR-0171's first workaround: tokens are issued, rotated and revoked with no restart, and kept as hashes.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F248; feature issue #1485; tracker #1493; draft PR #1495.
