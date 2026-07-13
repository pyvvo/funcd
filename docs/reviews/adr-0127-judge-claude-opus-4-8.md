# ADR-0127 judge verdict — context.blob data-plane

- **Verdict**: **Changes requested — NOT pass-worthy.** The ADR is well-written, concise, and its shim /
  identity / codegen reasoning is sound — but it routes `context.blob` to the **wrong Facade**. The seam it
  names (`services/blob.Facade`, ADR-0021) is unused legacy code that speaks the RBAC dialect, not the
  ADR-0080/0116 `blobBindings` bind-as-grant the feature (F92) requires. As prescribed, **every blob op
  returns 500** and no security scenario can pass. One Blocker + two Majors.
- **Judged by**: claude-opus-4-8 · **Date**: 2026-07-12
- **Judged against**: ADR-0000 (template/lifecycle), FEAT-0001/F92, ADR-0069 (the KV twin), ADR-0080 (the S3
  frontend + bind-as-grant), ADR-0073/0076/0116 (blob bindings + PDP), and the **actual code** in
  `internal/services/blob`, `internal/workernode/local`, `internal/auth/cedar`, `pkg/funcd`, `pkg/sdk`, `shim/`.
- **ADR status**: Proposed → stays Proposed (loops to draft for the Blocker/Majors).

## Goal alignment

ADR-0127 aims to give functions a first-class `context.blob.{get,put,delete,list,signedUrl}` — the blob twin
of `context.kv` — over the per-sandbox worker-node local API (ADR-0064), PDP-authorized by the `blobBindings`
bind-as-grant, removing the boto3/keypair path for in-function use. This is squarely F92 and advances the
blueprint data-plane story. The **shape** is right (verbs on the local API, connection-scoped identity, nil ⇒
no routes, shim mirror). The **wiring target is wrong**: the ADR picked `services/blob.Facade` (ADR-0021, a
`Kind:Service`+`Verb` RBAC facade, *never wired into the platform*) instead of the ADR-0080 s3gateway
bind-as-grant PEP path, which is the true blob analogue of `kvsvc.Facade`.

## Strengths — keep as-is

- **Shim `context.blob` is a genuinely faithful mirror of `context.kv`.** Verified against
  `shim/python/src/funcd_shim/kv.py` + `shim/nodejs/src/kv.ts`: same `FUNCD_INVOKE_SOCKET` dialing, same
  method set, typed on `FunctionContext` (`types.py:57`, `types.ts:31`). The proposed Python `signed_url`/Node
  `signedUrl` extension is clean. Keep the shim design wholesale.
- **Codegen scope-out is correct.** `pkg/sdk/types_gen.go:bindingRows` already emits a `blob` row from
  `b.Blob[].Alias` (verified ~line 267) — the ADR is right that no codegen change is needed. Keep.
- **The identity-adapter reasoning is correct and necessary.** The rebuilt
  `auth.Identity{Subject: ns/fn, Principal: &EntityRef{Type: KindFunction, …}}` exactly matches the
  connection-scoped `callerIdentity` in `internal/workernode/local/local.go:73-77`. This adapter is needed on
  the *correct* (cedar) path too, so the reasoning survives the fix. Keep.
- **`nil ⇒ no /blob routes` + socket-provisioned-for-links-or-kv-or-blob** is a faithful additive
  generalization of the real `NewHandler`/`NewManager` pattern (`local.go:79-81`, `manager.go:30,50`,
  nil-kv-means-no-routes verified). Keep.
- Well-formed Given/When/Then scenarios, each mapped to a named `TestScenarioBlob*`; `?sign=1`-on-GET
  alternative well-argued; template complete; concise; no identity/path leak. Keep.

## Findings by tier

### Blocker

**B1 (adr) — The prescribed wiring is functionally broken: `services/blob.Facade` + cedar PDP returns 500 on
every call and cannot enforce `blobBindings` bind-as-grant.**
Evidence:
- ADR Decision §3 + Contracts prescribe `blob.NewFacade{Bucket: <substrate>, Authorizer: <cedar PDP>}` and
  `blobLocalPort{ f *blob.Facade }` delegating to `services/blob.Facade`.
- `internal/services/blob/blob.go:50-59`: `authorize` builds `auth.Request{Verb: verb, Kind: v1.KindService}`
  — it **never sets `Action`**.
- `internal/auth/cedar/cedar.go:58-59`: the cedar `Authorize` **returns `fault.Internal` ("cedar driver
  requires a per-object Action") whenever `req.Action == ""`.** So routing `context.blob` through this Facade
  with the cedar PDP makes **every** get/put/delete/list/sign fail with 500 — not one scenario passes.
- `services/blob.NewFacade` has **zero non-test callers** (grep): it is unused legacy ADR-0021/F23 code. The
  ADR's premise — *"the only way a function reaches it today is the ADR-0080 S3 frontend"* — is false. The
  ADR-0080 path is `internal/blob/s3gateway` (ADR-0080 Decision; wired at `pkg/funcd/funcd.go:483`), whose
  backend is a PEP calling cedar `s3::read`/`s3::write` on a **`BlobPrefix`** resource with a **`blobBindings`**
  Set (`internal/auth/cedar/capabilities.go:187-206`, `S3Capability`). It does **not** use
  `services/blob.Facade`.
- Even ignoring the 500: `services/blob.Facade`'s `Kind:Service`+`Verb` question is answered by RBAC roles,
  **not** by `spec.blob` bindings — so scenario **blob-unbound-forbidden** ("unbound alias ⇒ 403 via the
  Facade's blobBindings PDP") is unsatisfiable by this seam under *any* authorizer choice.

Goal impact: F92's core clause — "PDP-authorized by the `blobBindings` bind-as-grant; an unbound alias ⇒ 403;
ADR-0073/0076 default-deny kept" — cannot be delivered by the prescribed seam. The entire authz spine is
wrong, and the "reuse the Facade unchanged" framing contradicts the ADR's own Constraints ("bind-as-grant
default-deny evaluates unchanged") and scenarios.

Fix direction: Re-anchor on the **ADR-0080 s3gateway bind-as-grant path**, which is the real `kvsvc.Facade`
twin — not `services/blob.Facade`. Concretely mirror KV: (a) a **blob binding resolver** alias→(bucket,prefix)
from the caller's `spec.blob` (the twin of `kvsvc.NewResolver`); (b) a facade that keys the substrate as
`s3/<ns>/<bucket>/<prefix>/…` (the existing `s3BucketFor` layout, `funcd.go:1462`) and asks the cedar PDP
`Action: s3::read`/`s3::write` on a `BlobPrefix` resource (reuse `S3Capability` / the s3gateway backend PEP
logic). Rewrite the header/Context claim, Decision §2-§3, and the Contracts block accordingly.

### Major

**M2 (adr) — Keyspace + signed-URL divergence: `context.blob` bytes would be invisible to the S3 frontend, and
`signedUrl` is not "the same URL space".** Downstream of B1 but independently makes claims false.
Evidence: `services/blob.Facade.tenantKey` = `<ns>/<binding>/<key>` on the raw `c.blob` bucket
(`blob.go:61-63`), whereas the S3 frontend serves `s3/<ns>/<bucket>/<prefix>/<key>` over the versitygw TCP
listener (`funcd.go:1470`; ADR-0080 addressing). So (a) objects written via `context.blob` land in a
**different prefix** than the frontend reads — contradicting Consequences ("coexist over the same substrate",
"the external/inspect path"); and (b) `services/blob.Facade.SignedURL` calls `f.bucket.SignedURL` on the
gocloud driver directly (`blob.go:113`), returning a **driver-native** URL (and for the memory backend, not a
real external URL) — **not** the versitygw endpoint URL scenario **blob-signed-url** claims ("the same URL
space the ADR-0080 frontend serves").
Fix: key via the `s3/<ns>/<bucket>/<prefix>` layout; for `signedUrl`, either presign against the substrate the
s3gateway also serves (so the URL is genuinely fetchable by an outside tool) or narrow the scenario claim to
what the driver can presign. The "same URL space" promise must be made true or dropped.

**M3 (adr) — Missing blob binding resolver: the plan is under-scoped vs its KV twin.**
Evidence: the KV data plane resolves alias→(store,table) via `kvsvc.NewResolver` **before** the facade
authorizes (`funcd.go:409`). ADR-0127 has no alias→(bucket,prefix) resolver — it inherits
`services/blob.Facade`'s flat `binding` assumption. But the real binding is `FunctionBlob{alias, bucket,
prefix}` (ADR-0080; `internal/function/references.go:46`), so the port cannot map the shim's alias to a
`BlobPrefix` to authorize without one.
Fix: add the blob binding resolver to the Contracts + Implementation plan (mirror `kvsvc.NewResolver` over the
metastore, reading `Function.spec.blob`).

### Minor / Nit

- **Minor (adr)** — the content-type scope-out is justified by `services/blob.Facade.Put` carrying none; that
  artifact is the wrong one, though the *decision* (bytes-only v1, mirroring `kv.put`) is defensible on its own.
  Reword the justification to the real put path so it survives the B1 fix.
- **Nit (adr)** — `maxBlobBytes = 64 MiB` coexists with the s3gateway's `maxUploadBytes` (default 1 GiB,
  ADR-0080). Two caps on one substrate via two surfaces is fine, but note the relationship so an implementer
  doesn't read them as conflicting.
- **Nit (process)** — F92 is a user-facing feature ADR; ensure a Project #4 tracking card exists (the `adr`
  skill's step, not a document defect).

## Template & scenario conformance

All ADR-0000 sections present and ordered. Scenarios are observable Given/When/Then with stable names, each
mapped to a named acceptance test (`TestScenarioBlobReadWrite/List/SignedURL/UnboundForbidden/SizeCap/Parity`).
`Realizes: FEAT-0001/F92` points at a real, in-scope v1.1 row (status `adr`). No absolute path / local
username / personal email leak; identity is `green-0-rabbit` throughout. The Contracts compile as written
(the `Blob` port's `blob.SignOptions` is a real type, `internal/blob/blob.go:42`) — but compiling is not the
bar: the *semantics* of the named seam are wrong (B1).

## Recommendation

Loop back to draft. The document is strong on form and on the shim/identity/codegen legs — keep those intact.
The one substantive move is to **stop reusing `services/blob.Facade` and re-anchor `context.blob` on the
ADR-0080 s3gateway `blobBindings` bind-as-grant path** (a resolver + cedar `s3::read`/`s3::write` over a
`BlobPrefix`, keyed under `s3/<ns>/<bucket>/<prefix>`), which is the genuine `context.kv` twin. That single
correction resolves B1, M2, M3, and the Minor together. Re-judge after the redraft.

## Re-judge (round 2)

- **Verdict**: **pass-worthy — no open Blockers/Majors, ready to accept.** The redraft applied the whole
  round-1 verdict: it re-anchors `context.blob` on a **new** binding-gated facade in `internal/services/blob`
  (the true `kv.Facade` twin) authorizing the existing `S3Capability` with a **Function** principal. B1 is
  resolved; M2, M3, the Minor and both Nits are addressed. The strong legs (shim/identity/codegen/nil-routes)
  are unchanged and still faithful. No new defect introduced.
- **Re-judged by**: claude-opus-4-8 · **Date**: 2026-07-12 · Proposed → pass-worthy (ready to accept).

### B1 — RESOLVED (no longer a Blocker). The authorization path now builds and will Allow via bind-as-grant.
The facade no longer routes through the RBAC `services/blob.NewFacade` (the `Action==""` ⇒ 500 seam). Traced
end-to-end against the code:
- ADR Decision §2 + Contracts (lines 127-142, 214-217) build `auth.Request{Identity{Principal:
  &EntityRef{Type: v1.KindFunction, Namespace: ns, Name: fn}}, Action: ActionS3Read|ActionS3Write, Resource:
  &EntityRef{Type: v1.KindBucket, Namespace: ns, Name: bucket, Path: prefix}}`. This is **byte-for-byte the
  shape the working S3 frontend builds** — `internal/blob/s3gateway/backend.go:70-79` (`authorize`) — except
  the principal is the **Function** ref (backend.go uses the external `S3Identity` ref). Action is **always
  set** (`ActionS3Read`/`ActionS3Write`), so the cedar `Action==""` ⇒ `fault.Internal` path is never hit — no
  500.
- The Function principal **is** materialized by the existing PDP: `internal/auth/cedar/capabilities.go:198-215`
  — `S3Capability.PrincipalBinding{Attr:"blobBindings"}.Bind` has a `case *v1.Function: blobs = o.Spec.Blob`
  arm (line 203-204) emitting `blobPrefixUID(ns, b.Bucket, b.Prefix)` per binding; `FunctionPrincipalSource`
  (line 272-288) resolves the backing `*v1.Function`. So a Function asking `s3::read`/`s3::write` on
  `BlobPrefix(ns,bucket,prefix)` is **Allowed iff** its `spec.blob` declares that binding (built-in
  `builtin_s3.cedar` read-binding-grant + prefix-owner write) — exactly F92's bind-as-grant, **zero PDP
  change**. `blob-unbound-forbidden` is now satisfiable (unbound alias ⇒ resolver default-deny and/or PDP
  deny ⇒ 403). The ADR explicitly supersedes the unused legacy `NewFacade` (Decision §2 line 141, Alternatives
  row, Consequences line 308).
- The "no 500 / builds the Function principal internally" pattern is real precedent: `kv.Facade.authorize`
  sets `principal := &auth.EntityRef{Type: v1.KindFunction, Namespace: ns, Name: fn}` at
  `internal/services/kv/kv.go:77`, and `kvFacade` is passed **directly** into `local.NewManager`
  (`pkg/funcd/funcd.go:441,447`) with no adapter — the ADR's blob facade mirrors this method-for-method.

### M2 — RESOLVED. Same keyspace + honest signed-URL.
Decision §2(c) + Contracts (line 217) act on the **`s3BucketFor(ns, bucket)` substrate view under
`blobKey(prefix, key)`** — the same resolver (`pkg/funcd/funcd.go:1462`, returns exactly the `func(ns,bucket)
(blob.Bucket,bool)` the `FacadeDeps.BucketFor` expects) and keyspace (`s3gateway.blobKey`, backend.go:98-103)
the S3 frontend serves, so objects coexist (`aws s3 ls` sees them). The `blob-signed-url` scenario (lines
58-61) + Consequences no longer overclaim "same URL space the frontend serves" — now a **substrate-driver
presigned URL** (real on S3/GCS, best-effort on mem/file), which is what `blob.Bucket.SignedURL(ctx, key, opts
SignOptions)` (internal/blob/blob.go:22) actually returns.

### M3 — RESOLVED. The blob binding resolver is specified.
Contracts (lines 208-213) define `BindingResolver.Resolve(ctx, ns, fn, alias) (Binding, error)` +
`Binding{Bucket, Prefix}` "mirrors services/kv.BindingResolver" (default-deny, `fault.Forbidden` on no
`spec.blob` entry) — the exact shape of `internal/services/kv/resolver.go:26-28,101-103`. Scope (lines 74-75),
Decision §2(a), and Implementation plan (`resolver.go`, line 249) all carry it.

### Adapter — correctly dropped, consistent throughout.
Scope (line 78), Decision §2 (139-141), Implementation plan (256), and Review checklist (289) all state the
facade **satisfies `local.Blob` directly, no `pkg/funcd` adapter** (builds the Function principal internally
like `kv.Facade`). No leftover "adapter" wording contradicts this.

### Minors — addressed.
- Content-type scope-out now cites the **substrate `blob.Bucket.Put(ctx, key, data []byte)`** (internal/blob/blob.go:18,
  carries no content-type) — Scope line 84, Alternatives line 108 — not the wrong legacy Facade. Correct.
- `maxBlobBytes` (64 MiB) vs s3gateway `maxUploadBytes` (1 GiB) relationship noted in Consequences (306-307).

### No new defect.
Decision ↔ Contracts ↔ Review checklist ↔ Scenarios agree (routes, `?sign=1`+`?method`/`?expiry`, RFC 9457
mapping, `blob.SignOptions`). The shim/identity/nil-routes/codegen legs are unchanged and still faithful.
Template complete; concise; no absolute-path / local-username / personal-email leak (grepped clean).
