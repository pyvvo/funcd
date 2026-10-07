# ADR-0048: DTO validation reference — the per-field validation table for every v1alpha1 entity

- **Status**: Implemented
- **Superseded in part by**: [ADR-0194](0194-api-duration-strings.md) (2026-10-07) — for durations only: the `nonNegInt` (67) and literal-ns duration-cap (69) rows, the schema-enforced bounds (80, 124, 196) and the int64-ns wire form (274): a duration is a string bounded by `CheckDuration` in `Validate`.
- **Date**: 2026-06-16 (**Accepted 2026-06-16** · **Implemented 2026-06-16** — judge: no Blockers/Majors (decision sound, tables verified against the
  real types, the duration-tag mechanism confirmed implementable, the huma-in-api cost honestly disclosed = 5 SDK packages).
  Folded 4 Minors: huma is **MIT** (not Apache-2.0); the **duration bound is the literal-ns tag as sole source** (a tag can't
  reference a const — the `MaxDuration` "single-source const" claim was wrong); added `toAny` to Contracts; noted
  `ResourceGroup` is status-bearing; clarified the replica cap (15) vs ADR-0046 pool cap (16) are different axes.
  **Reconciled during implementation** (the implement gate found 3 adr-attributed row defects): dropped `uriPattern`
  (`ArtifactRef.URI` accepts bare registry refs — a `scheme://` pattern would reject them); field *presence*
  (runtime/handler/artifact non-empty) is the **shape gate's** job (ADR-0020), not admission `Validate()` (it duplicated a
  layer + broke the shape-gate test); relaxed the timer `interval` floor 1s → **100ms** (a 1s floor rejected legitimate
  sub-second timers + the e2e fixture). The tables above reflect the corrected rules.)
- **Deciders**: green-0-rabbit
- **Tags**: validation, api, dto, huma, schema, admission
- **Realizes**: [FEAT-0000/F03](../feat/0000-feat-v1.md) (resource model & typed validation) — also extends
  [F07](../feat/0000-feat-v1.md) (API-server admission)
- **Relates to / refines**: [ADR-0003](0003-resource-model-and-api-typing.md) (the typed model + `Validate()`),
  [ADR-0005](0005-api-surface-code-first-huma.md) (huma generates the schema; tags + `SchemaProvider` feed it),
  [ADR-0018](0018-api-server-authn-rbac-admission.md) (admission runs `Validate()` on writes),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (**`api/types` may now import `github.com/danielgtaylor/huma/v2`**
  for `SchemaProvider`; the depguard rule — no `internal/`/`pkg/` — still holds)

## Context & Need

Validation was spread across procedural `Validate()` methods with nothing in the published schema. This ADR is the **single
normative reference**: every DTO field, its type, its constraint, and the **one layer** that enforces it. The tables below
*are* the spec — an implementer applies exactly the `Layer` column.

## Scenarios

- **scenario: schema-rejects-at-edge** — a body violating a schema constraint (bad `name` pattern, `replicas: -1`) is rejected
  **422 by huma before the handler**; the constraint is visible in the OpenAPI.
- **scenario: validate-rejects-semantic** — a body passing the schema but breaking a cross-field rule (`minReplicas >
  maxReplicas`, missing `resourceGroup` on a namespaced kind) is rejected by `Validate()` at admission (`fault.Invalid` → problem+json).
- **scenario: single-source-no-drift** — a leaf's pattern/enum is declared **once** in Go and referenced by both its
  `Schema()` and its `Validate()`; the OpenAPI and the server check can't disagree.

## Decision

1. **Two layers, one source.** A constraint lives in **exactly one** layer: the **schema** if expressible (pattern, enum,
   range, length, format, required) — enforced at the edge by huma and documented in the OpenAPI; else **`Validate()`** (cross-field,
   conditional, contextual, referential). A pattern/enum/const has **one** Go declaration, referenced by both `Schema()` and `Validate()`.
2. **Where the schema constraint lives:** a **`tag`** for a field-local one-off; a **`SchemaProvider`** on a typed leaf when
   the rule is reusable (DNS-1123, an enum) — then every field of that type inherits it by reflection.
3. **`api/types` imports huma** for the `SchemaProvider` methods. Accepted cost: the Go SDK transitively pulls huma (pure-Go,
   MIT). The `ids.go` "stdlib-only" note is corrected.
4. **The tables are normative.** `Layer` ∈ {`tag`, `schema`, `Validate`, `store`} — see the key. Required-ness: a field with
   **no `omitempty`** is schema-required (huma); contextually-required fields are marked `Validate`.
5. **Bounded by default (predictability).** Every numeric field carries a **finite `maximum`** — no unbounded resource
   request. A function's replica fields are capped at **15** (15 × ~56 MB ≈ 0.8 GB ceiling per function on the target box;
   a different axis from ADR-0046's pool cap of 16, which bounds *member functions per pool worker*); durations are bounded
   too. An unbounded count/interval is a defect, not an omission.

**Layer key:** `tag` = huma struct tag · `schema` = `SchemaProvider` on the typed leaf · `Validate` = semantic method ·
`store` = server-owned (rejected/ignored on input, not user-validated).

## Reusable schema fragments & custom validators

Declared once in `api/types/v1alpha1`, referenced by the tables. New ones this ADR adds are marked **(new)**.

| Fragment | Definition | Used by |
|---|---|---|
| `DNSLabel` (const) + `dnsLabel` (regexp) | `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$` | every DNS-1123 leaf's `Validate()` |
| `dnsLabelSchema()` | `{type:string, pattern:DNSLabel, minLength:1, maxLength:63}` | the DNS-1123 typed-ID `Schema()`s |
| `enumSchema(vals…)` **(new)** | `{type:string, enum:[vals…]}` | every typed enum's `Schema()` |
| `nonNegInt` tag | `minimum:"0"` | every count/duration field |
| **replica cap** `maximum:"15"` | literal `15` in the tag (the tag is the sole source — no Go code reads a replica max) | `replicas`, `minReplicas`, `maxReplicas` *(done)* |
| **duration cap** (literal ns) | `minimum:"100000000"` (100ms, interval) · idleTimeout min `0` · `maximum:"86400000000000"` (24h) — the **tag literal is the sole source** (a Go struct tag can't reference a const; no code reads a duration max, so no drift). Human value in a `//` comment. | `idleTimeout`, `interval` |
| `handlerPattern` **(new)** | `^[A-Za-z_][A-Za-z0-9_.]*$` | `Function/Revision Handler` |
| `digestPattern` | `^sha256:[a-f0-9]{64}$` | `ArtifactRef.Digest` |

> **No `uriPattern`.** `ArtifactRef.URI` is **not** scheme-constrained: funcd accepts `oci-layout://`, `file://`, **and bare
> registry refs** (`host/path:tag`, no scheme) — the materializer parses the concrete ref (ADR-0031); a `scheme://` pattern
> would reject the production registry case. URI is non-empty only, and **presence is the shape gate's job** (below).
>
> **Field *presence* (runtime/handler/artifact non-empty) is the materialization shape gate's job**, not admission —
> ADR-0020's `NewBasicValidator` + the shim write `ShapeValid: False` and block `Ready`. Re-checking presence in admission
> `Validate()` would duplicate a layer (one constraint, one layer) and is *not* done. Admission `Validate()` owns only what
> no other layer does: cross-field/conditional rules. Field *format* (handler/digest patterns, replica/duration bounds) is
> schema-enforced at the edge.

Typed leaves that gain a `Schema()` (each returns the shared fragment — one method, all call sites): `ObjectName` *(done)*,
`NamespaceName`, `ResourceGroupName`, `FunctionName` → `dnsLabelSchema()`; `Phase`, `ServiceType`, `SecretType`,
`EventSourceType`, `ConditionStatus` → `enumSchema(...)`.

## Validation tables

`Req` = required. Constraints not in a leaf's `Schema()` are applied as a field `tag` or in `Validate`.

### Shared `ObjectMeta` (every kind)

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| name | `ObjectName` | ✓ | DNS-1123 (pattern, len 1–63) | schema |
| namespace | `NamespaceName` | ✓ namespaced · ✗ cluster | DNS-1123; required iff namespaced, must be empty iff cluster | schema (pattern) + Validate (scope) |
| resourceGroup | `ResourceGroupName` | ✓ namespaced | DNS-1123; required iff namespaced | schema (pattern) + Validate (required) |
| tags | `map[string]string` | — | optional; keys DNS-1123-subdomain, values ≤63 (V1: free) | Validate (deferred) |
| finalizers | `[]string` | — | each non-empty | Validate |
| uid · generation · resourceVersion · creationTimestamp · deletionTimestamp · ownerReferences | — | — | server-set; ignored on input | store |

### `FunctionSpec`

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| runtime | `RuntimeName` **(new leaf)** | ✓ | DNS-1123 (a `RuntimeClass` name ref) | schema (`dnsLabelSchema`) |
| handler | `string` | ✓ | `handlerPattern` (format); presence → shape gate | tag (`pattern`); presence = shape gate (ADR-0020) |
| artifact | `ArtifactRef` | ✓ | see ArtifactRef | — |
| replicas | `int` | — | **0 ≤ r ≤ 15** — the static desired count | tag (`minimum:0 maximum:15`) *(done)* |
| scaling | `Scaling` | — | see Scaling | — |
| pooling | `Pooling` | — | see Pooling | — |

> **`replicas` vs `scaling`:** `replicas` = *how many run when up* (the static count). `scaling.minReplicas` = the floor —
> `0` enables scale-to-zero (reclaim when idle, wake to ≥1 on a request), `>0` is a hard floor (`effective = max(replicas,
> minReplicas)`). `scaling.maxReplicas` = the autoscale ceiling (recorded; 1→N is V3). `idleTimeout` = reclaim delay. All
> replica fields share the **≤ 15** cap so the effective count can never exceed it.

### `Scaling`

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| minReplicas | `int` | — | 0 ≤ ≤ 15 | tag (`minimum:0 maximum:15`) *(done)* |
| maxReplicas | `int` | — | 0 ≤ ≤ 15; **minReplicas ≤ maxReplicas** (when maxReplicas > 0) | tag (`minimum:0 maximum:15`) *(done)* + Validate (cross-field) |
| idleTimeout | `time.Duration` | — | 0 ≤ ≤ 24h (int64 ns) | tag (`minimum:"0" maximum:"86400000000000"`) `// 24h` |

### `Pooling`

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| worker | `string` | — | DNS-1123 when set | Validate *(done — Function.Validate)* |

### `ArtifactRef`

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| uri | `string` | ✓ | **non-empty only** — `oci-layout://` · `file://` · or a bare registry ref (no scheme pattern: it would reject registry refs); presence → shape gate | shape gate (ADR-0020) |
| digest | `string` | — | when set, `^sha256:[a-f0-9]{64}$` | tag (`pattern`) |

### `RevisionSpec`  *(server-stamped from the Function — immutable; user does not POST it)*

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| function | `ObjectRef` | ✓ | see ObjectRef | — |
| number | `int64` | ✓ | ≥ 1 | tag (`minimum:1`) |
| runtime | `RuntimeName` | ✓ | DNS-1123 | schema |
| handler | `string` | ✓ | `handlerPattern` | tag |
| artifact | `ArtifactRef` | ✓ | see ArtifactRef | — |

### `ObjectRef` (in RevisionSpec, OwnerReference)

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| kind | `Kind` | ✓ | one of the 15 known kinds | schema (enum) + Validate (`Kind.Validate`) |
| namespace | `NamespaceName` | — | DNS-1123 when set | schema |
| name | `ObjectName` | ✓ | DNS-1123 | schema |

### `ServiceSpec`

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| type | `ServiceType` | ✓ | enum {`kv`, `blob`} | schema (enum) |
| kv | `*KVServiceSpec` | iff type=kv | present iff `type==kv` | Validate (conditional) |
| blob | `*BlobServiceSpec` | iff type=blob | present iff `type==blob` | Validate (conditional) |

### `KVServiceSpec` / `BlobServiceSpec`

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| binding | `string` | ✓ | DNS-1123 | tag (`pattern`) + Validate (non-empty) |

### `ConfigSpec`

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| data | `map[string]string` | — | keys non-empty (env-name pattern `^[A-Za-z_][A-Za-z0-9_]*$` deferred) | Validate (deferred) |

### `SecretSpec`

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| type | `SecretType` | ✓ | enum {`Opaque`} | schema (enum) |
| data | `map[string][]byte` | — | keys non-empty | Validate |

### `EventSourceSpec`

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| type | `EventSourceType` | ✓ | enum {`http`, `timer`} | schema (enum) |
| timer | `*TimerSpec` | iff type=timer | present iff `type==timer` | Validate (conditional) |
| function | `ObjectName` | ✓ | DNS-1123 (target ref) | schema |

### `TimerSpec`

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| interval | `time.Duration` | ✓ | 100ms ≤ ≤ 24h (floor bars a μs/ns fire-storm yet allows sub-second timers; ceiling bars unbounded) | tag (`minimum:"100000000" maximum:"86400000000000"`) `// 100ms–24h` |

### `RuntimeClassSpec`

| Field | Type | Req | Constraint | Layer |
|---|---|---|---|---|
| handler | `string` | — | `handlerPattern` when set | tag (`pattern`) |

### Envelope-only kinds (no user spec — `Validate()` = `validateMeta` only)

| Kind | Spec | Note |
|---|---|---|
| `Namespace` | — (status-owned) | cluster-scoped; meta only |
| `Route` | empty | spec fields are a future ADR |
| `Grant` · `EgressPolicy` | empty | subject/verbs/rules are a future ADR |
| `Invocation` | — (status-owned) | read-only record |
| `WorkerNode` · `Gateway` | — (status-owned) | cluster infra shells |
| `ResourceGroup` | `description: string` (free) | status-bearing; meta only + free description |

## Contracts

```go
// api/types/v1alpha1 — the SchemaProvider surface (huma). Each typed leaf returns a shared fragment.
func (ObjectName) Schema(huma.Registry) *huma.Schema        { return dnsLabelSchema() } // done
func (NamespaceName) Schema(huma.Registry) *huma.Schema     { return dnsLabelSchema() }
func (ResourceGroupName) Schema(huma.Registry) *huma.Schema { return dnsLabelSchema() }
func (FunctionName) Schema(huma.Registry) *huma.Schema      { return dnsLabelSchema() }
func (RuntimeName) Schema(huma.Registry) *huma.Schema       { return dnsLabelSchema() } // new typed leaf for FunctionSpec.Runtime

func toAny(ss []string) []any { out := make([]any, len(ss)); for i, s := range ss { out[i] = s }; return out } // new (huma Enum is []any)
func enumSchema(vals ...string) *huma.Schema { return &huma.Schema{Type: huma.TypeString, Enum: toAny(vals)} } // new
func (ServiceType) Schema(huma.Registry) *huma.Schema     { return enumSchema(string(ServiceTypeKV), string(ServiceTypeBlob)) }
func (SecretType) Schema(huma.Registry) *huma.Schema      { return enumSchema(string(SecretTypeOpaque)) }
func (EventSourceType) Schema(huma.Registry) *huma.Schema { return enumSchema(string(EventSourceTypeHTTP), string(EventSourceTypeTimer)) }
func (Phase) Schema(huma.Registry) *huma.Schema           { return enumSchema(/* the 7 phases */) }
```

Tag conventions (huma reads these via reflection — no import): `minimum`, `exclusiveMinimum`, `maximum`, `minLength`,
`maxLength`, `pattern`, `enum`, `format`, `required`. **Dependencies & I/O:** `api/types` → `huma` (new); no signature change;
no new module beyond huma (already in `go.mod`).

## Implementation plan

1. **Leaves:** add `Schema()` to `NamespaceName`/`ResourceGroupName`/`FunctionName` (→ `dnsLabelSchema()`); add the
   `RuntimeName` typed leaf and retype `FunctionSpec.Runtime`/`RevisionSpec.Runtime`; add `enumSchema` + `Schema()` on
   `ServiceType`/`SecretType`/`EventSourceType`/`Phase`.
2. **Tags:** apply the `tag` column (`pattern` on handler/digest/binding — **not** uri, no scheme pattern; `minimum`/`maximum`
   on number/interval/idleTimeout). `Replicas`/`MinReplicas`/`MaxReplicas` `minimum:0 maximum:15` already done.
3. **`Validate()`:** add only the cross-field/conditional rows — `Scaling` (minReplicas ≤ maxReplicas),
   `ServiceSpec`/`EventSourceSpec` (type↔sub-spec presence, binding/function non-empty). **Not** runtime/handler/artifact
   presence — that is the shape gate's job (ADR-0020), not admission (one constraint, one layer).
4. **Doc:** correct the `ids.go` "stdlib-only" note (it imports huma now); `just generate`; verify every constraint appears
   in `api/openapi/funcd.v1alpha1.yaml`.
5. **Tests:** extend `internal/controlplane/api_test.go` `schema-rejects-invalid-dto` with one rejection per new schema
   constraint (bad runtime, bad uri, unknown enum) → 422; `api/types` unit tests for the new `Validate` cross-field rules.
6. **Verify:** `just ci` green; OpenAPI not stale; no identity leak.

## Review checklist

- [ ] Every `schema`/`tag` row appears in the generated OpenAPI; a violating body is rejected **422 at the edge** (one test per new constraint).
- [ ] Every `Validate` row is enforced at admission (cross-field/conditional unit tests pass).
- [ ] Each reusable fragment (`dnsLabelSchema`, `enumSchema`, patterns) has **one** Go declaration; leaves reference it, no per-field duplication.
- [ ] `ids.go` "stdlib-only" note corrected; `api/` still imports no `internal/`/`pkg/` (depguard green).
- [ ] No `Validate()` rule duplicates a constraint the schema already enforces (one layer per constraint); `just ci` green; no new module.

## Consequences

- (+) **The OpenAPI is the contract of record** — clients see + enforce every constraint; bad DTOs 422 at the edge.
- (+) **Single source, no drift** — patterns/enums declared once, shared by `Schema()` and `Validate()`.
- (+) **This table is the living validation map** — one place to read or extend the whole surface.
- (−) **`api/types` (and the SDK) now depend on huma** (5 packages) — accepted (pure-Go, MIT).
- (−) **A `Validate` row still needed for what JSON Schema can't say** (cross-field, conditional, referential) — two layers remain by design.

## Open questions

- **Referential checks** (runtime → an existing `RuntimeClass`, service `binding` uniqueness) need store access → **admission
  (handler)**, not the type's `Validate()`; their own follow-up.
- **`tags`/`config.data` key patterns** — left free in V1; tighten when a concrete rule is needed.
- **`time.Duration` wire form** — serializes as int64 ns; a human `"30s"` format is a separate codec decision.

## References

- [ADR-0003](0003-resource-model-and-api-typing.md) · [ADR-0005](0005-api-surface-code-first-huma.md) ·
  [ADR-0018](0018-api-server-authn-rbac-admission.md) · huma `SchemaProvider`/validation tags · `api/openapi/funcd.v1alpha1.yaml`.
