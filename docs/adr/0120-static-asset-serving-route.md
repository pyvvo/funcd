# ADR-0120: Static-asset serving — a `Bucket`-prefix backend on the `Route` (F82)

- **Status**: Implemented
- **Date**: 2026-07-10
- **Accepted**: 2026-07-10
- **Reviewing**: 2026-07-10
- **Implemented**: 2026-07-10
- **Acceptance note**: judge changes-requested folded (1 Blocker + 3 Majors + minors) — **B1** the path-traversal defense is owned by the handler (`path.Clean` + reject `..`/absolute + assert `HasPrefix(Prefix)` before any read; `traversal-rejected` scenario added); **M1** the validator is `(ModTime,Size)` (real `ModTime` into `http.ServeContent`, weak ETag), mirroring sibling ADR-0119, not a per-request `sha256` of the full body (content-hash demoted to an optional follow-on; shared `blob.Attributes` digest follow-up noted); **M2** `public: true` may relax an unset/default stance but NEVER overrides an explicit `authenticated` Route/namespace stance (rejected at admission); **M3** a three-tier cache policy (no-cache for index + stable names like `sw.js`/`manifest`/`favicon`; `immutable` only for hash-shaped filenames; else `max-age=300, must-revalidate`) — no blanket immutable. Minors: authenticated-static authorizes at namespace scope, the `405` is handler-owned, the F76 `function=<bucket>` label overload noted.
- **Deciders**: green-0-rabbit
- **Tags**: edge, ingress, route, static, blob, bucket, spa, data-platform
- **Realizes**: [FEAT-0003/F82](../feat/0003-feat-data-platform.md) — static-asset serving (`Bucket` prefix → `Route`): serve a prebuilt static site (the Observable BI bundle, API docs, any SPA, images, arbitrary asset bytes) as a first-class edge capability, so the consumption surface needs no hand-rolled file-server Function.
- **Relates to**: [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (F79 — the `Route`/`RouteBackend` + reconciler + edge `Router` this extends), [ADR-0113](0113-edge-authn-pep.md) (F77 — the edge authn stance `public` opts out of), [ADR-0111](0111-tls-termination.md) (F74 — the TLS the static site is served over), [ADR-0114](0114-edge-observability-shaping.md) (F76/F78 — the gzip/CORS/headers that already wrap the response), [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (the `Bucket` domain + per-namespace prefixing this reads), [ADR-0007](0007-blob-storage-layer-port.md) (the `blob.Bucket` port `Get`/`List` the handler reads).

## Context & Need

FEAT-0003 lands a lakehouse on funcd, and its consumption surface is a **prebuilt static site** — the
Observable BI bundle — plus API docs, arbitrary SPAs, images, and asset bytes. Today the only way to
serve those bytes over the edge is to **write a Function whose handler is a file-server**: it reads the
bytes from the blob substrate and streams them back with hand-rolled content-type / caching / index /
Range logic. That is boilerplate no deployment should own — every static site re-implements the same
HTTP file-server, and each one is a cold-startable sandbox in front of what is really a byte read. The
feat doc's current stance even hard-codes it (the FEAT-0003 diagram literally says *"BI served by a
function"*). Meanwhile the bytes already live in a governed `Bucket` prefix (ADR-0080), and the FEAT-0006
edge (TLS, authn PEP, gzip/CORS) already fronts every `Route` — everything the static site needs to be
served **is already there except the last mile**: a `Route` arm that reads a `Bucket` prefix directly.

**Purpose:** add a **`static` backend arm to the `Route` CRD**. A `Route` whose backend is a `Bucket`
prefix serves that prefix's objects directly through the edge — index resolution, correct content-types,
ETag + conditional GET, HTTP Range, an optional SPA fallback, and a pinned `Cache-Control` policy — with
**zero function code** and no per-site handler to maintain. It reuses the ADR-0110 `Route` matcher and
reconciler, the ADR-0080 per-namespace `Bucket` view, and the ADR-0113 auth stance (with an explicit
`public: true` opt-out). Callers: whoever `funcdctl apply`s a static `Route`; every public GET/HEAD that
matches it. This **refines** FEAT-0003's *"BI served by a function"* into a primitive.

Two seams shape the decision:
- **Where it plugs in.** The edge `Router` (ADR-0110) resolves a request to a target; today that target
  is always a `(namespace, function)` and `dataplane` runs the activator hop. A static match resolves to
  a `(bucket, prefix)` instead, and `dataplane` runs a **static handler** rather than the activator — the
  activator/scale-to-zero path is untouched, there simply is no function to wake.
- **What it reads.** The handler resolves the same **per-namespace `Bucket` view** the S3 frontend uses
  (`blob.Prefixed(shared, "s3/<ns>/<bucket>/")`, ADR-0080) so a static `Route` serves the *same bytes* a
  function or an S3 client wrote — one substrate, now a third read surface.

## Scenarios

Each becomes a named acceptance test.

- `serves-index-and-asset` — Given a static `Route` over a `Bucket` prefix containing `index.html` and
  `img/logo.png`, When `GET /` arrives, Then `index.html` is returned (`200`, `text/html`); When
  `GET /img/logo.png` arrives, Then the object is returned with `Content-Type: image/png` and an `ETag`.
- `etag-conditional-304` — Given a prior response's `ETag`, When the client repeats the request with
  `If-None-Match: <etag>`, Then it gets `304 Not Modified` with an empty body.
- `range-request` — Given a large object, When the request carries `Range: bytes=0-99`, Then the
  response is `206 Partial Content` with `Content-Range` and the requested 100 bytes (proves
  large-file / media serving).
- `spa-fallback` — Given `spa: true`, When a request targets a path with **no** matching object, Then the
  `Index` document is returned (`200`) so client-side routing works; Given `spa: false`, When the same
  path misses, Then `404`.
- `public-route-skips-authn` — Given `static.public: true`, When an anonymous request arrives, Then it is
  served with no bearer required (the authn PEP is short-circuited); Given `public: false` in an
  `authenticated` namespace, When an anonymous request arrives, Then it is `401` (the PEP enforces).
- `public-vs-explicit-authenticated-conflict` — Given a Route (or namespace) with an **explicit**
  `spec.auth.mode: authenticated` **and** `static.public: true`, Then admission **rejects** it
  (`fault.Invalid`); if such a Route is ever admitted, an anonymous request stays `401` (resolves to
  `authenticated` — `public: true` does **not** silently open it).
- `traversal-rejected` — Given a static `Route`, When a request carries a traversal path
  (`GET /..%2f..%2fsecret`, `GET /../../x`), Then it is rejected `404`/`400` and **never** escapes the
  `Prefix` (no blob `Get` outside the prefix/bucket/namespace is issued).
- `method-not-allowed` — Given a static `Route` (no method restriction compiled), When a `POST` arrives at
  a matching path, Then the **static handler** returns `405` (not `404`) — the handler, not the router,
  owns method rejection.
- `bucket-not-found-not-ready` — Given a static `Route` whose `static.bucket` names no `Bucket` in the
  Route's namespace, Then the `Route` is `NotReady` (`BucketNotFound`) and is not programmed.
- `route-validate-backend-exactly-one` — Given a `RouteRule` whose backend sets **both** `function` and
  `static`, or **neither**, Then `Validate` rejects it (`Invalid`); Given exactly one arm set, Then it
  passes.

## Scope

**In:**
- **The `static` backend arm** — `RouteBackend` becomes **exactly-one-of** `function` (existing, now
  optional) | `static` (new `*StaticBackend{Bucket, Prefix, Index, SPA, Public}`); `Validate` enforces
  the one-of.
- **The edge static handler** (`internal/edge/static`) — an `http.Handler`-shaped component that cleans and
  **containment-checks** the `<prefix><url-remainder>` path (traversal-rejecting), resolves it to a
  `Bucket` object, serves it via `http.ServeContent` (weak `(ModTime,Size)` validator / `Last-Modified` /
  Range / `If-None-Match`), does index resolution and the optional SPA fallback, sets `Content-Type` and
  the three-tier `Cache-Control`, and itself rejects non-GET/HEAD with `405`.
- **Router + dataplane integration** — the compiled `Router` entry carries the resolved `*StaticBackend`;
  `dataplane` serves a static match through the static handler (no activator hop) and applies the ADR-0113
  stance (per the §4 `public`-vs-`authenticated` precedence) **before** reading any byte.
- **Reconcile/status** — a static `Route` is `Ready` when its `Bucket` exists (a `BucketNotFound`
  NotReady reason, mirroring `BackendNotFound`); no backend Function is required or looked up.

**Out (named follow-ons):**
- **Per-object / per-glob cache overrides, configurable `max-age`, custom headers per path** — V1 pins one
  `Cache-Control` policy (below). A per-`Route` cache/header override rides the same V2 increment as
  F74–F78's per-route policy fields.
- **Directory auto-index / listing pages** — a miss is `404` (or the SPA `Index`); funcd never renders a
  directory listing.
- **Server-side rendering, on-the-fly transforms, the Observable *build* step** — funcd serves the
  **prebuilt** bundle (FEAT-0003 out-of-scope); a build/data-loader step is not funcd's job.
- **Writing assets through the `Route`** — a static `Route` is read-only (GET/HEAD); asset upload is the
  ADR-0080 S3 write path / a Function that owns the prefix.
- **A strong digest-sourced ETag** — V1 derives a **weak** validator from `blob.Attributes` `(ModTime,
  Size)` (mirroring sibling ADR-0119); a strong ETag from an extended `blob.Attributes` content digest is a
  follow-on (Open questions), and a per-request `sha256` strong ETag is an optional interim.

## Constraints & Decision drivers

- **Reuse the `Route` keystone, don't fork.** ADR-0110 froze `RouteSpec`/`RouteRule` as extensible; F82
  adds one **new backend arm** (an additive union member) + the reconciler/handler wiring — it does not
  edit the matcher or the exposure model.
- **One substrate, a third read surface.** The static handler reads the **same** per-namespace `Bucket`
  view as the S3 frontend (ADR-0080) so functions, S3 clients, and static `Route`s all see the same
  bytes. No second copy, no separate store.
- **The FEAT-0006 edge already applies.** TLS (F74), the authn PEP (F77), gzip/CORS/headers (F76/F78) wrap
  every `Route` request before it reaches the target — a static `Route` inherits them for free; this ADR
  adds **no** edge middleware.
- **Safe-by-default auth, explicit public.** A static `Route` runs the namespace authn stance like any
  `Route` (ADR-0113 default-deny-capable); serving it openly is a **declared** `public: true`, never the
  silent default.
- **Correct HTTP semantics from the stdlib.** ETag/`If-None-Match`/Range/`304`/`206` are delegated to
  `http.ServeContent` (battle-tested) rather than hand-rolled — the whole point of the primitive.
- **Two-tier validation** (the established split) — huma tags carry field shape; `Validate()` enforces
  the backend one-of and the static-field rules; the *bucket exists* check is the reconciler's (it reads
  cross-resource state), mirroring how `BackendNotFound` is a reconciler reason, not a `Validate` error.

## Alternatives considered

| Option | Verdict |
|---|---|
| **A `static` backend arm on `Route` reading a `Bucket` prefix via the blob port + `http.ServeContent` (chosen).** | Declarative, zero function code, reuses the `Route` matcher / the `Bucket` view / the FEAT-0006 edge / the stdlib file-server semantics; the bytes stay the one governed substrate. |
| **Keep the hand-rolled file-server Function** (the F82 status quo). | Rejected — this is exactly what F82 exists to remove: every static site re-implements content-type/caching/index/Range, and each is a cold-startable sandbox in front of a byte read. Retained only as the migration source. |
| A **separate `StaticSite` kind** (its own CRD + reconciler). | Rejected — a second exposure kind duplicates the `Route` host/path/method matching, the conflict resolution, and the edge-policy attach points ADR-0110 already owns. A backend arm reuses all of it. |
| **Redirect / presigned-URL** the client to the blob/S3 backend (302 to a signed URL). | Rejected — leaks the object-store endpoint, bypasses the funcd edge (TLS/authn/observability), needs a signer the memory/file backends lack (ADR-0007), and breaks SPA/index semantics. The edge must serve the bytes. |
| A **generic reverse-proxy** backend arm (proxy to an arbitrary upstream URL). | Rejected for this ADR — a different altitude (arbitrary upstream, not the governed `Bucket`); it neither reuses the substrate nor the tenancy guard. A proxy arm is a separate future decision. |
| **Drop `Public`; require `spec.auth.mode: open`** to serve a static site openly (no `public` flag). | Rejected — a co-located, self-documenting `public: true` on the static backend reads clearer at the site definition than a separate stance field. But only *with* the §4 guardrail: `public` may relax an unset default, never override an explicit `authenticated` (else it becomes a silent-open footgun). |

## Decision

1. **`RouteBackend` becomes an exactly-one-of union.** `Function` becomes **optional**; a new
   `Static *StaticBackend{Bucket ObjectName; Prefix, Index string; SPA, Public bool}` is added. Each
   `RouteRule.backend` sets **exactly one** arm. `Route.Validate` is extended: for each rule, exactly one
   of `function`/`static` is set (neither / both ⇒ `Invalid`); a `function` arm keeps today's DNS-1123
   check; a `static` arm requires a non-empty `bucket` (DNS-1123) and, if `prefix`/`index` are set,
   well-formed values (`index` a relative path, no leading `/`, default `index.html`). A static rule is
   compiled with **no method restriction at the router** (the router filters by method *before* the
   handler, so a `{GET, HEAD}`-compiled rule would `404` — not `405` — a `POST`; instead the static
   handler itself owns method handling: `GET`/`HEAD` serve, any other method returns `405`).
2. **The edge static handler** (`internal/edge/static`) serves a resolved static target. Given the
   Route's namespace, the `*StaticBackend`, and the function-relative remainder (the matched prefix
   already stripped, ADR-0110 §6.2), it:
   - resolves the **per-namespace `Bucket` view** via the injected resolver
     (`blob.Prefixed(shared, "s3/<ns>/<bucket>/")`, the same `s3BucketFor` the S3 frontend uses); a
     missing / cross-namespace `Bucket` ⇒ `404` (tenancy guard);
   - **owns the traversal defense (does not inherit it from the backend).** It URL-decodes the remainder,
     `path.Clean`s it, and **rejects** any request whose cleaned path is absolute or still contains a `..`
     segment (a decoded `/..%2f..%2fsecret`, `/../../x`) — `400` (malformed) / `404` (no-escape), never a
     `Get`. It then computes the object **key** = `Prefix + trimLeadingSlash(cleaned)` and **asserts
     `strings.HasPrefix(key, Prefix)` before any blob `Get`** — the prefix containment is a *stated
     contract of this handler*, not a property inherited from the blob driver. If the remainder is `/` or
     ends in `/` (a "directory"), it appends `Index`;
   - looks up the object's `blob.Attributes` (`{Key, Size, ModTime}`, ADR-0007) and `Get`s the bytes; on
     `fault.NotFound` it serves `Index` (`200`) when `SPA`, else `404` (RFC 9457 problem+json);
   - sets `Content-Type` from the key's extension (`mime.TypeByExtension`, falling back to
     `http.DetectContentType`), the pinned `Cache-Control` (§3), and derives the **validator from
     `(ModTime, Size)`** — a weak **`ETag`** `W/"<size>-<modtime-unix>"` — then calls
     `http.ServeContent(w, r, key, attrs.ModTime, bytes.NewReader(data))`, passing the **real `ModTime`**
     so the stdlib emits `Last-Modified` and honors `If-Modified-Since` / `If-Range` for free (in addition
     to `If-None-Match` ⇒ `304` and `Range` ⇒ `206 Partial Content`). This mirrors sibling **ADR-0119**
     (F83), which made the same `(ModTime, Size)` fingerprint choice against the identical ADR-0007 port;
     both share the follow-up "expose a content digest on `blob.Attributes` (ADR-0007)" for a future
     *strong* ETag. A per-request `sha256(content)` strong ETag is an **optional follow-on**, not the V1
     default. (The full in-memory `Get` remains a V1 simplification because the base `Bucket` port is
     `Get`-only — but that is *separate* from the validator choice: `blob.RangeReader` already exists
     (ADR-0080), so a ranged read is a simplification we skip in V1, not something the ETag forces.)
   - answers only `GET`/`HEAD`; any other method ⇒ `405 Method Not Allowed`.
3. **Pinned `Cache-Control` policy (V1, fixed) — three tiers, not a binary split.** A blanket
   `immutable` on everything-but-the-index is unsafe: an `immutable` service-worker or `favicon` would pin
   a stale asset in browser caches for a **year** with no revalidation escape. So V1 classifies by name:
   - **Always-revalidated (`no-cache`)** — the **`Index` document** (served for `/`, a directory, or an
     SPA fallback) **and** a known-stable set that keeps its filename across deploys: `sw.js` /
     service-worker, `manifest*`, `favicon*`, `robots.txt`, `.well-known/*`. These are always revalidated
     against their validator (a `304` when unchanged keeps it cheap), so a new deploy is picked up at once.
   - **Immutable (`public, max-age=31536000, immutable`)** — only a **content-hash-shaped filename**, by
     heuristic: a hash-like segment matching `\.[0-9a-f]{8,}\.` or `-[0-9a-f]{8,}\.` (the fingerprints
     Observable/Vite/webpack emit). Safe to cache forever precisely because a content change mints a new
     name.
   - **Everything else — conservative (`public, max-age=300, must-revalidate`)** — a non-hashed, non-stable
     asset gets a short TTL that must revalidate, **never** blanket `immutable`.

   Not configurable in V1; a per-`Route` cache override is the V2 exit for finer control.
4. **Auth: inherit by default, explicit `public: true` opts out — but never overrides an explicit
   `authenticated`.** `static.public: true` and `spec.auth.mode` both decide the *same* thing ("does the
   PEP run"), so their precedence is pinned. The resolved stance is computed in this **order**:
   1. an **explicit `authenticated`** — on the Route's `spec.auth.mode` *or* the namespace
      `edgeDefaults.auth.mode` — **wins and `public: true` MUST NOT override it**. An explicit
      `authenticated` combined with `public: true` is a contradiction: it is **rejected at admission**
      (`fault.Invalid` — preferred, so the operator sees the mistake) and, if ever admitted, resolves to
      **`authenticated`** (public loses — a site is never silently opened);
   2. otherwise (stance **unset / at the phased default**), `public: true` **relaxes** it to **`open`** —
      a declared public site (public BI / public assets) served with no bearer;
   3. otherwise the normal stance resolution applies (Route `spec.auth.mode`, else namespace default, else
      `open` phased).

   The PEP (when wired) is invoked with the resolved stance **before any byte is read** (reject-before-read
   — no `401`/`403` after leaking object existence, mirroring ADR-0113's reject-before-wake/no-enumeration
   posture). An **authenticated (non-public) static route authorizes at *namespace* scope**, reusing
   ADR-0113's coarse RBAC: since a static route has no function, the enforcer is passed a
   `FunctionRef{Namespace: ns}` (the `Name` is ignored by the namespace-scoped RBAC check). A
   *bucket-scoped* authz target is a follow-on. `public` stays a self-documenting, opt-in declaration; the
   default stays default-deny-capable.
5. **Router + dataplane integration.** The compiled `Router` rule carries the resolved `*StaticBackend`
   (nil for a function backend). `Match` gains `Static *v1.StaticBackend`. In `dataplane.ServeHTTP`, a hit
   with `m.Static != nil` resolves the stance (per §4's precedence), runs the enforcer (if any), fills the
   F76 observ `Target` holder with `(ns, bucket)` for the access-log/metric label, and dispatches to the
   **static handler** with the stripped remainder — **no activator hop** (there is no function to wake;
   scale-to-zero is untouched because it is simply not on this path). The F76 `observ.Target{Namespace,
   Function}` has only a `Function` field, so a static route emits `function="<bucket>"` — an **intentional
   V1 label overload** (the target still identifies uniquely within the namespace); adding a
   `backend`/`kind` discriminator label to `observ.Target` is a named follow-on.
6. **Reconcile/status.** The Route reconciler's backend check is extended: for a `function` arm it keeps
   the existing existence check (`BackendNotFound`); for a `static` arm it checks the **`Bucket` exists**
   in the Route's namespace (`store.Get(KindBucket, ns, bucket)`) — a miss ⇒ `NotReady` reason
   **`BucketNotFound`** (mirroring `BackendNotFound`), and the route is not programmed. A Ready static
   `Route` is compiled into the `Router` like any other. The host-required-in-`explicit` and
   `(host,path,method)` conflict rules (ADR-0110) apply unchanged.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **`(ModTime, Size)` validator (weak ETag `W/"<size>-<modtime-unix>"` + real `Last-Modified`)** | `blob.Attributes` (ADR-0007) exposes `{Key, Size, ModTime}`, no per-object digest — same as sibling **ADR-0119** (F83), which chose the identical `(ModTime, Size)` fingerprint against this port | when `blob.Attributes` carries a content digest (an S3 `ETag`/MD5) — the shared ADR-0007 follow-up ADR-0119 also awaits — source a **strong** ETag from it; a per-request `sha256(content)` strong ETag is the optional interim, not the V1 default. No contract change here |
| **Full in-memory `Get` (whole object read into a `bytes.Reader`)** | the base `blob.Bucket` port is `Get`-only; `http.ServeContent` needs an `io.ReadSeeker` | `blob.RangeReader` already exists (ADR-0080) — a ranged read that avoids the full buffer is a perf simplification we skip in V1, *independent* of the validator choice |
| **One fixed (three-tier) `Cache-Control` policy** | V1 needs a correct, safe default, not a config surface | the V2 per-`Route` edge-policy increment (with F74–F78's per-route fields) adds a cache/header override |

## Contracts

### Resource (`api/types/v1alpha1/route.go`)

```go
// RouteBackend is an exactly-one-of union: a Function backend (activator hop) OR a Static backend
// (a Bucket prefix served directly, F82). Validate enforces the one-of.
type RouteBackend struct {
	// NB: the tag changes `json:"function"` → `json:"function,omitempty"` (F82) — Function is now optional
	// because a static-only rule sets no function.
	Function ObjectName     `json:"function,omitempty"` // the target Function in this Route's namespace
	Static   *StaticBackend `json:"static,omitempty"`   // a Bucket-prefix static site (F82)
}

// StaticBackend serves a Bucket prefix as a static site (F82): index resolution, content-types,
// ETag + conditional GET, Range, an optional SPA fallback, served through the FEAT-0006 edge.
type StaticBackend struct {
	// Bucket is the Bucket in THIS Route's namespace whose objects are served (binding-as-grant:
	// the Route reads only this declared Bucket; the reconciler validates it exists → BucketNotFound).
	Bucket ObjectName `json:"bucket"`
	// Prefix is the key prefix within the Bucket that roots the site (e.g. "bi/"); "" ⇒ the bucket root.
	Prefix string `json:"prefix,omitempty"`
	// Index is the document served for "/" / a directory / (when SPA) a miss. Default "index.html".
	Index string `json:"index,omitempty"`
	// SPA, when true, serves Index (200) for any un-matched path so a client-side router owns routing;
	// when false, an un-matched path is 404.
	SPA bool `json:"spa,omitempty"`
	// Public, when true, serves this Route openly (stance forced to `open`, ADR-0113) regardless of the
	// namespace default — a declared public site. Default false ⇒ inherit the namespace/Route auth stance.
	Public bool `json:"public,omitempty"`
}

// Validate (extended): the current UNCONDITIONAL `dnsLabel.MatchString(Backend.Function)` check is
// REPLACED by the exactly-one-of union check — for each rule, EXACTLY ONE of backend.function /
// backend.static is set (neither or both ⇒ Invalid). A function arm keeps the DNS-1123 label check
// (now conditional on the function arm being the one set); a static arm requires a non-empty DNS-1123
// `bucket` and an `index` that is a relative path (no leading '/'; default index.html applied at
// compile). Static rules carry no method restriction (the handler owns GET/HEAD → else 405). The
// `bucket exists` check is the RECONCILER's (BucketNotFound → NotReady), not here — cross-resource state.
func (r *Route) Validate() error
```

Example `Route` YAML — one public BI site and one authenticated docs site:

```yaml
apiVersion: funcd.dev/v1alpha1
kind: Route
metadata: { name: bi, namespace: analytics }
spec:
  host: bi.example.com
  rules:
    - path: /
      pathType: Prefix
      backend:
        static:
          bucket: reports        # a Bucket in namespace "analytics"
          prefix: bi/            # site root within the bucket
          index: index.html
          spa: true              # Observable/SPA client-side routing
          public: true           # public BI site — served openly (stance forced `open`)
---
apiVersion: funcd.dev/v1alpha1
kind: Route
metadata: { name: docs, namespace: analytics }
spec:
  host: docs.example.com
  rules:
    - path: /
      pathType: Prefix
      backend:
        static:
          bucket: reports
          prefix: apidocs/
          # public omitted ⇒ inherits the namespace authn stance (authenticated ⇒ bearer required)
```

### Edge static handler (`internal/edge/static`)

```go
// BucketResolver resolves a (namespace, bucket) to its per-namespace blob view (the same
// s3BucketFor the S3 frontend uses, ADR-0080). ok=false ⇒ the Bucket is missing / cross-namespace.
type BucketResolver func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool)

type Deps struct {
	Buckets BucketResolver
	Logger  *slog.Logger
}

// Handler serves a Bucket-prefix static site over the edge (F82). It resolves it, it does not proxy —
// there is no function to wake.
type Handler struct { /* buckets, logger */ }

func New(d Deps) *Handler

// Serve writes the object at <back.Prefix><cleaned-remainder> in back.Bucket. It path.Cleans the decoded
// remainder and REJECTS an absolute or `..`-containing path (400/404) before any Get, then asserts
// strings.HasPrefix(key, back.Prefix) (containment is this handler's contract, not the driver's). It sets
// content-type, the weak validator ETag W/"<size>-<modtime-unix>" derived from blob.Attributes
// (ModTime,Size), the pinned Cache-Control, and calls http.ServeContent with the real ModTime — so
// Last-Modified / If-Modified-Since / If-Range and If-None-Match (→304) / Range (→206) all work. "/" or a
// directory → back.Index; a miss → back.Index (200) when back.SPA else 404; non-GET/HEAD → 405.
// remainder is the function-relative path (matched Route prefix already stripped), always starting "/".
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, ns v1.NamespaceName, back *v1.StaticBackend, remainder string)
```

### Edge router (`internal/edge/router`) — additive

```go
// CompiledRule gains an optional Static backend (nil ⇒ a Function backend, Function is then set).
type CompiledRule struct {
	Path     string
	Exact    bool
	Methods  map[string]bool
	Function v1.ObjectName      // set for a function backend
	Static   *v1.StaticBackend  // non-nil ⇒ a static backend (F82); Function unused
}

// Match gains the resolved Static backend (nil for a function match).
type Match struct {
	Namespace   v1.NamespaceName
	Function    v1.ObjectName
	StripPrefix string
	Auth        v1.AuthMode
	Static      *v1.StaticBackend // non-nil ⇒ serve via internal/edge/static (no activator hop)
}
```

### Dependencies & I/O

| Consumes | Produces |
|---|---|
| the ADR-0110 `Route`/`Router`/reconciler + `dataplane` serving path · the ADR-0080 per-namespace `Bucket` view (`s3BucketFor` → `blob.Prefixed`) · the `blob.Bucket` port (`Get`; `List` for a directory probe) · the ADR-0113 stance (forced `open` when `public`) · `http.ServeContent` / `mime` (stdlib) | a `static` `RouteBackend` arm · an `internal/edge/static` handler serving ETag/Range/index/SPA with a pinned `Cache-Control` · a `BucketNotFound` NotReady reason for a static `Route` · a programmed static route on the edge `Router` |

## Implementation plan

**Files:**
- `api/types/v1alpha1/route.go` — `StaticBackend`; `RouteBackend.Function` → optional + `Static`; extend
  `Validate` (backend one-of; static-field rules); huma tags/`Schema()` for the new fields.
- `internal/edge/static/static.go` (+ test) — the `Handler` (`Serve`): key resolution, index, SPA
  fallback, content-type, ETag, `Cache-Control`, `http.ServeContent`, `405`.
- `internal/edge/router/router.go` — `CompiledRule.Static` + `Match.Static`; `Program`/`Resolve` carry it.
- `internal/route/reconcile.go` — extend the backend check: a `static` arm validates the `Bucket` exists
  (`BucketNotFound`); `compile` emits the `*StaticBackend` into the entry.
- `internal/dataplane/dataplane.go` — on a `m.Static != nil` hit, resolve the stance (forced `open` when
  `Public`), run the enforcer before reading, fill the observ `Target`, dispatch to the static handler
  (no activator hop).
- `pkg/funcd/funcd.go` — build the static `Handler` from the existing `s3BucketFor` resolver + logger and
  pass it into `dataplane.Handler`; regenerate OpenAPI.

**Deps:** none (all stdlib — `net/http`, `mime`, `crypto/sha256`, `bytes` — + existing seams).

**Test plan** — one named test per Scenario:
- `internal/edge/static` unit over a memory `blob.Bucket`: `serves-index-and-asset`,
  `etag-conditional-304`, `range-request`, `spa-fallback` (both SPA branches), `traversal-rejected`
  (`/..%2f..%2fsecret`, `/../../x` ⇒ no escape), `method-not-allowed` (`POST` ⇒ handler `405`), plus
  content-type + the **three-tier** `Cache-Control` assertions (`no-cache` for the index *and* a
  `favicon`/`sw.js`; `immutable` for a hash-shaped `app.a1b2c3d4.js`; `max-age=300, must-revalidate` for a
  plain `data.json`).
- `api/types/v1alpha1` validate matrix: `route-validate-backend-exactly-one` (neither / both / each arm),
  `public-vs-explicit-authenticated-conflict` (explicit `authenticated` + `public:true` ⇒ `Invalid`),
  static-field rules (bucket required, index relative).
- `internal/route` reconciler unit over a memory store: `bucket-not-found-not-ready` (missing `Bucket` ⇒
  `NotReady` `BucketNotFound`; present ⇒ `Ready` + programmed).
- **Go in-process e2e** over `pkg/funcd` (real `funcd.New`, memory store + memory blob): apply a `Bucket`,
  `Put` `index.html` + an asset, apply a static `Route` (one `public: true`, one inheriting an
  `authenticated` namespace) → `public-route-skips-authn` (public served anonymously; the authenticated
  one `401` without a bearer, served with one), `serves-index-and-asset`, `range-request`.
- **Venom containerd lane** (extend an example lane): `Put` a small site into a `Bucket`, apply a public
  static `Route` with a host, `curl -H "Host: …" /` → the index HTML; `curl` an asset → correct
  content-type + `ETag`; a repeat with `If-None-Match` → `304`; a `Range` header → `206`; an SPA unknown
  path → the index.

**Definition of done:** all scenario tests green; `go build/test/lint/mod` green; the in-process e2e + the
Venom lane green; OpenAPI regenerated; F82 row `→ reviewing`; the FEAT-0003 diagram note (*"BI served by a
function"*) reconciled to the static primitive; no identity/path leak.

## Review checklist

- [ ] `RouteBackend`/`StaticBackend` match the Contracts; `Validate` enforces backend **exactly-one-of**
      and the static-field rules; no `any` in exported signatures; huma tags carry field shape.
- [ ] The static handler serves via `http.ServeContent`: the weak validator `ETag`
      `W/"<size>-<modtime-unix>"` from `blob.Attributes` `(ModTime, Size)` (no per-request `sha256`), the
      **real `ModTime`** passed so `Last-Modified` / `If-Modified-Since` / `If-Range` work, `If-None-Match`
      ⇒ `304`, `Range` ⇒ `206`; `Content-Type` from the key extension.
- [ ] **Traversal:** the handler `path.Clean`s the decoded remainder, rejects an absolute / `..` path
      (`400`/`404`) and asserts `strings.HasPrefix(key, Prefix)` **before** any `Get` — no escape past the
      prefix/bucket/namespace.
- [ ] **Method:** static rules carry no router method restriction; the **handler** returns `405` for
      non-GET/HEAD (a `POST` gets `405`, not `404`).
- [ ] Index resolution: `/` / a directory / (SPA) a miss ⇒ `Index` (`200`); a non-SPA miss ⇒ `404`
      (RFC 9457).
- [ ] `Cache-Control` (three tiers): `Index` + the stable set (`sw.js`/`manifest*`/`favicon*`/
      `robots.txt`/`.well-known/*`) ⇒ `no-cache`; a content-hash-shaped filename ⇒
      `public, max-age=31536000, immutable`; everything else ⇒ `public, max-age=300, must-revalidate`
      (no blanket `immutable`).
- [ ] Auth precedence (§4): an **explicit `authenticated`** + `public: true` is **rejected** at admission
      (`fault.Invalid`) / never silently opened; `public: true` only relaxes an unset default to `open`;
      `public: false` inherits the ADR-0113 stance; an authenticated static route authorizes at
      **namespace scope** (`FunctionRef{Namespace: ns}`); the PEP enforces **before** any byte is read (no
      existence oracle).
- [ ] The static handler reads the **per-namespace `Bucket` view** (`s3BucketFor`); a missing /
      cross-namespace `Bucket` ⇒ `404`; a static `Route` over a missing `Bucket` ⇒ `NotReady`
      (`BucketNotFound`), not programmed.
- [ ] A static match runs the static handler with **no activator hop**; the function path / scale-to-zero
      is unchanged; ADR-0110 host/conflict rules still apply.
- [ ] OpenAPI regenerated; F82 row advanced; the FEAT-0003 diagram note reconciled.

## Consequences

- **(+)** A prebuilt static site (BI bundle, docs, any SPA, images, arbitrary bytes) is served with **zero
  function code** — declarative, over the same TLS/authn/gzip edge, off the same governed `Bucket`
  substrate; the boilerplate file-server Function is gone.
- **(+)** Correct HTTP file-server semantics (ETag/Range/`304`/`206`/index/SPA) come from the stdlib
  `http.ServeContent`, not hand-rolled per site.
- **(+)** Safe by default: a static `Route` is default-deny-capable like any `Route`; public serving is a
  declared `public: true`, and rejects happen before any byte read.
- **(−)** V1 pins one (three-tier) `Cache-Control` policy and derives a **weak** `(ModTime, Size)`
  validator (no digest port yet — matching sibling ADR-0119); a strong content-digest ETag and a per-route
  cache override are acknowledged follow-ons, not gaps in correctness.
- **(−/risk)** `RouteBackend` becomes a union — a new shape the matcher/reconciler/handler must keep
  consistent; mitigated by the `Validate` one-of + a dedicated static handler that never touches the
  activator path.
- **(risk)** Large objects are read fully into memory to seek for `Range` (the base `Bucket` is `Get`-only);
  acceptable for a static bundle in V1, and the existing `blob.RangeReader` follow-on removes it — this is
  independent of the `(ModTime, Size)` validator, which needs no full read.

## Open questions

- **Digest-sourced ETag + ranged read without a full `Get`** — depends on `blob.Attributes` carrying a
  per-object digest and using `blob.RangeReader` (ADR-0080); a perf follow-on, no contract change here.
- **Per-`Route` cache / header / content-type overrides** — rides the V2 per-route edge-policy increment
  (with F74–F78's per-route fields); V1 is a single pinned policy.
- **Compression of static assets** — gzip is applied by the F78 shaping middleware today (process-level);
  whether to pre-store / serve precompressed (`.br`/`.gz`) variants is a follow-on.
- **Bucket-scoped authz + a `backend`/`kind` observ discriminator** — V1 authorizes an authenticated static
  route at *namespace* scope (ADR-0113 coarse RBAC, `FunctionRef{Namespace: ns}`) and overloads the F76
  `function` label with the bucket name; a bucket-scoped authz target and a distinct backend/kind label are
  follow-ons.

## References

- [FEAT-0003/F82](../feat/0003-feat-data-platform.md) (the static-serving feature) ·
  [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (`Route`/`RouteBackend`/`Router`/reconciler
  extended) · [ADR-0113](0113-edge-authn-pep.md) (the auth stance `public` opts out of) ·
  [ADR-0111](0111-tls-termination.md) / [ADR-0114](0114-edge-observability-shaping.md) (the TLS +
  gzip/CORS the site is served over) · [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (the
  per-namespace `Bucket` view read) · [ADR-0007](0007-blob-storage-layer-port.md) (the `blob.Bucket`
  port `Get`/`List`).
- [`net/http.ServeContent`](https://pkg.go.dev/net/http#ServeContent) — the stdlib file-server semantics
  (ETag / `If-None-Match` / Range / `304` / `206`) this delegates to.
- Prior art: the SPA cache split (revalidate `index.html`, immutable hashed assets) — Vite/webpack asset
  fingerprinting; AWS S3 static-website hosting; Cloudflare Pages `_headers`.
