# ADR-0097: Flatten the Function artifact ref to `spec.image` + `spec.imageDigest`

- **Status**: Implemented (2026-07-06)
- **Date**: 2026-07-06 (decided by green-0-rabbit — direct direction: "change the function `artefact.uri` by `image`", shape = top-level `spec.image`)
- **Deciders**: green-0-rabbit
- **Tags**: function, artifact, resource-model, refactor, naming
- **Realizes**: FEAT-0000/F13 (Function contract & lifecycle — this renames its artifact field; no behavior change)
- **Supersedes**: the `ArtifactRef` type + the `spec.artifact` field of [ADR-0020](0020-function-contract-lifecycle.md) only (the field is renamed/flattened below). The rest of ADR-0020 — the CloudEvents handler contract, the runtime/handler shape gate, the Revision lifecycle — **stands unchanged** (relates-to). Scoped supersession: ADR-0020 keeps status `Implemented` and receives a one-line back-link note; it is **not** given a terminal `Superseded by` status.
- **Relates to**: [ADR-0031](0031-oci-artifact-distribution-oras.md) (`funcdctl push` prints the ref the field carries), [ADR-0035](0035-artifact-digest-resolution-at-revision.md) (the digest pinned at Revision — now `imageDigest`), [ADR-0096](0096-engine-native-builtin-steps.md) (a workflow step is `function: { image: … }` — this aligns the Function resource to that same word).

## Context & Need

A Function's source artifact was `spec.artifact.uri` (the ref) + `spec.artifact.digest` (system-pinned at
Revision), an `ArtifactRef` struct (ADR-0020). ADR-0096 then named a workflow step's source `function: {
image: … }`. The platform now has **two words for one concept** — a step's source is `image`, a
Function's source is `artifact.uri` — for the same OCI ref. Materialization even copies one into the
other (`fn.Spec.Artifact.URI = st.Function.Image`). Flatten the Function to the same word: `spec.image`
(the ref, tag at the string end) + `spec.imageDigest` (the pinned digest). Purely a rename — no behavior,
resolution, or pinning change.

Callers: function authors (write `spec.image` in a manifest), `funcdctl push` (prints the ref they
paste), the materializer + artifact resolver + shim (read it), and the Revision snapshot.

## Scenarios

- `image-field-carries-the-ref` — Given a Function `spec.image: oci-layout:///…:counter`, Then it
  materializes/pulls/serves exactly as `spec.artifact.uri` did (behavior unchanged; the existing
  function e2es pass under the new field).
- `image-digest-pinned-at-revision` — Given a stamped Revision, Then the pinned digest is on
  `spec.imageDigest` (Function + Revision snapshot), read by the resolver as before (ADR-0035).
- `workflow-step-image-materializes-to-spec-image` — Given a workflow `function: { image: X }`, Then the
  owned Function's `spec.image` is `X` (materializer writes the flattened field; the workflow e2e passes).

## Scope

**In:** rename `FunctionSpec.Artifact ArtifactRef` → `FunctionSpec.Image string` + `FunctionSpec.ImageDigest
string`; the same flatten on `RevisionSpec`; delete the `ArtifactRef` type; migrate every Go read/write
site, every example manifest (`artifact: { uri: X }` → `image: X`), the `funcdctl push` help text, and the
OpenAPI spec. **Out:** any behavior/resolution/pinning change (none — this is a rename); a wire-compat
migration (the resource is unreleased — no stored Functions to migrate); the workflow `FunctionStep.Image`
(already `image`, ADR-0096 — untouched).

## Constraints & Decision drivers

- **One word for one concept.** A source OCI ref is `image` everywhere (Function spec + workflow step).
- **No behavior change.** Resolution (ADR-0031), digest pinning at Revision (ADR-0035), and the shape gate
  (ADR-0020) are byte-for-byte the same logic on a renamed field.
- **Unreleased ⇒ no migration burden.** No stored Function manifests exist; the rename is free.

## Alternatives considered

- **Rename inside the wrapper (`spec.artifact.image`).** Rejected: keeps the `artifact` wrapper the
  workflow step doesn't have, so the two sources still don't *look* identical. Flat `spec.image` matches
  `function: { image }` exactly.
- **Keep `spec.artifact.uri`, alias `image`.** Rejected: two names for one field is the confusion we're
  removing; an alias entrenches it.
- **Leave it.** Rejected: the divergence is a standing readability tax and a materializer field-copy that
  reads like a type mismatch.

## Decision

`FunctionSpec` and `RevisionSpec` drop `Artifact ArtifactRef` and gain:

```go
// Image is the source artifact OCI ref (oci-layout:// · file:// · a bare registry ref; tag at the string
// end), pulled + served by the runtime — F13/ADR-0020, renamed by ADR-0097. Was spec.artifact.uri.
Image string `json:"image,omitempty"`
// ImageDigest is the content digest pinned into the stamped Revision ("what was validated ships"),
// system-set at materialization (ADR-0035). Was spec.artifact.digest.
ImageDigest string `json:"imageDigest,omitempty" pattern:"^sha256:[a-f0-9]{64}$"`
```

The `ArtifactRef` type is deleted. Every `.Artifact.URI` → `.Image`, every `.Artifact.Digest` →
`.ImageDigest`; the shape gate's "artifact non-empty" check reads `spec.image`. Manifests write
`spec.image:` (no `artifact:` wrapper). `funcdctl push` still prints `<ref>@<digest>`; its help text points
at `spec.image`. The OpenAPI spec is regenerated. No resolver, pinning, or validation logic changes.

## Temporary workarounds

None.

## Contracts

| Was | Now |
|---|---|
| `FunctionSpec.Artifact.URI` / `RevisionSpec.Artifact.URI` | `.Image` |
| `FunctionSpec.Artifact.Digest` / `RevisionSpec.Artifact.Digest` | `.ImageDigest` |
| `type ArtifactRef struct { URI, Digest string }` | deleted |
| manifest `spec.artifact: { uri: X }` | manifest `spec.image: X` |
| `Function.spec.artifact` (push help text) | `Function.spec.image` |

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| ADR-0020 Function contract (this renames one field) | `FunctionSpec.Image` / `.ImageDigest` (+ same on `RevisionSpec`) |
| ADR-0031 push / ADR-0035 digest-at-Revision (unchanged logic) | no new type, no new dependency |

## Implementation plan

Files: `api/types/v1alpha1/function.go` (flatten `FunctionSpec`, delete `ArtifactRef`, huma tag on
`ImageDigest`), `api/types/v1alpha1/revision.go` (flatten `RevisionSpec`), then the compiler-driven sweep
across `internal/function/*`, `internal/artifact/*`, `internal/workflow/reconcile_workflow.go`,
`internal/testkit/bench/*`, `cmd/funcdctl/cli.go` (help text), and all `*_test.go` +
`pkg/funcd`/`tests/*` e2e using `.Artifact.URI`/`.Artifact.Digest`/`ArtifactRef{…}`; the 14 example
`*.yaml` manifests (`artifact: { uri: X }` → `image: X`); `api/openapi/funcd.v1alpha1.yaml` (regenerate).
No `go.mod` change. Definition of done: `go build/lint/test/mod` green (the pre-existing
`TestPythonPoolSmoke` env failure aside); the function + workflow e2es pass on the renamed field; ADR-0020
back-linked; FEAT-0000/F13 notes the rename; no identity leak.

## Review checklist

- [ ] `FunctionSpec`/`RevisionSpec` carry `Image` + `ImageDigest`; `ArtifactRef` is deleted; no
      `.Artifact.` reference remains (grep clean).
- [ ] `ImageDigest` keeps the `^sha256:[a-f0-9]{64}$` pattern; digest pinning at Revision unchanged.
- [ ] Every example manifest uses `spec.image:`; `funcdctl push` help points at `spec.image`.
- [ ] OpenAPI regenerated (no `ArtifactRef`/`artifact` schema; `image`/`imageDigest` present).
- [ ] Function + workflow e2es pass; behavior identical (rename only); ADR-0020 back-linked, its other
      contracts untouched.

## Consequences

- **One vocabulary**: a source OCI ref is `image` on both a Function and a workflow step; the materializer
  copies `st.Function.Image → fn.Spec.Image` (same word, no apparent type hop).
- **Slightly flatter resource**: one less nesting level (`spec.image` vs `spec.artifact.uri`).
- **Refactor, not a decision shift**: no behavior, resolution, or pinning change — a rename of an
  unreleased contract, so zero migration cost.

## Open questions

None.

## References

- [ADR-0020](0020-function-contract-lifecycle.md) — Function contract (artifact field renamed here).
- [ADR-0031](0031-oci-artifact-distribution-oras.md) / [ADR-0035](0035-artifact-digest-resolution-at-revision.md) — push + digest-at-Revision (unchanged).
- [ADR-0096](0096-engine-native-builtin-steps.md) — the workflow `function: { image }` idiom this aligns to.
