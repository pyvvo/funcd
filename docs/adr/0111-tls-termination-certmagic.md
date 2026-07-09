# ADR-0111: TLS termination — automatic HTTPS for funcd's listeners (F74)

- **Status**: Implemented
- **Date**: 2026-07-08
- **Implemented**: 2026-07-08
- **Deciders**: green-0-rabbit
- **Tags**: edge, tls, https, certmagic, acme, security, ingress
- **Acceptance note**: judge Blocker folded (the default-posture inversion vs FEAT-0006's exit criterion is now explicit + deliberate — plaintext stays the back-compat default, `selfsigned`-stdlib is the enabled-default, certmagic-on-`acme`; the FEAT-0006 exit-criterion wording + F74 row are reconciled at acceptance) + Majors M1 (`selfsigned` is **one multi-SAN cert**, not per-host; `GetCertificate` returns it; SNI scenario verifies SANs) / M2 (`Provider` gains `Close` → certmagic `Cache.Stop()`, wired into `Platform.Shutdown`; acme keeps certmagic's `NextProtos`/`acme-tls/1` and uses TLS-ALPN-01). Minors folded (`TLSConfig()` dropped its unused ctx; F74 per-Route-mode → V2 reconciled).
- **Realizes**: [FEAT-0006/F74](../feat/0006-feat-ingress-hardening.md) — TLS termination / automatic HTTPS for the data-plane and control-plane listeners.
- **Relates to**: [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (F79 — the edge Router whose `Hosts()` set drives per-host certs), [ADR-0013](0013-gateway-ingress-httputil-primary.md) (named + deferred the certmagic direction), [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (the data-plane listener that gains TLS), [ADR-0028](0028-control-plane-api-server.md) (the control-plane listener).

## Context & Need

funcd's two listeners — the control-plane API (ADR-0028) and the data-plane function ingress
(ADR-0033) — are **plaintext HTTP**. ADR-0013 named embedded `certmagic` as the TLS direction and
deferred it; the deferral never landed. F79 just made edge exposure declarative and default-deny;
serving that edge over plaintext is the remaining hole (credentials, request bodies, and — once F77
lands — bearer tokens all cross the wire in the clear).

**Purpose:** give funcd's own `http.Server`s a `*tls.Config` so both listeners serve **HTTPS**, with
no external terminator and no listener handover (funcd keeps `net.Listen` + `http.Server`, calling
`ServeTLS`). Three issuance modes cover the deployments: **`selfsigned`** (default — a self-signed
cert funcd generates and persists; zero-config HTTPS on a LAN/homebox, offline), **`provided`** (an
operator-supplied cert + key), and **`acme`** (automatic public certificates via Let's Encrypt /
certmagic). h2 falls out of ALPN. Per-host certs are served by SNI, covering the F79 Route hosts.

Callers: the operator enabling TLS (funcdconfig / `WithTLS`). The provider is consulted once at
startup to build the `*tls.Config`; certmagic (acme mode only) renews in the background.

**Dependency note:** certmagic ships only ACME/ZeroSSL issuers — it has **no internal-CA issuer** —
so `selfsigned` and `provided` are **pure stdlib** (`crypto/x509`, `crypto/tls`) and certmagic is
imported **only** for `acme`. The hermetic core (the default + provided modes) has no new dependency
in its path.

## Scenarios

Each becomes a named acceptance test.

- `selfsigned-serves-https` — Given TLS mode `selfsigned` for host `funcd.local`, When a client that
  trusts the generated cert GETs over HTTPS, Then the handshake completes and the request is served.
- `selfsigned-persists` — Given `selfsigned` and a data dir, When funcd restarts, Then it reuses the
  persisted cert/key (no new cert), so clients that pinned the first one still trust it.
- `provided-serves-https` — Given mode `provided` with an operator cert+key, When a client trusting
  that CA connects over HTTPS, Then the handshake completes and the request is served.
- `alpn-negotiates-h2` — Given TLS enabled, Then the `*tls.Config` advertises `h2`,`http/1.1` and an
  HTTP/2 client negotiates `h2`.
- `plaintext-opt-out` — Given TLS disabled (the default until opted in), Then the listeners serve
  plain HTTP exactly as before (back-compat).
- `acme-config-built` — Given mode `acme` with an email + CA directory, Then the provider builds a
  certmagic config with an ACME issuer for those hosts (unit-level: the config is well-formed; live
  issuance is the deferred integration test).
- `sni-serves-route-hosts` — Given mode `selfsigned` and two F79 Route hosts, Then the one multi-SAN
  cert the provider serves has **both** hosts in its SANs (a handshake with either SNI host verifies).
- `tls-invoke-e2e` — Given a funcd with `selfsigned` TLS and a deployed function, When it is invoked
  over HTTPS at the data-plane listener, Then the full hop completes and `resp.TLS` is set.

## Scope

**In:** an `internal/edge/tls` provider port + two drivers (`static` = provided|selfsigned on stdlib;
`acme` = certmagic); a process-level TLS config (`WithTLS(spec)` + funcdconfig `tls:` block) enabling
HTTPS on **both** listeners via `ServeTLS` (no handover); self-signed cert generation + persistence
under `<dataDir>/tls`; SNI cert selection over the F79 `Router.Hosts()` set (+ a configured host
list); ALPN h2; plaintext as the default opt-out.

**Out (named follow-ons):**
- **Live ACME issuance e2e** — deferred to a `FUNCD_IT` Pebble (Let's Encrypt test server) lane; V1
  ships the `acme` wiring + a config unit test. Exit criterion: the Pebble lane is added.
- **Per-Route / per-namespace TLS *mode* override** — V1 TLS mode is a listener/deployment-level
  decision (one terminator, per-host certs via SNI). A per-Route `tls` field is a V2 refinement; the
  F79 `edgeDefaults` container stays available for it.
- **mTLS / client-cert auth** — F77 (bearer) then FEAT-0002 (mTLS). **SNI-based routing** — FEAT-0002.

## Constraints & Decision drivers

- **No listener handover** (ADR-0013 / blueprint.md:216): funcd owns its `http.Server`s and their
  graceful shutdown; TLS is a `*tls.Config` we attach, not a server certmagic runs.
- **Hermetic default.** The default + provided modes must be testable with no network (self-signed in
  a temp dir), so `just ci` stays green offline; only live ACME is deferred.
- **Homebox is LAN-only** (no public DNS): the default mode must work offline → `selfsigned`.
- **Apache-2.0/MIT deps only** — certmagic is Apache-2.0 (verified).
- **Reuse F79's host set** — the Router already knows the Route hosts (`Hosts()`); `selfsigned`/`provided`
  cover them with **one multi-SAN cert**, `acme` obtains a per-host cert map (certmagic).

## Alternatives considered

| Option | Verdict |
|---|---|
| **Provider port + static(stdlib) + acme(certmagic) drivers, `ServeTLS` on our servers (chosen).** | Hermetic default, certmagic only on the acme path, no handover. |
| certmagic's internal CA for the LAN default. | Rejected — certmagic v0.25 has **no** internal-CA issuer (only ACME/ZeroSSL); a self-signed stdlib cert is simpler and dependency-free. |
| Hand certmagic the listener (`certmagic.HTTPS`/`Listen`). | Rejected — it takes over the server, breaking funcd's graceful shutdown + the middleware chain. We take its `TLSConfig()` and drive our own `ServeTLS`. |
| Terminate TLS at an external reverse proxy. | Rejected — funcd is single-binary, embed-first (blueprint); an external terminator contradicts the deployment model. |
| Per-Route TLS mode in V1. | Rejected — one listener terminates TLS; mode is deployment-level, per-host certs are SNI. Per-route override deferred to V2. |

## Decision

1. **`internal/edge/tls` provider port** — `Provider.TLSConfig(ctx) (*tls.Config, error)` returns the
   config to attach to an `http.Server` (GetCertificate-driven, `NextProtos=[h2,http/1.1]`), and
   `Manage(ctx, hosts)` ensures/authorizes certs for a host set (fed the F79 `Router.Hosts()` +
   config hosts).
2. **Two drivers.** `static` (mode `provided`: `tls.LoadX509KeyPair`; mode `selfsigned`: generate
   **one multi-SAN** self-signed cert covering the whole host set via `crypto/x509`, persist under
   `<dataDir>/tls`, reload on restart) — pure stdlib. `acme` (mode `acme`: a certmagic `Config` with
   an `ACMEIssuer`, TLS-ALPN-01 on the same listener; returns certmagic's own `TLSConfig()` + `ManageAsync`).
3. **Wire into both listeners.** When TLS is enabled, `pkg/funcd` sets `srv.TLSConfig = cfg` on the
   control-plane and data-plane servers and calls `srv.ServeTLS(ln, "", "")` (empty file args →
   GetCertificate). Disabled ⇒ `Serve(ln)` (today's plaintext). `Platform.Shutdown` calls the
   provider's `Close` so the acme renewal goroutine is stopped.
4. **Default `selfsigned`; plaintext is the back-compat default — a deliberate, acknowledged
   inversion of the feat exit criterion.** FEAT-0006's exit criterion reads *"HTTPS via embedded
   certmagic; plaintext is an explicit opt-out."* This ADR **intentionally decides the opposite on
   both counts**, because flipping a **browser-untrusted self-signed cert on by default would break
   every existing plaintext client** — strictly worse than a documented plaintext default — and the
   hermetic default is stdlib, not certmagic (certmagic has no offline issuer). So: TLS is **off until
   enabled** (plaintext default, zero breakage on upgrade); once enabled, the **default mode is
   `selfsigned`** (offline, zero-config). TLS-on-by-default is a **phased** V2 option (a namespace/
   deployment stance, mirroring how F79 phased exposure). **This inversion is reconciled into the
   FEAT-0006 exit-criterion wording + the F74 row at acceptance** (the propagation this ADR owes).
5. **certmagic storage** at `<dataDir>/tls` (`FileStorage`), matching the engine-owned-dir pattern.

## Temporary workarounds

- **Live ACME issuance is not e2e-tested in V1.** The `acme` mode is wired and its config unit-tested;
  real issuance rides a network. **Exit criterion:** a `FUNCD_IT` Pebble lane exercises live issuance
  (added as a follow-up); until then `acme` is documented as integration-verified-by-config-only.
- **Single listener-level mode.** Per-Route TLS override is deferred. **Exit criterion:** a V2 ADR
  adds a `RouteSpec.tls` field + per-host mode resolution when a real multi-mode-per-deployment need
  appears.

## Contracts

### Provider (internal/edge/tls)

```go
type Mode string

const (
	ModeSelfSigned Mode = "selfsigned" // default when TLS is enabled: generated + persisted (stdlib)
	ModeProvided   Mode = "provided"   // operator cert + key (stdlib)
	ModeACME       Mode = "acme"       // certmagic ACME (public HTTPS)
)

type Spec struct {
	Mode       Mode
	Hosts      []string // configured hosts (merged with the F79 Router.Hosts() at Manage time)
	CertFile   string   // provided: PEM cert (chain)
	KeyFile    string   // provided: PEM key
	Email      string   // acme: ACME account email
	CADir      string   // acme: ACME directory URL (default Let's Encrypt); set to Pebble in tests
	StorageDir string   // storage/persistence dir (default <dataDir>/tls)
}

// Provider builds the *tls.Config funcd attaches to its http.Servers (no listener handover).
type Provider interface {
	// TLSConfig returns the config to attach to an http.Server. static sets
	// NextProtos=[h2,http/1.1]; acme returns certmagic's cfg.TLSConfig() UNMODIFIED (it already
	// advertises h2/http1.1 PLUS acme-tls/1 for the TLS-ALPN-01 challenge — overriding NextProtos
	// would break issuance). No ctx: the static path needs none and certmagic's TLSConfig takes none.
	TLSConfig() (*tls.Config, error)
	// Manage ensures/authorizes certs for the host set (Router.Hosts() + Spec.Hosts). static
	// (re)generates/loads the persisted cert; acme ManageAsync's the hosts.
	Manage(ctx context.Context, hosts []string) error
	// Close halts background work (acme: certmagic Cache.Stop(); static: no-op). Wired into
	// Platform.Shutdown so the acme renewal goroutine does not leak.
	Close(ctx context.Context) error
}

// New builds the provider for a Spec (selects the driver by Mode).
func New(spec Spec, logger *slog.Logger) (Provider, error)
```

**`selfsigned` cert model (Major-fold):** `selfsigned` generates **one multi-SAN self-signed cert**
covering the whole host set (config + `Router.Hosts()`), persisted under `StorageDir`. Its
`GetCertificate` returns that single cert regardless of SNI (a multi-SAN cert already matches every
managed host); the `sni-serves-route-hosts` scenario asserts the returned cert's SANs cover the
requested host. `provided` likewise serves the one operator cert. `acme` is the only mode with a
genuine per-host cert map (certmagic's `GetCertificate`). This removes the "cert per host" ambiguity.

**`acme` challenge/lifecycle (Major-fold):** the acme driver uses **TLS-ALPN-01** on the same TLS
listener (no extra HTTP-01 port — single-binary friendly), which is why its `TLSConfig()` must keep
certmagic's `acme-tls/1` ALPN. `Manage` calls `ManageAsync` (starts the renewal goroutine); `Close`
calls the certmagic `Cache.Stop()` to halt it on shutdown.

### Dependencies & I/O

| Consumes | Produces |
|---|---|
| `crypto/tls`, `crypto/x509` (static) · `github.com/caddyserver/certmagic` v0.25 Apache-2.0 (acme only) · the F79 `router.Router.Hosts()` · funcdconfig `tls:` / `WithTLS` · `<dataDir>/tls` storage | a `*tls.Config` attached to both `http.Server`s via `ServeTLS`; persisted self-signed cert/key; background ACME renewal (acme mode) |

## Implementation plan

**Files:**
- `internal/edge/tls/tls.go` — the `Provider` port + `Mode`/`Spec` + `New` facade.
- `internal/edge/tls/static/static.go` — provided + selfsigned (stdlib): generate/persist/load; `GetCertificate` by SNI.
- `internal/edge/tls/acme/acme.go` — certmagic config + `TLSConfig()`/`ManageAsync`.
- `pkg/funcd/funcd.go` — `WithTLS(spec)` option + funcdconfig `tls:`; build the provider, `Manage(router.Hosts())`, set `TLSConfig`, `ServeTLS` on both listeners.
- `cmd/funcd` / funcdconfig — the `tls:` block (mode + cert files/email/CA).

**Deps:** `github.com/caddyserver/certmagic v0.25.4` (Apache-2.0, already `go get`-fetched).

**Test plan** — one named test per Scenario:
- `internal/edge/tls/static` unit: `selfsigned-serves-https` (generate → `httptest` TLS server → client with the gen'd root → 200 + `resp.TLS`), `selfsigned-persists` (regenerate reuses the file), `provided-serves-https` (test CA+leaf), `sni-serves-route-hosts`, `alpn-negotiates-h2`.
- `internal/edge/tls/acme` unit: `acme-config-built` (config has an ACMEIssuer for the hosts; no dial).
- **Go e2e** over `pkg/funcd`: `tls-invoke-e2e` (selfsigned TLS + a deployed function → HTTPS invoke succeeds, `resp.TLS != nil`), `plaintext-opt-out`.
- **Venom containerd lane**: enable `selfsigned` TLS, `curl --cacert <root>` the data-plane over HTTPS → function serves.
- **Deferred** (recorded): live ACME issuance → a `FUNCD_IT` Pebble lane.

**Definition of done:** all non-deferred scenario tests green; `go build/test/lint/mod` green; the Go e2e + the Venom TLS lane green; certmagic pinned + recorded; F74 row `→ reviewing`; no identity/path leak.

## Review checklist

- [ ] `Provider`/`Spec`/`Mode`/`Close` match the Contracts; no `any` in exported signatures; certmagic imported only under `acme`.
- [ ] `selfsigned` generates **one multi-SAN** cert covering the host set, **persists** under `<dataDir>/tls`, reloads on restart.
- [ ] `provided` loads the operator cert; a bad cert/key is a clear `fault.Invalid`, not a panic.
- [ ] both listeners use `ServeTLS` (no handover); graceful `Shutdown` works over TLS + calls `provider.Close`; plaintext is the default when TLS is disabled.
- [ ] static `TLSConfig` advertises `h2`+`http/1.1`; acme returns certmagic's `TLSConfig()` **unmodified** (keeps `acme-tls/1`); `sni-serves-route-hosts` verifies the SANs.
- [ ] `acme` builds a well-formed certmagic config (unit, TLS-ALPN-01); live issuance deferral recorded; `Close` → `Cache.Stop()`.
- [ ] Go e2e proves an HTTPS invoke; Venom lane curls over TLS; F74 row advanced + the FEAT-0006 exit-criterion wording reconciled.

## Consequences

- **(+)** Both listeners serve HTTPS; the default is offline zero-config (`selfsigned`), so homebox
  gets TLS with no DNS/ACME; `provided` + `acme` cover operator-PKI and public deployments; h2 free.
- **(+)** certmagic is confined to the `acme` path — the hermetic core (default) stays stdlib.
- **(−)** A new dependency (certmagic + its transitive ACME/dns libs) enters `go.mod`, used only for acme.
- **(−/deferred)** Live ACME isn't e2e-tested in V1 (Pebble lane follow-up).
- **(risk)** Self-signed certs aren't trusted by browsers by default — expected for a LAN default;
  operators wanting public trust choose `acme`. Documented, not a defect.

## Open questions

- **On-demand vs pre-obtained certs (acme)** — V1 pre-obtains for `Router.Hosts()` at startup +
  `ManageAsync`; certmagic on-demand (issue at first SNI hit, gated by a Route-host allowlist) is a
  possible refinement if hosts churn a lot.
- **Cert rotation for `selfsigned`** — V1 persists one long-lived self-signed cert; auto-rotation
  before expiry is a follow-up (regenerate on near-expiry at startup).

## References

- [FEAT-0006/F74](../feat/0006-feat-ingress-hardening.md) · [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (Router.Hosts()) · [ADR-0013](0013-gateway-ingress-httputil-primary.md) (deferred certmagic) · [ADR-0033](0033-data-plane-serving-and-trigger-wake.md)/[ADR-0028](0028-control-plane-api-server.md) (the listeners).
- [certmagic](https://github.com/caddyserver/certmagic) v0.25.4 (Apache-2.0, verified via GitHub API). Pebble (Let's Encrypt test ACME server) for the deferred live-issuance lane.
