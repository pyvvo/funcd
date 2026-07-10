# ADR-0117 implementation review — egress policy enforcement (FEAT-0007/F81)

- **ADR**: [0117-egress-policy-enforcement.md](../adr/0117-egress-policy-enforcement.md) — status `Reviewing`
- **Phase**: implementation (ADR-0000 review gate #5)
- **Producing model**: claude-opus-4-8
- **Reviewer**: adr-impl-review gate
- **Date**: 2026-07-10
- **Verdict**: **pass** — DoD met, no Blockers, no Majors; one benign Minor (robustness note).

## Verification run (captured exit codes)

All commands run through `nix develop -c`; the tree is clean (work committed: `55e6c37` + `6160b39`).

| Command | Exit |
|---|---|
| `go build ./...` | **0** |
| `env GOOS=linux GOARCH=arm64 go build ./...` (linux-only gateway/forwarder/nftables/resolv) | **0** |
| `go test ./internal/auth/... ./internal/network/... ./api/types/... ./internal/runtime/...` | **0** (all `ok`) |
| `go tool golangci-lint run ./internal/auth/cedar/... ./internal/network/... ./api/types/v1alpha1/... ./internal/runtime/containerd/...` | **0** — `0 issues.` |
| `go mod verify` | **0** — `all modules verified` |

`TestPythonPoolSmoke` (internal/testkit/bench) — pre-existing environmental flake, not in scope, not attributed.

## Conformance review against the ADR Contracts

### JSON-EST compiler — CONFIRMED (`internal/auth/cedar/egress_compile.go`)
- Each permit is assembled as a Cedar JSON EST tree of `estNode` (`interface{}` literal, the sanctioned JSON-shape escape — NOT the banned `any` alias, documented at :27), `json.Marshal`'d → `cedar.Policy.UnmarshalJSON` → `MarshalCedar` to text (:100-108). Values are JSON value nodes, so injection-safe by construction; a malformed CIDR is caught at parse time via `netip.ParsePrefix` before it ever reaches the EST (:187-189).
- **One permit per rule** (:85-110), not per function. Whole-namespace scope is `principal.namespace == "<ns>"` (`namespaceScope`, :121-123); named `appliesTo` is a bounded `principal == Function::"<ns>/<fn>"` disjunction (`appliesToScope`, :127-138) whose id matches `functionUID` byte-for-byte.
- Domains → `resource.domains.contains`, CIDRs → `resource.ip.isInRange(ip(...))`, ports → OR-fold `resource.port ==` set (:154-197).
- Tests: `TestScenarioEgressPolicyCompilesToCedar`, `TestEgress_CompilesToCedar` (asserts `resource.domains.contains("*.x.com")`, the `appliesTo` disjunction, **and the injection probe** — a `a");permit(...);//`-laden domain compiles inertly, `strings.Count(itext,"permit (")==1`, egress_test.go:148-162), `TestEgress_CollapseFunctionCountIndependent` (2 vs 50 functions ⇒ exactly 3 permits, 3× `principal.namespace == "acme"`, no `Function::` head). All pass.

### Admission caps — CONFIRMED (`api/types/v1alpha1/egresspolicy.go`)
- `maxEgressRules/Dests/Ports = 64/64/32` (:13-17); `Validate` enforces all three plus per-rule non-empty `to`, DNS/wildcard domains, parseable CIDRs, port range (:71-115). Test `TestScenarioEgressPolicyCapsRejectOversize` + `TestScenarioEgressPolicyValidate` pass.

### Atomic cache — CONFIRMED (`internal/auth/cedar/policies.go`)
- `atomic.Pointer[compiledPolicies]` with a lock-free `Load()` fast-path and a single-flight `mu`-guarded recompile with re-check under the lock (:49-71). Old generation GC'd on swap — one live + one transient. Test `TestScenarioPolicyCacheAtomicSwap` asserts same-revision returns the identical `*PolicySet` (`require.Same`), a revision bump returns a new one (`require.NotSame`), and the source is polled every `Get`.

### Egress capability — CONFIRMED (`internal/auth/cedar/capabilities.go`)
- `EgressCapability()` (:324-332) has **no `PrincipalBinding`** (zero value) and **no `Builtin`** (`""`) — default-deny with grants only from compiled policy, exactly the ADR's NONE/NONE claim.
- `netDestinationResource` materializes `NetDestination` from the request ref `Path` via `auth.ParseNetDestPath` with **no `MetaReader` read** (:338-363); emits UID + `{ip (Cedar ip()), port (Long), domains (Set)}`.
- Registry assembly narrowed-claim (M2/M3) is **honest**: `EntitiesFor` (entities.go:55-129) is generic — resource dispatch is "first capability whose `Resource` returns ok", no per-kind/NetDestination branch; principal binding loops over `caps` generically. The only driver request-path edits are the single `case v1.KindNetDestination` in `resourceUID` (entities.go:189-196, mirroring `principalUID`'s `KindS3Identity`) and the `entityTypeNetDestination` const in schema.go:32. `EgressCapability()` joins the assembled set at schema.go:45 and funcd.go:387. Test `TestScenarioNewCapabilityNoEntitiesForEdit` guards the no-edit property.

### B1 trust anchor — CONFIRMED (`internal/network/egress/gateway.go` + `gateway_linux.go` + `peek.go` + `forwarder.go`)
- `buildDestination` (gateway.go:84-95) authorizes on the **forwarder-attested** set only (`DomainResolver.DomainsFor(src,dst)`); the peeked SNI/Host is added **only if already attested** (`contains(attested, asserted)`), else dropped — a spoofed SNI to an unattested IP contributes nothing. `decideConnect` (:100-127) authenticates by source IP via `WorkerIndex` (unknown ⇒ deny+audit, fail-closed), never client-asserted.
- `peek.go` uses stdlib `crypto/tls` `GetConfigForClient` (aborts the handshake via an `errPeekDone` sentinel, no real TLS/dial) and `net/http.ReadRequest` — no hand-rolled byte parsers.
- Wildcard dependency wired: `correlator.DomainsFor` injects a matched `*.x.com` pattern token from `wildcardsFor` (forwarder.go:198-230, `matchWildcard` requires a real leading label), sourced from `WildcardPatterns` (forwarder.go:41-57). `pkg/funcd/funcd.go` wires `storeWildcardPatterns{c.store}` into `NewForwarder` (:420-424) and `p.egressWorkers` into both forwarder and gateway.
- Tests: `TestScenarioEgressSNISpoofDenied`, `TestScenarioForwarderAttestedBuild`, `TestScenarioDecideFlow`, `TestScenarioDomainsForCorrelationAndWildcard`, `TestScenarioSNIParse`, `TestScenarioHTTPHostParse` — all pass.

### Worker resolver — CONFIRMED (`internal/runtime/containerd/containerd_linux.go`)
- `writeWorkerResolv(cfg.SubnetCIDR)` (:184) writes a shared `/etc/resolv.conf` bind-mounted read-only into every worker at `/etc/resolv.conf` (:265). This is the fix that unblocked domain+IP egress (raw containerd provisions no resolv.conf). Sound.

### Path codec + authz-only kind — CONFIRMED (`internal/auth/netdest.go`, `metadata.go`, `authorizer.go`)
- `NetDestination` codec is the single shared source of truth: `EncodeNetDestPath` = `"<ip>:<port>#<domains>"`, `UIDString()` = the `<ip>:<port>` prefix only (stable, domain-independent), consumed identically by `netDestUID` in both `EgressCapability.Resource` and `resourceUID`. `KindNetDestination` is authorization-only (metadata.go:54). `ActionEgressConnect = "egress::connect"` (authorizer.go:79).

### Wiring, config, opt-in — CONFIRMED
- `server.network.egress` is a plain `bool` (config.go:92), zero-value **false** — no default override; `dnsForwarderPort` added (config.go:97). Gateway/forwarder wired only under `if c.egressGatewayEnabled` (funcd.go:418); `egress.New(false,…)` returns `noopGateway`. `funcdconfig.yaml` sets `egress: true` **only** in the egress-probe lane.
- `miekg/dns v1.1.72` promoted to a **direct** require (go.mod:27); `x/sys/unix` already present.

## Provided Lima e2e evidence (NOT re-run)

The DoD's containerd Lima behavioral lane is **authored and sound**, and was **run green by the orchestrator** on a real containerd Lima VM (5/5 testcases PASS, `final status: PASS`, venom exit 0). I verified the lane files rather than re-running (re-run needs colima + ~5-min VM boot):
- `e2e/egress.venom.yml` — 5 testcases each mapping to an ADR Scenario: `no-egresspolicy-denies-all` (default-deny), `allowlist-permits-wildcard-domain` (forwarder wildcard-token injection + splice), `cidr-rule-permits-declared-ip` (`isInRange`), `cidr-denies-unlisted-ip` (closed allow-list), `blocked-connection-audited` (funclog `allowed=false` record). EgressPolicies applied mid-suite via in-VM `limactl shell … sudo bash -c '…'` (co-located, no path leak; the env-quoting fix is documented inline).
- `scripts/lanes.yaml` `egress:` stanza — builds the JS probe (esbuild + shim contract toolchain), stages the two EgressPolicies + the egress-on funcdconfig, applies/pushes correctly, binds `venom: e2e/egress.venom.yml`.
- `examples/js/egress-probe/` — `probe.mjs`/schema, `function.yaml`, wildcard + CIDR `egresspolicy-*.yaml`, and the sole egress-enabled `funcdconfig.yaml`.

Accepted on the provided run evidence; the lane was verified green by the run, not re-run by this gate.

### The four e2e-caught bugs (attribution confirmed)
1. **F80 nftables first-run crash** (`internal/network/nftables_linux.go:113-127`) — a batched `DelTable` of a nonexistent table returns ENOENT and fails the atomic Flush; `delFuncdTables` now `ListTables()` first and deletes only existing funcd tables. **Attributed to ADR-0115/F80** (the substrate), not an ADR-0117 model defect. Fix is sound and idempotent.
2. **egress-probe wrong shim Handler signature** — example/test-asset fix (**env/example**), not core.
3. **venom apply-step env-quoting** — e2e harness fix (**env**), documented inline in the venom file.
4. **Missing worker `/etc/resolv.conf`** (`containerd_linux.go`) — **attributed to the ADR-0032 containerd runtime** (raw containerd provisions none); sound. All four are committed.

## ✅ Verified correct (strengths to keep)

- The B1 invariant is implemented exactly as designed: authorization rides only the forwarder-attested domain set; SNI/Host is a confirmation hint gated on prior attestation. Spoof path is provably closed and unit-covered.
- Injection-safety is real and *tested* with an adversarial probe, not merely asserted.
- The O(rules) namespace-attribute collapse is honest — the function-count-independence test drives it at 2 and 50 functions.
- The "register, don't edit" narrowing (M2/M3) is truthful: `EntitiesFor` and the schema/built-in assembly are genuinely generic; the only edits are one `resourceUID` case + one entity-type const, both mirroring the existing `S3Identity` precedent.
- Default-deny / fail-closed / opt-in posture holds end to end: zero-value config false, no-op gateway when disabled, unknown-source-IP denied, gateway-down ⇒ F80 redirect refused.
- Shared `NetDestination` codec guarantees the producer (gateway) and consumer (capability) UIDs agree byte-for-byte — a common source of authz drift, closed by construction.
- Peek uses stdlib TLS/HTTP parsers with an aborted handshake — no bespoke wire parsing.

## Findings

### Minor (non-blocking)
- **[model] Bounded 4096-byte / 500 ms SNI/Host peek** (`internal/network/egress/gateway_linux.go:150-152`). `peek` reads a single ≤4096-byte chunk under a 500 ms deadline; a ClientHello that exceeds one segment/4096 bytes (many extensions, large SNI) or a slow-start client could yield no parsed SNI hint. **Zero security impact** — the SNI is only a confirmation hint gated on forwarder attestation (B1), so a missed peek merely drops the hint and never grants; default-deny and the attested set are unaffected. Purely a hint-robustness observation; behavior is Lima-verified green. No change required for this ADR.

## Definition of Done — 10/10 met

1. build / vet / test / golangci-lint / go mod verify green — ✅ (exit 0 each)
2. unit scenarios pass — ✅
3. Lima behavioral scenarios authored + run green on containerd — ✅ (orchestrator evidence)
4. Validate+caps, JSON-EST compiler, capability registration, `resourceUID` case, atomic revision-keyed cache covered — ✅
5. `server.network.egress` defaults false — ✅
6. `miekg/dns` promoted + pinned (`v1.1.72`, direct) — ✅
7. ADR-0116 registry assembly unedited (grep-checked; only `resourceUID` case + `schema.go` const) — ✅
8. `just ci` green after commit (tree clean, four sub-checks pass) — ✅
9. F81 row → `reviewing` — ✅
10. no identity/path leak (grepped touched non-doc files — no hits) — ✅

## Verdict

**pass.** The implementation conforms to the ADR-0117 Contracts, Scenarios, Review checklist, and Definition of Done. No Blockers, no Majors; one benign model-attributed Minor (peek buffer bound) with no security or DoD impact. The orchestrator may advance ADR-0117 `Reviewing → Implemented` and the F81 feat row → `implemented`.
