# ADR-0035: Resolve the artifact tag → digest at Revision stamp (no manual pinning)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0172](0172-revision-integrity.md) (2026-10-05) — Decision 3: re-resolve the tag when no Revision exists for the generation.
- **Date**: 2026-06-15 (**Implemented 2026-06-16** — review pass: deploy-without-digest works (resolved+pinned in the immutable Revision, spec stays digest-free), tag-move-does-not-drift, explicit-digest honored, unresolvable→Failed; B1/M1/M2 folded + verified; no new dependency. **Accepted 2026-06-15** after judge pass — no Blockers left open. Folded **B1**
  (state the materialize mechanism — copy the pinned digest onto `fn.Spec.Artifact.Digest` in memory, never
  persisted, so the frozen `Materialize(ctx,fn)` reads it), **M1** (resolve only on the Revision-create path;
  an existing Revision never re-resolves — what makes `tag-move-does-not-drift` hold), **M2** (OCI uri + empty
  digest + no resolver → fail closed `ArtifactUnresolved`, never an empty-digest pull), + Minors (TOCTOU note;
  `ArtifactUnresolved` is a reason not a new condition; no admission change needed; the ADR-0034 scenario is
  frozen — only the journey test + demo change). Decision: resolve tag→digest at Revision stamp, pin into the
  immutable Revision. No new dependency.)
- **Deciders**: green-0-rabbit
- **Tags**: artifact, oci, digest, revision, immutability, F13
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (source-artifact deploy — making the digest pin
  automatic, the half ADR-0031 left to the user)
- **Relates to / refines**: [ADR-0031](0031-oci-artifact-distribution-oras.md) (**changes its "user supplies
  the digest / empty digest rejected" rule** — the rest of oras push/pull stands) ·
  [ADR-0020](0020-function-contract-lifecycle.md) (**refines** `ensureRevision` to pin the resolved digest) ·
  [ADR-0030](0030-function-execution-runtime-shim-node.md) (the `Materializer` that consumes the pinned digest)

## Context & Need

ADR-0031 makes the **digest the authority** for what runs — but requires the **user to pin it by hand**
(`artifact.digest` from the `funcdcli push` output; an empty digest is rejected). That is friction and a
copy-paste footgun. The well-precedented fix (Knative resolves an image tag → digest when it stamps a
Revision; Kyverno/Flux mutate the digest at admission) is to **resolve the tag → digest automatically and pin
it into the immutable Revision** — the user writes only a ref/tag, the platform makes it content-addressed.
"Validated = shipped" is preserved; the digest stays the authority — it is now *derived*, not *typed*.

## Scenarios

- **scenario: deploy-without-digest** — *given* a Function whose `artifact.uri` is an OCI ref with **no
  digest**, *when* it is applied, *then* the reconciler resolves the ref → digest, pins it into the immutable
  `Revision.spec.artifact.digest`, and the function runs those exact bytes.
- **scenario: tag-move-does-not-drift** — *given* a Function already stamped to `Revision N` at digest `D`,
  *when* the tag is re-pushed to new bytes `D'`, *then* `Revision N` still resolves `D` (a new generation is
  needed to adopt `D'`) — the running revision never silently changes.
- **scenario: explicit-digest-honored** — *given* a Function that **does** pin `artifact.digest`, *then* it is
  used as-is (no resolution) — the explicit-pin path still works.
- **scenario: unresolvable-ref-fails** — *given* an `artifact.uri` that does not exist in the target, *then*
  the Function goes `Failed` with a clear condition (never a silent or wrong deploy).

## Scope

- **In**: make `artifact.digest` **optional** in the Function spec; an `ArtifactResolver` seam
  (`Resolve(uri) → digest`) implemented by the oras driver; resolve-and-pin in `ensureRevision` (ADR-0020);
  materialize from the **Revision's** pinned digest. file:// dev artifacts (no digest) are unaffected.
- **Out**: signing/provenance (V2); tag-resolution policy knobs (always-resolve in V1, à la Knative's
  `skip-tag-resolving` — a later toggle); changing `funcdcli push` (it still prints `ref@digest`, now optional
  to copy).

## Constraints & Decision drivers

- **Digest stays the authority + immutable Revisions** — only the *source* of the digest changes (resolved,
  not typed); ADR-0031's integrity/caching and ADR-0020's immutability are preserved.
- **Refine, don't rewrite** — a new ADR changes ADR-0031's digest rule + ADR-0020's stamping; both stay otherwise frozen.
- **No new dependency** — oras already resolves a ref → descriptor digest (`Store.Resolve` / `Repository.Resolve`).
- **Backward compatible** — an explicitly pinned digest is still honored.

## Decision

1. **`artifact.digest` becomes optional.** The user supplies `artifact.uri` (an OCI ref, optionally tagged);
   `digest` may be empty.
2. **`ArtifactResolver` seam** — `Resolve(ctx, uri) (digest string, err error)`, implemented by the oras
   driver (`OrasMaterializer`), wired wherever the materializer is. The file driver needs none (file:// has no digest).
3. **Resolve-and-pin, only on the Revision-create path.** `ensureRevision` (ADR-0020) is already idempotent —
   it stamps one Revision per `fn.Generation` and early-returns if one exists. Resolution happens **only on
   that create path**: when no Revision exists for the generation and the spec digest is empty, resolve
   `uri → digest` once and stamp it into the immutable `Revision.spec.artifact.digest`. **A reconcile that
   finds an existing Revision never re-resolves** — that ordering is exactly what makes `tag-move-does-not-drift`
   hold. An explicit spec digest is copied through unchanged. For an **OCI** uri with an empty digest and
   **no resolver configured**, the function **fails closed** → `Phase=Failed`, reason `ArtifactUnresolved`
   (a new reason on the existing Failed write-back, parallel to `ShapeInvalid` — not a new condition type),
   never an empty-digest pull. file:// has no digest and needs no resolver.
4. **Materialize the pinned digest (no frozen-interface change).** After stamping, the reconciler sets
   `fn.Spec.Artifact.Digest = rev.Spec.Artifact.Digest` **in memory for this reconcile** — it is *not* written
   back to the Function via `store.Update` (only `Status.CurrentRevision` is) — so the frozen
   `Materializer.Materialize(ctx, fn)` (ADR-0030) reads the pinned digest with no signature change. The
   `OrasMaterializer` keeps pulling **by that digest** and content-verifying (ADR-0031); `Pull`'s empty-digest
   rejection stays as the integrity backstop. Only the *source* of the digest moved (spec → Revision).

## Contracts

```go
// internal/function — the resolver seam + the optional dep (nil → no resolution, e.g. file:// dev).
type ArtifactResolver interface {
    Resolve(ctx context.Context, uri string) (digest string, err error)
}
// Deps gains: Resolver ArtifactResolver

// internal/artifact — the oras driver implements it (Store.Resolve / Repository.Resolve → descriptor digest).
func (m *OrasMaterializer) Resolve(ctx context.Context, uri string) (string, error)
var _ function.ArtifactResolver = (*OrasMaterializer)(nil)

// api/types/v1alpha1 — ArtifactRef.Digest is already `omitempty`; only the "required" rule changes.
```

`ensureRevision` sets `rev.Spec.Artifact = {URI, Digest: pinned}` (pinned = spec digest if set, else
`Resolver.Resolve(uri)`) on the **create path only**; then the reconciler copies `pinned` onto
`fn.Spec.Artifact.Digest` **in memory** (never persisted) so the unchanged `Materialize(ctx, fn)` reads it.
**No admission/validator change is needed** — `ArtifactRef.Digest` is already `omitempty` and nothing at apply
time requires it (only `internal/artifact` did, on the pull path).

### Dependencies & I/O
| | Item |
|---|---|
| Adds (lib) | none (reuses oras `Resolve`) |
| Changes | ADR-0031 "empty digest rejected" → resolved; ADR-0020 `ensureRevision` pins the resolved digest |
| Consumes | the configured artifact target (oci layout / registry) at stamp time |

## Implementation plan

1. **`internal/artifact`** — add `OrasMaterializer.Resolve` (oras `Resolve` → `descriptor.Digest.String()`);
   drop the "empty digest rejected" precondition in favor of resolve-then-pull. `var _ function.ArtifactResolver`.
2. **`internal/function`** — `ArtifactResolver` in `Deps`; in `ensureRevision`, resolve+pin when the spec
   digest is empty and a resolver is set; on resolve error → `Failed` + `ArtifactUnresolved`. Materialize the
   current Revision's artifact.
3. **`pkg/funcd`** — wire the `OrasMaterializer` as the `Resolver` when artifact-store mode is on.
4. **Tests** — `deploy-without-digest` + `explicit-digest-honored` + `unresolvable-ref-fails` (pure-Go, local
   OCI layout); `tag-move-does-not-drift` (re-push, assert the stamped Revision's digest is unchanged);
   node-gated e2e: the journey + demo deploy a `function.yaml` with **no digest**.
5. **Propagate** — sync blueprint's artifact line (digest auto-pinned at Revision); update the demo
   `function.yaml`/`demo.yaml` (drop the digest) + the journey **test** + demo script (drop the push-output
   copy). ADR-0034's scenario text is frozen — its intent (deploy → Ready) is unchanged whether or not a
   digest is typed. ADR-0031 stays frozen (refined here).
6. **DoD** — `just ci` green; a no-digest Function deploys + runs; explicit digest still honored; a tag move
   doesn't drift a stamped Revision; no new dependency; no leak.

## Review checklist

- [ ] **No-digest deploy works** (`deploy-without-digest`): apply with only `artifact.uri` → resolved + pinned
      in the Revision → runs.
- [ ] **Immutability holds** (`tag-move-does-not-drift`): re-pushing the tag does not change a stamped Revision.
- [ ] **Explicit pin honored** (`explicit-digest-honored`); **unresolvable → Failed** (`unresolvable-ref-fails`).
- [ ] oras `Resolve` reused (no new dep); blueprint + demo synced; ADR-0002 conventions; no leak.

## Consequences

- (+) **No manual digest pinning** — `function.yaml` is just `artifact: { uri: …:v1 }`; the platform pins the
      digest into the immutable Revision (the Knative model). Same integrity + reproducibility guarantees.
- (+) **Tag-friendly, drift-free** — users think in tags; revisions stay content-addressed.
- (−) **A resolve step at apply** — one registry/layout round-trip per new generation; cached by the Revision.
- (note) **No resolve/pull race**: resolve→pin→pull-*by-digest* means a tag move after the pin can't change
  what is pulled — the pinned digest closes the window.
- (note) file:// dev artifacts are unchanged (no digest); resolution applies to OCI refs only.

## Temporary workarounds

| Workaround | Why | Exit |
|---|---|---|
| V1 always resolves an unpinned tag (no skip policy) | keep it simple | a `skip-tag-resolving`-style toggle later (à la Knative) if a registry should be trusted to be immutable |

## Alternatives considered

- **Resolve at pull time, record digest only in status** (the vanilla-k8s kubelet model) — rejected: a tag
  could drift across re-pulls; pinning into the immutable Revision is stronger and matches Knative.
- **Keep the digest mandatory** (status quo) — rejected: manual pinning is the friction this removes.
- **Mutating-admission digest pin** (Kyverno-style, at the API server) — deferred: the Revision stamp is funcd's
  natural one-place-to-pin and already immutable; an admission hook is redundant for V1.

## Open questions

| Question | Answered in |
|---|---|
| A per-registry "skip tag resolving" trust toggle? | a follow-up if needed (Knative precedent) |
| Re-resolve / auto-rollout when a tag moves (image-automation)? | V2 (rollout mechanics) |

## References

- [ADR-0031](0031-oci-artifact-distribution-oras.md) · [ADR-0020](0020-function-contract-lifecycle.md) — refined here.
- Knative Serving tag→digest resolution at Revision creation; Kyverno `mutateDigest` — the precedents.
