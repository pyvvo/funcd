# ADR-0317: A domain rule reaches globally reachable addresses only (amends ADR-0315)

- **Status**: Implemented (2026-11-05), review passed with no Blocker or Major ([report](../reviews/adr-0317-implementation-claude-opus-5-5.md)); Reviewing (2026-11-05, implementation done); Accepted (2026-11-05): the decider delegated acceptance to the session driving this work, on the conditions that no Blocker or Major stays open, the decision stands and Open questions reads None; judged once, its two Majors folded
- **Date**: 2026-11-05
- **Deciders**: green-0-rabbit
- **Tags**: egress, network, security
- **Realizes**: [FEAT-0007/F231](../feat/0007-feat-egress-control.md) (the addresses a domain rule reaches)
- **Supersedes (in part)**: [ADR-0315](0315-what-a-domain-rule-allows.md) — Decision 1's list of restricted addresses, its last sentence included

## Context & Need

ADR-0315 Decision 1 lists what a domain rule does not reach and counts any other address as public. FEAT-0007 asks that an allow-list permit exactly its domains and nothing else, and IANA's special-purpose address registries say of each entry whether it is globally reachable. **Purpose.** One table, read from those registries, says which addresses a domain rule does not reach. Caller: each connection the gateway decides whose address carries a domain.

## Scenarios

- `scenario: domain-rule-globally-reachable-only`: Given a domain rule whose lookup answers with an address in a prefix of the table (Decision 1), When the worker connects to it, Then refused and audited, with no `domain` in the record; at an address outside the table and off the node, allowed, and the rule's port holds.
- `scenario: globally-reachable-block-allowed`: Given an entry the registries mark globally reachable that no prefix holds, such as `192.31.196.0/24` or `2620:4f:8000::/48`, When a domain rule's lookup answers there, Then allowed.
- `scenario: cidr-rule-names-a-table-address`: Given a `cidrs` rule that names `198.18.0.5/32` on 443, When the worker connects there, by address or with a name attested, Then allowed; on another port, refused.

## Scope

Unchanged (ADR-0315), and **Out**: a table an operator changes; reading the IPv4 address inside an IPv6 form other than the mapping, which ADR-0315 leaves out: such a form counts by its own prefix.

## Constraints & Decision drivers

Unchanged (ADR-0315), and the decider's picks: the registries are the source, Azure's address is named, a `cidrs` rule that names an address still reaches it, and no new config key.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| One table from IANA's two registries, plus multicast and the named others | one list, checked row by row against its source | a copy, so a new registry row changes it by an amendment; a reachable row nested in an entry is restricted too | chosen by the decider |
| Add ranges to the `netip` checks one by one | a small change | no source to check them against, and each range is found on its own | rejected |
| Leave out each reachable row nested in an entry | follows the registries' column exactly | nine more rows of exceptions, for anycast and protocol blocks a `cidrs` rule reaches anyway | rejected |

## Decision

1. **The egress gateway: a domain rule reaches globally reachable addresses only.** The rest of ADR-0315 Decision 1 stands, with its list replaced. Restricted, read on the address with any IPv4 mapping and zone removed, is an invalid address, every address on the node's interfaces, and each prefix below, read 2026-11-05; any other address counts as globally reachable. An entry that holds narrower rows counts whole, so `192.0.0.0/24` and `2001::/23` include the rows marked globally reachable. `::ffff:0:0/96` is listed to match its registry row, and no address reaches it, as the mapping comes off first. A later registry row changes the table by an amendment.
   - IANA's IPv4 registry, not globally reachable: `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10`, `127.0.0.0/8`, `169.254.0.0/16`, `172.16.0.0/12`, `192.0.0.0/24`, `192.0.2.0/24`, `192.168.0.0/16`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `240.0.0.0/4`.
   - IANA's IPv6 registry, not globally reachable: `::/128`, `::1/128`, `::ffff:0:0/96`, `64:ff9b:1::/48`, `100::/64`, `100:0:0:1::/64`, `2001::/23`, `2001:db8::/32`, `3fff::/20`, `5f00::/16`, `fc00::/7`, `fe80::/10`.
   - Others: `192.88.99.0/24` (deprecated), `2002::/16` and `64:ff9b::/96`, registry rows that carry an IPv4 address; multicast `224.0.0.0/4`, `ff00::/8`; deprecated site-local `fec0::/10`; Azure's virtual public address `168.63.129.16/32`.

## Design model

None: a read of a fixed table and of the node's addresses at one moment, with no actors or timers. ADR-0315's order stands.

## Behavior rows

Chapter Security. **Changed**: `A domain rule at a private, link-local, loopback, shared or node address` becomes `A domain rule at an address that is not globally reachable`; `An operator's Cedar Policy by domain` (it reaches globally reachable addresses only); the note's sentence. **New**: a reachable row nested in an entry; a new registry row.

## Temporary workarounds

None.

## Contracts

`internal/network/egress` gains `var notGlobal []netip.Prefix`; `Deps.restricted` keeps its signature and reads it, then the node's addresses.

## Implementation plan

`internal/network/egress/gateway.go`: `notGlobal`, `restricted`. `egress_test.go`: `TestScenarioDomainRuleGloballyReachableOnly` (a sample per prefix but `::ffff:0:0/96`, and the mapped form, a zoned form and the zero `Addr` on `restricted` directly), `TestScenarioGloballyReachableBlockAllowed`, `TestScenarioCIDRRuleNamesATableAddress`, with the real compiler and PDP, and `TestNotGlobalIsTheDecidedTable`; the existing tests' node addresses move outside the table. Docs: `api/types/v1alpha1/egresspolicy.go`, `blueprint.md`, `docs/PROJECT-SUMMARY.md`, `docs/behavior/security.md`. **Done**: the scenarios, `scripts/agent/gate.sh` and the `egress` lane pass, F231 → `reviewing`.

## Review checklist

- [ ] `notGlobal` holds exactly Decision 1's prefixes, each masked and, but for `::ffff:0:0/96`, with a test sample; each entry the registries mark not globally reachable lies inside one.
- [ ] `restricted` removes the mapping and the zone first, and an invalid address counts as restricted; `decideConnect` keeps no domain there, and a `cidrs` rule that names the address still permits it. A node address outside the table is refused, and a mutant that skips the node comparison fails `TestScenarioDomainRulePublicOnly`.

## Consequences

- **(−)** A name that answers in a benchmark, documentation or reserved IPv4 range, such as a lab on `198.18.0.0/15`, takes a `cidrs` rule that names the address. So do the reachable rows nested in `192.0.0.0/24` and `2001::/23`, `64:ff9b::/96`, `2002::/16` and, on any cloud, `168.63.129.16`. A wider range holds these addresses too: `0.0.0.0/0` reaches `168.63.129.16` on its ports, as ADR-0315 says of a range.
- **(−)** A new registry row counts as globally reachable until an amendment adds it, as does IPv6 space outside these rows that IANA's address-space registry reserves; the IPv6 rows take effect once IPv6 worker egress lands, as V1's gateway sees IPv4 only. **(neutral)** 32 prefix checks per connection with a domain to keep, beside the read of the node's addresses.

## Open questions

None.

## References

[IANA's IPv4](https://www.iana.org/assignments/iana-ipv4-special-registry/iana-ipv4-special-registry.xhtml) and [IPv6](https://www.iana.org/assignments/iana-ipv6-special-registry/iana-ipv6-special-registry.xhtml) special-purpose registries, read 2026-11-05 · [Microsoft on 168.63.129.16](https://learn.microsoft.com/en-us/azure/virtual-network/what-is-ip-address-168-63-129-16) · [FEAT-0007](../feat/0007-feat-egress-control.md) · ADR-0272 Decision 7 · ADR-0313.
