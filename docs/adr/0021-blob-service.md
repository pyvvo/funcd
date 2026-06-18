# ADR-0021: Blob service — the storage layer, function-facing (`internal/services/blob`)

- **Status**: Implemented
- **Date**: 2026-06-14 (**Implemented 2026-06-14** — review pass (zero findings), see docs/reviews/adr-0021-implementation-claude-opus-4-8.md; DoD 4/4, 5 scenarios. **Reviewing 2026-06-14** — implemented: `internal/services/blob` (Facade over
  `blob.Bucket` + the blob `TypeHandler`) + `ServiceTypeBlob`/`BlobServiceSpec`; 5 scenarios pass, OpenAPI
  regenerated, four sub-checks green, no new deps. **Accepted 2026-06-14** after judge pass — no Blockers. Folded the judge's **Major**:
  `SignedURL` authorizes the **capability the URL grants**, derived from `opts.Method` (`SignGet`→`VerbGet`,
  `SignPut`→`VerbUpdate`, `SignDelete`→`VerbDelete`) — not read — so a `viewer` can't mint a presigned PUT
  (privilege escalation); added the viewer-denied-presign assertion. Decision: blob = a `TypeHandler` on the
  ADR-0019 dispatcher + a PDP-authorized, key-prefixed facade over the existing `blob.Bucket`; one pooled
  bucket, `Grant` authz + per-binding buckets deferred. The first reuse of the service pattern; no new deps.)
- **Deciders**: green-0-rabbit
- **Tags**: service, facade, blob, storage, reconciler, data-plane
- **Realizes**: [FEAT-0000/F23](../feat/0000-feat-v1.md) (blob storage service: get/put/list/delete/presign over `blob` port; built on F21)
- **Relates to**: [ADR-0019](0019-service-facade-pattern-kv.md) (the **service facade pattern** + the `Service`
  dispatcher this registers a `TypeHandler` on — blob copies KV's shape), [ADR-0007](0007-blob-storage-layer-port.md)
  (the `blob.Bucket` port + gocloud driver this exposes function-facing — **already built**),
  [ADR-0018](0018-api-server-authn-rbac-admission.md) (the `auth.Authorizer` PDP the facade calls — the PEP),
  [ADR-0003](0003-resource-model-and-api-typing.md) (the `Service` kind — this ADR adds its `blob` type),
  [ADR-0006](0006-store-database-layer-port.md) (the store the handler reconciles `Service`s from),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (conventions), [blueprint.md — Services / Blob storage](../../blueprint.md).
  **New deps: none** (the `blob.Bucket` port + gocloud driver already exist; this is the service layer over them).

## Context & Need

ADR-0007 shipped the **storage layer** — the `blob.Bucket` port (get/put/delete/exists/list/**signed URL**) with
a gocloud driver spanning memory/file/S3. ADR-0019 shipped the **service facade pattern** (CRD + facade +
dispatcher + driver) and its first instance (KV). F23 is the **second instance**: expose the storage layer
**function-facing** as a blob service — exactly KV's shape, only the driver SDK changes (the blueprint:
"the same shape applies to KV, vector, secrets, config — only the driver SDK changes"). This is the ADR-0019
pattern's first **reuse**, proving P-O/P-P add a `TypeHandler` + a facade, never a new `Service` reconciler.

**Purpose**: ship `internal/services/blob` — a function-facing **`Facade`** (PDP-authorized,
`<namespace>/<binding>/<key>`-prefixed) over the `blob.Bucket` port, and the blob **`TypeHandler`** the
ADR-0019 `Service` dispatcher routes `type:blob` to. Callers: a function reads/writes/presigns blob objects via
the facade (in-process for V1); the composition root constructs the facade + registers the handler on the
dispatcher. Conformance is mechanical (real in-memory gocloud bucket + the real PDP): a facade Put/Get
round-trips within a binding, keys are tenant-prefixed (no cross-namespace collision), an unauthorized access
is denied before the bucket, a `Service{type:blob}` reconciles to Ready, and a presigned URL is issued.

## Scenarios

- `scenario: blob-facade-roundtrips` — **Given** the blob facade over an in-memory bucket, **when** a function
  Puts an object under `(namespace, binding, key)` then Gets it, **then** the same bytes return; Delete makes
  it absent; List returns the binding's keys (tenant prefix stripped).
- `scenario: blob-facade-prefixes-by-namespace-and-binding` — **Given** the blob facade, **when** namespace
  `team-a` and `team-b` (same binding+key) Put, **then** the two objects are independent (stored as
  `team-a/<binding>/<key>` vs `team-b/...` — no cross-tenant collision).
- `scenario: blob-facade-authorizes-each-access` — **Given** the facade wired to the PDP, **when** an identity
  not permitted in the namespace calls Get/Put/SignedURL, **then** it is denied (`fault.Forbidden`) before the
  bucket is touched (the facade is a PEP).
- `scenario: blob-facade-presigns` — **Given** an authorized identity, **when** it requests a presigned URL for
  a `(binding, key)`, **then** the facade returns a signed URL from the bucket (for the prefixed key). **And** a
  `viewer` (read-only) is **denied** a presigned **PUT** URL (`fault.Forbidden`) — `SignedURL` authorizes the
  capability the URL grants, not read (no escalation).
- `scenario: blob-service-reconciles-to-ready` — **Given** a `Service{type:blob}` in the store, **when** the
  ADR-0019 dispatcher (with the blob handler registered) runs, **then** it validates the binding and writes
  `Status.Phase=Ready`.

## Scope

**In**:
- **`internal/services/blob`**: the **`Facade`** (`Get`/`Put`/`Delete`/`List`/`SignedURL` scoped to
  `(namespace, binding)`, PDP-authorized, key-prefixed, results prefix-stripped) over `blob.Bucket`; the blob
  **`TypeHandler`** (`NewHandler`, `Type()==ServiceTypeBlob`) for the ADR-0019 dispatcher.
- **`Service` spec**: add `ServiceTypeBlob` + `BlobServiceSpec{Binding}` to `ServiceSpec` (F23-owned).

**Out (deferred)**:
- **Per-binding distinct buckets / bucket lifecycle / quotas** — V1 pools one `blob.Bucket` and multiplexes by
  key prefix (the blueprint's "few buckets, metadata in keys"); per-binding bucket provisioning is a follow-up.
- **Workload-identity `Grant` authz** — V2 (ADR-0018 deferred); V1 facade authz uses the caller's `Identity`.
- **The sandbox→facade transport** — P-M/worker (V1 wires in-process), same as KV.
- **S3/file driver wiring at boot** — the composition root (P-I) selects the `blob.Bucket` driver; this ADR is
  driver-agnostic (tests use the in-memory gocloud bucket).

## Constraints & Decision drivers

- **C1 — reuse the ADR-0019 pattern, don't reinvent**: blob is a `TypeHandler` + a facade copying KV — the proof
  the service pattern generalizes. No new `Service` reconciler (one-reconciler-per-gvk; the dispatcher owns it).
- **C2 — the facade is a PEP**: every access calls the `auth.Authorizer` PDP before the bucket; default-deny.
- **C3 — tenant isolation by key prefix**: `<namespace>/<binding>/<key>`; results prefix-stripped (as KV).
- **C4 — ADR-0002 conventions**: `New(Deps)`; ctx-first; `api/fault`; typed enums; no globals; no `any`; no mocks
  (real in-memory gocloud bucket + real PDP).

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Blob = a `TypeHandler` + a facade over `blob.Bucket`, copying KV (ADR-0019)** | proves the pattern reuses; no new reconciler; presign included | the facade is near-identical to KV's (acceptable — different driver) | **chosen** |
| A bespoke blob subsystem | — | breaks the uniform shape; no PDP PEP, no dispatcher | rejected (ADR-0019 settled this) |
| Functions call `blob.Bucket` directly | one fewer layer | no authz, no tenant prefix, no pooling | rejected (security/isolation) |

Driver: the **gocloud `blob.Bucket`** (ADR-0007) already spans memory/file/S3 — no driver work here; the facade
is driver-agnostic.

## Decision

### 1. The blob facade (`internal/services/blob`) — the KV shape, blob driver
`Facade`, built with `NewFacade(FacadeDeps{Bucket, Authorizer, Logger})`. Each method takes the caller's
`auth.Identity` + `(namespace, binding, key)`: **authorize** (`Authorizer.Authorize(ctx, {Identity, verb,
KindService, namespace})`; `Get`/`List`→`VerbGet`/`VerbList`, `Put`→`VerbUpdate`, `Delete`→`VerbDelete`; deny →
`fault.Forbidden`) → **prefix** the key `<namespace>/<binding>/<key>` (`List` strips the prefix from results) →
**delegate** to `blob.Bucket`. **`SignedURL` authorizes the *capability the URL grants*, derived from
`opts.Method`** — `SignGet`→`VerbGet`, **`SignPut`→`VerbUpdate`, `SignDelete`→`VerbDelete`** — because a
presigned PUT/DELETE URL bypasses the facade to write/delete directly; minting it as a read would let a
`viewer` escalate. It then signs the prefixed key with the requested method/expiry.

### 2. The blob `TypeHandler` (the dispatcher slice)
`NewHandler()` returns the `services.TypeHandler` with `Type()==v1.ServiceTypeBlob`; its `Reconcile(ctx,
*v1.Service)` validates `spec.blob.binding` (the in-memory bucket needs no external provisioning) and returns —
the dispatcher writes `Status.Phase=Ready`. Registered on the **same** ADR-0019 dispatcher as KV.

### 3. The `Service` `blob` spec (F23-owned)
```go
const ServiceTypeBlob ServiceType = "blob"
type ServiceSpec struct {
	Type ServiceType    `json:"type,omitempty"`
	KV   *KVServiceSpec   `json:"kv,omitempty"`
	Blob *BlobServiceSpec `json:"blob,omitempty"` // set when Type == blob
}
type BlobServiceSpec struct { Binding string `json:"binding"` }
```

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **One pooled `blob.Bucket`, key-prefix multiplexing** | the blueprint's "few buckets, metadata in keys" | per-binding bucket provisioning + lifecycle is a follow-up |
| **Facade authz is RBAC (caller Identity), not workload `Grant`** | `Grant`/workload tokens are V2 | V2 internal-IAM via short-lived workload tokens through the same PDP |
| **In-process facade; no sandbox transport** | sandboxes are P-M | P-M/worker exposes the sandbox-local API |

## Contracts

### The blob facade + handler (`internal/services/blob/blob.go`)
```go
type FacadeDeps struct {
	Bucket     blob.Bucket
	Authorizer auth.Authorizer
	Logger     *slog.Logger
}
type Facade struct { /* unexported */ }
func NewFacade(d FacadeDeps) (*Facade, error)
func (f *Facade) Get(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) ([]byte, error)
func (f *Facade) Put(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string, data []byte) error
func (f *Facade) Delete(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) error
func (f *Facade) List(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, prefix string) ([]string, error)
func (f *Facade) SignedURL(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string, opts blob.SignOptions) (string, error)

// NewHandler returns the blob services.TypeHandler (Type() == ServiceTypeBlob).
func NewHandler() services.TypeHandler
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `internal/blob` (Bucket), `internal/auth` (PDP), `internal/services` (TypeHandler), `internal/controller` (Result), `api/types`, `api/fault` | no new lib |
| Adds (lib) | none | gocloud blob already present |
| Exposes | `blob` service `Facade` + `NewHandler`; `ServiceTypeBlob` + `BlobServiceSpec` | facade called by functions (P-M wires transport); handler registered on the dispatcher by P-I |

## Implementation plan

1. **`api/types/v1alpha1/service.go`** — add `ServiceTypeBlob` + `BlobServiceSpec` + `ServiceSpec.Blob`
   (keep roundtrip green; regenerate the OpenAPI).
2. **`internal/services/blob/blob.go`** — `FacadeDeps`, `Facade` (authorize→prefix→delegate over `blob.Bucket`,
   `List` strips, `SignedURL` signs the prefixed key), `NewHandler` (the blob `TypeHandler`).
3. **Test plan** (one named test per Scenario; real in-memory gocloud bucket + real rbac PDP + real store):
   - `internal/services/blob/blob_test.go` → `blob-facade-roundtrips`, `blob-facade-prefixes-by-namespace-and-binding`,
     `blob-facade-authorizes-each-access`, `blob-facade-presigns`, `blob-service-reconciles-to-ready` (the
     dispatcher with the blob handler → Ready).
4. **Definition of done**: `just ci` green (four sub-checks); blob round-trips + prefixes + authorizes +
   presigns; the handler drives `Service{type:blob}` to Ready on the ADR-0019 dispatcher; OpenAPI regenerated;
   no new dependency; no globals; no `any`; no identity/path leak.

## Review checklist

- [ ] `blob` **`Facade`** over `blob.Bucket`: authorizes via the PDP **before** the bucket
      (`blob-facade-authorizes-each-access`, `fault.Forbidden`); prefixes `<ns>/<binding>/<key>` + strips on List
      (`blob-facade-prefixes-by-namespace-and-binding`); round-trips (`blob-facade-roundtrips`); presigns the
      prefixed key (`blob-facade-presigns`).
- [ ] The blob **`TypeHandler`** (`Type()==ServiceTypeBlob`) drives `Service{type:blob}` to `Status.Phase=Ready`
      on the **ADR-0019 dispatcher** (no new `Service` reconciler) — `blob-service-reconciles-to-ready`.
- [ ] `ServiceTypeBlob` + `BlobServiceSpec` added; roundtrip + OpenAPI green.
- [ ] `New(Deps)`, ctx-first, `api/fault`, `slog`, no globals, **no `any`**, **no new dependency**; deferrals
      (per-binding buckets, `Grant` authz, transport) documented; no identity/path leak; every Scenario a named
      passing test.

## Consequences

- (+) The **storage layer is function-facing** (the V1 blob service), and the ADR-0019 pattern's **first reuse**
  proves it generalizes — P-P (secrets) follows the same path.
- (+) **No new dependency** (the blob port + gocloud driver already exist); **presign** included (the storage
  layer's signed-URL capability surfaced to functions, PDP-gated).
- (−) **One pooled bucket + key-prefix** (not per-binding buckets) — the blueprint's pooling guidance; bounded.
- (−) Facade authz is **RBAC, not workload `Grant`** — V2, same as KV.
- (note) **Roadmap build edges**: P-O's real edges are `ADR-0007` (blob), `ADR-0019` (the dispatcher/pattern),
  `ADR-0018` (PDP), `ADR-0003`/`ADR-0006`. (P-N is the dispatcher dep — real.) The Step-6 reconcile records these.

## Open questions

| Question | Where it gets answered |
|---|---|
| Per-binding distinct buckets + bucket lifecycle/quotas | a follow-up |
| Workload-identity `Grant` enforcement | **V2 internal IAM** |
| Sandbox→facade transport | **P-M / worker** |

## References

- [blueprint.md](../../blueprint.md) — "Services / Blob storage" (the storage layer exposed function-facing on
  the service pattern), "Internal IAM" (facades are PEPs; key-prefix tenant isolation).
- [ADR-0019](0019-service-facade-pattern-kv.md) — the service facade pattern + dispatcher this reuses.
- [ADR-0007](0007-blob-storage-layer-port.md) — the `blob.Bucket` port + gocloud driver this exposes.
