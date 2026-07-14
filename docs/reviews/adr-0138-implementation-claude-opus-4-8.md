# ADR-0138 Implementation Review — External edge exposure of the `catalog::query` PEP proxy

**Verdict**: **pass** — the corrected (Route-v2) design is implemented, conforms to the Contracts, and is proven
end-to-end on real containerd. Every Scenario has a named passing test; the four sub-checks are green.

**Producing model**: claude-opus-4-8
**Reviewed against**: ADR-0138 Contracts / Scenarios / Review checklist · blueprint (catalog + edge) · ADR-0110
(Route-v2 edge) · ADR-0137 (the PEP proxy it exposes) · ADR-0002 conventions.

## Verification run (evidence)

- `go build ./...` → exit 0. `go test ./...` → exit 0 (full suite). `go test -race ./internal/edge/... ./internal/dataplane/... ./internal/route/... ./internal/services/catalog/...` → exit 0. `go tool golangci-lint run …` → **0 issues**. `go mod verify` → all modules verified.
- OpenAPI golden regenerated (`just generate`) after the additive `CatalogIngress`; `TestSpecGeneratedFromGo` green.
- **e2e (real containerd, `just lima-example duckdb`, fresh colima)** — all four testcases PASS:
  provider Ready · engine serves Quack + S3 authorized · **internal** consumer SQL round-trip `[[42]]` · **ADR-0138 external edge**: `/catalog/lake` reaches the PEP proxy (200 reachability), an unexposed path is 404 (control), and a garbage-token Quack handshake is **fail-closed 403** (default-deny enforced at the edge).

## Scenario → test traceability (all passing)

| Scenario | Test |
|---|---|
| routes-coexist / source-isolation / remove | `TestAggregator_RoutesCoexist` / `_SourceIsolation` / `_Remove` |
| aggregator-concurrent-safe (`-race`) | `TestAggregator_ConcurrentSafe` |
| aggregator-program-failure-atomic | `TestAggregator_ProgramFailureAtomic` |
| router-resolves-upstream | `TestRouter_ResolvesUpstream` |
| dataplane-serves-upstream | `TestScenarioDataPlaneServesUpstreamBackend` / `_UpstreamUnreachable` |
| catalog-external-route-targets-proxy / not-exposed / teardown | `TestReconcile_ExternalRouteTargetsProxy` / `_NotExposedNoRoute` / `_ExternalTeardown` |
| CatalogService validate | `TestCatalogServiceValidate_ingress` |
| external-caller-pep (e2e) | duckdb venom lane testcase 4 |

## ✅ Verified correct — keep

- **Security model holds.** The edge entry targets the PEP **proxy**, never the engine; the proxy PEPs
  `catalog::query` (e2e-proven 403 on a bad token). Exposure is opt-in/default-closed. The node-private Upstream
  backend is **not** a user-facing `RouteBackend` arm — no tenant can point the edge at an arbitrary in-daemon
  address (SSRF avoided). This is the load-bearing property; do not "simplify" it into a user CRD field.
- **The aggregator is the sole `Program` writer**; per-source partitions + union + single locked Program; `-race`
  clean; failure-atomic recovery. The clobber is genuinely gone (function/user Routes coexist with the catalog).
- **Additive, back-compat.** `spec.ingress` nil ⇒ no edge entry (tested); the data-plane Upstream branch is inert
  without an Upstream match; the reverse-proxy strips the matched prefix (tested).

## Findings

### Blocker / Major — None.

### Minor
- The `internal/gateway` port remains vestigial (unmounted) — this ADR correctly does not use it, but the dead
  port + the provider's `programRoute` writing to it is pre-existing cleanup debt (out of scope here; board-worthy).

## Attribution note (lifecycle)

The **first** implementation draft aggregated `internal/gateway` — an **`adr`-attributed** design defect (that port
is unmounted; the live edge is Route-v2). It was caught by the e2e (404) and **corrected in-session by
decider-authorized rework in place** (not a superseding ADR, since ADR-0138 was uncommitted). The shipped design
(this review) is the corrected one. No `model`-attributed Blocker/Major survives; the defect was in the ADR's
initial mechanism, surfaced and fixed before commit.

## Recommendation

**Advance to `Implemented`.** The implementation is complete, conventional, secure, and e2e-proven on real
containerd. Stamp ADR-0138 `Reviewing → Implemented`; the realizing F102 row stays `implemented` (ADR-0138 linked
as the external-edge realizer). Move the board card to Done.
