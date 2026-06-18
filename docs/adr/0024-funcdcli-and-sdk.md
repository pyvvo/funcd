# ADR-0024: `funcdcli` + Go SDK — the client-access layer (`pkg/sdk`, `cmd/funcdcli`)

- **Status**: Implemented
- **Date**: 2026-06-14 (**Implemented 2026-06-14** · **Accepted 2026-06-14** after judge pass — no Blockers left open. Folded the judge's
  **Blocker** (B1: `v1.NewObject` is `(Object, bool)` + the kind accessor is `GroupVersionKind()`, not a
  `GetGVK` — contract made compile-true) and three **Majors**: M1 `problemToFault` keys on the JSON `status`
  field, **not** Content-Type, since handler faults arrive as `application/json` and only huma-422/401 as
  `application/problem+json`; M2 `Apply` = PUT `/{name}` then POST the collection on a 404 (+ test covers
  create *and* replace); M3 a `kindDescriptor` drift-guard test over `v1.AllKinds()`. Minors: per-element
  list allocation, unknown-`kind` manifest → `fault.Invalid`, roadmap-edge phrasing (`P-L` = ADR-0018).
  Added the `Scope` / `Constraints & Decision drivers` / `Temporary workarounds` headings. Decision
  unchanged: a stdlib-only typed kind-parameterized SDK + a stdlib kubectl-style CLI; cobra/YAML/generated-client/
  artifact-pre-flight deferred. No new deps.)
- **Deciders**: green-0-rabbit
- **Tags**: cli, sdk, client, kubectl-style, openapi, dx, public-surface
- **Realizes**: [FEAT-0000/F18](../feat/0000-feat-v1.md) (`funcdcli` + Go SDK over the generated client — kubectl-style verbs)
- **Relates to**: [ADR-0005](0005-api-surface-codefirst-huma.md) (the code-first huma control-plane REST API +
  the generated OpenAPI contract the SDK targets), [ADR-0018](0018-api-server-authn-rbac-admission.md) (the
  mounted API server — `Handlers`/`RegisterRoutes`, the `/apis/funcd.io/v1alpha1/...` path scheme + the
  RFC 9457 `problem+json` errors the SDK maps), [ADR-0002](0002-source-code-conventions-and-patterns.md)
  (functional-options facade, `api/fault`, typed surface, no `any`), [ADR-0003](0003-resource-model-store-watch.md)
  (the `api/types/v1alpha1` resource model + the shared `Validate()` the CLI pre-flights with)

## Context & Need

FEAT-0000/F18 wants the platform usable **from outside the process**: a Go **SDK** and a **`funcdcli`**
with kubectl-style verbs. ADR-0005/0018 already expose the control plane as a code-first huma REST API at
`/apis/funcd.io/v1alpha1/...` (15 kinds × {list, create, get, replace, delete}, errors as RFC 9457
`application/problem+json`). What's missing is the **client** half: a typed Go client that speaks that API,
and a thin command shell over it.

The blueprint fixes the shape (Layout): `pkg/sdk/` is "an ergonomic wrapper over the generated client";
`cmd/funcdcli/main.go` is a "separate CLI; **depends on `pkg/sdk` only**"; `api/openapi/generated/` is
"reserved for a future generated client (P-R/F18)". And the shape-validator note (blueprint §"Shape
enforcement"): the CLI does a local **pre-flight** before upload, from the **same validator the server
uses** ("single implementation imported by CLI and server — no drift; lives in the public surface so
`funcdcli` can use it"). The import-discipline rule (`api/**` is the only thing the SDK/CLI may depend on)
is the binding constraint.

This ADR decides that client layer. It is **one decision at one altitude** — "how an external caller drives
the API" — delivered as a layered pair (`pkg/sdk` library + `cmd/funcdcli` its command shell), exactly as
F18/P-R/the blueprint bundle them.

## Scope

- **In**: a typed Go SDK (`pkg/sdk`) over the ADR-0018 REST surface (CRUD for all 15 kinds, `fault`-mapped
  errors); a `funcdcli` with kubectl-style `get`/`describe`/`apply`/`delete`; the offline envelope
  pre-flight from the shared `api/types` validator.
- **Out**: see *Out of scope (V1)* below — cobra, YAML manifests, the artifact static-analysis pre-flight,
  label selectors / `get all` / cascade delete, per-kind typed sugar, the OpenAPI-generated client layer,
  watch/streaming, and auth-token minting. Each is a named follow-up.

## Constraints & Decision drivers

- **Import discipline** (blueprint): the SDK + CLI may depend on `api/**` only (+ stdlib); `cmd/funcdcli`
  "depends on `pkg/sdk` only". This is the dominant driver — it rules out reaching into `internal/**` and
  pushes toward a thin, dependency-light shell.
- **No `any`** (ADR-0002 §7 / forbidigo `\bany\b`) **and** Go forbids type-parameterized methods — together
  these rule out a generic `Get[T]` and push to the kind-parameterized `v1.Object` core.
- **No-drift validator** (blueprint §"Shape enforcement"): the CLI pre-flight must be the *same* `Validate()`
  the server uses, which already lives in `api/types` (public surface) — so the CLI reuses it, never forks it.
- **Minimal shipped dependencies** (the platform value + this batch's posture): prefer stdlib over a new
  runtime dependency where the stdlib path is adequate.
- **Faithful to the real server** (ADR-0018): the path scheme, the `Output.Body` shapes, and the dual
  error Content-Types are facts of the running server — the SDK conforms to them, proven by round-trip tests.

## Scenarios

- **scenario: sdk-applies-and-gets** — *Given* a running control plane, *when* a caller `Apply`s a `Function`
  through the SDK and then `Get`s it, *then* the returned object round-trips (name/namespace/spec preserved).
- **scenario: sdk-lists** — *Given* two applied `Function`s in a namespace, *when* the caller `List`s that
  kind, *then* both are returned.
- **scenario: sdk-deletes** — *Given* an applied `Function`, *when* the caller `Delete`s it and then `Get`s
  it, *then* the `Get` returns `fault.NotFound`.
- **scenario: sdk-maps-error-to-fault** — *Given* no such function, *when* the caller `Get`s it, *then* the
  SDK returns a typed `fault.Error` of kind `NotFound` (the `problem+json` body mapped back to the kernel),
  not a bare HTTP-status string.
- **scenario: cli-apply-then-get** — *Given* a JSON manifest, *when* `funcdcli apply -f m.json` then
  `funcdcli get function <name> -n <ns>` run against the server, *then* apply reports success and get prints
  the function's name.
- **scenario: cli-delete** — *Given* an applied function, *when* `funcdcli delete function <name> -n <ns>`
  runs, *then* it succeeds and a subsequent `get` reports a not-found error (non-zero exit).
- **scenario: cli-validates-before-apply** — *Given* a structurally-invalid manifest (e.g. missing the
  required `resourceGroup` on a namespaced kind), *when* `funcdcli apply` runs, *then* it fails locally with
  a validation error and **never** calls the server (the shared `api/types` pre-flight).

## Decision

A **stdlib-only, typed** client layer in two packages, on the public surface, depending only on `api/**`.

### 1. `pkg/sdk` — the typed Go client (kind-parameterized, not generic)

`New(baseURL string, opts ...Option) (*Client, error)` (functional-options facade per ADR-0002:
`WithHTTPClient`, `WithToken`). The CRUD surface is **parameterized by `v1.Kind`** and typed via the
`v1.Object` interface — **no `any`, no Go generics** (method type-params are illegal anyway; the
`\bany\b` ban forbids the generic-constraint form). This mirrors the server's own `handlers.go`
(non-generic `v1.Object` helpers + caller-side type assertion):

```go
type Client struct { /* baseURL, httpClient, token */ }

func New(baseURL string, opts ...Option) (*Client, error)

// Apply create-or-replaces obj. It PUTs to the named path /…/<plural>/{name}
// (ADR-0018's replace = read-RV-then-Update); on a 404 (fault.NotFound — the object
// does not yet exist) it POSTs to the collection path /…/<plural> to create it. It
// ensures TypeMeta is set and derives kind/namespace/name from obj via
// obj.GroupVersionKind()/obj.GetObjectMeta(). Returns the server's stored object.
func (c *Client) Apply(ctx context.Context, obj v1.Object) (v1.Object, error)

func (c *Client) Get(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
func (c *Client) List(ctx context.Context, kind v1.Kind, ns v1.NamespaceName) ([]v1.Object, error)
func (c *Client) Delete(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) error
```

- **URL building** — a `kindDescriptor` table (`kind → {plural, namespaced}`) is the client's contract
  knowledge of the path scheme (`/apis/funcd.io/v1alpha1/namespaces/{ns}/<plural>` for namespaced kinds;
  `/apis/funcd.io/v1alpha1/<plural>` for the four cluster-scoped kinds — `Namespace`, `RuntimeClass`,
  `Worker`, `Gateway`). The plurals are taken verbatim from ADR-0018's routes (`egresspolicies`,
  `runtimeclasses`, … — not naively derived). **Drift guard**: a contract test asserts every
  `v1.AllKinds()` has a `kindDescriptor` entry (so a future kind can't silently lack a route).
- **Typed decode** — responses decode into the concrete kind via `obj, ok := v1.NewObject(kind)` (the real
  two-value form — `ok=false` on an unknown kind → `fault.Invalid`; `NewObject` itself stamps `TypeMeta`).
  The Body is the resource itself for get/create/replace; **list** is a bare JSON array (huma's
  `Output.Body []v1.X`), so the SDK allocates **each element** via `v1.NewObject(kind)` then unmarshals into
  it — you cannot `json.Unmarshal` directly into a `[]v1.Object` interface slice.
- **Error mapping** — a non-2xx response is mapped back to a `fault.Error` by decoding the **JSON `status`
  field** of the body (`problemToFault`: `404→NotFound, 400→Invalid, 409→Conflict, 401→Unauthorized,
  403→Forbidden, 503→Unavailable, else Internal` — the exact inverse of `fault`'s `kindProblem` map). It
  keys on `status`, **not on the Content-Type**, because the server emits **handler** faults as
  `application/json` (huma's `faultError.ContentType` → `application/json`) and only its own structural-422 /
  middleware-401 as `application/problem+json`; both bodies carry `status`. The decoder tolerates huma's
  extra `errors[]` field. Callers branch on `fault.KindOf`, never on HTTP codes.
- `api/openapi/generated/` **stays reserved** (documented): V1 hand-writes the ergonomic client directly;
  an OpenAPI-**generated** low-level layer underneath this surface is a later swap (it needs a codegen tool
  + generated bulk — out of scope, and the hand-written client is the "ergonomic wrapper" the blueprint names).

### 2. `cmd/funcdcli` — the kubectl-style CLI (stdlib, depends on `pkg/sdk` only)

A `package main` with a **testable core** `run(ctx, args []string, out io.Writer, c *sdk.Client) error`
(the `main` shell parses `--server`/`$FUNCD_SERVER`, builds the `*sdk.Client`, calls `run`, maps a returned
error to a non-zero exit). Verbs (stdlib `flag` + manual subcommand dispatch — **no cobra**, honoring
"depends on `pkg/sdk` only"):

- `funcdcli get <kind> [name] [-n ns] [-o json]` — list a kind or get one (table by default; `-o json` for the object).
- `funcdcli describe <kind> <name> [-n ns]` — the full object (JSON).
- `funcdcli apply -f <file.json>` — read a JSON manifest → resolve its `kind` string to `v1.Kind` →
  `obj, ok := v1.NewObject(kind)` (an unknown `kind:` → `fault.Invalid`, never a panic) → unmarshal the
  manifest into `obj` → **`obj.Validate()` pre-flight** (the shared `api/types` validator — blueprint's CLI
  gate, offline, before any network) → `client.Apply`.
- `funcdcli delete <kind> <name> [-n ns]`.

`describe` is `get -o json` for V1 (full object, no table). Kind tokens (`function`/`functions`/`fn`)
resolve through the same `kindDescriptor` table (aliases included); an unknown token → `fault.Invalid`.

### Out of scope (V1 — bounded, documented deferrals)

- **cobra / a command framework** — stdlib verbs deliver the kubectl-*style* surface; cobra (already in the
  module graph) is a clean later swap if the command set grows. Honors "depends on `pkg/sdk` only".
- **YAML manifests** — `apply` is JSON-only in V1 (stdlib, no new dep); YAML is a follow-up (`yaml.v3` is
  already vendored).
- **The artifact static-analysis pre-flight** (esbuild JS export-analysis, Python wheel structure) — the
  blueprint's *other* CLI gate. It is the runtime/shim lane (P-S, Linux); V1's pre-flight is envelope
  `Validate()`. The function never trusts the CLI anyway (the authoritative gate is materialization).
- **Label-selector / `-l`, `get all`, resource-group cascade delete** — CLI ergonomics over the same SDK;
  follow-ups once the server exposes the corresponding list filters.
- **Per-kind typed SDK sugar** (`GetFunction(...) (*v1.Function, …)`) — the kind-parameterized core is the
  V1 surface; typed wrappers are additive.
- **Watch/streaming, server-side apply, auth token issuance** — later (the SDK carries a bearer token; how
  it's minted is ADR-0018's lane).

## Contracts

### `pkg/sdk` (`pkg/sdk/sdk.go`, `pkg/sdk/kinds.go`)
```go
type Option func(*Client)
func WithHTTPClient(h *http.Client) Option
func WithToken(token string) Option

func New(baseURL string, opts ...Option) (*Client, error) // baseURL required; defaults http.DefaultClient

func (c *Client) Apply(ctx context.Context, obj v1.Object) (v1.Object, error)
func (c *Client) Get(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
func (c *Client) List(ctx context.Context, kind v1.Kind, ns v1.NamespaceName) ([]v1.Object, error)
func (c *Client) Delete(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) error

// kindDescriptor is the client's knowledge of the path scheme (one entry per v1 kind).
type kindDescriptor struct { plural string; namespaced bool }
```

### `cmd/funcdcli` (`cmd/funcdcli/main.go`, `cli.go`)
```go
func main() // parses --server/$FUNCD_SERVER, builds *sdk.Client, os.Exit(code from run)
func run(ctx context.Context, args []string, out io.Writer, c *sdk.Client) error // testable core
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/types/v1alpha1`, `api/fault`, stdlib `net/http`/`encoding/json`/`flag`/`io`/`context` | SDK + CLI import **only** `api/**` + stdlib (import discipline) |
| Adds (lib) | none | stdlib HTTP client; stdlib `flag` CLI |
| Exposes | `pkg/sdk.Client` (+ `New`/`Apply`/`Get`/`List`/`Delete`/`Option`); `funcdcli` binary | `cmd/funcdcli` depends on `pkg/sdk` only |
| Test-only | `internal/controlplane`, `internal/store/memory` | scenario tests mount the **real** server on `httptest` and drive it through the SDK/CLI (no mocks) |

## Implementation plan

1. **`pkg/sdk/kinds.go`** — the `kindDescriptor` table (all 15 kinds → plural + namespaced) + a
   `kindFromToken(string) (v1.Kind, bool)` resolver (singular/plural/alias).
2. **`pkg/sdk/sdk.go`** — `Client`, `New`, `Option`s, the 4 CRUD methods, URL building, typed decode via
   `v1.NewObject`, and `problemToFault` (status→kind) error mapping.
3. **`cmd/funcdcli/cli.go`** — `run(ctx,args,out,c)`: subcommand dispatch (`get`/`describe`/`apply`/`delete`),
   `flag.FlagSet` per verb (`-n`, `-o`, `-f`), manifest read + `Validate()` pre-flight, output rendering.
4. **`cmd/funcdcli/main.go`** — the thin shell (`--server`/env → `sdk.New` → `run` → `os.Exit`).
5. **Test plan** (one named test per Scenario; real server mounted on `httptest`, no mocks):
   - `pkg/sdk/sdk_test.go` → `sdk-applies-and-gets` (covers **both** the *first* apply = create via POST and a
     *second* apply of the same object = replace via PUT), `sdk-lists`, `sdk-deletes`, `sdk-maps-error-to-fault`.
   - `pkg/sdk/kinds_test.go` → `kinddescriptor-covers-all-kinds` (the M3 drift guard: every `v1.AllKinds()`
     has a descriptor).
   - `cmd/funcdcli/cli_test.go` → `cli-apply-then-get`, `cli-delete`, `cli-validates-before-apply` (asserts
     both a missing-`resourceGroup` manifest **and** an unknown-`kind` manifest fail locally, no network).
6. **Definition of done**: `just ci` green (four sub-checks); the SDK round-trips every CRUD verb against the
   real control plane and maps errors to `fault`; the CLI applies/gets/deletes and pre-flights invalid
   manifests offline; **no new dependency**; SDK/CLI import only `api/**` + stdlib; no globals; no `any`; no leak.

## Review checklist

- [ ] **SDK CRUD round-trips** the real control plane: `Apply`→`Get` preserves the object
      (`sdk-applies-and-gets`); `List` returns all (`sdk-lists`); `Delete` then `Get`→`NotFound`
      (`sdk-deletes`). Kind-parameterized, typed via `v1.Object`/`v1.NewObject` — **no `any`**.
- [ ] **Errors map to `fault`** (`sdk-maps-error-to-fault`): a non-2xx → `fault.Error` of the right `Kind`,
      decoded from the body's **`status` field** (works for both the `application/json` handler-fault shape and
      the `application/problem+json` huma-422/401 shape); callers never see a raw status code.
- [ ] **Kind table has no gaps** (`kinddescriptor-covers-all-kinds`): every `v1.AllKinds()` has a
      `kindDescriptor` (plural + namespaced) — the drift guard against the server's routes.
- [ ] **CLI verbs** over the SDK (`cli-apply-then-get`, `cli-delete`): `get`/`describe`/`apply`/`delete`
      with `-n`/`-o`/`-f`; `run()` is testable; `main` is a thin shell. **No cobra** — imports `pkg/sdk` only.
- [ ] **CLI pre-flight** (`cli-validates-before-apply`): an invalid manifest fails on the shared
      `api/types` `Validate()` **before** any network call.
- [ ] Import discipline: SDK + CLI import only `api/**` + stdlib (tests may mount `internal/controlplane`);
      `New(...Option)`, ctx-first, `api/fault`, no globals, **no `any`**, **no new dependency**; deferrals
      (cobra, YAML, artifact pre-flight, selectors, typed sugar, generated client) documented; no leak; every
      Scenario a named passing test.

## Consequences

- (+) **The platform is drivable from outside**: a typed Go SDK + a kubectl-style CLI over the real API —
  F18, and the exit criterion's "all via API/CLI". The critical-path item `P-L → P-R` advances.
- (+) **Zero new dependency, import-clean**: stdlib HTTP + stdlib `flag`; SDK/CLI touch only `api/**`. The
  one shared validator (`api/types`) gives the CLI its offline pre-flight with no drift from the server.
- (+) **Kind-parameterized core** (4 methods, all 15 kinds) instead of 75 hand-written per-kind methods —
  small, faithful to `handlers.go`, and typed (`v1.Object`, no `any`); typed sugar stays additive.
- (−) **JSON-only `apply`, stdlib-flag UX, no `-l`/cascade** — bounded V1; cobra + YAML + selectors are
  named follow-ups (cobra/`yaml.v3` already vendored, so cheap later).
- (−) **The artifact static-analysis pre-flight is deferred** to the runtime/shim lane (P-S); V1's pre-flight
  is envelope validation — honest, since the authoritative shape gate is materialization, not the CLI.
- (note) **Roadmap build edges**: P-R's edges in `plan.json` are `ADR-0005` (the API surface) and `P-L`
  (= ADR-0018, the mounted server + path scheme + `problem+json`, which itself rests on `ADR-0003`'s resource
  model + `Validate`). It builds on **none** of bus/gateway/runtime. Step-6 reconciles the `P-L` placeholder
  → `ADR-0018` and graduates this ADR as 0024.

## Temporary workarounds

None. The V1 cuts (cobra, YAML, the artifact pre-flight, selectors, typed sugar, the generated-client layer)
are bounded scope reductions with named follow-ups — not stopgaps masking an unfinished decision. The
hand-written client is the intended `pkg/sdk` surface, not a placeholder.

## Alternatives considered

- **OpenAPI-generated client (oapi-codegen) under `api/openapi/generated/`** — the blueprint reserves that
  dir for exactly this. Rejected for V1: it adds a codegen tool + generated bulk for a code-first-huma server
  that ships no first-party client generator, when a hand-written typed client over 15 well-known kinds is
  smaller and fully conforms to the same OpenAPI contract. The dir stays reserved; this client is the
  "ergonomic wrapper" that would sit over a generated layer if one is ever added.
- **cobra/urfave CLI framework** — the de-facto kubectl-style framework (cobra is already in the module
  graph). Rejected for V1 in favor of the blueprint's "depends on `pkg/sdk` only" thinness + the batch's
  no-new-shipped-dep posture; it's a clean swap if the command surface grows. (Not strawmanned: cobra is the
  *better* UX — the trade is dependency-weight vs. a handful of `flag.FlagSet`s, and V1 takes the thin side.)
- **75 per-kind typed methods (`GetFunction`, …) mirroring `Handlers`** — most ergonomic, but a large
  hand-written surface duplicating the kind list. Rejected as the *core*; offered later as additive sugar
  over the kind-parameterized methods (which are the irreducible transport).
- **Go generics (`Get[T v1.Object]`)** — would give compile-time typed returns, but Go forbids type-parameterized
  *methods* (so it couldn't hang off `*Client`), and the constraint form trips the `\bany\b`-adjacent ban
  posture; the `v1.Object` + caller-assert shape is what the codebase already uses (`handlers.go`).

## Open questions

| Question | Where it gets answered |
|---|---|
| The exact create-vs-replace semantics of `Apply` | V1: PUT `/…/{name}`; on `fault.NotFound` (404) POST the collection path `/…/<plural>`. A server-side-apply endpoint is a later API addition (ADR-0005 lane) |
| Auth token acquisition (the SDK *carries* a bearer token; who *mints* it) | ADR-0018 (authn) — `login`/token issuance is a follow-up |
| YAML manifests + `-l` selectors + `get all` + resource-group cascade | CLI follow-ups over the same SDK (server list-filters first) |
| The artifact (JS/Python) static-analysis pre-flight | the runtime shim lane (P-S / Linux) |

## References

- [blueprint.md](../../blueprint.md) — Layout (`pkg/sdk`, `cmd/funcdcli` "depends on `pkg/sdk` only",
  `api/openapi/generated/` reserved); "Shape enforcement" (the CLI pre-flight from the shared validator);
  the import-discipline rule (`api/**` is the only SDK/CLI dependency).
- [ADR-0005](0005-api-surface-codefirst-huma.md) — the code-first huma API + generated OpenAPI the SDK targets.
- [ADR-0018](0018-api-server-authn-rbac-admission.md) — the mounted server, the `/apis/funcd.io/v1alpha1/...`
  path scheme, and the RFC 9457 `problem+json` errors the SDK maps to `fault`.
