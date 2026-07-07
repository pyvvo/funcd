# ADR-0080: S3-protocol frontend on the blob substrate

- **Status**: Implemented
- **Date**: 2026-06-24 (Accepted 2026-06-29 after judge pass — folded M1 new-Bucket-kind plumbing, M2 schema.go
  curated-vocabulary edits + S3Identity principal, M3 connection→identity trust anchor, m4 s3::write base-permit;
  reframed as a **built-in provider** per the blueprint provider model. **Reviewing → Implemented 2026-06-30** —
  review **pass** (DoD 10/10), see docs/reviews/adr-0080-implementation-claude-opus-4-8.md; built in 3 slices
  [Bucket CRD → Cedar s3 binding-as-grant → s3gateway over blob.Bucket via versitygw], 12 scenarios green via the
  real AWS SDK client, security verified [cannot-forge-peer 403], CGO-free. In-platform identity per the
  superseding ADR-0085. One decider-accepted license exception: versitygw's transitive MPL-2.0 deps from its
  unused Vault backend.)
- **Deciders**: green-0-rabbit
- **Tags**: storage, blob, s3, gateway, lakehouse, duckdb, ducklake, cedar, authz
- **Realizes**: FEAT-0003/F47
- **Superseded in part by**: [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md) — its in-platform
  **AuthN/Identity** decision (anonymous S3 + connection-source-derived `Ref`) and the `embedgw.RunVersityGW`
  entry point are superseded (unsatisfiable with versitygw v1.6.0); the rest of this ADR (the `Bucket` CRD,
  `spec.blob`, the Cedar `s3::read`/`s3::write` binding-as-grant PEP, backend-over-`blob.Bucket`, `RangeReader`,
  config) **stands**.
- **Relates to**: [ADR-0007](0007-blob-storage-layer-port.md) (the `blob.Bucket` port) ·
  [ADR-0013](0013-gateway-ingress-httputil-primary.md)/[0029](0029-gateway-drop-lura-single-driver.md) (gateway TLS/middleware) ·
  [ADR-0018](0018-api-server-authn-rbac-admission.md) (scoped credentials, PDP port) ·
  [ADR-0074](0074-cedar-authorization-resource-access.md)/[0075](0075-cedar-invoke-authorization.md)/[0076](0076-cedar-kv-read-binding-grant.md) (the Cedar schema this extends) ·
  [ADR-0011](0011-runtime-sandbox-port.md) (worker netns) ·
  [ADR-0064](0064-fn-to-fn-rpc-links.md)/[0069](0069-kv-data-plane.md) (connection-scoped identity + binding-as-grant — the model this reuses) ·
  [ADR-0073](0073-kv-bindings-and-subdomains.md) (`spec.kv` binding precedent for `spec.blob`)

## Context & Need

**Purpose**: a TCP **S3-API endpoint backed by funcd's blob substrate**, so third-party S3 clients —
chiefly **DuckDB/DuckLake `httpfs`** — read **and** write Parquet against the *same bytes* functions use
via the blob facade. **Callers**: DuckDB (running as an in-platform function, or externally over the
SSH-tunnel/tailnet) and any S3 tool.

**Why now**: FEAT-0003 deploys a DuckLake lakehouse onto funcd, and that needs an S3 endpoint. funcd's
**blob facade is a function-facing get/put surface over the worker-local UDS** ([ADR-0069](0069-kv-data-plane.md)),
**not an S3-wire server**; and DuckDB `httpfs` requires the **S3 API** for writes and globbing
(`ListObjectsV2`) — plain HTTP yields only read-only "frozen" access. Without this surface the data
stack stays a separate Garage system beside funcd, its bytes ungoverned by the platform PDP.

## Scenarios

- **scenario: binding-grants-read** — *Given* an in-platform function bound `spec.blob` `{bucket: lakehouse,
  prefix: gold}` and **no** `Policy`, *When* its DuckDB (anonymous S3, sandbox-scoped endpoint) reads
  `s3://lakehouse/gold/q.parquet`, *Then* it reads the rows (ranged GET) — the binding is the grant, identity is
  connection-scoped.
- **scenario: owner-writes** — *Given* a function that is `lakehouse/bronze`'s `owner`, *When* its DuckDB writes
  `s3://lakehouse/bronze/x.parquet` (multipart), *Then* it lands via `blob.Put`; *And* a non-owner function merely
  *bound* to `bronze` writing the same key is denied (`403`, owner-only write).
- **scenario: unbound-denied** — *Given* a function with **no** `spec.blob` binding for `lakehouse/gold`, *When* it
  reads `s3://lakehouse/gold/q.parquet`, *Then* it is Forbidden (`403`, default-deny) and no blob is touched.
- **scenario: listobjects-glob** — *Given* several Parquet under a bound `silver/` prefix, *When* DuckDB globs
  `s3://<ns>/silver/*.parquet`, *Then* `ListObjectsV2` returns all matching keys.
- **scenario: external-sigv4** — *Given* an **external** DuckDB with a scoped access-key/secret (an `S3Identity`),
  *When* it `SELECT`s `s3://<ns>/gold/q.parquet` permitted by a `Policy`, *Then* it reads the rows; *And* an
  absent/invalid signature is rejected `403`.
- **scenario: cross-namespace-rejected** — *Given* a principal scoped to namespace `A` (a binding or an external
  keypair), *When* it requests `s3://B/any.parquet`, *Then* it is denied (`403`, default-deny tenancy).
- **scenario: rangereader-fallback** — *Given* a blob driver without `RangeReader`, *When* DuckDB issues a
  ranged GET, *Then* the s3gateway serves the same bytes via full `Get`+slice.
- **scenario: disabled-by-default** — *Given* no s3gateway config, *When* the daemon starts, *Then* no S3
  port is opened.

## Scope

**In**: the S3 frontend (versitygw `embedgw`) over `blob.Bucket`; the DuckDB/DuckLake-required S3 subset; the
**`Bucket` resource** (domain + owner'd prefix sub-domains, the KVStore parallel); **connection-scoped identity +
a `spec.blob` binding-as-grant** (Cedar `s3::read`/`s3::write`, write = `prefix.owner`) for in-platform functions;
a **SigV4 keypair for external clients only**; an **opt-in, node-private** TCP listener; the optional `RangeReader`
capability seam.

**Out**: the catalog/query service (F48) and workflow engine (F49); the rest of the S3 API (ACLs, bucket
policies, versioning, tagging, object-lock, CORS); virtual-host addressing; S3 as the metastore/general
blob backend; multi-node; the Observable build.

## Constraints & Decision drivers

- Deps **Apache-2.0/MIT** only; **pure-Go** static binary (no cgo) — versitygw satisfies both.
- **Embed-first**, single-binary — an in-process listener, not a supervised child.
- **Reuse** the existing `blob.Bucket` port and the `auth.Authorizer` PDP — no parallel storage/authz path.
- DuckDB `httpfs` reality: **TCP** endpoint, **path-style**, **SigV4**, **range reads**, **multipart writes**.
- Inherit **default-deny + audit** from the PDP.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Keep Garage external** (`s3blob` points at it) | Zero new code, battle-tested — but a second storage system with its own disk/identity/backup, and bytes the funcd PDP never sees. Kept as the fallback the `blob` port preserves; rejected as the default for the "funcd owns the bytes" goal. |
| **Plain-HTTP read-only front** ("frozen DuckLake") | Trivial, but no writes/globbing — the ELT pipeline can't run. Read-only is insufficient. |
| **Hand-roll the S3 protocol** | Full control, but SigV4 + XML + multipart + `ListObjectsV2` is a large, error-prone surface to own. Rejected vs an embeddable lib. |
| **gofakes3** (MIT) | Lighter backend interface, but test-oriented with weak/partial auth. Rejected vs versitygw's production SigV4 + injectable backend. |
| **MinIO gateway** | **AGPL-3.0** — violates the license rule. Rejected outright. |
| **UDS transport for the s3gateway** | Matches funcd's fn↔service default — but DuckDB `httpfs` is **TCP-only**, so there is no consumer for a socket. Rejected; UDS stays the fn-SDK channel only ([ADR-0064](0064-fn-to-fn-rpc-links.md)/[0069](0069-kv-data-plane.md)). |

## Decision

Add an in-process frontend **`internal/blob/s3gateway`** that exposes the `blob.Bucket` substrate over the
S3 REST API via **versity/versitygw `embedgw`** (Apache-2.0), on an **opt-in, node-private TCP listener**
(reachable from worker netns and host loopback) behind the gateway's TLS + auth. **AuthN** = **connection-scoped
per-function identity** for in-platform workloads (no issued credential — the KV/invoke model), a manual SigV4
keypair only for external clients; **AuthZ** = a **`spec.blob` binding-as-grant** PEP on the existing **cedar-go**
PDP (`s3::read`/`s3::write`).
Garage is retired as the data-plane store; funcd owns the bytes (the `blob` port keeps external-S3 a swap).

Concretely:

- **Library / backend injection** — `embedgw.RunVersityGW(ctx, be backend.Backend, cfg *Config)` accepts
  **any** `backend.Backend`; we are **not** tied to its posix/FS backend. The funcd backend embeds
  `backend.BackendUnsupported` and overrides only the **DuckDB/DuckLake subset** (~12–15 of ~60 methods),
  mapping AWS-SDK-Go-v2 `s3` types ↔ `blob.Bucket`.
- **Ranged reads** — DuckDB reads Parquet by HTTP range (footer + needed row groups), but `blob.Bucket.Get`
  returns whole objects. Add an **optional** capability interface (below): the gocloud driver implements it
  via `bucket.NewRangeReader`, the memory driver by slicing; the s3gateway type-asserts and **falls back to
  full `Get`+slice** when absent. Keeps the port minimal and contract tests **additive**.
- **Addressing** — **path-style only** (`http://host:port/{bucket}/{key}`; DuckDB `s3_url_style='path'` +
  `s3_endpoint`). The S3 *bucket* is a namespaced **`Bucket`** resource and the key's **leading segment is its
  prefix sub-domain** — `s3://lakehouse/gold/q.parquet` = `Bucket` `lakehouse`, prefix `gold`.
- **Identity — connection→principal is resolved by funcd, never client-asserted, and fail-closed.** The single
  shared listener runs a funcd auth middleware *in front of* versitygw: it maps the **connection source** (the
  worker netns / per-sandbox endpoint provisioned on the [ADR-0011](0011-runtime-sandbox-port.md) path, the same
  anchor `context.kv`/`context.invoke` use) to the caller's `Function` `Ref`, and **a source it cannot map to a
  known sandbox is denied — never falls through to anonymous-allow**. SigV4 (an `S3Identity`) is consulted *only*
  for external/loopback connections that present a signature. The trust anchor is thus committed here; only the
  exact netns→`Ref` plumbing symbol (per-sandbox endpoint vs bridge source-IP map) is the implementation detail
  left to the open question.
- **AuthZ — `spec.blob` binding-as-grant** (the KV model, *mirroring `builtin_kv.cedar` exactly*): a Function binds
  `spec.blob` (alias → `Bucket` + prefix); the **binding IS the read grant** (default-deny), and **write requires
  `caller == prefix.owner`** (the single-writer invariant declared on the `Bucket`). The backend is a **PEP** on the
  PDP `(principal, s3::action, bucket/prefix)`; `s3::read`/`s3::write` + a `Bucket`/`BlobPrefix` resource
  (materialized from the `Bucket` CRD) join the curated schema, and a `blobBindings` Set on the caller `Function`
  drives the built-in read-`permit` + the write `permit`+owner-`forbid` pair — exactly as `spec.kv` +
  `KVStore.tables[].owner` ([ADR-0073](0073-kv-bindings-and-subdomains.md)/[ADR-0076](0076-cedar-kv-read-binding-grant.md)).
  Full contract below.

## Temporary workarounds

- **Multipart assembled before a single `Put`** — the `blob` port has no streaming-multipart seam, so a
  multipart upload buffers (memory/temp) then `Put`s once, bounded by `s3gateway.maxUploadBytes` (**fail-closed**:
  an upload past the cap is rejected, never OOMs the daemon). *Exit*: a streaming/multipart `blob` seam, at
  which point the cap is lifted.
- **One s3gateway per process** — versitygw `embedgw` is single-instance-per-process (package-level globals
  for bucket-name validation + debug logging). *Exit*: none needed at single-node; revisit at multi-node.

## Contracts

```go
// internal/blob — OPTIONAL capability; the s3gateway type-asserts and falls back to Get+slice.
type RangeReader interface {
    // GetRange returns bytes [offset, offset+length) of the object at key.
    // A negative length means "to end". fault.NotFound if the key is absent.
    GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error)
}
```

```go
// internal/blob/s3gateway — the funcd S3 backend over blob.Bucket (versitygw backend.Backend).
// Embeds BackendUnsupported; overrides ONLY the DuckDB/DuckLake subset. Each method is a PEP:
// it calls auth.Authorizer with an s3::read | s3::write Request before delegating to blob.
type backend struct {
    backend.BackendUnsupported          // all other methods → NotImplemented
    bucketFor func(ns, bucket string) blob.Bucket // (namespace, Bucket resource) → substrate bucket
    pdp       auth.Authorizer
    log       *slog.Logger
}
// Overridden: GetObject(+Range), PutObject, HeadObject, DeleteObject(s), ListObjects, ListObjectsV2,
//             CreateMultipartUpload, UploadPart, CompleteMultipartUpload, AbortMultipartUpload, ListParts,
//             HeadBucket, ListBuckets, CreateBucket, DeleteBucket (the last two overridden to return Forbidden).

// New returns the embeddable handler/server wiring (versitygw embedgw) over the backend.
func New(d Deps) (*Server, error)   // Deps: BucketFor, PDP, Credentials, Logger, Listen
func (s *Server) Run(ctx context.Context) error  // serves until ctx is done; opt-in (no-op if disabled)
```

*Buckets are `Bucket` resources*: `HeadBucket` succeeds iff a `Bucket` of that name exists in the caller's
namespace and the caller is bound to it; `ListBuckets` returns the `Bucket`s the caller is bound to;
`CreateBucket`/`DeleteBucket` are overridden to **return Forbidden** — `Bucket`s are managed via the control plane, not over S3.

**Config** (`internal/config`, all defaulted ⇒ zero-config unchanged):

```yaml
s3gateway:
  enabled: false            # opt-in (default-off): no listener unless true
  listenAddr: "127.0.0.1:9000"  # node-private; reachable from host loopback + worker netns
  maxUploadBytes: 1073741824     # fail-closed cap on a buffered (multipart) object — see Temporary workarounds
  # credentials resolved from the SigV4 keypair store (per-namespace access-key/secret) — see AuthN below
```

**Identity — connection-scoped for trusted workloads; a keypair only for external clients.** Mirroring the
worker-local API ([ADR-0064](0064-fn-to-fn-rpc-links.md)/[ADR-0069](0069-kv-data-plane.md)), an **in-platform**
function's S3 principal is its **connection-scoped `Ref`** (namespace + function), fixed by the sandbox and
**never asserted in the request** — the same identity `context.kv`/`context.invoke` use. Its DuckDB targets a
**sandbox-scoped S3 endpoint** with **anonymous** S3 (no keys); the gateway derives the caller from the
connection source. **Nothing is issued, stored, or rotated** — the Lambda-execution-role property without a
keypair. Only an **external** client (a DuckDB over the tunnel, outside any sandbox) presents a manually-issued
**SigV4 access-key/secret** → an `S3Identity` principal: the single untrusted edge, verified via versitygw's
IAM/account seam over a small keypair store (the exact account-provider symbol is pinned in the Open questions).

**Data domain — a `Bucket` resource, the exact KVStore parallel.** An S3 *bucket* is a namespaced **`Bucket`**
CRD (the domain); its `spec.prefixes[]` are **sub-domains** (the medallion layers), each with an **`owner`** =
the single writer — mirroring `KVStore.spec.tables[].owner` ([ADR-0073](0073-kv-bindings-and-subdomains.md)). The
S3 key's leading segment selects the prefix sub-domain; the rest is the object path.

**AuthZ — `spec.blob` binding-as-grant via Cedar**, the *exact* KV model
([ADR-0073](0073-kv-bindings-and-subdomains.md)/[ADR-0076](0076-cedar-kv-read-binding-grant.md)):

- A Function declares `spec.blob []FunctionBlob` (alias → bucket + prefix), the wrangler/`spec.kv` convention.
  **The binding IS the read grant; default-deny — no binding ⇒ Forbidden. Write requires `caller == prefix.owner`**
  (the single-writer invariant, declared on the `Bucket`, never self-asserted). **An owner-less prefix is read-only**
  (the write `forbid`'s `resource has owner` guard fires when there is no owner — the `builtin_kv.cedar` rule exactly).
- Actions → `curatedActions` (`internal/auth/cedar/schema.go`): add `auth.ActionS3Read = "s3::read"` and
  `auth.ActionS3Write = "s3::write"` to `authorizer.go`, and the two entries to the `curatedActions` map.
- Entity types → `curatedEntityTypes` (`schema.go`): add the entity-type-name consts **`entityTypeBucket = "Bucket"`**
  (domain) + **`entityTypeBlobPrefix = "BlobPrefix"`** (sub-domain, carries `owner` — the `KVStore`/`KVTable` parallel)
  + **`entityTypeS3Identity = "S3Identity"`** (the **external** principal only), and the three entries to the
  `curatedEntityTypes` map. Update `ValidateCedar`'s curated-vocabulary check so an admin `Policy` naming
  `Action::"s3::read"`/`S3Identity` passes admission (today it is rejected as an unknown action/entity type). The
  authz granularity is the declared `(bucket, prefix)` sub-domain, **materialized from the `Bucket` CRD** (owner/attrs)
  exactly like `KVTable` from `KVStore`; the object key is then checked to fall under a bound prefix.
- `EntitiesFor` materializes a **`blobBindings`** Set on the caller `Function` from `spec.blob` (each bound
  `(bucket, prefix)` as a `BlobPrefix` ref, guarded by `principal has blobBindings`) — exactly as it builds
  `kvBindings`/`links` ([entities.go:113-125](../../internal/auth/cedar/entities.go)); `resourceUID` gains a
  `BlobPrefix` branch, and **`principalUID` gains an `S3Identity` branch** (entities.go:56 today Internal-faults any
  non-`Function` principal, so the `external-sigv4` permit path needs it).
- Built-in rules ship as `internal/auth/cedar/builtin_s3.cedar` (concatenated into the built-in PolicySet in
  `compile()`, beside the KV built-ins), **mirroring `builtin_kv_read.cedar` + `builtin_kv.cedar` token-for-token**:
  a read `permit` guarded by `principal has blobBindings`, and the write **base `permit` + owner-`forbid` pair** with
  the `resource has owner` guard (the cedar block below). `Policy` resources refine (`forbid` to revoke a read;
  `permit` a cross-binding/cross-namespace read).

```go
// api/types/v1alpha1 — the Bucket domain (mirrors KVStore) + the spec.blob binding (mirrors FunctionKV).
type Bucket struct { /* TypeMeta + ObjectMeta */ Spec BucketSpec }
type BucketSpec struct {
    Prefixes       []BucketPrefix `json:"prefixes"`                 // sub-domains (the medallion layers)
    MaxObjectBytes int64          `json:"maxObjectBytes,omitempty"` // per-object resource policy on THIS bucket (0 ⇒ unset)
}
// Two distinct caps, both enforced (the binding one wins on conflict): BucketSpec.MaxObjectBytes is the
// per-bucket *resource policy* (a write past it ⇒ Forbidden); s3gateway.maxUploadBytes is the *daemon-wide*
// buffer safety cap on a single buffered multipart object (a write past it ⇒ rejected, fail-closed — see
// Temporary workarounds). A write must satisfy BOTH; the effective limit is min(MaxObjectBytes>0, maxUploadBytes).
type BucketPrefix struct {
    Name  string     `json:"name"`            // a key prefix (DNS-1123); the medallion layer
    Owner ObjectName `json:"owner,omitempty"` // the single writer (a Function in this ns); empty ⇒ read-only
}
type FunctionBlob struct {
    Alias  string     `json:"alias"`  // local handle; DNS-1123, unique within Blob
    Bucket ObjectName `json:"bucket"` // a Bucket in this function's namespace
    Prefix string     `json:"prefix"` // a sub-domain (BucketPrefix.name) of that bucket
}
// FunctionSpec gains:  Blob []FunctionBlob `json:"blob,omitempty"`
```

```cedar
// builtin_s3.cedar — mirrors builtin_kv_read.cedar + builtin_kv.cedar token-for-token. The binding is the
// read capability; the Bucket's prefix.owner is the single writer — no Policy for the happy path (ADR-0076
// parity). A Policy appears only to REVOKE a read or widen one.

// read: a declared spec.blob binding grants s3::read (the principal has blobBindings guard makes an
// unbound function inert; reads on unbound prefixes stay default-deny).
permit (principal, action == Action::"s3::read", resource)
  when { principal has blobBindings && principal.blobBindings.contains(resource) };

// write: single-writer. The base permit lets any principal write, the forbid overrides it unless the
// principal IS the prefix owner; the `resource has owner` guard keeps an owner-less prefix read-only
// (the forbid still fires with no owner to match). WITHOUT the base permit even the owner could not write.
permit (principal, action == Action::"s3::write", resource);
forbid (principal, action == Action::"s3::write", resource)
  unless { resource has owner && principal == resource.owner };
```

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | `blob.Bucket` (+ optional `RangeReader`) · `auth.Authorizer` (PDP) · the connection-scoped caller `Ref` ([ADR-0064](0064-fn-to-fn-rpc-links.md)/[0069](0069-kv-data-plane.md)) · `Function.spec.blob` bindings + `Bucket` resources (the data domain) · a **SigV4 keypair store for external clients only** (a new kind alongside [ADR-0018](0018-api-server-authn-rbac-admission.md) bearer keys) · the gateway TLS config ([ADR-0013](0013-gateway-ingress-httputil-primary.md)) · a worker-netns-reachable listen addr ([ADR-0011](0011-runtime-sandbox-port.md)) |
| Exposes | an S3 REST endpoint (TCP, path-style) — anonymous for in-netns fns / SigV4 for external — GET(+Range)/PUT(+multipart)/HEAD/DELETE/ListObjectsV2 |
| Config keys | `s3gateway.enabled`, `s3gateway.listenAddr`, `s3gateway.maxUploadBytes` |
| New deps | `github.com/versity/versitygw` (Apache-2.0) + `aws-sdk-go-v2/service/s3` (check the net `go list -deps` delta — some may already arrive via `gocloud.dev/blob` `s3blob`) |

## Implementation plan

- **Files**: `internal/blob/s3gateway/{s3gateway.go,backend.go,auth.go}`; `internal/blob/rangereader.go`
  (the capability + memory/gocloud impls); Cedar `internal/auth/cedar/{builtin_s3.cedar,schema.go,entities.go,policies.go}`
  (the new built-in file + its `compile()` concat; the `blobBindings` Set; the `Bucket`/`BlobPrefix`/`S3Identity`
  schema entries; the `resourceUID` `BlobPrefix` + `principalUID` `S3Identity` branches); config keys in
  `internal/config`; wiring as a `pkg/funcd` option + `cmd/funcd`.
- **The new `Bucket` kind — the full plumbing (modeled verbatim on ADR-0073's KVStore checklist), not just the
  type**: `api/types/v1alpha1/bucket.go` (the `Bucket`/`BucketSpec`/`BucketPrefix` types + `Validate`); **register it
  in `metadata.go`** — `KindBucket` const, the `Kind.Validate` switch arm, the `NewObject` switch, and `AllKinds`
  (it is namespaced by default; no `Namespaced()`/`StatusObject` edit — `Bucket` has no observed status);
  **per-kind CRUD handlers** in `internal/controlplane/handlers.go` (its own `getObj/createObj/listObj/replaceObj/
  deleteObj(KindBucket,…)` set + route registration — handlers are per-kind, not generic); **`spec.blob` on
  `function.go`** (the `FunctionBlob` field + structural admission, mirroring `spec.kv`); **admissions cloned from
  `internal/controlplane/admission/kvstore.go`** — `bucket-prefix-owner-exists` (each `prefixes[].owner` is a real
  Function), `blob-binding-validity` (each `spec.blob` names an existing `Bucket`+prefix), `bucket-deletion-protection`
  (block Delete **and** an Update that removes a prefix while it is bound or non-empty — via a `blob`-prefix prober
  like `KVProber`), and `bucket-count` quota; **OpenAPI regen** (`just generate` → `internal/controlplane/cmd/specgen`)
  + `pkg/sdk/kinds.go` (`{"buckets", true}`). The same apply-ordering as ADR-0073 (owner-exists needs the owning
  Function first; binding-validity needs the `Bucket` first) governs the e2e fixtures.
- **go.mod**: add `versity/versitygw`; record the `aws-sdk-go-v2/service/s3` delta.
- **Test plan**:
  - `blobcontract` gains an **additive** `RangeReader` case, run against memory + gocloud (skipped if the
    driver doesn't implement it — the fallback path tested separately).
  - s3gateway tests drive the **real AWS SDK Go v2 s3 client** against the in-process gateway — one acceptance
    test per Scenario (`binding-grants-read`, `owner-writes`, `unbound-denied`, `listobjects-glob`, `external-sigv4`,
    `cross-namespace-rejected`, `rangereader-fallback`, `disabled-by-default`).
  - a Cedar unit test for the built-ins: unbound `s3::read` default-deny; a bound read permitted; **the owner
    writes** (proves the base `permit(s3::write)` is present); a non-owner bound principal's write denied; an
    **owner-less prefix is read-only** (the `resource has owner` guard); and an admin `Policy` naming `s3::read`/
    `S3Identity` passes `ValidateCedar`.
  - an **optional node-gated DuckDB lane** (real DuckDB `httpfs` over the gateway) deferred to the homebox
    `FUNCD_IT=1` e2e, per the containerd-lane precedent ([ADR-0052](0052-bench-containerd-cgroup-footprint-lane.md)).
- **Definition of done**: `go build/test/lint` + `go mod verify` green; **OpenAPI regenerated** (the `Bucket` kind
  on the wire); `go list -deps` delta recorded; the s3gateway absent from the binary unless enabled; identity/path
  grep clean; `just ci` green after commit.

## Review checklist

- [ ] s3gateway opens **no** port unless `s3gateway.enabled`.
- [ ] backend embeds `BackendUnsupported`; only the required subset overridden; unrequired ops return NotImplemented.
- [ ] `RangeReader` optional with a working `Get`+slice fallback; `blobcontract` additive (memory + gocloud pass).
- [ ] in-platform identity is **connection-scoped** (the sandbox `Ref`); **no credential issued/rotated** for trusted fns; DuckDB uses anonymous S3.
- [ ] `spec.blob` **binding IS the read grant** (default-deny): a `blobBindings` Set on the caller `Function` + built-in `permit(s3::read)`; **write requires `caller == prefix.owner`** (declared on the `Bucket`); an unbound fn is Forbidden.
- [ ] external clients only: SigV4 verified against a scoped **access-key/secret keypair** → an `S3Identity`; bad/absent signature → `403`.
- [ ] **every** S3 op is a PEP → cedar PDP (`s3::read`/`s3::write`); default-deny; decisions audited; the `BlobPrefix` resource materialized from the `Bucket` CRD; `principalUID`/`resourceUID` gain the `S3Identity`/`BlobPrefix` branches.
- [ ] **cross-namespace denied**: a binding/keypair scoped to ns A cannot touch ns B's bucket (tenancy default-deny).
- [ ] path-style addressing; S3 bucket → namespace mapping.
- [ ] listener node-private (worker netns + host loopback), behind gateway TLS + auth.
- [ ] pure-Go (no cgo); new deps Apache-2.0/MIT; `go list -deps` delta recorded.
- [ ] one passing acceptance test per Scenario.

## Consequences

- **(+)** funcd owns all bytes — one substrate, two surfaces (function SDK over UDS · S3 wire over TCP);
  DuckLake data governed by the platform PDP; Garage retired.
- **(+)** **trusted in-platform functions need no issued credential** — S3 access is the declarative `spec.blob`
  binding + connection-scoped identity, the same model as KV/links (ADR-0073/0076); a manual keypair exists only
  for external clients. Medallion governance is real Cedar.
- **(+)** the `blob` port still keeps external-S3 (Garage/AWS) a config swap.
- **(+)** the S3 frontend is a **built-in provider** (the blueprint's provider model) — an in-daemon, pure-Go,
  always-on protocol endpoint: a node-private TCP listener + connection-scoped identity + Cedar binding-as-grant,
  bridging the S3 wire *directly* to the daemon-internal `blob.Bucket` port. Built-in because versitygw is
  embeddable pure-Go and it needs its **own** listener + native (SigV4) auth + direct port access. It is **not** an
  add-on: an add-on provider (e.g. F48's catalog, the FEAT-0004 observability serving provider) is an out-of-daemon
  DuckDB **service function** reached through the ingress gateway. (Earlier drafts wrongly called F48 a "Quack
  gateway / second consumer of a protocol-gateway seam" — F48 is an add-on provider, not a built-in one.)
- **(+)** **F48's catalog is an add-on provider that consumes the data plane through this S3 (built-in) provider** —
  *not* `blob.Bucket` in-process: its DuckDB engine is out-of-process (cgo can't live in the pure-Go daemon,
  ADR-0065) and reaches Parquet only via httpfs/S3, so F48 reads/writes under the **same** Cedar/binding governance
  as any function (no privileged bypass). F48's catalog *metadata* is separate — local to F48, reached over Quack.
- **(−)** new dependency weight (`aws-sdk-go-v2/service/s3` via versitygw).
- **(−)** a TCP surface (anonymous for in-netns fns, SigV4 for external) to operate; versitygw single-instance-per-process;
  the in-platform connection→identity middleware is **fail-closed** (an unmappable source denies, never anonymous-allows)
  and leans on ADR-0064/0069 + the network-manager identity — its concrete netns→`Ref` plumbing symbol is the one open item.
- **(−)** multipart RAM bound until a streaming blob seam exists.
- **Risk**: versitygw is young (pin **v1.6.0**, 2026-06-26) — API churn; range/multipart correctness with real
  DuckDB is mitigated by the deferred node-gated DuckDB lane.

## Open questions

- **The netns-source→`Ref` plumbing symbol** — the *trust anchor* is decided (the Decision's Identity bullet: a
  funcd middleware maps connection source → `Function` `Ref`, fail-closed on an unmappable source, SigV4 only at
  the external edge). What remains is the concrete mechanism — **per-sandbox endpoint vs bridge source-IP map** —
  which leans on the ADR-0064/0069 per-sandbox provisioning + the network-manager identity (egress precedent) and
  is *resolved at implementation* on the [ADR-0011](0011-runtime-sandbox-port.md) path. (If it grows past a wiring
  detail, it splits to a short follow-up ADR — the one-topic rule.)
- **External SigV4-keypair lifecycle & the versitygw IAM symbol** — the external-only keypair kind (issue /
  rotate / revoke) and the *exact* versitygw account-provider interface it backs are pinned *in implementation
  or a short follow-up ADR*.

## References

- versitygw `embedgw` + `backend` (pkg.go.dev, `github.com/versity/versitygw@v1.6.0`, 2026-06-26) — Apache-2.0;
  injectable `backend.Backend`, `BackendUnsupported` base; `embedgw.RunVersityGW(ctx, backend.Backend, *Config)`
  (one instance per process).
- DuckDB **httpfs** S3 API support; **Frozen DuckLake** (read-only over plain HTTP); **Quack** remote protocol
  (DuckDB v1.5.3) — context for F48.
- [ADR-0007](0007-blob-storage-layer-port.md), [ADR-0018](0018-api-server-authn-rbac-admission.md),
  [ADR-0074](0074-cedar-authorization-resource-access.md)/[0075](0075-cedar-invoke-authorization.md)/[0076](0076-cedar-kv-read-binding-grant.md),
  [ADR-0064](0064-fn-to-fn-rpc-links.md)/[0069](0069-kv-data-plane.md) (the UDS sibling channel).
- Tracking: [Project #4 card](https://github.com/users/green-0-rabbit/projects/4/views/1?pane=issue&itemId=206092364)
  *"S3-protocol frontend on the blob substrate (data-platform epoch)"* — status follows this ADR's lifecycle.
