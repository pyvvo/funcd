# funcd — Security Deep-Dive & Hardening Plan

> **Date:** 2026-07-07 · **Scope:** cryptography, mTLS/transport, IAM (authn/RBAC/Cedar/workload identity),
> secret & data persistence, ingress, function-to-function communication, runtime isolation, supply chain.
> **Basis:** direct source inspection of the `internal/`, `pkg/`, `cmd/`, `shim/` trees + ADR status
> (0000–0106) as of this date. Absence claims (TLS, seccomp, egress, signing) were confirmed by dependency
> and source grep returning no matches. All paths are repo-root-relative.
>
> **Reading note:** funcd is an honest, well-architected **single-node V1**. Most gaps below are *deferred by
> design* and documented as such in the ADRs. The value of this report is (a) firming which deferrals are
> **hard prerequisites** for a given deployment mode, and (b) flagging where the **blueprint over-states** the
> as-built posture. Severity reflects risk **if the platform were exposed as-is**, not a claim that the authors
> missed something.

---

## 1. Executive summary

The **authorization** core is genuinely strong: one PDP behind a port, real default-deny in both the RBAC and
Cedar drivers, server-derived (never client-asserted) principals, no policy-injection primitive, constant-time
credential comparison, and a shared conformance suite that pins the default-deny guarantee. The **at-rest secret
encryption** uses a correct stdlib AES-256-GCM construction. These are the platform's security strengths and
they are well-tested.

The material risks cluster in **transport, authentication of the data plane, workload identity, and runtime
isolation** — the layers the blueprint marks "V2 / multi-node" but which are load-bearing the moment the platform
is exposed beyond a trusted single host. The six headline findings:

| # | Headline finding | Severity |
|---|---|---|
| A | **Function invocation (data plane) is completely unauthenticated** and bound to `0.0.0.0:8081`; namespace is a self-asserted header | **H** |
| B | **No TLS on any listener** — control-plane bearer tokens and function payloads (incl. injected secrets) travel in cleartext | **H** |
| C | **Embedded NATS has no auth and no TLS**, on a real TCP port — full JetStream admin reachable by any network-adjacent client | **H** |
| D | **Internal workload identity is UNIX-socket connection-trust, not signed tokens** — no crypto; collapses in process-mode; blocks multi-node | **H** |
| E | **Function containers have no seccomp, no dropped capabilities, and no CPU/memory/PID limits** — weak isolation + trivial host DoS | **H** |
| F | **No artifact signature/provenance verification** before a worker runs code (digest pinning is integrity, not authenticity) | **M → H** under untrusted/multi-tenant |

Plus three **design-gaps** correctly deferred but each a prerequisite gate: **ingress TLS** (before any
non-loopback exposure), **egress/network control** (before untrusted code), **internal mTLS** (before multi-node).

**Deployment guidance in one line:** as-built, funcd is safe **only** as a single-tenant daemon on a trusted host
with all listeners bound to loopback and no untrusted function code. Every step beyond that (network exposure,
multi-tenant, untrusted code, multi-node) has a specific blocking control listed in §6.

---

## 2. Cross-cutting themes

**2.1 — Plaintext everywhere (no TLS anywhere in the codebase).** Grep for `tls.Config`, `ListenAndServeTLS`,
`ServeTLS`, `certmagic`, `autocert` across the whole tree returns zero hits outside ADR prose. The
control-plane API (`pkg/funcd/funcd.go:602`), the data-plane listener (`funcd.go:615`), the S3 gateway
(`internal/blob/s3gateway/`), and the embedded NATS server (`internal/bus/nats/nats.go:62`) all serve plain
HTTP/TCP. `certmagic` is decided in ADR-0013 but never added as a dependency; there is **no TLS knob in the
config schema at all** (`internal/platform/config/config.go` has no `tls`/`cert` field). Deferred by design
(ADR-0013/0033), but it means every credential and payload is on the wire in the clear.

**2.2 — Blueprint over-states the as-built posture (documentation drift).** Several controls the `blueprint.md`
asserts in the present tense are **not implemented**. A reader trusting the blueprint alone would materially
over-estimate security:

| Blueprint claim | Reality |
|---|---|
| "SPIFFE-style … short-lived signed token … audience-bound" (`blueprint.md:481-485`) | No token minting/signing/verification exists; identity is UDS connection-trust (§5.4). |
| "conservative OCI defaults … **default seccomp**" (`blueprint.md:50`, ADR-0011) | No seccomp profile applied; `ociOpts` sets none (§5.8). |
| Internal IAM: "account-scoped credentials … per-namespace NATS accounts" (`blueprint.md:498-504`) | NATS driver sets no accounts/JWT/nkey; single trust domain (§5.6). |
| "optionally verified against signatures (sigstore/cosign) before a worker starts; a pull-through registry cache…" (`blueprint.md:472`) | No signature verification and no pull-through cache exist (§5.9). |

Per the repo's own cross-document propagation rules, these blueprint lines should be marked target-state/V2. This
is a process-hygiene finding, not a runtime vuln — but it is the reason a paper review would miss the real gaps.

**2.3 — The V1 trust model is "one trusted host."** The isolation, identity, and tenancy stories all bottom out
in "the host and everything on it is trusted." That is a coherent V1 stance, but it is **not** a multi-tenant or
network-exposed posture, and the code has few defense-in-depth backstops if that single assumption is violated
(e.g., no peer-cred check behind the socket boundary, no cap-drop behind the namespace boundary).

---

## 3. Updated summary table (severities firmed by the deep dive)

Legend — status: ✅ Implemented · 🟡 Partial · ⛔ Design-only. Severity: `H`/`M`/`L`/`I`, `(gap)` = risk realized once built/exposed. **Bold** = changed from the pre-dive draft.

| # | Domain | Status | Sev | Deep-dive verdict (one line) |
|---|---|---|---|---|
| 1 | API authn | ✅ | M | Constant-time compare ✓; but tokens plaintext-in-RAM, no expiry/rotation/revocation |
| 2 | Control-plane RBAC | ✅ | L | Genuine default-deny, no client-asserted role; `admin` is unscoped all-allow (by design) |
| 3 | Fine-grained authz (Cedar) | ✅ | M | Strong; write rules are permit-then-forbid → **fail open if `owner` unset**; no ns-containment check on policies |
| 4 | Workload identity | 🟡→⛔ | **H** | **UDS connection-trust, zero crypto** — blueprint's signed tokens are not built |
| 5 | Secrets at rest | 🟡 | M | AES-256-GCM correct; **key file loaded with no permission check (H quick-fix)**; no rotation/envelope; no AAD |
| 6 | Secret delivery | ✅ | M | PDP-gated env-var injection; coarse (namespace-level) authz → intra-ns over-delivery; env not tmpfs |
| 7 | Persistence at rest | ✅ | M | Only `Secret` encrypted; **rest of metastore/KV is plaintext JSON on disk** |
| 8 | Blob / S3 + SigV4 | ✅ | M | SigV4 via versitygw (replay-window ✓); non-constant-time sig compare (L); plaintext listener; master-key global-rotation-only |
| 9 | Messaging / NATS | 🟡 | **H** | **No auth, no TLS, real TCP port, full JetStream admin exposed**; accounts unimplemented |
| 10 | Ingress + TLS | 🟡 | **H** | httputil routing ✓; **plaintext**; **data-plane invocation unauthenticated on 0.0.0.0** |
| 11 | Fn-to-fn RPC (links) | ✅ | **H** | Cedar `link::invoke` ✓ but authenticated only by socket bind-mount; **process-mode = full impersonation** |
| 12 | Network / egress | ⛔ | H (gap) | Not built — no `internal/network`; unrestricted egress + lateral for a compromised function |
| 13 | Internal mTLS | ⛔ | H (gap) | Not built — no cross-node transport security; hard blocker for multi-node |
| 14 | Runtime isolation | 🟡 | **H** | **No seccomp, no cap-drop, no PID/CPU/mem limits for functions**; crun+netns baseline only |
| 15 | Artifact trust | 🟡 | M | Digest pinning ✓ (tamper-evident); **no signature/provenance gate**; no registry allowlist |
| 16 | Observability / audit | **🟡** | **M** | Audit event type is leak-safe (refs not payloads) but the recorder is **never wired — zero runtime audit coverage** (§5.12) |

---

## 4. What is done well (keep and build on)

- **Single PDP, PEPs at every hop, real default-deny.** `internal/auth/rbac/rbac.go` denies unknown roles
  (`:49-51`); the Cedar driver fails closed on nil principal/resource/unknown action *before* evaluation
  (`internal/auth/cedar/cedar.go:58-70`) and cedar-go itself denies with no matching permit. The PEP fails
  closed if no identity is on the context (`internal/controlplane/handlers.go:35-38`).
- **Principals are server-derived, never client-asserted.** The Cedar principal comes from the fixed
  connection `Ref` (`internal/workernode/local/local.go:72-77`); KV `ns`/`fn` come from `caller.*`
  (`internal/workernode/local/kv.go:32-33`), never the request body. No `X-Role`/impersonation header path
  exists. This eliminates a whole class of privilege-escalation bugs.
- **Constant-time credential compare.** `internal/controlplane/middleware/authn.go:105` uses
  `subtle.ConstantTimeCompare`, looped over every configured token so a miss leaks no timing.
- **Correct AES-256-GCM.** `internal/secrets/aesgcm/aesgcm.go`: 32-byte key length enforced (`:29`), 12-byte
  `crypto/rand` nonce fresh per call (`:45-46`), `Seal`/`Open` used correctly, tamper fails (`:61-64`),
  short-input guarded against slice panic (`:57`). No hand-rolled crypto.
- **S3 authorization is layered and tested end-to-end with a real AWS SDK client.** Every backend method is a
  PEP (`internal/blob/s3gateway/backend.go:61-94`); tenant isolation is double-guarded (Cedar resource scoping
  **and** physical key-prefixing `s3/<ns>/<bucket>/`); cross-namespace access returns 403.
- **Per-tenant S3 secrets are HMAC-derived, non-forgeable.** `SecretKey = base64(HMAC-SHA256(master, "s3:"+ns+"/"+fn))`
  (`internal/blob/s3gateway/iam.go:49-55`); a function holding its own secret cannot recover the master or a
  peer's key (proven by test).
- **Digest-is-authority artifact model.** Tag→digest resolved once at Revision stamp and pinned immutably
  (ADR-0035); pull rejects empty digest and re-verifies bytes (`internal/artifact/artifact.go:155-169`). Closes
  the "mutable tag swaps running code" class.
- **Secrets never leak to logs/status/audit.** Failure messages carry secret *names* only; the audit event
  *type* records a namespaced `Resource` ref, not payloads (`internal/platform/observability/audit.go:38`) —
  though note the recorder itself is not yet wired into any code path (§5.12).

---

## 5. Detailed findings

### 5.1 API authentication — `internal/controlplane/middleware/authn.go` — **M**

Opaque bearer-token / API-key lookup against an in-memory map built from config; constant-time compare; public
allowlist for `/openapi*`, `/docs*`, `/schemas/*`; fail-closed on missing credential store
(`pkg/funcd/funcd.go:175-176`). `Production()` ships no default token; `InMemory()` hardcodes
`DevToken = "funcd-dev-token"`.

- **[M] Tokens held plaintext in process memory** — the map key *is* the raw bearer value (`authn.go:89-93`); no
  hashing (`grep bcrypt|sha256|argon` in the auth tree → empty). A core dump / `/proc` read / heap scrape yields
  live credentials.
- **[M] No expiry, rotation, or revocation** (`grep expir|rotat|revoke|ttl` → empty; `auth.Identity` has no
  expiry field). A leaked token is valid until the operator rebuilds the map and restarts.
- **[L] Hardcoded `DevToken`** is a guessable credential anywhere the `InMemory` preset is reachable.
- **Recos:** store `SHA-256`/HMAC of the token as the map key (keep the constant-time compare); add
  `NotAfter` + a revocation deny-list + hot-reload of the credential map; refuse `DevToken` on a non-loopback bind.

### 5.2 Control-plane RBAC — `internal/auth/rbac/rbac.go` — **L**

Total, default-deny switch on `Identity.Role`: `admin` allow-all; `developer` denied cluster-scope and denied
outside its `Namespaces`; `viewer` adds a write-verb deny; `default:` denies unknown role. Verb/Kind/Namespace
derive from the route, not the body.

- **[L] `admin` is unconditional allow-all** with no scoping and no second factor (`:27-29`); combined with §5.1
  (plaintext, non-expiring tokens), one leaked admin token is total cluster compromise.
- **[L] Only three compile-time roles** — no custom least-privilege role. Acceptable for V1.
- **Recos:** at minimum audit/alert on every `admin` decision; consider namespace-scoped admin for V2.

### 5.3 Fine-grained authorization (Cedar) — `internal/auth/cedar/` — **M**

`Policy` resources validated at admission before persistence (`internal/controlplane/admission/policy.go:29-38`):
must parse, be non-empty, name a specific action (an "All" scope is rejected), and reference only curated
actions/entity types. Built-ins + user policies compile into one cached `PolicySet`, recompiled on change.
Built-in permits are capability-scoped by the principal's *own declared bindings* (`spec.kv`→`kv::read`,
`spec.links`→`link::invoke`, `spec.blob`→`s3::read`).

- **[M] Write rules are permit-all-then-forbid; they fail *open* if `owner` is unset.** `builtin_kv.cedar` /
  `builtin_s3.cedar` grant `permit(write)` broadly and rely on `forbid(write) unless resource has owner &&
  principal == resource.owner` to claw it back. If `resource.owner` is ever materialized empty when it should be
  set (`internal/auth/cedar/entities.go:249-254`/`:302-307`), the `unless resource has owner` guard makes the
  forbid **not fire**, leaving the base permit in effect → any principal writes. The risky direction (missing
  attribute) is the fail-open one.
- **[L] No namespace-containment check on policy entity references** — `ValidateCedar` checks entity *type*, not
  that a Policy in namespace A only references namespace-A entities (`schema.go:114-118`); a latent
  cross-tenant-grant surface.
- **[L] One corrupt stored Policy fails the whole `PolicySet`** (`policies.go:90-91`) → DoS of all per-object
  authz if the "admission already validated it" invariant is ever violated.
- **Recos:** add a companion `forbid(write) unless resource has owner`, or require an owner at admission for any
  writable table/prefix; enforce namespace containment in `ValidateCedar`; skip-and-log a bad stored policy
  instead of failing the set.

### 5.4 Workload identity & fn-to-fn RPC — `internal/workernode/local/`, `shim/*` — **H**

**Definitive finding: internal identity rests entirely on UNIX-domain-socket connection trust. There is no
signed token, no SPIFFE SVID, no JWT, no cryptographic verification on the data plane.** The blueprint's
minted-signed-token model (`blueprint.md:481-485`) is aspirational. Grep across the tree for
`spiffe|jwt|jose|ed25519|MintToken|audience` finds only blueprint prose and transitive `go.sum` entries (pulled
by NATS, never imported). The only data-plane crypto is unrelated (S3 HMAC, secret AES-GCM).

How it works: one listener per sandbox; the caller `Ref` (namespace+function) is baked into the handler closure
at provisioning (`internal/workernode/local/local.go:68-77`); the function's own socket is bind-mounted to a
fixed in-container path (`internal/function/function.go:159`, `:843-848`). The request body/headers can never
name a different caller — a real strength *within* the socket surface. But:

- **[H] Identity == mount-namespace isolation, with no cryptographic backstop.** The entire internal IAM boundary
  is the OCI bind-mount. There is **no `SO_PEERCRED`/peer-credential check and no `chmod`** on the socket
  (`internal/workernode/local/manager.go:70,75` — dir is `0o700`, but `net.Listen("unix",…)` uses default umask
  and verifies no connecting peer). If a sandbox can reach another function's host socket (container escape,
  shared/misconfigured mount, host-side process), it *becomes* that principal for KV and invoke — no token would
  reject it.
- **[H] Process-mode (dev/e2e driver, ADR-0011) has no mount-namespace isolation at all.** Sockets sit on a
  shared host path (`function.go:774-783`); all functions run as the same daemon uid. Any function (or local
  process) can dial any `<ns>-<name>.sock` and fully impersonate it. Only the `0o700` *directory* perm stands
  between them, and it does not separate functions from each other.
- **[M] No audience binding, expiry, or replay protection** — a connection is ambient and unbounded; the "a kv
  token is useless against blob/another fn" property (`blueprint.md:483`) is unrealized.
- **Recos:** (1) immediate, cheap — `chmod 0o600` the socket + verify `SO_PEERCRED` on each accept; (2) the real
  fix — mint a short-lived, audience-bound, signed credential (Ed25519 JWS / SPIFFE JWT-SVID; the libs are
  already transitive) at sandbox creation, presented by the shim on every call and verified in `NewHandler`,
  **in addition to** the connection check; (3) gate process-mode to trusted single-tenant dev only.

### 5.5 Secrets — encryption at rest & key management — `internal/secrets/`, `cmd/funcd/main.go` — **M** (one **H** quick-fix)

AES-256-GCM is correct (§4). The gaps are in key management and scope.

- **[M, H-priority fix] Encryption key loaded from a plaintext file with no permission check.** `grep 0600|0400|FileMode|Perm()` in the key-handling packages → **zero hits**. funcd will load a world-readable key file
  (`cmd/funcd/main.go:374`). The master key is the whole boundary and funcd verifies nothing about its
  protection. **Cheapest high-value fix in the report:** `os.Stat` the key file and refuse to start if
  `mode&0o077 != 0`.
- **[M] Single static key, no rotation, no envelope encryption.** One key encrypts every secret forever; rotating
  requires manual decrypt-all/re-encrypt-all. Key compromise is catastrophic and unrecoverable in place.
- **[L] No associated data (AAD).** `Seal(..., nil)` (`aesgcm.go:50`) does not bind ciphertext to its
  `(kind,namespace,name)` key, so an attacker with raw-store write access could relocate one secret's ciphertext
  to another key and it decrypts cleanly.
- **Recos:** enforce key-file perms; introduce a KEK/DEK envelope (random per-secret DEK wrapped by the master)
  to make rotation and compromise-recovery tractable; pass the store key as AAD (behind a format-version byte).

### 5.6 Secret delivery & persistence at rest — **M**

- **[M] Coarse secret authorization → intra-namespace over-delivery.** Delivery is PDP-gated with a single
  `VerbGet`/`KindSecret`/namespace decision (`internal/secrets/secrets.go:51`); RBAC allows any namespace-scoped
  developer all verbs on all namespaced kinds. There is **no per-secret-name binding check** — a function author
  can name any secret in its namespace and receive it. Secrets are delivered as **env vars**, not tmpfs (visible
  in `/proc/<pid>/environ`, inherited by children).
- **[M] Only `Secret` is encrypted at rest; the rest of the metastore/KV is plaintext JSON on disk.**
  `WithEncryptor([]v1.Kind{v1.KindSecret}, enc)` is the sole call site (`cmd/funcd/main.go:352`); Badger's own
  `WithEncryptionKey` is never set. ConfigMaps (often semi-sensitive: endpoints, non-secret tokens), Function
  specs, and bindings are readable from the data dir with a hex editor.
- **Recos:** move toward per-secret/per-function binding authz (the V2 `Grant` model); prefer tmpfs (0400)
  delivery; add ConfigMaps to the encrypted set or enable Badger block-encryption for defense-in-depth against
  disk theft.

### 5.7 S3 / SigV4 frontend — `internal/blob/s3gateway/` — **M**

SigV4 is delegated to `versity/versitygw v1.6.0` (not hand-rolled — a good choice). Replay/clock-skew is bounded
to ±15 min; canonical-request handling is library-standard; the master key is stored `0600`, node-local, never
logged. Authorization maps cleanly to Cedar `s3::read`/`s3::write` with double-guarded tenant isolation. No
anonymous/public-bucket path is reachable (funcd passes `WithDisableACL()` and overrides ACL/policy getters).

- **[M] Plaintext listener** (`http://…`, default `127.0.0.1:9000`) — fine for the loopback/netns case, but
  ADR-0080's "external client over a tunnel" scenario would put a SigV4 secret + object bytes on the wire in the
  clear if the tunnel isn't already encrypting. No `WithTLS` knob is wired.
- **[M] Master key rotation/revocation is global-only** — no way to revoke one compromised function's derived key
  without rotating the node master (invalidates everyone).
- **[M, unconfirmed] Presigned-URL expiry cap** — the server-side `X-Amz-Expires` ceiling in versitygw v1.6.0
  was not confirmed; if uncapped, a keypair holder could mint a very long-lived URL. Verify against the pinned
  version.
- **[L] Non-constant-time signature string compare** inside versitygw (`!=`), and `HeadBucket` skips the
  action-level PEP (existence oracle only). Both low impact.
- **Recos:** wire `s3api.WithTLS` behind a config flag + a startup guard refusing a non-loopback bind without
  TLS; add a `(ns,fn)` revocation check in `iam.GetUserAccount`; confirm/cap presign TTL; gate `HeadBucket` on
  `s3::read`.

### 5.8 Runtime isolation — `internal/runtime/containerd/containerd_linux.go` — **H**

The containerd/crun driver's `ociOpts` (`:555-577`) applies exactly one hardening primitive —
`oci.WithNoNewPrivileges` — plus namespace isolation (own PID/IPC/UTS/net; no host-namespace-sharing code path;
not privileged). Verified first-hand: grep for `seccomp|WithCapabilities|WithPidsLimit|ReadonlyRootfs|WithUserNamespace`
across `internal/` returns **nothing**.

- **[H] No seccomp profile.** ADR-0011/blueprint claim "default seccomp," but containerd's Go client applies none
  unless explicitly requested; `Linux.Seccomp` is left nil → the process is **syscall-unconfined**. For a
  platform whose threat model is arbitrary user code, this is the single highest-priority code fix.
- **[H] No CPU/memory/PID limits for functions.** The cgroup plumbing exists (`WithMemoryLimit`/`WithCPUCFS`,
  `:569-575`) but is only ever populated for `CatalogService` (`internal/provider/runtime.go`), never for
  `Function` — the three `WorkerSpec{}` sites in `internal/function/function.go` set no `Limits`, and the
  `Function` spec has no `resources` field to read. Every function runs with **unlimited memory/CPU** and no
  `pids.max` — a one-line memory loop or fork bomb can OOM/starve the host. No exploit required.
- **[M] Capabilities not dropped** — the container keeps the OCI default set (~a dozen caps incl.
  `SETUID`/`SETGID`/`SYS_CHROOT`), not the empty set most FaaS platforms use. "No added caps" (ADR wording) ≠
  "all dropped."
- **[M] Writable rootfs, no size-capped tmpfs for `/tmp`** — disk-exhaustion vector layered on the memory one.
- **[M] Non-root is by base-image accident, not enforced** — the distroless bases run non-root, but a custom
  `--image runtime=<ref>` override (ADR-0054) that declares `USER root` would run as uid 0 with no gate.
- **[H if ever selected in prod] Process driver provides zero isolation** — plain `exec.Command` sharing all
  namespaces, filesystem, and privilege of the daemon. Self-documented as dev-only; recommend a code-level guard
  refusing it outside a dev build.
- **Recos:** apply `seccomp.WithDefaultProfile()`; add a `Function.spec.resources` field and populate
  `WorkerSpec.Limits` at the three sites; drop-all-then-add-back capabilities; `WithPidsLimit`;
  `WithRootFSReadonly` + sized tmpfs; enforce non-root independent of the base image.

### 5.9 Supply chain / artifact trust — `internal/artifact/` — **M (→H under untrusted/multi-tenant)**

Digest pinning is real and sound (§4) — integrity and tamper-evidence are covered. The gap is **authenticity**.

- **[H under the untrusted-code model] No signature/provenance verification before run.** Grep for
  `cosign|sigstore|in-toto|attestation|slsa|notation` → zero implementation hits (all deferred to V2 per
  ADR-0031/0020). `blueprint.md:472` claims signatures are "optionally verified … before a worker starts" — not
  implemented. Anyone who can push to the configured OCI target and get their ref referenced by
  `Function.spec.image` gets arbitrary code executed; there is no cryptographic gate on the pusher's identity.
- **[M] No registry allowlist / trust policy and no pull-through cache** (the latter also claimed in the
  blueprint). Any resolvable ref or `oci-layout://` path is accepted.
- **Recos:** add a signature/provenance gate (cosign keyless or notation) gated by a per-namespace policy, or at
  minimum a registry allowlist as an interim control, before running untrusted or multi-tenant workloads.

### 5.10 Transport, ingress & message bus — **H**

- **[H] Data-plane function invocation is unauthenticated.** The chain is
  `gateway.Chain(dataplane.Handler(...), Recover, RequestID)` (`pkg/funcd/funcd.go:613`) — no auth middleware
  exists in `internal/gateway/`. The listener binds `0.0.0.0:8081` (in-code comment: "intentionally public (auth
  is V2)"). Namespace is a self-asserted `X-Funcd-Namespace` header (`internal/dataplane/dataplane.go:51-54`) —
  any client that can reach the port invokes any function in any namespace.
- **[H] No TLS on control-plane or data-plane** (§2.1) — bearer tokens and payloads (incl. injected secrets) in
  cleartext.
- **[H] Embedded NATS: no auth, no TLS, real TCP port.** `natsserver.Options{}` sets `Port:-1` (random TCP,
  library default bind is all-interfaces), `JetStream:true`, and **no** `TLSConfig`/`Users`/`Accounts`/nkey/JWT
  (`internal/bus/nats/nats.go:62-69`). Any host that can route to the port gets full unauthenticated
  read/write/**JetStream-admin** over the control-plane backbone (controller work queue, eventing, KV/service
  bindings). Account-per-namespace (`blueprint.md:498-504`) is unimplemented — single trust domain. (Functions
  never get a NATS connection, so the exposure is network-adjacency, not function-level.)
- **[M] Activator cold-start buffering is unbounded** — no cap on concurrent in-flight buffered requests and no
  `MaxBytesReader` on the body (`internal/activator/activator.go`); each waiter holds a goroutine + full body for
  up to `ActivationTimeout` (30s). ADR-0033 names this as an unaddressed hardening follow-up. Memory-exhaustion
  DoS.
- **[M] No rate limiting anywhere; only `ReadHeaderTimeout` is set** (no `ReadTimeout`/`WriteTimeout`/
  `IdleTimeout`) — residual slow-body Slowloris-family exposure; no backpressure against floods or
  credential-stuffing.
- **Recos:** bind all internal listeners (NATS especially) to loopback/UDS immediately; add a data-plane auth PEP
  (even a shared invocation token / mTLS client cert) and stop trusting `X-Funcd-Namespace`; wire TLS (certmagic
  or operator cert/key); add NATS TLS+auth before any networked control plane; bound the activator buffer +
  `MaxBytesReader`; add `x/time/rate` + the remaining server timeouts.

### 5.11 Network / egress control & internal mTLS — **H (gap)** each

Both are **design-only** — correctly deferred, but each blocks a specific deployment mode.

- **Egress/network manager not built** — no `internal/network` package (verified). The blueprint's nftables
  default-deny + transparent egress proxy + SNI/DNS-aware policy do not exist. A compromised function today has
  unrestricted outbound network and unrestricted lateral reach on the host network namespace. **Prerequisite
  before running untrusted code.**
- **Internal mTLS not built** — no cross-node RPC transport security (no `.proto` files present; the only `tls`
  references are the OTLP exporter's dev `WithInsecure()` path and an unwired `Gateway` TLS field).
  **Prerequisite before any multi-node deployment** — connection-trust identity (§5.4) provides zero security
  once it crosses a network.

### 5.12 Observability & audit — `internal/platform/observability/` — **M**

The audit *design* is sound: a typed `AuditEvent` (`Actor`/`Action`/`Resource`/`Decision`/`Reason`,
`audit.go:35-41`), decision validated to `allow|deny`, JSON output on a dedicated `source=audit` stream kept
separate from operational logs, refs-not-payloads by construction. It is unit-tested. But:

- **[M] The recorder is never wired — funcd records zero audit events at runtime.** Grep for
  `NewAuditRecorder|AuditEvent{` outside `internal/platform/observability/` (and its tests) returns **no
  hits**: the composition root (`cmd/funcd/main.go`, `pkg/funcd/funcd.go`) never constructs it, and no
  PEP/PDP call site emits an event. Every authn failure and authz allow/deny today surfaces only as an
  RFC-7807 problem response (`internal/controlplane/handlers.go:44`) plus operational logs. There is **no
  security audit trail at all** — the §5.2 recommendation to "audit every `admin` decision" currently has no
  substrate, and a post-incident investigation would have nothing to replay.
- **[L] The sink is a plain `io.Writer` JSON stream** — no durable retention, no append-only/tamper-evidence
  guarantee; self-documented as a V2 audit ADR (`audit.go:43-45`). Fine once wired, provided the operator
  points it at protected storage.
- **[L] OTel exporter's dev path uses `WithInsecure()`** (see §5.11) — telemetry, not audit, but the same
  plaintext-transport theme; funclog capture (ADR-0081) stores function output in the blob substrate under the
  same at-rest posture as §5.6.
- **Recos:** construct the `AuditRecorder` in the composition root and emit at the three PEP layers — authn
  middleware (success + failure), the control-plane route PEP (RBAC decision), and the Cedar PDP call sites
  (KV / `link::invoke` / S3), allow **and** deny; keep the refs-only discipline; add durable, append-only sink
  + retention as the planned V2 audit ADR.

---

## 6. Prioritized remediation roadmap

Organized by **deployment gate** — the mode you cannot safely enter until the listed controls exist. Within each
gate, ordered by effort-adjusted impact.

### Gate 0 — Immediate hardening (cheap, do regardless of deployment mode)
1. **Enforce encryption-key-file permissions** — refuse start if group/other-readable (`cmd/funcd/main.go:370`). *(§5.5, trivial)*
2. **`chmod 0o600` the per-sandbox sockets + verify `SO_PEERCRED` on accept** (`internal/workernode/local/manager.go`). *(§5.4, small)*
3. **Apply a seccomp profile** (`seccomp.WithDefaultProfile()`) and **drop capabilities** in `ociOpts`. *(§5.8, small)*
4. **Add `WithPidsLimit`** and a **sized tmpfs**; make rootfs read-only. *(§5.8)*
5. **Hash bearer tokens in memory**; refuse `DevToken` on a non-loopback bind. *(§5.1)*
6. **Guard the Cedar write built-ins** against a missing `owner`. *(§5.3)*
7. **Wire the `AuditRecorder`** into the authn middleware + RBAC/Cedar PDP call sites (allow **and** deny) — the type exists, unused. *(§5.12, small)*

### Gate 1 — Before binding anything beyond loopback (network exposure)
8. **Wire TLS** on control-plane + data-plane (certmagic per ADR-0013, or operator cert/key). *(§5.10)*
9. **Add a data-plane auth PEP**; stop trusting `X-Funcd-Namespace`. *(§5.10)*
10. **Bind NATS to loopback/UDS and add TLS+auth**; do not expose JetStream admin. *(§5.10)*
11. **Wire S3-gateway TLS** + startup guard on non-loopback bind. *(§5.7)*
12. **Bound the activator buffer** + `MaxBytesReader`; add rate-limiting + server timeouts. *(§5.10)*

### Gate 2 — Before multi-tenant (mutually untrusting namespaces on one host)
13. **Per-secret / per-function delivery authz** (V2 `Grant` model), not namespace-coarse. *(§5.6)*
14. **Encrypt the whole metastore** (add kinds or Badger block-encryption). *(§5.6)*
15. **Cedar policy namespace-containment** at admission; isolate a corrupt policy. *(§5.3)*
16. **Per-tenant S3 key revocation** without global rotation. *(§5.7)*
17. **Token expiry + rotation + revocation** for control-plane credentials. *(§5.1)*
18. **Durable, append-only audit sink** with retention (the V2 audit ADR). *(§5.12)*

### Gate 3 — Before running untrusted function code
19. **Build the egress/network manager** (nftables default-deny + egress proxy, fail-closed). *(§5.11)*
20. **Artifact signature/provenance verification** + registry allowlist. *(§5.9)*
21. **Signed, audience-bound workload identity** (Ed25519/SPIFFE) layered over the socket check. *(§5.4)*
22. **Enforce non-root + per-function resource limits**; consider WASM/Kata for untrusted tiers. *(§5.8)*

### Gate 4 — Before multi-node
23. **Internal mTLS (SPIFFE X.509-SVID)** on all cross-node RPC — mandatory, no connection-trust over a network. *(§5.11)*
24. **Envelope encryption / KMS** for keys that now cross a trust boundary. *(§5.5)*

### Process hygiene (parallel)
25. **Sync the blueprint** to reflect V2 deferrals (signed tokens, seccomp, NATS accounts, cosign, pull-through cache) per the repo's own propagation rules. *(§2.2)*

---

## 7. Method, scope & caveats

- **Evidence base:** direct read of `internal/auth/*`, `internal/secrets/*`, `internal/controlplane/*`,
  `internal/blob/s3gateway/*`, `internal/runtime/*`, `internal/bus/nats/*`, `internal/gateway/*`,
  `internal/activator/*`, `internal/workernode/local/*`, `internal/artifact/*`,
  `internal/platform/observability/*`, `shim/*`, the dependency
  manifest, and ADRs 0008/0011/0013/0018/0022/0031/0032/0033/0035/0041/0057/0064/0065/0074-76/0080/0085/0088.
- **Absence claims** (TLS/certmagic, `internal/network`, seccomp/caps/pids, mTLS, cosign/sigstore, NATS
  accounts) were each verified by dependency + source grep returning no matches.
- **Not covered here (candidates for a follow-up):** the wasm runtime driver's isolation specifics; the eventing
  sensor/trigger expression-evaluation surface; the workflow engine's step-dispatch trust; DoS modeling of the
  Badger stores; a dependency-CVE audit of the third-party trust boundaries (notably versitygw, cedar-go,
  nats-server, badger).
- **Severity reflects risk if exposed as-is.** Most gaps are deferred by design for a single-node V1; the report
  firms *which* deferrals gate *which* deployment mode. It is not a claim of oversight by the authors — the
  ADRs are unusually honest about what is and isn't built. The one consistent exception is the **blueprint**,
  which reads as if several unbuilt controls are present.
