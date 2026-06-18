# ADR-0003: Resource model & API typing (v1alpha1)

- **Status**: Implemented
- **Date**: 2026-06-14 (revised same day after judge review: added the optional `StatusObject`
  interface for generic status write-back; single-sourced scope on `Kind.Namespaced()` and added
  the shared `validateMeta` TypeMeta-consistency check; registry-completeness test; doc nits;
  accepted 2026-06-14; implemented 2026-06-14 after review — 9/9 scenarios pass, lint 0 issues,
  build/vet/test green)
- **Superseded in part by**: [ADR-0045](0045-rename-sandbox-to-worker.md) (2026-06-16) — **naming only**: the `Worker`
  resource kind (a compute node) → `WorkerNode` (+ `KindWorker`→`KindWorkerNode`). The resource model is unchanged; read
  this ADR for the model, ADR-0045 for the name; "Worker" in this frozen text ≡ "WorkerNode".
- **Deciders**: green-0-rabbit
- **Tags**: resource-model, api-types, crd, typing, v1alpha1
- **Realizes**: [FEAT-0000/F03](../feat/0000-feat-v1.md), [FEAT-0000/F22](../feat/0000-feat-v1.md)
- **Relates to**: [ADR-0002](0002-source-code-conventions-and-patterns.md) (typed primitives,
  `api/fault`, no-`any` rule, package idioms — this ADR is the first big consumer of them and
  resolves ADR-0002's open question *"exact resource enum/ID sets → resource-model ADR (F03)"*),
  [ADR-0001](0001-project-setup-and-structure.md) (module, lint, `just`),
  [blueprint.md — Resources definition / Resource groups & tags / Resource model](../../blueprint.md)

## Context & Need

The blueprint models the platform's control surface as a set of **CRD-like resources** —
`spec` (desired, user-owned) and `status` (observed, controller-owned) — addressed by
`apiVersion: funcd.io/v1alpha1`, grouped by namespace and by a required `resourceGroup`,
and reconciled by one generic controller engine. Every other component sits on top of this
model: the API server validates and serves these objects, codegen (F02) emits the wire
client/server from them, the store (F05/F21) persists them, the controller (F08) watches and
reconciles them, and the CLI/SDK render them. Until the resource model exists as concrete,
typed Go, none of those can be built — and each would otherwise re-invent metadata, naming,
status, and the spec/status split incompatibly.

**Purpose**: this ADR defines the *typed resource model and its API framework* — the data
types and the small amount of machinery that turn a kind into a uniformly-handled object.
Its "caller" is (a) an LLM/developer about to declare or extend a resource kind, (b) the
store and controller, which must handle *any* kind generically (persist, watch, diff, set
status) without a type switch per kind, and (c) the codegen/admission layers, which need a
single canonical source for what a `funcd.io/v1alpha1` object *is*. Conformance is observable
mechanically: the types compile, round-trip through JSON unchanged, validate to typed
`fault` errors, and every kind satisfies the generic `Object` interface and is reachable from
the kind registry.

This ADR fixes the **framework and the envelope**, not the per-kind field substance. The
rich behavioral fields of a `Function` (runtime, handler, artifact, scaling, triggers) or a
`Route` (rules, traffic split) are owned by the feature ADRs that decide those topics
(F10–F16); they are *appended* to the skeletons defined here. This keeps one ADR at one
altitude (ADR-0000 strict rule 3) and lets the critical-path tier (codegen, store,
controller) start against a stable type model now.

## Scenarios

- `scenario: objectmeta-requires-resource-group` — **Given** any namespaced resource whose
  `metadata.resourceGroup` is empty, **when** `Validate()` is called, **then** it returns a
  typed `fault.Invalid` naming `resourceGroup` as the missing field (the check admission in
  F07 will call) — the object is never treated as valid.
- `scenario: tags-optional` — **Given** a resource with `metadata.resourceGroup` set and no
  `metadata.tags`, **when** `Validate()` is called, **then** it passes — tags are optional and
  carry no validation or authorization meaning.
- `scenario: name-rejects-non-dns-label` — **Given** a resource whose `metadata.name` (or
  `namespace`) is not a valid DNS-1123 label, **when** `Validate()` is called, **then** it
  returns `fault.Invalid` (so the edge maps it to HTTP 400, never 500).
- `scenario: scope-enforced` — **Given** a cluster-scoped kind (e.g. `Namespace`,
  `RuntimeClass`) carrying a non-empty `metadata.namespace`, **or** a namespaced kind with an
  empty `metadata.namespace`, **when** `Validate()` is called, **then** it returns
  `fault.Invalid` — the kind's scope is modeled and enforced.
- `scenario: json-roundtrip-stable` — **Given** any of the registered kinds populated and
  marshalled to JSON, **when** it is unmarshalled back, **then** the result equals the
  original and the wire form carries `apiVersion: funcd.io/v1alpha1` and the correct `kind` —
  the property codegen, store, and CLI all depend on.
- `scenario: generic-object-access` — **Given** any kind handed to code as the generic
  `Object` interface, **when** the caller asks for `GroupVersionKind()`, `GetName()`,
  `GetNamespace()`, `GetResourceGroup()`, and `GetGeneration()`, **then** it returns the
  object's identity with no per-kind type switch — proving the store/controller can be generic.
- `scenario: generic-status-writeback` — **Given** a status-bearing kind handed to code as the
  `StatusObject` interface, **when** a generic caller calls `GetStatus()` and sets the `Phase`
  or a `Condition`, **then** the change is visible on the concrete object — proving the
  controller can write status back without a per-kind switch; and the four pure-data/policy
  kinds (`Config`, `Secret`, `Grant`, `EgressPolicy`) do **not** satisfy `StatusObject`.
- `scenario: kind-registry-roundtrips-every-kind` — **Given** the kind registry, **when**
  code lists `AllKinds()` and constructs each via `NewObject(kind)`, **then** it gets a
  zero-valued typed `Object` of that kind for **all 15** kinds, and the produced object's
  `GroupVersionKind().Kind` equals the requested kind — proving the registry is complete and
  exact (what lets the store/codegen enumerate the model).
- `scenario: conditions-upsert-by-type` — **Given** a resource `status` with a set of
  `Conditions`, **when** a condition of an existing `Type` is set with a new `Status`, **then**
  it is replaced in place (not duplicated) and its `LastTransitionTime` advances only when the
  `Status` actually changed — the status-bookkeeping contract every controller relies on.

## Scope

**In**:
- The shared **envelope**: `TypeMeta` (apiVersion + kind), `ObjectMeta` (name, namespace,
  **required** `resourceGroup`, optional `tags`, plus carrier bookkeeping fields), and the
  shared `Status` base (`Phase`, `ObservedGeneration`, `Conditions`).
- The **generic machinery**: the `Object` interface, `GroupVersionKind`, the typed `Kind`
  enum + scope, `ObjectRef`/`OwnerReference`, and a stateless kind **registry**
  (`NewObject`, `AllKinds`).
- The **15 kinds** of the blueprint resource model, each as a typed Go struct embedding the
  envelope, with `spec`/`status` structs populated with **identity-intrinsic or pure-data
  fields only**, and a `GroupVersionKind()`/`Validate()` per kind.
- The **typed primitives** these need (`ObjectName`, `ResourceGroupName`, `UID`, `Tags`,
  `Phase` value set, condition types), extending ADR-0002's `ids.go`/`enums.go`.
- Resolution of ADR-0002's deferred item: the **exact `Phase` value set** (reconciled to the
  blueprint's resource state machine).

**Out**:
- **Per-kind behavioral spec fields** — runtime/handler/artifact/scaling/triggers
  (`Function`), route rules/traffic split (`Route`), service driver config (`Service`), event
  source config (`EventSource`), grant subjects (`Grant`), egress rules (`EgressPolicy`),
  listeners/TLS (`Gateway`), etc. Each is owned and appended by its feature ADR (F10–F16).
  This ADR defines the *struct that will hold them*, not the fields.
- **OpenAPI schema + codegen** (oapi-codegen `types.gen.go`, drift CI, generated-file rules) —
  the API/codegen ADR (F02/P-B). P-B authors the OpenAPI to **match** these canonical Go types
  and adds the drift gate.
- **Persistence, watch, generations** semantics (when `generation`/`resourceVersion` change,
  how watch deltas form) — the store ADR (F05/P-C). P-A defines the carrier fields; P-C owns
  their lifecycle.
- **Admission wiring** (running `Validate`, defaulting, quota, stripping user-set `status`) —
  the API-server ADR (F07/P-L). P-A provides the `Validate()` admission calls.
- **Deep-copy codegen** — deferred (see *Temporary workarounds*); the store may copy via JSON
  round-trip until a generated `DeepCopy` lands with codegen (F02).

## Constraints & Decision drivers

- **C1 — ADR-0002 conventions inherited**: typed enums + typed IDs/names; no `any`/
  `map[string]any` in exported signatures (`map[string]string`/`[]byte`/`json.RawMessage` are
  the sanctioned escape hatches); `Validate()` returns `fault.Invalid`; `api/types/v1alpha1`
  is a stdlib + `api/fault`-only public-contract leaf (imports nothing from `internal/`/`pkg/`).
- **C2 — Blueprint is the model authority**: the [Resource model](../../blueprint.md) table
  (15 kinds, scopes), the spec/status split, the required `resourceGroup` + optional `tags` in
  a *shared* `ObjectMeta`, and `apiVersion: funcd.io/v1alpha1` are inherited, not re-decided.
- **C3 — Hand-written Go is canonical** (decision Q3): these types are the source of truth for
  the resource model; OpenAPI (F02) is authored to match them and drift-checked. Rationale:
  typed IDs, enums, `Validate()` methods, and the `Object` interface carry semantics OpenAPI
  cannot express, and the blueprint layout lists `api/types/v1alpha1/*.go` as hand-written.
- **C4 — Generic-first**: the store and controller (the immediately-next tier on the critical
  path) must treat kinds uniformly; the type model must therefore expose a generic `Object`
  accessor + a complete kind registry, not force a per-kind switch upstream.
- **D1 — Framework over fields**: define the envelope and machinery completely; defer behavioral
  fields to the owning feature ADR. Maximizes reuse, keeps each ADR at one altitude, and lets
  an Accepted ADR stay frozen while features grow the skeletons they own.
- **D2 — Mechanically conformant**: every rule here is observable by a compiling, round-tripping,
  `Validate`-ing test — no taste-only conventions (those live in ADR-0002).

## Alternatives considered

**Source of truth** (driver: avoid drift between Go model and the OpenAPI wire surface):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Hand-written Go canonical; OpenAPI authored to match + drift CI (F02)** | keeps typed IDs/enums/`Validate()`/`Object` semantics OpenAPI can't express; matches blueprint layout; store/controller get rich Go now | a drift gate must be built in F02 | **chosen** (Q3) |
| OpenAPI-first, generate the structs | single source; one schema | loses typed-ID/method ergonomics; pulls F02's codegen topic into P-A; generated `interface{}` for oneOf/additionalProps reintroduces the `any` the model forbids | rejected |
| Two independent models (Go + OpenAPI), reconciled by hand | flexible | guaranteed silent drift; exactly what CI is supposed to prevent | rejected |

**Per-kind depth** (driver: one topic at one altitude vs. immediately-rich types):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Framework + identity skeleton; behavioral fields appended by feature ADRs** | respects ADR-0000 rule 3; small, fast, on critical path; Accepted ADR stays frozen while features grow their own kinds | many later ADRs touch these files (append-only growth) | **chosen** (Q1) |
| Full spec for the core kinds now | codegen/store get rich types immediately | decides substance feature ADRs own (scope creep); fields then frozen in an Accepted ADR and awkward to evolve | rejected |

**Kind coverage** (driver: a complete registry for store/controller vs. strict F03 scope):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **All 15 blueprint kinds as typed shells** | complete GVK registry; store/controller/codegen enumerate the whole model once; fewer later registry edits | defines cluster/infra kinds (Worker, Gateway, RuntimeClass, Invocation) ahead of their features; needs a deliberate F03 scope amendment (F03 named 11) | **chosen** (Q2) — F03 amended in the same session |
| F03 core 11 only | strictly in-scope; leaner | registry grows kind-by-kind; store/controller special-case "unknown kind" until each lands | rejected |

**Generic accessor** (driver: where the type-model machinery belongs):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Define `Object` + GVK + `ObjectRef`/`OwnerReference` + registry in P-A** | it is a type-model concern; P-C/P-J consume it directly; one home | machinery exists slightly before its first consumer | **chosen** (Q4) |
| Defer to the store/controller ADR | smaller P-A | forces P-C to define type-model machinery in the wrong layer; cross-ADR ownership muddle | rejected |

**Metadata shape** — Kubernetes `metav1.ObjectMeta`/`TypeMeta`/`OwnerReference`/`Condition` are
the proven idiom and the mental model the blueprint already uses ("same split as in
Kubernetes"). funcd adopts the *shape* (so the model is instantly legible and the controller
pattern transfers) but **not** the dependency: these are small hand-written types in
`api/types/v1alpha1`, not `k8s.io/apimachinery` (heavy tree, would violate the stdlib-only
leaf rule and embed-first sizing). The one deliberate departure is the **required
`resourceGroup`** (Azure-style second grouping axis) carried in the shared `ObjectMeta` so no
kind can forget it (F22).

## Decision

### 1. One API group, version-stamped wire identity
`Group = "funcd.io"`, `Version = "v1alpha1"`; `apiVersion` on the wire is `"funcd.io/v1alpha1"`.
A typed `Kind` enum names every kind; `GroupVersionKind` is the `(Group, Version, Kind)` triple
used for registry lookup, `ObjectRef`, and codegen. Graduate the version to `v1` (a new package)
when the contract stabilizes — never mutate `v1alpha1` in place.

### 2. The shared envelope, embedded by every kind
- `TypeMeta{ APIVersion, Kind }` — inline JSON; carries wire identity.
- `ObjectMeta` — the shared metadata on **every** kind: `Name` (required), `Namespace`
  (required for namespaced kinds, empty for cluster-scoped), **`ResourceGroup` (required for
  namespaced kinds — F22)**, `Tags` (optional `map[string]string`), plus **carrier bookkeeping
  fields** the platform fills: `UID`, `Generation`, `ResourceVersion`, `CreationTime`,
  `DeletionTime`, `OwnerReferences`, `Finalizers`. P-A defines these fields and their *envelope
  validation*; their **lifecycle semantics** (when `Generation` bumps, how `ResourceVersion` is
  minted, finalizer handling) are owned by the store/controller ADRs (P-C/P-J). They live here
  so those ADRs never have to edit `api/types`.
- `Status` base — `Phase`, `ObservedGeneration`, `Conditions` — embedded by kinds that have
  observed state.

Every kind is `struct{ TypeMeta; ObjectMeta; Spec XSpec; Status XStatus }`. The metadata
accessors are **promoted from `ObjectMeta`**, so a kind hand-writes only `GroupVersionKind()`
(returns its constant GVK) and `Validate()` (delegates to the shared `validateMeta` envelope
check); a **status-bearing** kind adds a one-line `GetStatus()` (see §3).

### 3. The generic `Object` interface (+ optional `StatusObject`)
```
Object       : GroupVersionKind() · GetObjectMeta() · GetName() · GetNamespace() ·
               GetResourceGroup() · GetGeneration() · Validate()
StatusObject : Object + GetStatus() *Status
```
All 15 kinds implement `Object`: the store persists them, the store/admission stamp and read
**metadata generically** via `GetObjectMeta() *ObjectMeta` (UID, generation, resourceVersion),
and admission validates via `Validate()` — no per-kind switch. **Status write-back** is the one
generic operation `Object` alone cannot do — not every kind has status — so kinds with observed
state additionally implement `StatusObject`, exposing the embedded shared `Status` for the
controller to mutate; pure-data/policy kinds (`Config`, `Secret`, `Grant`, `EgressPolicy`) do
not. Deep copy is intentionally **not** on either interface yet (see *Temporary workarounds*).

### 4. Scope is modeled and enforced — single-sourced on the kind
`Kind.Namespaced() bool` is the **one** source of scope (cluster: `Namespace`, `RuntimeClass`,
`Worker`, `Gateway`; namespaced: the other 11). `ObjectMeta.Validate(k Kind)` derives scope from
`k.Namespaced()` — no separate scope argument that could drift — and rejects a namespaced object
with no namespace, a cluster object that carries one, and an empty/invalid `Name`; for namespaced
kinds it additionally requires `ResourceGroup`. Each kind's `Validate()` calls the shared
`validateMeta(TypeMeta, *ObjectMeta, Kind)` helper, which **also verifies `TypeMeta` matches the
kind's GVK** (so a hand-built object with empty or wrong `apiVersion`/`kind` cannot validate
clean), then delegates to `ObjectMeta.Validate`.

### 5. Typed primitives (extending ADR-0002)
Add to `api/types/v1alpha1`: `ObjectName`, `ResourceGroupName`, `UID` (named string types with
`Validate()` → `fault.Invalid`), `Tags = map[string]string`. **Reconcile `Phase`** to the
blueprint resource state machine — `Pending`, `Deploying`, `Ready`, `Idle`, `Degraded`,
`Failed`, `Terminating` — superseding the placeholder set ADR-0002's scaffold seeded
(`Scaling`/`Reconciling`/`Deleted`), as ADR-0002 explicitly deferred the exact set to F03. The
existing `NamespaceName`/`FunctionName`/`RevisionID` stay.

### 6. The 15 kinds — skeleton altitude
Each kind gets its file (`function.go`, …), per the blueprint's prescribed
`api/types/v1alpha1/` layout (this prescribed per-kind layout is the sanctioned exception to
ADR-0002 §8's "don't pre-split" default — kinds evolve independently and the layout is fixed by
the blueprint). `XSpec`/`XStatus` are **named structs** carrying only identity-intrinsic or
pure-data fields now; behavioral fields are appended by the owning feature ADR. Pure-data kinds
(`Config`, `Secret`) get their data field (it *is* the kind); behavioral kinds get an
envelope-only spec with a doc comment naming the ADR that fills it. (Full per-kind table in
*Contracts*.)

### 7. Stateless kind registry
`NewObject(Kind) (Object, bool)` returns a zero-valued typed object (TypeMeta stamped) for a
kind; `AllKinds() []Kind` lists all 15. Implemented as pure functions (a `switch` + a slice
literal) — **no package-level mutable state**, honoring ADR-0002 §5 (no globals). The
composition root, codegen, and store enumerate the model through these.

### 8. Conditions bookkeeping
`Conditions.Set(Condition)` upserts by `Type` (replace in place, never duplicate) and advances
`LastTransitionTime` only when `Status` changes; `Conditions.Get(Type)` reads one. `ConditionType`
is an open string type; well-known condition types (`Ready`, `Scheduled`, `RouteConfigured`, …)
are defined by the feature ADRs that raise them, not here.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| No `DeepCopy` on `Object`; store/controller copy via JSON round-trip if they need isolation | hand-writing deep-copies for 15 kinds is churn; codegen does it better | a generated `DeepCopyObject()` lands with the codegen ADR (F02/P-B); add it to the `Object` interface then |
| Behavioral `XSpec` structs are near-empty (envelope-only) with a "filled by Fxx" comment | the owning feature ADR hasn't decided those fields yet (Q1) | each feature ADR (F10–F16) appends its fields to the kind it owns |
| `ConditionType`/`ServiceType`/runtime-class flavor are open string types, no enumerated values | the values are feature-owned | the feature ADR that raises them defines the typed constant set |

## Contracts

`api/types/v1alpha1` — hand-written, canonical, stdlib + `api/fault` only.

### Group, kind, GVK
```go
const (
	Group   = "funcd.io"
	Version = "v1alpha1"
)

// Kind is the typed set of resource kinds.
type Kind string

const (
	KindNamespace     Kind = "Namespace"
	KindResourceGroup Kind = "ResourceGroup"
	KindFunction      Kind = "Function"
	KindRevision      Kind = "Revision"
	KindRoute         Kind = "Route"
	KindService       Kind = "Service"
	KindEventSource   Kind = "EventSource"
	KindConfig        Kind = "Config"
	KindSecret        Kind = "Secret"
	KindGrant         Kind = "Grant"
	KindEgressPolicy  Kind = "EgressPolicy"
	KindInvocation    Kind = "Invocation"
	KindRuntimeClass  Kind = "RuntimeClass"
	KindWorker        Kind = "Worker"
	KindGateway       Kind = "Gateway"
)

func (k Kind) Validate() error      // → fault.Invalid on unknown
func (k Kind) Namespaced() bool     // false for Namespace, RuntimeClass, Worker, Gateway
func (k Kind) GVK() GroupVersionKind

type GroupVersionKind struct {
	Group   string
	Version string
	Kind    Kind
}

func (gvk GroupVersionKind) APIVersion() string // "funcd.io/v1alpha1"
func (gvk GroupVersionKind) String() string     // "funcd.io/v1alpha1, Kind=Function"
```

### Envelope
```go
type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       Kind   `json:"kind"`
}

// ObjectMeta is embedded by every kind (F22: resourceGroup required, tags optional).
type ObjectMeta struct {
	Name            ObjectName        `json:"name"`
	Namespace       NamespaceName     `json:"namespace,omitempty"`     // empty ⇒ cluster-scoped
	ResourceGroup   ResourceGroupName `json:"resourceGroup,omitempty"` // REQUIRED for namespaced kinds
	Tags            Tags              `json:"tags,omitempty"`          // optional, free-form
	UID             UID               `json:"uid,omitempty"`           // carrier — set by the store (P-C)
	Generation      int64             `json:"generation,omitempty"`    // carrier — bumped on spec change (P-C/P-J)
	ResourceVersion string            `json:"resourceVersion,omitempty"` // carrier — optimistic-concurrency token (P-C)
	CreationTime    time.Time         `json:"creationTimestamp,omitempty"`
	DeletionTime    *time.Time        `json:"deletionTimestamp,omitempty"`
	OwnerReferences []OwnerReference  `json:"ownerReferences,omitempty"`
	Finalizers      []string          `json:"finalizers,omitempty"`
}

// scope is single-sourced on the Kind (Kind.Namespaced()); there is no separate Scope type.
func (m *ObjectMeta) Validate(k Kind) error // derives scope from k.Namespaced(); name DNS-label; namespace/scope consistency; resourceGroup required iff namespaced

// validateMeta is the shared envelope check every kind's Validate() calls: it verifies TypeMeta
// matches the kind's GVK, then delegates to ObjectMeta.Validate. Unexported.
func validateMeta(tm TypeMeta, m *ObjectMeta, k Kind) error

// promoted accessors (pointer receiver, so they satisfy Object on *Kind)
func (m *ObjectMeta) GetObjectMeta() *ObjectMeta
func (m *ObjectMeta) GetName() ObjectName
func (m *ObjectMeta) GetNamespace() NamespaceName
func (m *ObjectMeta) GetResourceGroup() ResourceGroupName
func (m *ObjectMeta) GetGeneration() int64

// ObjectRef addresses an object within the single v1alpha1 group/version, so it carries no
// group/version field. Cross-version refs (the v1 graduation, §1) would add apiVersion.
type ObjectRef struct {
	Kind      Kind          `json:"kind"`
	Namespace NamespaceName `json:"namespace,omitempty"`
	Name      ObjectName    `json:"name"`
}

type OwnerReference struct {
	ObjectRef          `json:",inline"`
	UID                UID  `json:"uid"`
	Controller         bool `json:"controller,omitempty"`
	BlockOwnerDeletion bool `json:"blockOwnerDeletion,omitempty"`
}
```

### Generic object + registry
```go
// Object is satisfied by every kind (via promoted ObjectMeta accessors + GroupVersionKind/Validate).
type Object interface {
	GroupVersionKind() GroupVersionKind
	GetObjectMeta() *ObjectMeta // mutable: store/admission stamp UID/Generation/ResourceVersion generically
	GetName() ObjectName
	GetNamespace() NamespaceName
	GetResourceGroup() ResourceGroupName
	GetGeneration() int64
	Validate() error
}

// StatusObject is the optional extension implemented only by kinds with observed state, giving the
// controller a generic status write-back seam. Config/Secret/Grant/EgressPolicy do NOT implement it.
type StatusObject interface {
	Object
	GetStatus() *Status // pointer to the embedded shared Status (Phase/ObservedGeneration/Conditions)
}

// Registry — stateless (no package-level mutable state; ADR-0002 §5).
func NewObject(k Kind) (Object, bool) // zero-valued, TypeMeta stamped; false if unknown
func AllKinds() []Kind                // all 15; every Kind const appears here (registry-completeness test)
```

### Typed primitives (added to ids.go / enums.go)
```go
type ObjectName string        // DNS-1123 label; Validate() → fault.Invalid
type ResourceGroupName string // DNS-1123 label; Validate() → fault.Invalid
type UID string               // server-assigned opaque id
type Tags map[string]string   // optional; map[string]string is allowed (the any-ban is interface{}/any/map[string]any)

// Phase reconciled to the blueprint resource state machine (supersedes ADR-0002 scaffold placeholders)
type Phase string
const (
	PhasePending     Phase = "Pending"
	PhaseDeploying   Phase = "Deploying"
	PhaseReady       Phase = "Ready"
	PhaseIdle        Phase = "Idle"
	PhaseDegraded    Phase = "Degraded"
	PhaseFailed      Phase = "Failed"
	PhaseTerminating Phase = "Terminating"
)
func (p Phase) Validate() error
func (p Phase) String() string
func (p Phase) IsTerminal() bool // only PhaseTerminating (the sole state with no live successor)
```

### Status + conditions
```go
type ConditionStatus string
const (
	ConditionTrue    ConditionStatus = "True"
	ConditionFalse   ConditionStatus = "False"
	ConditionUnknown ConditionStatus = "Unknown"
)

type ConditionType string // open set; well-known types defined by feature ADRs

type Condition struct {
	Type               ConditionType   `json:"type"`
	Status             ConditionStatus `json:"status"`
	ObservedGeneration int64           `json:"observedGeneration,omitempty"`
	LastTransitionTime time.Time       `json:"lastTransitionTime,omitempty"`
	Reason             string          `json:"reason,omitempty"`
	Message            string          `json:"message,omitempty"`
}

type Conditions []Condition

func (cs *Conditions) Set(c Condition)                  // upsert by Type; LastTransitionTime advances only on Status change
func (cs Conditions) Get(t ConditionType) (Condition, bool)

// Shared status base embedded by kinds with observed state.
type Status struct {
	Phase              Phase      `json:"phase,omitempty"`
	ObservedGeneration int64      `json:"observedGeneration,omitempty"`
	Conditions         Conditions `json:"conditions,omitempty"`
}
```

### Representative kinds (full)
```go
// function.go — behavioral kind: envelope-only spec now; fields appended by F11–F14.
// NOTE: `json:",inline"` flattens TypeMeta because stdlib encoding/json treats an *empty* tag
// name as "anonymous → promote fields" (the `inline` option itself is a no-op in stdlib; it's a
// yaml/k8s-codec convention). ObjectMeta keeps the name "metadata", so it nests. The roundtrip
// test guards this — do not rename the TypeMeta tag.
type Function struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       FunctionSpec   `json:"spec"`
	Status     FunctionStatus `json:"status,omitempty"`
}
type FunctionSpec struct {
	// Behavioral fields owned by feature ADRs:
	//   runtime               → F12 (runtime port)
	//   handler/artifact/shape → F13 (function contract & lifecycle)
	//   scaling               → F11 (scale-to-zero)
	//   triggers              → F10/F16 (routes / eventing)
	//   services              → F14 (KV) and the service pattern
}
type FunctionStatus struct {
	Status `json:",inline"` // embeds the shared Status (Phase, ObservedGeneration, Conditions)
	// e.g. Replicas, CurrentRevision → appended by F11/F13
}
func (f *Function) GroupVersionKind() GroupVersionKind { return KindFunction.GVK() }
func (f *Function) Validate() error                    { return validateMeta(f.TypeMeta, &f.ObjectMeta, KindFunction) }
func (f *Function) GetStatus() *Status                 { return &f.Status.Status } // implements StatusObject

// config.go — pure-data kind: its data IS the kind, so it's fully specified here.
// No Status ⇒ Config implements Object but NOT StatusObject.
type Config struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       ConfigSpec `json:"spec"`
}
type ConfigSpec struct {
	Data map[string]string `json:"data,omitempty"` // non-sensitive key/values; map[string]string is allowed
}
func (c *Config) GroupVersionKind() GroupVersionKind { return KindConfig.GVK() }
func (c *Config) Validate() error                    { return validateMeta(c.TypeMeta, &c.ObjectMeta, KindConfig) }
```

### All 15 kinds — scope, P-A content, deferred owner
| Kind | Scope | `spec`/`status` content in P-A | Behavioral fields deferred to |
|---|---|---|---|
| `Namespace` | cluster | status: `Phase` | quotas/RBAC refs → F07 |
| `ResourceGroup` | namespaced | spec: `Description` (opt); status: `Phase` | cascade-delete behavior → controller (F08) |
| `Function` | namespaced | spec: envelope only; status: `Phase`, `Conditions` | F10–F14 |
| `Revision` | namespaced | spec: `Function ObjectRef`, `Number int64` (immutable identity) | frozen snapshot payload → F13 |
| `Route` | namespaced | status: `Phase`, `Conditions` | rules/traffic split → F10 |
| `Service` | namespaced | spec: `Type ServiceType` (discriminator) | driver config/binding → F14/F23 |
| `EventSource` | namespaced | status: `Phase`, `Conditions` | source config → F16 |
| `Config` | namespaced | spec: `Data map[string]string` (full) | — |
| `Secret` | namespaced | spec: `Type SecretType`, `Data map[string][]byte` (full) | delivery/encryption driver → F15 |
| `Grant` | namespaced | envelope only | subjects/verbs → IAM ADR (V2) |
| `EgressPolicy` | namespaced | envelope only | rules → network-manager ADR (V2) |
| `Invocation` | namespaced (read-only) | status: `Phase`, `StartTime`, `EndTime`, `Error` | retention policy → F16 |
| `RuntimeClass` | cluster | spec: `Handler string` (flavor id) | flavor params → F12 |
| `Worker` | cluster (status-owned) | status: `Phase`, `Conditions` | capacity/heartbeat → multi-node ADR |
| `Gateway` | cluster | status: `Phase`, `Conditions` | listeners/TLS → F10 |

`ServiceType`/`SecretType` are open typed-string discriminators here; their enumerated values
are owned by the service/secrets ADRs.

**`StatusObject` membership**: every kind with a `status` column above implements `StatusObject`
(embeds the shared `Status`) — i.e. all kinds **except** the four pure-data/policy kinds
`Config`, `Secret`, `Grant`, `EgressPolicy`, which implement `Object` only.

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/fault` (ADR-0002) | `Validate()` returns `fault.Invalid`; the only non-stdlib import |
| Consumes | ADR-0002 typed-primitive + no-`any` conventions, `.golangci.yml` | inherited, not re-decided; this ADR is their first big exercise |
| Consumes | existing `api/types/v1alpha1/{ids.go,enums.go}` | extended (Phase reconciled, primitives added) |
| Exposes | `Object`, `GroupVersionKind`, `Kind`, `TypeMeta`, `ObjectMeta`, `ObjectRef`, `OwnerReference`, `Condition(s)`, `Status`, the 15 kinds, `NewObject`/`AllKinds` | the canonical resource model; imported *up* by store, controller, API server, codegen, SDK |
| Exposes | the spec/status struct skeletons | feature ADRs append behavioral fields to the kind they own |

## Implementation plan

No business logic — type definitions, validation, and the generic machinery, with passing
scenario tests (this ADR has no infrastructure dependencies, so **every** scenario test runs
green now — there are no harness-deferred scenarios).

1. **`api/types/v1alpha1/metadata.go`** — `Group`/`Version`, `Kind` enum + `Validate`/
   `Namespaced`/`GVK`, `GroupVersionKind`, `TypeMeta`, `ObjectMeta` + `Validate(k Kind)` +
   promoted accessors, the shared `validateMeta` helper (TypeMeta-matches-kind + envelope),
   `ObjectRef`, `OwnerReference`, the `Object` + `StatusObject` interfaces, `NewObject`/`AllKinds`.
2. **`api/types/v1alpha1/status.go`** — `ConditionStatus`/`ConditionType`/`Condition`/
   `Conditions` (`Set`/`Get`), `Status` base.
3. **Extend `ids.go`** — `ObjectName`, `ResourceGroupName`, `UID`, `Tags` (+ `Validate`).
   **Revise `enums.go`** — reconcile `Phase` to the 7 blueprint phases; update its `Validate`,
   `String`, and (re-point) `IsTerminal` to `Terminating`; update `types_test.go` accordingly.
4. **One file per kind** (15): `namespace.go, resourcegroup.go, function.go, revision.go,
   route.go, service.go, eventsource.go, config.go, secret.go, grant.go, egresspolicy.go,
   invocation.go, runtimeclass.go, worker.go, gateway.go` — each: struct embedding the
   envelope, `XSpec`/`XStatus` per the table, `GroupVersionKind()`, `Validate()` (via
   `validateMeta`), and `GetStatus()` for the status-bearing kinds (all but
   `Config`/`Secret`/`Grant`/`EgressPolicy`).
5. **Test plan** (one acceptance test per Scenario, all passing; table-driven across kinds):
   - `metadata_test.go` → `objectmeta-requires-resource-group`, `tags-optional`,
     `name-rejects-non-dns-label`, `scope-enforced`, `generic-object-access`,
     `kind-registry-roundtrips-every-kind` (incl. a **registry-completeness** assertion: every
     `Kind` const is returned by `AllKinds()` and resolves via `NewObject`), and a TypeMeta/kind
     mismatch case for `validateMeta`.
   - `roundtrip_test.go` → `json-roundtrip-stable` (marshal→unmarshal equality for all 15 via
     `AllKinds`/`NewObject`, asserting `apiVersion`/`kind`).
   - `status_test.go` → `conditions-upsert-by-type` (in-place replace; `LastTransitionTime`
     advances only on status change) and `generic-status-writeback` (mutate `Phase`/a `Condition`
     through `StatusObject.GetStatus()`; assert the four pure-data/policy kinds don't satisfy it).
   - Plus unit tests for each typed primitive's `Validate()` (valid + invalid).
6. **Definition of done** (= Scenarios executed):
   - `just ci` exits 0 (build, lint incl. the no-`any` rule, test, mod verify).
   - All 15 kinds satisfy `Object`, and all status-bearing kinds satisfy `StatusObject`
     (compile-time `var _ Object = (*Function)(nil)` / `var _ StatusObject = (*Function)(nil)`),
     while `Config`/`Secret`/`Grant`/`EgressPolicy` satisfy `Object` only.
   - `AllKinds()` returns exactly 15 and covers **every** `Kind` const; `NewObject(k)` round-trips
     every one with matching GVK (registry-completeness test passes).
   - `validateMeta` rejects a TypeMeta/kind mismatch; `ObjectMeta.Validate(k)` enforces
     resourceGroup (namespaced) and namespace/scope consistency, scope derived from the kind.
   - JSON round-trips stable with `apiVersion: funcd.io/v1alpha1`.
   - No `any`/`map[string]any` in any hand-written signature (lint green).
   - Every Scenario has a named, **un-skipped, passing** test.

## Review checklist

- [ ] `api/types/v1alpha1` imports only stdlib + `api/fault` (no `internal/**`, `pkg/**`,
      `k8s.io/**`); lint's `api/**` boundary rule passes.
- [ ] `TypeMeta`, `ObjectMeta` (with required `resourceGroup`, optional `tags`, carrier fields),
      and `Status` exist and are embedded by the kinds.
- [ ] All **15** kinds are defined, each in its own file, each implementing `Object`
      (`GroupVersionKind` + `Validate` + promoted accessors).
- [ ] `Kind.Namespaced()` is the single source of scope (cluster: Namespace, RuntimeClass,
      Worker, Gateway); `ObjectMeta.Validate(k)` derives scope from it and rejects
      namespace/scope mismatches and missing `resourceGroup`; `validateMeta` rejects a
      TypeMeta/kind mismatch.
- [ ] `NewObject`/`AllKinds` cover exactly the 15 kinds **and every `Kind` const**
      (registry-completeness test); the registry is stateless (no globals; `gochecknoglobals`
      passes).
- [ ] Status-bearing kinds implement `StatusObject` (`GetStatus() *Status`); the four
      pure-data/policy kinds (Config, Secret, Grant, EgressPolicy) implement `Object` only.
- [ ] No `interface{}`/`any`/`map[string]any` in hand-written signatures; `map[string]string`
      and `[]byte` used for `Tags`/`Config.Data`/`Secret.Data` only.
- [ ] `Phase` matches the blueprint state machine (7 values); the ADR-0002 placeholder values
      are gone and `types_test.go` is updated.
- [ ] `Conditions.Set` upserts by `Type` (no duplicates) and advances `LastTransitionTime` only
      on `Status` change.
- [ ] Behavioral kinds carry envelope-only specs with a comment naming the owning feature ADR;
      no behavioral field substance is decided here.
- [ ] Every Scenario has a named passing test; `just ci` exits 0; no identity/path leak.

## Consequences

- (+) Every later component (codegen, store, controller, API server, CLI) builds against one
  canonical, typed resource model instead of re-inventing metadata/status/naming.
- (+) The store and controller can be **generic** (the `Object` interface + `StatusObject` for
  status write-back + the registry), which is what keeps reconciliation duplication-free across
  15 kinds — without forcing a status accessor onto kinds that have none.
- (+) Resolves ADR-0002's deferred `Phase`/enum-set question; the typed-primitive conventions
  get their first real, mechanically-checked exercise.
- (+) Required `resourceGroup` in the shared `ObjectMeta` makes "every kind has a resource
  group" true by construction (F22) — admission only has to call `Validate`.
- (−) F03's scope is amended (11 → 15 kinds), a deliberate widening recorded in the feat doc;
  the four infra/record kinds (`Invocation`, `RuntimeClass`, `Worker`, `Gateway`) enter as
  **inert shells** — no behavior, so multi-node/ingress work stays V1-out — present only to
  complete the GVK registry.
- (−) Behavioral kinds start near-empty; many feature ADRs will append fields to these files
  (append-only growth — accepted, and the price of one-topic-per-ADR).
- (risk) Hand-written Go vs. OpenAPI can drift — mitigated by the drift gate F02 must build
  (called out as that ADR's responsibility).
- (risk) Carrier fields (`Generation`/`ResourceVersion`/finalizers) exist before their
  semantics are decided (P-C/P-J) — mitigated by defining them as inert carriers here, with
  lifecycle explicitly deferred.

## Open questions

| Question | Where it gets answered |
|---|---|
| Deep-copy strategy (generated `DeepCopyObject` vs. JSON round-trip) and adding it to `Object` | codegen ADR (F02/P-B) |
| How OpenAPI schemas reference/mirror these Go types + the drift CI gate | codegen ADR (F02/P-B) |
| Lifecycle of carrier fields: when `Generation` bumps, how `ResourceVersion` is minted, finalizer handling | store/controller ADRs (F05/P-C, F08/P-J) |
| Behavioral `spec`/`status` fields per kind (runtime, scaling, triggers, rules, listeners, grant subjects, egress rules) | each owning feature ADR (F10–F16, IAM/network V2) |
| Enumerated values for `ServiceType`, `SecretType`, `RuntimeClass` handler/flavor | service/secrets/runtime ADRs (F14/F15/F12) |
| Admission wiring that invokes `Validate` + defaulting + status-stripping | API-server ADR (F07/P-L) |
| Cross-version object references (group/version on `ObjectRef`) | the v1 graduation — a new `api/types/v1` package (§1) |

## References

- Kubernetes apimachinery — `TypeMeta`, `ObjectMeta`, `OwnerReference`, `metav1.Condition`
  (shape adopted, dependency not).
- [OAM spec](https://github.com/oam-dev/spec) — application/component resource modeling.
- RFC 1123 (DNS labels); RFC 9457 (problem+json, via `api/fault`).
- [ADR-0002](0002-source-code-conventions-and-patterns.md) — typed primitives, `api/fault`,
  no-`any`, package idioms (this ADR resolves its deferred enum/ID question).
- [blueprint.md](../../blueprint.md) — "Resources definition (CRD-like)", "Resource groups &
  tags", "Resource model".
