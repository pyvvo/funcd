# ADR-0022: Secrets service — at-rest encryption + delivery resolver (`internal/secrets`)

- **Status**: Implemented
- **Date**: 2026-06-14 (**Implemented 2026-06-14** — review pass (zero findings), see docs/reviews/adr-0022-implementation-claude-opus-4-8.md; DoD 5/5, 5 scenarios. **Reviewing 2026-06-14** — implemented: internal/secrets/aesgcm (AES-256-GCM store.Encryptor) + internal/secrets (PDP-authorized Resolver) + SecretTypeOpaque; 5 scenarios pass, OpenAPI regen, four sub-checks green, no new deps. **Accepted 2026-06-14** after judge pass — no Blockers. Folded the judge's **Major**:
  named the secret-injection last-mile's owner honestly — a **P-M-superseding ADR** (the frozen P-M doesn't
  inject secrets; no worker is built), recorded as a V1 follow-up, so this ADR ships **at-rest encryption + the
  delivery `Resolver`** but does **not** by itself complete "reads a secret" end-to-end (P-S *proves* it, doesn't
  implement it). Minor: clarified the V1 key is operator-provided via config (key management → V2 KMS).
  Decision: stdlib AES-256-GCM `store.Encryptor` (fills the ADR-0006 seam) + a PDP-authorized env-var Resolver;
  tink/OpenBAO/S3-envelope + Grant + rotation deferred behind the seam. No new deps. Secrets is a distinct
  resource+encryptor+resolver — **not** a Service-dispatcher TypeHandler (so the roadmap drops the P-N/ADR-0011
  edges).)
- **Deciders**: green-0-rabbit
- **Tags**: secrets, encryption, at-rest, aes-gcm, delivery, security, service, data-plane
- **Realizes**: [FEAT-0000/F15](../feat/0000-feat-v1.md) (secrets service: resource encrypted at rest, delivered to sandboxes env/tmpfs; drivers memory + S3-backed-encryption; OpenBAO deferred to V2)
- **Relates to**: [ADR-0006](0006-store-database-layer-port.md) (the store's **`Encryptor` seam** + `WithEncryptor`
  — this ADR supplies the concrete encryptor for `KindSecret`, the seam ADR-0006 left for F15),
  [ADR-0018](0018-api-server-authn-rbac-admission.md) (the `auth.Authorizer` PDP the delivery resolver calls —
  the secrets PEP), [ADR-0020](0020-function-contract-lifecycle.md) (the function whose bound secrets are
  resolved into env vars for the sandbox — the worker injects them), [ADR-0003](0003-resource-model-and-api-typing.md)
  (the `Secret` kind — `SecretSpec.Data` already exists; this ADR adds its `Opaque` type),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (conventions), [blueprint.md — Secrets management / Security model](../../blueprint.md).
  **New deps: none** (the V1 in-memory encryptor is stdlib `crypto/aes`+`crypto/cipher` AES-256-GCM; tink/OpenBAO/S3-envelope are the deferred production drivers).

## Context & Need

The blueprint: "**Secrets management**: securely store and deliver sensitive values, **encrypted at rest**,
delivered to sandboxes via **env/tmpfs**. Drivers: in-memory (dev), S3-backed + envelope encryption, external
(OpenBAO)." And: "**Secrets at rest**: encrypted in the metastore … delivered to sandboxes via env vars or
tmpfs mounts." The V1 exit criterion needs a function to "**read a secret**." Two pieces are missing: (1)
nothing actually **encrypts** `Secret` resources at rest — ADR-0006 shipped the `store.Encryptor` *seam* and
`WithEncryptor(kinds, enc)` but **no encryptor**, leaving the comment "P-P/F15 supplies the implementation";
(2) nothing **delivers** a function's secrets to its sandbox.

**Purpose**: ship `internal/secrets` — (1) a concrete **`store.Encryptor`** (V1: **AES-256-GCM** with a
configured key, stdlib-only) wired for `KindSecret` so every stored Secret is ciphertext at rest; (2) a
**delivery `Resolver`** that reads a function's bound `Secret` resources (auto-decrypted by the store) and
returns the **env-var map** to inject — **PDP-authorized** (the secrets PEP). Callers: the composition root
wires the encryptor into the store and the resolver into the worker's sandbox-boot path; a function reads its
secret from the injected env. Conformance is mechanical: a `Secret` round-trips through an encryptor-wired
store with **ciphertext on disk, plaintext on read**; the resolver returns a bound secret's values as env vars
for an authorized identity and denies an unauthorized one.

## Scenarios

- `scenario: encryptor-roundtrips` — **Given** the AES-GCM encryptor, **when** a plaintext is `Encrypt`-ed then
  `Decrypt`-ed, **then** the original bytes return; the ciphertext differs from the plaintext and is unique per
  call (random nonce); `Decrypt` of tampered ciphertext fails.
- `scenario: secret-stored-encrypted-at-rest` — **Given** a store wired `WithEncryptor([KindSecret], enc)`,
  **when** a `Secret` with data is created then read back, **then** the read returns the plaintext data, but the
  raw stored value (decrypted-bypass) is **not** the plaintext (ciphertext at rest).
- `scenario: resolver-returns-bound-secret-env` — **Given** a `Secret{data: {API_KEY: "s3cr3t"}}` and an
  authorized identity, **when** the resolver resolves that secret in its namespace, **then** it returns
  `{API_KEY: "s3cr3t"}` as env vars (decrypted via the store).
- `scenario: resolver-authorizes` — **Given** an identity not permitted in the namespace, **when** it resolves a
  secret there, **then** it is denied (`fault.Forbidden`) — the resolver is a PEP.
- `scenario: encryptor-rejects-bad-key` — **Given** `NewAESEncryptor` with a key that is not 32 bytes, **when**
  it is constructed, **then** it fails with `fault.Invalid` (a misconfigured key must not silently weaken crypto).

## Scope

**In**:
- **`internal/secrets/aesgcm`**: the V1 **AES-256-GCM `store.Encryptor`** (`NewAESEncryptor(key []byte)`,
  random 96-bit nonce per encrypt, prepended to the ciphertext); stdlib-only.
- **`internal/secrets`**: the **`Resolver`** — `ResolveEnv(ctx, Identity, namespace, secretNames)
  (map[string]string, error)`: PDP-authorize (read), read each `Secret` from the store (store-decrypted),
  flatten `Data` to env vars.
- **`Secret` type**: add `SecretTypeOpaque` (the default generic secret) — F15-owned (`SecretSpec.Data` already exists).

**Out (deferred — with a NAMED owner, not hand-waved)**:
- **The actual sandbox env/tmpfs injection** — populating `SandboxSpec.Env` (+ tmpfs mounts) from the
  `Resolver` at sandbox boot. **P-M (ADR-0020) is frozen and its `sandboxSpec` does not inject secrets**, so this
  needs a **superseding function-lifecycle ADR** (a P-M-successor that calls the `Resolver` when building the
  `SandboxSpec`). **Honest exit-criterion status:** V1-as-this-ADR ships **at-rest encryption + the delivery
  contract (the `Resolver`)** — it does **not** by itself complete the exit-criterion clause "**reads a
  secret**" end-to-end; that completes only when the P-M-successor wires the resolver→`SandboxSpec.Env`
  injection (a follow-up item the Step-6 roadmap reconcile records as the remaining F15 work; P-S then *proves*
  it e2e, but P-S is the test, not the injection). This ADR deliberately does not edit the frozen P-M.
- **S3-backed envelope encryption + OpenBAO + tink-go KMS drivers** — V1 is the in-memory AES-GCM driver with a
  configured key; envelope/KMS/external are the production drivers behind the same `store.Encryptor` seam (V2).
- **Key rotation / per-namespace keys** — V1 uses one configured platform key; rotation is a follow-up.
- **Workload-identity `Grant` authz** — V2 (same as KV/blob); V1 resolver authz uses the caller's `Identity`.

## Constraints & Decision drivers

- **C1 — encrypted at rest is non-negotiable (blueprint security model)**: a stored `Secret` must be ciphertext;
  V1 wires a real AEAD (AES-256-GCM) into the store's `Encryptor` seam for `KindSecret`.
- **C2 — the delivery resolver is a PEP**: resolving a secret calls the `auth.Authorizer` PDP; default-deny.
- **C3 — don't hand-roll crypto**: use the stdlib AEAD (`crypto/cipher.NewGCM`), random nonce per message — the
  standard primitive, not a bespoke scheme.
- **C4 — ADR-0002 conventions**: one-file driver (`aesgcm`); `New`-style constructors; ctx-first; `api/fault`;
  typed; no globals; no `any`; no mocks (real encryptor + real store + real PDP).

## Alternatives considered

**At-rest encryptor for V1**:
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **stdlib AES-256-GCM `store.Encryptor`, configured key** | real AEAD, zero new dep, fills the ADR-0006 seam; envelope/KMS swap in behind the seam | one platform key (no per-ns / rotation yet) | **chosen** (V1 in-memory driver) |
| **tink-go now** | the blueprint's crypto-service lib; key management, envelope | a new dep for V1's single-key need; the blueprint makes envelope/KMS the *production* driver | rejected (premature; V2 envelope driver) |
| **No at-rest encryption (store plaintext)** | less code | violates the blueprint's "encrypted at rest" — a security-model breach | rejected (security) |

**Delivery for V1**: a **`Resolver`** producing the env map (the worker injects) — chosen; the env/tmpfs
*injection* is deferred to the worker (P-M frozen), but the resolver is the testable contract. A function-facing
runtime facade (KV-style) was considered but the blueprint specifies env/tmpfs boot delivery — the resolver
serves that model.

## Decision

### 1. The AES-256-GCM encryptor (`internal/secrets/aesgcm`)
`NewAESEncryptor(key []byte) (store.Encryptor, error)` — requires a **32-byte** key (`fault.Invalid` otherwise).
`Encrypt`: a fresh random 12-byte nonce, `gcm.Seal`, return `nonce||ciphertext`. `Decrypt`: split the nonce,
`gcm.Open` (fails on tamper). The composition root builds the store `WithEncryptor([]Kind{KindSecret}, enc)` so
every `Secret` value is ciphertext at rest, transparently decrypted on read (ADR-0006). **The V1 key is
operator-provided via config** (a 32-byte key, like P-L's credential store) — *key management* (generation,
storage at rest, rotation, per-namespace keys) is the deferred envelope/KMS/OpenBAO concern, not V1's.

### 2. The delivery resolver (`internal/secrets`)
`Resolver`, built with `NewResolver(Deps{Store, Authorizer, Logger})`. `ResolveEnv(ctx, id, ns, names)`:
1. **authorize** — `Authorizer.Authorize(ctx, {id, VerbGet, KindSecret, ns})`; deny → `fault.Forbidden`.
2. **read + flatten** — for each name, `store.Get(KindSecret, ns, name)` (the store decrypts); merge each
   `Secret.Spec.Data` (`map[string][]byte`) into a `map[string]string` env map (`NAME=value`).
The worker injects the returned map into the sandbox env (+ tmpfs); that wiring is the worker's (deferred).

### 3. The `Secret` `Opaque` type (F15-owned)
```go
const SecretTypeOpaque SecretType = "Opaque" // the default generic key/value secret
```

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **Sandbox env/tmpfs injection deferred to the worker** | P-M (`SandboxSpec` build) is frozen; the worker owns boot injection | the worker / a P-M-successor injects the resolver's env map (+ tmpfs) at sandbox boot |
| **One configured platform key, AES-GCM (no envelope/KMS/rotation)** | V1 single-node, one key; envelope/KMS is the production driver | S3-envelope + tink/OpenBAO drivers behind the same `store.Encryptor` seam (V2); + key rotation |
| **Resolver authz is RBAC, not workload `Grant`** | `Grant`/workload tokens are V2 | V2 internal-IAM via short-lived workload tokens through the same PDP |

## Contracts

### The AES-GCM encryptor (`internal/secrets/aesgcm/aesgcm.go`)
```go
// NewAESEncryptor returns an AES-256-GCM store.Encryptor. key must be 32 bytes.
func NewAESEncryptor(key []byte) (store.Encryptor, error)
```

### The delivery resolver (`internal/secrets/secrets.go`)
```go
type Deps struct {
	Store      store.Store
	Authorizer auth.Authorizer
	Logger     *slog.Logger
}
type Resolver struct { /* unexported */ }
func NewResolver(d Deps) (*Resolver, error)

// ResolveEnv reads the named Secrets in ns (store-decrypted), PDP-authorized, and returns
// their merged Data as an env-var map for sandbox injection.
func (r *Resolver) ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `internal/store` (Encryptor seam + Get), `internal/auth` (PDP), `api/types`, `api/fault`, stdlib `crypto/aes`/`crypto/cipher`/`crypto/rand` | no new lib |
| Adds (lib) | none | stdlib AEAD |
| Exposes | `aesgcm.NewAESEncryptor` (a `store.Encryptor`); `secrets.Resolver` + `NewResolver`; `SecretTypeOpaque` | encryptor wired into the store by P-I; resolver wired into the worker's boot path |

## Implementation plan

1. **`api/types/v1alpha1/secret.go`** — add `SecretTypeOpaque` (keep roundtrip + OpenAPI green).
2. **`internal/secrets/aesgcm/aesgcm.go`** — `NewAESEncryptor` (32-byte key guard; nonce-prepended GCM), one file.
3. **`internal/secrets/secrets.go`** — `Deps`, `Resolver`, `NewResolver`, `ResolveEnv` (authorize → read → flatten).
4. **Test plan** (one named test per Scenario; real encryptor + real store + real rbac PDP, no mocks):
   - `internal/secrets/aesgcm/aesgcm_test.go` → `encryptor-roundtrips`, `encryptor-rejects-bad-key`.
   - `internal/secrets/secrets_test.go` → `secret-stored-encrypted-at-rest` (store `WithEncryptor`; raw engine
     value ≠ plaintext; read = plaintext), `resolver-returns-bound-secret-env`, `resolver-authorizes`.
5. **Definition of done**: `just ci` green (four sub-checks); the encryptor round-trips + rejects a bad key +
   detects tamper; a Secret is ciphertext at rest + plaintext on read; the resolver returns env vars
   PDP-authorized + denies the unauthorized; OpenAPI regenerated; no new dependency; no globals; no `any`; no
   identity/path leak.

## Review checklist

- [ ] **AES-256-GCM `store.Encryptor`** (`NewAESEncryptor`): 32-byte-key guard (`encryptor-rejects-bad-key`),
      random nonce per message + tamper-detection (`encryptor-roundtrips`); stdlib AEAD, not hand-rolled.
- [ ] **Encrypted at rest**: a `Secret` in an encryptor-wired store is **ciphertext** on the engine, **plaintext**
      on read (`secret-stored-encrypted-at-rest`).
- [ ] **Delivery resolver** is a **PEP**: authorizes via the PDP **before** reading (`resolver-authorizes`,
      `fault.Forbidden`); returns the bound secret's `Data` as env vars (`resolver-returns-bound-secret-env`).
- [ ] `SecretTypeOpaque` added; roundtrip + OpenAPI green.
- [ ] `New(Deps)`, ctx-first, `api/fault`, `slog`, no globals, **no `any`**, **no new dependency**; the env/tmpfs
      injection + envelope/KMS/OpenBAO drivers + `Grant` authz are documented deferrals; no identity/path leak;
      every Scenario a named passing test.

## Consequences

- (+) **Secrets are encrypted at rest** (the blueprint's security floor) and the **delivery contract** (the
  env-var `Resolver`) exists. *Honest scope:* this ADR delivers **those two**, not the end-to-end "reads a
  secret" — the consuming injection is a separately-owned **P-M-successor** step (below).
- (+) **No new dependency** (stdlib AEAD); the `store.Encryptor` seam (ADR-0006) is filled, and the
  envelope/KMS/OpenBAO production drivers stay a clean swap behind it.
- (−) **One platform key, no rotation/per-namespace keys** — V1 single-node; bounded, the production drivers
  add envelope/KMS.
- (−) **The sandbox injection last-mile needs a P-M-successor ADR** (P-M is frozen; no `internal/worker` is
  built/scheduled) — so "reads a secret" end-to-end is a **named, recorded follow-up**, not delivered here.
  This is the exit-criterion's one open consuming step; the Step-6 reconcile keeps the F15 row honest about it.
- (note) **Roadmap build edges**: P-P's real edges are `ADR-0006` (Encryptor seam + store), `ADR-0018` (PDP),
  `ADR-0003`. (`ADR-0011` was the roadmap edge — but V1 secrets imports no runtime; the worker injection is the
  integration point.) The Step-6 reconcile records `ADR-0006`/`ADR-0018`/`ADR-0003` and demotes `ADR-0011`/`P-N`.

## Open questions

| Question | Where it gets answered |
|---|---|
| Sandbox env/tmpfs injection (populating `SandboxSpec.Env` + tmpfs from the `Resolver`) | a **P-M-superseding ADR** (the frozen P-M doesn't inject secrets) — a recorded V1 follow-up; **proven** (not implemented) at **P-S** e2e |
| S3-envelope + tink/OpenBAO encryptor drivers; key rotation / per-namespace keys | **V2** (behind the `store.Encryptor` seam) |
| Workload-identity `Grant` enforcement | **V2 internal IAM** |

## References

- [blueprint.md](../../blueprint.md) — "Secrets management" (encrypted at rest, env/tmpfs delivery, drivers),
  "Security model — Secrets at rest" (encrypted in the metastore; env/tmpfs).
- [ADR-0006](0006-store-database-layer-port.md) — the `store.Encryptor` seam + `WithEncryptor` this fills.
- [ADR-0018](0018-api-server-authn-rbac-admission.md) — the PDP the resolver calls.
