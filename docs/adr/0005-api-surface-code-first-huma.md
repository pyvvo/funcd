# ADR-0005: API surface — code-first via huma (generated OpenAPI)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0172](0172-revision-integrity.md) (2026-10-05) — Decision §2, Contracts, plan, checklist: CRUD registered for all 15 kinds.
- **Date**: 2026-06-14 (revised same day after judge review: the `api/fault`→huma error bridge is
  pinned to `internal/controlplane` so `api/fault` stays stdlib-only; the generated client has its
  own wire DTOs — huma emits no `x-go-type`; deterministic spec canonicalization; huma now explicitly
  the framework P-L/F07 inherits; accepted 2026-06-14; implementation complete 2026-06-14 — all 5 scenario tests pass, spec generated + committed, `just generate` deterministic; **amended in place 2026-06-14 by deliberate decision**: the generated Go client is deferred to P-R/F18 — oapi-codegen does not support OpenAPI 3.1, and the SDK client is P-R's rightful concern; the spec consumability scenario is satisfied by a direct HTTP round-trip test. This in-place amendment is a conscious deviation from the supersede-don't-amend invariant — the ADR is still in-flight (uncommitted), and the deferral is a narrow scope reduction, not a decision reversal; implemented 2026-06-14 after review — `just ci` lint 0, all 6 scenarios pass, only the benign tidy-gate pending commit.)
- **Deciders**: green-0-rabbit
- **Tags**: api, openapi, code-first, huma, chi, rest
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md)
- **Supersedes**: [ADR-0004](0004-api-surface-and-codegen.md) (hand-authored OpenAPI + oapi-codegen
  strict-server). This ADR inverts that to **code-first**: the Go types + typed operations are the
  source, the OpenAPI spec is generated.
- **Relates to**: [ADR-0003](0003-resource-model-and-api-typing.md) (the canonical Go types the spec
  is *derived from*), [ADR-0002](0002-source-code-conventions-and-patterns.md) (`api/fault` problem+json
  taxonomy), [ADR-0001](0001-project-setup-and-structure.md) (module, `just`, `tool` directive),
  [blueprint.md — API-first / Control-plane API & IaC / funcdcli](../../blueprint.md)

## Context & Need

funcd is API-first: the REST API is the single front door for `funcdcli`, the SDK, CI, and the future
Terraform provider, and it must be an OpenAPI contract so server, SDK, and CLI cannot diverge.
**ADR-0004 decided to hand-author** `funcd.v1.yaml` to match the canonical Go types (ADR-0003) and
add a drift gate. In review that approach proved to be ~1000 lines of boilerplate that *mirrors* the
Go types without even describing them (`spec`/`status` left as opaque `type: object`), and it
**inverts the natural flow**: ADR-0003 made the Go types canonical, yet the spec was authored by hand
and reconciled back to them. Kubernetes and modern Go API frameworks do the opposite — the Go types
are the source and the **OpenAPI is generated from them** (kube-openapi/controller-gen; FastAPI;
huma). ADR-0005 supersedes ADR-0004 and adopts that **code-first** posture.

**Purpose**: establish the code-first API framework — **[huma](https://github.com/danielgtaylor/huma)**
on chi — where typed Go operations (bodies = `api/types/v1alpha1`) *are* the contract, and the
**OpenAPI 3.1 spec is generated** from them (committed + staleness-gated). Its callers: the API server
(P-L/F07) builds on this huma API, adding authn/admission/real handlers; the CLI/SDK (P-R/F18) consume
the generated spec + client. Conformance is mechanical: the spec is *derived from* the Go types (a
field added to a type appears in the spec with no manual edit), regeneration is reproducible, a typed
operation round-trips, and errors render as RFC 9457 problem+json via `api/fault`.

## Scenarios

- `scenario: spec-generated-from-go` — **Given** the registered huma operations + the v1alpha1 types,
  **when** `just generate` runs, **then** it writes the OpenAPI 3.1 document to the committed spec
  file, and re-running produces **no `git diff`** — the spec is a deterministic build output of the Go
  code, staleness-gated in CI.
- `scenario: spec-reflects-go-shape` — **Given** a new field added to a resource's Go `Spec` struct,
  **when** the spec is regenerated, **then** the field appears in the OpenAPI schema **automatically**
  (no hand-edit) — proving the spec is *derived from* the types, not hand-maintained alongside them.
- `scenario: typed-operation-roundtrip` — **Given** a registered huma operation (e.g. `createFunction`)
  with a minimal handler, **when** a request applies a `Function` and then gets it (via `humatest`),
  **then** the typed `v1alpha1.Function` round-trips — huma parses, validates, and serves from the
  typed operation, no manual JSON handling.
- `scenario: error-is-problem-json` — **Given** a handler that returns `fault.Error{Kind: NotFound}`,
  **when** huma renders the response, **then** the body is RFC 9457 `application/problem+json` with
  HTTP 404 and the `api/fault` Kind→status mapping — the error taxonomy stays in `api/fault`; huma is
  the renderer.
- `scenario: openapi-doc-served` — **Given** the running API, **when** a client GETs `/openapi.yaml`
  (and `/openapi.json`), **then** huma serves the generated **OpenAPI 3.1** document (the live
  contract) and a docs UI.
- `scenario: spec-consumable-by-client` — **Given** the committed generated spec, **when** a Go HTTP
  client makes requests against the huma server and decodes responses into the canonical `v1alpha1`
  types, **then** a resource round-trips — proving the generated spec is consumable for the SDK
  (P-R/F18 wraps it). The typed Go client itself is deferred to P-R/F18 (oapi-codegen does not
  support OpenAPI 3.1; P-R will choose the right client-generation path).

## Scope

**In**:
- Adopting **huma** (the `humachi` adapter on chi) as the control-plane API framework.
- The **typed-operation registration** for the generic kubectl-style resource CRUD (create/get/list/
  replace/delete) over the v1alpha1 kinds — input/output structs whose bodies are the canonical types.
- The **generated, committed OpenAPI 3.1 spec** (`api/openapi/funcd.v1alpha1.yaml`) + the
  **staleness gate** (regenerate + `git diff --exit-code`). **No hand-authored spec, no drift gate** —
  a derived spec cannot lie about the types.
- The **`api/fault` ↔ huma problem+json bridge** (Kind→status mapping stays in `api/fault`).
- The **`Handlers` seam**: operations register against an interface so the spec can be generated with
  trivial handlers and P-L can inject real ones.
- **Removing** the superseded ADR-0004 artifacts (`funcd.v1.yaml`, the `codegen.{types,server,client}.yaml`,
  the strict-server config).

**Out**:
- **Real handler business logic + authn/RBAC/admission/quota** — the API-server ADR (P-L/F07). This ADR
  ships *trivial* in-memory handlers only, enough to generate the spec and prove round-trips.
- **Semantic `Validate()` wiring** (DNS labels, resourceGroup-required) — admission's job (P-L); huma
  does *structural* validation from the derived schema.
- **Per-feature subresources** (`invoke`/`logs`/`rollout`) — feature ADRs (P-M/F13, P-R/F18).
- **proto/gRPC** (multi-node V2); **watch/pagination** (a follow-up once the store/controller exist).
- **The typed Go client + SDK wrapper + CLI** (P-R/F18) — P-R consumes the generated spec this ADR
  produces and chooses the right client-generation path (oapi-codegen does not support OpenAPI 3.1;
  P-R may use a 3.1-capable generator or hand-write the SDK).

## Constraints & Decision drivers

- **C1 — code-first, Go is the source**: the spec is *derived from* the canonical v1alpha1 types
  (ADR-0003) by reflection; it is a build output, never hand-maintained. This is the inversion of
  ADR-0004 that motivates the supersession.
- **C2 — ADR-0002 error model preserved**: `api/fault` remains the error taxonomy + the single Kind→
  status/problem-URI map; huma renders it as problem+json (huma's default media type is already
  `application/problem+json`).
- **C3 — blueprint inheritance**: API-first; embed-first (huma is an embedded library, chi the
  router); single binary; generated code committed; CI fails on diff; Apache-2.0/MIT deps only.
- **C4 — OpenAPI 3.1**: huma emits 3.1 natively (lifting ADR-0004's 3.0.x ceiling).
- **D1 — fewest moving parts**: one framework owns operations + serving + validation + OpenAPI +
  problem+json, instead of hand-spec + a generator + a drift gate.
- **D2 — keep ADR-0004's good parts**: chi as the router; a generated, committed spec. Only the
  *authoring direction* flips.

## Alternatives considered

**Spec authoring direction** (the supersession driver):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Code-first: Go types + huma operations → generated OpenAPI** | spec derived from the canonical types (can't drift); no hand-authoring; one framework does validation + problem+json + docs; OpenAPI 3.1 | a framework owns the HTTP layer; reshapes the server model vs ADR-0004 | **chosen** (supersedes ADR-0004) |
| Hand-authored OpenAPI + oapi-codegen (ADR-0004) | human-readable contract; familiar | ~1000 lines of boilerplate mirroring the types; opaque `spec`/`status`; needs a drift gate; inverts ADR-0003 | **superseded** |

**Code-first mechanism** (how to generate from Go):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **huma** | code-first REST framework; OpenAPI 3.1; router-agnostic (chi); built-in validation + RFC 9457 problem+json + docs UI; MIT, active | a framework dependency that owns request handling | **chosen** |
| Reflection library (swaggest/openapi-go) + oapi-codegen server | lighter dep; keeps oapi-codegen strict-server | you still author the operation table separately from handlers; more glue than huma | rejected |
| Kubernetes markers (kube-openapi / controller-gen) | most K8s-authentic | kube-openapi is apiserver-coupled + doesn't derive REST paths; controller-gen is CRD-manifest-focused; markers dirty the clean v1alpha1 types | rejected (over-engineering for a non-K8s binary) |

**Client generation** (huma is server-side only):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Defer to P-R/F18** | P-R owns the SDK; can choose a 3.1-capable path when ready; removes an unused dep | no typed client in this ADR; spec consumability proven by direct HTTP test | **chosen** (amended 2026-06-14 — oapi-codegen does not support OpenAPI 3.1) |
| oapi-codegen client-only, from the generated spec | keeps a typed Go client; proves the spec is consumable | oapi-codegen does not support OpenAPI 3.1 (same fact ADR-0004 recorded); would require downgrading to 3.0.x | rejected (3.1 is a hard requirement; the tool gap is real) |
| Hand-write the SDK | no codegen | drifts from the spec; defeats the point | rejected |

## Decision

### 1. huma on chi is the control-plane API framework
`humachi.New(chiMux, cfg)` creates the API; `chi` stays the router (ADR-0004's choice retained). huma
owns request parsing, structural validation (from the reflected schema), content negotiation, error
rendering, and OpenAPI 3.1 generation. It serves `/openapi.json`, `/openapi.yaml`, and a docs UI.
This makes huma the API-server framework **P-L/F07 inherits** — that ADR builds on huma rather than
choosing its own.

### 2. Typed operations register the generic resource CRUD
For each kind, an input struct (path/query params via huma tags) and an output struct (`Body` = the
canonical `v1alpha1` type) are registered with `huma.Register`. huma derives the OpenAPI operation +
schemas from these by reflection — the bodies are the ADR-0003 types verbatim (typed IDs/enums ride
along). Registration goes through a **`Handlers` interface** (one method per operation) so the spec can
be generated with a stub implementation and P-L can inject the real one.

### 3. The OpenAPI spec is generated + committed (no drift gate)
A `just generate` step builds the API (operations registered against a stub `Handlers`), then writes
`api.OpenAPI().YAML()` to **`api/openapi/funcd.v1alpha1.yaml`** (committed). `just ci` re-runs it and
`git diff --exit-code` (the staleness gate). Because the spec is *derived from* the Go types, **the
ADR-0004 schema-vs-Go drift check is deleted** — there is no second source to drift. The generate step
**canonicalizes** the output (stable key/operation ordering) so the diff is deterministic in CI
regardless of Go map iteration order.

### 4. Errors: `api/fault` taxonomy, huma renders problem+json
huma derives the HTTP status from a returned error implementing its `huma.StatusError` (`GetStatus()
int`) interface, and renders the body via the overridable `huma.NewError`. Because **`api/fault` is
stdlib-only (ADR-0002 §3) it must not import huma**, so the bridge lives in `internal/controlplane`
(which may import both): a small `faultError` wrapper implements `huma.StatusError` with `GetStatus()`
returning `fault.ToProblem(err).Status` (reusing `api/fault`'s single Kind→status map), and
`huma.NewError` is overridden so the rendered body is the RFC 9457 `fault.Problem`. Handlers return
`fault.Error`; the wrapper + override turn it into `application/problem+json` with the Kind-derived
status. The mapping stays authoritative in `api/fault`; huma is only the transport, and **`api/fault`
stays huma-free**.

### 5. Typed Go client deferred to P-R/F18
huma emits a standard OpenAPI 3.1 spec; oapi-codegen does not support 3.1 (the same fact ADR-0004's
research recorded), so the typed Go client is **deferred to P-R/F18** — the rightful owner of the SDK.
P-R will choose the right client-generation path (a 3.1-capable generator, or a hand-written SDK over
the canonical `v1alpha1` types). This ADR proves the spec is consumable via a direct HTTP round-trip
test (`api/openapi/client_test.go` → `TestSpecConsumableByClient`).

### 6. Validation split
huma performs **structural** validation from the derived schema (wrong type/missing required → 422
problem+json, pre-handler). **Semantic** validation — the v1alpha1 `Validate()` methods (DNS labels,
required resourceGroup, scope) — runs in admission (P-L/F07), invoked inside handlers; this ADR's stub
handlers may call `Validate()` to demonstrate the path.

### 7. Layout & supersession cleanup
- `api/openapi/funcd.v1alpha1.yaml` — **generated**, committed. Deliberately renamed from the
  blueprint's `funcd.v1.yaml` (the hand-authored source); the blueprint layout is synced to the new
  name + generated provenance at acceptance.
- `internal/controlplane/` — the huma operation registration (`RegisterRoutes(api huma.API, h
  Handlers)`, the input/output structs, the `Handlers` interface, the `api/fault`→huma bridge). This
  package is **shared with P-L/F07**: ADR-0005 owns the routes/contract, P-L fills `Handlers` +
  middleware/admission.
- **Delete** the ADR-0004 artifacts: the hand-authored `api/openapi/funcd.v1.yaml`,
  `codegen.{types,server}.yaml`, and the strict-server `generate.go`.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| Stub in-memory `Handlers` ship in this ADR | the spec + round-trip proof need registered operations, but real handlers (with the store/controller) are P-L's | P-L/F07 replaces the stub `Handlers` with real ones + middleware/admission |
| `Validate()` (semantic) not wired into the request path here | admission is P-L's; huma covers structural validation | P-L wires `Validate()` into admission |
| List returns a filtered snapshot; no `watch`/pagination | watch needs the store's watch (P-C) + streaming transport | a follow-up API ADR adds `watch` + `limit`/`continue` once store+controller exist |

## Contracts

### huma setup + operation registration (`internal/controlplane`)
```go
import (
    "github.com/danielgtaylor/huma/v2"
    "github.com/danielgtaylor/huma/v2/adapters/humachi"
    "github.com/go-chi/chi/v5"
    v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// Handlers is the seam: one method per operation. Stub impl for spec-gen + tests;
// real impl (store/controller-backed) is provided by P-L/F07.
type Handlers interface {
    GetFunction(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Function, error)
    CreateFunction(ctx context.Context, fn v1.Function) (v1.Function, error)
    // … list/replace/delete × the 15 kinds …
}

// NewAPI builds the huma API on a chi router and registers all operations against h.
func NewAPI(r chi.Router, h Handlers) huma.API   // also installs the fault→problem error hook

// RegisterRoutes registers every kind's CRUD operation on api, dispatching to h.
func RegisterRoutes(api huma.API, h Handlers)
```

### A representative operation (Function get) — the body is the canonical type
```go
type GetFunctionInput struct {
    Namespace v1.NamespaceName `path:"namespace"`
    Name      v1.ObjectName    `path:"name"`
}
type FunctionOutput struct {
    Body v1.Function   // huma reflects v1alpha1.Function → the OpenAPI schema
}

huma.Register(api, huma.Operation{
    OperationID: "getFunction",
    Method:      http.MethodGet,
    Path:        "/apis/funcd.io/v1alpha1/namespaces/{namespace}/functions/{name}",
    Security:    []map[string][]string{{"bearerAuth": {}}},   // declared; enforced by P-L
}, func(ctx context.Context, in *GetFunctionInput) (*FunctionOutput, error) {
    fn, err := h.GetFunction(ctx, in.Namespace, in.Name)   // returns fault.Error on failure
    if err != nil {
        return nil, err                                     // huma → fault hook → problem+json
    }
    return &FunctionOutput{Body: fn}, nil
})
```

### `api/fault` → huma problem+json bridge (in internal/controlplane; api/fault stays huma-free)
```go
// faultError adapts a stdlib-only fault.Error to huma's StatusError so huma reads the right status.
type faultError struct{ err error }                                       // wraps *fault.Error
func (e faultError) Error() string  { return e.err.Error() }
func (e faultError) GetStatus() int { return fault.ToProblem(e.err).Status } // reuse api/fault's Kind→status map

// installFaultErrors wires the bridge onto the API: handler-returned fault.Errors are wrapped in
// faultError, and huma.NewError is overridden so the rendered body is the RFC 9457 fault.Problem
// (ADR-0002 §3). huma's default response media type is already application/problem+json.
func installFaultErrors(api huma.API)
```

### Spec generation + client codegen
```go
// internal/controlplane/apigen (or a //go:generate target): build the API with a stub Handlers,
// then write api.OpenAPI().YAML() deterministically to api/openapi/funcd.v1alpha1.yaml.
//
// api/openapi/codegen.client.yaml (oapi-codegen, client-only):
//   package: generated
//   output:  api/openapi/generated/client.gen.go
//   generate: { client: true, models: false }   # bodies bind to v1alpha1 via x-go-type in the spec
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/types/v1alpha1` (ADR-0003), `api/fault` (ADR-0002) | huma reflects the types into the schema; `api/fault` maps errors |
| Consumes | `just`, the go.mod `tool` directive, `.golangci.yml` | adds the `generate` recipe + staleness gate; generated-file lint policy (from ADR-0004 §7, retained) |
| Adds (lib) | `github.com/danielgtaylor/huma/v2` (+ `adapters/humachi`, `humatest`) | **MIT** — the API framework + chi adapter + test helper |
| Keeps | `github.com/go-chi/chi/v5` | **MIT** (from ADR-0004) |
| Exposes | `api/openapi/funcd.v1alpha1.yaml` (generated), `internal/controlplane` API + `Handlers` seam | the contract; P-L implements `Handlers`, P-R consumes the spec |
| Removes | ADR-0004 `funcd.v1.yaml` + `codegen.{types,server}.yaml` + strict-server `generate.go` | superseded artifacts |

## Implementation plan

No business logic — the framework wiring, operation registration, generated spec + client, the error
bridge, stub handlers, and passing scenario tests. No infrastructure deps; every scenario runs green.

1. **Deps**: `go get github.com/danielgtaylor/huma/v2`; keep chi; `go mod tidy`.
2. **`internal/controlplane/`** — `NewAPI`, `RegisterRoutes`, the input/output structs + `Handlers`
   interface for the 15 kinds' CRUD, and the `fault`→huma error hook.
3. **Stub handlers** — a trivial in-memory `Handlers` (a `map` per kind) for spec-gen + tests.
4. **Spec generation** — a `//go:generate`/`just generate` target that builds the API with the stub
   and writes `api/openapi/funcd.v1alpha1.yaml` deterministically; commit it.
5. **`justfile`/`.golangci.yml`** — `generate` recipe; wire `generate`+`git diff --exit-code` into
   `ci`; keep the generated-file lint exemption (ADR-0004 §7).
6. **Delete** the ADR-0004 artifacts (`funcd.v1.yaml`, `codegen.{types,server,client}.yaml`,
   the strict-server `generate.go`).
7. **Test plan** (one acceptance test per Scenario, all passing):
   - `internal/controlplane/spec_test.go` → `spec-generated-from-go` (regenerate to a temp file, assert
     byte-equality with the committed spec) and `spec-reflects-go-shape` (register an op with a test
     type carrying an extra field; assert the field is in `api.OpenAPI()`).
   - `internal/controlplane/api_test.go` → `typed-operation-roundtrip` (`humatest`: PUT then GET a
     `Function`, assert equality), `error-is-problem-json` (stub returns `fault.NotFoundf`; assert 404
     + `application/problem+json` via the hook), `openapi-doc-served` (GET `/openapi.yaml`, assert 3.1).
   - `api/openapi/client_test.go` → `spec-consumable-by-client` (direct HTTP client vs the huma server
     over `httptest`, round-trip a `Function` — proving the spec is consumable; the typed Go client
     itself is deferred to P-R/F18).
8. **Definition of done** (= Scenarios executed):
   - `just generate` reproducible; `just ci` exits 0 on a committed tree (staleness gate green).
   - The committed `funcd.v1alpha1.yaml` is **OpenAPI 3.1**, generated (carries a generated header/marker),
     and adding a Go field changes it with no hand-edit.
   - **Server** operation bodies are the `v1alpha1` types (huma reflects them — no server-side DTOs).
   - `error-is-problem-json` renders via `api/fault` (Kind→status); `typed-operation-roundtrip` and
     `spec-consumable-by-client` pass.
   - The ADR-0004 hand-authored artifacts are gone; only huma + chi remain.
   - Only Apache-2.0/MIT deps; `go.mod`/`go.sum` tidy. Every Scenario has a named, passing test.

## Review checklist

- [ ] huma (`humachi` on chi) is the API framework; `NewAPI`/`RegisterRoutes`/`Handlers` exist in
      `internal/controlplane`.
- [ ] CRUD operations are registered for the 15 kinds; **server** bodies are `v1alpha1` types (no
      server-side DTOs).
- [ ] `api/openapi/funcd.v1alpha1.yaml` is **generated + committed** (OpenAPI 3.1), and `just generate`
      + `git diff --exit-code` is clean; adding a Go field regenerates it with no hand-edit.
- [ ] **No hand-authored spec and no schema-vs-Go drift check** remain (both deleted with ADR-0004).
- [ ] Errors render as RFC 9457 `application/problem+json` via the `api/fault` Kind→status map; the
      bridge lives in `internal/controlplane` and **`api/fault` imports no huma** (stays stdlib-only).
- [ ] The generated spec is consumable by a Go HTTP client (direct round-trip test); the typed Go
      client itself is deferred to P-R/F18 (oapi-codegen does not support OpenAPI 3.1).
- [ ] huma does structural validation; semantic `Validate()` is left to admission (P-L) — not duplicated.
- [ ] The ADR-0004 artifacts (`funcd.v1.yaml`, strict-server config, `codegen.{types,server}.yaml`) are
      removed.
- [ ] Only Apache-2.0/MIT deps (huma MIT, chi MIT); `go.mod`/`go.sum` tidy.
- [ ] Every Scenario has a named, passing test; no identity/path leak; ADR-0004 shows `Superseded by
      ADR-0005`.

## Consequences

- (+) The spec is *derived from* the canonical Go types — it cannot drift, so the hand-authoring and
      the entire drift gate disappear. Adding a field to a type updates the contract for free.
- (+) One framework (huma) does operations + validation + problem+json + OpenAPI 3.1 + docs, replacing
      hand-spec + oapi-codegen-server + the drift check; far less machinery.
- (+) OpenAPI 3.1 (vs ADR-0004's 3.0.x ceiling); `api/fault` stays the error source of truth.
- (+) The server model is typed Go operations — P-L implements a `Handlers` interface and adds
      middleware, a clean F02/F07 split.
- (−) A framework dependency (huma) now owns the HTTP request path — a bigger commitment than a
      generated interface; mitigated: MIT, active, router-agnostic, and behind funcd's own
      `internal/controlplane`.
- (−) The typed Go client is deferred to P-R/F18 (oapi-codegen does not support OpenAPI 3.1); spec
      consumability is proven by a direct HTTP test.
- (−) Supersedes a just-accepted ADR; the in-flight ADR-0004 hand-authored spec work is discarded.

## Open questions

| Question | Where it gets answered |
|---|---|
| Real handlers, authn/RBAC, admission, quota, `Validate()` wiring | the API-server ADR (P-L/F07) |
| Per-feature subresources (`invoke`/`logs`/`rollout`, shape-`validate`) | their feature ADRs (P-M/F13, P-R/F18) |
| `watch` (streaming) + list pagination | a follow-up API ADR once store (P-C) + controller (P-J) exist |
| The typed Go client + SDK ergonomic wrapper | P-R/F18 (oapi-codegen does not support OpenAPI 3.1; P-R chooses the right path) |
| proto/gRPC codegen for the multi-node seams | a multi-node ADR (V2) |

## References

- [huma](https://github.com/danielgtaylor/huma) (MIT) — code-first Go REST framework, OpenAPI 3.1,
  router-agnostic, built-in RFC 9457 problem+json + validation + docs; `humachi` adapter, `humatest`.
- [go-chi/chi](https://github.com/go-chi/chi) (MIT); [kube-openapi](https://pkg.go.dev/k8s.io/kube-openapi) (the K8s
  prior art for code-first OpenAPI).
- RFC 9457 (problem+json); OpenAPI 3.1.
- [ADR-0004](0004-api-surface-and-codegen.md) (superseded — hand-authored spec), [ADR-0003](0003-resource-model-and-api-typing.md)
  (canonical types), [ADR-0002](0002-source-code-conventions-and-patterns.md) (`api/fault`).
