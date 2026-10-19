# ADR-0246: Bindings move under `spec.bindings`, typed per kind

- **Status**: Implemented (2026-10-19), review passed (docs/reviews/adr-0246-implementation-claude-opus-5-5.md);
  Reviewing (2026-10-19), implemented in #1096, pyvvo/funcd-typescript#80 (v0.18.0), pyvvo/funcd-python#93 (v0.13.0) and
  pyvvo/funcd-functions#20 (v0.18.0); Accepted (2026-10-19), after two judge rounds, under `adr-batch` with acceptance
  delegated by the decider: the first round split the store half into ADR-0248 and gave each kind its own binding type;
  the second moved the Site into ADR-0248 and pinned the grant check to a golden fixture.
- **Date**: 2026-10-19
- **Deciders**: green-0-rabbit
- **Tags**: api, bindings, refactor
- **Realizes**: [FEAT-0001/F159](../feat/0001-feat-v1.1.md), with ADR-0248 (#1068, tracker #1072)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0246` back-link, for where a binding lives only;
  what each binding grants does not change:
  - on a Function: [ADR-0057](0057-secret-injection-last-mile.md) and [ADR-0093](0093-function-configmap-consumption.md)
    (`spec.secrets`, `spec.config`), [ADR-0064](0064-fn-to-fn-rpc-links.md) (`spec.links`),
    [ADR-0073](0073-kv-bindings-and-subdomains.md) (`spec.kv`), [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md)
    (`spec.blob`), [ADR-0091](0091-function-catalog-consumer-binding.md) (`spec.catalogs`),
    [ADR-0233](0233-sql-access-from-functions.md) (`spec.sql`);
  - on a CatalogService: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) and
    [ADR-0087](0087-add-on-provider-runtime.md) (`spec.blob`, `spec.config`, `spec.secrets`);
  - on a Workflow step: [ADR-0094](0094-workflow-engine-core.md) (the step bindings).
- **Relates to**: ADR-0248 (the stores a resource declares at its root) · [ADR-0245](0245-ctx-config-secret-catalog.md)
  (the accessor names) · [ADR-0127](0127-context-blob-data-plane.md) (`ctx.blob`) · [ADR-0136](0136-roles-and-role-assignments.md)
  (writer roles)

## Context & Need

Bindings sit at each spec's root, beside what the resource defines: a Function's `kv`, `blob`, `sql`, `catalogs`,
`links`, `config` and `secrets` (`api/types/v1alpha1/function.go`), a step's six of them (`workflow.go`), and a
CatalogService's `blob`, `config` and `secrets`. ADR-0248 puts what a resource owns at that root, so what its code
consumes moves aside first. No stored object exists yet (the decider, 2026-10-19).

**Purpose**: one place per resource for what its code uses, named like the `ctx` accessor that reaches it. The callers
are authors, agents and every controller that reads a binding.

## Scenarios

- **scenario: bindings-under-spec** — *Given* a Function binding KV table `counts` of `counters` as `counters` under
  `spec.bindings.kv`, *Then* `ctx.kv.get('counters', …)` reads it, and the same holds for `blob`, `sql`, `catalog`,
  `links`, `config` and `secret`.
- **scenario: step-bindings** — *Given* a step with `function.bindings.kv`, *Then* its materialized Function carries
  the binding and the step reads the table.
- **scenario: catalog-service-bindings** — *Given* a CatalogService with `bindings.blob`, `config` and `secret`,
  *Then* its engine reads them as before.
- **scenario: binding-kind-refused** — *When* a step sets `bindings.links`, or a CatalogService `bindings.kv` or
  `bindings.sql`, *Then* apply refuses it, naming the field.
- **scenario: old-field-refused** — *When* a Function sets `spec.links` or a `spec.kv` entry with `alias`, *Then* the
  API refuses it, naming the field.
- **scenario: grants-unchanged** — *Given* each kind's bindings, *Then* Cedar grants exactly what the old fields
  granted, no more.
- **scenario: dev-still-runs** — *Given* today's `funcdctl.yaml` projects, *When* `funcdctl dev` runs them, *Then* they
  run as before.

## Scope

**In**: the binding types and where they sit on Function, Workflow step and CatalogService; the App's parts, which
inline those specs; every reader; the YAML in funcd, funcd-typescript, funcd-python and funcd-functions.

**Out**: what a resource declares at its root (ADR-0248), and the Site, whose bucket and served prefix change
together there; the contract file and dev reading resource files (ADR-0247); EventSource (`blob`, `sql`) and Sensor, whose fields are sources, not bindings for code; a converter (none
needed).

## Constraints & Decision drivers

- Grants stay identical: the binding is the capability; writes stay the owner's and the writer roles' (ADR-0073,
  ADR-0136, ADR-0233).
- A kind accepts only the bindings something reads.
- Every moved reader must fail to compile until it is moved, never silently change meaning.
- Old shapes fail loudly: each spec schema sets `additionalProperties: false` (`api/openapi/funcd.v1alpha1.yaml:1910`).

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. A binding type per kind under `spec.bindings`** | Each kind accepts what it reads; grants cannot widen | Four small types | **Chosen** |
| B. One shared `Bindings` type | One type | A Site or CatalogService would accept `kv` or `links` that nothing reads, and a generic reader could grant a CatalogService more | Rejected (the judge) |
| C. Rename the old fields in place | Smaller diff | Same Go names with new meaning compile silently | Rejected |

## Decision

1. **Per-kind types** under `spec.bindings`, named like the `ctx` accessors (ADR-0245): `config`, `secret` and
   `catalog` are singular. `links` keeps its name, because code calls `ctx.invoke` and `ctx.fetch`.

   | Kind | Type | Keys |
   |---|---|---|
   | Function (and an App's function part) | `FunctionBindings` | `kv`, `blob`, `sql`, `catalog`, `links`, `config`, `secret` |
   | Workflow step | `StepBindings` | `kv`, `blob`, `sql`, `catalog`, `config`, `secret` |
   | CatalogService (and an App's catalog entry) | `CatalogServiceBindings` | `blob`, `config`, `secret` |

2. **The old fields are deleted** from the Go spec structs, not renamed, so every reader fails to compile until it reads
   `.Bindings`. ADR-0248 then adds the root entries under new Go names.
3. **Grants are derived from the new paths with the same rules**: `internal/auth/cedar` reads a Function's and a
   CatalogService's bindings separately, as it does today (`capabilities.go:254`).
4. **The API refuses an old field**, through the closed schema and the SDK's strict decode.
5. **A clean break**: every file in the four repos moves in the same change.

## Temporary workarounds

- **`funcdctl dev` still reads `funcdctl.yaml`**, and writes the new shape from it. *Exit*: ADR-0247.

## Contracts

```go
// api/types/v1alpha1
type FunctionBindings struct {
	KV      []FunctionKV      `json:"kv,omitempty"`
	Blob    []FunctionBlob    `json:"blob,omitempty"`
	SQL     []FunctionSQL     `json:"sql,omitempty"`
	Catalog []FunctionCatalog `json:"catalog,omitempty"`
	Links   []FunctionLink    `json:"links,omitempty"`
	Config  []ObjectName      `json:"config,omitempty"`
	Secret  []ObjectName      `json:"secret,omitempty"`
}

type StepBindings struct { // FunctionBindings without Links
	KV      []FunctionKV      `json:"kv,omitempty"`
	Blob    []FunctionBlob    `json:"blob,omitempty"`
	SQL     []FunctionSQL     `json:"sql,omitempty"`
	Catalog []FunctionCatalog `json:"catalog,omitempty"`
	Config  []ObjectName      `json:"config,omitempty"`
	Secret  []ObjectName      `json:"secret,omitempty"`
}

type CatalogServiceBindings struct {
	Blob   []FunctionBlob `json:"blob"`
	Config []ObjectName   `json:"config,omitempty"`
	Secret []ObjectName   `json:"secret,omitempty"`
}
```

| Kind | Removed | Added |
|---|---|---|
| Function | `kv`, `blob`, `sql`, `catalogs`, `links`, `config`, `secrets` | `bindings` (`FunctionBindings`) |
| Workflow step | `kv`, `blob`, `sql`, `catalogs`, `config`, `secrets` | `bindings` (`StepBindings`) |
| CatalogService | `blob`, `config`, `secrets` | `bindings` (`CatalogServiceBindings`) |

## Implementation plan

- **funcd:**
  - The types, `Validate`, and `just generate` (the OpenAPI spec).
  - Every reader the compiler flags. The inventory on #1072 lists them, about 200 source references in 15 packages:
    `auth/cedar`, `gc`, `health`, `backup/workload`, `backup/escrow`, `services`, `function` (the pool key in
    `internal/pooling` included), `workflow`, `app`, `site`, `catalog`, `controlplane`, and `cmd/funcdctl` (the
    printers and dev's writer).
  - Tests: one per scenario, named after it. `grants-unchanged` compares each kind's Cedar entities with a golden
    fixture recorded from main in a first commit, over the same resources written in both shapes.
- **funcd-typescript, funcd-python, funcd-functions:** every Function, Workflow, CatalogService and App file in the
  new shape (an App's parts inline these specs), and funcd-functions re-tags the templates the lanes pin by digest.
  Their CI checks no funcd shape, so the funcd PR validates them first.
- **Order:** these three repos release first (on the decider's go), then one funcd PR changes the API and pins them.
- **Docs:** the back-links, the F159 row, the blueprint's field paths.
- **Done when** `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e` and `-tags dev`), `go mod verify`
  and every Lima lane pass, and no old binding field remains in any of the four repos.

## Review checklist

- [ ] Each kind has its own binding type with exactly the keys above.
- [ ] No old binding field remains in the Go types; no reader kept a removed field's meaning.
- [ ] Cedar grants equal the old grants for every kind; a CatalogService gains nothing.
- [ ] The API refuses each old field and each binding key a kind does not take.
- [ ] One test per scenario, named after it; every lane passes.

## Consequences

- **Positive**: a resource's root holds only what it defines; a binding is named like its accessor.
- **Negative**: a breaking change in four repos at once.
- **Risks accepted**: a missed reader fails to compile or fails a test, never production, since nothing is stored.

## Open questions

None.

## References

- #1068, tracker #1072 (the inventory of readers).
- `internal/auth/cedar/capabilities.go` (grants from bindings, `:254` for a CatalogService), `internal/pooling/pooling.go:57`
  (the pool key), `api/openapi/funcd.v1alpha1.yaml:1910`.
