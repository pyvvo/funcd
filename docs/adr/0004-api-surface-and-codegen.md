# ADR-0004: API surface & codegen (OpenAPI v1alpha1)

- **Status**: Accepted
- **Date**: 2026-06-14 (revised same day after judge review: `…List` envelopes are generated not
  bound; `codegen-reproducible` gets a real shell-out test; drift-reach, error-boundary, and a
  `bearerAuth` security-scheme declaration clarified; accepted 2026-06-14)
- **Deciders**: green-0-rabbit
- **Tags**: api, openapi, codegen, oapi-codegen, rest, drift
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md)
- **Relates to**: [ADR-0003](0003-resource-model-and-api-typing.md) (the canonical Go types this
  binds to; resolves its deferred *"how OpenAPI references these Go types + the drift CI gate"*),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (resolves its deferred *generated-code
  policy*; `api/fault` problem+json), [ADR-0001](0001-project-setup-and-structure.md) (module,
  `just`, the `tool` directive), [blueprint.md — API-first / funcdcli / Control-plane API & IaC](../../blueprint.md)

## Context & Need

funcd is **API-first**: the control-plane REST API is the single front door for every client —
`funcdcli`, the Go SDK, CI, and the future Terraform provider (blueprint *Control-plane API & IaC*).
For that to hold, the API must be an **OpenAPI contract** from which the server interface, the client,
and the wire types are *generated*, so server, SDK, and CLI cannot drift from the spec or from each
other. ADR-0003 made the hand-written Go resource types (`api/types/v1alpha1`) the canonical model and
explicitly deferred *authoring the OpenAPI to match them, plus the drift gate*, to this ADR. ADR-0002
deferred *the full generated-code lint policy* here too. Nothing is generated yet; without this
decision every later feature would hand-roll handlers and the CLI/SDK would diverge from the server.

**Purpose**: establish (a) the **OpenAPI v1alpha1 contract** for the generic resource lifecycle, (b)
the **codegen toolchain** that turns it into a typed strict-server interface + client + types bound to
the canonical Go model, and (c) the **drift gate** that guarantees the spec, the generated code, and
the Go types never silently diverge. Its callers: the API server (P-L/F07) *implements* the generated
strict-server interface; the CLI/SDK (P-R/F18) *consume* the generated client; every later feature ADR
*extends* the spec with its own paths. Conformance is mechanical: `just generate` is reproducible (CI
fails on diff), the schema-vs-Go drift check passes, generated files are lint-exempt while hand-written
code is not, and a typed resource round-trips client→server.

## Scenarios

- `scenario: codegen-reproducible` — **Given** the committed `funcd.v1.yaml` and generated `*.gen.go`,
  **when** `just generate` re-runs, **then** `git diff --exit-code` reports no changes — so CI fails if
  anyone edits the spec without regenerating, or hand-edits generated output.
- `scenario: spec-go-shape-drift-caught` — **Given** an OpenAPI resource schema whose documented shape
  diverges from its `x-go-type`-bound Go struct (a field added on one side only), **when** the drift
  check runs, **then** it fails naming the mismatched field — making "the OpenAPI matches the Go types"
  mechanically true despite `x-go-type` substituting the type at generation time.
- `scenario: generated-code-lint-exempt` — **Given** generated `*.gen.go` files containing
  `interface{}`/`map[string]interface{}` (oapi-codegen output for `additionalProperties`/`oneOf`),
  **when** `just lint` runs, **then** forbidigo does **not** flag the generated files, while the same
  pattern in a hand-written file **is** still flagged.
- `scenario: strict-handler-typed` — **Given** the generated strict-server interface, **when** a
  handler implements an operation, **then** it receives a typed request object and returns a typed
  response/error (no manual JSON unmarshal in the handler); an operation left unimplemented **fails to
  compile**.
- `scenario: client-server-roundtrip` — **Given** the generated client pointed at the generated server
  (over `httptest`), **when** a `Function` is applied and then fetched, **then** the returned typed
  object equals the applied one — proving client, server, and types agree end-to-end.
- `scenario: error-is-problem-json` — **Given** a handler that returns a `fault.Error{Kind: NotFound}`,
  **when** the response is written through the `api/fault` edge mapping, **then** the body is RFC 9457
  `application/problem+json` with HTTP 404, matching the error schema the spec declares for the
  operation.

## Scope

**In**:
- The **OpenAPI 3.0.x document** `api/openapi/funcd.v1.yaml` — the hand-authored wire contract for the
  **generic kubectl-style resource lifecycle**: create / get / list (with `namespace`, `resourceGroup`,
  `tag` filters) / replace (apply) / delete, for the v1alpha1 kinds, under `/apis/funcd.io/v1alpha1/…`.
- The **`x-go-type` binding**: resource request/response bodies bind to `api/types/v1alpha1` (Q1) — one
  type set, no generated DTO duplication.
- The **oapi-codegen toolchain**: tool pinned via the go.mod `tool` directive; per-output config;
  `go:generate` + a `just generate` recipe; **strict-server on chi**; generated **types + server +
  client**, committed under `api/openapi/generated/`.
- The **drift gate** (two checks, both in `just ci`): codegen-staleness (`generate` + `git diff
  --exit-code`) **and** schema-vs-Go-shape validation.
- The **generated-code lint policy** (closes ADR-0002 §4/§7): generated files exempt from the
  hand-written `any`-ban and style linters; hand-written code still enforced.
- The **problem+json error declaration**: every operation declares `application/problem+json`
  (RFC 9457) as its error media type, mapped at the edge by `api/fault` (ADR-0002).

**Out**:
- **Per-feature endpoints & subresources** — `invoke`, `logs`, `rollout undo`, shape-`validate`, and
  any non-CRUD operation — added to the spec by their owning feature ADRs (P-M/F13, P-R/F18); admission,
  authn, RBAC, quota by the **API-server ADR** (P-L/F07). This ADR fixes the *contract + tooling*; those
  fix server behavior.
- **proto/gRPC codegen** (`api/proto/**`, buf) — the worker/control-plane/runtime protos are multi-node
  seams (V1-out); their own ADR.
- **Watch (streaming) and pagination** — REST `watch` and `limit`/`continue` are deferred to a
  follow-up (the store's internal watch is P-C/F05); V1 list is a filtered snapshot.
- **The SDK ergonomic wrapper** (`pkg/sdk`) and the CLI — P-R/F18 (it wraps the client this ADR
  generates).

## Constraints & Decision drivers

- **C1 — bind to ADR-0003, don't duplicate**: the generated code must reuse the canonical
  `api/types/v1alpha1` types (typed IDs, enums, `Validate()`), not emit parallel structs. `x-go-type`
  is the mechanism.
- **C2 — ADR-0002 generated-code policy**: generated files are exempt from the `any`-ban (oapi-codegen
  emits `interface{}` for `additionalProperties`/`oneOf`); errors are `api/fault` problem+json. This ADR
  owns the *full* generated-code policy ADR-0002 deferred.
- **C3 — blueprint inheritance**: API-first; OpenAPI is the wire source of truth; single Go module;
  **generated code committed**; CI re-runs codegen and **fails on diff**; tools pinned via the go.mod
  `tool` directive; Apache-2.0/MIT deps only.
- **C4 — OpenAPI 3.0.x**: oapi-codegen does not fully support 3.1; the spec targets 3.0.x.
- **D1 — drift caught mechanically, both directions**: staleness *and* schema-vs-Go-shape — because
  `x-go-type` makes the schema documentation that can silently lie.
- **D2 — embed-first, weighed**: a router dependency is accepted only with a reason; chi is chosen on
  its merits (below), not by default.

## Alternatives considered

**Type binding** (driver: keep ADR-0003's single source of truth):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **`x-go-type` → reuse `api/types/v1alpha1`** | one type set; typed IDs/enums/`Validate()` flow through; no converter | schema carries `x-go-type` extensions; needs the shape-drift check (the spec can lie) | **chosen** (Q1) |
| Separate generated DTOs + converter | "OpenAPI-pure" standalone structs | reintroduces the duplicate-type/drift problem ADR-0003 avoided; a converter to maintain | rejected |
| OpenAPI-first, generate the types | one tool authors both | inverts ADR-0003 (Go is canonical); loses typed-ID/method semantics | rejected (ADR-0003) |

**HTTP server style** (driver: ergonomics + middleware vs embed-first):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **chi strict-server** | mature, lightweight (MIT, pure-Go, no transitive deps); first-class oapi-codegen support; rich middleware ecosystem for the authn/audit/recovery/request-id/otel stack (P-L) | one runtime router dependency | **chosen** (Q3) |
| stdlib `net/http` (Go 1.26 ServeMux) | zero router dep (embed-first); method+path routing built in | middleware hand-rolled; less ergonomic param/group handling | rejected (chi's middleware ergonomics won; the dep is tiny) |
| echo / gin | batteries included | heavier; opinionated; more surface than needed | rejected |

**Codegen tool** (driver: strict typed server + `x-go-type` + maintenance):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **oapi-codegen v2** | blueprint choice; Apache-2.0; strict-server; `x-go-type`/`x-go-type-import`; chi + std-http; active | OpenAPI 3.1 not fully supported (use 3.0.x) | **chosen** |
| ogen | very fast, type-safe, no reflection | different idioms; no `x-go-type` reuse of external types; larger generated surface | rejected (breaks C1) |
| go-swagger | mature | heavy, 2.0-centric, opinionated project layout | rejected |

**Spec authoring** (driver: ADR-0003 said P-B *authors* the OpenAPI to match):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Hand-author `funcd.v1.yaml`, drift-checked against Go** | the wire contract is human-owned + reviewable; non-Go clients get a real schema | author the shape twice (YAML + Go), reconciled by the drift check | **chosen** |
| Generate the OpenAPI *from* the Go types | one source | inverts ADR-0003's "OpenAPI authored to match"; reflection-derived schemas are awkward to hand-tune | rejected |

**Drift mechanism** (driver: make "OpenAPI ≡ Go types" mechanically true):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **`generate`+`git diff` AND schema-vs-Go-shape test** | staleness *and* the `x-go-type`-hidden divergence both caught | two checks to maintain | **chosen** |
| `generate`+`git diff` only | simple | misses the case where the schema's documented properties drift from the bound Go type (oapi-codegen uses the Go type, so it won't notice) | rejected (D1) |

## Decision

### 1. One OpenAPI 3.0.x document, hand-authored
`api/openapi/funcd.v1.yaml` is the wire contract. Paths are kubectl-style under
`/apis/funcd.io/v1alpha1/`. Namespaced kinds:
`…/namespaces/{namespace}/{plural}` (list, create) and `…/namespaces/{namespace}/{plural}/{name}`
(get, replace, delete); cluster kinds drop the `namespaces/{namespace}` segment. List supports
`resourceGroup` and `tag` query filters (blueprint *Control-plane API & IaC*: "list/filter by resource
group and tags"). The standard CRUD set is authored for **all 15 kinds** (mechanical — same template,
bodies bound by `x-go-type`); feature subresources are added later by their ADRs.

The spec **declares** a `bearerAuth` security scheme (HTTP bearer — static tokens + scoped API keys,
blueprint *Security model*) applied globally, so the generated client is auth-ready. **Declaring is not
enforcing**: authentication/RBAC/admission are the API-server ADR's (P-L/F07); P-B only puts the scheme
in the contract so the client carries credentials.

### 2. Generated code binds to the canonical types (`x-go-type`)
Each resource schema carries `x-go-type` + `x-go-type-import` pointing at `api/types/v1alpha1`, so
oapi-codegen emits the operations, params, and request/response wrappers but uses the **hand-written
types** as the bodies. No parallel DTOs **for the kinds themselves**; typed IDs/enums and `Validate()`
flow through unchanged. The schema still documents the properties (for non-Go clients and the drift
check — §6).

**List/transport envelopes are generated, not bound.** ADR-0003 (frozen) defines no `…List` types —
and a list wrapper is a transport concern (it will later carry `continue`/`resourceVersion` for watch +
pagination), not a domain type. So `FunctionList` et al. are **oapi-codegen-generated** structs whose
`Items` field is `[]v1alpha1.Function` (only the inner `items` schema is `x-go-type`-bound). The
"no duplicate DTOs" rule applies to the kinds, never to their list/pagination envelopes.

### 3. oapi-codegen toolchain, pinned + committed
- Pin the generator via the go.mod **`tool`** directive: `github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen` (Apache-2.0).
- Three per-output configs under `api/openapi/` produce `generated/{types,server,client}.gen.go`
  (`package generated`), committed.
- `//go:generate` directives + a **`just generate`** recipe drive it; `just ci` runs `generate` then
  `git diff --exit-code` (staleness gate).
- Runtime deps the generated code imports: `github.com/oapi-codegen/runtime` (Apache-2.0, strict-server
  + param binding) and `github.com/go-chi/chi/v5` (MIT).

### 4. Strict-server on chi; generated client
The server output is a **strict** interface (`generate: { chi-server, strict-server, models: false }` —
models come from `types.gen.go`): each operation is a method taking a typed request and returning a
typed response, so handlers never touch raw JSON and a missing operation fails to compile. P-L/F07
implements this interface with its middleware. The client output (`generate: { client }`) is the typed
client P-R/F18 wraps in `pkg/sdk`.

### 5. Errors are problem+json (RFC 9457), declared in the spec
Every operation declares `application/problem+json` as its error response media type, referencing a
`Problem` schema bound (`x-go-type`) to `api/fault.Problem` (verified: that struct exists with
`type`/`title`/`status`/`detail`/`instance`). **Boundary**: P-B *declares* problem+json in the spec and
*proves it is realizable* — its test wires `fault.WriteProblem` (ADR-0002 §3) as the strict-server's
`ResponseErrorHandler` and asserts a `fault.NotFound` → HTTP 404 problem+json. The **production** wiring
of that error handler onto the running server is the API-server ADR's (P-L/F07); P-B only shows the
pattern works and that the spec's declaration is achievable.

### 6. Drift gate — two checks
- **Staleness**: `just generate` + `git diff --exit-code` (committed generated code ≡ spec).
- **Schema-vs-Go-shape**: a test loads `funcd.v1.yaml`, and for every schema with `x-go-type`,
  constructs the bound Go type, marshals it, and validates the JSON against the schema (resource schemas
  set `additionalProperties: false` so a Go field absent from the schema fails, and required schema
  fields absent from the Go type fail). This is what makes ADR-0003's "OpenAPI matches the Go types"
  *enforced*, not asserted.
- **Reach**: the check covers the **envelope** (`apiVersion`/`kind`/`metadata`/`spec`/`status` keys);
  `spec`/`status` are opaque `type: object`, so feature-owned deep fields are *not* shape-checked here
  (they grow per feature ADR). Nested types (`ObjectMeta`, `TypeMeta`, `Conditions`) ride along via the
  top-level kind's `x-go-type`, so their schema `$ref`s are documentation/drift targets, not separate
  Go bindings.

### 7. Generated-code lint policy (closes ADR-0002 §4/§7)
Generated files carry the `// Code generated … DO NOT EDIT.` header, so golangci-lint's
`exclusions.generated: lax` (already in `.golangci.yml`) and the `*.gen.go` / `generated/` path
exemption (ADR-0002) exempt them from forbidigo's `any`-ban and style linters; hand-written code stays
enforced. P-B owns this policy and extends `.golangci.yml` only if a specific linter still fires on
generated output (editing the lint *config* is allowed — ADR-0002's *file* is frozen, its config code is
not).

### 8. Versioning
`v1alpha1` rides in the path; a stable `v1` later is a new spec + a new `api/types/v1` package
(ADR-0003 §1), never an in-place mutation of v1alpha1.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| OpenAPI pinned to **3.0.x**, not 3.1 | oapi-codegen lacks full 3.1 support | upgrade the spec to 3.1 when oapi-codegen ships full support |
| List returns a **filtered snapshot**; no `watch`/pagination on REST yet | watch needs the store's watch (P-C) + chunked transport; out of this ADR's altitude | a follow-up ADR adds `watch` + `limit`/`continue` once the store + controller exist |
| Resource schemas are **hand-authored twice** (YAML properties + Go struct), reconciled by the shape-drift check | ADR-0003 keeps Go canonical and this ADR keeps the wire schema human-authored | revisit if a reliable Go→OpenAPI schema generator removes the double-authoring without inverting ADR-0003 |

## Contracts

### OpenAPI — CRUD path template + `x-go-type` binding (`api/openapi/funcd.v1.yaml`)
```yaml
openapi: 3.0.3
info: { title: funcd control-plane API, version: v1alpha1 }
security: [ { bearerAuth: [] } ]                 # declared globally; enforced by P-L (F07)
paths:
  /apis/funcd.io/v1alpha1/namespaces/{namespace}/functions:
    parameters: [ { $ref: '#/components/parameters/Namespace' } ]
    get:           # list (filters: resourceGroup, tag)
      operationId: listFunctions
      parameters:
        - { name: resourceGroup, in: query, schema: { type: string } }
        - { name: tag,           in: query, schema: { type: array, items: { type: string } } }
      responses:
        '200': { description: ok, content: { application/json: { schema: { $ref: '#/components/schemas/FunctionList' } } } }
        default: { $ref: '#/components/responses/Problem' }
    post:          # create
      operationId: createFunction
      requestBody: { required: true, content: { application/json: { schema: { $ref: '#/components/schemas/Function' } } } }
      responses:
        '201': { description: created, content: { application/json: { schema: { $ref: '#/components/schemas/Function' } } } }
        default: { $ref: '#/components/responses/Problem' }
  /apis/funcd.io/v1alpha1/namespaces/{namespace}/functions/{name}:
    parameters:
      - { $ref: '#/components/parameters/Namespace' }
      - { $ref: '#/components/parameters/Name' }
    get:    { operationId: getFunction,    responses: { '200': { description: ok, content: { application/json: { schema: { $ref: '#/components/schemas/Function' } } } }, default: { $ref: '#/components/responses/Problem' } } }
    put:    { operationId: replaceFunction, requestBody: { required: true, content: { application/json: { schema: { $ref: '#/components/schemas/Function' } } } }, responses: { '200': { description: ok, content: { application/json: { schema: { $ref: '#/components/schemas/Function' } } } }, default: { $ref: '#/components/responses/Problem' } } }
    delete: { operationId: deleteFunction, responses: { '204': { description: deleted }, default: { $ref: '#/components/responses/Problem' } } }

components:
  parameters:
    Namespace: { name: namespace, in: path, required: true, schema: { type: string } }
    Name:      { name: name,      in: path, required: true, schema: { type: string } }
  responses:
    Problem:
      description: error
      content: { application/problem+json: { schema: { $ref: '#/components/schemas/Problem' } } }
  schemas:
    Function:                                   # the body binds to the canonical Go type
      x-go-type: v1alpha1.Function
      x-go-type-import: { path: github.com/green-0-rabbit/funcd/api/types/v1alpha1, name: v1alpha1 }
      type: object
      additionalProperties: false               # so shape-drift is caught
      properties:                               # documented shape — the drift check's target
        apiVersion: { type: string }
        kind:       { type: string }
        metadata:   { $ref: '#/components/schemas/ObjectMeta' }
        spec:       { type: object }
        status:     { type: object }
    Problem:
      x-go-type: fault.Problem
      x-go-type-import: { path: github.com/green-0-rabbit/funcd/api/fault, name: fault }
      type: object
      properties: { type: {type: string}, title: {type: string}, status: {type: integer}, detail: {type: string}, instance: {type: string} }
    FunctionList:                                 # GENERATED envelope (no x-go-type): Items are the canonical kind
      type: object
      properties:
        apiVersion: { type: string }
        kind:       { type: string }
        items:      { type: array, items: { $ref: '#/components/schemas/Function' } }   # → Items []v1alpha1.Function
    # … ObjectMeta (x-go-typed → v1alpha1.ObjectMeta), the other 14 kinds (x-go-typed), and each
    #   kind's generated <Kind>List envelope, follow these templates …
  securitySchemes:
    bearerAuth: { type: http, scheme: bearer }    # static tokens + scoped API keys (enforcement: P-L)
```

### oapi-codegen config (one per output, e.g. `api/openapi/codegen.server.yaml`)
```yaml
package: generated              # all three configs (types/server/client) share package generated
output: api/openapi/generated/server.gen.go
generate:
  chi-server: true
  strict-server: true
  models: false                 # models live in types.gen.go; x-go-type-import resolves the canonical types
output-options:
  skip-prune: true
```
```go
// api/openapi/generate.go
//go:generate go tool oapi-codegen -config codegen.types.yaml  funcd.v1.yaml
//go:generate go tool oapi-codegen -config codegen.server.yaml funcd.v1.yaml
//go:generate go tool oapi-codegen -config codegen.client.yaml funcd.v1.yaml
package openapi
```

### Generated strict-server interface (shape oapi-codegen emits; illustrative)
```go
// api/openapi/generated/server.gen.go  (// Code generated … DO NOT EDIT.)
type StrictServerInterface interface {
    ListFunctions(ctx context.Context, req ListFunctionsRequestObject) (ListFunctionsResponseObject, error)
    CreateFunction(ctx context.Context, req CreateFunctionRequestObject) (CreateFunctionResponseObject, error)
    GetFunction(ctx context.Context, req GetFunctionRequestObject) (GetFunctionResponseObject, error)
    ReplaceFunction(ctx context.Context, req ReplaceFunctionRequestObject) (ReplaceFunctionResponseObject, error)
    DeleteFunction(ctx context.Context, req DeleteFunctionRequestObject) (DeleteFunctionResponseObject, error)
    // … the other 14 kinds …
}
// CreateFunctionRequestObject.Body is *v1alpha1.Function (x-go-type), not a generated DTO.
func HandlerFromMux(si ServerInterface, r chi.Router) http.Handler
```

### Drift check (`api/openapi/drift_test.go`)
```go
// Loads funcd.v1.yaml via kin-openapi (MIT, already an oapi-codegen dep); for each schema with
// x-go-type, constructs the bound Go value, marshals it, and validates against the schema.
func TestSchemaMatchesGoTypes(t *testing.T)   // scenario: spec-go-shape-drift-caught
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/types/v1alpha1` (ADR-0003), `api/fault` (ADR-0002) | bound via `x-go-type`; the only bodies/errors on the wire |
| Consumes | go.mod `tool` directive, `just`, `.golangci.yml` (ADR-0001/0002) | pins the generator; adds the `generate` recipe + drift gate |
| Adds (tool) | `github.com/oapi-codegen/oapi-codegen/v2` | **Apache-2.0** |
| Adds (runtime) | `github.com/oapi-codegen/runtime`, `github.com/go-chi/chi/v5` | **Apache-2.0**, **MIT** |
| Adds (test) | `github.com/getkin/kin-openapi` | **MIT** (already transitive via oapi-codegen) — spec load + validate |
| Exposes | `api/openapi/funcd.v1.yaml` + `api/openapi/generated/{types,server,client}.gen.go` | the contract + the generated `StrictServerInterface`, client, and (re-exported) types |
| Exposes | the drift gate + generated-code lint policy | every later feature ADR extends the spec under these rules |

## Implementation plan

No business logic — the spec, the generated code, the toolchain, the drift gate, and passing scenario
tests. This ADR has no infrastructure deps; every scenario test runs green at implement time.

1. **`api/openapi/funcd.v1.yaml`** — the 3.0.3 document: the CRUD template (§Contracts) applied to all
   15 kinds, `x-go-type` bindings to `api/types/v1alpha1`, `resourceGroup`/`tag` list filters, the
   `Problem` response bound to `api/fault`, `additionalProperties: false` on resource schemas.
2. **`api/openapi/codegen.{types,server,client}.yaml`** — oapi-codegen configs (types: models from the
   `x-go-type`-bound schemas; server: chi + strict, models:false; client).
3. **`api/openapi/generate.go`** — the `//go:generate` directives (`package openapi`).
4. **Generate + commit** `api/openapi/generated/{types,server,client}.gen.go`.
5. **`go.mod`** — `go get -tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen`; `go get
   github.com/oapi-codegen/runtime github.com/go-chi/chi/v5`; `go mod tidy`.
6. **`justfile`** — a `generate` recipe (`go generate ./api/openapi/...`); wire `generate` +
   `git diff --exit-code` into `ci` as the staleness gate.
7. **`.golangci.yml`** — verify generated files are exempt (header + path rule from ADR-0002); extend
   only if a linter still fires on generated output.
8. **Test plan** (one acceptance test per Scenario, all passing):
   - `api/openapi/drift_test.go` → `spec-go-shape-drift-caught` (the kin-openapi validation above) +
     a negative case (a deliberately-mismatched fixture schema fails).
   - `api/openapi/roundtrip_test.go` → `client-server-roundtrip` (mount the generated `HandlerFromMux`
     with a tiny in-memory `StrictServerInterface` impl on `httptest`; the generated client applies +
     gets a `Function`; assert equality) and `strict-handler-typed` (compile-time `var _
     StrictServerInterface = (*stubServer)(nil)`; the stub returns typed responses) and
     `error-is-problem-json` (the stub returns `fault.NotFoundf`; the test wires `fault.WriteProblem` as
     the strict `ResponseErrorHandler` — the pattern P-L adopts — and asserts 404 + `application/problem+json`).
   - `api/openapi/reproducible_test.go` → `codegen-reproducible`: a (`testing.Short`-skippable) test
     that runs the pinned generator (`go tool oapi-codegen`) for each config into a temp dir and asserts
     byte-equality with the committed `generated/*.gen.go`; plus the `just ci` `generate` +
     `git diff --exit-code` staleness gate as the CI backstop.
   - `generated-code-lint-exempt` → reuse the `tests/lint-fixtures` harness (ADR-0002): a
     `// Code generated … DO NOT EDIT.` fixture with `any` is **not** flagged; a hand-written twin **is**.
9. **Definition of done** (= Scenarios executed):
   - `just generate` is reproducible; `just ci` exits 0 (incl. the staleness gate) on a committed tree.
   - The generated server/client/types compile and **bind to `api/types/v1alpha1`** (no duplicate
     resource DTOs); `grep` shows resource bodies are `v1alpha1.*`.
   - The schema-vs-Go-shape drift test passes (and fails on the negative fixture).
   - Generated files are lint-exempt; hand-written `any` is still flagged.
   - `client-server-roundtrip` and `error-is-problem-json` pass; the strict interface is complete.
   - Only the sanctioned deps added; `go.mod`/`go.sum` tidy. Every Scenario has a named, passing test.

## Review checklist

- [ ] `api/openapi/funcd.v1.yaml` exists (OpenAPI **3.0.x**), covers CRUD for the 15 kinds, declares
      `application/problem+json` errors, and sets `additionalProperties: false` on resource schemas.
- [ ] The spec declares a `bearerAuth` security scheme (applied globally); the generated client is
      auth-ready (enforcement deferred to P-L).
- [ ] Resource **kind** bodies bind via `x-go-type`/`x-go-type-import` to `api/types/v1alpha1` (generated
      bodies are `v1alpha1.*`); **`…List` envelopes are generated** (not bound), with `Items
      []v1alpha1.<Kind>`. No duplicate *kind* structs.
- [ ] oapi-codegen pinned via the go.mod `tool` directive; configs produce `generated/{types,server,
      client}.gen.go`; **server is chi + strict**.
- [ ] `just generate` is reproducible: `go generate` then `git diff --exit-code` is clean; the gate is
      wired into `just ci`.
- [ ] The schema-vs-Go-shape drift test passes and fails on a deliberately-mismatched fixture.
- [ ] Generated files (`*.gen.go`, `generated/**`) are lint-exempt (header + `.golangci.yml`); a
      hand-written `any` is still flagged (fixture).
- [ ] The generated `StrictServerInterface` is implementable with typed req/resp; a missing op fails to
      compile; `client-server-roundtrip` passes over `httptest`.
- [ ] `error-is-problem-json`: a `fault.Error` renders as RFC 9457 with the Kind-derived status.
- [ ] Only Apache-2.0/MIT deps added (oapi-codegen + runtime, chi, kin-openapi); `go.mod`/`go.sum` tidy.
- [ ] Every Scenario has a named, passing test; no identity/path leak; ADR substance unchanged.

## Consequences

- (+) Server, client, SDK, and CLI are generated from one contract — they cannot drift from the spec or
      each other; the API server (P-L) just implements a typed interface.
- (+) `x-go-type` keeps ADR-0003's single source of truth: the typed model flows onto the wire with no
      converter and no duplicate structs.
- (+) "OpenAPI matches the Go types" becomes a passing test (the shape-drift check), not a hope —
      closing ADR-0003's deferred concern.
- (+) Closes ADR-0002's deferred generated-code policy with an executable lint-exemption proof.
- (−) Resource shapes are authored twice (YAML + Go), reconciled by the drift check — accepted cost of
      keeping both a human-readable wire contract and a canonical Go model.
- (−) A runtime router dependency (chi) enters, against the embed-first default — justified by
      middleware ergonomics; chi is tiny, MIT, and dependency-free.
- (risk) OpenAPI 3.0.x ceiling until oapi-codegen supports 3.1 (workaround logged).
- (risk) Generated-code churn on oapi-codegen upgrades — pinned via `tool`, surfaced by the staleness
      gate.

## Open questions

| Question | Where it gets answered |
|---|---|
| REST `watch` (streaming) + list pagination (`limit`/`continue`) | a follow-up API ADR once the store watch (P-C/F05) + controller (P-J/F08) exist |
| Per-feature subresources (`invoke`, `logs`, `rollout`, shape-`validate`) | their feature ADRs (P-M/F13, P-R/F18) extend the spec |
| Authn/RBAC/admission/quota wiring on the server | the API-server ADR (P-L/F07) |
| proto/gRPC codegen (buf) for the multi-node worker/control-plane seams | a multi-node ADR (V2) |
| Whether a reliable Go→OpenAPI schema generator could end the double-authoring | revisit at v1 graduation (workaround exit) |

## References

- [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) (Apache-2.0) — strict-server, chi,
  `x-go-type`/`x-go-type-import`; [v2.4.0 notes](https://github.com/oapi-codegen/oapi-codegen/releases/tag/v2.4.0)
  (std-http Mux, import-mapping). OpenAPI 3.1 not yet fully supported → 3.0.x.
- [go-chi/chi](https://github.com/go-chi/chi) (MIT); [getkin/kin-openapi](https://github.com/getkin/kin-openapi) (MIT).
- RFC 9457 (problem+json); OpenAPI 3.0.3 spec.
- [ADR-0003](0003-resource-model-and-api-typing.md) (canonical types; deferred the drift gate here),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (`api/fault`; deferred generated-code policy
  here), [blueprint.md](../../blueprint.md) — "API-first", "Control-plane API & IaC", "funcdcli".
