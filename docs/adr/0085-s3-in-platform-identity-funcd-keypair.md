# ADR-0085: In-platform S3 identity — funcd-managed per-function keypair via an in-process IAM

- **Status**: Implemented
- **Superseded in part by**: [ADR-0158](0158-pool-member-identity.md) (2026-10-05) — Constraints, cannot-forge-peer premise, Alternatives, Consequences: pool mates can read each other's keypairs.
- **Superseded in part by**: [ADR-0175](0175-engine-is-its-own-principal.md) (2026-10-05) — fixed in-platform key format and decoding a key to (ns, fn): the key carries the owner kind.
- **Date**: 2026-06-29 (Accepted 2026-06-29 after one judge pass — **no Blockers; the security model was verified
  end-to-end against real versitygw v1.6.0** [SigV4 → `CheckValidSignature(…, account.Secret, …)` against
  `IAMService.GetUserAccount`'s returned secret ⇒ a function can sign only as itself]. Folded the two Majors
  [`iam` lists the full 6-method `auth.IAMService` incl. `Shutdown()`; entry point corrected to
  `s3api.New` + `ServeMultiPort([]string{listenAddr})`/`ShutDown()` — funcd owns the bind addr + lifecycle, not the
  `net.Listener` object] + the Minor [`Account.UserID` is `int`; identity is carried by `Access`] + Nits. Clean
  partial supersession of ADR-0080 §AuthN + entry point only. **Reviewing → Implemented 2026-06-30** — review
  **pass** (DoD 7/7), see docs/reviews/adr-0080-implementation-claude-opus-4-8.md; `internal/blob/s3gateway/iam.go`
  [HMAC per-fn keypair + the full 6-method in-process `auth.IAMService` via `s3api.New`] + the worker-env injection;
  signs-as-self / cannot-forge-peer [403] / deterministic-across-restart green.)
- **Deciders**: green-0-rabbit
- **Tags**: storage, blob, s3, identity, authn, sigv4, cedar, lakehouse
- **Realizes**: [FEAT-0003/F47](../feat/0003-feat-data-platform.md)
- **Supersedes (in part)**: [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) — **only** its in-platform
  **AuthN/Identity** decision (the "anonymous S3 + connection-source-derived `Ref`, nothing issued" model) and the
  **`embedgw.RunVersityGW` entry point**. Everything else in ADR-0080 stands unchanged: the `Bucket` CRD,
  `spec.blob`, the Cedar `s3::read`/`s3::write` **binding-as-grant PEP**, the backend-over-`blob.Bucket`,
  `RangeReader`, and config.
- **Relates to**: [ADR-0057](0057-secret-injection-last-mile.md) (the worker-env injection seam this reuses) ·
  [ADR-0011](0011-runtime-sandbox-port.md) (`WorkerSpec.Env`) · [ADR-0018](0018-api-server-authn-rbac-admission.md)
  (scoped credentials) · [ADR-0074](0074-cedar-authorization-resource-access.md)/[0076](0076-cedar-kv-read-binding-grant.md)
  (the Cedar PEP, unchanged) · [ADR-0065](0065-metastore-badger-engine.md) (pure-Go, no cgo)

## Context & Need

**Purpose**: decide **how a caller's identity reaches the S3 backend** so the ADR-0080 Cedar `spec.blob`
binding-as-grant authz can run. ADR-0080 specified *anonymous S3 for in-platform functions, with funcd deriving
the `Function` `Ref` from the connection source via "a funcd auth middleware in front of versitygw."*

**Why now (the blocker)**: that model is **not realizable with the chosen library** (verified at implementation,
versitygw v1.6.0):
- `embedgw.RunVersityGW(ctx, be, cfg)` **owns its own listener** (`ServeMultiPort(cfg.Ports)`) and returns no
  wrappable handler — there is **no "middleware in front" seam** on its listener.
- it **requires** SigV4 (`RootUserAccess`/`RootUserSecret` are mandatory) and authenticates **every** request as a
  SigV4 `auth.Account`; there is **no anonymous-with-identity** mode (public access exists only via per-bucket
  ACL/Policy — all-or-nothing, not per-caller).
- `embedgw` builds its IAM internally from `auth.New(Opts{Dir: cfg.IAMDir})` — **no custom in-process
  `auth.IAMService` can be injected** through `RunVersityGW`.

So an Accepted-but-frozen ADR-0080 carries an unsatisfiable AuthN contract. This ADR re-decides *only* that, the
minimal change that makes F47 buildable, keeping ADR-0080's authz and data model intact. **Callers**: the S3
backend (reads the resolved principal), the function reconciler (injects the keypair), versitygw's SigV4 layer
(resolves the access key via funcd's IAM).

## Scenarios

- **scenario: keypair-injected** — *Given* a function declares `spec.blob` and the s3gateway is enabled, *When*
  its sandbox launches, *Then* funcd injects a **per-function SigV4 keypair** + endpoint into the worker env
  (`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_ENDPOINT_URL_S3`/`AWS_REGION`), so its DuckDB signs as itself.
- **scenario: signs-as-self** — *Given* a request SigV4-signed with function `A`'s injected keypair, *When* the
  gateway authenticates it, *Then* the backend principal is `A`'s `Ref` and the ADR-0080 Cedar PEP runs on it.
- **scenario: cannot-forge-peer** — *Given* function `A` (which holds only its own injected secret), *When* it
  tries to sign a request as function `B`'s access key, *Then* SigV4 verification fails (`403`) — `A` cannot
  derive `B`'s secret, so tenant isolation holds without the connection source.
- **scenario: deterministic-across-restart** — *Given* the same function, *When* the daemon restarts, *Then* its
  keypair is **identical** (derived, not stored) — nothing is persisted per function or rotated.
- **scenario: external-keypair** — *Given* an **external** client with a funcd-issued scoped access-key/secret,
  *When* it signs a request, *Then* the same in-process IAM resolves it to an `S3Identity` principal (the ADR-0080
  external edge, unchanged); a bad/absent signature is `403`.
- **scenario: disabled-no-injection** — *Given* the s3gateway is disabled, *When* a function launches, *Then* no
  keypair is injected and no IAM/listener exists.

## Scope

**In**: the in-platform S3 **AuthN** mechanism — a funcd-derived **per-function deterministic SigV4 keypair**
(secret = HMAC of a node master secret over the `Ref`), **injected into the sandbox env** (reusing the ADR-0057
seam); a funcd **in-process `auth.IAMService`** that resolves an access key → its `Ref` (in-platform) or an
`S3Identity` (external); using versitygw's **`s3api.New(be, root, region, iam, …)`** entry point (which accepts a
custom IAM) **instead of `embedgw.RunVersityGW`**; the backend's `account → principal` mapping.

**Out** (unchanged — ADR-0080 governs): the `Bucket` CRD + `spec.blob`; the Cedar `s3::read`/`s3::write`
binding-as-grant **authz** (`builtin_s3.cedar`, schema, entities); the backend-over-`blob.Bucket` method set;
`RangeReader`; the `s3gateway.*` config; path-style addressing; the rest of the S3 API. External-keypair
**lifecycle** (issue/rotate/revoke UX) beyond "the IAM resolves a stored scoped keypair" — a follow-on.

## Constraints & Decision drivers

- **Must fit versitygw v1.6.0**: SigV4-account auth, IAM injected via `s3api.New` (not `RunVersityGW`).
- **Preserve the security property**: a function can act **only as itself** — it must not be able to sign as a
  peer or another tenant. (ADR-0080's tenancy default-deny is non-negotiable.)
- **No stored/rotated per-function credential** (keep ADR-0080's "no issued credential" spirit as far as the
  library allows): derive deterministically; the function never *chooses* its secret, funcd injects it.
- **Reuse**: the ADR-0057 worker-env injection; the existing Cedar PEP (no authz change); pure-Go, no cgo.
- **Minimal supersession**: touch only AuthN + the entry point; leave ADR-0080's authz/data model verbatim.

## Alternatives considered

| Option | Why it lost / won |
|---|---|
| **Per-function funcd-derived keypair + in-process IAM via `s3api.New`** — **chosen** | The minimal model versitygw supports: a deterministic keypair (no storage/rotation), injected into the sandbox, resolved by a funcd `auth.IAMService` plugged into `s3api.New`. A function holds only its own secret ⇒ isolation. Keeps ADR-0080's Cedar PEP untouched. |
| **Keep `embedgw.RunVersityGW` + `IAMDir` files** | Stays with ADR-0080's exact entry point — but forces funcd to **write/delete a per-function account file** on every Function reconcile (stateful dir, races, cleanup). Rejected vs a stateless in-process IAM via `s3api.New`. |
| **funcd re-signing front-proxy** (preserve "anonymous" to the worker) | Keeps DuckDB key-free — but funcd runs a second listener, derives the `Ref` from the connection source, **re-signs** as a per-`Ref` account, and forwards to versitygw on loopback. More moving parts + funcd still holds per-`Ref` secrets. Rejected as heavier for no isolation gain. |
| **Single root account for all in-platform fns** | Trivial — but every request authenticates as root, so the backend **can't distinguish principals** and the Cedar PEP collapses. Rejected: breaks per-function authz/tenancy. |
| **Reconsider the library** | versitygw is otherwise a good fit (Apache-2.0, pure-Go, injectable backend + IAM); the only gap is the front-middleware/anonymous assumption, which this ADR closes. Not worth re-opening. |

## Decision

In-platform S3 AuthN is a **funcd-managed per-function deterministic SigV4 keypair**, injected into the sandbox,
resolved by a **funcd in-process `auth.IAMService`** wired through versitygw's **`s3api.New`**. Concretely:

- **Derivation (deterministic, no storage).** For a function `Ref` `(ns, fn)`:
  - `accessKey = "FUNCD" + base32-noPad-upper(ns + "\x00" + fn)` — a decodable, AWS-charset-valid access key. The
    IAM base32-decodes it back to `(ns, fn)`; it is **not secret** (knowing it grants nothing).
  - `secretKey = base64( HMAC-SHA256(nodeMasterSecret, "s3:" + ns + "/" + fn) )` — the secret a request must sign
    with. Only funcd (holding `nodeMasterSecret`) can compute it; a function never derives a peer's secret.
  - `nodeMasterSecret` is a **node-local** secret: generated on first start and persisted `0600` at
    `<dataDir>/s3gateway/master.key`, or supplied via `s3gateway.masterSecretFile`. Restart-stable ⇒ keypairs are
    stable (*deterministic-across-restart*); never leaves the daemon.
- **Injection (reuses ADR-0057).** When the s3gateway is enabled and a function declares `spec.blob`, the
  reconciler merges into the worker `Env` (the same `secretEnv` merge `workerSpec` already does, reserved-`FUNCD_`
  precedence): `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION` (`us-east-1`), `AWS_ENDPOINT_URL_S3`
  (the node-private `s3gateway.listenAddr`). The function's DuckDB picks these up natively (`httpfs`); the function
  **does not choose** the secret. No `spec.blob` ⇒ no injection.
- **Resolution (`auth.IAMService`).** `internal/blob/s3gateway`'s `iam` implements versitygw's `auth.IAMService`:
  `GetUserAccount(access)` decodes an in-platform access → `(ns, fn)`, recomputes `secretKey`, and returns
  `auth.Account{Access: access, Secret: secretKey, Role: auth.RoleUser}` (identity is carried by `Access`;
  `Account.UserID` is an `int` and unused here); an **external** access is looked up in the funcd keypair store →
  its stored secret + an `S3Identity` marker. SigV4 verification (versitygw's `authentication` middleware →
  `CheckValidSignature(…, account.Secret, …)`) therefore passes **only** for the holder of the matching secret —
  *signs-as-self* / *cannot-forge-peer*. Existence/authorization is **not** decided here (a decodable access for a
  missing function yields a derivable secret the attacker still cannot produce); the **Cedar PEP in the backend**
  is the real gate (an absent `spec.blob` ⇒ no `blobBindings` ⇒ default-deny, unchanged from ADR-0080).
- **Entry point.** The s3gateway server is built with **`s3api.New(be, root, region, iam, auditLog, …)`** and served
  via the returned `*S3ApiServer`'s **`ServeMultiPort([]string{s3gateway.listenAddr})`** (versitygw binds funcd's
  node-private addr; `ShutDown()` for graceful stop), **not** `embedgw.RunVersityGW` (which cannot take a custom
  IAM). funcd owns the **bind address + lifecycle**, not the `net.Listener` object (the Fiber app is unexported).
  `root` is a daemon-internal root account (a random per-process keypair, used by nothing external) so versitygw's
  required-root invariant is satisfied without granting anyone root.
- **Principal mapping.** The backend reads `utils.ContextKeyAccount.Get(ctx).(auth.Account)` set by versitygw after
  SigV4, decodes `Access` → an in-platform `Function` `Ref` (the Cedar principal) or an `S3Identity` (external),
  and runs the ADR-0080 Cedar PEP `(principal, s3::action, bucket/prefix)` exactly as specified.

The result is ADR-0080's intent — *a function reaches S3 as itself, governed by its `spec.blob` binding, with no
user-chosen or rotated credential* — realized within versitygw's SigV4 model: the credential is funcd-derived,
funcd-injected, and never leaves the function's own sandbox.

## Temporary workarounds

- **A secret is injected into the sandbox env** (vs ADR-0080's "no keys in the function"). Bounded: it is the
  function's *own* per-`Ref` secret, scoped to its bindings, non-rotating, and unusable to reach any other tenant.
  *Exit*: a versitygw (or replacement) anonymous/trusted-connection mode, or a re-signing front-proxy, would let
  the sandbox be key-free — revisit only if injection proves a real exposure.

## Contracts

```go
// internal/blob/s3gateway — the funcd in-process IAM (versitygw auth.IAMService) + keypair derivation.

// Keypair derives a function's deterministic S3 credentials from a node master secret. Pure; no I/O, no storage.
type Keypair struct{ AccessKey, SecretKey string }

// DeriveKeypair returns the stable per-(namespace, function) keypair (access decodable to the Ref, secret =
// HMAC-SHA256(master, "s3:"+ns+"/"+fn)).
func DeriveKeypair(master []byte, ns, fn string) Keypair

// decodeAccess maps an in-platform access key back to its Ref; ok=false for a non-funcd (external) access.
func decodeAccess(access string) (ns, fn string, ok bool)

// iam implements the FULL versitygw auth.IAMService — all SIX methods must exist (CreateAccount, GetUserAccount,
// UpdateUserAccount, DeleteUserAccount, ListUserAccounts, Shutdown). In-platform accounts are DERIVED (stateless);
// external accounts resolve from the funcd keypair store. Only GetUserAccount/ListUserAccounts are load-bearing
// for the gateway read path; the three mutators route to the external store (or return a not-supported fault —
// derived accounts have no mutable state), and Shutdown is a no-op.
type iam struct {
    master   []byte
    external ExternalKeys // the funcd-issued scoped-keypair store (external clients only)
}
func (s *iam) GetUserAccount(access string) (auth.Account, error)             // derive (in-platform) or store-lookup (external)
func (s *iam) ListUserAccounts() ([]auth.Account, error)                      // the external store's accounts
func (s *iam) CreateAccount(account auth.Account) error                       // external store, else fault.Invalid (not supported)
func (s *iam) UpdateUserAccount(access string, props auth.MutableProps) error // external store, else fault.Invalid
func (s *iam) DeleteUserAccount(access string) error                          // external store, else fault.Invalid
func (s *iam) Shutdown() error                                                // no-op

// ExternalKeys is the funcd store of issued external scoped keypairs (the one untrusted edge); its lifecycle UX
// is a follow-on. An access → (secret, scoped namespace) for the S3Identity principal.
type ExternalKeys interface {
    Lookup(access string) (secret string, namespace string, ok bool)
}

// Server is built on s3api.New (NOT embedgw.RunVersityGW) so the custom iam is injectable; it is served via the
// returned *S3ApiServer's ServeMultiPort([]string{listenAddr}) and stopped via ShutDown().
func New(d Deps) (*Server, error) // Deps adds Master []byte, External ExternalKeys to the ADR-0080 Deps
```

**Worker-env injection** (reconciler, when `s3gateway.enabled` && `len(fn.Spec.Blob) > 0`):

| key | value |
|---|---|
| `AWS_ACCESS_KEY_ID` | `DeriveKeypair(master, ns, fn).AccessKey` |
| `AWS_SECRET_ACCESS_KEY` | `DeriveKeypair(master, ns, fn).SecretKey` |
| `AWS_REGION` | `us-east-1` |
| `AWS_ENDPOINT_URL_S3` | `http://<s3gateway.listenAddr>` (node-private) |

**Config** (additive to ADR-0080's `s3gateway.*`):

```yaml
s3gateway:
  masterSecretFile: ""   # optional; path to the node S3 master secret. Empty ⇒ generated + persisted 0600 at <dataDir>/s3gateway/master.key
```

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | the node master secret (file/generated) · `Function.spec.blob` (gate injection) · `WorkerSpec.Env` (ADR-0057 seam) · versitygw `s3api.New` + `auth.IAMService`/`auth.Account` · the funcd external-keypair store |
| Exposes | a per-function derived SigV4 keypair (injected) · an `auth.IAMService` resolving access → principal · the `account → Ref/S3Identity` mapping the ADR-0080 backend PEP consumes |
| Config keys | `s3gateway.masterSecretFile` (+ the ADR-0080 `s3gateway.*`) |
| New deps | none beyond ADR-0080's `versity/versitygw` (now via its `s3api`/`auth` packages) |

## Implementation plan

- **Files**: `internal/blob/s3gateway/{iam.go (DeriveKeypair, decodeAccess, iam, ExternalKeys),s3gateway.go (build
  on s3api.New + a funcd listener; the root-account internal keypair),auth.go (account→principal mapping)}`; the
  master-secret load/generate in `s3gateway` + a `s3gateway.masterSecretFile` config key; the **keypair injection**
  in `internal/function` (the `workerSpec`/`secretEnv` path, gated on `spec.blob` + s3gateway enabled), wired from
  `pkg/funcd`. (All ADR-0080 files — `backend.go`, the Cedar `builtin_s3.cedar`/schema/entities, the `Bucket` CRD,
  `RangeReader` — are implemented per ADR-0080, unchanged by this ADR except that `backend` reads the principal
  from the account.)
- **go.mod**: `versity/versitygw` (already required by ADR-0080); use its `s3api` + `auth` packages.
- **Test plan** (one acceptance test per Scenario, names echoing `scenario: <name>`):
  - `s3gateway` unit: `DeriveKeypair` is deterministic (`deterministic-across-restart`); `decodeAccess`
    round-trips a `Ref` and rejects an external access; `iam.GetUserAccount` returns the derived secret for an
    in-platform access and the stored secret for an external one.
  - `signs-as-self` / `cannot-forge-peer`: drive the **real AWS SDK Go v2 s3 client** against the in-process
    gateway — A's keypair authenticates as A and reads its bound prefix; a request signed with the wrong secret
    for B's access ⇒ `403`.
  - `keypair-injected`: the reconciler injects the four `AWS_*` env keys for a `spec.blob` function and none for a
    function without it / when disabled (`disabled-no-injection`).
  - `external-keypair`: a stored external keypair authenticates → `S3Identity`; bad signature ⇒ `403`. (These
    extend the ADR-0080 `external-sigv4` scenario with the concrete IAM.)
- **Definition of done**: `go build/test/lint` + `go mod verify` green; one passing test per Scenario; the secret
  never logged; pure-Go (no cgo); identity/path grep clean; `just ci` green after commit. (ADR-0080's own DoD —
  Bucket kind on the wire, OpenAPI regen, etc. — is satisfied by the F47 implementation that consumes this ADR.)

## Review checklist

- [ ] In-platform keypair is **derived** (HMAC over the `Ref`), **deterministic** across restart, **never stored
      per function**; the master secret is `0600`, node-local, never logged/exported.
- [ ] The keypair is **injected into the sandbox env** (ADR-0057 seam), only when `s3gateway.enabled` &&
      `spec.blob` present; the function does not choose the secret.
- [ ] `iam` implements `auth.IAMService`; the gateway is built on **`s3api.New`** with the custom `iam` (not
      `embedgw.RunVersityGW`); the internal root account is unused externally.
- [ ] SigV4 verification passes **only** for the secret holder — `signs-as-self` ✓, `cannot-forge-peer` → `403`.
- [ ] `account → principal`: in-platform access decodes to a `Function` `Ref`; external → `S3Identity`; the
      **ADR-0080 Cedar PEP is unchanged** and remains the authorization gate (default-deny).
- [ ] External clients: a stored scoped keypair → `S3Identity`; bad/absent signature → `403`.
- [ ] Supersedes ADR-0080 **only** in AuthN + entry point; its `Bucket`/`spec.blob`/Cedar/backend/`RangeReader`/
      config are untouched.
- [ ] One passing acceptance test per Scenario; secret not in any log line.

## Consequences

- **(+)** **F47 is buildable** — the identity model fits versitygw exactly; the Cedar binding-as-grant authz from
  ADR-0080 runs unchanged on the resolved principal.
- **(+)** **Isolation preserved without the connection source** — a function holds only its own derived secret, so
  it can act only as itself; cross-tenant signing is cryptographically impossible (needs the node master secret).
- **(+)** **No per-function credential storage or rotation** — keypairs are derived on demand and stable; only a
  single node master secret is persisted.
- **(+)** **Minimal supersession** — ADR-0080's data model + authz + backend are untouched; only AuthN + the
  versitygw entry point change.
- **(−)** **A secret now lives in the sandbox env** (vs ADR-0080's "no keys in the function") — bounded to the
  function's own scope, non-rotating; a key-free sandbox needs a library anonymous/trusted mode (Temporary workaround).
- **(−)** **funcd owns the bind address + lifecycle** (`s3api.New` + `ServeMultiPort([]string{listenAddr})` +
  `ShutDown()`) instead of letting `embedgw.RunVersityGW` run it — a little more wiring, in exchange for injectable
  identity.
- **Risk**: a weak/leaked `nodeMasterSecret` would let an attacker mint any function's keypair — mitigated by
  `0600` + node-local + never-logged + never-exported; rotating it re-derives all keypairs (a deliberate, rare op).

## Open questions

- **External-keypair lifecycle UX** — issue/rotate/revoke of the external scoped keypairs (the `ExternalKeys`
  store) — a follow-on (a `Secret`-kind or a dedicated resource); ADR-0080 already deferred this.
- **DuckDB credential delivery** — `AWS_*` env is the portable default; whether a DuckDB `CREATE SECRET` is
  preferable for some runtimes is an implementation tuning, not a contract.

## References

- versitygw v1.6.0 (Apache-2.0): `s3api.New(be, root, region, iam auth.IAMService, …)` (the IAM-injectable entry
  point) served via `*S3ApiServer.ServeMultiPort([]string)` + `ShutDown()`; `auth.IAMService` (6 methods) /
  `auth.Account` / `utils.ContextKeyAccount`; `s3api/middlewares/authentication.go` — SigV4 verified via
  `CheckValidSignature(…, account.Secret, …)` against `IAMService.GetUserAccount`'s returned secret (the trust
  anchor); `embedgw.RunVersityGW` (the root-only/IAMDir convenience this replaces for the in-process IAM).
- [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (the S3 frontend this refines),
  [ADR-0057](0057-secret-injection-last-mile.md) (worker-env injection), [ADR-0076](0076-cedar-kv-read-binding-grant.md)
  (the binding-as-grant Cedar model, unchanged), [FEAT-0003](../feat/0003-feat-data-platform.md).
