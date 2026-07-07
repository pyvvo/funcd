# funcd — Security Assessment: Summary Table

> **Status:** draft summary skeleton (2026-07-07). This table is the executive summary of the
> forthcoming full hardening report. Rows are grounded in the **as-built** stack — verified against
> the code and ADR acceptance status — not the blueprint's target state. Preliminary severities and
> directional recommendations are first-pass; the per-domain deep dive will expand each into detailed
> findings and confirm the ratings.

## Legend

**Implementation status**

- ✅ **Implemented** — present and wired in code (Accepted/Implemented ADR + verified package).
- 🟡 **Partial** — baseline built; hardening features (rotation, KMS, strong isolation) deferred, or wiring depth pending deep-dive confirmation.
- ⛔ **Design-only** — specified in the blueprint but **not built**; treated as a *design-gap finding* to harden before GA / before the relevant deployment mode (e.g. multi-node, untrusted workloads).

**Preliminary severity** — `H` High · `M` Medium · `L` Low · `I` Informational. `(gap)` marks a design-gap finding (risk realized only once the feature is built and exposed, or once the deployment mode it guards is reached).

## Scope focus

Cryptographic algorithms, mTLS / transport security, IAM (authn, RBAC, Cedar policy, workload identity), secret persistence, data persistence, ingress, and function-to-function communication — per the assessment brief.

## Summary table

| # | Security domain | As-built stack (mechanism in code) | Status | Focus | Prelim. severity | Directional hardening reco |
|---|---|---|---|---|---|---|
| 1 | **API authn** | Static bearer tokens + scoped API keys; in-memory `CredentialStore`; `crypto/subtle` constant-time compare. OIDC = V2 | ✅ | IAM | M | Add token expiry + rotation; hash credentials at rest; move to OIDC; per-key scoping audit |
| 2 | **Control-plane authz (RBAC)** | Built-in namespace-scoped RBAC, default-deny; `auth.Authorizer` PDP port (ADR-0018) | ✅ | IAM | L | Confirm default-deny coverage on every route; audit-log allow decisions |
| 3 | **Fine-grained authz (Cedar)** | `cedar-go v1.8.0`; `Policy` resources validated at admission; entities materialized from resources; KV r/w, fn→fn invoke, S3 (ADR-0074/75/76) | ✅ | IAM | M | Review built-in `permit` breadth (binding-as-grant); add policy unit-test corpus; verify no over-broad principal attributes |
| 4 | **Workload identity (internal IAM)** | SPIFFE-style short-lived tokens minted at worker boot, audience-bound (blueprint); connection-scoped `Ref` over UDS | 🟡 | IAM / crypto | H | **Confirm cryptographic binding** — signing keys, verification path, expiry, audience enforcement (may currently be UDS-connection trust, not signed tokens) |
| 5 | **Secrets at rest** | AES-256-GCM (stdlib), operator-provided key via config; `store.Encryptor` seam (ADR-0022). Envelope/KMS/rotation/OpenBAO = V2 | 🟡 | Crypto | M | Envelope encryption + KMS/OpenBAO; key rotation; keep master key out of plaintext config; per-secret DEKs |
| 6 | **Secret delivery (last-mile)** | PDP-authorized resolver → env-var injection (ADR-0057) | ✅ | IAM / crypto | M | Prefer tmpfs files over env vars (env leaks via `/proc`, children, crash dumps); scope to declared bindings only |
| 7 | **Persistence (metastore + KV)** | Badger pure-Go LSM, local file; at-rest encryption is **selective (secrets only)** (ADR-0065/66) | ✅ | Crypto / persistence | M | Offer full-store encryption option; document disk-encryption requirement; protect Badger dir perms |
| 8 | **Blob / S3 substrate** | `gocloud.dev/blob`; S3 frontend with SigV4 auth + funcd keypair identity (ADR-0080/85/88) | ✅ | Crypto / IAM | M | Verify SigV4 (replay window, clock-skew, canonicalization); manage/rotate funcd keypair; presign TTL review |
| 9 | **Messaging / bus** | Embedded NATS/JetStream; account-per-namespace tenancy model (ADR-0008) | 🟡 | IAM / network | M | Confirm per-namespace account credential scoping; ensure JetStream API never exposed to functions; TLS on external NATS |
| 10 | **Ingress gateway + TLS** | httputil reverse proxy, **plain HTTP**; routing + activator built. certmagic/TLS/ACME **absent from code + go.mod** | 🟡 | mTLS / TLS | H (gap) | Implement TLS termination (certmagic) before any non-loopback exposure; HSTS; TLS ≥1.2, modern ciphers |
| 11 | **Fn-to-fn RPC (links)** | `context.invoke` over per-sandbox UDS, connection-scoped identity, Cedar `link::invoke` (ADR-0064/75) | ✅ | IAM / network | M | Verify UDS socket permissions + peer-cred check; ensure caller identity is unspoofable at the broker |
| 12 | **Network / egress control** | No `internal/network` package. nftables default-deny, transparent egress proxy, SNI/DNS-aware policy — **not built** (V2+) | ⛔ | Network | H (gap) | Build L3/L4 default-deny + egress proxy before running untrusted code; fail-closed; per-namespace `EgressPolicy` enforcement |
| 13 | **Internal mTLS (CP ⇆ worker)** | Zero code references; blueprint defers to "once multi-node" | ⛔ | mTLS | H (gap) | mTLS (or SPIFFE X.509-SVID mTLS) on all CP↔worker RPC before any multi-node / networked deployment |
| 14 | **Runtime isolation** | containerd + crun; conservative OCI defaults, `no_new_privileges`, seccomp; process/wasm drivers. Kata microVM = V3 | 🟡 | Isolation | M | Audit dropped caps + seccomp profile; rootless + user namespaces; Kata/WASM path for untrusted workloads |
| 15 | **Artifact trust / supply chain** | OCI digest pinning at Revision stamp (ADR-0035). cosign/sigstore signature verification **not built** | 🟡 | Crypto / supply-chain | M | Add cosign/sigstore verification before worker start; pull-through cache; provenance/SBOM attestation |
| 16 | **Observability / audit** | Audit channel + OTel; funclog capture (ADR-0010/81) | ✅ | Audit | L | Ensure security decisions are all audited; tamper-evident / append-only audit stream; retention policy |

## Notes on the 🟡 / ⛔ rows (deep-dive targets)

- **#4 Workload identity** is the highest-value item to confirm first: the blueprint describes signed, audience-bound SPIFFE tokens, but the invoke/KV path currently authenticates via a **connection-scoped `Ref` over the per-sandbox UDS**. Whether cryptographic token minting/verification is wired (vs. relying on UDS connection trust) materially changes the internal-IAM risk picture.
- **#10 / #12 / #13** are the three transport/network design-gaps. They are correctly deferred per the blueprint's phasing (single-node V1), but each is a **hard prerequisite** before the deployment mode it guards: TLS before any non-loopback ingress, egress control before untrusted workloads, mTLS before multi-node.
- **#5 / #7** define the cryptographic at-rest posture: strong algorithm (AES-256-GCM) but a **single operator-supplied static key, no rotation, and encryption applied only to `Secret` resources** — the rest of the metastore/KV is plaintext-at-rest, relying on host disk protection.

## Method

Grounded in: `blueprint.md`; ADR acceptance status (`docs/adr/`, 0000–0106, of which the security-relevant set — 0011, 0018, 0022, 0057, 0064, 0065/66, 0074–76, 0080/85/88 — are Implemented); and direct code inspection (`internal/auth`, `internal/secrets`, `internal/controlplane/middleware`, `internal/blob/s3gateway`, dependency manifest). Absence claims (TLS/certmagic, `internal/network`, mTLS) were verified by dependency + source grep returning no matches as of 2026-07-07.
