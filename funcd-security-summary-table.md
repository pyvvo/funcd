# funcd — Security Assessment: Summary Table

> **Status:** confirmed (2026-07-07). Statuses and severities are **firmed by the full deep dive**
> ([funcd-security-deep-dive.md](funcd-security-deep-dive.md)) — direct source inspection plus dependency/source
> greps for every absence claim. Rows are grounded in the **as-built** stack, not the blueprint's target state
> (the deep dive found four blueprint claims that over-state the as-built posture — see its §2.2). **Bold** marks
> cells changed from the pre-dive draft. The last column links each row's detailed finding.

## Legend

**Implementation status**

- ✅ **Implemented** — present and wired in code (Accepted/Implemented ADR + verified package).
- 🟡 **Partial** — baseline built; hardening features (rotation, KMS, strong isolation) deferred or unwired.
- ⛔ **Design-only** — specified in the blueprint but **not built**; a *design-gap finding* to close before the deployment mode it guards (non-loopback exposure, untrusted code, multi-node).

**Severity** — `H` High · `M` Medium · `L` Low · `I` Informational. `(gap)` marks a design-gap finding (risk realized once the feature is built and exposed, or once the deployment mode it guards is reached). Severity reflects risk **if the platform were exposed as-is**, not a claim of oversight — most gaps are deferred by design for a single-node V1.

## Scope focus

Cryptographic algorithms, mTLS / transport security, IAM (authn, RBAC, Cedar policy, workload identity), secret persistence, data persistence, ingress, and function-to-function communication — per the assessment brief.

## Summary table

| # | Security domain | As-built stack (mechanism in code) | Status | Focus | Severity | Directional hardening reco | Deep dive |
|---|---|---|---|---|---|---|---|
| 1 | **API authn** | Static bearer tokens + scoped API keys; in-memory `CredentialStore`; `crypto/subtle` constant-time compare. **Tokens plaintext in RAM; no expiry/rotation/revocation.** OIDC = V2 | ✅ | IAM | M | Hash tokens in the credential map; expiry + rotation + revocation; refuse `DevToken` on non-loopback bind | §5.1 |
| 2 | **Control-plane authz (RBAC)** | Built-in namespace-scoped RBAC, genuine default-deny; `auth.Authorizer` PDP port (ADR-0018); `admin` is unscoped allow-all (by design) | ✅ | IAM | L | Audit/alert on every `admin` decision; namespace-scoped admin in V2 | §5.2 |
| 3 | **Fine-grained authz (Cedar)** | `cedar-go v1.8.0`; policies validated at admission; entities materialized from resources; KV r/w, fn→fn invoke, S3 (ADR-0074/75/76). **Write built-ins are permit-then-forbid → fail *open* if `owner` unset** | ✅ | IAM | M | Guard write built-ins against a missing `owner`; namespace-containment check at admission; isolate a corrupt stored policy | §5.3 |
| 4 | **Workload identity (internal IAM)** | **Confirmed: UDS connection-trust only — no signed tokens, no SPIFFE/JWT, zero crypto.** Caller `Ref` baked into per-sandbox socket handler; no peer-cred check, no socket `chmod`. Blueprint's minted-token model is not built | **⛔** | IAM / crypto | **H** | `chmod 0600` sockets + `SO_PEERCRED` now; mint short-lived audience-bound signed credentials (Ed25519/SPIFFE) as the real fix | §5.4 |
| 5 | **Secrets at rest** | AES-256-GCM (stdlib, correct construction), operator key via `store.Encryptor` seam (ADR-0022). **Key file loaded with no permission check (H quick-fix); no rotation/envelope/AAD** | 🟡 | Crypto | M | Refuse start on group/other-readable key file; KEK/DEK envelope; store key as AAD | §5.5 |
| 6 | **Secret delivery (last-mile)** | PDP-authorized resolver → env-var injection (ADR-0057). **Namespace-coarse authz → intra-ns over-delivery; env visible in `/proc`, inherited by children** | ✅ | IAM / crypto | M | Per-secret/per-function binding authz (V2 `Grant`); prefer tmpfs (0400) over env vars | §5.6 |
| 7 | **Persistence (metastore + KV)** | Badger pure-Go LSM, local file; at-rest encryption **selective — `Secret` only**; rest of the store is plaintext JSON on disk (ADR-0065/66) | ✅ | Crypto / persistence | M | Add ConfigMaps to the encrypted set or enable Badger block-encryption; document disk-encryption requirement | §5.6 |
| 8 | **Blob / S3 substrate** | `gocloud.dev/blob`; SigV4 via `versitygw v1.6.0` (**confirmed: ±15 min replay window; layered Cedar + key-prefix tenant isolation; HMAC-derived non-forgeable per-tenant keys**); plaintext listener; master-key rotation global-only | ✅ | Crypto / IAM | M | Wire `WithTLS` + non-loopback startup guard; per-`(ns,fn)` revocation; confirm/cap presign TTL | §5.7 |
| 9 | **Messaging / bus** | Embedded NATS/JetStream. **Confirmed: no auth, no TLS, real TCP port (random, all-interfaces default), full JetStream admin exposed to any network-adjacent client; account-per-namespace (ADR-0008) unimplemented** | 🟡 | IAM / network | **H** | Bind to loopback/UDS immediately; TLS + auth (accounts/nkeys) before any networked control plane | §5.10 |
| 10 | **Ingress gateway + TLS** | httputil reverse proxy, plain HTTP; routing + activator built; certmagic/TLS absent from code + go.mod, **no TLS knob in the config schema**. **Data-plane invocation is unauthenticated on `0.0.0.0:8081`; namespace is a self-asserted header** | 🟡 | mTLS / TLS | **H** | TLS termination before any non-loopback exposure; data-plane auth PEP; stop trusting `X-Funcd-Namespace`; bound activator buffer + rate limiting | §5.10 |
| 11 | **Fn-to-fn RPC (links)** | `context.invoke` over per-sandbox UDS, connection-scoped identity, Cedar `link::invoke` (ADR-0064/75). **Authenticated only by the socket bind-mount; process-mode = any function fully impersonates any other** | ✅ | IAM / network | **H** | Socket perms + peer-cred check; signed workload identity layered over connection trust; gate process-mode to trusted dev only | §5.4 |
| 12 | **Network / egress control** | No `internal/network` package. nftables default-deny, transparent egress proxy, SNI/DNS-aware policy — **not built** (V2+). Compromised function has unrestricted egress + lateral reach | ⛔ | Network | H (gap) | Build L3/L4 default-deny + egress proxy, fail-closed, **before running untrusted code** | §5.11 |
| 13 | **Internal mTLS (CP ⇆ worker)** | Zero code references; blueprint defers to "once multi-node" | ⛔ | mTLS | H (gap) | SPIFFE X.509-SVID mTLS on all CP↔worker RPC **before any multi-node deployment** — connection-trust identity is zero security over a network | §5.11 |
| 14 | **Runtime isolation** | containerd + crun, own namespaces, `no_new_privileges` — and **nothing else. Confirmed: no seccomp (blueprint claim false), no capability drop, no CPU/mem/PID limits for functions → trivial host DoS.** Process driver = zero isolation (dev-only) | 🟡 | Isolation | **H** | `seccomp.WithDefaultProfile()`; drop-all caps; `spec.resources` → `WorkerSpec.Limits`; pids limit; read-only rootfs + sized tmpfs; enforce non-root | §5.8 |
| 15 | **Artifact trust / supply chain** | OCI digest pinning at Revision stamp, immutable, re-verified on pull (ADR-0035) — integrity ✓. **No signature/provenance verification, no registry allowlist, no pull-through cache** (blueprint over-states) | 🟡 | Crypto / supply-chain | M **(→H under untrusted/multi-tenant)** | cosign/notation gate per namespace policy; registry allowlist as interim; provenance/SBOM attestation | §5.9 |
| 16 | **Observability / audit** | Audit channel + OTel; funclog capture (ADR-0010/81). **Confirmed: the typed, leak-safe `AuditRecorder` exists but is never constructed — zero security decisions audited at runtime** | **🟡** | Audit | **M** | Wire the recorder into authn middleware + RBAC/Cedar PDP call sites (allow **and** deny); durable append-only sink + retention (V2 audit ADR) | §5.12 |

## Notes on the firmed rows

- **#4 Workload identity — the pre-dive open question is resolved, in the bad direction.** The blueprint's
  signed, audience-bound SPIFFE-style tokens are **not built**; grep for `spiffe|jwt|jose|ed25519|MintToken|audience`
  finds only blueprint prose and transitive `go.sum` entries. Internal identity is UDS connection trust with no
  peer-cred check — hence 🟡→⛔ and `H`. It collapses entirely in process-mode (#11) and blocks multi-node (#13).
- **#9 / #10 / #14 — the three "confirmed worse than drafted" rows.** Embedded NATS has no auth/TLS on a real
  TCP port with JetStream admin exposed; the data plane is unauthenticated on `0.0.0.0:8081` with a self-asserted
  namespace header; function containers run with no seccomp, default capabilities, and no resource limits.
  Each was verified first-hand (options struct, middleware chain, `ociOpts`).
- **#16 — new finding from the deep dive:** the audit type is well-designed (refs not payloads, dedicated
  stream) but unwired; there is currently no security audit trail at all.
- **#10 / #12 / #13** remain the transport/network design-gaps, each a **hard prerequisite** for the deployment
  mode it guards: TLS before any non-loopback ingress, egress control before untrusted workloads, mTLS before
  multi-node.
- **#5 / #7** define the at-rest posture: strong algorithm (AES-256-GCM, correct stdlib use) but a single static
  operator key **loaded without a permission check**, no rotation/envelope/AAD, and encryption applied only to
  `Secret` — the rest of the metastore/KV is plaintext-at-rest.
- **Blueprint drift (deep dive §2.2):** four controls the blueprint asserts in the present tense are not
  implemented — signed workload tokens, default seccomp, per-namespace NATS accounts, cosign verification +
  pull-through cache. Sync per the repo's propagation rules.

**Deployment guidance in one line:** as-built, funcd is safe **only** as a single-tenant daemon on a trusted
host with all listeners bound to loopback and no untrusted function code. The gate-ordered remediation roadmap
is the deep dive's §6.

## Method

Grounded in: `blueprint.md`; ADR acceptance status (`docs/adr/`, 0000–0106, of which the security-relevant set —
0011, 0018, 0022, 0057, 0064, 0065/66, 0074–76, 0080/85/88 — are Implemented); and direct code inspection
(`internal/auth`, `internal/secrets`, `internal/controlplane`, `internal/blob/s3gateway`, `internal/runtime`,
`internal/bus/nats`, `internal/gateway`, `internal/activator`, `internal/workernode/local`, `internal/artifact`,
`internal/platform/observability`, `shim/*`, dependency manifest). Absence claims (TLS/certmagic,
`internal/network`, mTLS, seccomp/caps/limits, cosign/sigstore, NATS accounts, audit wiring) were verified by
dependency + source grep returning no matches as of 2026-07-07. Full findings:
[funcd-security-deep-dive.md](funcd-security-deep-dive.md).
