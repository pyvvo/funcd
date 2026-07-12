# ADR-0122: funcdctl.yaml — the client push/dev config for native contract & type codegen

- **Status**: Implemented (2026-07-11 — the reduced push/dev-config scope: `pkg/sdk` Manifest{Runtime,Handler,Bindings,
  Contract} + LoadManifest/ContractSides/GenerateTypes, `funcdctl push` reading funcdctl.yaml + `funcdctl types`, no
  Compile()/apply; green on the four sub-checks, block-style example manifests. Review:
  docs/reviews/adr-0122-implementation-claude-opus-4-8.md.)
- **Date**: 2026-07-11 (**Accepted 2026-07-11** — adr-judge split the runtime-compile half into ADR-0123 (this ADR
  **depends on** it). **Revised 2026-07-11 (same session, by decider direction — override):** re-scoped `funcdctl.yaml`
  from *a manifest that compiles to the `Function` CRD* down to *the client-side push/dev config* — the `wrangler.toml`
  analogue that drives `funcdctl push` (bake the contract) and, later, `funcdctl dev` (run the function locally). The
  `Compile()`→`Function` CRD and `funcdctl apply -f funcdctl.yaml` are **removed** (deploy stays the hand-written
  `Function` CRD); `scaling`/`pooling`/`name`/`namespace`/`image` are **dropped** (deploy knobs, not authoring).
  `bindings` stay — the function needs them to *run* (`funcdctl dev`) and to *type* (`funcdctl types`).)
- **Deciders**: green-0-rabbit
- **Tags**: dx, tooling, contracts, funcdctl
- **Realizes**: FEAT-0001/F87
- **Depends on**: [ADR-0123](0123-runtime-compiled-io-validators.md) — this config bakes a **schema-only** contract; the
  worker validates it via ADR-0123's runtime-compiled validator (that is why no push-time language toolchain is needed).
- **Relates to**: [ADR-0058](0058-contract-codegen-from-code-types.md) (contract codegen — source & direction changed) ·
  [ADR-0059](0059-contract-as-oci-metadata.md) (contract-as-OCI-metadata — mechanism unchanged) ·
  [ADR-0090](0090-mandatory-single-io-schema.md) (mandatory single I/O schema — source-resolution refined) ·
  [ADR-0089](0089-python-function-dependency-bundling.md) (bundle push) ·
  [ADR-0024](0024-funcdcli-and-sdk.md)/[ADR-0042](0042-cobra-cli-framework.md) (funcdctl)

## Context & Need

Today a function's I/O contract is **derived from code types** (`FuncInput`/`FuncOutput` → JSON Schema via
pydantic/typia) by the author's **language toolchain running at `funcdctl push`** (ADR-0058). On a platform whose CLI is
a **single Go binary** (blueprint §funcdctl) and whose runtime substrate is **containerd, not docker** (ADR-0052), that
forces a language runtime — or a docker-shaped container/plugin — onto every push host.

**Purpose.** Make `funcdctl` self-sufficient with a small colocated **`funcdctl.yaml`** — the **client-side push/dev
config**, the analogue of Cloudflare's `wrangler.toml`. It declares only what the *client tooling* needs: the
`runtime`, the `handler`, the `bindings` the code uses, and the inline I/O `contract`. From it `funcdctl` (pure Go)
gates + bakes a **schema-only** contract into the artifact and generates code types — with **no language plugin,
container, or docker**. (The worker turns the baked schema into a live validator at warm-up — ADR-0123.) Who calls it:
a function author running `funcdctl push` / `funcdctl types` (and, later, `funcdctl dev`).

**What it is *not*.** `funcdctl.yaml` is **not** the deploy manifest and does **not** compile to the `Function` CRD.
Deploy concerns — the resource `name`/`namespace`, `scaling`, pooling, placement — stay on the hand-written `Function`
resource, applied with `funcdctl apply -f function.yaml` as today. `funcdctl.yaml` sits **beside** that CRD, owning the
client push/dev loop, not the cluster's desired state.

## Scenarios

- **scenario: push-from-manifest** — Given a directory with `funcdctl.yaml` declaring `runtime`/`handler`/`bindings`/
  `contract`, When `funcdctl push <dir> <ref>`, Then funcdctl gates each schema side (`contract.Check`), bakes the
  `{dialect,input,output}` contract blob into the artifact, records the runtime annotation, and prints `ref@digest` —
  **with no language toolchain invoked**.
- **scenario: types-python** — Given `funcdctl.yaml` with a `contract` and bindings on a `python314` runtime, When
  `funcdctl types`, Then a `.pyi` is written declaring `FuncInput`/`FuncOutput` (from the schema) and a typed binding
  context, enabling editor autocomplete.
- **scenario: types-node** — same input on a `nodejs22` runtime, When `funcdctl types`, Then a `.d.ts` is written with
  the same two surfaces.
- **scenario: out-of-profile-rejected** — Given `funcdctl.yaml` `contract.input` uses `anyOf`, When `funcdctl push`,
  Then `contract.Check` fails → `fault.Invalid` and nothing ships.

## Scope

**In**: the `funcdctl.yaml` push/dev config (`runtime`, `handler`, `bindings`, inline `contract`); the contract
**source-resolution** with `funcdctl.yaml` primary; gating + baking the **schema-only** contract blob at `funcdctl
push`; `funcdctl types` emitting `.pyi`/`.d.ts` for the payload **and** the typed binding context.

**Out**: **compiling `funcdctl.yaml` → the `Function` CRD** and **`funcdctl apply -f funcdctl.yaml`** (deploy stays the
hand-written `Function` resource); deploy knobs in the config (`name`/`namespace`/`scaling`/`pooling`/`image`);
**`funcdctl dev`** (the local-run loop this config is *designed toward* — a follow-up ADR); the **runtime-compiled
validator** + relaxed `VerifyBundleContract` (**ADR-0123**); the esbuild-in-Go JS builder + any OCI plugin (deferred);
Python **wheel-vendoring** (ADR-0089 stays).

## Constraints & Decision drivers

- `funcdctl` is a **single Go binary**, depends only on `pkg/sdk` + `api/*` (blueprint §funcdctl) — **no language
  toolchain at push**.
- **containerd-native** platform (ADR-0052) — **no docker dependency** on the push host.
- Keep the **on-artifact** contract shape (ADR-0059 blob + annotations), the **mandatory both-sides** rule (ADR-0090),
  and the **funcd profile** gate (`contract.Check`, ADR-0058).
- **Authoring vs deploy separation**: the config carries only what the *client* (push/dev/types) needs; the cluster's
  desired state (name, scaling, placement) stays the `Function` CRD.
- Apache-2.0/MIT dependencies only.

## Alternatives considered

- **Schema derived from code types (status quo, ADR-0058) rather than declared in `funcdctl.yaml`.** *Pros*: the code
  type is the single source of truth. *Cons*: needs pydantic/typia at push — the coupling being removed. **Rejected**
  for the push path; `funcdctl types` restores the typed-`FuncInput` DX by going **schema → types** instead (and that
  round-trips losslessly within the bounded funcd profile).
- **`funcdctl.yaml` compiles to / replaces the `Function` CRD (the originally-accepted, now-overridden scope).** *Pros*:
  one file per function; wrangler-style "config generates the resource". *Cons*: pulls deploy concerns (name, scaling,
  placement) into a client config and duplicates the `Function` spec; the decider judged the authoring/deploy split
  cleaner. **Rejected (override):** `funcdctl.yaml` owns the client push/dev loop; the `Function` CRD stays the deploy
  manifest, hand-written and `apply`-ed as before.
- **Name it `funcd.yaml`.** **Rejected** — collides with the daemon operator config (ADR-0061). **Chosen**:
  `funcdctl.yaml` (CLI-named, parallels `wrangler.toml`; distinct from the `funcd` daemon).
- *(The validator precompile-vs-runtime-compile choice is decided in ADR-0123, not here.)*

## Decision

1. **`funcdctl.yaml`** — a colocated, per-function **client push/dev config**. Fields: `runtime`, `handler`, `bindings`
   (`blob`/`kv`/`catalogs`/`links`/`config`/`secrets` — what the code is wired to, needed to *run* and *type* it), and
   **`contract: { input, output }`** — inline JSON Schema, **both sides always present**, a void side spelled
   `{"type":"null"}` (ADR-0090). No deploy knobs. It does **not** compile to, or replace, the `Function` CRD.
2. **`funcdctl push`** — `funcdctl.yaml` is the **primary** contract source (source-resolution refines ADR-0090's order:
   `funcdctl.yaml.contract` → bundle `__funcd_contract.json` → `--schema` → code-derived). funcdctl runs `contract.Check`
   per side, bakes `{dialect,input,output}` as the ADR-0059 contract blob (media type / annotation / dialect
   **unchanged**), and records the runtime annotation. The artifact is **schema-only** (no baked validator — ADR-0123
   compiles it at the worker).
3. **`funcdctl types`** — from `funcdctl.yaml`, generate the `FuncInput`/`FuncOutput` payload types **and** a typed
   binding context — `.pyi` (Python) / `.d.ts` (Node) — from the `contract` schema + `bindings`. DX-only; the platform
   never trusts it.
4. **Deploy is separate.** The `Function` CRD stays hand-written and `funcdctl apply -f function.yaml`-ed as today.
   `funcdctl.yaml` neither generates nor mutates it. (`funcdctl dev`, the local-run loop this config is built toward, is
   a follow-up ADR.)
5. **No language plugin/container.** A minimal OCI plugin remains **deferred**, introduced only when a use case that
   genuinely needs language-native execution (e.g. esbuild bundling) is scoped.

## Temporary workarounds

- **`funcdctl dev` not yet built** — `funcdctl.yaml` already carries the `bindings` it will need; today it only drives
  `push` + `types`. *Exit*: the `funcdctl dev` ADR.
- **`funcdctl types` binding richness** is limited to declared aliases + the schema payload. *Exit*: richer typing when
  providers export type descriptors (follow-up).

## Contracts

**`funcdctl.yaml`** (documented YAML, **block style**; a void side is `{"type":"null"}` inline is *not* used — spell it
as a block `type: "null"`):

```yaml
runtime: python314
handler: handle
bindings:
  kv:
    - alias: cache
      store: kv
      table: t
contract:
  input:
    type: object
    additionalProperties: false
    properties:
      file:
        type: string
    required:
      - file
  output:
    type: object
    additionalProperties: false
    properties:
      rows:
        type: integer
    required:
      - rows
```

**Go (in `pkg/sdk`, so `funcdctl` and the SDK share it; no `internal/` import):**

```go
type Manifest struct {
    Runtime  v1alpha1.RuntimeName // dev.funcd.runtime.v1
    Handler  string               // the bundle entry
    Bindings Bindings             // blob/kv/catalogs/links/config/secrets — for funcdctl dev + types
    Contract Contract             // { Input, Output json.RawMessage } — baked schema-only
}

func LoadManifest(path string) (*Manifest, error)                  // parse + structural validate (runtime, handler, both sides)
func (m *Manifest) ContractSides() (input, output []byte, error)   // the two JSON Schema sides for gate + bake
func GenerateTypes(m *Manifest) (files map[string][]byte, error)   // ".pyi"/".d.ts" by runtime (payload + binding context)
```

There is **no `Compile()`** and **no `funcdctl.yaml` → `Function` map** — that path was removed by the override.

**Push flow** (refines `cmd/funcdctl` push): if `funcdctl.yaml` is present it is the **primary** contract source;
funcdctl (in the CLI layer, which owns `contract.Check`) gates each side, then `artifact.ContractBlob(input, output)`
and `artifact.Push`/`PushBundle`, recording the runtime annotation. Baking the blob is unchanged (it was always schema);
the worker-side validator is ADR-0123.

**Unchanged mechanism/constants** (ADR-0059/0090): `contractMediaType = application/vnd.funcd.contract.v1+json`,
`contractAnnotation = dev.funcd.contract.v1`, `runtimeAnnotation = dev.funcd.runtime.v1`, `contractDialect` (2020-12),
`VoidSchema = {"type":"null"}`, `ContractBlob(input, output)` (both required).

**Dependencies & I/O**

| Consumes | Exposes |
|---|---|
| `funcdctl.yaml` (new) · `internal/contract.Check` (CLI layer) · `internal/artifact.{ContractBlob,Push,PushBundle,Inspect}` · `api/types/v1alpha1` (binding + runtime types) | `funcdctl push` (schema-only contract baked) · `funcdctl types` (`.pyi`/`.d.ts`) |

No new deps: the YAML decoder is already vendored for `sdk.DecodeManifest`. No docker/container/plugin. `pkg/sdk`
imports no `internal/*` — the `contract.Check` profile gate lives in the `cmd/funcdctl` layer.

## Implementation plan

**Files**: `pkg/sdk/manifest.go` (`Manifest`, `LoadManifest`, `ContractSides`) · `pkg/sdk/types_gen.go`
(`GenerateTypes` → `.pyi`/`.d.ts`) · `cmd/funcdctl/manifest.go` + `cli.go` (push reads `funcdctl.yaml` primary; new
`types` command; `apply` stays plain-resource decode — unchanged from before this ADR).

**Deps**: none.

**Test plan** — one acceptance test per Scenario (`push-from-manifest`, `types-python`, `types-node`,
`out-of-profile-rejected`), each written to pass; contract/unit tests for `LoadManifest`/`ContractSides`/`GenerateTypes`
+ structural-validation errors.

**Definition of done**: `go build/vet/test` + `just lint` green; `funcdctl push` (bakes the contract) and `funcdctl
types` work from a `funcdctl.yaml`; a Python **and** a Node curated example carry a `funcdctl.yaml` **beside** their
existing `Function` CRD; the on-artifact contract blob unchanged (ADR-0059 `inspect` still renders it); FEAT-0001/F87
linked.

## Review checklist

- [ ] `funcdctl.yaml` is `{runtime, handler, bindings, contract}` only — no deploy knobs, no `Compile()`, no
      `apply -f funcdctl.yaml` (apply still decodes a plain resource).
- [ ] Contract source-resolution puts `funcdctl.yaml` first; each side passes `contract.Check`; out-of-profile fails push.
- [ ] Artifact carries a **schema-only** contract; ADR-0059 blob shape + annotations unchanged; `funcdctl inspect` works.
- [ ] `funcdctl types` emits `.pyi` **and** `.d.ts` (payload + binding context); DX-only, not trusted.
- [ ] **No** language toolchain invoked by funcdctl at push; docker not required. `pkg/sdk` imports no `internal/*`.
- [ ] Depends on ADR-0123 for worker-side validation (schema-only bake is inert without it).
- [ ] Both example `funcdctl.yaml` files are **block-style YAML**; FEAT-0001/F87 linked.

## Consequences

- (+) `funcdctl` is self-sufficient (pure Go) for the push/dev loop — no language plugin, container, or docker.
- (+) Clean **authoring/deploy split**: `funcdctl.yaml` owns the client push/dev loop; the `Function` CRD stays the
  deploy manifest. The config stays small and is the natural home for the coming `funcdctl dev`.
- (−) `bindings` are declared in **both** `funcdctl.yaml` (for dev/types) and the `Function` CRD (for deploy) until a
  future ADR unifies them — a small, deliberate duplication (the config is client-side, the CRD is desired-state).
- (−) Inert without ADR-0123 — a schema-only artifact needs the runtime-compiled validator to actually enforce I/O.

## Open questions

- **`funcdctl dev`** — the local-run loop (wrangler-`dev` analogue) this config is designed toward. *(Answered at: its
  own ADR.)*
- Whether a future ADR lets `funcdctl.yaml` be the single source for the bindings (generating/patching the CRD) to
  remove the duplication. *(Answered at: the `funcdctl dev` / manifest-unification ADR.)*
- Exact `.pyi`/`.d.ts` binding-type richness. *(Answered at: the implementation PR.)*

## References

- ADRs: [0058](0058-contract-codegen-from-code-types.md), [0059](0059-contract-as-oci-metadata.md),
  [0089](0089-python-function-dependency-bundling.md), [0090](0090-mandatory-single-io-schema.md),
  [0024](0024-funcdcli-and-sdk.md), [0123](0123-runtime-compiled-io-validators.md) (the runtime-compile dependency).
- [blueprint.md](../../blueprint.md) — the OCI distribution model, the CLI pre-flight, the funcdctl section. (The "no
  `func.yaml`" stance is **unaffected** now — `funcdctl.yaml` is a client push/dev config, not a deploy manifest, and
  the `Function` CRD remains the platform's desired-state manifest.)
- Prior art (verified 2026-07-11): Cloudflare Wrangler — `wrangler.toml` drives both `wrangler dev` (local run) and
  `wrangler deploy`/`wrangler types` (<https://developers.cloudflare.com/workers/wrangler/bundling/>,
  <https://developers.cloudflare.com/workers/languages/python/basics/>).
