# ADR-0133: Server-side name generation (ObjectMeta.GenerateName)

- **Status**: Implemented
- **Implemented**: 2026-07-12 — implemented + tested this session on branch `feat/funcdctl-contract-codegen`:
  `TestGenerateObjectName` (api) + `TestCreateWithGenerateName` (store) pass, the OpenAPI spec is regenerated
  (`just generate`) and `TestSpecGeneratedFromGo` is green, four sub-checks green both build tags, lint 0 issues,
  and it is proven live — `funcdctl workflow run releve-pipeline` (no run-name) creates `releve-pipeline-<8hex>`
  and re-runs never collide.
- **Date**: 2026-07-12
- **Deciders**: green-0-rabbit
- **Tags**: api, store, control-plane, funcdctl, dx
- **Realizes**: [FEAT-0001/F98](../feat/0001-feat-v1.1.md) (server-side name generation — `generateName`)
- **Relates to**: [ADR-0005](0005-resource-model-and-envelope.md) (the ObjectMeta envelope this extends) ·
  [ADR-0006](0006-store-port-and-drivers.md) (store.Create, where the name is assigned) · [ADR-0018](0018-api-server-authn-rbac-admission.md)
  (the create handler that generates before admit) · [ADR-0094](0094-workflow-engine-core.md) (WorkflowRun — the
  first consumer, `funcdctl workflow run`) · [ADR-0109](0109-sensor-event-actions.md) (the Sensor already names
  runs `<sensor>-<action>-<hex>`; this unifies the convention)

## Context & Need

Creating a resource required the client to invent a **unique name**. For `WorkflowRun` this is pure friction: a
run is one immutable execution record, so re-running the same workflow demanded a *new* name each time (`funcdctl
workflow run <wf> <run-name>` was `ExactArgs(2)`) — reuse collided (`already exists`). Callers worked around it
with timestamps or delete-then-recreate. Kubernetes solved this long ago with **`metadata.generateName`**: give a
prefix, the server assigns `<prefix><random>`. funcd had no such mechanism — yet the Sensor *already* generates
run names (`<sensor>-<action>-<hex>`, ADR-0109), so the convention existed, just not as a first-class server
feature. This ADR makes it one, so any client (CLI, Sensor, API) can create without naming.

## Scenarios

- **scenario: generate-on-create** — Given an object with an empty `Name` and a `GenerateName` prefix, When it is
  created, Then the server assigns `Name = GenerateName + <8 hex>` and returns it.
- **scenario: unique-across-creates** — Given two creates with the same `GenerateName`, Then the two assigned names
  differ (no collision), and a (near-impossible) collision is resolved by the store retrying with a new suffix.
- **scenario: workflow-run-no-name** — Given `funcdctl workflow run <wf>` with no run-name, Then the run is created
  as `<wf>-<id>` (printed), and repeated invocations never collide.
- **scenario: explicit-name-unchanged** — Given a create WITH a `Name` (no `GenerateName`), Then behaviour is
  unchanged — the name is required and used as-is; a duplicate still conflicts.

## Scope

**In**: an `ObjectMeta.GenerateName` prefix field (all kinds); server-side assignment in `store.Create`
(`Name = GenerateName + GenerateObjectName suffix`, retry-on-conflict); the shared `GenerateObjectName(prefix)`
helper (the one naming convention); `Name` becomes schema-optional (`json:"name,omitempty"` — the store still
requires a non-empty Name once assigned); the control-plane create path assigns the name *before* admission; the
SDK creates a name-less object via a collection POST; and `funcdctl workflow run <wf> [run-name]` (run-name
optional). The OpenAPI spec is regenerated.

**Out**: migrating the Sensor onto `GenerateName` (it keeps its own `runName` for now — a follow-up); generateName
for `apply`/`replace` (it is a **create**-only concept); a user-visible name-template beyond a prefix.

## Constraints & Decision drivers

- **k8s-aligned + one convention** — mirror `generateName`; reuse the Sensor's `<prefix><8 hex>` shape so
  CLI-, Sensor-, and API-created names are indistinguishable.
- **The store is the authority** — the name is assigned server-side (not by the client), and uniqueness is
  *guaranteed* by the store's conflict retry, not merely by entropy.
- **Additive & backward-compatible** — an explicit `Name` behaves exactly as before; only an empty-Name create
  gains the new behaviour. Zero new deps (`crypto/rand`, already the codebase's randomness source).

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| Client-side unique names (timestamp / delete-first) | No platform change | Every client reinvents it; timestamps are ugly; delete-first destroys history. **Rejected.** |
| CLI-side generation in `funcdctl workflow run` only | Smallest | Doesn't help the Sensor or other clients; not a platform capability. **Rejected** (was the interim). |
| Server-side `ObjectMeta.GenerateName` ✅ | The k8s pattern; one convention for all clients | Chosen — the store assigns + guarantees uniqueness; any client benefits. |

## Decision

Add `ObjectMeta.GenerateName` (a plain prefix string). On **create**, when `Name` is empty and `GenerateName` is
set, the server assigns `Name = GenerateObjectName(GenerateName)` = the prefix (truncated to keep the name ≤63-char
DNS-1123) + a **`crypto/rand` 4-byte → 8-hex-char** suffix (16⁸ ≈ 4.3e9 per prefix), and `store.Create` **retries
with a fresh suffix on the near-impossible conflict** (bounded), so uniqueness is guaranteed. The control-plane
create handler fills the name **before admission + store validation** (both require a non-empty Name); `Name`
becomes `json:"name,omitempty"` so a name-less create passes request-schema validation (the store still enforces a
non-empty Name). The SDK, seeing an empty Name, POSTs to the collection (the server assigns the name). `funcdctl
workflow run <wf> [run-name]` makes the run-name optional — omitted → `GenerateName = "<wf>-"`, and the assigned
`<wf>-<id>` is printed.

## Temporary workarounds

- **The Sensor keeps its own `runName`** rather than setting `GenerateName`. Exit criterion: migrate the Sensor to
  set `GenerateName = "<sensor>-<action>-"` and delete `runName`/`randHex`, so there is a single code path. A
  small follow-up; both already produce the identical `<prefix><8 hex>` shape via `GenerateObjectName`.

## Contracts

```go
// api/types/v1alpha1 — ObjectMeta gains a create-only prefix; Name becomes schema-optional.
type ObjectMeta struct {
    Name         ObjectName `json:"name,omitempty"`
    GenerateName string     `json:"generateName,omitempty" pattern:"^[a-z0-9][a-z0-9-]{0,62}$"`
    // …
}
// GenerateObjectName(prefix) → ObjectName = bounded(prefix) + crypto/rand 8-hex suffix (the one convention).
func GenerateObjectName(prefix string) ObjectName

// internal/store — Create assigns + retries when GenerateName is set; else the normal single attempt.
func (s *store) Create(ctx, obj) (v1.Object, error) // GenerateName ⇒ Name = GenerateObjectName(prefix), retry on Conflict

// internal/controlplane — createObj fills Name from GenerateName BEFORE admit + store validate.
// pkg/sdk — Apply with an empty Name + GenerateName POSTs to the collection (server assigns the name).
// cmd/funcdctl — `workflow run <workflow> [run-name]`; omitted ⇒ ObjectMeta.GenerateName = "<workflow>-".
```

| consumes | exposes |
|---|---|
| the ADR-0005 ObjectMeta; ADR-0006 store.Create; ADR-0018 create handler; `crypto/rand` | `ObjectMeta.GenerateName`; `GenerateObjectName`; name-less create (SDK/CLI); `funcdctl workflow run <wf>` |

## Implementation plan

**Files**
- `api/types/v1alpha1/metadata.go` — `GenerateName` field; `Name` → `omitempty`; `GenerateObjectName` helper.
- `internal/store/store.go` — `Create` generate-and-retry wrapper over the extracted `createOnce`.
- `internal/controlplane/handlers.go` — `createObj` assigns the name before `admit`.
- `pkg/sdk/sdk.go` — `Apply` routes an empty-Name + GenerateName object to the collection POST.
- `cmd/funcdctl/workflow.go` — `run <workflow> [run-name]` (`RangeArgs(1,2)`); optional name → `GenerateName`.
- `api/openapi/funcd.v1alpha1.yaml` — regenerated (`just generate`).

**Test plan** (named tests)
- `TestGenerateObjectName` (api) — `prefix + 8 hex`, a valid ObjectName, random across calls, bounded to 63.
- `TestCreateWithGenerateName` (store) — an empty-Name + GenerateName create is assigned `cfg-<8 hex>`, the
  caller's input stays read-only, and two creates get distinct names.
- `TestSpecGeneratedFromGo` (control-plane) — the regenerated spec matches (guards the schema change).

**Definition of done**: four Go sub-checks green both tags; the named tests + the spec test pass; `funcdctl
workflow run <wf>` (no name) creates `<wf>-<id>` and re-runs never collide.

## Review checklist

- [ ] Empty Name + GenerateName ⇒ server-assigned `<prefix><8 hex>`; explicit Name unchanged (still required).
- [ ] `store.Create` retries a generated-name conflict (bounded); a user-name conflict is returned as-is.
- [ ] Name assigned before admission + store validation; `Name` is `omitempty` but the store still requires it.
- [ ] SDK creates a name-less object via the collection POST; `funcdctl workflow run <wf>` prints the assigned name.
- [ ] OpenAPI spec regenerated; `GenerateObjectName` is the single naming convention (Sensor migration noted).
- [ ] Named tests pass; no `any`; `api/fault`; ctx-first.

## Consequences

**Positive**: any client can create without inventing a unique name — `funcdctl workflow run <wf>` just works and
re-runs never collide (no timestamps, no delete-first); the store *guarantees* uniqueness; one naming convention
across CLI/Sensor/API. **Negative (accepted)**: `Name` is now schema-optional, so a truly nameless create (no
GenerateName) is caught at the store rather than the request schema — a slightly later, but equally firm, error.
**Neutral**: the Sensor still uses its own `runName` until a follow-up migrates it; explicit-name and the thin
release client are otherwise unaffected.

## Open questions

- **Migrate the Sensor to `GenerateName`** (drop `runName`/`randHex`) so there is exactly one run-naming path —
  a small follow-up on the board.

## References

- Kubernetes `metadata.generateName`; ADR-0005 (ObjectMeta), ADR-0006 (store), ADR-0018 (create handler),
  ADR-0094 (WorkflowRun), ADR-0109 (the Sensor's existing run-naming this unifies).
