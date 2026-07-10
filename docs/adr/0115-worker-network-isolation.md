# ADR-0115: Worker network isolation — the L3/L4 egress substrate

- **Status**: Implemented
- **Date**: 2026-07-09
- **Accepted**: 2026-07-09
- **Implemented**: 2026-07-09
- **Deciders**: green-0-rabbit
- **Tags**: egress, network, security, nftables, network-manager
- **Acceptance note**: judge Blockers folded — **B1** (lateral-deny moved to the nftables **`bridge` family**: a same-bridge worker↔worker frame is L2-switched and an `inet`/L3 rule would silently pass it → fail-open) and **B2** (the `inet` accepts for internal services + DNS now **precede** the `drop` policy, so host-resident internal services stay reachable). Majors — **M1** (explicit chain/hook per rule: nat-prerouting REDIRECT · filter-forward accepts · filter-input accept), **M2** (DNS accept **scoped to `DNSResolver`**, not any `:53`; the "all external denied" consequence softened to except the resolver), **M3** (`InternalAllow` are **worker-reachable endpoint** addrs, not `127.0.0.1` listen addrs; `WorkerSubnet` read from `containerd.Config.SubnetCIDR`). Minors: `New` build-tag dispatch, `server.network` grouping rationale, `inet`-nat min-kernel note.
- **Realizes**: [FEAT-0007/F80](../feat/0007-feat-egress-control.md) (worker network isolation — the L3/L4 default-deny substrate)
- **Relates to**: [ADR-0116](0116-egress-policy-enforcement.md) (F81 — egress policy enforcement, the gateway this substrate redirects into — *to be written*) · [ADR-0032](0032-curated-runtime-images-container-execution.md)/[ADR-0011](0011-worker-runtime-port.md) (containerd worker execution + the `funcd0` CNI bridge) · [ADR-0056](0056-temporary-runtime-self-provisioning.md) (the self-provisioned CNI conflist this layers on, never-clobber) · [ADR-0074](0074-cedar-authorization-resource-access.md) (the PDP F81 calls) · blueprint *Network manager (egress control)*

## Context & Need

funcd's ingress is now declared, encrypted, limited, and authenticated (FEAT-0006), but **worker egress is wide open**. The containerd runtime already wires each worker into the `funcd0` CNI bridge with **`isGateway` + `ipMasq` + a `0.0.0.0/0` route** ([internal/runtime/containerd/cni_conf.go](../../internal/runtime/containerd/cni_conf.go), ADR-0011/0032/0056), so today a worker reaches any internet host directly and can send lateral packets to sibling workers — the CNI `firewall` plugin does host↔container baseline only, no lateral-deny, no egress governance.

The blueprint's **Network manager** requires the opposite: lateral traffic **default-deny**, and outbound governed by the namespace's `EgressPolicy` so *"a compromised function cannot scan the host or sibling workers, and every outbound call is observable and blockable."*

**Purpose.** F80 is the **substrate** every egress decision rests on: a host-level **nftables policy layer** — programmed over the existing CNI wiring, not replacing it — that, when enabled, makes worker egress **default-deny** and **redirects** remaining external outbound to the egress gateway (F81 builds the gateway; this ADR lays the ruleset it plugs into). It is **fail-closed by construction** and **opt-in** so no existing deployment breaks. Caller: the composition root, once at daemon start (after the runtime is up, before workers serve).

## Scenarios

Each becomes a named acceptance test. The behavioral ones need a real kernel + netns (a Linux integration lane, deferred per the test plan); the substrate-construction ones are unit-level.

- `disabled-passthrough` — Given the network manager is **off** (the default), When a worker sends outbound traffic, Then it egresses exactly as today (open) — no funcd ruleset is programmed.
- `ruleset-programmed` — Given the manager is **enabled** at daemon start, Then the host carries the `funcd_egress` nftables table over the worker subnet — default-deny lateral, allow internal services + DNS, redirect remaining external TCP — verifiable by reading the ruleset back, applied **once before any worker starts**.
- `lateral-denied` — Given enabled, When one worker sends a packet directly to a sibling worker's IP, Then it is **dropped**.
- `external-redirected-failclosed` — Given enabled and **no F81 gateway listening**, When a worker opens a TCP connection to an external host, Then the connection is redirected to the gateway port and **refused** (nothing leaves) — fail-closed.
- `internal-service-allowed` — Given enabled, When a worker connects to a funcd node-private service (the S3 frontend / catalog endpoint in the internal allowlist), Then it passes **directly** — not dropped, not redirected.
- `dns-resolves` — Given enabled (F80 alone), When a worker resolves a name, Then DNS to the node resolver **succeeds** (F81 later swaps this to the correlating forwarder).
- `teardown-clean` — Given enabled, When funcd shuts down, Then the `funcd_egress` table is **removed** — no orphaned rules.
- `non-linux-noop` — Given the process runtime (macOS/dev), When enabled, Then it is a **no-op** (netns/nftables unavailable), logged once — no error.

## Scope

**In:** an `internal/network` package — a `Manager` port + a Linux **`google/nftables`** driver and a **no-op** driver — that programs/removes **two host nftables objects** over the funcd0 worker subnet at daemon start/stop when enabled: a **`bridge`-family** lateral-deny (worker↔worker) and an **`inet`** egress-policy table (accept internal-allowlist + the scoped DNS resolver, redirect remaining external TCP to the gateway port, else default-deny — which also covers worker→host/LAN); the opt-in config flag (`server.network.*` — grouped with the other server-side posture toggles `limits`/`auth` from FEAT-0006, though it governs the worker path); the composition-root wiring; the process-runtime no-op.

**Out (named follow-ons):**
- **The egress gateway itself** — accepting the redirect, `SO_ORIGINAL_DST`, SNI/Host peek, the src-IP→workload `Ref` identity map, PDP decision, connection splice, per-connection audit — and **`EgressPolicy`→Cedar**: all **F81** ([ADR-0116](0116-egress-policy-enforcement.md), to be written).
- **The DNS forwarder** (domain↔IP correlation) — F81; F80 lets DNS reach the node resolver.
- **`TPROXY` / UDP** — V1 uses `REDIRECT` + `SO_ORIGINAL_DST` (TCP: HTTP/HTTPS/DB); TPROXY is a later refinement.
- **Multi-node / multi-bridge egress** — single funcd0 bridge, single node (FEAT-0002).
- **The kernel `connect()` seccomp tier** — blueprint tier 4, candidate.

## Constraints & Decision drivers

- **Pure-Go, single static binary, no cgo, no provisioned binary** → nftables via netlink (`google/nftables`), not shelling `nft`.
- **Never clobber the CNI conflist** (ADR-0056) → **layer** nftables on top of the existing bridge, do **not** modify the conflist.
- **Same-bridge lateral is L2, not L3** → worker↔worker traffic on funcd0 (same subnet) is bridge-switched and never hits the `inet` hooks, so lateral filtering **must** live in the nftables **`bridge` family** (chosen over enabling the host-wide `br_netfilter`/`bridge-nf-call-iptables` sysctl — surgical, no cross-bridge side effect). The `inet` **nat** chains used for the redirect require kernel **≥ 4.18** (the homebox target satisfies this).
- **Opt-in / off by default (phased)** → flipping to default-deny egress is breaking; existing open-egress workloads must be untouched until enabled (mirrors TLS/limits/authn in FEAT-0006).
- **Fail-closed** → the table is applied **once at daemon start over the whole worker subnet** (before any worker), and redirect-with-no-gateway refuses.
- **Internal node services are not external egress** → worker→funcd's own node-private services (S3 frontend ADR-0080, ingress/catalog) are **allowed directly**; they keep their own PEP (SigV4 / token). Egress governs only the outside world.
- **Linux/containerd only** → the process runtime (dev/CI, macOS) is a no-op.
- **Apache-2.0/MIT deps only** → `google/nftables` (Apache-2.0), `mdlayher/netlink` (MIT). Verified 2026-07-09.

## Alternatives considered

- **Modify the CNI conflist (drop `ipMasq`/default route)** vs **layer nftables on top.** Layering wins: it honors ADR-0056's never-clobber rule, doesn't touch the validated conflist (or an operator's custom one), and a `REDIRECT` in nat-PREROUTING intercepts a worker's packet *before* the POSTROUTING masquerade anyway — so the open path is neutralized without editing the bridge. Rejected: conflist surgery is invasive and diverges from operator conflists.
- **Per-worker netns nftables (`setns` into each worker)** vs **one host subnet-keyed table.** Host-level wins: applied once *before* any worker exists (no race window between CNI-up and rule-apply — the fail-closed property), covers all present + future workers, and is far less work than entering every netns. Rejected per-worker: a real fail-open window and N× the churn.
- **Shell out to `nft -f`** vs **`google/nftables` netlink.** Netlink wins: no runtime binary to provision, atomic transactions, type-safe rule construction. Rejected shell: adds a provisioned dependency and fragile string rules.
- **A CNI ACL/firewall plugin** vs **funcd-owned nftables.** funcd-owned wins: the redirect target is an **in-process Go goroutine** (F81) and the decision is the funcd PDP — no CNI plugin does app-level redirect into the daemon. The existing `firewall` plugin stays for the host↔container baseline; funcd's table sits above it.
- **`REDIRECT` (TCP, `SO_ORIGINAL_DST`)** vs **`TPROXY`** for V1. REDIRECT wins: TCP covers HTTP/HTTPS/Postgres (everything the data-platform needs) with no `IP_TRANSPARENT`/policy-routing machinery. TPROXY (UDP, fully transparent) is deferred.
- **On-by-default default-deny** vs **opt-in.** Opt-in wins (decider's call): default-deny egress is a breaking change for any workload that currently reaches out; phased opt-in matches the FEAT-0006 posture. Rejected on-by-default: breaks existing deployments on upgrade and couples F80's usefulness to F81 shipping.

## Decision

Add an **`internal/network`** package with a `Manager` port and two drivers — a **Linux `google/nftables`** driver and a **no-op** driver (non-Linux or disabled). When `server.network.egress` is **true** on a Linux host, the composition root calls `Manager.Apply` once after the runtime is up (before workers serve) and `Manager.Remove` at shutdown.

`Apply` programs **two nftables objects** over the funcd0 worker subnet, split by packet path (L2 vs L3) — this split is load-bearing (see Constraints):

**A. Lateral deny — table `funcd_lateral` (family `bridge`).** Same-subnet worker↔worker traffic is L2-switched by funcd0 and never reaches the L3 (`inet`) hooks, so it is filtered in the **bridge** family: a chain hooked at bridge `forward` **drops** any frame whose source *and* destination are both funcd0 worker IPs. A worker therefore cannot reach a sibling worker directly (fn→fn goes through the local-API/facades, never raw packets).

**B. L3 egress policy — table `funcd_egress` (family `inet`).** Rules are evaluated top-down and **every accept precedes the default-deny** (this ordering is explicit — the accepts must come first or internal services, which live on the host, are dropped):
- **`nat` chain, hook `prerouting`** (prio `dstnat`): **`REDIRECT`** worker-source **external TCP** — destination *not* in the internal allowlist and *not* the DNS resolver — to `GatewayPort`. With no F81 gateway listening the target is dead → the connection is **refused** (fail-closed).
- **`filter` chain, hook `forward`, policy `drop`**: (1) **accept** worker → each internal-service endpoint (`InternalAllow`, worker-reachable addrs — see Contracts); (2) **accept** worker → the configured DNS resolver (`DNSResolver`, scoped — *not* any `:53`) so name resolution works (F81 later swaps this for the correlating forwarder). Everything else — worker→host, worker→LAN, un-redirected external — falls through to the **`drop`** policy (default-deny).
- **`filter` chain, hook `input`**: **accept** the REDIRECT'd packets now destined to the local `GatewayPort`.

The CNI netns/veth/`funcd0` bridge is unchanged (still CNI's job); F80 adds only these two policy objects and reads the subnet from `containerd.Config.SubnetCIDR`. Disabled (default) or non-Linux → `Apply`/`Remove` are no-ops and egress stays exactly as today.

## Temporary workarounds

- **DNS reaches the configured node resolver directly (scoped to that resolver), un-correlated.** F80 accepts worker→`DNSResolver:53` so names resolve, but with no forwarder it cannot yet tie a resolved IP back to a domain for raw-TCP policy — so a resolver-scoped DNS channel remains open while F80 is alone. *Exit:* F81 ([ADR-0116](0116-egress-policy-enforcement.md)) adds the DNS forwarder and swaps this accept for a redirect into it.
- **`REDIRECT`/TCP only** — UDP and fully-transparent capture are absent. *Exit:* a later ADR moves rule 4 to `TPROXY` when a UDP/raw-transparency need is concrete.

## Contracts

### Go port + drivers (`internal/network`)

```go
package network

import (
	"context"
	"net/netip"
)

// Manager programs the host-level worker-egress isolation substrate (F80). It is enabled opt-in;
// disabled or on a non-Linux host every method is a no-op. Apply is idempotent — it is called once
// at daemon start (before any worker serves) and reconciles the funcd_egress table to Policy; Remove
// tears it down at shutdown (safe if never applied).
type Manager interface {
	Apply(ctx context.Context, p Policy) error
	Remove(ctx context.Context) error
}

// Policy is the static isolation frame F80 programs. Behavioral egress *decisions* (allow/deny a
// destination) are F81's — this only frames default-deny + the internal allowlist + the redirect hook.
type Policy struct {
	// WorkerSubnet is the funcd0 host-local IPAM subnet (from containerd.Config.SubnetCIDR); rules
	// match it as the source.
	WorkerSubnet netip.Prefix
	// GatewayPort is where remaining external TCP is REDIRECTed (F81's egress gateway). A zero port
	// is rejected (fault.Invalid) when enabled — the redirect must have a target.
	GatewayPort uint16
	// InternalAllow are funcd node-private services a worker may reach directly (S3 frontend,
	// ingress/catalog) — passed, never redirected. These are the WORKER-REACHABLE endpoint addresses
	// (the funcd0-gateway-IP:port a service's injected Endpoint resolves to), NOT the service's
	// 127.0.0.1 listen address (which is the worker's own netns loopback and unreachable).
	InternalAllow []netip.AddrPort
	// DNSResolver is the single node resolver a worker may reach on :53 — the accept is scoped to
	// this address (not any :53), so it is not an open exfil channel.
	DNSResolver netip.AddrPort
}

// New returns a Manager: the google/nftables driver when enabled on Linux, else a no-op. network.go
// is un-tagged and delegates to a build-tagged internal constructor — newDriver() in nftables_linux.go
// (google/nftables) vs nftables_other.go (no-op) — so New compiles on every GOOS.
func New(enabled bool) Manager
```

- Linux driver: `internal/network/nftables_linux.go` (build tag `linux`) over `github.com/google/nftables`.
- No-op driver: `internal/network/nftables_other.go` (`!linux`) + the `enabled==false` path — both program nothing, return nil.

### Dependencies & I/O

| Consumes | From |
|---|---|
| worker subnet CIDR | the containerd runtime's `subnetCIDR` (ADR-0056) |
| `server.network.egress` (bool, default `false`) | funcdconfig ([ADR-0062](0062-config-single-env-validated-struct.md)) |
| `server.network.egressGatewayPort` (uint16) | funcdconfig (F81's gateway listen port) |
| internal-service allowlist (worker-reachable endpoint addrs) | the S3-frontend + ingress **Endpoint** addresses (the funcd0-gateway-IP:port workers actually dial — not the 127.0.0.1 listen addrs) |
| DNS resolver address | the node resolver (`/etc/resolv.conf` / config) — scopes the DNS accept |
| `github.com/google/nftables` (Apache-2.0) · `github.com/mdlayher/netlink` (MIT) | `go get` |

| Exposes | To |
|---|---|
| the `funcd_egress` nftables table (default-deny + internal-allow + redirect) | the kernel / the F81 gateway (its redirect target) |
| `Manager.Apply`/`Remove` | the composition root (`pkg/funcd`) |

No CRD in this ADR — `EgressPolicy` behavioral fields are **F81's** ([ADR-0116](0116-egress-policy-enforcement.md)); F80 is config-flag-gated infra.

## Implementation plan

- **Files:** `internal/network/network.go` (the `Manager` port + `Policy` + `New`), `internal/network/nftables_linux.go` (the google/nftables driver — build the table/chains/rules, one atomic `Flush`; `Remove` deletes the table), `internal/network/nftables_other.go` (no-op), `internal/network/network_test.go`. Config: add `server.network.egress` + `server.network.egressGatewayPort` to [internal/platform/config/config.go](../../internal/platform/config/config.go). Wire `Manager.Apply`/`Remove` into `pkg/funcd` (Linux, enabled) around the runtime lifecycle.
- **Deps:** `go get github.com/google/nftables` (pins `mdlayher/netlink`); record resolved versions.
- **Test plan** (one named test per Scenario):
  - Unit / cross-platform: `disabled-passthrough` (New(false) → no-op), `non-linux-noop` (the `!linux` driver programs nothing), `ruleset-programmed` (the Linux driver's **rule-builder** — factored to build the `[]*nftables.Rule` for a `Policy` **without applying** — produces the expected default-deny + internal-allow + DNS + redirect set; assert against the built expressions), `teardown-clean` (Remove targets the `funcd_egress` table).
  - **Deferred to a Linux containerd (Lima) e2e lane** (real kernel + netns, per the roadmap test-sequencing note): `lateral-denied`, `external-redirected-failclosed`, `internal-service-allowed`, `dns-resolves` — assert, with the manager enabled and **no** F81 gateway, that a worker's external TCP is refused, a sibling-worker packet is dropped, an internal-service address passes, and DNS resolves. Record the deferral.
- **Definition of done:** `go build`/`vet`/`test`/`golangci-lint`/`go mod verify` green; the rule-builder + no-op paths unit-tested and passing; the Linux integration scenarios deferred + recorded; config flag defaults `false`; `google/nftables` added + pinned; `just ci` green after commit.

## Review checklist

- [ ] `server.network.egress` defaults **false**; disabled ⇒ no table programmed (open egress unchanged).
- [ ] Non-Linux / disabled ⇒ `Apply`/`Remove` no-op, return nil, program nothing.
- [ ] nftables driven via **`google/nftables` netlink** — no `nft` binary, no cgo.
- [ ] The **CNI conflist is not modified** (never-clobber honored); F80 only adds the `funcd_egress` table.
- [ ] The programmed ruleset matches the Contracts: default-deny lateral, internal-allow, DNS-allow, external-TCP redirect to `GatewayPort`, else drop.
- [ ] Lateral deny is a **`bridge`-family** chain (not an `inet`/L3 rule) — same-bridge worker↔worker frames are actually dropped, not silently passed.
- [ ] In the `inet` table the internal + DNS **accepts precede** the `drop` policy (internal services stay reachable).
- [ ] The DNS accept is **scoped to `DNSResolver`** (not any `:53`).
- [ ] `InternalAllow` holds **worker-reachable endpoint** addresses (not `127.0.0.1` listen addrs).
- [ ] `New` compiles on non-Linux (build-tagged `newDriver()` dispatch).
- [ ] `GatewayPort == 0` when enabled ⇒ `fault.Invalid` (no dangling redirect).
- [ ] `Remove` deletes the table (no orphaned rules on shutdown).
- [ ] Typed surface — `netip` types, no `any`/`map[string]any` in the port; `ctx` first.
- [ ] Every Scenario has a named test (Linux-integration ones deferred + recorded, not skipped silently).

## Consequences

- **Positive:** the fail-closed L3/L4 substrate every egress decision needs; opt-in ⇒ zero upgrade breakage; pure-Go netlink keeps the single static binary; subnet-keyed + applied-once closes the fail-open window; internal services stay on their own PEP (no double-governance, no proxy hop).
- **Negative / accepted:** Linux/containerd only (process runtime no-op); **F80 enabled but F81 absent ⇒ all external egress is denied *except* DNS to the configured resolver** (a resolver-scoped interim channel until F81's forwarder; the substrate is only *useful* once F81 lands — acceptable: opt-in means no one enables it expecting open egress); the single-`funcd0`-subnet assumption defers multi-bridge/multi-node.
- **Risks:** if an operator ships a custom conflist with a different subnet/bridge, funcd must derive the **actual** subnet from the runtime (it reads `subnetCIDR`, not a hard-coded value) — covered by taking `WorkerSubnet` from the runtime.

## Open questions

- **src-IP → workload `Ref` mapping** (how the gateway identifies the caller) — answered in [ADR-0116](0116-egress-policy-enforcement.md) (F81).
- **DNS forwarder + raw-TCP domain correlation; TPROXY/UDP** — F81 / a later ADR.
- **Multi-node / multi-bridge egress** — FEAT-0002 (V2 hardening).

## References

- blueprint.md — *Network manager (egress control)*, *Security → Egress control*.
- [github.com/google/nftables](https://github.com/google/nftables) — Apache-2.0, pure-Go netlink (no cgo, no `nft`); [mdlayher/netlink](https://github.com/mdlayher/netlink) — MIT. Verified 2026-07-09.
- [ADR-0056](0056-temporary-runtime-self-provisioning.md) (CNI conflist self-provisioning, never-clobber) · [ADR-0032](0032-curated-runtime-images-container-execution.md)/[ADR-0011](0011-worker-runtime-port.md) (containerd + funcd0) · [ADR-0074](0074-cedar-authorization-resource-access.md) (the PDP, F81) · [FEAT-0007](../feat/0007-feat-egress-control.md).
