# ADR-0137 implementation review — per-caller `catalog::query` RBAC (claude-opus-4-8)

**Verdict**: **pass** — every model-attributable Contract/Scenario/DoD item is implemented, green, and
security-sound; the one Major (external ingress exposure not live) is **`adr`-attributed** (the ADR assumed
provider/ingress infrastructure the platform lacks) and is carded as a follow-up, not a model defect.
**Reviewed against**: ADR-0137 Contracts/Scenarios/Review-checklist/DoD · blueprint §providers/§ingress ·
ADR-0002 conventions · verified by running the four sub-checks + line-by-line security read (driver
verification; the ADR itself was independently judged pre-accept).
**Producing model**: claude-opus-4-8 · **phase**: implementation.

## Verification run (evidence)

- `nix develop -c go build ./...` → **exit 0**.
- `nix develop -c go tool golangci-lint run ./...` → **0 issues**.
- `nix develop -c go test ./...` → **0 failures** (the pre-existing `internal/catalog/embedengine`
  env-flake, from a locally-present engine tarball, is unrelated and excluded).
- `nix develop -c go mod verify` → **all modules verified**.
- identity/path/email leak grep over all changed/new files → **clean**.

## Scenario → test (all passing, hermetic)

`internal/catalog/gateway/{scenario,proxy,manager,handshake}_test.go` + `internal/services/catalog` +
`internal/function` cover every ADR Scenario: internal-fn-query-granted / -unbound-denied,
external-identity-query-granted / -denied, unknown-credential-denied, scope-bounds-the-grant,
**forged-function-token-denied** (B1), plus handshake round-trip + fail-closed, proxy allow/deny (upstream
never called), the per-catalog Manager listener lifecycle, and the per-function-token injection.

## ✅ Verified correct — keep

- **Token unforgeability (B1 fix, security-critical).** The per-function token is an HS256 JWT (go-jose,
  `internal/catalog/gateway/token.go`) signed with a SHA-256-derived node-master key; `PrincipalFor` verifies
  the signature with the **alg pinned to `[HS256]`** (kills alg-confusion) before trusting `{ns, fn}`. A forged
  `(ns,fn)` under the wrong key fails verification → denied. Constant-time comparison handled inside go-jose;
  the minted per-Identity token uses `hmac.Equal`.
- **Fail-closed handshake** (`handshake.go`): `encoding/binary` length codec, golden-fixture-tested; `ok=false`
  forwards un-swapped so the caller token (≠ engine token) is rejected — no engine-token smuggling.
- **PEP proxy** (`proxy.go`): resolve → `catalog::query` Authorize (resource = the endpoint's catalog, ns from
  the principal) → **403 on deny/unresolved with upstream never called**; the shared engine token is injected
  only post-allow and never exposed.
- **`catalog::query` is read-shaped → a Cedar permit** (not a `writers` entry); `builtin_catalog.cedar` binding
  permit is now **enforced** for internal functions via the node-private proxy (no longer vestigial).
- **S3-gateway-shape fidelity**: one proxy door, `principalFor` (JWT-verify→Function / lookup→Identity / deny),
  the backend secret used only after allow — mirrors `internal/blob/s3gateway`.

## Findings

### Major — `adr`-attributed (does NOT count against the model)
- **External ingress exposure of the proxy is not live** (ADR Decision 4). The platform cannot route the
  ingress to the proxy: the provider `programRoute` (`internal/provider/runtime.go:222`) hard-codes the upstream
  to the **engine** IP, and `api/types/v1alpha1/route.go`'s `RouteBackend` union (`Function | Static`) has **no
  node-private-upstream backend**; plus the documented replace-all gateway limitation (ADR-0087). ADR-0137's
  Decision 4 assumed a capability the platform lacks. **Internal** function→catalog per-caller PEP is fully
  live; the proxy logic already handles external `Identity` tokens (tested) — only the external **network path**
  is missing. Tracked as a follow-up/superseding ADR (board card *"External ingress exposure of the catalog PEP
  proxy"*). Reconciler keeps `Route: nil`.

### Minor — `adr`-attributed
- **Contract typo**: the ADR sketches `EngineTarget.Catalog` as `v1.EntityRef` (no such type); the impl
  correctly uses `auth.EntityRef` (the PDP resource type), noted in a code comment.

### Refinements folded in (decider-directed, strengthen the impl)
- Per-function token: the ADR's illustrative `base32(payload).base32(HMAC)` layout → a standard **HS256 JWT**
  (go-jose, already in the module graph, Apache-2.0) — the same MAC-authenticated-bearer-token contract via a
  vetted library instead of hand-rolled framing, at the decider's direction.
- Removed hand-rolled logic per decider review: `encoding/binary` for the handshake length codec, `strings.Cut`
  for token splits; the Cedar additions and the random-token minting already reused the framework/`randomSecret`.

## Definition of Done

Met for the model-attributable scope: four sub-checks green; every Scenario a named passing test;
`catalog::query` grants (binding + RolesAssignment) compile to permits; the proxy resolves both principal
kinds, PEPs, swaps, and never leaks the engine token; internal injection points at the proxy with a
per-function token (superseding ADR-0091's Decision-3). **Not met (adr-attributed, deferred)**: the external
ingress Route (infra gap above). Live containerd e2e deferred to the catalog lane per the ADR test plan.

## Recommendation

**Pass → Implemented** for the internal per-caller PEP + the proxy logic (the primary capability, live and
verified). The external ingress exposure is an `adr`-attributed infrastructure gap → a follow-up ADR (carded),
not a rework of this implementation.
