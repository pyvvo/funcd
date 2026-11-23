# ADR-0354: The edge takes the client address from a proxy it trusts

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: edge, ingress, config, add-ons
- **Realizes**: [FEAT-0006/F272](../feat/0006-feat-ingress-hardening.md) (trusted proxies)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0112](0112-ingress-protection-limits.md) (its follow-up
  "Trusted-proxy XFF": the client address behind a proxy)
- **Relates to**: ADR-0353 (the caller on the bridge address) · [ADR-0335](0335-the-edge-signs-users-in-with-an-openid-connect-provider.md)
  Decision 9 (`server.edgePublicScheme`, which no header changes) · ADR-0352 (the tunnel engine's egress)

## Context & Need

Behind a tunnel, every request reaches the edge from the tunnel engine, so the rate limit's `clientIP` key (ADR-0112),
the `X-Forwarded-For` the edge sends a worker, and its logs all see one address. Experiment 0027's header run (path C)
showed what each hop does: Bunny sets `X-Forwarded-For` to the visitor alone and overwrites `X-Real-IP` with it,
dropping forged values; frps appends its peer, one of Bunny's edge addresses (594, changing), and rewrites
`X-Forwarded-Proto` to `http`. The decider decided: "Trusting Bunny's hop. This is the only thing we can do": the
visitor is the client, and how funcd knows Bunny's hop is this ADR's.

## Scenarios

- `scenario: tunnel-client-from-header`: Given `server.trustedProxies` with an entry for the add-on kind `Tunnel` and
  the header `X-Real-IP`, and a request from a `Tunnel` engine carrying `X-Real-IP: 198.51.100.7`; then the edge's
  client address is `198.51.100.7`: the rate limit keys on it, and the worker gets `X-Forwarded-For: 198.51.100.7`.
- `scenario: forwarded-for-walk`: Given an entry with `cidrs: [203.0.113.0/24]`, a request from `203.0.113.9` with
  `X-Forwarded-For: 198.51.100.7, 203.0.113.4`; then the client address is `198.51.100.7`.
- `scenario: untrusted-peer-headers-dropped`: Given a request whose peer matches no entry; then the edge drops its
  forwarding headers and the peer is the client, as today.
- `scenario: scheme-never-from-header`: Given a trusted request with `X-Forwarded-Proto: http`; then the edge's public
  scheme stays `server.edgePublicScheme`.
- `scenario: a-function-cannot-forge`: Given a Function worker on the bridge address that sends `X-Real-IP`; then it
  matches no `addonKinds` entry, and its own address is the client.

## Scope

In: one config key, the client address it yields, and where the edge uses it. Out: the scheme and the host, which no
header sets; a list of Bunny's edge ranges, which change; several proxies in a row before a tunnel engine.

## Constraints & Decision drivers

- Empty means today: with no entry, nothing changes (ADR-0112's default).
- A header counts only from a peer that an entry trusts, so a visitor cannot pick its own address.
- Trust names the tunnel's kind, not the bridge range, so a Function on the bridge cannot forge a client.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| An entry trusts CIDRs or an add-on kind; an optional header names the client, else an `X-Forwarded-For` walk | covers Bunny with no address list, and any fixed proxy by its ranges | a new config shape | chosen |
| CIDR ranges only, with Bunny's edge list | standard | 594 changing addresses to refresh | rejected (the decider: Bunny's list changes) |
| Trust every bridge address | simple | any Function could forge its client address | rejected |
| A trusted hop count | standard in proxies | breaks when a hop is added; trusts the wrong entry | rejected |

## Decision

1. **The key.** `server.trustedProxies` (new) is a list. Each entry has `cidrs`, a list of CIDR ranges, or
   `addonKinds`, a list of add-on kinds whose engines reach the edge on the bridge address (ADR-0353), and an optional
   `header`, the name of a header that the trusted hop sets to the client. Unset or empty: no proxy is trusted.
2. **The client address.** The edge starts from the connection's peer. When the peer matches an entry with `header`,
   the client is that header's value, when it is one IP address; else the peer. When the peer matches an entry
   without `header`, the edge walks `X-Forwarded-For` from the right, past every address that matches a `cidrs`
   entry; the first other address is the client, and the leftmost when all match. A peer that matches no entry is the
   client.
3. **Matching.** A `cidrs` entry matches an address in its ranges. An `addonKinds` entry matches a peer that the
   engine index holds as an add-on object of one of those kinds (ADR-0353 Decision 3).
4. **Its uses.** The rate limit's `clientIP` key (ADR-0112), the `X-Forwarded-For` that the edge sends a worker, and
   the edge's logs take the client address. The edge still drops a request's own forwarding headers before the
   worker (`internal/dataplane/dataplane.go:415`).
5. **Never from headers.** The scheme stays `server.edgePublicScheme` (ADR-0335 Decision 9) and the host stays the
   request's own.

## Design model

None. The decision is a pure function of a request and the config, with no lifecycle, timer or writer.

## Behavior rows

- **edge.md**: the client address behind a trusted proxy; forwarding headers from an untrusted peer dropped; the
  scheme never from a header.

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
// internal/edge
type TrustedProxy struct {
	CIDRs      []netip.Prefix
	AddonKinds []v1.Kind
	Header     string
}

// ClientAddr is the request's client address under the trusted proxies (Decision 2); peerKind is the add-on kind
// that the engine index holds for the peer, empty when it holds none.
func ClientAddr(r *http.Request, peer netip.Addr, peerKind v1.Kind, proxies []TrustedProxy) netip.Addr
```

| Item | Consumes / exposes | Notes |
|---|---|---|
| `server.trustedProxies` (`FUNCD_TRUSTED_PROXIES`, YAML only for its entries) | consumes | new; empty is today |
| the engine index | consumes | the peer's add-on kind |
| the rate limit, the worker's `X-Forwarded-For`, the logs | expose the client address | |

## Implementation plan

1. `internal/platform/config`: the key, its doc and validation (a `cidrs` or `addonKinds` entry, a header name).
2. `internal/edge`: `ClientAddr`; the limiter's `clientIP` and the data plane's `X-Forwarded-For` read it.
3. `pkg/funcd`: the peer's add-on kind from the engine index.
4. Tests, each written to pass: `ClientAddr`'s table (every scenario's case, a bad header value, all-trusted chains);
   one `TestScenario…` per scenario in `pkg/funcd`.
5. Docs: edge.md rows, the guide's running-funcd page for the key, `just generate`.
6. Done when `just ci-full` passes.

## Review checklist

- [ ] With no entry, the client address and the headers a worker gets are unchanged.
- [ ] A header counts only from a peer an entry trusts, and an `addonKinds` entry matches only engines of those kinds.
- [ ] The scheme and the host never come from a header.
- [ ] Every scenario has its passing `TestScenario…`.

## Consequences

- Behind the tunnel, rate limits and logs see each visitor, not the tunnel engine.
- An admin who trusts a kind trusts every engine of it to set the header; only an admin registers an Addon (ADR-0332).
- A proxy chain longer than one trusted hop before a tunnel engine is not covered.

## Open questions

None.

## References

- The feature issue: [#1554](https://github.com/pyvvo/funcd/issues/1554).
- The decider's answers of 2026-11-23, in the DR session.
- Experiment 0027's header run (pyvvo/experiment#42, private).
