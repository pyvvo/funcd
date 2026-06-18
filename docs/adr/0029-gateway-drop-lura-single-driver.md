# ADR-0029: Gateway — drop the Lura driver (single embedded driver; supersedes ADR-0013)

- **Status**: Implemented
- **Date**: 2026-06-15 (**Implemented 2026-06-15** · **Accepted 2026-06-15** after judge pass — no Blockers. The judge confirmed the
  single-driver justification is legitimate (verified `presets.go` wires the embedded driver as *both*
  `InMemory()` and `Production()` — real==in-memory for an in-process port), the removal is clean (only
  `lura/lura.go` imports luraproject), and the depguard-deny mechanism is already fixture-proven. Folded two
  **Majors**: M1 — also fix the stale `gatewaycontract/contract.go` doc (it too names the Lura driver); M2 —
  the supersession bookkeeping (re-point F10 + add the `Superseded by ADR-0029` back-link to ADR-0013) lands at
  the **Implemented** stamp, not before, so F10 stays consistently `implemented`+linked-to-0013 (the gateway
  feature is already delivered; the row can't walk backward). Decision: one embedded `httputil` driver; drop
  Lura + its dependency; the external-gateway driver is the named V2 second driver.)
- **Deciders**: green-0-rabbit
- **Tags**: gateway, ingress, reverse-proxy, httputil, ports-drivers, dependencies, cleanup
- **Realizes**: [FEAT-0000/F10](../feat/0000-feat-v1.md) (gateway / ingress)
- **Supersedes**: [ADR-0013](0013-gateway-ingress-httputil-primary.md) — **re-affirms** its core decisions
  unchanged (the embedded `net/http/httputil.ReverseProxy` is the primary production ingress; ingress concerns
  are composable `net/http` middleware; streaming is first-class; TLS via embedded `certmagic`, deferred) and
  **changes only the driver set**: the optional **Lura** driver is **removed** (resolving ADR-0013's own open
  question, *"whether Lura is ever dropped… a future gateway ADR, if/when the need is real"*). The
  `gateway.Gateway` port is unchanged; the change is fully reversible.
- **Relates to**: [ADR-0002](0002-source-code-conventions-and-patterns.md) (ports/drivers — the ≥2-driver
  convention this ADR reconciles for an in-process port), [blueprint.md — Ingress / API Gateway](../../blueprint.md)

## Context & Need

ADR-0013 made the embedded `httputil` reverse-proxy the primary gateway and **retained Lura
(`internal/gateway/lura`) as an optional driver** for two reasons: a possible future API-aggregation need,
and the ports-and-drivers "≥2 drivers" convention. ADR-0013 itself flagged this as provisional
(*"may be dropped later if never used"*) and parked the question for a future gateway ADR. That review is now:

- **Lura is unused.** Nothing imports `internal/gateway/lura` — not the composition root (ADR-0028's
  `pkg/funcd`), not `InMemory()`/`Production()`, nothing. It is a dead code path behind a dead doc comment
  (`gateway.go` still describes it as *"production"*, a stale ADR-0012-era line ADR-0013 missed).
- **It is the wrong shape and weak on the hot path.** Lura is an API-*aggregation* framework; funcd's gateway
  is 1:1 transparent reverse-proxy (the Lura driver had to *disable* Lura's engine to behave like one). It has
  no streaming — ADR-0013 had to **exempt** it from the `streaming-passthrough` contract — yet streaming
  (SSE / token streams for agents/MCP) is the non-negotiable hot path.
- **It is a real, carried dependency.** `github.com/luraproject/lura/v2` (+ the transitive
  `github.com/krakend/flatmap`) is compiled into the binary, against funcd's minimal-dependency / single-binary
  value.
- **It does not even fill the ≥2-driver convention's *intent*.** That rule is *"at least two drivers — one
  real, one in-memory"* (blueprint). For an **in-process** port, the pure-Go embedded `httputil` driver is
  **simultaneously** the real (production) and the in-memory (the `InMemory()` harness) driver — there is no
  real-vs-in-memory split to fill. Lura was a *second "real" driver*, not the missing in-memory one; it
  satisfied the literal count, not the purpose.

So Lura is dead weight: unused, wrong-shape, streaming-weak, a dependency, and not actually serving the
convention it was kept for. This ADR removes it. It is **one topic at one altitude** — the gateway driver set.

## Scope

- **In**: remove the `internal/gateway/lura` driver + its tests; drop the `luraproject/lura/v2` dependency
  (`go mod tidy`); a depguard deny so it can't be re-imported; fix the stale doc comments that reference Lura
  — **both** `gateway.go`'s package doc **and** `gatewaycontract/contract.go`'s ("the embedded driver *and the
  Lura driver* both call it"); sync the blueprint (remove the optional-Lura mentions **and** the stale
  ADR-0012-era lines that still call Lura *the* embedded gateway).
- **Out**: re-deciding ADR-0013's primary-driver / middleware / TLS choices (re-affirmed, unchanged); adding a
  *new* second driver now — the natural one (an **external-gateway** driver that programs routes into an
  external APISIX/Caddy via its admin API, for multi-node / fronted deployments) is a **V2** item, recorded
  when that need is real; API-aggregation (BFF) — not a V1/near-term need (the reason Lura existed).

## Constraints & Decision drivers

- **YAGNI + minimal deps** — an unused framework dependency kept for a speculative need is exactly what the
  port abstraction exists to defer; remove it and add a *useful* second driver when a real need appears.
- **The port keeps it reversible** — `gateway.Gateway` is unchanged, so dropping a driver costs nothing
  architecturally; an external-gateway / aggregation / future-Caddy driver remains a clean later add.
- **Honor the convention's intent, not its letter** — the ≥2-driver rule wants a proven seam + a no-infra
  test path; for the in-process gateway, the pure-Go embedded driver provides both, and the port's reality is
  proven by the `gatewaycontract` suite + its consumers (the function reconciler + the activator depend on the
  *interface*, not the driver) + demonstrated reversibility.
- **Newest accepted ADR wins; sync the blueprint** — the blueprint is currently inconsistent (some lines say
  "embedded httputil primary, Lura optional"; older lines still say "embedded API gateway (Lura)"). This ADR
  makes it say one thing: a single embedded `httputil` driver.

## Scenarios

- **scenario: lura-dependency-gone** — *Given* the dropped driver, *when* the module is inspected, *then*
  `internal/gateway/lura` no longer exists and `go.mod` has no `luraproject` (nor the `krakend/flatmap`
  transitive); `go build ./...` + `go mod verify` are clean. A guard test asserts `go.mod` is free of `luraproject`.
- **scenario: lura-import-denied** — *Given* the depguard config, *when* code tries to import
  `github.com/luraproject/...`, *then* the lint rule rejects it (the dependency can't silently return) — the
  same `main`-rule deny mechanism the existing `mock-framework` fixture already proves fires.
- **scenario: gateway-contract-holds-single-driver** — *Given* only the embedded driver, *when* the shared
  `gatewaycontract` suite runs, *then* it passes — the `gateway.Gateway` port is real and complete with one
  production driver.
- **scenario: streaming-preserved** — *Given* the embedded driver, *when* the streaming test runs, *then* SSE
  / chunked responses still flush incrementally — no hot-path regression from the cleanup.

## Decision

1. **Single gateway driver.** The embedded `net/http/httputil.ReverseProxy` driver (`internal/gateway/embedded`)
   is the **only** `gateway.Gateway` driver. ADR-0013's decisions stand, unchanged: it is the primary
   production ingress (transparent 1:1 proxy, longest-prefix routing, declarative `ProgramRoutes`,
   streaming-native); ingress concerns are composable `net/http` middleware (`Chain`); TLS is embedded
   `certmagic` (deferred wiring).
2. **Remove Lura.** Delete `internal/gateway/lura/` (driver + tests) and drop `github.com/luraproject/lura/v2`
   via `go mod tidy`. Add a depguard `main`-rule deny for `github.com/luraproject` so it cannot be re-imported.
3. **Document the single-driver port.** The pure-Go in-process embedded driver is *both* the real and the
   in-memory driver (no infra split), so the ≥2-driver convention's intent — a proven seam + a no-infra test
   path — is met by one driver; the port's reality is the `gatewaycontract` suite + its interface-only
   consumers. Fix `gateway.go`'s stale doc comment accordingly.
4. **Name the real future second driver.** When multi-node arrives, the second driver is an **external-gateway**
   driver (route programming into an external APISIX/Caddy via its admin API), recorded as a **V2** item — a
   genuine need, unlike Lura's speculative aggregation. The port keeps it a clean add.
5. **Sync the blueprint** to a single embedded `httputil` driver — removing both the optional-Lura mentions and
   the stale ADR-0012-era lines that still call Lura the embedded gateway.

## Contracts

This ADR **removes** code + a dependency and **adds** one guard; it introduces no new exported API.
- `internal/gateway/lura/` — **deleted**.
- `go.mod` — `github.com/luraproject/lura/v2` removed (`go mod tidy`); `.golangci.yml` `main` rule gains
  `deny: github.com/luraproject` (prefix).
- `internal/gateway/gateway.go` **and** `internal/gateway/gatewaycontract/contract.go` — package docs updated:
  one embedded `httputil` driver (no "Lura — production"; the contract is run by the embedded driver).
- A guard test (`internal/gateway/nodep_test.go`) asserts `go.mod` contains no `luraproject`.

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Removes (lib) | `github.com/luraproject/lura/v2` (+ transitive `krakend/flatmap`) | the point of the ADR |
| Consumes | the unchanged `gateway.Gateway` port + embedded driver | no new dep |
| Exposes | nothing new | a removal + a guard |

## Implementation plan

1. **`rm -rf internal/gateway/lura/`** (lura.go, lura_test.go).
2. **`go mod tidy`** — drops `luraproject/lura/v2` + `krakend/flatmap` from `go.mod`/`go.sum`.
3. **`.golangci.yml`** — add `deny: { pkg: "github.com/luraproject", desc: "Lura dropped — single embedded gateway driver (ADR-0029)" }` to the `main` depguard rule.
4. **`internal/gateway/gateway.go`** + **`internal/gateway/gatewaycontract/contract.go`** — rewrite the
   package docs: one embedded `httputil` driver (no "Lura — production"; the contract is run by the embedded
   driver); the activator plugs in as a `Route.Upstream`.
5. **`internal/gateway/nodep_test.go`** — the `lura-dependency-gone` guard (read `go.mod`, assert no `luraproject`).
6. **`blueprint.md`** — sync the Ingress line + the stale ADR-0012-era Lura mentions (the repo-layout `lura/`
   entry, the "embedded API gateway (Lura)" lines, the `lura.New(...)` example, the resource-definition/driver
   diagrams) to a single embedded `httputil` driver.
7. **Verify**: `go build ./...` · `go test ./...` (the `gatewaycontract` via `embedded_test`, the streaming
   test, and the ADR-0028 control-plane e2e all still pass) · `golangci-lint run ./...` · `go mod verify`.
8. **Definition of done**: `just ci` green; `internal/gateway/lura` gone; `luraproject` absent from `go.mod`
   and denied by depguard; the embedded driver still passes the contract + streams; the blueprint says one
   driver; no identity/path leak.

## Review checklist

- [ ] **Lura gone, dep dropped** (`lura-dependency-gone`): `internal/gateway/lura` deleted; `go.mod` has no
      `luraproject`/`krakend/flatmap`; `go build`/`go mod verify` clean; the guard test passes.
- [ ] **Can't come back** (`lura-import-denied`): the depguard `main` rule denies `github.com/luraproject`.
- [ ] **Port still real with one driver** (`gateway-contract-holds-single-driver`, `streaming-preserved`):
      the embedded driver passes `gatewaycontract` + the streaming test; the ADR-0028 e2e still reconciles.
- [ ] **Supersession bookkeeping (at the Implemented stamp, not before)**: ADR-0013 gains a `Superseded by
      ADR-0029` back-link and **F10's link re-points to ADR-0029** — done when ADR-0029 reaches `Implemented`
      (F10 stays `implemented` throughout: the gateway feature was already delivered, so the row can't walk
      backward; until then it correctly links ADR-0013 with Lura still retained). The `gateway.Gateway` port +
      the embedded driver are **not** re-implemented; the blueprint is synced to one driver.
- [ ] No new dependency; no identity/path leak; every Scenario a named passing test.

## Consequences

- (+) **Smaller, honest gateway**: one streaming-native in-process driver, one fewer framework dependency
  (and its transitive), and a blueprint that says one consistent thing.
- (+) **The convention is satisfied in spirit**: the pure-Go embedded driver is the real *and* the in-memory
  driver; the port's reality is its contract suite + interface-only consumers + reversibility — not a
  bolted-on second framework.
- (+) **Reversibility intact**: the `gateway.Gateway` port keeps an external-gateway / aggregation / Caddy
  driver a clean future add; the *useful* second driver (external-gateway, multi-node) is named for V2.
- (−) **The gateway is now a single-driver port** — a documented, justified exception to the ≥2-driver
  heuristic for an in-process port (the heuristic targets real-vs-in-memory infra splits, which an in-process
  proxy does not have). If a reviewer wants strict ≥2, the external-gateway driver is the answer, not Lura.
- (−) **API-aggregation is no longer one import away** — but it was never wired, and the port makes it a clean
  add if a real BFF need ever appears.
- (note) **Roadmap**: this is a gateway cleanup under F10 (no new build item); the external-gateway driver is a
  V2/FEAT-0001 note.

## Temporary workarounds

None. The removal is the permanent shape; the guard test + depguard deny are the permanent regression guards.

## Alternatives considered

- **Keep Lura as the optional driver** (ADR-0013's choice) — rejected: it's unused, wrong-shape,
  streaming-weak, a carried dependency, and doesn't fill the ≥2-driver intent. ADR-0013 itself parked it for
  this review; the review says drop.
- **Drop Lura and immediately add an external-gateway driver** (to keep a literal ≥2 drivers) — rejected for
  *this* ADR as scope: the external-gateway driver is a real multi-node (V2) feature with its own design
  (admin-API route programming, health, failover); bundling it here would overload a cleanup ADR. Named as the
  V2 second driver instead.
- **Accept the single-driver port silently** (just `rm` the package) — rejected: the ≥2-driver convention is a
  stated blueprint rule, so the single-driver exception must be *documented and justified* (the in-process
  real==in-memory argument), and the blueprint synced — not left as an unexplained gap.

## Open questions

| Question | Where it gets answered |
|---|---|
| The external-gateway driver (program routes into APISIX/Caddy via admin API) — the real ≥2nd driver | V2 / FEAT-0001 (multi-node) |
| API-aggregation / BFF (the original reason for Lura) | a future ADR *iff* a real need appears (the port makes it a clean add) |

## References

- [ADR-0013](0013-gateway-ingress-httputil-primary.md) (superseded by this ADR on the driver-set question;
  its httputil-primary / middleware / certmagic decisions are re-affirmed unchanged).
- [ADR-0002](0002-source-code-conventions-and-patterns.md) §ports/drivers — the ≥2-driver convention.
- [blueprint.md](../../blueprint.md) — "Ingress / API Gateway" (synced to a single embedded `httputil` driver).
