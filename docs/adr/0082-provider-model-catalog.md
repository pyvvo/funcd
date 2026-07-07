# ADR-0082: The provider model — a built-in/add-on provider catalog

- **Status**: Implemented
- **Date**: 2026-06-29 (Accepted 2026-06-29 after two judge passes — folded M1 `bus` mis-tiering → `eventing`/`invoke`
  per the blueprint, m1 `New` runs `Validate`, and the F56 feat-row propagation. **Reviewing → Implemented 2026-06-29**
  — review **pass** (DoD 10/10), see docs/reviews/adr-0082-implementation-claude-opus-4-8.md; `internal/provider`
  (catalog) + `pkg/funcd` wiring + startup log, 6 scenario tests, leaf verified, behavior-preserving — full suite green)
- **Deciders**: green-0-rabbit
- **Tags**: provider, capability-model, architecture, registry, catalog, refactor, wasmcloud
- **Realizes**: [FEAT-0001/F56](../feat/0001-feat-v1.1.md)
- **Relates to**: the blueprint *provider model* (built-in vs. add-on) this lands in code ·
  [ADR-0019](0019-service-facade-pattern-kv.md) (the CRD+facade+controller+port four-part shape a provider names) ·
  [ADR-0007](0007-blob-storage-layer-port.md)/[ADR-0069](0069-kv-data-plane.md) (blob/KV ports + facades) ·
  [ADR-0013](0013-gateway-ingress-httputil-primary.md) (ingress gateway) ·
  [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (S3 — a built-in provider) ·
  [ADR-0081](0081-function-log-capture-side-channel-blob.md) (log-ingest — a built-in provider) ·
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (no global mutable state, typed enums, `New(…)`)

## Context & Need

**Purpose**: make **"provider"** a first-class, legible concept in the codebase. The blueprint now names a
*provider* — a shared platform capability endpoint (funcd's wasmCloud-style capability provider: a **binding** is
the link, a **port + ≥2 drivers** the contract) — in two tiers: **built-in** (in-daemon, pure-Go, trusted core)
vs **add-on** (out-of-daemon deployed service function). The code already *has* this structure (every data-plane
service is the ADR-0019 four-part shape; the gateways are in-daemon endpoints) — it just isn't *named* one and
nothing enumerates or classifies it. **Callers**: the daemon (startup logging of its providers) and future
introspection (a `funcdctl providers` / endpoint, out of scope here). **Why now**: two new providers just landed
(S3/ADR-0080, log-ingest/ADR-0081); naming the concept keeps the growing set coherent and the built-in/add-on
trust boundary explicit.

## Scenarios

- **scenario: catalog-enumerates-builtins** — *Given* the daemon's provider catalog, *When* `ByKind(Builtin)` is
  queried, *Then* it returns the built-in providers (kv, blob, secrets, eventing, invoke, ingress, egress, s3,
  log-ingest), each tagged `Kind=built-in`. (`bus` is **not** a provider — it is the internal messaging substrate,
  never function-bindable.)
- **scenario: catalog-classifies-addons** — *Given* the catalog, *When* `ByKind(Addon)` is queried, *Then* the
  known add-on providers (catalog/query, observability-serving) are returned, distinct from the built-ins.
- **scenario: descriptor-names-the-shape** — *Given* a built-in descriptor (kv), *Then* its `Port` names the
  contract (`kvstore.KV`) and its `Bindings` name the link (`spec.kv`) — the descriptor is the umbrella over the
  existing four-part shape, not a replacement.
- **scenario: behavior-preserving** — *Given* the provider catalog is added, *When* the existing suite runs, *Then*
  every test passes unchanged — no facade/port/driver behavior is touched.
- **scenario: startup-lists-providers** — *Given* the daemon boots, *Then* it logs its provider catalog
  (name + kind) once — the legibility payoff.
- **scenario: duplicate-name-rejected** — *Given* two descriptors with the same `Name`, *When* the catalog is
  built, *Then* `New` returns `fault.Invalid` (a wiring bug fails fast, not silently shadows).

## Scope

**In**: a new leaf package **`internal/provider`** (the `Kind`, `Descriptor`, `Catalog` types + `New`/`All`/
`ByKind`/`Get`); a **static catalog assembled at the composition root** classifying every known provider
(built-in + add-on); **startup logging** of the catalog. Behavior-preserving — pure addition.

**Out**: a `Provider` lifecycle/health *interface* every provider implements (a heavier abstraction, deferred);
**relocating or renaming** the existing `internal/{kvstore,blob,secrets,eventing,gateway,…}` packages (kept in place);
a public **introspection CLI/endpoint** (`funcdctl providers` — a follow-on); any change to facade/port/driver
**behavior**; the add-on providers' **runtime** (that is F48/F54).

## Constraints & Decision drivers

- **Behavior-preserving** — this is a classification/legibility layer; no runtime behavior of any provider changes.
- **Minimal churn** — the existing provider packages are implemented and tested; don't relocate/rename them.
- **No global mutable state**, typed enums, `New(…)` construction ([ADR-0002](0002-source-code-conventions-and-patterns.md)).
- **Reconcile with, not replace,** the ADR-0019 facade pattern — a provider *names* the four-part shape; the
  facade stays its consumer-facing PEP surface.
- **Zero new deps**, pure-Go.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Full `Provider` interface** every provider implements (lifecycle/health/contract methods) + registry | Most uniform/introspectable — but touches **every** implemented (frozen) provider package, and lifecycle/health is a *separate* concern not needed for legibility. Deferred: the catalog can grow an interface later if runtime health/enumeration is wanted. |
| **Conventions only** (naming + docs, no code) | Lowest churn — but "provider" stays implicit; nothing enumerates or classifies them at runtime, so the legibility payoff (startup log, future `funcdctl providers`) is unreachable. |
| **Relocate packages under `internal/provider/{kv,blob,…}`** | Most legible at the file-tree level — but a mass rename of implemented packages = import churn across the whole codebase, for directory cosmetics. Rejected vs an in-place catalog. |
| **Co-located `var Provider` descriptor in each package** | Authoritative (can't drift from the package) — but adds an import edge + edit to every provider package. Rejected vs central-at-composition for thinness; revisitable if drift appears (see Temporary workarounds). |
| **Provider replaces "facade"** (rename the facade layer) | More direct naming — but renames a well-understood, *implemented* concept (ADR-0019) for no behavior gain. Provider is the **umbrella**, the facade is its surface. |

## Decision

Add a **leaf package `internal/provider`** holding a light **catalog**: a typed `Kind` (built-in | add-on), a
`Descriptor` that **names** one provider (its `Name`, `Kind`, the `Port` that is its contract, the `Bindings` a
function declares to consume it, a one-line `Summary`), and an immutable `Catalog` (`All`/`ByKind`/`Get`). The
descriptor is the **umbrella over the ADR-0019 four-part shape** — it classifies and ties together the existing
CRD + facade + controller + port + drivers; it replaces none of them.

The **catalog is assembled once at the composition root** (`pkg/funcd`, which already wires every provider) from a
static descriptor set covering all known providers — the implemented built-ins (kv, blob, secrets, eventing,
invoke, ingress, egress), the accepted-but-pending built-ins (s3/ADR-0080, log-ingest/ADR-0081), and the known
add-ons (catalog/query F48, observability-serving F54). `bus` is **excluded** — it is the internal-plane messaging
substrate (never handed to functions, no `spec.*` binding), so it is not a provider. The daemon **logs the catalog
at startup** (name + kind), the
first consumer of the legibility payoff. The existing packages stay exactly where they are — this is a pure,
behavior-preserving addition.

`internal/provider` imports nothing from `internal/` (a true leaf — descriptors are declared at the composition
root, which already imports the providers), so no new import edges land on the frozen packages.

## Temporary workarounds

- **Descriptors live centrally (composition root), not co-located** — so adding a provider means adding its
  `Descriptor` there; the compiler doesn't force it. *Exit*: if drift appears, move to a co-located
  `var Provider = provider.Descriptor{…}` per package plus a test asserting every wired provider has a descriptor.

## Contracts

```go
// internal/provider — the platform provider catalog (the blueprint provider model in code). A leaf:
// imports nothing from internal/. Classifies the existing ADR-0019 four-part shapes + the gateways; it does
// NOT replace any facade/port/driver, and changes no runtime behavior.

// Kind is a provider's deployment tier (blueprint provider model).
type Kind string

const (
	Builtin Kind = "built-in" // in-daemon, pure-Go, always-on, trusted core
	Addon   Kind = "add-on"   // out-of-daemon, a deployed service function (cgo/heavy engine)
)

// Valid reports whether k is a known tier.
func (k Kind) Valid() bool { return k == Builtin || k == Addon }

// Descriptor names ONE platform provider — the umbrella over its ADR-0019 four-part shape
// (CRD + facade + controller + port + drivers). Classification metadata, not a runtime handle.
type Descriptor struct {
	Name     string   // stable id: "kv","blob","secrets","eventing","invoke","ingress","egress","s3","log-ingest",…
	Kind     Kind     // built-in | add-on
	Port     string   // the contract: the port/interface name (e.g. "kvstore.KV","blob.Bucket"); "" for an infra provider with no function-facing port
	Bindings []string // how a function consumes it: spec.* link(s) — e.g. kv→["spec.kv"], invoke→["spec.links"], blob→["spec.blob"]; eventing→EventSource triggers; nil for infra (ingress/egress)
	Summary  string   // one-line human description
}

// Validate checks a descriptor is well-formed (non-empty Name, valid Kind).
func (d Descriptor) Validate() error

// Catalog is the immutable registry of platform providers (ADR-0002 §5: no package-level mutable state).
type Catalog struct { /* unexported: descriptors sorted by Name + a name index */ }

// New builds a Catalog: it runs Validate on each descriptor, then rejects duplicate Names — fault.Invalid on
// either. (fault lives at api/fault, outside internal/, so this does not break the leaf invariant.)
func New(ds ...Descriptor) (*Catalog, error)

// All returns every descriptor in stable Name order.
func (c *Catalog) All() []Descriptor

// ByKind returns the descriptors of one tier, stable Name order.
func (c *Catalog) ByKind(k Kind) []Descriptor

// Get returns the descriptor for name, or ok=false if absent.
func (c *Catalog) Get(name string) (d Descriptor, ok bool)
```

```go
// pkg/funcd (composition root) — the platform's static provider catalog, the single source of truth.
// Assembled where every provider is already wired; logged at startup.
func providerCatalog() (*provider.Catalog, error) // New(kvDescriptor, blobDescriptor, …, s3Descriptor, logIngestDescriptor, catalogQueryDescriptor, obsServingDescriptor)
```

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | nothing (`internal/provider` is a leaf types package); the composition root (`pkg/funcd`) supplies the descriptor set |
| Exposes | a `*provider.Catalog` held by the daemon — queried for **startup logging** today, a future introspection CLI/endpoint later |
| Config keys | none |
| New deps | none (pure-Go, stdlib `sort`/`slog`) |

## Implementation plan

- **Files**: `internal/provider/provider.go` (the `Kind`/`Descriptor`/`Catalog` types + `Validate`/`New`/`All`/
  `ByKind`/`Get`); `internal/provider/provider_test.go` (the scenarios — enumerate, classify, get-by-name,
  duplicate-name-rejected, descriptor-validate). In `pkg/funcd`: a `providers.go` declaring the static descriptor
  set (the known built-ins + add-ons) + `providerCatalog()`, and a **startup log** line emitting the catalog
  (name + kind) through the root logger.
- **go.mod**: none.
- **Test plan** (one acceptance test per Scenario, names echoing `scenario: <name>`):
  - `internal/provider` unit tests: `catalog-enumerates-builtins`, `catalog-classifies-addons`,
    `descriptor-names-the-shape` (a kv descriptor's `Port`/`Bindings`), `duplicate-name-rejected` (`New` →
    `fault.Invalid`), plus `Descriptor.Validate` edge cases.
  - a `pkg/funcd` test: `startup-lists-providers` (boot the daemon with an in-memory log handler; assert one log
    line lists the provider catalog) and `behavior-preserving` is satisfied by the **existing suite staying green**
    (no edits to facade/port/driver packages — verified by the tree diff + `go test ./...`).
- **Definition of done**: `go build/test/lint` + `go mod verify` green; **no existing provider package edited**
  (tree diff shows only `internal/provider/*` + `pkg/funcd` additions); `internal/provider` imports nothing from
  `internal/` (leaf); no new deps; identity/path grep clean; `just ci` green after commit.

## Review checklist

- [ ] `internal/provider` is a **leaf** — imports nothing from `internal/`; descriptors declared at the composition root.
- [ ] `Catalog` is immutable, built by `New`; **no package-level mutable state**; `Kind` is a typed enum.
- [ ] `New` rejects a **duplicate `Name`** and an invalid descriptor with `fault.Invalid`.
- [ ] `ByKind` correctly partitions **built-in vs add-on**; `All`/`ByKind` are stable-ordered.
- [ ] The catalog covers every known provider (kv, blob, secrets, eventing, invoke, ingress, egress, s3, log-ingest, catalog/query, observability-serving) with the right `Kind`; `bus` is **excluded** (internal substrate, not function-bindable).
- [ ] Each descriptor `Port`/`Bindings` correctly **names** the existing four-part shape (no facade/port renamed or moved).
- [ ] The daemon **logs the catalog at startup** (name + kind).
- [ ] **Behavior-preserving**: no existing provider package edited; the full existing suite passes unchanged.
- [ ] Pure-Go, **zero new deps**.
- [ ] One passing acceptance test per Scenario.

## Consequences

- **(+)** "Provider" is now legible in code — a single catalog enumerates and classifies every platform capability
  endpoint by tier; the built-in/add-on **trust boundary** is explicit and queryable.
- **(+)** **Behavior-preserving, minimal churn** — a new leaf package + composition-root wiring; not a single
  implemented provider package is touched, so nothing regresses.
- **(+)** **Extensible** — a new provider is one `Descriptor` in the catalog; future runtime introspection
  (`funcdctl providers`, a `/providers` endpoint) is a thin read over the catalog.
- **(+)** Reconciles cleanly with ADR-0019 — provider is the umbrella, the facade stays the surface; no concept war.
- **(−)** The catalog is **declarative** (classification metadata), not a runtime health/liveness view — a provider
  in the catalog means "recognized," not "running." (A `Provider` interface with health is the deferred heavier option.)
- **(−)** Descriptors live **centrally**, so they can drift from a provider package until the co-location exit
  (Temporary workarounds) is taken.

## Open questions

- **A public introspection surface** (`funcdctl providers` / a `/providers` endpoint) — wanted, but its own small
  ADR once a consumer needs it; this ADR ships the catalog + startup log only.
- **Promoting the catalog to a `Provider` interface** (lifecycle/health/enumerable contracts) — deferred until a
  runtime concern (health checks, dynamic add-on registration) needs it; resolved in a future ADR, not here.
- **Co-located vs central descriptors** — central now (Temporary workarounds names the co-location exit if drift appears).

## References

- The blueprint *provider model* bullet (built-in vs. add-on; the wasmCloud capability-provider analog).
- [ADR-0019](0019-service-facade-pattern-kv.md) (the four-part service shape), [ADR-0002](0002-source-code-conventions-and-patterns.md) (conventions).
- wasmCloud capability providers + link definitions (the prior-art the model rhymes with: binding = link, port+drivers = contract).
