# ADR-0315: What a domain rule allows, and the forwarder that enforces it (amends ADR-0117 and ADR-0115)

- **Status**: Reviewing (2026-11-05, implementation done); Accepted (2026-11-05): the decider delegated acceptance to the session driving this work, which settled the
  last choices; judged once, its Blocker and Major folded
- **Date**: 2026-11-05
- **Deciders**: green-0-rabbit
- **Tags**: egress, network, dns, security
- **Realizes**: [FEAT-0007/F229](../feat/0007-feat-egress-control.md) (what a domain rule allows)
- **Supersedes (in part)**: [ADR-0117](0117-egress-policy-enforcement.md) — §1, the domains a destination carries; §2, "It
  forwards each query to the node resolver" and what it records; in *Contracts*, `WildcardPatterns`, `NewForwarder` ·
  [ADR-0115](0115-worker-network-isolation.md) — the DNS accept while F80 is alone: in Decision B the nat chain's "and *not*
  the DNS resolver" and the forward accept (2), its *Temporary workarounds* entry and scenario `dns-resolves`
- **Relates to**: [ADR-0175](0175-engine-is-its-own-principal.md) (the owner kinds a rule covers) ·
  [ADR-0238](0238-container-bridge-address.md) (the bridge address, a bind retried until it exists) ·
  [ADR-0269](0269-a-provider-engine-reaches-only-its-one-internal-endpoint.md) (the engines' fence) ·
  [ADR-0281](0281-ai-gateways.md) (an engine's generated permit, not built)

## Context & Need

The gateway reads the EgressPolicy for every connection. ADR-0117 §2 has the forwarder forward each query to the node
resolver and read the policy only to add a wildcard token, and §1 lets a domain rule fire at whatever address the answer
gave. No ADR says where the forwarder listens. FEAT-0007 asks of the whole outbound edge that an allow-list permits
"exactly" its domains "and nothing else", and that a workload with none is "provably empty". ADR-0115's Scope says
default-deny also covers worker→host and worker→LAN.

**Purpose.** With `server.network.egress` on, the forwarder answers known workers only and forwards only the names an
EgressPolicy domain rule allows them, and a domain rule reaches public addresses only. Callers: each lookup a worker makes on
`:53` and each connection the gateway decides.

## Scenarios

- `scenario: lookup-allowed-name`: Given the rules `api.x.example` and `*.y.example` for a Function's worker, When it looks
  up `api.x.example` or `a.b.y.example`, Then the upstream answers, over UDP and TCP; the bare `y.example` gets `REFUSED`.
- `scenario: lookup-case-and-trailing-dot`: When it looks up `API.X.Example.`, Then the upstream answers, and the rule fires.
- `scenario: lookup-refusals`: Given those rules, When it looks up `other.example`, or its namespace has no EgressPolicy, or its
  source is not in the worker index, or the EgressPolicy list fails, Then `REFUSED`, and the upstream receives no query.
- `scenario: lookup-follows-who-a-rule-covers`: Given a whole-namespace policy and one with `appliesTo` naming only Function
  `etl`, Then a worker of `etl` is answered for both names, a worker of `web` only for the whole-namespace one, and a Lake's
  engine for neither, as the PDP gives it no grant from them.
- `scenario: lookup-not-a-query`: Given a `NOTIFY` for an allowed name, Then `REFUSED`; a message the DNS server itself turns
  down, an `UPDATE` or one with two questions, gets its `NOTIMP` or `FORMERR`. No such message goes upstream.
- `scenario: lookup-name-outside-host-characters`: Given the rule `*.y.example`, When a name holds more than letters, digits,
  `-`, `_` and the dots between labels, such as the first label `a.y` under `example`, read as `a\.y.example`, Then `REFUSED`.
- `scenario: forwarder-and-pdp-agree`: Given a table of policies and principals (no policy, whole namespace, `appliesTo` in and
  out, both, exact and wildcard names with a CIDR, any port, a Lake of the same name, another namespace), Then the forwarder's
  list holds a domain exactly when the real compiler and PDP permit `egress::connect` for that principal with that domain attested.
- `scenario: rule-fires-through-a-cname`: Given the rule `api.x.example` on 443, When its lookup answers through a CNAME to
  `edge.cdn.example` at a public address, Then a connection to that address on 443 is allowed.
- `scenario: answer-names-not-recorded`: Given the rules `a.x.example` and `b.y.example`, When the lookup of `a.x.example`
  answers with an A record named `b.y.example`, Then the worker holds `a.x.example` for that address and not `b.y.example`.
- `scenario: cidr-rule-name-refused`: Given only the rule `1.1.1.1/32` on 443, When a worker looks up a name, Then `REFUSED`;
  a connection to `1.1.1.1:443` by address is allowed.
- `scenario: domain-rule-public-only`: Given a domain rule whose lookup answers with a restricted address (Decision 1), IPv4
  or IPv6, When the worker connects to it, Then refused and audited; at a public address allowed, and the rule's port holds.
- `scenario: cidr-rule-names-the-address`: Given a rule with the domain `db.internal.example` and the CIDR `10.0.0.5/32` on
  5432, When the worker looks up the name and connects to `10.0.0.5:5432`, Then allowed; on another port, refused.
- `scenario: forwarder-on-the-bridge`: Given egress on under containerd, When a worker looks up a name on `:53`, Then the
  forwarder bound to the bridge address serves it, and no listener on its port is bound to all addresses.
- `scenario: forwarder-waits-for-the-bridge`: Given a start before the bridge exists, Then the forwarder retries the bind
  each second and serves once the first worker's bridge appears; a stop during the wait returns at once.
- `scenario: refused-lookup-audited`: Given a worker repeating a refused lookup, Then funcd writes one line `egress lookup
  refused` per interval, naming the first name and how many were refused since the last line.
- `scenario: no-forwarder-nothing-out`: Given `server.network.egress` on and `server.network.dnsForwarderPort` 0, When a worker
  sends a lookup on `:53`, over UDP or TCP, or opens an external connection, Then nothing leaves the node.

## Scope

Unchanged (ADR-0117, ADR-0115), and: **In**: what the forwarder forwards, records and listens on; which addresses a domain rule
reaches; DNS with no forwarder. **Out**: the `input` chain's policy; the query type (the check is by name); the parts of a
forwarded query besides its name, which go upstream as they came; IPv6 forms that embed an IPv4 address, since V1 redirects IPv4
only (ADR-0115).

## Constraints & Decision drivers

Unchanged (ADR-0117, ADR-0115), and the decider's picks: forward by name, a failed read refuses, listen on the bridge address,
and a domain rule never reaches a private, link-local or node address while a `cidrs` rule does.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Forward only a name a domain rule of an applying policy allows | one allow-list for names and connections | a host reached through a CIDR rule by name needs a domain rule too; two readers of "who a rule covers", pinned by a test | chosen by the decider |
| Refuse only for a namespace with no EgressPolicy | a CIDR-only policy's names keep working | a namespace with one rule still has every other name forwarded, which is not "exactly" its domains | rejected by the decider |
| Keep forwarding every name, with a written cost | no Function changes | FEAT-0007's "nothing else" and "provably empty" and ADR-0115's exit text would have to change | rejected by the decider |
| A new Cedar action for a lookup, decided by the PDP | one decision point, as "enforce, never decide" asks; an operator's Cedar `Policy` could grant a lookup | a new action and attribute and a second permit per rule, for an answer that needs only a name and a stored list; a wildcard needs `like` or the token | rejected: the list sits beside the compiler, and `forwarder-and-pdp-agree` pins it |
| Keep recording the answer record's name | no change | a rule for a name served through a CNAME never fires, against ADR-0117's `allowlist-permits-declared-domain`; a record may name what was never asked | rejected |
| A restricted address carries no domains at the gateway | a one-line check; the Cedar decision, the audit record and CIDR rules stay as they are | an operator's Cedar `Policy` by domain gets no domains there either | chosen |
| The compiled domain permit adds a Cedar condition on the address | the decision sits in the PDP | a new resource attribute, and every compiled rule changes | rejected |
| The forwarder drops such answers | the name fails early | a name that a CIDR rule permits by address could not resolve, against the decider's example | rejected |
| A start refused with egress on and `dnsForwarderPort` 0 | one mode fewer | ADR-0206's first drill keeps outbound off with that mode, while `minScale` workers serve held | rejected |
| Keep the wildcard bind and rely on the source check | no bind retry | a socket on every interface for a service only the bridge needs | rejected by the decider |
| `IP_FREEBIND` instead of a retried bind | no wait at the first worker | Linux-only code in a cross-platform file, where ADR-0238 has a retry | rejected |

## Decision

### 1. The egress gateway

The attested set stands, with one more condition: **a domain rule reaches public addresses only.** When a connection's real
destination is restricted, it carries no domains, so only a CIDR rule can permit it, on that rule's ports; the decision and
the audit record are otherwise as ADR-0117 §1. Restricted, read on the address with any IPv4 mapping removed: private
(`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`), link-local (`169.254.0.0/16`, `fe80::/10`), loopback,
unspecified, shared (`100.64.0.0/10`), multicast, the IPv4 broadcast address, and every address on the node's interfaces,
the bridge address among them, read for each connection that has a domain to keep. A failed read ends the connection as a
failed authorization does. Any other address counts as public here, reserved ranges such as `198.18.0.0/15` included.

### 2. The DNS forwarder

With `server.network.egress` on (off, nothing runs, as ADR-0117 §6), for each message:

1. **Who asks.** The source address resolves through the `WorkerIndex`. An unknown source gets `REFUSED`.
2. **Which names.** It forwards a standard query (one question, opcode `QUERY`) only when the question's name, lower-cased
   and without its trailing dot, holds only letters, digits, `-`, `_` and the dots between labels, and equals a domain rule,
   or matches a `*.` rule (`matchWildcard`), of an EgressPolicy that applies to the worker. Anything else gets no upstream
   query: `REFUSED`, or the code the DNS server itself gives a malformed message (`NOTIMP`, `FORMERR`).
3. **Who a rule covers.** As `CompileEgressPolicy` grants: with no `appliesTo`, each Function of the namespace; with one,
   each Function it names; no other owner kind (ADR-0175 Decision 2). `EgressDomains`, beside the compiler, is the one list.
   An AIGateway's generated permit (ADR-0281 Decision 9, not built) adds its hosts to `DomainRules.For` when it lands.
4. **A failed read refuses.** The rules' read returns an error, and the query gets `REFUSED`; an error never reads as
   allowed. A nil worker index or nil rules know no source and no rule, so everything is `REFUSED`.
5. **What it records.** Each A or AAAA of the answer binds the question's name, the one the rule allowed, to that address
   with that record's TTL. A name in the answer section is not recorded. The wildcard token comes from the same list,
   narrowed to the rules that apply to the worker, which grants nothing new: the PDP reads `appliesTo` too. A failed read at
   the connection gives no token.
6. **Where it listens.** On the bridge address, UDP and TCP: `WithBridgeAddress`'s, else `127.0.0.1` as for the other ADR-0238
   listeners. `Serve` retries a bind whose address does not exist yet each `bindRetry` (1 s, chosen, as ADR-0238 Decision 3),
   warning once, until the address exists or `ctx` ends; any other bind error ends it as today.
7. **A refused lookup's line.** A refused lookup writes `egress lookup refused`, at most one line per source per
   `refusedLookupLogInterval` (10 s, chosen): the first refused name, the reason, and how many were refused since the last
   line, with the fields `namespace`, `function` (the principal's name, as `egress decision`), `domain`, `source`, `reason` and
   `more`. Unknown sources share one allowance and their line names no domain. Lookups repeat far faster than connections, so
   a line per refusal would let one worker fill the node's log.

The rest of §2 stands: the forwarder is the only resolver a worker reaches, and it is mandatory for domain policy.

### B. The DNS accept (ADR-0115)

With `server.network.egress` on and `server.network.dnsForwarderPort` 0, no forwarder and no gateway run, and the rule plan
holds no DNS accept: a worker's UDP `:53` falls to the forward chain's drop policy, its TCP `:53` to the redirect, whose gateway
is not there. Nothing goes out, so ADR-0206's first drill stays true. ADR-0115's redirect stands. `server.network.dnsResolver`
is read only with a forwarder, and required then.

## Design model

None. Forwarder and PDP are two rules that must agree, and they agree by test, not by construction: `EgressDomains` is a Go
reading of what the compiler writes as Cedar. They can differ only if one of those texts drifts; a spec would restate both and
not see it, so `forwarder-and-pdp-agree` runs every class of input through the real compiler and PDP. A connection needs both
the lookup's rule and the PDP's decision at that moment, so a policy changed between them can only narrow what it reaches. The
restricted check reads the address and the node's addresses at that moment. The log line's interval promises a rate, held by a
test on a fake clock. The bind retry is ADR-0238 Decision 3's rule on a new loop: one actor waits for an address, as in ADR-0297,
and the lane holds that it serves; `Serve` still starts in the Executor's `Start` and ends at `Stop`, so
`docs/design/platform/start-stop.fizz` is untouched.

## Behavior rows

Chapter Security:
- **Machine**: a new state `Lookup`, with `[*] --> Lookup : a worker's lookup, egress on`, `Lookup --> Refused : an unknown
  source, not a standard query, a name no rule allows, or a failed read` and `Lookup --> Allowed : a name a domain rule allows`;
  the edge `Redirected --> cedar` carries no names at a restricted address.
- **Changed**: `A domain rule` (it holds the name asked for, also through a CNAME, and reaches public addresses only); `A
  worker that just started` (its lookups too).
- **New**: a worker's lookup and its `REFUSED`; a source that is not a worker; a host reached through a CIDR rule by name needs
  a domain rule; a private, link-local or node address takes a `cidrs` rule; the forwarder's listener and its wait for the
  bridge; a refused lookup's line; an operator's Cedar `Policy` by domain; a name outside host-name characters; a name funcd
  hands a worker; `bridgeAddress` must be the bridge's first address; DNS with no forwarder.

## Temporary workarounds

None.

## Contracts

```go
// internal/network/egress: DomainRules supplies the domain rules (exact names and "*." wildcards) of the EgressPolicies that apply to a
// worker's principal. An error means they could not be read: the lookup is refused.
type DomainRules interface {
	For(ctx context.Context, principal auth.EntityRef) ([]string, error)
}

// NewForwarder replaces ADR-0117's signature. A nil workers or rules refuses every query; a nil logger means slog.Default().
func NewForwarder(listen, upstream netip.AddrPort, workers WorkerIndex, rules DomainRules, logger *slog.Logger) Forwarder

// Deps gains:
NodeAddrs func() ([]netip.Addr, error) // the addresses on the node's interfaces; nil reads them from the host

// internal/auth/cedar: EgressDomains returns ep's domain rules that apply to principal: the principals CompileEgressPolicy grants.
func EgressDomains(ep *v1.EgressPolicy, principal auth.EntityRef) []string
```

`WildcardPatterns` is removed. `WithEgressGateway` keeps its signature: the address comes from `WithBridgeAddress`.

## Implementation plan

1. `internal/auth/cedar/egress_compile.go`: `EgressDomains`. `internal/network`: `planRules` drops the DNS accept, and
   `Policy.Validate` needs a resolver only with a forwarder; `cmd/funcd/main.go` reads `dnsResolver` only then.
2. `internal/network/egress/forwarder.go`: the check before the exchange, `record` taking the question's name, the bind retry,
   the line; consts `bindRetry`, `refusedLookupLogInterval`. `gateway.go`: `Deps.NodeAddrs`; `decideConnect` drops the attested
   set for a restricted address.
3. `pkg/funcd`: `storeDomainRules` replaces `storeWildcardPatterns`; `buildEgress` passes it, the logger and
   `cmp.Or(c.bridgeAddress, "127.0.0.1")`, warning under containerd with no bridge address, as `binding_providers.go` does.
4. Doc comments on `Forwarder`, `NewForwarder`, `WithEgressGateway`, `WithBridgeAddress`, `Policy.DNSResolver`, `EgressPolicy`,
   the config key; `examples/funcdconfig.yaml`, `examples/restore-runbook.md`; `blueprint.md`; `docs/PROJECT-SUMMARY.md`.
5. Tests, one per scenario, each failing before its change: `TestScenarioLookup*` and the forwarder's others, with a counting fake
   upstream, and `TestScenarioDomainRulePublicOnly` (each restricted kind, IPv4 and IPv6) and `TestScenarioCIDRRuleNamesTheAddress`
   over `decideConnect`, in `internal/network/egress`; `TestScenarioForwarderAndPDPAgree` in `internal/auth/cedar`;
   `TestScenarioNoForwarderNothingOut` in `internal/network`. The two over `decideConnect` run the real compiler and PDP; tests
   that pass nil, nil pass a worker and a rule, and `TestIssue455` names the new fields.
6. Lane `egress`: a refused lookup with no policy, an allowed and a refused name with the wildcard policy, from the worker's netns;
   the listener's address; the line. The existing cases stay. `go test -run Lane` after the edit.
7. A real kernel: `forwarder-on-the-bridge` and `forwarder-waits-for-the-bridge` run in the `egress` lane, where the bridge appears
   after the start; a netns run in `funcd-linux-tests`, with a small embedder, checks the redirect and the restricted addresses.

**Done**: the scenarios pass, `just ci` and the `egress` lane are green, F229 → `reviewing`.

## Review checklist

- [ ] The check runs before `client.Exchange`; every refusal, a failed `For` and a nil index or rules included, sends nothing up.
- [ ] `EgressDomains` and the compiler's scopes agree, by `TestScenarioForwarderAndPDPAgree`, with the real compiler and PDP.
- [ ] `decideConnect` keeps no domain for a restricted address; a failed read of the node's addresses permits nothing.
- [ ] `buildEgress` never passes an unspecified address; a bind's `EADDRNOTAVAIL` is retried and `Serve` returns on `ctx`.
- [ ] `grep -rn WildcardPatterns` finds nothing in code; with `dnsForwarderPort` 0, `planRules` names neither `:53` nor the resolver.
- [ ] Behavior rows match this section, and `just check-behavior` passes.

## Consequences

- **(−)** A Function that reaches a host by name and a CIDR rule must also list that name as a domain rule. A Function that
  reaches an internal host by name lists both: `domains: db.internal.example` and `cidrs: 10.0.0.5/32`.
- **(−)** A CIDR rule is read as a range: `0.0.0.0/0` also covers private, link-local and node addresses, on that rule's ports.
- **(−)** A name funcd hands a worker, such as a hostname in `s3gateway.endpoint`, or one for an `internalAllow` endpoint, needs a rule.
- **(−)** A worker holds no `/etc/hosts`, so `localhost` and its own hostname go to the forwarder: `REFUSED` without a rule.
- **(−)** A refused connection to a restricted address reads as any denial in the audit, with an empty `domain`.
- **(−)** A node with egress on and no `server.network.dnsForwarderPort` has no DNS at all, where its workers reached `dnsResolver`.
- **(−)** A new worker's lookups are refused until the next sync finds it, up to `server.network.workerSyncInterval` 2 s, as its
  connections are. The first worker after a start can also find no listener for up to `bindRetry`.
- **(−)** A refused lookup fails at once; common libraries report a temporary name-resolution error.
- **(−)** An operator's Cedar `Policy` that grants `egress::connect` by domain needs an EgressPolicy domain rule for that name,
  and reaches public addresses only. One by `resource.ip` is untouched. A Cedar `forbid` stops the connection, not the lookup.
- **(−)** A policy that lists only a CNAME's target no longer covers the name a worker asks for: the rule must name that name.
- **(−)** A wildcard rule's zone can still receive names under it, and its public answers count: its author allowed that zone.
- **(−)** One store list per lookup from a known worker, one read of the node's addresses per connection with a domain to keep.
- **(neutral)** Lake and SQL engines need no lookup: their one endpoint is an address, and their fence drops DNS (ADR-0269).
- **(risk)** `runtime.containerd.bridgeAddress` must be the bridge's first address, the one the kernel's redirect picks; otherwise
  lookups fail, closed. A custom CNI setup that adds another first address must set it (ADR-0238's field).

## Open questions

None.

## References

- [FEAT-0007](../feat/0007-feat-egress-control.md) · the ADRs in the header · ADR-0272 Decision 7 · ADR-0313.
