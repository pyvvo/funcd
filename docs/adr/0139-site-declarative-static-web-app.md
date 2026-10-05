# ADR-0139: `Site` — the declarative static web app (F103)

- **Status**: Implemented
- **Date**: 2026-08-04 (**Implemented 2026-09-17** — review pass (claude-opus-5): 19/19 scenarios, in-process e2e + the s3 containerd lane green; **Accepted 2026-09-17** — judge pass folded: the owned `Route` is the durable
  serving record and `status` is derived (an apply wipes status); tag resolved once per generation;
  index written last; `Route` readiness polled with requeue; `PrefixOwned` on adoption collision;
  `public`+`authenticated` rejected in `Validate`; `spec.prefix` immutable unconditionally; `ingress`
  required; `file://` dropped; the unwired bucket-admission object check no longer cited)
- **Superseded in part by**: [ADR-0140](0140-path-mounted-site.md) (2026-09-27) — **only Decision §8's
  pinned `/` bundle-rule path** (now the resolved `ingress.path`; host-less ⇒ `/site/<name>`) and the
  `SiteIngress` contract (one added field). Every other decision here — the digest-scoped prefix,
  index-last materialization, the Route as durable record, inline ownership, `spec.prefix`
  immutability, the artifact type — **stands**, as does its FEAT-0003/F103 row.
- **Superseded in part by**: [ADR-0163](0163-retry-times-in-config.md) (2026-10-05) — Decision 6 RequeueAfter 2 s: now controller.referentPollInterval.
- **Superseded in part by**: [ADR-0170](0170-owner-garbage-collector.md) (2026-10-05) — §5, rows 293 and 296, checklist, Consequences: the collector reclaims a deleted Site's Route and a Function's Revisions.
- **Deciders**: green-0-rabbit
- **Tags**: edge, static, blob, bucket, route, artifact, dx, data-platform
- **Realizes**: [FEAT-0003/F103](../feat/0003-feat-data-platform.md) — declarative static web app (`Site`): the content half of F82.
- **Relates to**: [ADR-0120](0120-static-asset-serving-route.md) (F82 — the static `Route` backend this feeds; **unchanged**) · [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (F79 — the `Route` materialized) · [ADR-0089](0089-python-function-dependency-bundling.md) (the deterministic tar+gzip directory bundle + traversal-safe untar reused) · [ADR-0031](0031-oci-artifact-distribution-oras.md) (OCI push/pull) · [ADR-0035](0035-artifact-digest-resolution-at-revision.md) (tag → digest at stamp time, mirrored here) · [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (the per-namespace `Bucket` view written) · [ADR-0007](0007-blob-storage-layer-port.md) (the `blob.Bucket` port) · [ADR-0094](0094-workflow-engine-core.md) (`Workflow.spec.kv` — the inline owned-resource pattern copied) · [ADR-0136](0136-roles-and-role-assignments.md) (the generalized `writers` single-writer rule the ownerless prefix relies on) · [ADR-0091](0091-function-catalog-consumer-binding.md) (the requeue-until-Ready precedent)

## Context & Need

ADR-0120 made funcd **serve** a `Bucket` prefix over the edge. Nothing **puts the bytes there,
versions them, or reports whether they arrived**. Three consequences, all live today:

- A static `Route` is `Ready` as soon as its `Bucket` exists — an empty prefix serves `404`s and the
  platform calls it green.
- No resource records **which build is live**. `examples/python/releve-lakehouse` points its `bi` Route
  at `reports/` and the bytes get there by whatever wrote them last.
- There is **no rollback**, and a redeploy overwrites in place, so a visitor mid-deploy sees a mix of
  old and new files.

**Purpose.** `Site` is the deployable unit of a prebuilt web app: it names an **immutable bundle**,
materializes it into a governed `Bucket` prefix, and reports `Ready` only once that bundle is actually
servable. It **declares its adjacent `Bucket` and `Route` inline and owns them** (the
`Workflow.spec.kv` pattern), so a whole site — assets, exposure, lifecycle — is one manifest, and it
exposes **sibling data prefixes alongside the app** (the Observable BI bundle plus the `gold` Parquet it
fetches). Callers: whoever `funcdctl apply`s a `Site`; `funcdctl push` on the authoring side.

This ADR is the **content and lifecycle** half. It changes **nothing** in ADR-0120 — the materialized
`Route` uses the existing `backend.static.{bucket,prefix}` arm verbatim.

## Scenarios

Each becomes a named acceptance test.

- `site-materializes-and-serves` — Given a `Site` naming a pushed bundle containing `index.html` and
  `img/logo.png`, When it reconciles, Then the objects land under `<prefix>/<digest-slug>/`, the `Site`
  is `Ready` with `status.digest` set, and `GET /` returns `index.html` while `GET /img/logo.png`
  returns the asset.
- `owned-bucket-and-route-materialized` — Given a `Site` with `spec.bucket` and `spec.ingress`, When it
  reconciles, Then a `Bucket` and a `Route` (named after the `Site`) exist — the `Route` carrying an
  `OwnerReference` to the `Site`, the `Bucket` **not** (V1 is `retain`-only, §5) — the `Bucket` holds the
  site prefix (no owner) plus every declared prefix, and the `Route` carries one rule per
  `spec.ingress.rules` plus the bundle rule at `/`.
- `adopted-bucket-prefixes-preserved` — Given a pre-existing `Bucket` carrying prefixes the `Site` did
  not declare (a lakehouse's `gold` with its owner), When the `Site` adopts it, Then those entries are
  byte-identical afterwards — the reconciler only **adds** its own prefix and never removes or rewrites
  one it did not add.
- `adopted-prefix-with-owner-rejected` — Given a pre-existing `Bucket` that already declares
  `spec.prefix` **with an owner**, When the `Site` adopts it, Then the `Site` is `NotReady`
  (`PrefixOwned`), the entry is left untouched, and nothing is materialized — a `Site` never takes over a
  written prefix.
- `redeploy-swaps-atomically` — Given a Ready `Site` serving digest A, When `spec.image` changes to a
  bundle with digest B, Then no request is served from a partially-written prefix: the `Route` still
  resolves A until B is fully materialized **and** its index is present, after which every request
  resolves B and `status.digest` reads B.
- `partial-unpack-recovers` — Given a materialization of digest B that stopped after some objects but
  **before the index object was written** (a crash), When the `Site` reconciles again, Then the `Route`
  never resolved B in between, the missing objects are re-uploaded, the index is written **last**, and
  only then does the `Route` swap to B.
- `not-ready-until-index-present` — Given a **redeploy** whose bundle has no `index.html`, When it
  materializes, Then the `Site` is `NotReady` (`IndexMissing`), `status.digest` still names the
  **previous** serving digest (recovered from the owned `Route`, which is what is served — not from a
  status field the apply may have wiped), and the `Route` is not re-programmed — the old build keeps
  serving; and Given the **first** deploy fails the same way, Then `status.digest` stays empty and **no
  `Route` is materialized at all**, so the host `404`s rather than serving a broken site.
- `tag-resolved-once-per-generation` — Given a Ready `Site` whose `spec.image` is a **tag**, When the tag
  moves in the registry and a reconcile runs with **no spec change**, Then the `Site` keeps serving the
  pinned digest; and When the `Site` is re-applied, Then the tag is resolved again and the new digest
  materialized — an apply is deploy intent, a self-triggered reconcile is not.
- `rollback-to-previous-digest` — Given a `Site` that has served digests A then B, When `spec.image` is
  set back to A, Then it serves A again with **no re-upload** (the objects were never removed) and
  `status.digest` reads A.
- `data-mount-serves-sibling-prefix` — Given `spec.ingress.rules: [{path: /data, prefix: gold}]`, When
  `GET /data/part-0.parquet` arrives, Then the object under the bucket's `gold` prefix is returned.
- `data-mount-does-not-spa-fallback` — Given `spec.spa: true` and a `/data` rule, When
  `GET /data/missing.parquet` arrives, Then the response is `404` — **not** `index.html` with `200`.
- `bundle-prefix-has-no-writer` — Given a materialized `Site` and no writer-role `RolesAssignment` on
  the prefix, Then the site prefix carries **no** `owner`, and a write to it over the S3 frontend by any
  principal is `Forbidden`.
- `retain-stamps-no-owner-reference` — Given a `Site` whose `spec.bucket.deletion` is `retain` (or
  omitted), When it materializes, Then the `Bucket` carries **no** `OwnerReference` — it outlives the
  `Site` — while the owned `Route` always carries one.
- `delete-cascade-rejected` — Given a `Site` with `spec.bucket.deletion: delete`, Then `Validate`
  rejects it (`fault.Invalid`, not implemented in V1).
- `public-and-authenticated-rejected` — Given `spec.ingress.public: true` and
  `spec.ingress.auth.mode: authenticated`, Then `Validate` rejects it (`fault.Invalid`) — the same
  contradiction ADR-0120 §4 rejects on a `Route`, caught at apply rather than as a reconcile error.
- `foreign-route-not-adopted` — Given a pre-existing `Route` of the same name **not** carrying an
  `OwnerReference` to this `Site`, When the `Site` reconciles, Then it is `NotReady` (`RouteNotOwned`)
  and the foreign `Route` is left untouched. (Distinct from ADR-0110's `RouteConflict`, which is a
  cross-namespace host/path collision.)
- `site-not-ready-when-route-not-ready` — Given a `Site` whose owned `Route` is `NotReady` (an empty
  `ingress.host` in an `explicit`-mode namespace, or an ADR-0110 `RouteConflict`) **or still pending**
  (just written, its Ready condition not yet observing its generation), Then the `Site` is `NotReady`
  (`RouteNotReady`) — and requeues while pending — even though its bundle materialized; a `Site` is
  never `Ready` while unreachable.
- `prefix-is-immutable` — Given any `Site`, When an Update changes `spec.prefix`, Then admission rejects
  it (`fault.Invalid`) — unconditionally, materialized or not: a prefix that has held a bundle is never
  removed from the `Bucket` by this reconciler, so a changed prefix would orphan it; a new prefix is a
  new `Site`.
- `traversal-safe-unpack` — Given a bundle whose tar contains `../escape` or an absolute path, When it
  materializes, Then the entry is refused, no object is written outside the site prefix, and the `Site`
  is `NotReady` (`MaterializeFailed`).

## Scope

**In:**
- The **`Site` kind** (`api/types/v1alpha1/site.go`) + `KindSite` registration + `Validate`.
- A **site OCI artifact type** — `application/vnd.funcd.site.artifact.v1` carrying one ADR-0089
  `BundleTarMediaType` layer; **no contract blob, no runtime annotation** (a site has no I/O contract).
- `funcdctl push --site <dir> <ref>` (producer) and a **`Site` reconciler** (consumer) that resolves
  tag → digest once per spec generation, pulls, unpacks into `<prefix>/<digest-slug>/` over
  `blob.Bucket` (index written last), then materializes/updates the owned `Bucket` + `Route` and
  publishes a status **derived** from what the `Route` serves.
- **Inline owned adjacent resources** — `spec.bucket` (adopt-or-create, `retain`-only in V1, therefore
  **never** stamped with an `OwnerReference`) and `spec.ingress` (**required** in V1: an owned `Route`,
  **always** stamped — a `Site` with no `Route` could never be `Ready`, §6).
- A **`site-prefix-immutable` Validating admission** on `Site` Update (ADR-0063 framework),
  unconditional.

**Out (named follow-ons):**
- **`deletion: delete` cascade** — rejected at `Validate` in V1 (see *Temporary workarounds*).
- **Retention / GC of superseded digest prefixes** — they accumulate, exactly as `Revision` does today
  (no GC field anywhere in the platform). A count-based retention policy is a platform-wide decision
  covering `Revision` and `Site` together, not a static-hosting side effect.
- **A `backend.static.site` arm on `RouteBackend`** — exposure is `spec.ingress` only; a hand-written
  `Route` cannot name a `Site` (it would have to type a digest, which this ADR exists to avoid).
- **Per-route cache/header overrides, compression variants, SSR, the site *build* step** — inherited
  unchanged from ADR-0120's out-of-scope list.
- **Multi-host / multi-Route exposure of one `Site`** — one `Site` owns exactly one `Route`.

## Constraints & Decision drivers

- **ADR-0120 stays frozen.** The materialized `Route` uses the existing `backend.static.{bucket,prefix}`
  arm. This ADR adds no backend arm and touches no edge middleware or handler.
- **Reuse the ADR-0089 bundle transport.** A deterministic tar+gzip directory layer with a
  **traversal-safe untar** already exists; identical trees yield an identical digest, so ADR-0035 pinning
  holds. No new dependency.
- **In-daemon writes go over the port.** `internal/edge/static` reads and `internal/funclog/compact`
  writes `blob.Bucket` directly; the PDP guards the S3 wire and the function-facing facade, not
  control-plane components.
- **Copy `Workflow.spec.kv` for inline ownership** — adopt-if-present/create-if-absent, and the
  `OwnerReference` attached **only** for `delete`, exactly as `buildKVStore` does it.
- **Two-tier validation** — huma tags carry field shape; `Validate()` carries semantic rules;
  cross-resource existence is reconcile-time (ADR-0121), never admission.

## Alternatives considered

| Option | Verdict |
|---|---|
| **`Site` materializes into a digest-scoped prefix; owns its `Bucket` + `Route` (chosen).** | Atomic swap and rollback fall out of the digest-scoped key; `Ready` becomes meaningful; one manifest per site; ADR-0120 unchanged. |
| Keep the status quo — upload out-of-band, hand-write `Bucket` + `Route`. | Rejected — this is exactly the gap F103 exists to close: no live-build record, no rollback, `Ready` over an empty prefix. |
| A `Site` + a `SiteRevision` kind, mirroring `Function` + `Revision`. | Rejected — a second kind buys nothing here. `Revision` exists to carry rollout/canary mechanics (explicitly out of V1 scope); `status.digest` plus a digest-scoped prefix already gives immutability and rollback. |
| Unpack over the site prefix **in place** (no digest segment). | Rejected — a visitor mid-deploy sees a mix of old and new files, a failed unpack leaves a broken site live, and rollback needs a re-upload. |
| Name the kind `Artifact`. | Rejected — `artifact` already means the OCI *function* bundle throughout `internal/artifact` and ADR-0031/0035/0059; "artifact digest" would become ambiguous in every one of them. |
| Add `backend.static.site` so a hand-written `Route` can expose a `Site`. | Rejected for V1 — two ways to do one thing, and it extends ADR-0120's exactly-one-of union. `spec.ingress` covers the case; revisit if multi-host exposure is needed. |
| Give the `Site` an owned `Identity` + `RolesAssignment` so it writes through the PDP. | Rejected — `IdentityType` is `external`-only and ADR-0135 states a system-assigned identity is not modeled as an `Identity`; it would mint an unused SigV4 keypair and add two owned resources. An **ownerless** prefix written in-daemon is *stricter*: by default no principal can write it over the S3 wire — only a deliberate ADR-0136 writer-role `RolesAssignment` by the operator could. |
| A `retain: N` count on the `Site` bounding superseded digests. | Rejected — it would be the platform's first count-based retention policy, decided as a side effect of static hosting, while `Revision` (the direct precedent) has none. Deferred to a platform-wide ADR. |
| Record the serving digest in `status.digest` and treat it as durable. | Rejected — the store is last-writer-wins on status and `funcdctl apply` PUTs the manifest as-is, so every apply wipes it. The owned `Route`'s bundle rule **is** what is served; it is the durable record, and `status` stays a derived view like every other kind's. |
| Make `spec.ingress` optional (a materialize-only `Site`). | Rejected for V1 — with no `Route` nothing is served, the `Site` can never be `Ready` (§6), and no durable record of the digest exists. A hand-written `Route` would have to type a digest, which this ADR exists to avoid. |

## Decision

1. **`Site` is a namespaced, status-bearing kind.** `spec.image` is an OCI ref (tag or digest);
   `spec.bucket` declares the target `Bucket`; `spec.prefix` is the bucket sub-domain rooting the site;
   `spec.index`/`spec.spa` describe the bundle; `spec.ingress` declares the owned `Route` (required).

2. **The digest is the authority; the serving key is digest-scoped; a tag is resolved once per spec
   generation.** The reconciler resolves `spec.image` → a manifest digest **only when
   `status.observedGeneration != metadata.generation`** (mirroring ADR-0035's resolve-at-stamp-time: the
   author never types a digest, and a tag that moves in the registry is never picked up by a
   self-triggered reconcile — an apply is the deploy intent that re-resolves it). `observedGeneration`
   is advanced to `metadata.generation` **only once that generation's digest is programmed on the
   `Route`**; a generation whose deploy failed (`IndexMissing`, `MaterializeFailed`, …) stays unobserved,
   so every later reconcile re-resolves and retries it and the `Site` stays `NotReady` until the spec
   changes or the deploy succeeds. It materializes every bundle entry under
   **`<prefix>/<digest-slug>/`**, where `digest-slug` is the digest with `:` replaced by `-` (a clean
   object-key segment). Rollback is re-pointing `spec.image`; the prior digest's objects are never
   removed.

3. **Ready is gated on the bundle being servable; the index is written last; the owned `Route` is the
   durable record.** The reconciler `Put`s every non-index entry first and the index object **last**, so
   the index's presence is the completeness marker: a crash mid-unpack leaves no index, and the next
   reconcile re-uploads. An already-complete digest (index present) is re-verified and not re-uploaded.
   Only once the index is asserted does the reconciler re-program the owned `Route`'s bundle rule to
   `<prefix>/<digest-slug>/`. **The programmed `Route` is what is served, so it is the durable record of
   the serving digest** — `status.digest` / `status.servingPrefix` are **derived from it on every
   reconcile**, never treated as durable (the store is last-writer-wins on status and an apply wipes it).
   A failure leaves the `Route` — hence the **previous** digest — serving and reports `NotReady`; a
   broken deploy never takes the site down.

4. **The site prefix has no writer.** The reconciler writes over `blob.Bucket` in-daemon (the
   `internal/funclog/compact` pattern) against the same per-namespace view ADR-0120 reads
   (`s3BucketFor`). The materialized `BucketPrefix` for `spec.prefix` carries **no `owner`**, so the
   generalized single-writer rule (ADR-0080, `writers` set per ADR-0136) denies every S3-wire and function
   write to it — unless an operator deliberately grants a writer role on it by `RolesAssignment`, which is
   their act, not a default. Prefixes declared in `spec.bucket.prefixes` are passed through verbatim,
   owners included. Note the declaration is for **visibility and S3-wire denial**, not for the write
   itself: an in-daemon `blob.Bucket` write never consults `BucketPrefix`, so the prefix entry is not the
   enforcement path for the reconciler.

5. **Inline ownership follows `Workflow.spec.kv` exactly — the `OwnerReference` *is* the cascade marker,
   not an ownership label.** Per [`internal/workflow/reconcile_workflow.go`](../../internal/workflow/reconcile_workflow.go)
   (`buildKVStore`), `delete` attaches a cascading `OwnerReference` and `retain` attaches **none**, so the
   object outlives its parent. Therefore:
   - **`Bucket`** — **adopted if present, created if absent**, stamped with an `OwnerReference` **only**
     when `deletion: delete`. Since V1 rejects `delete` (§9), a V1 `Site` **never** stamps its `Bucket`.
     Adopting a `Bucket` the `Site` did not create is **explicitly permitted** — that is the shared-substrate
     case (a site prefix inside a lakehouse bucket) — and there is deliberately **no** not-owned check,
     because under `retain` no marker exists to check one against. What the reconciler may touch is bounded
     instead: it only **adds** `spec.prefixes` entries, and never removes or rewrites one it did not add.
     The one collision this leaves — the adopted `Bucket` **already declares `spec.prefix` with an
     owner** — resolves in favour of the existing writer: the `Site` is `NotReady` (`PrefixOwned`), touches
     nothing, and materializes nothing; a `Site` never takes over a written prefix. Like `Workflow.spec.kv`,
     these store-direct writes do not pass the API admission chain, so a created `Bucket` is not counted by
     the `bucket-count` quota admission — a property of the copied pattern, named here rather than fixed.
   - **`Route`** — named after the `Site` (`metadata.name`), **always** stamped (pure exposure, no data),
     which makes it the one object whose ownership *is* checkable: a same-named `Route` carrying no
     `OwnerReference{Kind: Site, Name, UID}` matching this `Site` is foreign, so the `Site` goes `NotReady`
     (`RouteNotOwned`) and leaves it untouched.

   **This ADR stamps the reference; it does not reclaim.** No `OwnerReference` collector exists in the
   platform — the field is written by `internal/workflow` and `internal/services/identity` and **read
   nowhere** — so deleting a `Site` leaves its owned `Route` behind. That is a **pre-existing platform gap**
   (it equally affects `Workflow.spec.kv[].deletion: delete` and `Identity`'s owned `Secret` today), recorded
   in *Temporary workarounds*; this ADR neither invents nor fixes it.

6. **A `Site` is `NotReady` while its owned `Route` is — or is still pending.** There is one reconciler
   per kind and no owner-watch, so right after the `Site` writes its `Route` the `Route`'s status is
   stale. The reconciler reads the `Route`'s Ready condition: **absent, or `ObservedGeneration` below the
   `Route`'s `metadata.generation`, is *pending*** ⇒ the `Site` is `NotReady` (`RouteNotReady`) and
   requeues (`RequeueAfter` 2 s, the ADR-0091 consumer-binding precedent) until the condition is current;
   a current `NotReady` (an ADR-0110 `HostRequired` / `RouteConflict`, any reason) propagates as
   `RouteNotReady` without requeue. A later `Route` regression is observed at the next `Site` reconcile. A
   `Site` whose bundle materialized but whose edge entry never programmed is **unreachable**, and reporting
   it `Ready` would reintroduce the exact false-green this ADR exists to remove.

7. **`spec.prefix` is immutable — unconditionally.** The reconciler only ever *adds* prefixes to the
   `Bucket` (§5), so a changed `spec.prefix` would leave the old prefix orphaned with its objects and no
   `Site` referring to it. (The bucket deletion-protection admission does **not** guard this: production
   wires it with a nil blob prober, so it protects only prefixes named by a `Function.spec.blob` binding, never
   object presence.) The `site-prefix-immutable` Update admission rejects any change to `spec.prefix`,
   materialized or not — a condition on `status` would be wiped by the next apply (§3); moving a site to a
   new prefix means a new `Site`.

8. **The owned `Route` uses ADR-0120's existing arm.** Two rule shapes are compiled (the router is
   longest-path-first, ADR-0110, so their order is immaterial):
   - one rule per `spec.ingress.rules[]`, `backend.static{bucket, prefix: "<prefix>/", public}` — a data
     mount. `SiteRule` carries **no `spa` or `index` field at all**, so the compiler cannot set them: a
     data-mount miss is `404` by construction, never the app shell. This is structural, not a default;
   - the bundle rule at `/`, `backend.static{bucket, prefix: "<prefix>/<digest-slug>/", index, spa,
     public}`.

   `spec.ingress.host` maps to `Route.spec.host` and `spec.ingress.auth` to `Route.spec.auth`;
   `spec.ingress.public` sets `static.public` on each backend (never `spec.auth.mode`, preserving
   ADR-0120 §4's precedence). `public: true` together with `auth.mode: authenticated` is the contradiction
   ADR-0120 §4 rejects on a `Route`; `Site.Validate` rejects it at apply, so the reconciler never writes a
   `Route` the store's `Validate()` would refuse. ADR-0110's `HostRequired` / `RouteConflict` rules then apply
   to the owned `Route` like any other — a `Site` can never shadow another namespace's edge, and §6
   propagates the result to the `Site`'s own status.

9. **`spec.bucket.deletion` accepts `retain` only in V1.** `delete` is rejected at `Validate`
   (`fault.Invalid`): the collector that would honour it does not exist (§5), and even with one, a cascade
   today would delete the `Bucket` *resource* while its objects remain (the deletion-protection admission's
   object-presence check is not wired, §7) — an orphaned substrate. `DeletionPolicy`'s shared huma
   `Schema()` renders both values, so the generated OpenAPI advertises `delete`; the divergence is deliberate
   (the enum is shared with `Workflow`) and is caught by `Validate` with an explicit "not implemented in V1"
   message rather than a generic enum error.

10. **The site artifact is its own type.** `application/vnd.funcd.site.artifact.v1` with a single
    `BundleTarMediaType` layer and **no** contract blob or runtime annotation. A distinct type keeps a
    site from being pulled by the function materializer, and keeps ADR-0090's "no contract-less *function*
    artifact" rule intact rather than weakening it with a void contract.

11. **Producer side.** `funcdctl push --site <dir> <ref>` packs the directory with the existing
    deterministic tar and prints `<ref>@<digest>`. `--site` is mutually exclusive with the contract/runtime
    flags (a site has neither).

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **No owned object is ever reclaimed — deleting a `Site` leaves its `Route` behind** | the `OwnerReference` field is written by `internal/workflow` and `internal/services/identity` and **read nowhere**: the platform has no collector. Building one here would be a platform-wide mechanism decided inside a static-hosting ADR, and it already silently affects two shipped features | a platform-wide `OwnerReference` collector ADR (carded); once it lands, a deleted `Site` reclaims its `Route` with no change to this ADR's contracts — the reference is already stamped |
| **`spec.bucket.deletion: delete` rejected at `Validate`** | no collector honours it (above), *and* nothing drains objects: the bucket deletion-protection admission is wired with a nil blob prober (`pkg/funcd/funcd.go`), so a cascade would delete the `Bucket` resource and orphan its objects — a half-run cascade is data loss either way | the collector ADR lands **and** a follow-on ships an ordered drain-then-delete teardown with a scenario proving it; `Validate` then accepts `delete` |
| **`spec.prefix` immutable (unconditionally)** | the reconciler only adds prefixes, so a changed prefix would orphan the old one with its objects; and no `status`-conditioned rule can gate it, since an apply wipes `status` | a prefix migration (materialize into the new prefix, re-program, then drain the old) once a drain path exists; until then a new prefix is a new `Site` |
| **Superseded digest prefixes accumulate** | `Revision` — the direct precedent — has no retention or GC; inventing one here would put a platform-wide policy at the wrong altitude | a platform-wide retention ADR covering `Revision` and `Site`; until then an operator drains stale prefixes over the S3 frontend |
| **One `Route` per `Site`** | multi-host exposure needs either several owned Routes or a `backend.static.site` arm — both widen this ADR | a follow-on ADR if multi-host or hand-written exposure is requested |

## Contracts

### Resource (`api/types/v1alpha1/site.go`)

```go
// Site is a namespaced, status-bearing static web deliverable (ADR-0139, F103): an immutable bundle
// materialized into a governed Bucket prefix and exposed at the edge, with its adjacent Bucket and
// Route declared inline and owned by it. Ready only once the bundle is materialized and its index is
// present, so a Site is never Ready over an empty prefix.
type Site struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       SiteSpec   `json:"spec"`
	Status     SiteStatus `json:"status,omitempty"`
}

// SiteSpec is the desired state: which bundle, where it materializes, and how it is exposed.
type SiteSpec struct {
	// Image is the site bundle OCI ref (oci-layout://<dir>[:<tag>] or a registry ref; a tag or a
	// digest). A tag is resolved to a digest once per spec generation — the author never types one.
	Image string `json:"image"`
	// Bucket declares the Bucket the bundle materializes into: adopted if present, created if absent.
	Bucket SiteBucket `json:"bucket"`
	// Prefix is the Bucket sub-domain rooting the site; objects land under <Prefix>/<digest-slug>/.
	// Immutable after creation (the site-prefix-immutable admission).
	Prefix string `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Index is the document served for "/" / a directory / (when SPA) a miss. Default "index.html".
	Index string `json:"index,omitempty"`
	// SPA serves Index (200) for an un-matched path under the bundle rule so a client-side router owns
	// routing. It NEVER applies to a data mount (Ingress.Rules), whose miss is always 404.
	SPA bool `json:"spa,omitempty"`
	// Ingress declares the Site-owned Route (named after the Site). Required in V1.
	Ingress SiteIngress `json:"ingress"`
}

// SiteBucket declares the target Bucket (the WorkflowKVStore pattern): adopted if present, created if
// absent; Deletion governs teardown only.
type SiteBucket struct {
	// Name is the Bucket name; a DNS-1123 label.
	Name ObjectName `json:"name"`
	// Deletion is "retain" (default — the Bucket outlives the Site). "delete" is rejected in V1.
	Deletion DeletionPolicy `json:"deletion,omitempty"`
	// Prefixes are additional sub-domains materialized on the Bucket verbatim (owners included). The
	// Site's own Prefix is added implicitly with NO owner and must not be repeated here.
	Prefixes []BucketPrefix `json:"prefixes,omitempty"`
}

// SiteIngress declares the Site-owned Route. The bundle is served at "/"; Rules add sibling prefixes.
type SiteIngress struct {
	// Host is the exact host match on the owned Route; "" matches any host (ADR-0110 requires a
	// non-empty host in an `explicit`-mode namespace).
	Host string `json:"host,omitempty"`
	// Public sets static.public on every compiled backend (ADR-0120 §4 precedence preserved).
	Public bool `json:"public,omitempty"`
	// Auth is the edge auth stance copied onto the owned Route; nil ⇒ inherit the namespace default.
	Auth *RouteAuth `json:"auth,omitempty"`
	// Rules are additional bucket prefixes served alongside the app (the data the app fetches).
	Rules []SiteRule `json:"rules,omitempty"`
}

// SiteRule serves one bucket prefix at an edge path. Never SPA: a miss is 404, not the app shell.
type SiteRule struct {
	// Path is the edge path; must begin "/" and must not be "/" (that is the bundle's).
	Path string `json:"path"`
	// Bucket names a Bucket in this Site's namespace; "" ⇒ the Site's own Bucket.
	Bucket ObjectName `json:"bucket,omitempty"`
	// Prefix is the Bucket sub-domain served at Path.
	Prefix string `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}

// SiteStatus is the observed state: which build is live and where it is served from. Every field is
// DERIVED on each reconcile — Digest/ServingPrefix from the owned Route's bundle rule (what is
// actually served) — never read back as durable: the store is last-writer-wins on status and an apply
// wipes it. Status.ObservedGeneration gates tag resolution (Decision §2).
type SiteStatus struct {
	Status `json:",inline"`
	// Digest is the manifest digest the owned Route currently serves.
	Digest string `json:"digest,omitempty" pattern:"^sha256:[a-f0-9]{64}$"`
	// ServingPrefix is the digest-scoped key prefix the owned Route is programmed with.
	ServingPrefix string `json:"servingPrefix,omitempty"`
	// Objects and Bytes are the materialized bundle's observed size.
	Objects int   `json:"objects,omitempty"`
	Bytes   int64 `json:"bytes,omitempty"`
}

// Validate performs envelope + semantic validation: Image non-empty; Prefix a DNS-1123 label;
// Index a relative path (no leading '/'); Deletion ∈ {"", retain} — `delete` is Invalid in V1 with an
// explicit "not implemented" message (the DeletionPolicy enum is shared with Workflow, so the generated
// OpenAPI still advertises `delete`); spec.bucket.prefixes must not repeat spec.prefix and must have
// unique names; every ingress rule Path begins "/" and is neither "/" nor a duplicate;
// ingress.public together with ingress.auth.mode == authenticated is Invalid (the ADR-0120 §4
// contradiction, caught here so the reconciler never writes a Route the store's Validate refuses).
// Cross-resource state (does the Bucket exist) is the RECONCILER's (→ NotReady), never Validate.
func (s *Site) Validate() error

// GroupVersionKind returns the constant GVK for Site.
func (s *Site) GroupVersionKind() GroupVersionKind { return KindSite.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (s *Site) GetStatus() *Status { return &s.Status.Status }
```

`NotReady` reasons: `ArtifactNotFound` · `DigestUnresolved` · `MaterializeFailed` · `IndexMissing` ·
`PrefixOwned` · `RouteNotOwned` · `RouteNotReady`. (There is no `BucketNotOwned` — Bucket adoption is
permitted by Decision §5 and, under `retain`, unverifiable by construction; `PrefixOwned` is the one
adoption collision, §5.)

### Prefix immutability admission (`internal/controlplane/admission/site.go`)

`spec.prefix` immutability is a **Validating admission on Update**, not a `Validate()` rule: `Validate()`
never sees the previous object, whereas the ADR-0063 `Request` already carries `Old` — the exact shape
[`bucketDeletionProtection.admitUpdate`](../../internal/controlplane/admission/bucket.go) uses. It is
**unconditional** — it must not consult `Old.Status`, which an apply wipes (Decision §3).

```go
// NewSitePrefixImmutableAdmission returns the Validating admission that rejects any change to
// spec.prefix on Update. The reconciler only ever adds prefixes to the Bucket, so a changed prefix
// would orphan the old one with its objects (Decision §7). It needs no dependencies — Old and
// Object are both on the Request.
//
//	Name()  == "site-prefix-immutable"
//	Phase() == Validating
//	Handles(gvk, op) == (gvk == KindSite.GVK() && op == Update)
//	Admit   → fault.Invalid when Object.Spec.Prefix != Old.Spec.Prefix; otherwise req.Object unchanged.
func NewSitePrefixImmutableAdmission() Admission
```

Registered alongside the existing Validating admissions in the control-plane wiring.

### Site artifact (`internal/artifact`) — additive

```go
// SiteArtifactType marks an OCI manifest as a funcd static-site bundle: one BundleTarMediaType layer,
// no contract blob and no runtime annotation (a site has no I/O contract). A distinct type keeps a site
// from being pulled by the function materializer.
const SiteArtifactType = "application/vnd.funcd.site.artifact.v1"

// PushSite packs dir as the deterministic tar+gzip bundle layer (ADR-0089) under SiteArtifactType and
// pushes it to ref's target, returning the manifest digest. Empty dir ⇒ fault.Invalid. It packs
// WITHOUT an entry-file gate: the deterministic packer behind PackBundle is factored so PackBundle
// keeps its handler-entry check for function bundles and PushSite uses the entry-less path (a site's
// index is a reconcile-time spec.index, unknown at push).
func PushSite(ctx context.Context, ref, dir string) (digest string, err error)

// ResolveSite resolves ref (a tag or a digest) to its manifest digest, asserting SiteArtifactType.
// A function artifact ⇒ fault.Invalid; an absent ref ⇒ fault.NotFound.
func ResolveSite(ctx context.Context, ref string) (digest string, err error)

// PullSite fetches the digest-pinned site artifact and untars its bundle layer into dir using the
// existing traversal-safe untar (untarBundle + safeJoin: a "../"/absolute/symlink-escape entry is
// refused).
func PullSite(ctx context.Context, ref, digest, dir string) error
```

### Site reconciler (`internal/site`) — an internal component

```go
// BucketResolver resolves a (namespace, bucket) to its per-namespace blob view — the SAME s3BucketFor
// the S3 frontend and the ADR-0120 static handler use, so one substrate serves all three.
type BucketResolver func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool)

type Deps struct {
	Store   store.Store
	Buckets BucketResolver
	Logger  *slog.Logger
}

// Reconciler drives a Site to its desired state (controller.Reconciler).
type Reconciler struct { /* store, buckets, logger */ }

func New(d Deps) *Reconciler

// Reconcile, in order: (1) reads the owned Route (if present and owned) and takes its bundle rule's
// prefix as the CURRENT serving digest — the durable record; (2) resolves spec.image → a digest only
// when status.observedGeneration != metadata.generation, else keeps the serving digest; (3) adopts or
// creates the Bucket (PrefixOwned if the site prefix is already declared with an owner); (4) if the
// target digest's index object is absent, unpacks the bundle under <prefix>/<digest-slug>/ over
// blob.Bucket — every non-index entry first, the index object LAST; an index already present means
// complete, nothing is re-uploaded; (5) asserts the index, then materializes/updates the owned Route
// (RouteNotOwned if foreign) with the bundle rule at the target digest; (6) reads the Route's Ready
// condition — pending (absent or stale ObservedGeneration) ⇒ NotReady RouteNotReady + RequeueAfter 2s;
// (7) publishes a status DERIVED from the Route, advancing status.observedGeneration only once this
// generation's digest is the one programmed on the Route. A failure before (5) leaves the Route —
// hence the PREVIOUS digest — serving, and the generation unobserved so the next reconcile retries.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error)
```

### Dependencies & I/O

| Consumes | Produces |
|---|---|
| `store.Store` (Get the `Site`; Create/Update the owned `Bucket` + `Route`; Watch drives reconcile) · `blob.Bucket` via the ADR-0080 `s3BucketFor` per-namespace view · `internal/artifact` (`ResolveSite`/`PullSite`) · the `controller.Reconciler` seam | a Ready/NotReady `Site` with `status.{digest,servingPrefix,objects,bytes}` · an owned `Bucket` (site prefix ownerless + declared prefixes) · an owned `Route` (bundle rule + data-mount rules) · bundle objects under `<prefix>/<digest-slug>/` |

**Deps:** none — `oras-go`, the blob port, and the bundle tar are already in `go.mod`.

## Implementation plan

**Files:**
- `api/types/v1alpha1/site.go` (+ test) — `Site`/`SiteSpec`/`SiteBucket`/`SiteIngress`/`SiteRule`/`SiteStatus` + `Validate` + huma tags.
- `api/types/v1alpha1/metadata.go` — `KindSite` const, `NewObject`, `AllKinds`.
- `internal/artifact/site.go` (+ test) — `SiteArtifactType`, `PushSite`, `ResolveSite`, `PullSite`; `bundle.go` refactored so the deterministic packer behind `PackBundle` has an entry-less path (`PackBundle` keeps its entry gate) and `untarBundle`/`safeJoin` are reused as-is.
- `internal/site/reconcile.go` (+ test) — the reconciler in the `Reconcile` contract's order: read Route (serving digest) → resolve once per generation → adopt/create Bucket → unpack (index last) → index gate → materialize Route → pending/ready check → derived status.
- `internal/site/materialize.go` — the `Site` → (`Bucket`, `Route`) compilation, incl. the two rule shapes and `OwnerReference` stamping (`Route` always; `Bucket` only for `delete`, therefore never in V1), and the inverse: serving digest ← the Route's bundle rule prefix.
- `internal/controlplane/admission/site.go` (+ test) — `NewSitePrefixImmutableAdmission`, registered with the existing Validating admissions.
- `cmd/funcdctl/cli.go` — `push --site`.
- `pkg/funcd/funcd.go` — build the reconciler from the existing `s3BucketFor` + store and `ctrl.Register(v1.KindSite.GVK(), siteReconciler)`.
- `internal/controlplane/handlers.go` (+ the REST route table) — the five `Site` CRUD handlers, mirroring the `KindBucket`/`KindRoute` blocks.
- `pkg/sdk/kinds.go` — the `v1.KindSite: {"sites", true}` entry; without it `funcdctl apply`/`get` cannot reach the kind.
- Regenerate OpenAPI (`just generate`).

**Test plan** — one named test per Scenario:
- `api/types/v1alpha1` validate matrix: `delete-cascade-rejected`, `public-and-authenticated-rejected`, prefix/index/rule rules, missing `ingress`, duplicate and `/` rule rejection.
- `internal/controlplane/admission` unit: `prefix-is-immutable` — an Update changing `spec.prefix` ⇒ `Invalid` whether or not `Old.Status.Digest` is set; a non-prefix Update ⇒ admitted.
- `internal/artifact` unit: round-trip `PushSite`/`ResolveSite`/`PullSite`; `ResolveSite` rejects a function artifact; `traversal-safe-unpack`; `PackBundle` still rejects a missing entry (the refactor is behaviour-preserving).
- `internal/site` reconciler unit over a memory store + memory blob: `owned-bucket-and-route-materialized`,
  `redeploy-swaps-atomically`, `partial-unpack-recovers` (pre-seed the digest prefix with some objects and no
  index; the Route must not swap before the index is written last), `not-ready-until-index-present` (drive
  the redeploy through a status-wiping Update, as an apply would), `tag-resolved-once-per-generation` (a
  fake resolver that moves the tag between reconciles), `rollback-to-previous-digest`,
  `foreign-route-not-adopted`, `adopted-bucket-prefixes-preserved`, `adopted-prefix-with-owner-rejected`,
  `retain-stamps-no-owner-reference` (assert the materialized `Bucket` has **empty** `OwnerReferences` and
  the `Route` has one), `site-not-ready-when-route-not-ready` (a freshly written Route ⇒ `RouteNotReady` +
  `RequeueAfter`; an empty host in an `explicit` namespace ⇒ `RouteNotReady`, no requeue).
  These assert **stored state**, not HTTP — the reconciler has no serving surface.
- `bundle-prefix-has-no-writer` is an **e2e** assertion, not a unit one: a `PutObject` to the site prefix
  through the real S3 frontend (both an external SigV4 `Identity` and a bound Function) must be
  `403 Forbidden` — asserting the prefix has no owner is necessary but does not prove the scenario.
- **Go in-process e2e** over `pkg/funcd` (real `funcd.New`, memory store + memory blob): push a bundle to
  an `oci-layout://` target, apply a `Site`, then `site-materializes-and-serves`,
  `data-mount-serves-sibling-prefix`, `data-mount-does-not-spa-fallback`, `redeploy-swaps-atomically`.
- **Venom containerd lane** — extend an example lane: `funcdctl push --site`, apply the `Site`,
  `curl -H "Host: …" /` → the index; an asset → correct content-type + `ETag`; `/data/<file>` → the
  object; `/data/missing` → `404`; redeploy → the new build served with no 5xx window.

**Definition of done:** every scenario has a named passing test; `go build ./... && go test ./... && go tool golangci-lint run ./... && go mod verify` green; the in-process e2e + the Venom lane green; OpenAPI regenerated; F103 row `→ reviewing`; blueprint synced if the decision refines it; no identity/path leak.

## Review checklist

- [ ] `Site*` types match the Contracts; `Validate` enforces the semantic rules, **rejects `deletion: delete`**, **rejects `public` + `auth.mode: authenticated`**, and requires `ingress`; huma tags carry field shape; no `any` in exported signatures.
- [ ] The bundle materializes under `<prefix>/<digest-slug>/`; every non-index entry is written **before** the index object, and the `Route` is re-programmed **only after** the index is asserted present.
- [ ] `status.digest`/`status.servingPrefix` are **derived from the owned `Route`'s bundle rule** on every reconcile; nothing reads `status` back as durable state; `spec.image` is re-resolved only when `status.observedGeneration != metadata.generation`.
- [ ] A failed or index-less materialization leaves the **previous** digest serving; the owned `Route` is not re-programmed and the `Site` is `NotReady` with the right reason — including when the redeploy arrived as a status-wiping Update.
- [ ] An already-complete digest (index present) is not re-uploaded; a digest prefix with objects but **no index** is re-uploaded; reverting `spec.image` to a prior digest serves it with no re-upload.
- [ ] The materialized site `BucketPrefix` carries **no owner**; a `PutObject` to it through the **real S3 frontend** is `403` for both an external `Identity` and a bound Function (absent a writer-role `RolesAssignment`). Declared `spec.bucket.prefixes` pass through verbatim.
- [ ] The `Bucket` carries an `OwnerReference` **only** when `deletion: delete` — so a V1 `Site` (retain-only) leaves `OwnerReferences` **empty**, and an adopted `Bucket` is never stamped. The `Route` always carries one.
- [ ] The reconciler only **adds** `spec.prefixes` entries to an adopted `Bucket` — it never removes or rewrites an entry it did not add; an adopted `Bucket` already declaring `spec.prefix` **with an owner** ⇒ `PrefixOwned`, nothing touched.
- [ ] A same-named `Route` without an `OwnerReference{Kind: Site, Name, UID}` to this `Site` is never adopted or mutated (`RouteNotOwned`). There is no Bucket ownership check (adoption is permitted by design).
- [ ] A pending owned `Route` (Ready condition absent / stale `ObservedGeneration`) ⇒ `RouteNotReady` + `RequeueAfter`; a current `NotReady` (`HostRequired` / `RouteConflict` / any) ⇒ `RouteNotReady` — a `Site` is never `Ready` while unreachable.
- [ ] `spec.prefix` cannot be changed on any Update — enforced by the `site-prefix-immutable` **Update admission** (not `Validate`, which never sees `Old`; not conditioned on `Old.Status`), and registered with the other Validating admissions.
- [ ] Adopting a pre-existing `Bucket` leaves every prefix entry the `Site` did not declare byte-identical.
- [ ] Nothing in the implementation attempts to reclaim an owned object on `Site` delete — the collector is out of scope; the `Route` is expected to survive.
- [ ] The owned `Route` uses ADR-0120's existing `backend.static.{bucket,prefix}` arm — **no new backend arm, no edit to `StaticBackend` or the static handler**.
- [ ] `SiteRule` has no `spa`/`index` field, so data-mount rules cannot carry them; a data-mount miss is `404` by construction, never the app shell. Only the bundle rule carries `index`/`spa`.
- [ ] `ingress.public` sets `static.public` on each backend, not `spec.auth.mode`; ADR-0110's `HostRequired`/`RouteConflict` still apply to the owned `Route`.
- [ ] `SiteArtifactType` is distinct from the function artifact type; `ResolveSite` rejects a function artifact; the untar refuses traversal entries.
- [ ] OpenAPI regenerated; F103 row advanced; no identity/path leak.

## Consequences

- **(+)** A static web app is one manifest — bundle, bucket, exposure, lifecycle — and `Ready` finally means "the site is actually servable", closing the empty-prefix-reports-green hole.
- **(+)** Atomic swap and instant rollback come free from the digest-scoped prefix; a broken deploy leaves the previous build serving instead of taking the site down.
- **(+)** The bundle prefix is unwritable by any function or S3 client by default — stricter than the F82 status quo, where whoever owned the prefix could overwrite the site; only a deliberate writer-role `RolesAssignment` (ADR-0136) could open it.
- **(+)** No new dependency and **no change to ADR-0120**; the whole serving path is reused verbatim.
- **(+)** Crash-safe by construction: the index is written last, the `Route` is the durable record, and `status` is derived — a restart mid-deploy converges without serving a partial site (blueprint crash-only).
- **(−)** Superseded digest prefixes accumulate until an operator drains them (bounded by deploy frequency × bundle size).
- **(−)** One `Site` exposes exactly one `Route`, so multi-host serving needs the follow-on.
- **(−)** **Deleting a `Site` leaves its owned `Route` behind** — no collector exists (Decision §5; the gap equally affects `Workflow.spec.kv[].deletion: delete` and `Identity`'s owned `Secret`, whose code comments claim a cascade — carded as a platform gap). Until the collector ADR lands, removing a `Site` is a two-step operation for the operator.
- **(−)** `spec.prefix` is immutable, so relocating a site within its bucket means a new `Site`.
- **(−)** Readiness of the owned `Route` is polled (`RequeueAfter` 2 s) — the controller has no owner-watch; a `Route` regression after the `Site` went `Ready` is only seen at the next `Site` reconcile.
- **(−/risk)** The reconciler creates and mutates two other kinds — a new surface. Mitigated by the `Route` ownership check, by bounding `Bucket` mutation to *adding* prefixes only (`PrefixOwned` on collision), and by keeping `delete` out of V1.
- **(risk)** The unpack reads bundle entries into memory to `Put` them (the `blob.Bucket` port is `Put([]byte)`); a very large bundle is a memory spike. Acceptable for a prebuilt web app; a streaming put rides the existing `blob.RangeReader` follow-on.

## Open questions

- **Multi-host / hand-written exposure of a `Site`** — whether to add `backend.static.site` after all. Answered by a follow-on ADR if the need appears.
- **Retention of superseded digest prefixes** — answered by a platform-wide retention ADR covering `Revision` and `Site` together.
- **`deletion: delete` cascade ordering** — answered by the follow-on that ships drain-then-delete against `NewBucketDeletionProtectionAdmission`.
- **Strong digest-sourced `ETag` for site assets** — inherited from ADR-0120's open question (needs a content digest on `blob.Attributes`); unchanged by this ADR.

## References

- [FEAT-0003/F103](../feat/0003-feat-data-platform.md) · [ADR-0120](0120-static-asset-serving-route.md) (the static `Route` backend reused unchanged) · [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (the `Route` materialized) · [ADR-0089](0089-python-function-dependency-bundling.md) (deterministic tar+gzip bundle + traversal-safe untar) · [ADR-0031](0031-oci-artifact-distribution-oras.md) · [ADR-0035](0035-artifact-digest-resolution-at-revision.md) (tag → digest at stamp time) · [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (per-namespace `Bucket` view + single-writer rule) · [ADR-0094](0094-workflow-engine-core.md) (`Workflow.spec.kv` inline ownership) · [ADR-0121](0121-declarative-referential-integrity-admission.md) (reconcile-time cross-resource existence) · [ADR-0136](0136-roles-and-role-assignments.md) (`writers` set) · [ADR-0091](0091-function-catalog-consumer-binding.md) (requeue-until-Ready).
- Prior art: Cloudflare Pages / Netlify immutable deploys with atomic pointer swap; Kubernetes `ownerReferences` cascade semantics.
