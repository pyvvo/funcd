# ADR-0115 implementation review — worker network isolation (L3/L4 egress substrate)

## Re-review (post-rework loop 1): PASS — 0 blockers, 0 majors  (model: claude-fable-5)

The builder's loop-1 rework resolves all three model findings; DoD now holds 14/14 (the four Linux
behavioral scenarios remain correctly Lima-deferred). Status stamped `Reviewing → Implemented`.
Re-verified evidence (all `nix develop -c`):

| Check | Command | Exit |
|---|---|---|
| build (darwin) | `go build ./...` | 0 |
| build (GOOS=linux) | `env GOOS=linux go build ./...` | 0 |
| vet (GOOS=linux) | `env GOOS=linux go vet ./internal/network/` | 0 |
| linux test cross-compile | `env GOOS=linux go test -c -o /dev/null ./internal/network/` | 0 |
| lint | `go tool golangci-lint run ./internal/network/ ./pkg/funcd/ ./cmd/funcd/ ./internal/platform/config/` | 0 (`0 issues.`) |
| test (network, cross-platform) | `go test ./internal/network/ -count=1 -v` | 0 (4 passing: DisabledPassthrough, NonLinuxNoop, **TeardownClean**, PolicyValidate) |
| mod verify | `go mod verify` | 0 |

**Blocker 1 — resolved.** `planRules` (`nftables_linux.go:144-165`) now emits a `kindRedirectSkip`
rule (`expr.VerdictReturn`, matched on src∈subnet + dst + tcp + dport) for each `InternalAllow`
endpoint **and** the `DNSResolver` **before** the single catch-all `kindRedirect` in `prerouting`
(`exprsFor`, lines 201-216). In a NAT `prerouting` base chain (default policy accept) a `return`
skips the DNAT and the packet proceeds to routing with its **original** destination — so internal-
service TCP and DNS-over-TCP are no longer hijacked to the gateway, matching ADR Decision B1
("destination *not* in the internal allowlist and *not* the DNS resolver"). The `forward` accepts
then pass the returned internal/DNS traffic past the drop policy. Checklist #5 now holds.

**Major 1 — resolved.** `program()` (lines 169-198) is refactored to consume the pure
`planRules(p)`, and `TestPreroutingExcludesInternalAndDNSBeforeRedirect`
(`network_linux_test.go:45-84`) asserts the load-bearing ordering: exactly one redirect, it is
**last** in prerouting, every pre-redirect rule is a `kindRedirectSkip` covering both the internal
endpoint and the DNS resolver, and `forward` accepts = `len(InternalAllow)+2`. The built plan is now
asserted, per the ADR test plan.

**Major 2 — resolved.** `TestScenarioTeardownClean` (`network_test.go:32-35`, cross-platform,
passing) covers the no-op Remove (clean + idempotent); the driver-side `delFuncdTables → Flush`
deletion is recorded in the test comment as Lima-deferred (needs a kernel + `CAP_NET_ADMIN`).
Checklist #14 now holds — every scenario has a named test or a recorded deferral.

**Minor — resolved.** `newEnabledManager → newDriver` across all three files + the `network.go`
comment, matching the ADR Contract. (One cosmetic stale comment lead-line in `nftables_other.go:7`
still reads `newEnabledManager` — trivial doc nit, non-blocking.)

**DoD: 14/14** ADR Review-checklist items hold. The four Linux behavioral scenarios
(`lateral-denied`, `external-redirected-failclosed`, `internal-service-allowed`, `dns-resolves`)
remain correctly deferred to the Lima e2e lane and recorded in the ADR. Verdict: **pass** — status
advanced to `Implemented`; feat row F80 → `implemented`. Board card left `In Progress` (F80+F81
share it; it moves to Done when F81 lands).

---

## Original review (loop 0) — changes requested — 1 blocker, 2 majors  (ADR-0115 implementation, model: claude-fable-5)

The package builds on both GOOS targets, lints clean, the cross-platform tests pass, and the
Linux driver cross-compiles/vets clean. The port surface, config, and composition-root wiring are
faithful to the Contracts. **But the core deliverable — the programmed Linux ruleset — diverges
from the ADR's Decision: the prerouting `REDIRECT` rule omits the internal-allowlist / DNS-resolver
destination exclusion the ADR requires**, so (by the ADR's own model) internal-service TCP and
DNS-over-TCP are hijacked to the dead gateway. The unit test that should have caught it was
weakened to assert only the structural plan, and the `teardown-clean` scenario has no test at all.
These are model-attributed and block sign-off; the Linux *behavioral* deferral is not itself a
reason.

### Captured evidence (all via `nix develop -c`)

| Check | Command | Exit |
|---|---|---|
| build (darwin, noop path) | `go build ./...` | 0 |
| build (GOOS=linux, driver) | `env GOOS=linux go build ./...` | 0 |
| lint | `go tool golangci-lint run ./internal/network/ ./pkg/funcd/ ./cmd/funcd/ ./internal/platform/config/` | 0 (`0 issues.`) |
| test (touched pkgs) | `go test ./internal/network/ ./internal/platform/config/ ./pkg/funcd/ -count=1` | 0 |
| linux test cross-compile | `env GOOS=linux go test -c -o /dev/null ./internal/network/` | 0 |
| linux vet | `env GOOS=linux go vet ./internal/network/` | 0 |
| mod verify | `go mod verify` | 0 (`all modules verified`) |

Cross-platform scenario tests, all named + passing (`go test -v ./internal/network/`):
`TestScenarioDisabledPassthrough` (disabled-passthrough), `TestScenarioNonLinuxNoop`
(non-linux-noop), `TestScenarioRulesetProgrammed` (ruleset-programmed), `TestPolicyValidate`.

---

### 🔴 Blocker 1 — the prerouting `REDIRECT` rule redirects *all* worker TCP, not "external" TCP · attribution: model

`internal/network/nftables_linux.go:140-141` builds the prerouting NAT rule as
`match(src ∈ WorkerSubnet) AND match(l4proto == TCP) → REDIRECT :GatewayPort` — **with no
destination match**. The ADR Decision B, bullet 1 is explicit:

> `nat` chain, hook `prerouting`: **REDIRECT** worker-source **external TCP** — destination *not*
> in the internal allowlist and *not* the DNS resolver — to `GatewayPort`.

The exclusion is missing. The rule's own comment (`nftables_linux.go:136-139`) states the intended
reasoning — *"Internal-allow and the DNS resolver are excluded by accepting them first in forward (a
redirected packet skips forward)"* — but that reasoning is mechanically wrong: a NAT `REDIRECT` in
`prerouting` (prio `dstnat`) rewrites the destination **before** the routing decision, so the
packet is already local-destined and routes to `input`, never reaching the `forward` chain where
the internal/DNS accepts live. The `forward` accepts (B2, lines 146-155) therefore cannot protect
that traffic from the redirect.

**Failure scenario** (the ADR's `internal-service-allowed` case, per the ADR's own model): a worker
opens TCP to an internal-service endpoint (a funcd0-gateway-IP:port in `InternalAllow`). The
prerouting rule matches (src ∈ subnet, TCP) and redirects it to `GatewayPort`. With no F81 gateway
the connection is **refused**; with F81 it is treated as external egress and PDP-governed — either
way it does **not** "pass directly." This contradicts the `internal-service-allowed` scenario, the
`dns-resolves` scenario for DNS-over-TCP, Review-checklist item *"The programmed ruleset matches the
Contracts … external-TCP redirect to `GatewayPort`, else drop"*, and the Consequence *"internal
services stay on their own PEP (no double-governance, no proxy hop)."* Fail-*closed* still holds
(nothing leaks), but the substrate mis-routes internal + DNS traffic.

**Fix (builder):** add a destination-exclusion guard to the prerouting rule — do not redirect when
the destination is an `InternalAllow` endpoint or the `DNSResolver` (e.g. an exclusion set / `!=`
matches, matching how B2 scopes those same destinations), so only genuinely-external TCP is
redirected. Then extend the Linux unit test to assert the built rule set (see Major 1) so the guard
is verified.

### 🟡 Major 1 — `ruleset-programmed` asserts only the structural plan, not the built rules · attribution: model

The ADR Implementation-plan test entry for `ruleset-programmed` calls for the rule-builder
*"factored to build the `[]*nftables.Rule` for a Policy **without applying** … assert against the
built expressions."* The implementation instead splits into `planTables` (a pure structural plan —
table families, chain hooks/types/policies) and `program(c *nftables.Conn, p Policy)` which needs a
**live netlink conn** and calls `c.AddRule` directly, so the built `expr` rules can never be
asserted without a kernel. `internal/network/network_linux_test.go:16-39` accordingly asserts only
`planTables` (families, `prerouting`=NAT, `forward`=drop-policy, `input`=accept). It never exercises
`program`, the `matchIPv4Field`/`matchDstAddr`/`matchDport`/`redirectTo` expression builders, or the
prerouting rule's match set — which is precisely why Blocker 1 slipped through green.

**Fix (builder):** factor the rule construction to return a testable `[]*nftables.Rule` (or use
`nftables.New(nftables.WithTestDial(...))`) and assert the built expressions per the ADR test plan —
including the prerouting destination exclusion.

### 🟡 Major 2 — the `teardown-clean` scenario has no named test and no recorded deferral · attribution: model

The ADR lists `teardown-clean` as a Scenario and places it in the **unit / cross-platform** column
of the test plan (*"`teardown-clean` (Remove targets the `funcd_egress` table)"*). No such test
exists — `grep 'func Test' internal/network/` yields only `TestScenarioDisabledPassthrough`,
`TestScenarioNonLinuxNoop`, `TestScenarioRulesetProgrammed`, `TestPolicyValidate`. The disabled/
non-linux Remove assertions only exercise the **noop** path, never the driver's `delFuncdTables`
(`nftables_linux.go:104-107`) queuing of the `funcd_lateral`/`funcd_egress` tables. This misses
Review-checklist item *"Every Scenario has a named test (Linux-integration ones deferred +
recorded, not skipped silently)"* — the scenario is neither tested nor explicitly recorded as
deferred.

**Fix (builder):** add a named `teardown-clean` test (assert `delFuncdTables` targets both table
names structurally, or run it on the Linux lane) — or, if genuinely kernel-bound, record it in the
deferred set alongside the other Lima scenarios.

### Minor — internal constructor named `newEnabledManager`, ADR Contract comment says `newDriver()` · attribution: model

Cosmetic: the exported `New(enabled bool) Manager` signature matches the Contract exactly; only the
build-tagged internal helper differs in name from the ADR's illustrative comment. Harmless, no fix
required beyond noting the drift.

### ✅ Verified correct (keep it)

- **Cross-platform build/lint/test all green** on both GOOS targets (table above); the noop path
  (darwin/disabled) and the Linux driver both compile, and the Linux test cross-compiles + vets.
- **Port surface matches the Contracts**: `Manager.Apply/Remove` ctx-first; `Policy` with `netip`
  types (`Prefix`, `AddrPort`, `uint16`) — no `any`/`map[string]any`; `Policy.Validate` rejects a
  zero subnet / zero `GatewayPort` (`fault.Invalid`) / invalid resolver, matching checklist #11 and
  `TestPolicyValidate`.
- **Build-tag dispatch is correct**: `New` is un-tagged and delegates to `newEnabledManager` in
  `nftables_linux.go` (google/nftables driver) vs `nftables_other.go` (`!linux`, logs once + noop);
  `New` compiles on every GOOS (checklist #10).
- **Lateral deny is `bridge`-family** (`funcd_lateral`, `TableFamilyBridge`, forward hook) dropping
  frames whose src *and* dst are both worker IPs — the judge-B1 fold, correct per checklist #6.
- **`inet` table shape** is right: `prerouting`=NAT/dstnat, `forward` policy `drop` with accepts
  added before fall-through, `input`=accept for the redirected port (checklist #7); the `forward`
  internal-allow + DNS accepts are correctly **scoped to the endpoint/resolver address:port**
  (`matchDstAddr` + `matchDport`), not any `:53` (checklist #8, #9). The defect is isolated to the
  prerouting rule's over-broad match, not these.
- **CNI conflist untouched** — `Apply` only `AddTable`s the two funcd tables; `Remove`/`delFuncdTables`
  delete exactly those two (checklist #4, #12). Pure-Go netlink via `google/nftables`, no `nft`
  binary, no cgo (checklist #3).
- **Wiring is clean**: `pkg/funcd` applies before workers serve (fatal on error) and removes at
  shutdown; `WithEgressIsolation` facade; `cmd/funcd` parses `server.network.*` → `network.Policy`
  and gates on `egress`; config block defaults `egress:false` (checklist #1, #2). `slog`-only, no
  `panic`/`fmt.Print`, `api/fault` throughout.
- **Deps sanctioned + license-clean**: `github.com/google/nftables v0.3.0` (Apache-2.0) and
  `github.com/mdlayher/netlink v1.7.3-…` (MIT) — both confirmed from the module cache LICENSE files;
  `go mod verify` clean.
- **Tracking**: ADR at `Reviewing`, substance unchanged (new file, only the status bump); F80 feat
  row at `reviewing`; blueprint synced with one egress-control line pointing at ADR-0115.

### Definition of Done

**12 / 14** ADR Review-checklist items hold. Misses:
- #5 *"programmed ruleset matches the Contracts … external-TCP redirect … else drop"* — **fails**
  (Blocker 1: redirect over-matches internal/DNS destinations). model.
- #14 *"Every Scenario has a named test (deferred ones recorded)"* — **fails** (Major 2:
  `teardown-clean` has neither; Major 1: `ruleset-programmed` asserts the plan, not the built
  rules). model.

The four Linux *behavioral* scenarios (`lateral-denied`, `external-redirected-failclosed`,
`internal-service-allowed`, `dns-resolves`) are correctly deferred to the Lima e2e lane and recorded
in the ADR — that deferral is **not** counted against the DoD.

### Model scorecard

Recorded: claude-fable-5 on ADR-0115 (implementation) → changes-requested, 1 blocker / 2 majors / 1
minor, 4 model-attributed, DoD 12/14. See docs/reviews/model-scorecard.md.

### Recommendation

Loop back to the builder (`adr-impl`) for the three model findings — all fixable in place, no
superseding ADR needed (the ADR Decision is correct; the implementation under-implements it):

1. Add the internal-allow / DNS-resolver **destination exclusion** to the prerouting `REDIRECT`
   rule (Blocker 1).
2. Factor the rule-builder to yield a testable `[]*nftables.Rule` and assert the built expressions,
   including the exclusion (Major 1).
3. Add / record the `teardown-clean` scenario test (Major 2).

The ADR stays `Reviewing`; advance nothing until a re-review passes.
