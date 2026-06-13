# ADR-0002: Source-code conventions & architecture patterns

- **Status**: Reviewing
- **Date**: 2026-06-13 (revised same day after judge review: error kernel relocated to
  `api/fault`, `store`/`blob` port naming, generated-file lint exemption, `revive` dropped;
  accepted 2026-06-13; clarified post-acceptance that a driver folder is per-engine, not
  per-backend — one `blob/gocloud` adapter spans memory/file/S3, no `blob/memory` folder;
  and §8 file-count rule: a driver is one file in its own package, not a fan-out of files)
- **Deciders**: green-0-rabbit
- **Tags**: conventions, go-idioms, api-design, errors, linting
- **Realizes**: [FEAT-0000/F25](../feat/0000-feat-v1.md)
- **Relates to**: [ADR-0001](0001-project-setup-and-structure.md) (`.golangci.yml`, module, `just`),
  [blueprint.md — Go best practices / Platform as a library / Platform logging](../../blueprint.md)

## Context & Need

The blueprint *asserts* a house style — `funcd.New(WithStore(...))`, ports with two
drivers, wrapped sentinels + problem+json, context-first, no globals, depguard import
discipline — scattered across "Go best practices", "Platform as a library", and "Platform
logging". Nothing yet states it as one enforceable contract. Every feature ADR from F02
onward scaffolds Go code; without a single source of truth they will each re-decide
constructor shape, error handling, and interface placement, and drift apart.

**Purpose**: this ADR is the codebase rulebook. Its "caller" is a developer or an LLM
about to scaffold or implement a funcd package. It must answer, unambiguously: how do I
construct a component, where do interfaces live, how do I return and map errors, what may
cross a public/port boundary, and what will the linter reject. Conformance is observable
mechanically (it compiles, `just lint` passes, contract suites are green) — that is what
tells the implementer what to test.

## Scenarios

- `scenario: facade-missing-dep` — **Given** the public facade `funcd.New`, **when** it is
  called without a required dependency (e.g. no store) and no preset supplies one,
  **then** it returns a typed `Invalid` error naming the missing dependency — never panics
  and never returns a half-built `*Platform`.
- `scenario: error-maps-to-problem` — **Given** an internal operation that fails with a
  wrapped `fault.Error{Kind: NotFound}`, **when** it propagates to the API edge,
  **then** the client receives an RFC 9457 `application/problem+json` body with HTTP 404
  and a stable `type` URI, and the failure is logged exactly once (at the edge).
- `scenario: lint-blocks-cross-feature-import` — **Given** a package under
  `internal/features/foo` that imports `internal/features/bar`, **when** `just lint` runs,
  **then** depguard fails the build with a message pointing at the rule.
- `scenario: lint-blocks-any-leak` — **Given** a port or `api/` exported signature that
  uses `interface{}`/`any` or `map[string]any`, **when** `just lint` runs, **then** it is
  flagged (forbidigo/custom rule), forcing a typed struct or `json.RawMessage`.
- `scenario: driver-conformance-parity` — **Given** any port with a real and an in-memory
  driver, **when** the shared contract suite runs against both, **then** both pass the
  identical assertions — proving the fake matches the real one.
- `scenario: context-cancellation` — **Given** any blocking port call, **when** the passed
  `context.Context` is cancelled, **then** the call returns promptly with a `ctx.Err()`-derived
  error and leaks no goroutine.
- `scenario: no-mock-framework` — **Given** a test that imports a mocking framework
  (gomock/testify-mock), **when** `just lint` runs, **then** depguard rejects the import.

## Scope

**In**: constructor pattern (functional options vs config struct, and where each applies);
interface/struct placement and the ports-and-drivers convention; the typed-error model and
its edge mapping; typed-primitive policy (enums, IDs, no `any`-leakage); context-first and
no-globals rules; logging API conventions (how components receive a logger); the static
analysis baseline and depguard import graph; package-naming idioms; the testing-style rules
that are *conventions* (no-mocks, contract-suite shape).

**Out**: the OpenAPI/proto codegen toolchain and generated-code rules (ADR-0002+ → the API
ADR, F02); the concrete resource-type definitions (F03); the full testing *strategy* —
harness, CI lanes, coverage gates (F20); per-port interface *content* (each port's ADR).
This ADR fixes the *form*; those fix the *substance*.

## Constraints & Decision drivers

- **C1 — Blueprint rules inherited**: library-first (`pkg/funcd` facade), embed-first,
  single binary, `context`-first, `log/slog` only, OTel for traces/metrics, RFC 9457 on
  the wire, depguard import discipline, single Go module, generated code committed.
- **C2 — LLM-scaffoldable**: the rules must be concrete enough that an LLM produces
  conforming code from the ADR alone, and a linter can enforce them — taste that can't be
  encoded as a rule or a review-checklist item doesn't belong here.
- **C3 — Idiom over cleverness**: prefer standard-library and accepted community idioms;
  every deviation (e.g. centrally-defined port interfaces) must be justified.
- **D1 — Enforced beats documented**: a convention a linter checks is worth ten in a style
  guide. Maximize the share of rules that fail CI rather than fail review.
- **D2 — Low ceremony where safety isn't at stake**: typing and options where mix-ups bite,
  plain structs and strings where they don't.

## Alternatives considered

**Constructor pattern** (decision driver: ergonomics of the public surface vs. internal noise):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Functional options on the public facade only; explicit deps struct internally** | ergonomic, back-compatible public API; internal code stays flat and obvious | two patterns to know | **chosen** |
| Functional options everywhere | uniform | an `Option` type + `With*` funcs per internal package, for code that never needs extensibility | rejected (ceremony, D2) |
| Config struct everywhere incl. facade | flattest | public `New(Config{})` breaks callers as it grows; loses optional wiring | rejected |

**Error model** (driver: uniform edge mapping without per-handler switches):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **`fault.Error` with a `Kind` enum + wrapped sentinels** (in `api/fault`) | one Kind→HTTP/problem table; structured detail fields; `errors.As` recovers Kind through wrapping | a small error package everyone imports | **chosen** |
| Pure exported sentinels + `errors.Is` | very idiomatic, minimal | edge needs a growing sentinel switch; no structured detail | rejected (scales poorly) |
| Rich error struct per package | maximally expressive | most boilerplate; hard to map uniformly | rejected |

**Mocking** (driver: trustworthy fakes for the e2e-on-library strategy):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **No mock frameworks; in-memory drivers + one contract suite per port** | the fake is provably equivalent to the real driver; tests read like real usage | must write/maintain in-memory drivers (needed anyway) | **chosen** |
| Allow mocks for external leaves | flexible at true edges | reopens mock-drift; inconsistent style | rejected — an in-memory driver covers even external SDKs behind the port |
| Generated mocks (mockgen) | familiar | tests interactions not behavior; extra codegen | rejected (conflicts with contract suites) |

**Typed primitives** (driver: catch mix-ups without conversion soup):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Typed enums + typed IDs/names at boundaries; no `any`-leakage** | stops the bugs that actually happen (namespace vs name, wrong enum) | some conversion at glue points | **chosen** |
| Maximal newtypes everywhere | strongest | conversion ceremony swamps glue code | rejected (D2) |
| Typed enums only, plain string IDs | least ceremony | the exact mix-ups typed IDs prevent stay possible | rejected |

**Interface placement** — Go idiom is "define interfaces where they're consumed." funcd
deliberately deviates for **ports**: a port interface lives in its own package
(`internal/store`, `internal/blob`) with drivers in subpackages, because multiple
consumers and multiple drivers share it and the contract suite targets it. This is the one
sanctioned exception; *non-port* collaborator interfaces still follow the consumer-side
idiom. Recorded so reviewers don't "fix" it.

**Error-kernel placement** — the error taxonomy (`Kind` ↔ problem+json `type` URIs) is
*public contract*: the SDK reads it to interpret responses, and `api/types` validators must
return it. It therefore lives in `api/` (stdlib-only, the public-contract layer), **not** in
`internal/platform` — otherwise `api/**` would have to import `internal/**`, which the
depguard rule in §7 forbids. Everything (`internal/**`, `pkg/**`) imports *up* into it;
nothing imports down out of `api/`. Package name is `fault`, not `errors`, to avoid shadowing
stdlib `errors` (which §3/§6 rely on for `errors.As`/`Is`) — consistent with §8 ("the name is
part of the API").

## Decision

### 1. Construction — options on the edge, structs inside
- **Public facade** (`pkg/funcd`, and any driver constructor meant to be selected by an
  embedder): functional options. `func New(opts ...Option) (*Platform, error)`;
  `type Option func(*config) error`; `WithStore`, `WithBus`, `WithGateway`, `WithRuntime`,
  `WithBlob`, `WithLogger`, … . `presets.go` provides `InMemory()` and `Production()` as
  bundles of options. `New` validates required deps and returns a typed `Invalid` error if
  one is missing — never a partial platform, never a panic.
- **Internal components**: `func New(deps Deps) (*T, error)` (or `New(cfg Config)`), where
  `Deps`/`Config` is an explicit struct with documented fields. No `With*` for internal
  constructors.
- Constructors return `(*T, error)` when they can fail; `*T` only when construction is
  infallible. Never return an interface from a constructor (return the concrete struct;
  callers depend on the interface) — except port driver constructors, which return the
  port interface so they're swappable in the facade.

### 2. Ports & drivers
- A port is an interface in its own package; drivers are subpackages. A driver folder is
  warranted only for a **distinct engine** — not for each backend of a library that already
  spans backends. So `internal/store/{memory,sqlite,slatedb}` (the metastore/database layer:
  three genuinely different engines) but `internal/blob/gocloud` is a **single** adapter that
  already provides memory + file + S3 (go-cloud `memblob`/`fileblob`/`s3blob`, by URL) — no
  `blob/memory` or `blob/file` folder, that would re-wrap what the library hands you. The
  port (`blob.Bucket`, matching blueprint/F21; `store`/`blob` deliberately distinct, not
  `store`/`storage`) owns the interface, shared types, and the **contract suite**
  (`<port>contract` helper) — and the contract suite is what proves the one gocloud adapter's
  memory backend behaves like its S3 backend, satisfying the "in-memory driver per port" rule
  without a separate folder.
- "Accept interfaces, return structs" holds everywhere else: non-port collaborators are
  small interfaces declared by the consumer.

### 3. Errors — `api/fault` kernel + edge mapping
- `api/fault` (package `fault`, **stdlib-only**, public-contract leaf) defines `type Kind`
  (`Invalid`, `NotFound`, `Conflict`, `Unauthorized`, `Forbidden`, `Unavailable`,
  `Internal`, …) and a `fault.Error{Kind, Op, Msg, Detail, Err}` implementing `error` +
  `Unwrap`. Helpers: `fault.NotFoundf(...)`, etc., and `fault.KindOf(err) Kind` (walks the
  chain via `errors.As`). Every layer — `internal/**`, `pkg/**` — imports *up* into
  `api/fault`; it imports nothing itself beyond stdlib.
- `Op` is a free `string` (operation names are an open set, not enumerable — the upspin
  error model) and `Detail` is `map[string]string` (deliberately flat to stay inside the
  `any`-ban; nested/typed detail is a non-goal — promote a recurring detail to a typed
  field instead). These two are the conscious exceptions to §4's typed-everything rule.
- Inner layers **wrap and return** (`fmt.Errorf("render route: %w", err)` or
  `fault.Wrapf(err, Kind, op, ...)`); they do **not** log. The **owner of the operation**
  (HTTP middleware, a control loop) logs once.
- `api/fault/problem.go` maps `KindOf(err)` → HTTP status + a stable RFC 9457
  `application/problem+json` (`type`, `title`, `status`, `detail`, `instance`). The map is
  the single place status codes are decided.

### 4. Typed API surface
- **Enums**: typed constant sets (`type Phase string`; `PhaseReady Phase = "Ready"`) with
  `Validate() error` and `String()`; never bare strings in signatures.
- **IDs/names**: named types in `api/types/v1alpha1` (`NamespaceName`, `FunctionName`,
  `RevisionID`, …) used across port and public signatures. Their `Validate()` methods
  return `fault.Invalid` so validation failures map to HTTP 400 at the edge (not 500).
- **No `any`-leakage**: `interface{}`/`any` and `map[string]any` are forbidden in
  hand-written exported and port signatures; use typed structs, or `json.RawMessage`/`[]byte`
  at genuine pass-through points (e.g. opaque event payloads). Enforced by a forbidigo rule
  that **excludes generated files** (`**/generated/**`, `*.gen.go`) — oapi-codegen emits
  `interface{}`/`map[string]interface{}` for `additionalProperties`/`oneOf`, and the full
  generated-code policy is owned by the API/codegen ADR (F02).

### 5. Context & globals
- Every blocking/IO call takes `ctx context.Context` as its **first** parameter; honor
  cancellation and deadlines; never store a `ctx` in a struct.
- **No package-level mutable state** (no singletons, no `init()` side effects, no global
  loggers/registries). Everything is constructed and injected from the composition root
  (`internal/app`). Enforced by `gochecknoglobals`/`gochecknoinits` (allowlist: errors,
  enum constants, regex `MustCompile`).

### 6. Logging
- `log/slog` only (no other logging API anywhere — depguard). `internal/observability`
  builds the root logger from config; the app hands each component a named child
  (`root.With("component", "controller")`) via its `Deps` struct. No package-level logger.
  Use the `*Context` variants so trace/span IDs ride along. (Full field list & levels:
  blueprint "Platform logging".)

### 7. Static analysis & import graph (extends ADR-0001 `.golangci.yml`)
- Linters: `govet`, `staticcheck`, `errcheck`, `ineffassign`, `misspell`,
  `gochecknoglobals`, `gochecknoinits`, `forbidigo` (bans `any` in APIs, `panic` in
  non-main, `fmt.Print*`), `depguard`, `errorlint` (enforces `%w`/`errors.As`).
  (`revive` is intentionally omitted — it overlaps `staticcheck`/`govet` and its opinionated
  rules generate churn on scaffolded code for little signal.)
- **Generated files exempt**: `**/generated/**` and `*.gen.go` are excluded from
  `forbidigo` (and `revive`-style style rules) — committed codegen output is not held to the
  hand-written `any`-ban (see §4); F02 owns generated-code policy.
- **depguard rules**: `api/**` imports nothing from `internal/**` or `pkg/**` (it is the
  bottom contract layer; `api/fault` and `api/types` are stdlib-only and may be imported by
  everyone); `internal/features/*` never import sibling features (communicate via the bus);
  `internal/platform/**` imports no other `internal/**` (leaf kernel; it may import
  `api/**`); `tests/e2e/**` imports only `pkg/**` + `api/**`; no mock framework anywhere; no
  logging library but `log/slog`.

### 8. Package & naming idioms
- Short, lowercase, no-underscore package names; the name is part of the API
  (`store.Store`, not `store.StoreInterface`). No `util`/`common`/`helpers` grab-bags —
  the only shared package is `internal/platform` and it holds **no business logic**.
- **Don't pre-split files.** A package — and especially a **driver** — is a *single file*
  (`gocloud.go`, `slatedb.go`, `memory.go`) until it genuinely outgrows one; split by
  responsibility only when the file is actually large, never speculatively. Resist a
  file-per-type / folder-per-backend reflex. A driver lives in its **own subpackage
  directory only for dependency isolation** — so its third-party imports (go-cloud, the
  slatedb client, …) stay out of the port package and don't leak to every consumer — *not*
  as licence to fan it out into many files. One driver → one directory → one `.go` file
  (+ its `_test.go`, + embedded assets like `migrations/*.sql` where a driver needs them).
- `doc.go` only when a package's purpose isn't obvious from its name + the port file.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| `forbidigo` "no `any` in APIs" is a regex heuristic, not type-aware | no off-the-shelf type-aware linter for this; cheap to start | replace with a small `go/analysis` pass if false-positives/negatives bite |
| depguard feature-isolation rule lists features explicitly | no glob for "any sibling" in depguard | revisit if a custom analyzer becomes worthwhile once features multiply |

## Contracts

This ADR's contract is the set of code shapes below — compilable signatures, not prose.

### Constructor shapes
```go
// pkg/funcd — public facade: functional options.
type Option func(*config) error
func WithStore(s store.Store) Option   { return func(c *config) error { c.store = s; return nil } }
func WithLogger(l *slog.Logger) Option { return func(c *config) error { c.logger = l; return nil } }
func New(opts ...Option) (*Platform, error) // validates required deps → fault.Error{Kind:Invalid}
func (p *Platform) Run(ctx context.Context) error
func (p *Platform) Shutdown(ctx context.Context) error

// pkg/funcd/presets.go
func InMemory() Option   // bundle: memory store/blob/bus, embedded gateway, process runtime
func Production() Option

// internal component: explicit deps struct, no options.
type Deps struct {
    Store  store.Store
    Bus    bus.Bus
    Logger *slog.Logger   // already namespaced by the app
    Clock  clock.Clock    // internal/platform/clock
}
func New(d Deps) (*Controller, error)
```

### Error model — `api/fault` (stdlib-only, public-contract leaf)
```go
// api/fault — package fault. Imported "up" by internal/** and pkg/**; imports only stdlib.
type Kind string
const (
    Invalid      Kind = "invalid"
    NotFound     Kind = "not_found"
    Conflict     Kind = "conflict"
    Unauthorized Kind = "unauthorized"
    Forbidden    Kind = "forbidden"
    Unavailable  Kind = "unavailable"
    Internal     Kind = "internal"
)
type Error struct {
    Kind   Kind
    Op     string            // e.g. "store.Get" — free string, open set
    Msg    string            // human, safe to surface
    Detail map[string]string // deliberately flat (stays inside the any-ban)
    Err    error             // wrapped cause
}
func (e *Error) Error() string
func (e *Error) Unwrap() error
func KindOf(err error) Kind                          // errors.As walk; default Internal
func NotFoundf(op, format string, a ...any) *Error   // + Invalidf, Conflictf, …
func Wrapf(err error, k Kind, op, format string, a ...any) *Error

// api/fault/problem.go — the single status-mapping site (same package, public)
func ToProblem(err error) Problem                    // Problem = RFC 9457 fields
func WriteProblem(w http.ResponseWriter, err error)
```

### Typed primitives
```go
// api/types/v1alpha1 — imports api/fault for the Invalid kind
type NamespaceName string
type FunctionName  string
type RevisionID    string
func (n NamespaceName) Validate() error           // DNS-label → fault.Invalidf on failure

type Phase string
const ( PhasePending Phase = "Pending"; PhaseReady Phase = "Ready"; /* … */ )
func (p Phase) Validate() error                   // → fault.Invalidf on unknown value
```

### Contract-suite shape (per port)
```go
// internal/store/storecontract
// RunContract executes the same assertions against any driver; both memory and sqlite call it.
func RunContract(t *testing.T, newStore func(t *testing.T) store.Store)
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | ADR-0001 `.golangci.yml`, module, `just lint` | this ADR extends the linter set + adds depguard rules |
| Consumes | `log/slog`, `context`, `errors` (stdlib only for these) | no third-party logging/errors libs |
| Exposes | `api/fault` (error kernel + problem mapping), `internal/platform/clock`, typed IDs/enums in `api/types/v1alpha1` | `api/fault` + `api/types` are stdlib-only and imported platform-wide |
| Exposes | depguard import-graph rules | the enforceable form of blueprint "Import discipline" |
| Exposes | constructor/port/typing conventions | every later feature ADR inherits these; they are not re-decided |

## Scaffold plan

No business logic. Produces the shared kernel, the lint config, and skeletons.

1. Create `api/fault/fault.go` (Kind, Error, Unwrap, KindOf, Wrapf, `NotFoundf`/`Invalidf`/… —
   full bodies, stdlib-only; this *is* the kernel) + `fault_test.go` (table tests: KindOf
   through wrapping, default Internal).
2. Create `internal/platform/clock/clock.go` (`Clock` interface + `System()` + `Fake`) —
   the canonical injected-dependency example.
3. Create `api/fault/problem.go` (Problem struct, `ToProblem`, `WriteProblem`) +
   `problem_test.go` (each Kind → expected status + type).
4. Create `api/types/v1alpha1/ids.go` + `enums.go` with the named types above and
   `Validate()` stubs (returning `fault.Invalidf`) + tests.
5. Create `pkg/funcd/{funcd.go,options.go,presets.go}` skeletons: `New`/`Option`/`With*`
   signatures returning `fault.Error{Kind:Invalid}` for missing deps (compiles; `Run` body
   is `return errors.New("not implemented")`).
6. Extend `.golangci.yml` with the linters, the generated-file exemption, and depguard
   rules in Decision §7.
7. Add a `docs/conventions.md` one-pager that *links* this ADR (discoverable from the repo
   root) — not a second source of truth.
8. **Test skeletons** (compiling, `t.Skip("ADR-0002 scaffold")` until implemented):
   - `pkg/funcd/funcd_test.go` → `scenario: facade-missing-dep`
   - `api/fault/problem_test.go` → `scenario: error-maps-to-problem`
   - a lint-rule fixture under `tests/` → `scenario: lint-blocks-cross-feature-import`,
     `scenario: lint-blocks-any-leak`, `scenario: no-mock-framework` (a `just lint` of a
     known-bad fixture must fail)
   - `internal/store/storecontract/contract_test.go` → `scenario: driver-conformance-parity`
     (skeleton; real assertions land with the store ADR)
   - `scenario: context-cancellation` — only a compiling placeholder here; a clock isn't a
     real blocking-I/O call, so the genuine cancellation assertion lands with the first real
     port (store ADR). The placeholder documents the rule; it doesn't fake it.
9. **Definition of done** (= Scenarios executed): `just build` and `just lint` pass on the
   kernel; the known-bad lint fixtures fail `just lint` as asserted; `api/fault` and its
   problem mapping have passing unit tests; every scenario has a named, skipped skeleton;
   no business logic outside the kernel packages.

## Review checklist

- [ ] `pkg/funcd` uses functional options; no internal package defines `Option` types.
- [ ] No constructor returns an interface except port-driver constructors.
- [ ] `api/fault` exists (package `fault`, stdlib-only) with `Kind` + `Error`+`Unwrap`+`KindOf`;
      no other package defines its own error-kind enum; edge mapping lives only in `api/fault`.
- [ ] `api/**` imports no `internal/**` or `pkg/**`; `api/fault`/`api/types` are stdlib-only
      and the rest of the tree imports *up* into them (lint passes).
- [ ] No `interface{}`/`any`/`map[string]any` in hand-written exported or port signatures;
      generated files (`**/generated/**`, `*.gen.go`) are exempt and the exemption is in `.golangci.yml`.
- [ ] Every blocking signature in the scaffold takes `ctx` first; no `ctx` stored in a struct.
- [ ] No package-level mutable globals/`init()` outside the allowlist.
- [ ] `log/slog` is the only logging import; no package-level logger.
- [ ] No package named `errors` (no stdlib shadowing); the error kernel is `fault`.
- [ ] `.golangci.yml` contains the full linter set (no `revive`), the generated-file
      exemption, and all depguard rules; the known-bad fixtures fail and the kernel passes.
- [ ] Every Scenario has a named skeleton; none are silently dropped.
- [ ] `internal/platform/**` imports no other `internal/**` (leaf kernel; may import `api/**`).

## Consequences

- (+) Every later feature ADR inherits a fixed code shape — they decide substance, not form.
- (+) Most conventions fail CI, not review (D1): import graph, globals, `any`-leakage,
  logging API, error wrapping.
- (+) The typed-error model gives uniform, correct HTTP mapping for free at the edge.
- (−) Two construction patterns (options vs deps-struct) to learn — mitigated by a crisp
  rule (edge vs internal) and examples in the scaffold.
- (−) In-memory drivers must be written for every port — but they're required for the
  e2e-on-library strategy regardless, so this is cost already owed.
- (risk) `forbidigo` `any`-detection is heuristic; false positives possible — exit path in
  *Temporary workarounds*.

## Open questions

| Question | Where it gets answered |
|---|---|
| Generated-code conventions (oapi-codegen output, `//go:generate`, drift CI) | API/codegen ADR (F02) |
| Coverage thresholds, CI lane wiring, e2e harness shape | testing-strategy ADR (F20) |
| Exact resource enum/ID sets and their `Validate()` bodies | resource-model ADR (F03) |
| Whether a type-aware `any`-leak analyzer replaces the forbidigo heuristic | revisit if false hits appear (workaround exit) |
| Per-port interface content (method sets) | each port's own ADR |

## References

- Rob Pike, "Self-referential functions and the design of options" (functional options).
- Go stdlib `errors` (`Is`/`As`/`%w`); Dave Cheney on error wrapping.
- RFC 9457 (problem+json); golangci-lint v2 linters (`depguard`, `forbidigo`, `errorlint`,
  `gochecknoglobals`).
- [ADR-0001](0001-project-setup-and-structure.md); [blueprint.md](../../blueprint.md) —
  "Go best practices", "Platform as a library", "Platform logging".
