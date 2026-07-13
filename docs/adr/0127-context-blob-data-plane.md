# ADR-0127: context.blob data-plane — native blob binding accessor over the worker-node local API

- **Status**: Implemented
- **Implemented**: 2026-07-12 — review gate (claude-opus-4-8) **pass**: all builds/lint/tests green on both tags
  plus the Node (50) and Python (79) shim suites; the facade's authz shape matches `s3gateway.authorize`
  byte-for-byte with a Function principal, the legacy no-`Action` facade is gone, and every Scenario maps to a
  named passing test ([review](../reviews/adr-0127-implementation-claude-opus-4-8.md)).
- **Date**: 2026-07-12 (judged 2026-07-12 — round 1 caught a **Blocker**: the draft routed to the unused legacy
  `services/blob.NewFacade` (RBAC `Kind:Service`, no per-object cedar `Action` ⇒ 500, cannot enforce bind-as-grant);
  re-anchored on a new binding-gated facade — the `services/kv.Facade` twin — authorizing the already-Function-aware
  `S3Capability` (`s3::read`/`s3::write` on a `BlobPrefix`) with a Function principal, resolving alias→(bucket,prefix)
  from `spec.blob`, keyed via the s3gateway `blobKey`/`s3BucketFor` so objects coexist with the S3 frontend; folded
  the keyspace/signed-URL (M2), missing-resolver (M3), and content-type/size-cap Minors. Round 2: pass-worthy, no open
  Blockers/Majors — verified the authz shape matches `s3gateway.authorize` byte-for-byte.)
- **Deciders**: green-0-rabbit
- **Tags**: blob, data-plane, worker-node, local-api, sdk, shim, facade, bind-as-grant, identity
- **Realizes**: [FEAT-0001/F92](../feat/0001-feat-v1.1.md) (context.blob — native blob binding accessor)
- **Relates to**: [ADR-0069](0069-kv-data-plane.md) (**the pattern** — context.kv over the worker-node local
  API; this is its blob twin), [ADR-0091](0091-function-catalog-consumer-binding.md) (context.catalog — the
  same binding-accessor shape), [ADR-0064](0064-fn-to-fn-rpc-links.md) (**reuses** the per-sandbox local API —
  HTTP-over-UDS + connection-scoped identity), [ADR-0019](0019-service-facade-pattern-kv.md) (the Facade
  pattern), [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (the S3 frontend this **complements** —
  external inspect stays there; in-function native access lands here) + [ADR-0085](0085-s3-gateway-request-signing-keypair.md)
  (the injected-keypair path this removes for in-function use), [ADR-0073](0073-kv-bindings-and-subdomains.md)
  / [ADR-0076](0076-cedar-kv-read-binding-grant.md) (blob bindings + the **bind-as-grant** PDP),
  [ADR-0116](0116-capability-authorization-framework.md) (the `blobBindings` capability the Facade authorizes against)

## Context & Need

A function reaches blob today **only** through the ADR-0080 **S3-protocol frontend** (`internal/blob/s3gateway`,
served over a versitygw TCP listener), addressed with an **injected SigV4 keypair** (ADR-0085). So every
function that touches blob hand-rolls an S3 client (boto3 in Python, `@aws-sdk/*` in Node) and drags in the
keypair + prefix-owner model — see `examples/python/releve-lakehouse/functions/s3util.py`. There is **no
`context.blob`**, even though its two siblings — `context.kv` (ADR-0069) and `context.catalog` (ADR-0091) —
already exist over the same per-sandbox worker-node local API.

This ADR adds the missing **function-facing blob data-plane**: `context.blob.{get,put,delete,list,signedUrl}`,
the exact blob analogue of `context.kv`. Every seam already exists:

- **The transport + identity** — ADR-0064's per-sandbox worker-node local API (HTTP-over-UDS, connection-scoped
  identity fixed at provisioning), which already carries `/invoke/` and `/kv/`.
- **The bind-as-grant PDP** — the cedar **`S3Capability`** (ADR-0080/0116) `s3::read`/`s3::write` on a
  `BlobPrefix`. Crucially, its principal materializer **already handles a `*v1.Function`**: a Function's
  `spec.blob` bindings become its `blobBindings` Set (`internal/auth/cedar/capabilities.go` `S3Capability`),
  and `FunctionPrincipalSource` resolves the Function principal. So a **function** principal asking
  `s3::read`/`s3::write` on `BlobPrefix(ns, bucket, prefix)` is **Allowed iff** its `spec.blob` declares that
  binding — bind-as-grant, with **zero PDP changes**.
- **The substrate + keyspace** — the s3gateway resolves an S3 `(ns, bucket)` to a prefixed substrate view via
  `s3BucketFor` (`pkg/funcd`) and keys objects `blobKey(prefix, object)`. Reusing **the same** resolver and
  keyspace makes `context.blob` objects the **same** objects the S3 frontend serves.

So the function-facing path is a small **binding-gated facade that mirrors `services/kv.Facade`** — resolve
alias→(bucket, prefix) from the Function's `spec.blob`, authorize `s3::read`/`s3::write` on the `BlobPrefix`
via the existing `S3Capability`, then act on the `s3BucketFor` substrate view under `blobKey` — with new
`/blob/…` verbs on the local API in front of it. (The unused legacy `services/blob.NewFacade`, ADR-0021,
authorizes `Kind:Service` with no per-object `Action` — an RBAC-role question, not bind-as-grant — and has
**zero production callers**; it is **not** the seam and is superseded by this facade.) No new transport,
identity model, or dependency is invented.

## Scenarios

- **scenario: blob-read-write** — Given a function with a blob binding `b`, When its handler calls
  `context.blob.put("b","k",data)` then `context.blob.get("b","k")`, Then it reads back `data` (over the
  worker-node local API → Facade → bucket), with **no keypair and no S3 client**.
- **scenario: blob-list** — Given several objects written under binding `b`, When the handler calls
  `context.blob.list("b","prefix/")`, Then it gets exactly the binding's keys under `prefix/`, tenant-prefix
  stripped.
- **scenario: blob-signed-url** — Given an object under binding `b`, When the handler calls
  `context.blob.signedUrl("b","k")`, Then it gets a **substrate-driver presigned URL** for that object (a real
  fetchable URL on an S3/GCS backend; best-effort on the local dev mem/file backend), the PDP having
  authorized `s3::read` (or `s3::write` for a PUT/DELETE method) on the bound prefix first.
- **scenario: blob-unbound-forbidden** — Given a function that did **not** declare an alias `nope`, When its
  handler calls `context.blob.get("nope","k")`, Then the handler returns **403** (RFC 9457) — the Facade's
  bind-as-grant PDP denies an unbound alias (ADR-0073/0076 default-deny).
- **scenario: blob-size-cap** — Given a `put` whose body exceeds the in-memory cap, When the handler calls
  `context.blob.put`, Then the local API rejects it with **413/422** (RFC 9457) before buffering unboundedly.
- **scenario: blob-parity** — Given one Node function and one Python function each bound to the same blob
  substrate, When both `context.blob.put` then `context.blob.get`, Then both round-trip identical bytes (the
  two shims speak the same wire).

## Scope

**In**: a **function-facing blob facade** in `internal/services/blob` (binding-gated + `S3Capability`-authorized
+ `blobKey`-keyed, mirroring `services/kv.Facade`) + its **blob binding resolver** (alias→(bucket, prefix)
from `spec.blob`); blob verbs on the worker-node local API (`GET/PUT/DELETE /blob/{binding}/{key...}`,
`GET /blob/{binding}` list, `GET /blob/{binding}/{key...}?sign=1` signed-URL); a `Blob` port in
`internal/workernode/local` (the connection-scoped `(ns, fn, binding, key)` shape kv uses — the facade
satisfies it directly, building the Function principal internally like `kv.Facade`, so **no adapter**);
wiring the facade into the local-API Manager (nil ⇒ no `/blob` routes, exactly like nil kv); the shim
`context.blob` client (Node + Python) + its typed `FunctionContext`; a per-object **in-memory size cap**.

**Out**: **streaming** / multipart blob I/O (v1 is bytes-in-memory, capped — streaming is the named v2
follow-up); **content-type / metadata** on `put` (the substrate `Bucket.Put` carries none today — v2, with
streaming); a *public* blob API outside the sandbox (the local API is sandbox-only, ADR-0064; external access
stays the ADR-0080 S3 frontend); changing the **`S3Capability` PDP** or the s3gateway (both reused unchanged);
the legacy Service-dispatcher blob `TypeHandler` (untouched); the typed-context **codegen** (`funcdctl types`
already emits blob aliases — `bindingRows` in `pkg/sdk/types_gen.go` — unchanged).

## Constraints & Decision drivers

- **Reuse, don't reinvent** — the worker-node local API (ADR-0064), the `services/blob.Facade` (ADR-0080), and
  the `blobBindings` bind-as-grant PDP (ADR-0076/0116) are all reused. **Zero new deps.**
- **Symmetry with context.kv** — the shape, wire, error mapping, and cap must mirror ADR-0069 so the three
  accessors (`kv`, `blob`, `catalog`) read the same to an author and to a maintainer.
- **Connection-scoped identity** — the caller `Ref` is the sandbox's fixed `(ns, fn)`; the request never names
  a caller (the ADR-0064 security spine — no SSRF, no client-asserted identity). Bind-as-grant default-deny
  (ADR-0073/0076) evaluates unchanged: an unbound alias is Forbidden.
- RFC 9457 on the wire; ctx-first; `api/fault`; pure-Go; block-style YAML in examples.

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| Keep the **injected-keypair + S3 client** path (status quo) | Already works; standard S3 tooling | Every function drags an S3 SDK (boto3 / `@aws-sdk`) + the keypair/prefix-owner model into handler code; asymmetric with kv/catalog; the motivating friction this ADR removes |
| Reuse the **legacy `services/blob.NewFacade`** (ADR-0021) unchanged | It exists and copies kv's shape | It authorizes `Kind:Service` with **no per-object `Action`** — an RBAC-role question the cedar driver rejects (`Action==""` ⇒ Internal), and it cannot enforce `spec.blob` bind-as-grant. Zero production callers. **Rejected** — it is the wrong PEP; superseded by the new binding-gated facade. |
| A **separate blob UDS** per sandbox | Isolation | A second socket to provision/mount for no benefit — the worker-node local API already is the per-sandbox, identity-bound channel (same reasoning ADR-0069 used) |
| **A binding-gated blob facade (mirroring `kv.Facade`) behind blob verbs on the worker-node local API** ✅ | Reuses transport + identity + the `S3Capability` PDP + the `s3BucketFor` substrate + `blobKey` | One socket, one identity model, the PDP + substrate unchanged, objects **shared** with the S3 frontend — the exact ADR-0069 decision, applied to blob via the already-Function-aware `S3Capability`. **Chosen.** |
| `put` carries a **content-type** argument (as first sketched) | S3 objects have a content-type | The substrate `blob.Bucket.Put(ctx, key, data)` carries none, and threading one through would change the ADR-0007/0080 `Bucket` surface — out of altitude. v1 `put` is bytes-only (mirrors `kv.put`); content-type joins **streaming in v2**. Recorded so the shim signature is not later mistaken for an omission. |
| A single `?sign=1` **query on GET** vs a distinct `/sign` route | Fewer routes vs explicit verb | `?sign=1` on `GET /blob/{binding}/{key...}` is unambiguous (list has no `{key...}`; bytes-get vs url-get differ only by the flag), keeps the route table small, and reads like the S3 presign it is. **Chosen** over a separate route. |

## Decision

Expose blob to functions as **verbs on the per-sandbox worker-node local API** (ADR-0064), routed to the
ADR-0080 `services/blob.Facade` — the ADR-0069 decision, applied to blob.

1. **Local-API blob routes** — a new `internal/workernode/local/blob.go` registers, mirroring `kv.go`:
   - `GET /blob/{binding}/{key...}` → **get** bytes (missing key ⇒ 404); with `?sign=1` → **signedUrl**
     (`text/plain` body, the URL), honoring optional `?method=GET|PUT|DELETE` (default GET) + `?expiry=<dur>`
     (Go duration; default the driver's).
   - `PUT /blob/{binding}/{key...}` → **put** (body capped at `maxBlobBytes` via `http.MaxBytesReader`;
     over-cap ⇒ 413/422).
   - `DELETE /blob/{binding}/{key...}` → **delete**.
   - `GET /blob/{binding}` → **list** (`?prefix=…`; empty list is `[]`).
   The handler holds a **`Blob` port** and calls it with the **fixed caller `Ref`** as `(ns, fn)` — never read
   from the request. Errors map RFC 9457 (`Forbidden→403`, `NotFound→404`, `Invalid→422`, over-cap→413,
   `Internal→500`), identical to `registerKV`.
2. **Function-facing blob facade (the true `kv.Facade` twin)** — a new binding-gated PEP in
   `internal/services/blob`, mirroring `services/kv.Facade` method-for-method:
   `Get/Put/Delete/List/SignedURL(ctx, ns v1.NamespaceName, fn v1.ObjectName, alias, key string)`. Each call
   (a) **resolves** the alias→`Binding{Bucket, Prefix}` from the caller Function's `spec.blob` via a new
   **blob binding resolver** (default-deny — no `spec.blob` entry for the alias ⇒ `fault.Forbidden`, mirroring
   `kvsvc.BindingResolver`); (b) **authorizes** via the existing **`S3Capability`** PDP — it builds the
   `auth.Request{Identity{Principal: Function::"<ns>/<fn>"}, Action: s3::read|s3::write, Resource:
   EntityRef{Type: KindBucket, Namespace: ns, Name: bucket, Path: prefix}}`, exactly as `s3gateway.authorize`
   does but with the **Function** principal (whose `blobBindings` the capability already materializes from
   `spec.blob`) — so an unbound alias is Forbidden and the prefix-owner write rule holds, **with no PDP
   change**; (c) acts on the `s3BucketFor(ns, bucket)` substrate view under key `blobKey(prefix, key)` — the
   **same** resolver + keyspace the S3 frontend uses, so the object is the same object `aws s3` sees.
   `read` = get/list/sign-GET; `write` = put/delete/sign-PUT/DELETE. The facade builds the Function principal
   **internally** (like `kv.Facade`), so it satisfies the local `Blob` port directly — **no `pkg/funcd`
   adapter**. It **supersedes** the unused legacy `services/blob.NewFacade` (ADR-0021); the package's live
   Service-dispatcher `TypeHandler` is untouched.
3. **Facade wiring** — `pkg/funcd` builds the facade from the pieces it already has for the S3 frontend: the
   `s3BucketFor(c.blob, c.store)` bucket resolver, the blob binding resolver (reads Function `spec.blob` from
   the metastore), and the cedar PDP, then passes it into the local-API Manager/handler. A **nil** blob port ⇒
   **no `/blob` routes** (exactly like nil kv), so the change is additive.
4. **Socket provisioning** — the per-sandbox local-API socket is provisioned when a function **declares links
   OR has KV bindings OR has blob bindings** (generalized from ADR-0069's links-or-kv). One socket serves
   `/invoke/`, `/kv/`, and `/blob/`.
5. **Shim `context.blob`** — the Node + Python shims add `context.blob` dialing the local-API socket
   (`FUNCD_INVOKE_SOCKET`), mirroring `context.kv` byte-for-byte:
   - Python (`shim/python/src/funcd_shim/blob.py`): `get(binding,key) -> bytes|None`, `put(binding,key,data:
     bytes)`, `delete(binding,key)`, `list(binding,prefix="") -> list[str]`, `signed_url(binding,key,
     method="GET", expiry=None) -> str`. Stdlib-only (mirrors `kv.py`).
   - Node (`shim/nodejs/src/blob.ts`): `get -> Uint8Array|null`, `put(value: Uint8Array)`, `del`, `list`,
     `signedUrl(binding,key,opts?) -> string` (`opts.method`, `opts.expiry`). `node:http` over the UDS
     (mirrors `kv.ts`).
   Both add `blob` to the `FunctionContext` (`types.py` / `types.ts`) and construct it in the context
   (`shim.py` / `shim.ts`).

**In-memory cap.** `maxBlobBytes = 64 << 20` (64 MiB) — larger than kv's 1 MiB (blob objects are parquet/PDF,
not small values) but bounded, because v1 buffers the whole body in memory on both the shim and the local API.
The cap **is** the boundary that makes streaming the honest v2 follow-up.

## Temporary workarounds

- **Bytes-in-memory, capped** instead of streaming. **Exit criterion**: a v2 ADR adds a streaming `get`/`put`
  (chunked over the local API) + content-type/metadata, lifting the `maxBlobBytes` cap for the streamed path.

## Contracts

```go
// internal/workernode/local/blob.go — the handler gains blob routes, backed by a thin Blob port.
// Connection-scoped (ns, fn) shape, identical to the KV port; the sandbox identity is the fixed caller Ref.
type Blob interface {
    Get(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) ([]byte, bool, error)
    Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string, data []byte) error
    Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) error
    List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, prefix string) ([]string, error)
    SignedURL(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string, opts blob.SignOptions) (string, error)
}

// registerBlob adds GET/PUT/DELETE /blob/{binding}/{key...}, GET /blob/{binding} (list),
// and GET /blob/{binding}/{key...}?sign=1 (signed URL) — mirroring registerKV.
func registerBlob(mux *http.ServeMux, caller Ref, b Blob, logger *slog.Logger)

// NewHandler / NewManager gain a `blob Blob` param (nil ⇒ no /blob routes), placed after `kv`.
func NewHandler(caller Ref, res Resolver, inv Invoker, authz auth.Authorizer, kv KV, blob Blob, logger *slog.Logger) http.Handler
func NewManager(dir string, store FunctionStore, invoker Invoker, authz auth.Authorizer, kv KV, blob Blob, logger *slog.Logger) *Manager
```

```go
// internal/services/blob — the function-facing, binding-gated facade (the kv.Facade twin). Satisfies
// local.Blob directly: it builds the Function principal internally, so NO pkg/funcd adapter is needed.
type FacadeDeps struct {
    Resolver   BindingResolver   // alias → (bucket, prefix) from the caller Function's spec.blob (default-deny)
    BucketFor  func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool) // = pkg/funcd's s3BucketFor
    Authorizer auth.Authorizer   // the cedar PDP; the facade asks S3Capability s3::read / s3::write
    Logger     *slog.Logger
}
func NewFacade(d FacadeDeps) (*Facade, error)
func (f *Facade) Get(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string) ([]byte, bool, error)
func (f *Facade) Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string, data []byte) error
func (f *Facade) Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string) error
func (f *Facade) List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, prefix string) ([]string, error)
func (f *Facade) SignedURL(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string, opts blob.SignOptions) (string, error)

// BindingResolver maps a caller (function, alias) to its bound (bucket, prefix) from spec.blob (ADR-0073
// bind-as-grant: no entry ⇒ fault.Forbidden). Mirrors services/kv.BindingResolver.
type BindingResolver interface {
    Resolve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) (Binding, error)
}
type Binding struct { Bucket v1.ObjectName; Prefix string }
// authorize builds: auth.Request{Identity{Principal: &EntityRef{Type: v1.KindFunction, Namespace: ns, Name: fn}},
//   Action: auth.ActionS3Read|auth.ActionS3Write, Resource: &EntityRef{Type: v1.KindBucket, Namespace: ns,
//   Name: b.Bucket, Path: b.Prefix}} — the same shape s3gateway.authorize builds, Function principal.
// substrate key = blobKey(b.Prefix, key) — the same keyspace the S3 frontend serves.
```

```python
# shim/python/src/funcd_shim/blob.py — context.blob (stdlib-only, dials FUNCD_INVOKE_SOCKET; mirrors kv.py).
class BlobClient:
    def get(self, binding: str, key: str) -> bytes | None: ...
    def put(self, binding: str, key: str, data: bytes) -> None: ...
    def delete(self, binding: str, key: str) -> None: ...
    def list(self, binding: str, prefix: str = "") -> list[str]: ...
    def signed_url(self, binding: str, key: str, method: str = "GET", expiry: float | None = None) -> str: ...
```

```ts
// shim/nodejs/src/blob.ts — context.blob (node:http over the UDS; mirrors kv.ts).
export interface BlobClient {
  get(binding: string, key: string): Promise<Uint8Array | null>;
  put(binding: string, key: string, value: Uint8Array): Promise<void>;
  del(binding: string, key: string): Promise<void>;
  list(binding: string, prefix?: string): Promise<string[]>;
  signedUrl(binding: string, key: string, opts?: { method?: 'GET' | 'PUT' | 'DELETE'; expiry?: string }): Promise<string>;
}
```

| consumes | exposes |
|---|---|
| the worker-node local API + socket (ADR-0064), the `S3Capability` PDP (ADR-0080/0116, already Function-aware), `s3BucketFor` + `blobKey` (the s3gateway substrate + keyspace), the Function `spec.blob` bindings (ADR-0073) | the function-facing blob `Facade` + resolver; `GET/PUT/DELETE /blob/{binding}/{key...}`, list, `?sign=1` on the per-sandbox UDS; `context.blob` in both shims |
| config: none new (reuses the ADR-0080 blob backend selection) | the Facade wired into the local-API Manager; nil ⇒ no `/blob` routes |

## Implementation plan

**Files**
- `internal/services/blob/facade.go` (+ `resolver.go`) — the function-facing binding-gated `Facade`
  (`NewFacade`, `Get/Put/Delete/List/SignedURL` authorizing `S3Capability` `s3::read`/`s3::write`, keyed via
  `blobKey`) + the `BindingResolver` reading Function `spec.blob`. Supersedes the unused legacy `NewFacade`;
  leaves the live `TypeHandler` in place.
- `internal/workernode/local/blob.go` — the `Blob` port + `registerBlob` (the `/blob/...` routes,
  `maxBlobBytes`); `local.go` + `manager.go` gain the `blob Blob` param and call `registerBlob` when non-nil.
- `pkg/funcd` — construct the blob `Facade` from the existing `s3BucketFor` resolver + a Function-`spec.blob`
  binding resolver + the cedar PDP; pass it (satisfies `local.Blob` directly) into `local.NewManager`;
  generalize socket provisioning to links-or-kv-**or-blob**.
- `shim/python/src/funcd_shim/blob.py` + `shim/nodejs/src/blob.ts` — `context.blob`; add `blob` to
  `types.py`/`types.ts` + construct it in `shim.py`/`shim.ts`; regen `shim.mjs`/`pool.mjs`.
- `examples/js/blob-object/` — a minimal Node example: a function that `context.blob.put/get/list`s an object
  (the blob analogue of `examples/js/kv-counter`), exercised by the e2e.

**Test plan** (one acceptance test per scenario, grep-able names)
- Local-API handler level (Facade + memory bucket, hermetic): `TestScenarioBlobReadWrite`,
  `TestScenarioBlobList`, `TestScenarioBlobSignedURL`, `TestScenarioBlobUnboundForbidden`,
  `TestScenarioBlobSizeCap`.
- `pkg/funcd` in-process blob e2e mirroring the kv e2e (a function drives `context.blob` end-to-end).
- `TestScenarioBlobParity` — a Node handler + a Python handler both round-trip through `context.blob` (the
  cross-shim wire); the Node/Python-shim legs are `-tags e2e` / runtime-gated where a real shim is needed,
  the state/authz legs stay hermetic.

**Definition of done**: the four Go sub-checks green for **both** tags (`go build ./...` · `go tool
golangci-lint run ./...` · `go test ./...` · `go mod verify`), plus `build-shim` / `check-shim-python`; every
scenario has a named, passing (un-skipped where hermetic) test; identity is connection-scoped (a request
cannot name a caller); no new dep; the example runs in the e2e; blueprint synced if it refined the data-plane
picture.

## Review checklist

- [ ] `GET/PUT/DELETE /blob/{binding}/{key...}`, `GET /blob/{binding}` (list), `GET …?sign=1` (signedUrl) on
      the worker-node local API; RFC 9457 errors identical to `registerKV`.
- [ ] Identity is the fixed caller `Ref` (`ns, fn`) — never read from the request body/query/header.
- [ ] Bind-as-grant preserved: an unbound alias ⇒ 403 — the resolver default-denies (no `spec.blob` entry)
      and/or the `S3Capability` PDP denies; default-deny intact.
- [ ] The facade authorizes `s3::read`/`s3::write` on a `KindBucket`+`Path:prefix` resource with a **Function**
      principal, and keys objects via `blobKey(prefix, key)` (same as the S3 frontend — objects coexist).
- [ ] `maxBlobBytes` enforced via `http.MaxBytesReader` on `put`; over-cap ⇒ 413/422.
- [ ] `pkg/funcd` constructs the blob `Facade` from `s3BucketFor` + the `spec.blob` resolver + the PDP and
      wires it (satisfies `local.Blob` directly, no adapter); nil ⇒ no `/blob` routes (additive).
- [ ] Socket provisioned for links **or** kv **or** blob bindings; one socket serves all three verb namespaces.
- [ ] `context.blob` in Node + Python shims, typed on `FunctionContext`; regenerated bundles; stdlib/`node:http`
      only (no S3 SDK in the shim).
- [ ] `examples/js/blob-object` exercised by the e2e; parity test round-trips Node↔Python.
- [ ] The `S3Capability` PDP + s3gateway are unchanged; the legacy Service `TypeHandler` is unchanged; no new
      dep; ctx-first; `api/fault`.

## Consequences

**Positive**: functions get a first-class blob accessor symmetric with `context.kv`/`context.catalog` — no
boto3, no keypair, no prefix-owner reasoning in handler code; because the facade reuses the `S3Capability` PDP
+ `s3BucketFor` + `blobKey`, objects written via `context.blob` are the **same** objects the ADR-0080 S3
frontend serves (`aws s3 ls` sees them; a signed URL is fetchable) — the two paths genuinely coexist over one
substrate; the releve-lakehouse example (and any lakehouse function) drops its hand-rolled `s3util`. Reuses the
ADR-0064 transport + identity wholesale (no new transport/identity/dep).
**Negative (accepted)**: the worker-node local API grows a third verb namespace (more surface to keep
consistent across shims); v1 is bytes-in-memory-capped at `maxBlobBytes` (64 MiB) — below the s3gateway's
`maxUploadBytes` (1 GiB) — so a larger object needs the ADR-0080 S3 frontend until the v2 streaming ADR lands;
the new binding-gated facade supersedes the unused legacy `services/blob.NewFacade` (dead code removed/replaced,
its tests migrated).
**Neutral**: the ADR-0080 S3 frontend + the `S3Capability` PDP are unchanged and remain the external/inspect
path; the legacy Service-dispatcher `TypeHandler` is unchanged.

## Open questions

- **Streaming + content-type/metadata** — the named v2 follow-up; lifts the in-memory cap for the streamed path.
- **Per-binding quotas / rate** — V2 (with `Grant`/quota policy), same as kv.
- **`get` typed decoders** (`get_json`/`getText`-style, as ADR-0070 added for kv) — a cheap later refinement if
  authors want them; out of scope here.

## References

- ADR-0069 (context.kv — the pattern), ADR-0091 (context.catalog), ADR-0080/0085 (the S3 frontend + keypair
  this complements/removes-for-in-function-use), ADR-0073/0076/0116 (blob bindings + bind-as-grant PDP + the
  capability framework), ADR-0064 (the per-sandbox worker-node local API).
