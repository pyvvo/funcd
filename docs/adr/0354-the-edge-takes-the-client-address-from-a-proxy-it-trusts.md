# ADR-0354: The edge takes the client address from a proxy it trusts

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: edge, ingress, config, add-ons
- **Realizes**: [FEAT-0006/F272](../feat/0006-feat-ingress-hardening.md) (trusted proxies)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0164](0164-rate-limit-per-target.md) Decision 6 and its
  Contracts comment on `KeyClientIP` ("the RemoteAddr host; no forwarding header is read": the client address of
  Decision 2); [ADR-0171](0171-static-credential-list.md) Decision 5's last sentence ("An `open` route forwards every
  header": not the caller's own forwarding headers, Decision 4)
- **Relates to**: [ADR-0112](0112-ingress-protection-limits.md) (this ADR answers its open question "Trusted-proxy
  XFF") · ADR-0353 (the caller on the bridge address) · ADR-0352 (`egress.Sources`; the tunnel engine's egress) ·
  [ADR-0335](0335-the-edge-signs-users-in-with-an-openid-connect-provider.md) Decision 9 (`server.edgePublicScheme`,
  which no header changes)

## Context & Need

Behind a tunnel, every request reaches the edge from the tunnel engine, so the rate limit's `clientIP` key (ADR-0164
Decision 6), the `X-Forwarded-For` a backend gets and the edge's logs all see one address. Experiment 0027's header run
(path C) showed what each hop does: Bunny sets `X-Forwarded-For` to the visitor alone and overwrites `X-Real-IP` with
it, dropping forged values; frps appends its peer, one of Bunny's edge addresses (594, changing), and rewrites
`X-Forwarded-Proto` to `http`. The decider decided: "Trusting Bunny's hop. This is the only thing we can do": the
visitor is the client, and how funcd knows Bunny's hop is this ADR's.

## Scenarios

- `scenario: tunnel-client-from-header`: Given `server.trustedProxies` with an entry for the add-on kind `Tunnel` and
  the header `X-Real-IP`, and a request from a `Tunnel` engine on the bridge address carrying `X-Real-IP:
  198.51.100.7`; then the client address is `198.51.100.7`: the rate limit keys on it, and a Function worker, like an
  add-on engine served at its host, gets `X-Forwarded-For: 198.51.100.7` alone.
- `scenario: forwarded-for-walk`: Given an entry whose `cidrs` holds `203.0.113.0/24`, and a request from
  `203.0.113.9` with `X-Forwarded-For: 198.51.100.7, 203.0.113.4`; then the client address is `198.51.100.7`.
- `scenario: untrusted-peer-headers-dropped`: Given a request whose peer matches no entry, with `X-Forwarded-For:
  198.51.100.7` and `X-Real-IP: 198.51.100.8`; then the peer is the client, as today, and the worker gets
  `X-Forwarded-For` holding the peer alone and no `X-Real-IP`, where today it gets both, the peer appended.
- `scenario: scheme-never-from-header`: Given a trusted request with `X-Forwarded-Proto: http`; then the edge's public
  scheme stays `server.edgePublicScheme`.
- `scenario: a-function-cannot-forge`: Given an entry for the kind `Tunnel` and one whose `cidrs` holds `10.0.0.0/8`,
  which holds the worker subnet, and a Function worker on the bridge address that sends `X-Real-IP` and
  `X-Forwarded-For`; then neither entry matches it, and its own address is the client.

## Scope

In: one config key, the client address it yields, and the uses and headers that carry it. Out: the scheme and the
host, which no header sets; the scheme a worker reads (`X-Forwarded-Proto` reaches it as the caller sent it, as
today); a list of Bunny's edge ranges, which change; several proxies in a row before a tunnel engine.

## Constraints & Decision drivers

- Empty trusts no proxy: with no entry, the client address is the peer, as today (ADR-0164 Decision 6).
- A header counts only from a peer that an entry trusts, so a visitor cannot pick its own address.
- Trust names the tunnel's kind, not the bridge range, so a workload on the bridge cannot forge a client.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| An entry trusts CIDRs or an add-on kind; an optional header names the client, else an `X-Forwarded-For` walk | covers Bunny with no address list, and any fixed proxy by its ranges | a new config shape | chosen |
| CIDR ranges only, with Bunny's edge list | standard | 594 changing addresses to refresh | rejected (the decider: Bunny's list changes) |
| Trust every bridge address | simple | any Function could forge its client address | rejected |
| A trusted hop count | standard in proxies | breaks when a hop is added; trusts the wrong entry | rejected |

## Decision

1. **The key.** `server.trustedProxies` (new) is a list, set in the config file only, as `auth.credentials` is
   (ADR-0171 Decision 1). Each entry has either `cidrs`, a list of CIDR ranges, or `addonKinds`, a list of add-on kinds
   whose engines reach the edge on the bridge address (ADR-0353), and an optional `header`, the name of a header that
   the trusted hop sets to the client. Unset or empty: no proxy is trusted. funcd refuses to start on an entry with
   both lists or neither, a range that does not parse, or a header name that is not a token.
2. **The client address.** The edge starts from the connection's peer, and the first entry, in list order, that
   matches the peer decides. With `header`, the client is that header's value when it is exactly one IP address, else
   the peer. Without, the edge walks `X-Forwarded-For` from the right past each address that a `cidrs` entry matches:
   the first other address is the client, the leftmost when all match, and the peer when an element is not one IP
   address. A peer that matches no entry is the client.
3. **Matching.** A `cidrs` entry matches an address in its ranges, but never one in `runtime.containerd.subnetCIDR`:
   that is a workload's address, which only an `addonKinds` entry can trust. An `addonKinds` entry matches a peer that
   `egress.Sources` (ADR-0352 Decision 3) names as
   an add-on object whose stored object's kind is one of the entry's, spelled the same. Where no engine index exists
   (the process runtime, or containerd without the egress gateway), an `addonKinds` entry matches nothing, and funcd
   warns at start.
4. **Its uses.** The rate limit's `clientIP` key, the edge's logs and the backends' `X-Forwarded-For` take the client
   address. A Function worker that a request through the listener reaches gets `X-Forwarded-For` holding the client
   address alone, and none of the caller's `X-Forwarded-For`, `Forwarded` or `X-Real-IP`; a fn-to-fn call is
   unchanged. An upstream entry (an add-on engine served at its host, a Lake), whose forwarding headers the edge drops
   already (`serveUpstream`, `internal/dataplane/dataplane.go`), gets `X-Forwarded-For` holding the client address in
   place of the peer. With no entry the client is the peer, so a worker's change is the caller's headers dropped.
   `X-Forwarded-Proto` and `X-Forwarded-Host` reach a worker as the caller sent them, as today; funcd reads neither.
5. **Never from headers.** The edge's scheme stays `server.edgePublicScheme` (ADR-0335 Decision 9) and the host stays
   the request's own.

## Design model

None. The client address is a function of the request, the config and what `egress.Sources` names for the peer, read
at each request; the decision adds no writer, lifecycle or timer. The indexes that name a peer follow the workers within one
worker sync (ADR-0163).

## Behavior rows

- **calls.md**: the client address behind a trusted proxy, for the rate key and the logs; the `X-Forwarded-For` a
  worker and an upstream entry get, and the caller's own forwarding headers dropped; the scheme never from a header;
  an `addonKinds` entry where no engine index exists.
- **security.md**: the row "A call allowed on an `authenticated` route" (an `open` route forwards every header) leaves
  out the caller's `X-Forwarded-For`, `Forwarded` and `X-Real-IP`.

## Temporary workarounds

None.

## Contracts

```yaml
server:
  trustedProxies:
    - addonKinds:
        - Tunnel
      header: X-Real-IP
    - cidrs:
        - 203.0.113.0/24
```

```go
// internal/platform/config: Server gains TrustedProxies, file-only (env:"-"), as Auth.Credentials is.

// internal/edge (new)
type TrustedProxy struct {
	CIDRs      []netip.Prefix
	AddonKinds []string
	Header     string
}

// Proxies decides a request's client address (Decisions 2 and 3); Workers is runtime.containerd.subnetCIDR, the zero
// Prefix on the process runtime.
type Proxies struct {
	Entries []TrustedProxy
	Workers netip.Prefix
}

// Client is r's client address; peerKind is the kind of the stored add-on object that egress.Sources names for peer,
// "" when it names none.
func (p Proxies) Client(r *http.Request, peer netip.Addr, peerKind string) netip.Addr
```

| Item | Consumes / exposes | Notes |
|---|---|---|
| `server.trustedProxies` | consumes | new, file-only; empty trusts no proxy |
| `egress.Sources` and the stored add-on objects | consumes | the peer's add-on kind |
| the rate limit, the logs, a worker's and an upstream's `X-Forwarded-For` | expose the client address | |

## Implementation plan

1. `internal/platform/config`: the key, its doc and validation.
2. `internal/edge`: `Proxies`; the limiter's `clientIP` reads it; the data plane drops the caller's forwarding headers
   before the activator, and sets `X-Forwarded-For` for a worker and an upstream entry, to which the activator's
   proxy appends nothing.
3. `pkg/funcd` and `cmd/funcd`: the peer's kind through `egress.Sources` and the store; the warning at start.
4. Tests, each written to pass: `Proxies.Client`'s table (every scenario's case, the first matching entry, a bad
   header value, an all-trusted chain, a worker-subnet peer under a wide range); one `TestScenario…` per scenario in
   `pkg/funcd`.
5. Docs: the calls.md and security.md rows, the guide's running-funcd page for the key, `just generate`.
6. Done when `just ci-full` passes.

## Review checklist

- [ ] With no entry, the client address is the peer, and a worker gets none of the caller's own `X-Forwarded-For`,
  `Forwarded` or `X-Real-IP`.
- [ ] A header counts only from a peer that the first matching entry trusts; a `cidrs` entry never matches the worker
  subnet, and an `addonKinds` entry matches only engines of those kinds.
- [ ] The scheme and the host never come from a header.
- [ ] Every scenario has its passing `TestScenario…`.

## Consequences

- Behind the tunnel, rate limits and logs see each visitor, and a Function or an add-on engine served at its host gets
  the visitor's address in `X-Forwarded-For`.
- An HTTP Function that reads a caller's own `X-Forwarded-For`, `X-Real-IP` or `Forwarded` today gets the edge's
  `X-Forwarded-For` alone.
- An admin who trusts a kind trusts every engine of it to set the header, and every hop before the engine: a tunnel
  server that takes connections from anywhere but its CDN lets a visitor set `X-Real-IP`, and so its own rate key
  (risk accepted; routed to the tunnel add-on in pyvvo/funcd-addons, whose README should say to take only the CDN's).
- On the process runtime, workers share the host's addresses, so a `cidrs` entry that holds them trusts them too.
- A proxy chain longer than one trusted hop before a tunnel engine is not covered.

## Open questions

None.

## References

- The feature issue: [#1554](https://github.com/pyvvo/funcd/issues/1554).
- The decider's answers of 2026-11-23, in the DR session.
- Experiment 0027's header run (pyvvo/experiment#42, private).
