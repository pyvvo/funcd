# ADR-0117: Egress policy enforcement — the in-binary egress gateway PEP (F81)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0175](0175-engine-is-its-own-principal.md) (2026-10-05) — §4(b) namespace permit gains principal is Function; the §5 Ref and the WorkerIndex carry the owner kind.
- **Date**: 2026-07-09
- **Accepted**: 2026-07-09
- **Implemented**: 2026-07-10
- **Deciders**: green-0-rabbit
- **Acceptance note (re-judge)**: no Blockers; 3 Majors + 3 Minors folded — **M1** (the JSON-EST-over-`ast` rationale was a strawman: both are injection-safe, only `fmt.Sprintf` isn't — reframed honestly, EST kept as the decider's chosen declarative-document mechanism); **M2** (the wildcard-token injector had no dependency — the `Forwarder` now takes a `WildcardPatterns` source + `WorkerIndex` so a wildcard rule stays an exact set-membership check); **M3** (the "`entities.go` untouched" claim contradicted M2's one `resourceUID` case — narrowed to the registry *assembly* `EntitiesFor`/schema-vocabulary/built-ins, one mapper case + one `schema.go` const being the sole driver edits). Minors: admission cap numbers pinned (64/64/32); `rp_filter` de-emphasised (not relied on — F80 lateral-deny + veth binding carry source-IP unforgeability); F81 feat-row reconciled on this acceptance.
- **Tags**: egress, network, security, pep, cedar, capability, dns, network-manager
- **Design note (pre-commit revision)**: an earlier self-accepted draft of this ADR compiled the `EgressPolicy` to Cedar **text** with **one permit per (function × rule)**. Before any of it was committed the decider redirected the compile model, so this uncommitted Draft is revised in place (not superseded — nothing landed): (a) compile via the **Cedar JSON EST** (`Policy.UnmarshalJSON`), injection-safe by construction, not `fmt.Sprintf`/text-templating; (b) **collapse to one permit per *rule*** scoped by the principal's **existing `namespace` attribute** (`principal.namespace == "<ns>"` for a whole-namespace policy; a bounded `principal == Function::…` disjunction for named `appliesTo`) — so the compiled set is O(rules), independent of the namespace's Function count, using **no Cedar group entity, no principal-parent materialization, and no edit to the ADR-0116 registry *assembly*** (`EntitiesFor` / the schema-vocabulary assembly / the built-in set are unchanged; the only request-path edits are the one `resourceUID` `KindNetDestination` case + a `NetDestination` entity-type const in `schema.go` — M2); (c) a **bounded, atomic, revision-keyed policy cache** + **admission input caps** so compiled-policy memory has a hard ceiling while `Authorize` stays a lock-free pre-compiled evaluation. The prior judge's **B1** (DNS-forwarder domain trust anchor), **M2** (one `resourceUID` `KindNetDestination` case), and **M3** (destination on the string `Path`) findings are retained below; **M1** is refined by the collapse + bounded-cache model (still an additive `PolicySource` extension, now with the cache-growth answer made explicit).
- **Realizes**: [FEAT-0007/F81](../feat/0007-feat-egress-control.md) — egress policy enforcement: govern every outbound worker connection against its namespace's `EgressPolicy`, evaluated by the `auth.Authorizer` PDP and enforced by a transparent in-binary gateway.
- **Relates to**: [ADR-0115](0115-worker-network-isolation.md) (F80 — the L3/L4 default-deny substrate that REDIRECTs remaining external worker TCP to this gateway; F80's DNS-accept workaround exits here), [ADR-0116](0116-capability-authorization-framework.md) (F84 — the capability registry this registers egress on, *without* editing shared assembly; egress is its first *new* consumer), [ADR-0074](0074-cedar-authorization-resource-access.md) (the Cedar PDP + `EgressPolicy`→Cedar precedent), [ADR-0113](0113-edge-authn-pep.md) (F77 — the ingress authn PEP this is the outbound mirror of), [ADR-0032](0032-curated-runtime-images-container-execution.md)/[ADR-0011](0011-worker-runtime-port.md) (the containerd worker + funcd0 IPs the identity map is keyed on).

## Context & Need

F80 ([ADR-0115](0115-worker-network-isolation.md), Implemented) put worker egress behind a **default-deny nftables substrate**: lateral worker↔worker is dropped, internal node services pass directly, and remaining external worker TCP is **REDIRECTed to `GatewayPort`** in the host netns. Today that redirect has **no listener** — so F80-enabled egress is *fail-closed but useless*: everything external is refused. F80 also still lets worker DNS reach the node resolver directly, un-correlated.

**Purpose.** F81 builds the **enforcement point** F80 redirects into: a **transparent egress gateway** embedded in the funcd binary that, per redirected connection, recovers the original destination, identifies the calling workload, resolves the destination **domain**, asks the **PDP** whether that `Function` may `egress::connect` to that `NetDestination`, and **splices** the connection on ALLOW / **refuses** it on DENY — **auditing every attempt**. It also adds the **DNS forwarder** (F80's named exit) so domain policy applies to raw-TCP connects, and delivers the **`EgressPolicy` CRD** (F80 left `EgressPolicySpec` a `struct{}`) as an **allow-list that compiles to Cedar** on the [ADR-0116](0116-capability-authorization-framework.md) registry. It is **default-deny, fail-closed**, **opt-in** (live only when `server.network.egress` is on), and **Linux/containerd only**. Callers: every outbound connection a worker opens; the composition root wires the gateway + forwarder when egress is enabled.

This is the exact mirror of the F77 ingress authn PEP ([ADR-0113](0113-edge-authn-pep.md)): **enforce, never decide** — the gateway authenticates the workload by source IP and delegates the allow/deny to `auth.Authorizer`.

## Scenarios

Each becomes a named acceptance test. The behavioral ones need a real kernel + netns (a containerd Lima lane, deferred per the test plan); the CRD/compile/registration ones are unit-level cross-platform.

- `no-egresspolicy-denies-all` — Given a namespace with **no** `EgressPolicy`, When a worker opens an external connection, Then it is **denied and audited** — zero connectivity (the confidential default). [Lima]
- `allowlist-permits-declared-domain` — Given an `EgressPolicy` allowing `api.x.com:443`, When a worker resolves `api.x.com` via the forwarder and connects to that resolved IP:443, Then it is **allowed and audited**; a connect to any other domain/port is **denied**. [Lima]
- `sni-spoof-denied` — Given the allow-list above, When a worker connects to an **unrelated IP** (never forwarder-resolved for `api.x.com`) while asserting `SNI=api.x.com`, Then it is **denied and audited** — the asserted SNI has no forwarder-attested record for that IP, so no domain permit fires (the B1 invariant). [Lima]
- `blocked-connection-audited` — Given a denied connect, Then the gateway emits an **audit record** to the funclog channel and returns a descriptive **`403`** (HTTP) or **RST/close** (raw TCP). [Lima]
- `confidential-provably-local` — Given a workload whose namespace has an empty/absent `EgressPolicy`, Then it reaches **nothing external** — the *"a confidential byte never leaves the box"* invariant is an enforced property. [Lima]
- `sibling-and-internal` — Given F80+F81 enabled, When a worker targets a **sibling worker** it is **blocked** (F80 lateral-deny), and When it targets an **internal S3/catalog** endpoint it is **reachable** (F80 internal-allow) — the substrate re-asserted end-to-end. [Lima]
- `egresspolicy-compiles-to-cedar` — Given an `EgressPolicy` allowing `*.x.com:443` + `10.0.0.0/8:5432` for `appliesTo: [etl]`, When it is compiled (via the JSON EST), Then it yields the expected `egress::connect` permits as **synthetic `v1.Policy` Cedar text** — `resource.domains.contains("*.x.com")` + `resource.ip.isInRange(ip("10.0.0.0/8"))`, `resource.port` guards, principal scoped by a **bounded `principal == Function::"…/etl"` disjunction**; a domain/CIDR string is a JSON value node (no syntax injection — a `");"`-laden domain compiles inertly or is rejected, never breaks out). [unit]
- `egress-collapse-is-function-count-independent` — Given a **whole-namespace** `EgressPolicy` (`appliesTo` empty) with N rules, When it is compiled against a namespace of K Functions, Then it yields exactly **N permits** each scoped `principal.namespace == "<ns>"` (independent of K) — the O(rules) memory property, using no group entity and no edit to `entities.go`. [unit]
- `egresspolicy-caps-reject-oversize` — Given an `EgressPolicy` exceeding a cap (too many rules / destinations-per-rule / ports-per-rule), Then `Validate` rejects it (`fault.Invalid`) — the compiled-set memory ceiling is enforced at admission. [unit]
- `egress-capability-registered` — Given the egress `Capability` registered on the ADR-0116 registry, Then `egress::connect` + `NetDestination` are **Known** in the assembled schema vocabulary and a request materializes a `NetDestination` resource with the **one** `resourceUID` `KindNetDestination` case added — **with no edit to the shared schema / EntityProvider / built-in *assembly* (the registry)**. [unit]
- `egresspolicy-validate` — Given an `EgressPolicy` with a bad domain / malformed CIDR / out-of-range port / a rule missing `to`, Then `Validate` rejects it (`fault.Invalid`). [unit]
- `disabled-passthrough` — Given `server.network.egress` **off** (default), Then no gateway/forwarder is wired and egress is open exactly as today. [unit]

## Scope

**In:** an **`internal/network/egress`** package — a `Gateway` port (`Serve(ctx)`/`Close`) with a **Linux** driver (`SO_ORIGINAL_DST` recovery, SNI/Host peek, splice) and a **no-op** driver (mirroring F80's `internal/network` linux/other split); a **DNS forwarder** that F80's nftables redirects `:53` into (forward + record `worker → domain→IP`); the **`EgressPolicy` CRD** (flesh `EgressPolicySpec`); the **egress `Capability`** + the **`EgressPolicy`→Cedar compiler** (to synthetic `v1.Policy` text) and the reconcile/admission step that lists it alongside `KindPolicy` for the PDP; a new `egress::connect` action + `NetDestination` entity type + the one `resourceUID` case; the **IP→worker identity map** seam the containerd runtime populates; the config/wiring (reuse `server.network.egressGatewayPort`, add `server.network.dnsForwarderPort`); flip F80's DNS accept to a redirect into the forwarder.

**Out (named follow-ons):**
- **Full-URL / path-level TLS policy (MITM)** — V1 is **SNI-domain** granularity (no interception); per-path rules need a per-namespace CA (breaks cert pinning) — FEAT-0002.
- **`TPROXY` / UDP** — V1 inherits F80's `REDIRECT` + `SO_ORIGINAL_DST` (TCP: HTTP/HTTPS/DB); TPROXY is a later refinement.
- **The kernel `connect()` seccomp tier** (blueprint tier 4) — the gateway is the V1 enforcement floor.
- **Multi-node / multi-bridge egress** — single funcd0, single node (FEAT-0002).
- **The runtime-shim HTTP interception** (blueprint tier 2) — a courtesy `403` layer *above* this floor, not decided here.

## Constraints & Decision drivers

- **Enforce, never decide** ([ADR-0113](0113-edge-authn-pep.md)/[ADR-0074](0074-cedar-authorization-resource-access.md)) — the gateway authenticates the workload (source IP → `Ref`) and calls `auth.Authorizer.Authorize`; the `EgressPolicy` (compiled to Cedar) is the decision, not gateway code.
- **Connection-scoped principal** — the caller is the **source IP** of the redirected connection, resolved to `(namespace, function)` via the runtime's IP→worker map — **never** client-asserted (mirrors the F77 PEP and ADR-0075). The source IP is unforgeable here: F80's lateral-deny ([ADR-0115](0115-worker-network-isolation.md)) + the CNI veth/bridge-port binding pin a worker's netns to its funcd0 IP, so a worker cannot present another worker's IP (host reverse-path filtering, where enabled, is a further backstop — not relied on by this ADR).
- **The DNS forwarder is the domain trust anchor — SNI/Host is NEVER the authorization basis (the core security invariant).** A **domain** rule authorizes a connection **only** when the connection's real destination IP (`SO_ORIGINAL_DST`) is in the **funcd DNS forwarder's** recorded resolved-IP set for that `(worker, domain)`: the domain→IP binding comes from funcd's own resolver, never from the client's ClientHello SNI or `Host`. Without this, a worker could L4-connect to `IP_evil` while sending `SNI=api.allowed.com` and be spliced to `IP_evil`. SNI/Host is used only as a fast-path hint that **must itself be forwarder-attested** (an SNI matching no forwarder record is ignored). A **CIDR** rule matches the dst IP directly (no domain needed). This makes the forwarder **mandatory for domain policy**, not just raw TCP.
- **Default-deny, fail-closed** — no `EgressPolicy` ⇒ nothing external; gateway down ⇒ F80's redirect has no target ⇒ refused. An `EgressPolicy` is an **allow-list** (implicit deny). SNI-domain granularity only (no MITM).
- **Register, don't edit** ([ADR-0116](0116-capability-authorization-framework.md)) — egress is a `Capability` value + a CRD compiler; the schema/EntityProvider/built-ins **assemble** it with no shared-code change. `EgressPolicy` is the **first high-level typed policy CRD that compiles to the core** (the pattern VolumePolicy etc. follow).
- **Opt-in / phased** — live only when `server.network.egress` is enabled (same posture as F80); off ⇒ pass-through.
- **Linux/containerd only** — `SO_ORIGINAL_DST` + netns are Linux; the process runtime is a no-op.
- **In-binary, no child process** — the gateway is a **goroutine server** (single static binary, blueprint), like the F80 substrate it plugs into.
- **Apache-2.0/MIT-compatible deps** — `github.com/miekg/dns` (BSD-3-Clause, already in the module graph) for the forwarder; `golang.org/x/sys/unix` (BSD-3) for `getsockopt`; cedar-go (already a dep) for the compiler; SNI/Host peek via stdlib `crypto/tls`/`net/http`. Verified 2026-07-09.

## Alternatives considered

| Option | Verdict |
|---|---|
| **In-binary transparent gateway (goroutine) recovering `SO_ORIGINAL_DST`, domain matched against the funcd forwarder's attested resolved-IP set, delegating to the PDP, splicing (chosen).** | The redirect target F80 already built; single binary; enforce-never-decide; forwarder-attested domain policy works for HTTPS/HTTP/DB and is not spoofable by client SNI/Host. |
| **Trust the ClientHello SNI / `Host` as the domain** (match policy directly on the asserted name). | Rejected — client-asserted; a worker connects to `IP_evil` with `SNI=api.allowed.com` and a domain permit would splice to `IP_evil` (exfiltration). The domain must be bound to the dst IP by funcd's own forwarder, not the client. |
| A **sidecar/child-process proxy** (Envoy-style). | Rejected — a provisioned binary + IPC, contradicts the single-static-binary blueprint; the F80 redirect already points into the daemon. |
| **MITM TLS termination** (per-ns CA) for full-URL policy. | Rejected V1 — invasive, breaks cert pinning, needs per-namespace CA management. SNI-domain is the security win now; MITM is FEAT-0002. |
| **Model egress as a fourth Cedar consumer hand-wired** (like pre-ADR-0116 kv/invoke/s3). | Rejected — ADR-0116 exists precisely so egress *registers*; hand-wiring re-introduces the `EntitiesFor` hotspot ADR-0116 dissolved. |
| **`EgressPolicy` as a binding-as-grant `Function.spec` field** (like `spec.kv`). | Rejected — egress is a **namespace** posture governing *all* the namespace's workers, not a per-function opt-in binding; it belongs in a namespaced CRD that compiles to permits, not a spec field. `PrincipalBinding` is therefore **NONE**. |
| **Public reverse-DNS / IP-only rules** for raw TCP. | Rejected — public reverse-DNS is attacker-controlled and IP-only can't express `db.x.com`; funcd's **own forwarder correlation** (Cilium-style: record the resolved IP↔domain per worker) gives trustworthy domain policy for any protocol. |
| **A built-in permit** (always-on grant, like kv-read). | Rejected — egress is default-deny with *no* implicit grant; the grant comes **only** from a compiled `EgressPolicy`. `Builtin` is therefore **""**. |
| **Compile via `fmt.Sprintf` string templating** to Cedar text. | Rejected — it interpolates untrusted domain/CIDR strings into policy *syntax* (Cedar-injection). |
| **Compile via the native cedar-go `ast` verb-builder** (typed nodes) vs the **JSON EST** (`Policy.UnmarshalJSON`). | Both are injection-safe (values are typed/escaped nodes, not syntax). **JSON EST chosen** (decider's call) — a declarative policy *document* in Cedar's own interchange format; trades the `ast` chain's compile-time node type-checking for parse-time validation. Not a security distinction. |
| **One permit per (function × rule)** — enumerate `appliesTo` (or the whole namespace) into per-`Function` permit heads. | Rejected — the compiled set grows **O(functions)** (500 fns × 10 rules = 5 000 permits), unbounded by policy size. The **`principal.namespace`-scoped** collapse is O(rules) and function-count-independent for the common whole-namespace case. |
| **A Cedar group/hierarchy entity** (`principal in Group::"…"`) for `appliesTo`. | Rejected V1 — cleaner in theory, but it needs principal-**parent** materialization, which the *frozen* ADR-0116 framework (attribute Sets only) doesn't provide → a framework extension or a hot-path store `List`. The `namespace`-attribute scope reuses an attribute the principal **already carries** — no framework touch, no `entities.go` edit. |

## Decision

### 1. The egress gateway (`internal/network/egress`)

A `Gateway` goroutine server listening on `GatewayPort` (F80's `egressGatewayPort`) in the host netns. Per redirected connection it: recovers the pre-DNAT `dst-IP:port` (`getsockopt(SO_ORIGINAL_DST)`); resolves the **source IP** to a `(namespace, function)` ref via the `WorkerIndex` (§5, unknown ⇒ deny+audit); builds the **forwarder-attested** destination (below); asks `auth.Authorizer.Authorize` (`egress::connect` over the `NetDestination`); and on **ALLOW** dials the real dst and **splices** (bidirectional `io.Copy`, replaying peeked bytes) or on **DENY** returns a descriptive **`403`** (RFC 9457, HTTP) / **RST-close** (raw TCP/TLS) — **auditing every connection** (allowed + blocked) to the **funclog** channel. Default-deny. (The per-step interfaces are in Contracts.)

**The destination the gateway authorizes is the *resolved* one, not the *asserted* one (the B1 invariant).** The gateway asks the forwarder for the set of **domains this worker resolved to this `dst-IP`** (`DomainsFor(src, dst)`) — this is the trust anchor. It materializes `NetDestination{ip: dst-IP, port, domains: <that forwarder-attested set>}`. The ClientHello **SNI** / HTTP **Host** is peeked only as a *confirmation hint*: it is added to `domains` **only if it is already in the forwarder-attested set**, and otherwise dropped. A raw-TCP connect with no SNI/Host authorizes purely on the attested set (or, if empty, only a **CIDR** rule can permit it by matching `ip`). So a domain permit fires **only** when funcd's own resolver bound that domain to this exact IP for this worker — a spoofed SNI to an unrelated IP has no attested record and is denied.

### 2. The DNS forwarder — mandatory for domain policy

A small resolver funcd runs on `DNSForwarderPort`; F81 flips F80's DNS accept to **REDIRECT worker `:53` into it** (so the forwarder is the *only* reachable resolver — a worker cannot resolve out-of-band). It forwards each query to the node resolver and, on the answer, **records `(worker-src-IP, domain) → resolved-IP set` with a TTL**, and exposes the **reverse** view `DomainsFor(src, dst-IP) → domains` the gateway consumes in §1. For a **wildcard** rule the forwarder/gateway loads the namespace's `EgressPolicy` domain patterns and, when an attested FQDN matches `*.x.com`, also injects the matched **pattern token** `*.x.com` into `domains` — so wildcard authorization stays an exact set-membership check over a *forwarder-derived* token (trust preserved: the FQDN came from the forwarder). This is the **exit** for F80's interim direct-resolver DNS workaround, and it is required for **all** domain policy (not just raw TCP) since domain matching now depends on the forwarder's records.

### 3. The `EgressPolicy` CRD

Flesh `EgressPolicySpec` (F80 left it `struct{}`): **namespaced, allow-list, implicit default-deny**. `spec.rules[]`, each `{ to: { domains []string (SNI/Host match, wildcard `*.x.com`); cidrs []string }, ports []int }`, plus optional `appliesTo []ObjectName` (omit ⇒ the whole namespace). YAML block syntax. `Validate` enforces: valid domains (or `*.`-prefixed wildcard), parseable CIDRs, ports `1–65535`, and **at least a `to`** per rule. A namespace with **no** `EgressPolicy` ⇒ its workers reach **nothing** external.

### 4. Egress as an ADR-0116 `Capability` + the `EgressPolicy`→Cedar compiler

Register `EgressCapability()`: `Actions: [egress::connect]`; `EmitsEntityType: NetDestination`; **`PrincipalBinding` zero (NONE)** — no `spec.<field>` grant; **`Builtin` "" (NONE)** — default-deny. Its `Resource` func materializes a `NetDestination` Cedar entity **from the request `EntityRef`** (attributes `ip`, `port`, `domains`, parsed from the ref's `Path` — §Contracts; **no `MetaReader` read** — the destination is ephemeral, not stored) when `resource.Type == KindNetDestination`.

**One driver request-path edit is required (not zero — M2).** The driver's `resourceUID` switch (`internal/auth/cedar/entities.go`) faults `Internal` for any resource kind it doesn't case, so a `NetDestination` request would fault. Egress adds **one `case v1.KindNetDestination`** to `resourceUID` (its UID byte-matching what `EgressCapability.Resource` emits) — exactly as `KindS3Identity` is a case in the sibling `principalUID`. So the accurate claim is: egress **registers with no edit to the schema / EntityProvider / built-in *assembly* (the ADR-0116 registry)**; the driver's request-path `resourceUID` mapper gains one case, mirroring `S3Identity`.

The **grant** comes from compiling each namespace's `EgressPolicy`(ies) to Cedar. Two properties are load-bearing — **injection-safety** and **bounded memory**:

**(a) Built as a Cedar policy structure, never `fmt.Sprintf`-templated.** The one genuinely unsafe option is **string templating** (`fmt.Sprintf("… contains(\"%s\") …", domain)`) — it interpolates an untrusted `EgressPolicy` domain/CIDR straight into policy *syntax*, so a crafted value could inject a clause (Cedar-injection). It is rejected. Both safe alternatives put every value in a *typed node* the encoder escapes: cedar-go's native `ast` verb-builder (`ast.String(d)`, `ast.IPAddr(pfx)`) and the **Cedar JSON EST** (the documented [JSON policy format](https://docs.cedarpolicy.com/policies/json-format.html), parsed with `Policy.UnmarshalJSON`). **We build via the JSON EST** — the decider's chosen mechanism: a single declarative policy *document* (Cedar's own interchange format) that keeps the compiler a data-shape rather than a fluent verb chain, at the cost of trading the `ast` builder's compile-time node type-checking for parse-time validation (a malformed CIDR is caught at `UnmarshalJSON`, not `go build`). Injection-safety is **equal** to the `ast` builder — the security property is "no `fmt.Sprintf`", not "EST over `ast`". The validated `Policy` is rendered to canonical text with `MarshalCedar` for the synthetic `v1.Policy.Spec.Cedar` (the JSON EST is the *safe construction*; text is the *transport* that rides the existing `PolicySource`→`compile()` path unchanged — M1).

**(b) One permit per *rule*, scoped by the principal's `namespace` attribute — O(rules), not O(functions).** The naïve "one permit per (function × rule)" makes the compiled set grow with the namespace's Function count (500 functions × 10 rules = 5 000 permits). Instead the compiler scopes each rule with the principal entity's **existing `namespace` attribute** (materialized on every principal today — `entities.go`):

- **whole-namespace policy** (`appliesTo` empty — the common case): one `permit(principal, action == Action::"egress::connect", resource) when { principal.namespace == "<ns>" && <port-set> && ( resource.domains.contains("<domain|*.wild>") || resource.ip.isInRange(ip("<cidr>")) ) }` per rule — **independent of Function count**;
- **named `appliesTo`**: the same, with the namespace guard replaced by a disjunction over the *explicit, bounded* list `( principal == Function::"<ns>/<f1>" || … )` — O(len(appliesTo)), an operator-authored short list, never the whole namespace.

Domain/wildcard rules → exact `.contains` over the forwarder-attested `domains` set; CIDR rules → `isInRange`; ports as a set. Crucially this uses **only the `namespace` attribute already present on the principal + the principal UID** — **no Cedar group entity, no principal-parent materialization, and no store `List`**. The ADR-0116 registry *assembly* (`EntitiesFor`, the schema-vocabulary assembly, the built-in set) is **unchanged**; the only request-path additions are the single `resourceUID` `KindNetDestination` case + the `NetDestination` entity-type const in `schema.go` (M2) — so "register, don't edit" holds for the *assembly*, with one mapper case as the sole driver edit (mirroring `principalUID`'s `S3Identity`). It is a **small, additive extension to the ADR-0074 policy pipeline** (M1): the real `PolicySource.Policies(ctx)` yields `[]v1.Policy` of Cedar **text** which `compile()` runs through `cedar.NewPolicyListFromBytes`; the composition root today lists **only** `KindPolicy`. So `CompileEgressPolicy` returns **synthetic `[]v1.Policy`** (one per namespace), the policy source lists them **alongside** `KindPolicy`, and the cache revision incorporates `EgressPolicy` writes *and* the namespace Function-set (a named-`appliesTo` Function add/remove) so a change recompiles. Egress is the **first high-level typed policy CRD that compiles to the core** — the template future typed policy CRDs (VolumePolicy, …) follow.

### 4a. Bounded, fast policy cache + admission caps

The compiled egress policies are **never persisted** — they are a pure function of the persisted `EgressPolicy` CRDs, compiled into the **in-memory PDP cache** (exactly as ADR-0074 compiles user `Policy` CRDs; nothing derived is written back to the metastore). Two guards keep memory bounded while `Authorize` stays fast:

- **Atomic, revision-keyed single-slot cache.** The composition root holds the compiled `cedar.PolicySet` behind an `atomic.Pointer`; `Authorize` `Load()`s it **lock-free** (the compile — parse → evaluable — is paid once at build, so the hot path is evaluation-only). A rebuild constructs a fresh set and swaps it in; the old set is GC'd when the last in-flight reader drops it — **one live version + one transient during build**, no stale accumulation. The revision keys on `max(ResourceVersion)` over `Policy` + `EgressPolicy` + the namespace Function-set; equal revision ⇒ no rebuild.
- **Admission input caps (hard ceiling).** `EgressPolicy.Validate` caps `len(rules) ≤ maxEgressRules (64)`, and per rule `len(domains)+len(cidrs) ≤ maxEgressDests (64)` and `len(ports) ≤ maxEgressPorts (32)`. An oversized policy is **rejected** (`fault.Invalid`), never compiled — so one policy compiles to at most `maxEgressRules` permits, each of bounded size, i.e. the compiled-set size is deterministically bounded by (caps × policies), independent of user behavior.

Together: compiled memory is **O(rules × policies)** with a capped ceiling (never O(functions)), and `Authorize` is a lock-free evaluation over a pre-compiled `PolicySet`.

### 5. The IP→worker identity map (`WorkerIndex`)

A seam the **containerd runtime writes** (worker provisioning already assigns a funcd0 IP via CNI/IPAM): on worker-up it records `funcd0-IP → (namespace, function) Ref`; on worker-down it removes it. The gateway reads it to authenticate the caller (step 2). This is the answer to ADR-0115's parked open question.

### 6. Opt-in, PEP-before-anything, Linux-only

Wired **only** when `server.network.egress` is true on Linux (the same flag that arms F80's substrate) — off ⇒ no gateway/forwarder, egress open as today. The gateway is the **sole** egress PEP; the runtime-shim HTTP `403` layer is a courtesy tier *above* it. The process runtime is a **no-op** (build-tagged, mirroring F80).

## Temporary workarounds

- **SNI-domain granularity (no MITM).** Policy matches at domain granularity (the forwarder-attested name), not the full URL/path. SNI/Host is a confirmation hint, never the authorization basis. *Exit:* a FEAT-0002 ADR adds a per-namespace MITM CA for path-level rules when concretely needed.
- **`REDIRECT` + `SO_ORIGINAL_DST`, TCP only** (inherited from F80). UDP (beyond DNS) and fully-transparent capture are absent. *Exit:* a later ADR moves to `TPROXY` when a UDP/raw-transparency need is concrete.
- **A Cedar group/hierarchy entity for `appliesTo`** is *not* used — the collapse rides the principal's existing `namespace` attribute (whole-namespace) + a bounded explicit disjunction (named `appliesTo`), which needs no principal-parent materialization and no ADR-0116 framework change. A named-`appliesTo` change still keys the cache revision on the Function-set (so an add/remove recompiles). *Exit:* if per-label/selector `appliesTo` (beyond an explicit name list) is ever needed, a follow-on adds a principal label attribute + a `.containsAny` guard — still no group entity.

## Contracts

### `EgressPolicy` CRD (`api/types/v1alpha1/egresspolicy.go` — flesh the stub)

```go
// EgressPolicy is a namespaced, pure-policy allow-list (implicit default-deny) governing worker egress
// (ADR-0117, F81). No status (implements Object, not StatusObject). A namespace with no EgressPolicy ⇒
// its workers reach nothing external.
type EgressPolicy struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       EgressPolicySpec `json:"spec"`
}

// EgressPolicySpec is an allow-list of egress rules, optionally scoped to named Functions.
type EgressPolicySpec struct {
	// Rules is the allow-list; each grants egress to its `to` on its `ports`. At least one required.
	Rules []EgressRule `json:"rules"`
	// AppliesTo scopes every rule to these Functions (by name, this namespace); empty ⇒ the whole namespace.
	AppliesTo []ObjectName `json:"appliesTo,omitempty"`
}

// EgressRule permits egress to a destination set on a port set.
type EgressRule struct {
	To    EgressTo `json:"to"`             // at least one of domains/cidrs (Validate enforces)
	Ports []int    `json:"ports,omitempty"` // 1..65535; empty ⇒ any port to the matched destination
}

// EgressTo is a destination match: SNI/Host domains (wildcard "*.x.com" ok) and/or CIDRs.
type EgressTo struct {
	Domains []string `json:"domains,omitempty"`
	CIDRs   []string `json:"cidrs,omitempty"`
}

func (ep *EgressPolicy) GroupVersionKind() GroupVersionKind { return KindEgressPolicy.GVK() }

// Validate: envelope + each rule has a non-empty `to`; domains are DNS names or "*."-wildcards; CIDRs
// parse (netip.ParsePrefix); ports are 1..65535; AND the admission caps that bound compiled-policy memory
// — len(rules) ≤ maxEgressRules, per-rule len(domains)+len(cidrs) ≤ maxEgressDests, len(ports) ≤
// maxEgressPorts (all fault.Invalid otherwise).
func (ep *EgressPolicy) Validate() error
```

### The egress gateway (`internal/network/egress`)

```go
package egress

// Gateway is the transparent egress PEP: it accepts F80-redirected worker connections on GatewayPort,
// recovers the original destination, identifies the caller, asks the PDP, and splices or refuses —
// auditing every connection. Serve blocks until ctx is cancelled or Close is called.
type Gateway interface {
	Serve(ctx context.Context) error
	Close() error
}

// Deps wires the gateway. On a non-Linux host or when disabled, New returns a no-op Gateway.
type Deps struct {
	GatewayPort uint16          // F80's REDIRECT target (server.network.egressGatewayPort)
	Workers     WorkerIndex     // src-IP → caller Ref (containerd runtime populates it, §5)
	DNS         DomainResolver  // forwarder-attested (worker, dst-IP) → domains (the trust anchor)
	Authz       auth.Authorizer // the PDP (egress::connect over a NetDestination)
	Audit       AuditSink       // every connection (allowed + blocked) → the funclog channel
}

// New returns the Linux driver when enabled on Linux, else a no-op (build-tagged newGateway() dispatch,
// mirroring internal/network's nftables_linux.go / _other.go split).
func New(enabled bool, d Deps) Gateway

// WorkerIndex resolves a worker's funcd0 source IP to its (namespace, function) principal Ref.
type WorkerIndex interface {
	Lookup(ip netip.Addr) (ref auth.EntityRef, ok bool)
}

// DomainResolver returns the domains THIS worker resolved to dst via the funcd forwarder — the authoritative
// domain→IP binding (never client SNI/Host). It includes any namespace wildcard pattern token an attested
// FQDN matched (so wildcard rules stay exact set-membership). Empty ⇒ only a CIDR rule can permit dst.
type DomainResolver interface {
	DomainsFor(src netip.Addr, dst netip.Addr) []string
}

// AuditSink records one egress decision to the funclog audit channel.
type AuditSink interface {
	Egress(ctx context.Context, rec AuditRecord)
}
type AuditRecord struct {
	Namespace, Function, Domain string
	IP                          netip.Addr
	Port                        uint16
	Allowed                     bool
	Reason                      string
}

// Linux-only building blocks (build tag `linux`), each with a no-op sibling under `!linux`:
//   originalDst(conn *net.TCPConn) (netip.AddrPort, error)      // getsockopt SO_ORIGINAL_DST
//   peekSNI(c net.Conn) (serverName string, replayed net.Conn, ok bool) // passive ClientHello SNI
//   peekHost(c net.Conn) (host string, replayed net.Conn, ok bool)      // plaintext HTTP Host
//   splice(a, b net.Conn) error                                  // bidirectional io.Copy
```

### The DNS forwarder (`internal/network/egress`)

```go
// Forwarder is the ONLY resolver a worker can reach (F80 redirects worker :53 into it). It forwards to the
// node resolver and records (src, domain)→resolved-IP set, exposing the reverse DomainsFor the gateway uses
// to authorize domain rules. On each answer it also asks WildcardPatterns for the resolving worker's
// namespace patterns and records every "*.x.com" the attested FQDN matched, so a wildcard rule stays an
// exact set-membership check over a forwarder-derived token. It satisfies DomainResolver. Serve blocks
// until ctx is cancelled.
type Forwarder interface {
	Serve(ctx context.Context) error
	DomainResolver
}

// WildcardPatterns supplies a namespace's EgressPolicy wildcard domain patterns (e.g. "*.x.com") so the
// forwarder can inject a matched pattern token — the dependency the wildcard mechanism needs (it is derived
// from the same compiled EgressPolicy set the PDP cache holds; nil ⇒ no wildcard tokens, exact domains only).
type WildcardPatterns interface {
	Wildcards(ns v1.NamespaceName) []string
}

// NewForwarder builds it over the node resolver (github.com/miekg/dns; BSD-3-Clause). workers maps the query
// source IP → namespace (to select patterns); patterns supplies that namespace's wildcards (both may be nil
// on a deployment with no wildcard rules, in which case only exact attested FQDNs are recorded).
func NewForwarder(listen, upstream netip.AddrPort, workers WorkerIndex, patterns WildcardPatterns) Forwarder
```

### The egress capability + compiler (`internal/auth/cedar`)

```go
// EgressCapability registers egress on the ADR-0116 registry: Action egress::connect; resource entity
// NetDestination materialized from the request EntityRef's Path (no MetaReader read — the destination is
// ephemeral); NO PrincipalBinding (governed by EgressPolicy, not a spec field); NO Builtin (default-deny —
// grants come only from compiled EgressPolicies). Its Resource emits UID + attributes {ip, port, domains};
// the driver's resourceUID gains a matching KindNetDestination case (see entities.go, M2).
func EgressCapability() Capability

// CompileEgressPolicy compiles one namespace's EgressPolicy into SYNTHETIC v1.Policy objects (Spec.Cedar),
// listed by the policy source alongside user KindPolicy and compiled by the existing compile()/
// NewPolicyListFromBytes path — NOT a pre-built PolicySet. ONE permit per rule (domains/wildcards via
// resource.domains.contains, CIDRs via resource.ip.isInRange, ports as a set). Each permit is built as a
// Cedar JSON EST document and parsed with cedar-go Policy.UnmarshalJSON (injection-safe: a domain/CIDR
// string is a JSON value node, never interpolated syntax; a malformed CIDR is an error), then rendered to
// canonical text via MarshalCedar for Spec.Cedar. The principal scope is the entity's `namespace` attribute
// (`principal.namespace == "<ns>"`) for a whole-namespace policy, or a bounded disjunction over the explicit
// spec.appliesTo list (`principal == Function::"<ns>/<fn>"`) — O(rules), independent of the namespace
// Function count, using NO Cedar group entity and NO edit to the registry assembly. `funcs` is the namespace
// Function-set (drives the named-appliesTo membership check AND the cache revision).
func CompileEgressPolicy(ns v1.NamespaceName, ep *v1.EgressPolicy, funcs []v1.ObjectName) ([]v1.Policy, error)
```

### New action + entity type (`internal/auth`, `api/types/v1alpha1`)

```go
const ActionEgressConnect auth.Action = "egress::connect"  // internal/auth/authorizer.go
const KindNetDestination  v1.Kind      = "NetDestination"  // authorization-only kind (EXACT precedent:
// KindS3Identity — no metastore registration, excluded from Kind.Validate's CRUD set / NewObject / AllKinds).
// KindNetDestination REINTERPRETS the EntityRef fields (as S3Identity does for a principal): the destination
// rides the plain-string Path — PINNED encoding Path = "<dst-ip>:<port>#<domain1>,<domain2>,…" (the "#…"
// domain segment is the forwarder-attested set, empty for a pure-IP/CIDR target); Name/Namespace are unused
// (Name is v1.ObjectName = a single DNS-1123 label, so it CANNOT hold "api.x.com"/"*.x.com"/"10.0.0.1").
// resourceUID uses only the "<dst-ip>:<port>" prefix (before '#') so the UID is stable; EgressCapability.
// Resource parses the same Path and MUST agree byte-for-byte. cedar entityTypeNetDestination mirrors it.
```

### Dependencies & I/O

| Consumes | From |
|---|---|
| the F80 REDIRECT (external worker TCP on `GatewayPort`) + the DNS redirect (`:53`) | [ADR-0115](0115-worker-network-isolation.md)'s nftables table (F81 flips the DNS accept to a redirect) |
| `server.network.egress` (bool) · `server.network.egressGatewayPort` (uint16, reused) · `server.network.dnsForwarderPort` (uint16, **new**) | funcdconfig ([ADR-0062](0062-config-single-env-validated-struct.md)) |
| src-IP → `(namespace, function)` Ref | the containerd runtime's worker provisioning (`WorkerIndex`, §5) |
| `auth.Authorizer` (the PDP) · the ADR-0116 `Registry` (schema/entities/built-ins) | `internal/auth`, `internal/auth/cedar` |
| the node resolver address | `/etc/resolv.conf` / config (the forwarder upstream) |
| `github.com/miekg/dns` (BSD-3) · `golang.org/x/sys/unix` (BSD-3) — both already in the module graph | `go get` (promote to direct) |

| Exposes | To |
|---|---|
| `Gateway.Serve`/`Close`, `Forwarder.Serve` | the composition root (`pkg/funcd`), wired when egress is enabled on Linux |
| `EgressCapability()` + `CompileEgressPolicy` | the cedar registry (registration) + the `EgressPolicy` reconcile/admission |
| a per-connection audit record | the funclog audit channel |
| the fleshed `EgressPolicy` CRD | the API server / `funcdctl` |

## Implementation plan

**Files:**
- `internal/network/egress/gateway.go` — the `Gateway` port + `Deps` + `New` (build-tagged dispatch) + `WorkerIndex`/`DomainResolver`/`AuditSink` interfaces + `AuditRecord`.
- `internal/network/egress/gateway_linux.go` (`linux`) — the accept loop, `originalDst` (`x/sys/unix` `SO_ORIGINAL_DST`), `peekSNI`/`peekHost` (replaying conn), `splice`, decision + audit.
- `internal/network/egress/gateway_other.go` (`!linux`) + the `enabled==false` path — no-op.
- `internal/network/egress/forwarder.go` — the `Forwarder` (`miekg/dns`) + the correlation map (`DomainResolver`).
- `api/types/v1alpha1/egresspolicy.go` — flesh `EgressPolicySpec`/`EgressRule`/`EgressTo` + `Validate`.
- `internal/auth/authorizer.go` — `ActionEgressConnect`; `api/types/v1alpha1/metadata.go` — `KindNetDestination` (authorization-only, mirroring `KindS3Identity`; NOT in the served-kind lists).
- `internal/auth/cedar/capabilities.go` — `EgressCapability()` + `netDestinationResource`; `internal/auth/cedar/entities.go` — **add one `case v1.KindNetDestination` to `resourceUID`** (UID = the Path's `<ip>:<port>` prefix, byte-matching the capability's Resource); `internal/auth/cedar/egress_compile.go` — `CompileEgressPolicy` (returns synthetic `[]v1.Policy`).
- the `EgressPolicy` reconcile/admission hook that compiles on write; the **`PolicySource`** (`internal/auth/cedar/policies.go` consumer in `pkg/funcd`) lists the synthetic egress policies **alongside `KindPolicy`**, and the **policy-cache revision incorporates `EgressPolicy` + the namespace Function-set** (so a write or an `appliesTo`-affecting Function change recompiles) — a small additive extension to ADR-0074's list + revision (`pkg/funcd/funcd.go` ~1077-1090).
- `internal/platform/config/config.go` — add `server.network.dnsForwarderPort`.
- `pkg/funcd` — register `EgressCapability()` on the cedar `Registry`; on Linux+enabled, start the `Gateway` + `Forwarder` and populate the `WorkerIndex` from the containerd runtime; flip F80's DNS rule to the forwarder redirect.

**Deps:** `go get github.com/miekg/dns` (promote to direct; BSD-3-Clause); `x/sys/unix` already present. Record resolved versions.

**Test plan** (one named test per Scenario):
- **Unit / cross-platform:** `egresspolicy-validate` (bad domain/CIDR/port, missing `to`); `egresspolicy-caps-reject-oversize` (rules/dests/ports over the caps ⇒ `fault.Invalid`); `egresspolicy-compiles-to-cedar` (assert the JSON-EST-built `egress::connect` permits — `resource.domains.contains`, CIDR `isInRange`, port set, named-`appliesTo` `principal == Function::…` disjunction; **plus an injection probe**: a `");"`-laden domain compiles inertly, never breaking policy syntax); `egress-collapse-is-function-count-independent` (whole-namespace policy ⇒ N permits scoped `principal.namespace == …`, independent of the K-Function set); `egress-capability-registered` (register `EgressCapability()` on the registry; assert `egress::connect`/`NetDestination` Known and a `NetDestination` request materializes — **grep-check no edit** to the shared assembly / `entities.go`); `disabled-passthrough` (`New(false)`/no-op ⇒ nothing wired). The compiler + a fake PDP prove `no-egresspolicy-denies-all` and `allowlist-permits-declared-domain` at the **decision** level cross-platform; a cache test asserts the **atomic revision-keyed** swap (same revision ⇒ no rebuild; a change ⇒ new set, lock-free `Load`).
- **Deferred to a NEW containerd Lima e2e lane — `e2e/egress.venom.yml`** (authored now; run deferred): boot the containerd VM with `server.network.egress` on, deploy a probe function + an `EgressPolicy`, assert **external-denied-without-policy** (`no-egresspolicy-denies-all` / `confidential-provably-local`), **allowed-with-policy** (`allowlist-permits-declared-domain`), **`sni-spoof-denied`** (asserted SNI to an unattested IP ⇒ deny), **blocked-audited** (`blocked-connection-audited` — a funclog record + `403`/RST), and **`sibling-and-internal`** (sibling blocked, internal S3/catalog reachable). This is the **F80+F81 behavioral coverage** (F80's Lima-deferred scenarios land here too). Record the deferral.

**Definition of done:** `go build`/`vet`/`test`/`golangci-lint`/`go mod verify` green; the unit scenarios pass; the Lima behavioral scenarios authored in `e2e/egress.venom.yml` + **run green on the containerd Lima lane** (per the batch's "e2e in go and finally in venom" mandate); `EgressPolicy.Validate` (incl. caps) + the JSON-EST compiler (synthetic `[]v1.Policy`, `principal.namespace`-scoped collapse) + the capability registration + the `resourceUID` case + the atomic revision-keyed cache covered; `server.network.egress` still defaults `false`; `miekg/dns` promoted + pinned; **the ADR-0116 registry *assembly* (`EntitiesFor` / the schema-vocabulary assembly / the built-in set) is unedited** (grep-check: no per-capability branch added to `EntitiesFor`; the only cedar-driver edits are the one `resourceUID` `KindNetDestination` case + the `NetDestination` entity-type const in `schema.go`); `just ci` green after commit; F81 row → `reviewing`; no identity/path leak.

## Review checklist

- [ ] The gateway recovers `SO_ORIGINAL_DST`, identifies the caller by **source IP** via `WorkerIndex` (never client-asserted), and **delegates** the decision to `auth.Authorizer` (`egress::connect` over a `NetDestination`) — it embeds no policy.
- [ ] **Domain trust anchor (B1):** a domain permit fires **only** when the real dst IP is in the **funcd forwarder's** attested resolved-IP set for `(worker, domain)`; SNI/Host is a hint that must itself be attested (a spoofed SNI to an unattested IP ⇒ deny — the `sni-spoof-denied` test). CIDR rules match `ip` directly. No MITM.
- [ ] Default-deny, fail-closed: no `EgressPolicy` ⇒ nothing external; gateway down ⇒ F80 redirect refused; ALLOW splices, DENY ⇒ `403`/RST.
- [ ] **Every** connection (allowed + blocked) is audited to the funclog channel.
- [ ] The DNS forwarder is the **only** reachable resolver (F80's `:53` accept flipped to a redirect into it); it records `(worker,domain)→IP set` and exposes the reverse `DomainsFor`; it is **required for all domain policy**, not just raw TCP.
- [ ] `EgressPolicy` is an allow-list; `Validate` rejects bad domain/CIDR/port and a rule with no `to`; `appliesTo` empty ⇒ whole namespace.
- [ ] `EgressCapability()` has **no `PrincipalBinding`** and **no `Builtin`**; the grant comes **only** from `CompileEgressPolicy`; the `NetDestination` resource is materialized from the request ref's **`Path`** with **no `MetaReader` read**; `Path` encoding = `"<ip>:<port>#<domains>"` and `resourceUID`'s new case agrees byte-for-byte.
- [ ] Egress adds **no** per-capability branch to the ADR-0116 registry assembly (schema/EntityProvider/built-ins) — grep-checkable; the **only** cedar-driver edit is the one `resourceUID` `KindNetDestination` case (mirroring `principalUID`'s `S3Identity`). `egress::connect`/`NetDestination` join the assembled vocabulary.
- [ ] The compiler builds each permit as a **Cedar JSON EST** document (`Policy.UnmarshalJSON`, injection-safe — a domain/CIDR string is a JSON value node, never interpolated syntax) rendered to text via `MarshalCedar`; it emits **one permit per rule** scoped by `principal.namespace == "<ns>"` (whole-namespace) or a bounded `principal == Function::"…"` disjunction (named `appliesTo`) — **O(rules), function-count-independent**, with **no group entity**; the registry *assembly* (`EntitiesFor`/schema-vocabulary/built-ins) is unedited, the only driver edits being the one `resourceUID` `KindNetDestination` case + the `NetDestination` entity-type const. The policy source lists it **alongside `KindPolicy`**; the **cache revision keys on `EgressPolicy` + the Function-set** (a write/named-`appliesTo` Function change recompiles).
- [ ] The compiled `PolicySet` lives **only** in the in-memory PDP cache (never persisted); the cache is an **atomic revision-keyed single slot** (lock-free `Authorize`, no stale-version accumulation). `EgressPolicy.Validate` enforces the **admission caps** (`maxEgressRules`/`maxEgressDests`/`maxEgressPorts`) so compiled memory has a hard ceiling.
- [ ] Non-Linux / disabled ⇒ `New` returns a no-op, nothing wired, egress open as today; build-tagged dispatch compiles on every GOOS.
- [ ] `KindNetDestination` is authorization-only (like `KindS3Identity` — excluded from `Kind.Validate`/`NewObject`/`AllKinds`); `server.network.egress` defaults `false`; `dnsForwarderPort` added.
- [ ] Typed surface (`netip`/typed refs, no `any`; `ctx` first); every Scenario has a named test (Lima ones authored + deferred, not silently skipped).

## Consequences

- **(+)** F80's redirect finally has an enforcement point: governed, audited, forwarder-attested outbound, enforcing the confidential "a byte never leaves the box" invariant.
- **(+ trust model)** The domain→IP binding is funcd's own (the forwarder), not the client's — so a spoofed SNI/Host cannot smuggle traffic to a denied IP; the authorization question is genuinely "may this worker reach *this resolved destination*."
- **(+)** `EgressPolicy` is the first high-level typed policy CRD that **compiles to Cedar** — a reusable template (VolumePolicy, secret policy); the ingress/egress PEPs now mirror through one PDP. The registry assembly is untouched; the only additions are one `resourceUID` case + the additive policy-source/cache-revision extension (both mirror existing precedent — `S3Identity`, the `KindPolicy` list).
- **(+ memory/perf)** Compilation via the **JSON EST** is injection-safe by construction; the `principal.namespace`-scoped **collapse** makes the compiled set **O(rules), function-count-independent** (not O(functions)); admission **caps** give a hard ceiling; and the **atomic revision-keyed cache** keeps one live version with **lock-free** `Authorize`. Compiled policy is never persisted — a pure function of the `EgressPolicy` CRDs, so there is no derived state to garbage-collect.
- **(+)** Opt-in + Linux-gated ⇒ zero upgrade breakage; in-binary ⇒ single static binary preserved; source-IP identity ⇒ no worker cooperation needed (unforgeable at L3, per Constraints).
- **(−/accepted)** SNI-domain (no full-URL/MITM) and TCP-only (no UDP/TPROXY) in V1; Linux/containerd only (process runtime no-op); the behavioral guarantees are Lima-verified (deferred), so the unit tests carry the cross-platform decision/compile/registration proof.
- **(risk)** DNS-correlation staleness (a resolved IP reused for another domain within TTL) could mis-scope a rule — mitigated by per-worker keying + short TTL. A worker that resolves out-of-band would lose attestation — F80's `:53` redirect closes that (the forwarder is the *only* reachable resolver), so a connect with no attested record simply has no domain permit (default-deny holds).

## Open questions

- **Wildcard matching expression** — the default lowers `*.x.com` to an exact `resource.domains.contains("*.x.com")` over a forwarder-injected pattern token (Decision §2). If a future need wants richer patterns, a Cedar `like` over a single attested `domain` attribute is the alternative — decided at implementation. (Whole-namespace `appliesTo` scoping and its recompile trigger are **decided**: per-Function enumeration keyed into the cache revision — Temporary workarounds + M1, not open.)
- **Audit record schema convergence** with the F76 access log / funclog envelope — reuse vs a dedicated egress record. Resolved when wiring the funclog sink.

(The `KindNetDestination` registration question is **decided by precedent**, not open: it mirrors `KindS3Identity` exactly — an authz-only Cedar kind with no metastore registration, excluded from `Kind.Validate`'s CRUD set / `NewObject` / `AllKinds`.)

## References

- [FEAT-0007/F81](../feat/0007-feat-egress-control.md) · [ADR-0115](0115-worker-network-isolation.md) (F80 substrate — the redirect + the DNS-workaround exit) · [ADR-0116](0116-capability-authorization-framework.md) (the capability registry) · [ADR-0074](0074-cedar-authorization-resource-access.md) (the PDP + `EgressPolicy`→Cedar precedent) · [ADR-0113](0113-edge-authn-pep.md) (the ingress-PEP mirror).
- [github.com/miekg/dns](https://github.com/miekg/dns) — BSD-3-Clause, already in the module graph · `golang.org/x/sys/unix` — BSD-3, `SO_ORIGINAL_DST`. Verified 2026-07-09.
- blueprint.md — *Network manager (egress control)*, *Security → Egress control*.
