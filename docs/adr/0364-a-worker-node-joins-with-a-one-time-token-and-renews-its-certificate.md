# ADR-0364: A worker node joins with a one-time token, then holds a certificate it renews

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, security, identity
- **Realizes**: [FEAT-0002/F242](../feat/0002-feat-multi-node.md) (join with a one-time token)
- **Relates to**: [ADR-0363](0363-one-funcd-binary-runs-as-the-all-in-one-the-control-plane-alone-or-a-worker-node.md)
  (the roles) · [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model)

## Context & Need

A worker node (ADR-0363) must prove to its control plane which node it is before it gets the objects, Secrets and keys
of the namespaces placed on it, and it must reach the real control plane before it hands over anything secret. The
decider chose on 2026-11-16 a one-time join token bound to a node name and carrying the hash of the control plane's CA,
as k3s formats its tokens, swapped for a node certificate, then mTLS, with no reusable token, so automation can join
nodes with no human step.

Its purpose: an admin, or automation an admin set up, mints a token for one declared node; `funcd node join` turns it
into an identity that the node keeps and renews for as long as it reaches its control plane.

## Scenarios

- `scenario: join-with-a-token`: Given a declared WorkerNode vm1 and a token minted for it, When `funcd node join` runs
  with it, Then the node gets a certificate for vm1, keeps it under `cluster.dataDir`, and its requests are admitted.
- `scenario: token-works-once`: Given a token that joined, When a second machine joins with it, Then it is refused.
- `scenario: token-past-its-end`: Given a token minted for a minute, When a machine joins with it after two, Then it
  is refused.
- `scenario: new-token-ends-old`: Given an unused token, When the admin mints another, Then the first is refused and
  the second joins.
- `scenario: impostor-control-plane`: Given a token minted by one control plane, When the machine reaches a server
  with another CA, Then it sends no token.
- `scenario: name-held-by-polling-node`: Given a node vm1 that polls, When another machine joins as vm1, Then it is
  refused; once vm1 has stopped polling for `defaultReadyFor`, the join passes.
- `scenario: renewal-takes-over`: Given a node in the last third of its certificate's life, When it renews, Then its
  requests with the old certificate pass until its first request with the new one, which ends the old one.
- `scenario: lost-renew-answer`: Given a renewal whose answer never reached the node, When the node renews again, Then
  its certificate passes until then, and the new renewal replaces the lost one.
- `scenario: rejoin-ends-the-old-identity`: Given node vm1 with a renewed certificate waiting, When vm1 joins again with
  a new token, Then its old certificate and the waiting one are refused, and the new one passes.
- `scenario: certificate-past-its-end`: Given a node certificate past its end, When the node connects, or sends a
  request on a link it opened before the end, Then it is refused, and the node must join again.

## Scope

- **In**: the join token, the join, the node certificate, its renewal, the CA, and the node API's own certificate.
- **Out**: the transport that carries the join and the requests (F243); the version checks at the join (F252); a node
  that provisions itself (F253); the backup of the CA (F251); the rotation of the CA.

## Constraints & Decision drivers

- No reusable token, and no secret sent to a server before it proves it is the control plane (decider, 2026-11-16).
- Hands-free joins for automation (F253 later): no human approval step.
- A leaked node identity must end with the node: a new join or a deleted WorkerNode ends it.
- Standard Go TLS only; no new dependency.

## Alternatives considered

- **A reusable bootstrap token**, as k3s's server token: one leak joins any machine under any name, for as long as it
  lives. Rejected by the decider's choice.
- **An admin approves each certificate request**, as kubelet's TLS bootstrap can: a human step that automation cannot
  take. Rejected.
- **A shared cluster secret**: no identity per node, so no node can be refused alone. Rejected.
- **A certificate with no renewal**: every node must join again after a year. Rejected: renewal costs one request.
- **SPIFFE or ACME**: an external issuer for a first version that needs none (the blueprint keeps SPIFFE naming for
  later). Rejected for now.

## Decision

1. **The token.** `funcdctl node token <name> [--ttl]` (`POST /apis/funcd.io/v1alpha1/workernodes/{name}/token`, an
   admin action) mints `K10<CA hash>::<node>:<secret>` for a declared WorkerNode; an undeclared one is 404. It lives
   `defaultTokenTTL` 1 h, or the TTL asked, from `minTokenTTL` 1 s up to `maxTokenTTL` 24 h; another is 400. The
   control plane keeps only the secret's SHA-256 and the token's end, in `status.pendingJoin`, and a new token replaces
   one not used yet.
2. **The join.** `funcd node join --server <url> --token <token>` checks that the server's chain holds a CA with the
   token's hash before it sends the token, then sends it with a certificate request for the token's name. The control
   plane refuses a token of another CA (403), a name whose node polls (409 `a running node holds the name`, until it has
   not polled for `defaultReadyFor`), and a node on a newer release (F252). Then one conditional write of the
   WorkerNode's status checks that the token waits, is not past its end and matches, signs the request, records the
   certificate in `status.certificate`, and clears `status.pendingJoin` and `status.renewedCertificate`. A refusal
   there is 403, and so is a name no WorkerNode declares. Under `cluster.dataDir`, the node then removes its name,
   writes its certificate and key, the CA and the server's URL, each file whole, and writes its name last, so a join
   that a crash cut short reads as no join.
3. **The certificate.** The cluster CA, made under `cluster.dataDir` at the control plane's first start and valid
   `caLifetime` 87600 h, signs a node certificate with the node's name as its common name, valid `certLifetime` 8760 h
   from `certBackdate` 1 h before its signing. Every later request is mTLS with it. The control plane admits the serial
   of `status.certificate` or of `status.renewedCertificate` only, and only before that certificate's end: TLS checks
   the end at a link's handshake, and admission checks it again at every request, since a driver's link outlives its
   handshake. Any other request is 401 `join again`. So a new join, which clears both, or a deleted WorkerNode ends a
   node's old certificates.
4. **Renewal.** Every `renewEvery` 1 h the node looks whether its certificate is in the last third of its life. If so,
   it sends a new certificate request; the control plane signs it and keeps it in `status.renewedCertificate`, in place
   of one waiting there. The node writes it to disk, switches to it, and its first request with it makes it
   `status.certificate`, which ends the old one. A renewal whose answer is lost leaves the node on its certificate, and
   its next look renews again. A node whose certificate ended is refused, at the handshake or at its next request, and
   joins again with a new token. The node API's own server certificate is signed again before it ends too.

## Design model

- **Spec**: [`docs/design/multinode/join.fizz`](../design/multinode/join.fizz) · state machine
  [`join.md`](../design/multinode/join.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `ATokenJoinsOnce` | always | `scenario: token-works-once` | `TestANodeJoinsFollowsItsNamespaceAndReportsBack` |
  | `NoJoinWithATokenPastItsEnd` | always | `scenario: token-past-its-end` | `TestAJoinTokenPastItsEndIsRefused` |
  | `NoJoinWithAReplacedToken` | always | `scenario: new-token-ends-old` | `TestANewJoinTokenEndsTheUnusedOne` |
  | `NoJoinOverAPollingNode` | always | `scenario: name-held-by-polling-node` | `TestAJoinUnderTheNameOfAPollingNodeIsRefused` |
  | `NoRequestRefusedMidRenewal` | always | `scenario: renewal-takes-over` | `TestARenewedCertificateTakesOverAtItsFirstRequest` |
  | `TheOldCertificateEndsAtTheNewOnesFirstRequest` | always | `scenario: renewal-takes-over` | `TestARenewedCertificateTakesOverAtItsFirstRequest`, `TestANodeRenewsItsCertificateBeforeItEnds` |
  | `NoRequestWithACertificatePastItsEnd` | always | `scenario: certificate-past-its-end` | `TestANodeCertificatePastItsEndIsRefused`, `TestANodeCertificatePastItsEndIsRefusedOnAnOpenLink` |
  | `NoRequestWithACertificateAJoinReplaced` | always | `scenario: rejoin-ends-the-old-identity` | `TestARejoinEndsTheOldCertificates` |

- **Bounds**: two machines, two mints, a clock of four ticks (a token lives two, a certificate three, renewed from its
  second), one action at a time, 12 actions: enough for a copied token, a renewal, a rejoin and a request in flight. A
  lost renew answer is not modeled: its certificate reaches no machine, so nothing presents it, and the model renews
  only when no renewed certificate waits.
- **Faults**: none; every step is atomic, since a join's checks and its write are one conditional write.
- **Environment**: the status write is conditional on the version read (`writeNodeStatus` retries a conflict);
  liveness reads Ready while the node polled in its window; admission refuses a certificate past its end at every
  request, as TLS does at a handshake. All three are modeled as guards.
- **Mutants**: `no-use-up`, `no-end-check`, `keep-old-tokens`, `no-ready-check`, `renew-replaces`, `keep-old-cert`,
  `no-cert-end` and `join-keeps-renewed`, each failing the assertion it names.
- **Result**: pass, 8,714 states, 5.1 s, FizzBee v0.5.4; wider (14 actions): pass, 9,302 states, 6.6 s.
- **Left to tests**: the liveness check reads before the conditional write, so a node that starts polling again
  between the two is cut off by the new certificate, which `TestAJoinUnderTheNameOfAPollingNodeIsRefused` and the
  admission rule hold; the agent's hourly look and its write to disk before the switch are
  `TestANodeRenewsItsCertificateBeforeItEnds`'s; a renewal that replaces a waiting one is
  `TestALostRenewAnswerCostsNothing`'s.

## Behavior rows

- **multi-node.md**: "A join token" (now from a second), "Joining", "A name a running node holds" (now with
  `TestAJoinUnderTheNameOfAPollingNodeIsRefused`) and "A certificate near its end" (now also refused on an open link,
  with `TestANodeCertificatePastItsEndIsRefusedOnAnOpenLink`) cite this ADR's Decisions 1 to 4 at acceptance.

## Temporary workarounds

None.

## Contracts

```text
funcdctl node token <name> [--ttl <duration>]          # prints the token alone
funcd node join --server <url> --token <token>          # else $FUNCD_JOIN_TOKEN
POST /apis/funcd.io/v1alpha1/workernodes/{name}/token   # admin; {"ttl": "1h"} -> {"token": "...", "expiresAt": "..."}
```

```yaml
# WorkerNode status fields this ADR writes
status:
  pendingJoin:
    tokenHash: <sha-256 of the secret>
    expiresAt: 2026-11-27T19:00:00.000Z
  certificate:
    serial: <hex>
    notAfter: 2027-11-27T18:00:00.000Z
    issuedAt: 2026-11-27T18:00:00.000Z
  renewedCertificate:
    serial: <hex>
    notAfter: 2027-11-27T18:00:00.000Z
    issuedAt: 2026-11-27T18:00:00.000Z
```

```go
// internal/nodeapi: the join and the renewal, which a driver carries (F243)
type JoinRequest struct {
	Token    string `json:"token"`
	CSR      []byte `json:"csr"` // PEM certificate request, CN = the token's node
	Version  string `json:"version,omitempty"`
	Platform string `json:"platform,omitempty"`
}

type JoinResponse struct {
	Certificate []byte `json:"certificate"` // PEM node certificate
	CA          []byte `json:"ca"`          // PEM cluster CA
}

type RenewRequest struct {
	CSR []byte `json:"csr"`
}
```

| Part | Reads | Writes |
|---|---|---|
| control plane | `cluster.dataDir` (the CA), `cluster.hosts` | `WorkerNode.status.pendingJoin`, `.certificate`, `.renewedCertificate` |
| `funcd node join` | `--server`, `--token` | `cluster.dataDir`: `node.pem`, `ca.crt`, `server`, `name` |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `internal/multinode/token.go` (the token), `cluster.go`
(`MintJoinToken`), `handler.go` (`Join`, `Admit`, `Renew`), `ca.go` (the CA, `SignNode`, `ServerTLS`),
`internal/multinode/agent/join.go` and `renew.go`, `internal/controlplane/jointoken.go` (the route),
`cmd/funcdctl/node.go` and `cmd/funcd/cluster.go` (the commands).

| Scenario | Test |
|---|---|
| join-with-a-token | `TestANodeJoinsFollowsItsNamespaceAndReportsBack`, `TestAJoinTokenNeedsADeclaredNodeAndABoundedLife` |
| token-works-once | `TestANodeJoinsFollowsItsNamespaceAndReportsBack` |
| token-past-its-end | `TestAJoinTokenPastItsEndIsRefused` |
| new-token-ends-old | `TestANewJoinTokenEndsTheUnusedOne` |
| impostor-control-plane | `TestAJoinRefusesAControlPlaneWithAnotherCA`, the contract's `a-join-sends-its-token-only-to-the-ca-it-trusts` |
| name-held-by-polling-node | `TestAJoinUnderTheNameOfAPollingNodeIsRefused` |
| renewal-takes-over | `TestARenewedCertificateTakesOverAtItsFirstRequest`, `TestANodeRenewsItsCertificateBeforeItEnds`, `TestTheNodeAPIsCertificateIsRenewedBeforeItEnds` |
| lost-renew-answer | `TestALostRenewAnswerCostsNothing` |
| rejoin-ends-the-old-identity | `TestARejoinEndsTheOldCertificates` |
| certificate-past-its-end | `TestANodeCertificatePastItsEndIsRefused`, `TestANodeCertificatePastItsEndIsRefusedOnAnOpenLink` |

Done when every test above passes, `just check-specs` passes the spec and its mutants, and `just ci` is green.

## Review checklist

- [ ] The control plane keeps a token's hash and end only, a new token replaces an unused one, and a TTL under
      `minTokenTTL` or over `maxTokenTTL` is 400.
- [ ] The node checks the CA's hash before it sends the token, on every driver.
- [ ] A join's token checks, its signing and its certificate record are one conditional write that uses the token up
      and clears `status.renewedCertificate`.
- [ ] The node removes its name file before it writes its identity, each file whole, and writes the name last.
- [ ] A join under a name whose node polls is 409, and passes once the node has not polled for `defaultReadyFor`.
- [ ] Admission takes the serial of `status.certificate` or `status.renewedCertificate` only, refuses it past its
      `notAfter` at every request, and the first request with the renewed one promotes it.
- [ ] The node writes a renewed certificate to disk before it switches to it.
- [ ] Each oracle test carries its `oracle:` line, and the spec passes with its mutants failing.

## Consequences

- A node joins with one command and a token that works once, and keeps its identity with no further step while it
  reaches its control plane.
- A leaked token is spent at its first join, or ends with its hour; a leaked certificate ends at the node's next join,
  with its WorkerNode, or at its end, on an open link too.
- A join whose answer is lost spends its token: the admin, or the automation, mints another.
- A node that stays away past its certificate's year joins again, with a new token.
- Losing the CA under `cluster.dataDir` changes its hash, so every node joins again; its backup is the F251 ADR's.
- Risk accepted: a certificate is trusted until it ends, a new join or a deleted WorkerNode, with no revocation list.
- The 409 reads liveness the control plane keeps in memory; after its restart, a node whose WorkerNode read Ready keeps
  its name for `defaultReadyFor` while it polls again (ADR-0367 Decision 3).
- Risk accepted: the CA lives `caLifetime` 10 years, and nothing rotates it yet.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F242; feature issue #1479; tracker #1493; draft PR #1495.
- The k3s token format, the model for `K10<CA hash>::<node>:<secret>`.
