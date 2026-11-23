# ADR-0353: The edge serves a workload only its own namespace's hosts

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: edge, network, security, add-ons, hosts
- **Realizes**: [FEAT-0006/F273](../feat/0006-feat-ingress-hardening.md) (workload host pinning)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md)
  Decision 3 (an engine's fence gains the edge's address on the bridge); [ADR-0115](0115-worker-network-isolation.md)
  (`InternalAllow` gains the edge's address on the bridge)
- **Relates to**: [ADR-0238](0238-container-bridge-address.md) (the bridge address) · [ADR-0240](0240-namespace-hosts.md)
  (a namespace's hosts) · [ADR-0117](0117-egress-policy-enforcement.md) and [ADR-0175](0175-engine-is-its-own-principal.md)
  (the indexes by source address) · ADR-0352 (an engine's egress) · ADR-0354 (the client address behind a proxy)

## Context & Need

A tunnel connector (frpc as an add-on engine) hands each visitor's request to funcd's edge, so the edge must be
reachable from inside the container network. A workload or an engine that reaches the edge can name any host in its
`Host` header, so it could reach another namespace's Functions, Sites and add-on objects through it, past the
isolation that ADR-0115 gives it. The decider decided on 2026-11-23, after a deep dive, that the edge serves a funcd
workload only its own namespace's hosts.

## Scenarios

- `scenario: tunnel-reaches-its-hosts`: Given containerd with egress on, an add-on engine of object `public` in
  namespace `team`, and a Function `api` with its host in `team`; when the engine sends a request for that host to the
  edge on the bridge address; then the edge serves it as it serves any visitor.
- `scenario: other-namespace-host-refused`: Given the same engine; when it names a host of namespace `other`; then the
  edge answers 403 and serves nothing of `other`.
- `scenario: unknown-caller-refused`: Given a connection to the edge on the bridge address from an address that
  neither egress index holds; then the edge answers 403.
- `scenario: a-worker-is-pinned-too`: Given a Function worker of `team` that calls the edge on the bridge address;
  then it reaches `team`'s hosts and no other namespace's.
- `scenario: the-public-listener-is-unchanged`: Given a request to `server.dataPlaneAddr` from outside the container
  network; then the edge serves every namespace's hosts, as today.

## Scope

In: the edge's address on the bridge, how a caller there is known, and which hosts it gets. Out: the client address
that the edge reports for a request a proxy forwards (ADR-0354); the process runtime and `funcdctl dev`, where workers
run on the host and no bridge exists (no pin, as today); several nodes (FEAT-0002, F247).

## Constraints & Decision drivers

- Fail closed: a caller on the bridge that funcd cannot name gets nothing.
- No new identity: the egress indexes already map a source address to its principal (ADR-0117 §5, ADR-0175).
- The gateway never dials the edge's addresses, as it never dials engines' (ADR-0332), so no workload reaches the edge
  by an EgressPolicy rule; the pinned listener is the only path from the container network.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| A listener on the bridge, callers pinned to their namespace | one path; the existing indexes name the caller | only with containerd and egress on; a new worker's first calls can wait for its index (2 s sync) | chosen by the decider (2026-11-23) |
| The engine reaches the public listener through an EgressPolicy CIDR rule | no new listener | the gateway would dial the edge, and every namespace's hosts stay reachable | rejected |
| A per-object list of hosts in the add-on object | explicit | the namespace already owns its hosts (ADR-0240); a second list to keep in step | rejected |

## Decision

1. **The address.** With containerd and `server.network.egress: true`, the edge accepts connections on the bridge
   address (ADR-0238) at the port of `server.dataPlaneAddr`. When `server.dataPlaneAddr` binds every address, its
   listener's connections to the bridge address get this Decision; otherwise funcd opens a listener on the bridge
   address at that port.
2. **The way in.** `InternalAllow` (ADR-0115) and every add-on engine's fence (ADR-0332 Decision 3) gain that address
   and port, so a worker or an engine reaches the edge directly. The egress gateway never dials it.
3. **The caller.** A connection to the edge on the bridge address is from the principal that the worker index or the
   engine index holds for its source address: a Function, a Job run's Job, a Lake engine or an add-on object, each in
   its namespace. A source that neither index holds gets 403, and the connection is closed.
4. **The pin.** The edge serves such a caller only the hosts of its own namespace (ADR-0240: the namespace's host, its
   Routes, Sites and add-on objects' hosts). Any other host gets 403 with the problem type `HostNotInNamespace` (new),
   and nothing of the other namespace runs.
5. **Freshness.** A caller is known from the moment its index holds it: before an engine starts (ADR-0352), before a
   Job run's call (ADR-0351's fix), and within one worker sync for a Function (ADR-0163, 2 s by default).

## Design model

None. The decision adds no lifecycle or timer: the pin is a lookup at each connection, over indexes whose freshness
ADR-0163, ADR-0351 and ADR-0352 decide.

## Behavior rows

- **edge.md**: a caller on the bridge address reaches only its namespace's hosts; an unknown caller gets 403; the
  public listener is unchanged.

## Temporary workarounds

None.

## Contracts

```go
// internal/edge: the bridge-side guard, wrapping the edge's handler for connections whose local address is the bridge.
type CallerIndex interface {
	// Caller is the principal whose worker or engine holds addr; ok is false for an unknown source.
	Caller(addr netip.Addr) (ref auth.EntityRef, ok bool)
}

// PinToNamespace serves a request only when its host belongs to the caller's namespace; 403 HostNotInNamespace else.
func PinToNamespace(next http.Handler, callers CallerIndex, hosts HostsOf) http.Handler

// HostsOf answers whether host belongs to namespace ns (its host, Routes, Sites and add-on objects' hosts).
type HostsOf func(ns v1.NamespaceName, host string) bool
```

| Item | Consumes / exposes | Notes |
|---|---|---|
| the bridge address at the data plane's port | exposes | containerd with egress on only |
| `InternalAllow`, an engine's fence | consumes | gain the address |
| the worker and engine indexes | consumes | name the caller |
| 403 `HostNotInNamespace` | exposes | new problem type |

## Implementation plan

1. `internal/edge`: `PinToNamespace`, the caller index over the two egress indexes, and the hosts lookup over the
   edge's routing table.
2. `pkg/funcd`: the bridge listener (or the guard on a wildcard listener's bridge connections); `InternalAllow` and the
   engines' fences gain the address.
3. `internal/services/addon/reconcile.go` and the containerd driver: the engine fence's new endpoint.
4. Tests, each written to pass: `PinToNamespace`'s table; one `TestScenario…` per scenario in `pkg/funcd`; a case in
   the egress lane on containerd, an add-on engine reaching its namespace's host on the bridge address and refused
   another's.
5. Docs: edge.md and security.md rows, the guide's network page, `just generate`.
6. Done when `just ci-full` and the egress lane pass.

## Review checklist

- [ ] With containerd and egress on, the edge accepts the bridge address at the data plane's port, and only then.
- [ ] A caller there gets only its namespace's hosts; an unknown source and another namespace's host get 403.
- [ ] The gateway never dials the edge's addresses; the public listener serves as before.
- [ ] Every scenario has its passing `TestScenario…`, and the egress lane case passed on containerd.

## Consequences

- A tunnel engine reaches the edge for its namespace's hosts, and no workload can reach another namespace through the
  edge from inside the container network.
- A Function worker's first calls to the edge can wait up to one worker sync for its index.
- With the process runtime or under dev, nothing changes: no bridge, no pin.

## Open questions

None.

## References

- The feature issue: [#1553](https://github.com/pyvvo/funcd/issues/1553).
- The decider's answers of 2026-11-23, in the DR session ("Host pinning, decided yes after a deep dive").
- Experiment 0027 (pyvvo/experiment, private), path C.
