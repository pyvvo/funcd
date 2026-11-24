# ADR-0353: The edge serves a workload only its own namespace's hosts

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: edge, network, security, add-ons, hosts, egress
- **Realizes**: [FEAT-0006/F273](../feat/0006-feat-ingress-hardening.md) (workload host pinning)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0238](0238-container-bridge-address.md) Decision 3 (the egress allow list gains the edge's bridge address and
  port, and the edge binds that address as the other readers do);
  [ADR-0240](0240-namespace-hosts.md) Decision 6 (a caller on the bridge address whose `X-Funcd-Namespace` names
  another namespace gets 403, on any host);
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) Decision 3 (what the egress gateway never
  connects to gains the edge's addresses);
  [ADR-0317](0317-a-domain-rule-reaches-globally-reachable-addresses-only.md) Decision 1 (a `cidrs` rule that names a
  node address no longer reaches the data plane's port there);
  [ADR-0175](0175-engine-is-its-own-principal.md) Decision 1, as ADR-0281 and ADR-0351 amend it (the runtime writes
  the egress indexes, not `reconcileEgressWorkers`);
  [ADR-0163](0163-retry-times-in-config.md) (its `server.network.workerSyncInterval` row and the egress half of
  `server-keys-pace-shutdown-and-egress-sync`: the key goes)
- **Relates to**: [ADR-0117](0117-egress-policy-enforcement.md) §5 (the index the containerd runtime writes) ·
  ADR-0352 (an engine's egress; `egress.Sources`) · ADR-0354 (the client address behind a proxy) ·
  [ADR-0115](0115-worker-network-isolation.md) (the allow list)

## Context & Need

A tunnel connector (frpc as an add-on engine) hands each visitor's request to funcd's edge, so the edge must be
reachable from inside the container network. A workload or an engine that reaches the edge can name any host and any
`/function/<name>` path, so it could reach another namespace's Functions, Sites and add-on objects through it, past the
isolation that ADR-0115 gives it. The decider decided on 2026-11-23, after a deep dive, that the edge serves a funcd
workload only its own namespace's hosts. The egress indexes that name such a caller follow the workers with a 2 s poll
today, so an address freed and handed out again can still name its former holder: the pin needs them exact.

## Scenarios

- `scenario: tunnel-reaches-its-hosts`: Given containerd with egress on, an add-on engine of object `public` in
  namespace `team`, and a Function `api` with its host in `team`; when the engine sends a request for that host to the
  edge on the bridge address; then the edge serves it as it serves any visitor.
- `scenario: other-namespace-host-refused`: Given the same engine; when it names a host of namespace `other`; then the
  edge answers 403 and serves nothing of `other`.
- `scenario: by-name-pinned`: Given the same engine; when it sends `/function/payroll` on `team`'s Route host
  `api.example.com`, which has no `/` rule, with `X-Funcd-Namespace: other`; then 403; without the header the call
  resolves in `default`, and gets 403 too.
- `scenario: default-caller`: Given a Function worker of `default`; then it reaches `default`'s host-less entries and
  its Functions by name, and gets 403 on a host that `team` reserves.
- `scenario: sign-in-pinned`: Given the engine of `team`; when it forwards `/.auth/callback` whose state names an
  OIDCProvider of `other`; then 403, and no code exchange runs.
- `scenario: unknown-caller-refused`: Given a connection to the edge on the bridge address from an address that
  neither egress index holds, or that the two hold with different principals; then the edge answers 403.
- `scenario: a-worker-is-pinned-too`: Given a Function worker of `team` that calls the edge on the bridge address;
  then it reaches `team`'s hosts and no other namespace's.
- `scenario: reused-address-is-its-new-holder`: Given an engine of `team` that stopped, and a Function worker of
  `other` given its address; when that worker calls the edge on the bridge address, from its first call; then it gets
  `other`'s hosts only.
- `scenario: gateway-never-dials-the-edge`: Given `server.dataPlaneAddr` on every address, and an EgressPolicy whose
  `cidrs` rule holds the node's LAN address; when a worker connects to that address at the data plane's port; then the
  gateway refuses it.
- `scenario: the-public-listener-is-unchanged`: Given a request to `server.dataPlaneAddr` from outside the container
  network; then the edge serves every namespace's hosts, as today.

## Scope

In: the edge's address on the bridge, how a caller there is known, what it gets, and when the indexes that name it
are written. Out: the client address that the edge reports for a request a proxy forwards (ADR-0354); the process
runtime and `funcdctl dev`, where workers run on the host and no bridge exists, and containerd with egress off, where a
worker reaches the edge on any node address: no pin, as today; several nodes (FEAT-0002, F247).

## Constraints & Decision drivers

- Fail closed: a caller on the bridge that funcd cannot name, or names twice, gets nothing.
- No new identity: the egress indexes map a source address to its principal (ADR-0117 §5, ADR-0332 Decision 3), and
  the lateral deny makes that address unforgeable (ADR-0117 Constraints).
- The pin reads the namespace that serves the request, the one the data plane resolves, never a header alone.
- The gateway never dials the edge, as it never dials an engine (ADR-0332 Decision 3), so the pinned listener is the
  only path from the container network inside funcd.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| A listener on the bridge, callers pinned to the namespace the data plane resolves | one path; the existing indexes name the caller | only with containerd and egress on | chosen by the decider (2026-11-23) |
| A pin on the Host header | simple | the by-name path serves `/function/<name>` on a host outside the base domain in the namespace a header names (ADR-0240 Decision 6) | rejected |
| The engine reaches the public listener through an EgressPolicy CIDR rule | no new listener | the gateway would dial the edge, and every namespace's hosts stay reachable | rejected |
| A per-object list of hosts in the add-on object | explicit | the namespace already owns its hosts (ADR-0240); a second list to keep in step | rejected |
| The listener's address in each engine's `EgressOnly` too | the decider's answer | with egress on an engine has no output chain (ADR-0352 Decision 2); with egress off no pinned listener exists, and a wildcard data plane would serve that engine unpinned | rejected here (to confirm) |
| The indexes keep their 2 s poll | no change | up to one poll, a new worker is refused, and a reused address names its former holder, maybe of another namespace | rejected here; the decider's answer accepted the window (to confirm) |

## Decision

1. **The listener.** Where the egress gateway runs (containerd, `server.network.egress: true` and a
   `dnsForwarderPort`, `cmd/funcd/main.go`), the edge serves on funcd's bridge address (ADR-0238) at the port the data
   plane bound: when `server.dataPlaneAddr` binds every address, through that listener; otherwise through a listener
   of its own on the bridge address, retried until the bridge exists, as the forwarder's is (ADR-0238 Decision 3).
   It serves TLS when `server.tls.enabled`, as both listeners do. With port 0, the default, the port changes at each
   start, so a tunnel that names the edge needs a fixed port.
2. **The way in.** `InternalAllow` gains that address and port, beside the change feed's (ADR-0238 Decision 3), so a
   worker, and an engine with egress on (ADR-0352 Decision 2), reaches the edge directly. The address enters no
   engine's fence. The gateway refuses, whatever rule permits it, a `cidrs` rule included, every address and port the
   data plane listens on: the bridge address and the bound address at its port, and with a wildcard bind every node
   address at that port, as it refuses an engine's (ADR-0332 Decision 3).
3. **The caller.** A request on the bridge address is from the principal that `egress.Sources` (ADR-0352 Decision 3)
   names for its source address: a Function, a Job, a Lake or an add-on object, each in its namespace. A source it
   does not name, unknown or held by the two indexes with different principals, gets 403.
4. **The pin.** The edge serves such a caller only what it resolves to the caller's namespace. Under `/.auth/`, the
   callback resolves to the namespace of the provider its sign-in state names, and the logout, which names none, is
   served; any other request resolves to the namespace of the edge entry it matches, else of its `/function/<name>`
   path (ADR-0240 Decision 6). Such a caller's `X-Funcd-Namespace` is refused unless it names the caller's namespace,
   on any host. Another namespace, or a refused header, gets 403 (`fault.Forbidden`, `<namespace> is not the caller's
   namespace`) before any code exchange, edge PEP, wake or backend, so nothing of it runs.
5. **When an index is written.** An entry of the worker index or the engine index is written before its worker serves
   and removed before its address can be reused. The containerd runtime writes it once it has set up the worker's
   network, before `Create` returns and so before the worker starts, and removes it once the worker's task is gone,
   before it releases the address; a failed write fails the `Create`. These are the indexes' only writes:
   `syncEgressWorkers`, the add-on runtime's `BeforeStart` index and a Job run's `indexEgress` go, and so does
   `server.network.workerSyncInterval`, which a config can no longer set, as with any unknown key (ADR-0238 Decision
   2). A worker is indexed as today: a Function's or a Lake's worker by its owner kind, a run's worker as its run's Job
   (ADR-0351 Decision 6), an add-on engine as its object, in the engine index.

## Design model

Decision 5 removes the indexes' timer and their racing writers: the runtime writes an address's entry after it leases
the address to a worker and removes it before it takes the address back, in the same `Create` and stop, so the lease
orders every write of an address and one writer remains. The spec checks that order.

- **Spec**: [`docs/design/egress/address-index.fizz`](../design/egress/address-index.fizz) · state machine
  [`address-index.md`](../design/egress/address-index.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `HolderIsNamed` | always | `scenario: reused-address-is-its-new-holder` | `TestScenarioReusedAddressIsItsNewHolder` |

- **Bounds**: two workers at one address, 2 concurrent actions, 3 actions: the smallest reuse, a Create, its stop,
  and the other worker's Create while that stop runs.
- **Faults**: none. A stop cut between the removal and the release leaves an address that nothing sends from; a crash
  of funcd loses the indexes with their readers, and the boot sweep returns every lease before a worker starts
  (ADR-0167 Decision 8).
- **Environment**: the IPAM leases an address to one worker at a time, from its `Create` to the release in its stop
  (`lease`); a worker sends nothing before its start or once its task is gone (the phases).
- **Mutants**: `write-after-start`, the 2 s sync's late add, and `remove-after-release`, a removal by address after
  the release, as ADR-0347's run executor removes its worker's entry once the stop returns, each fail `HolderIsNamed`.
- **Result**: pass, 13 states, 0.4 s, FizzBee v0.5.4; wider (3 concurrent actions, 8 actions): pass, 20 states.
- **Left to tests**: the engine index, written by the same two steps; that every release of an address, a stop's or a
  sweep's, runs the removal first (the driver's tests); the readers, which look the entry up at each connection or
  request, the pin included.

## Behavior rows

- **calls.md**: a caller on the bridge address gets only what the data plane resolves to its namespace, by-name calls
  and `/.auth/callback` included; an unknown or conflicting caller, or its `X-Funcd-Namespace` naming another
  namespace, gets 403; the public listener is unchanged.
- **egress.md**: the row "A worker's traffic to the node, egress on" gains the edge's bridge address and port; the
  gateway refuses the edge's addresses whatever rule permits them; the row "A worker that just started" (refused until
  the next sync) becomes: a worker is in its index from its creation and leaves it before its address is released.
- **addons.md**: the row "Who reaches an engine" cites the runtime's write in place of `BeforeStart`'s.
- The generated values of calls.md and platform-lifecycle.md lose `server.network.workerSyncInterval` and
  `defaultWorkerSyncInterval`.

## Temporary workarounds

None.

## Contracts

```go
// internal/dataplane (new Option): a request whose local address is bridge is served under Decisions 3 and 4, its
// caller looked up in callers (egress.Sources).
func WithBridgeCallers(bridge netip.Addr, callers egress.WorkerIndex) Option

// internal/network/egress: Deps gains Edge (new), each address and port the data plane listens on, an unspecified
// address standing for every node address (NodeAddrs); decideConnect refuses a destination there as an engine's.
Edge []netip.AddrPort

// internal/runtime/containerd: Config gains Addresses (new), nil unless the egress gateway runs.
type AddressHooks struct {
	// Assigned runs once spec's worker has its network, before Create returns; its error fails the Create.
	Assigned func(ctx context.Context, spec runtime.WorkerSpec, in runtime.Instance) error
	// Released runs once the worker's task is gone, before its address is released.
	Released func(in runtime.Instance)
}
```

| Item | Consumes / exposes | Notes |
|---|---|---|
| the bridge address at the data plane's port | exposes | only where the egress gateway runs |
| `InternalAllow` | consumes | gains that address and port |
| `egress.Sources` (ADR-0352) | consumes | names the caller |
| 403 `fault.Forbidden` | exposes | another namespace, an unknown or a conflicting caller |
| `server.network.workerSyncInterval` | — | removed |

## Implementation plan

1. `internal/runtime/containerd`: the hooks, in `Create` and before each release of an address (a stop, and a sweep
   of a worker the driver tracks).
2. `pkg/funcd`: the hooks write the two indexes as `egressPrincipal` names a worker; `syncEgressWorkers`, the
   `BeforeStart` index and `indexEgress` go; `internal/platform/config` and `cmd/funcd` lose the key.
3. `internal/dataplane`: `WithBridgeCallers`, checked where the namespace is resolved and in the sign-in callback
   before its exchange. `pkg/funcd`: the bridge listener, or the guard on a wildcard listener; `InternalAllow` gains
   the bound port. `internal/network/egress`: `Deps.Edge` and its refusal.
4. Lands with ADR-0352.
5. Tests, each written to pass: the pin's table; the hooks against a release and a reused address; one
   `TestScenario…` per scenario in `pkg/funcd`, `TestScenarioReusedAddressIsItsNewHolder` with the oracle marker
   `docs/design/egress/address-index.fizz#HolderIsNamed`; a case in the egress lane on containerd, an add-on engine
   reaching its namespace's host on the bridge address and refused another's.
6. Docs: the calls.md, egress.md and addons.md rows, the guide's network page, `just generate`, `just behavior-values`.
7. Done when `just ci-full` and the egress lane pass.

## Review checklist

- [ ] The bridge listener exists only where the gateway runs, at the data plane's bound port, and follows TLS.
- [ ] A bridge caller gets only what the data plane resolves to its namespace, by-name calls, the header and
  `/.auth/callback` included; an unknown or conflicting caller gets 403.
- [ ] The gateway never dials an address and port the data plane listens on, a `cidrs` rule included; the public
  listener serves as before.
- [ ] Only the runtime writes the indexes: after a worker's network is set up, and before its address is released;
  `HolderIsNamed`'s oracle test carries its marker, and `just check-specs` passes.
- [ ] Every scenario has its passing `TestScenario…`, and the egress lane case passed on containerd.

## Consequences

- A tunnel engine reaches the edge for its namespace's hosts, and no workload reaches another namespace's through the
  bridge listener.
- A worker or an engine is known from its creation, so its first calls are not refused, and a reused address never
  names its former holder.
- What no rule covers: a hop outside funcd that loops back, such as a public NAT or the CDN in front of a tunnel, gives
  a workload whose policy allows that public name the path any visitor has, unpinned.
- With the process runtime, under dev, or on containerd with egress off, nothing changes: no pin.
- With port 0, a tunnel's configured edge address breaks at each restart.
- A config that sets `server.network.workerSyncInterval` no longer starts.

## Open questions

None.

## References

- The feature issue: [#1553](https://github.com/pyvvo/funcd/issues/1553).
- The decider's answers of 2026-11-23, in the DR session ("Host pinning, decided yes after a deep dive").
- Experiment 0027 (pyvvo/experiment, private), path C.
