# FEAT-0007: Egress control — the outbound edge (network manager)

- **Status**: Active (living document — the tracking table updates as ADRs progress)
- **Date**: 2026-07-09
- **Deciders**: green-0-rabbit
- **Defines**: an **egress-hardening epoch** — the **outbound counterpart to [FEAT-0006](0006-feat-ingress-hardening.md)'s
  inbound hardening**. FEAT-0006 made *reaching* a function declared, encrypted, limited, and
  authenticated; this feat governs what a function can *reach* on its way out. It realizes the
  blueprint's **Network manager (egress control)** component — designed in
  [blueprint.md](../../blueprint.md) (§Components → *Network manager*, §Security → *Egress control*) but
  never built — and delivers the deferred **egress PEP** that [ADR-0074](../adr/0074-cedar-authorization-resource-access.md)
  names as a *"remaining follow-on consumer"* of the Cedar PDP and that
  [FEAT-0003](0003-feat-data-platform.md) keeps referencing as *"the V2 egress ADR's work."* Positioned
  **alongside** the FEAT-0002 V2 hardening outlook; **not** part of v1.1 (FEAT-0001).

## Initial need

funcd's data plane is now exposed, encrypted, rate-limited, and authenticated **at ingress** (FEAT-0006),
but **outbound is wide open**: a running function can dial any host on the internet, scan the host, or
reach a sibling worker directly — and nothing observes or blocks it. The blueprint's security model is
explicit that this is a gap to close: worker networking must be **default-deny for lateral traffic**, and
outbound internet egress must be **governed by the namespace's `EgressPolicy`** and enforced by the
network manager, so that *"a compromised function cannot scan the host or sibling workers, and every
outbound call it makes is observable and blockable."*

The `EgressPolicy` resource is already **modeled** (the CRD exists; enforcement was deferred to V2). Two
concrete drivers make it due now:

- **The data-platform confidentiality invariant.** A real single-host lakehouse on funcd (FEAT-0003) has
  a cardinal rule — *a `confidential` byte never leaves the box.* Without enforced egress, funcd cannot
  make that guarantee: a transform function could ship confidential data to an external API.
- **The catalog `egress::connect` grant.** FEAT-0003/F61 explicitly parks the function→catalog egress
  grant for *"the V2 egress ADR"* — this epoch is that ADR's home.

This feat scopes **building the network manager** the blueprint already designed: the wiring that isolates
a worker, and the policy layer that governs its outbound calls.

## How this document works

This file captures **what** this capability set must contain — high level only. The **how** (netns
wiring, nftables rules, TPROXY/`SO_ORIGINAL_DST`, SNI peek, the DNS forwarder, the Cedar `egress:*`
schema) lives in ADRs (`docs/adr/`, process in [ADR-0000](../adr/0000-adr-process.md)): every feature
maps to one or more ADRs; no implementation detail belongs here. Feature status:
`idea → adr → accepted → reviewing → implemented`.

## Features

Build order runs by dependency: **F80 lands first** — it is the network substrate every egress decision
sits on — then **F81** builds the policy layer on top of it.

| # | Feature | Builds on | ADR(s) | Status |
|---|---------|-----------|--------|--------|
| F80 | **Worker network isolation (L3/L4 default-deny)** — flip worker egress from today's **open** default to **default-deny**: a function reaches other functions only *through the gateway* and platform services only *through their facades*, has **no direct route to the internet**, and cannot scan the host or sibling workers — the **fail-closed substrate** every egress decision (F81) rests on. **Opt-in / phased** so no existing deployment breaks; internal node-private services are allowed directly (they keep their own PEP), egress governs only the outside world. | [ADR-0032](../adr/0032-curated-runtime-images-container-execution.md) (worker execution + the `funcd0` CNI bridge it layers on) · blueprint *Network manager* | [ADR-0115](../adr/0115-worker-network-isolation.md) | implemented |
| F81 | **Egress policy enforcement (governed outbound)** — govern **every** outbound connection a worker makes against its namespace's **`EgressPolicy`** (domain / CIDR / port allowlists): the policy is evaluated by the **same `auth.Authorizer` PDP** as every other decision (a new `egress::*` action namespace, `EgressPolicy` compiled to Cedar), and enforced by a **transparent, in-binary egress gateway** that recovers each connection's original destination, identifies the calling workload, **allows or blocks**, and **captures every connection to the audit channel** — with **DNS-aware** domain policy so `host:port` rules work for databases and any custom protocol. **Default-deny, fail-closed** (if the gateway is down, outbound has nowhere to go). A confidential-classified workload with an empty allowlist is **provably local-only**. The curated runtime shim's HTTP interception (descriptive `403`s, no app cooperation) is a courtesy tier *above* this enforcement floor, not a substitute for it. | F80 · `EgressPolicy` (already modeled) · [ADR-0074](../adr/0074-cedar-authorization-resource-access.md) (the Cedar PDP + `EgressPolicy`→Cedar precedent) · [FEAT-0003](0003-feat-data-platform.md)/F61 (the deferred `egress::connect` grant) · blueprint *Egress control* | [ADR-0117](../adr/0117-egress-policy-enforcement.md) | implemented |

## How it lands on funcd (high level)

Outbound TCP/UDP from a worker is **redirected at the network-namespace boundary** into an egress gateway
embedded in the funcd binary (a goroutine server, not a child process). The gateway recovers the intended
destination, identifies the caller by its worker source IP, and asks the **PDP** against the namespace's
`EgressPolicy`; a **deny** returns a descriptive `403` to a well-behaved HTTP client (or drops the
connection for raw TCP), and **every** attempt — allowed or blocked — is written to the audit channel.
Because the worker has no other route out, there is **nothing to bypass**: a function with no `EgressPolicy`
reaches nothing external, and a `confidential` workload's allowlist is simply empty. This is the exact
mirror of FEAT-0006's ingress PEP, pointed the other way.

## Exit criterion

The outbound edge refuses what it should and accounts for what it serves:

- a function with **no `EgressPolicy`** cannot reach the internet **or** a sibling worker — verified by a
  blocked outbound call that produces an **audit record and zero connectivity** (default-deny, F80+F81);
- a declared `EgressPolicy` **allowlist** permits **exactly** its domains / CIDRs / ports (DNS-aware) and
  **nothing else**, and each permitted call is audited (F81);
- a **`confidential`-classified** workload's outbound is **provably empty** — the data-platform
  *"a confidential byte never leaves the box"* invariant becomes an enforced property, not a convention;
- the function→catalog **`egress::connect`** grant ([FEAT-0003](0003-feat-data-platform.md)/F61's parked
  V2 hook) is expressible as an `EgressPolicy` and enforced.

## Out of scope (tracked elsewhere)

- **Full-URL / path-level TLS policy (MITM)** — V1 of this epoch does **SNI-domain** policy for TLS (no
  interception); full-path rules would need a per-namespace MITM CA (invasive, breaks cert pinning) —
  decide in the egress ADR, not here.
- **The kernel `connect()` seccomp tier** (blueprint's tier 4, candidate) — the transparent gateway is the
  enforcement floor for V1; the kernel notifier is a later depth.
- **Multi-node egress** — single-node network manager first; cross-node egress is FEAT-0002 (V2) material.
- **Secrets as a Cedar consumer** — the *other* deferred PDP follow-on ([ADR-0074](../adr/0074-cedar-authorization-resource-access.md)) is its own scope, not egress.
