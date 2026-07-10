# ADR-0116 implementation review — capability authorization framework

## Verdict: pass — 0 blockers, 0 majors  (ADR-0116 implementation, model: claude-fable-5)

A behavior-preserving refactor that lands cleanly: the `Capability`/`PrincipalSource`/`Registry`
abstraction is assembled from the registered set, `EntitiesFor` no longer carries a per-capability
branch, and the five pre-existing cedar suites pass **byte-unchanged**. Two contract-detail
completions the implementer flagged are **`adr`-attributed** (the frozen Contract was imperfect) and
are faithful, minimal fixes that preserve the decision — they do not block the pass and do not require
a superseding ADR.

## Verification (captured)

| Check | Command | Exit |
|---|---|---|
| build | `nix develop -c go build ./...` | 0 |
| lint | `nix develop -c go tool golangci-lint run ./internal/auth/... ./pkg/funcd/ ./api/types/...` | 0 (`0 issues`) |
| test | `nix develop -c go test ./internal/auth/... ./pkg/funcd/ ./api/types/... -count=1` | 0 |
| mod verify | `nix develop -c go mod verify` | 0 (`all modules verified`) |
| five suites diff | `git diff --stat -- cedar_test.go invoke_test.go s3_test.go s3_provider_test.go schema_test.go` | 0 / **empty** |

`ld: warning … built for newer 'macOS'` lines on the lint run are host-linker noise, not findings.
`just ci`'s git-diff gate fails only because the tracked-file changes are uncommitted — expected per
CLAUDE.md and satisfied on commit.

## Behavior-preserving primary bar — CONFIRMED

The five existing suites (`cedar_test.go`, `invoke_test.go`, `s3_test.go`, `s3_provider_test.go`,
`schema_test.go`) are **unchanged**: `git diff --stat` on them is empty, no working-tree status entry,
timestamps untouched (the four consumer suites at their prior mtime; only the new `capability_test.go`
is new). No assertion was weakened or deleted. They pass against the registry-assembled
provider/schema/built-ins.

## Two adr-attributed contract completions (faithful — not model-scored)

### 1. `SupportingEntityTypes []cedartypes.EntityType` on `Capability`  ·  attribution: `adr`

The frozen Contract listed only `EmitsEntityType` (singular), which would make `KnownEntityType` a union
of {KVTable, Function, BlobPrefix} — 3 types. But `ValidateCedar` marshals a policy's scope and checks
the entity types cedar-go emits: `resource in KVStore::"…"` marshals as type `KVStore`, and the S3 path
admits `principal == S3Identity::"…"`. `schema_test.go:17` (`resource in KVStore::"default/orders"`,
frozen) therefore genuinely needs `KVStore` in the vocabulary; the s3 path needs `Bucket` + `S3Identity`.
The completion adds a **typed** `[]cedartypes.EntityType` field (no `any`), assembled from the registry
exactly like `EmitsEntityType` (`KnownEntityType` unions `EmitsEntityType ∪ SupportingEntityTypes`,
capability.go:134-146) — KV contributes `+KVStore`, S3 contributes `+Bucket,+S3Identity`
(capabilities.go:73,196). Nothing hand-listed. `schema_test` passes unchanged. This is the minimal
faithful fix; the singular field was a contract-detail omission, not a decision change.

### 2. `PrincipalObject` declared in `api/types/v1alpha1`, re-exported from cedar as an alias  ·  attribution: `adr`

The Contract placed the closed interface in `cedar`, but a Go interface sealed by an **unexported** marker
method can only be satisfied inside the interface's own package — so `*v1.Function` could never satisfy a
`cedar`-declared one. The completion declares `PrincipalObject interface{ isPrincipalObject() }` in
`api/types/v1alpha1` (function.go), seals it with unexported markers on `*Function` (function.go) and
`*CatalogService` (catalogservice.go), and re-exports it from cedar as `type PrincipalObject =
v1.PrincipalObject` (capability.go:73). It is genuinely closed (only api/types implements it), carries no
`any`, and the `cedar.PrincipalObject` name still holds (a type alias). Faithful; the placement was a
mechanical Go-visibility correction, not a decision change.

## ✅ Verified correct (keep it)

- **Composite provider carries no per-capability branch.** `registryProvider.EntitiesFor`
  (entities.go:55-129) loops the registered capabilities for both the binding Set and resource dispatch;
  a grep of `entities.go` for `kvBindings`/`blobBindings`/`"links"`/`Spec.KV/Links/Blob`/`FunctionBlob`
  finds only prose in comments, no code branch. The kv/invoke/s3 binding+resource logic lives in
  `capabilities.go` (`KVCapability`/`kvResource`, `InvokeCapability`/`invokeResource`,
  `S3Capability`/`s3Resource`); the Function-then-CatalogService resolution lives in the two sources
  (`FunctionPrincipalSource`/`CatalogServicePrincipalSource`).
- **Resource dispatch is by the `Resource` func's `ok`** (entities.go:112-127) — never a
  `v1.Kind`↔entity-type match; unmodeled resource → `fault.Internalf` (a wiring bug), as before.
- **`KnownAction`/`KnownEntityType`/built-ins assembled from the registry** — `schema.go`'s exported
  helpers delegate to `defaultRegistry`; `curatedActions`/`curatedEntityTypes` maps are gone;
  `policies.go` `compile()` now uses `defaultRegistry.Builtins()` (the per-capability `//go:embed` moved
  to `capabilities.go`, each `.cedar` owned by its `Capability.Builtin`).
- **`NewRegistry` validation** (capability.go:90-116): rejects empty/duplicate `Name`, duplicate
  `Action` across capabilities, and an empty source list — all `fault.Invalid`. `TestNewRegistryValidation`
  passes.
- **New-capability-no-shared-edit proven** — `TestScenarioNewCapabilityNoEntitiesForEdit` registers a
  test-only `dummyCapability()` and authorizes it with zero edits to the shared assembly.
- **Every Scenario has a named passing test**: `existing-behavior-preserved` (the 5 suites, unchanged),
  `TestScenarioNewCapabilityNoEntitiesForEdit`, `TestScenarioBindingGrantStillSelfEnforces`,
  `TestScenarioAssembledSchemaCoversAll`, `TestScenarioUnmodeledPrincipalOrResourceFaults`, plus
  `TestNewRegistryValidation` — all PASS.
- **ADR-0002 conventions**: no `any` in the exported `Capability`/`Registry`/`PrincipalObject` API (the
  closed interface is how `any` is avoided in `Bind`); `ctx`-first on `Resource` and `PrincipalSource`;
  `api/fault` throughout; no `panic` (the static `defaultRegistry` discards its always-nil error with
  `_` and a justified `//nolint:gochecknoglobals`); `auth.Authorizer` port, routing, default-deny, and
  binding-as-grant semantics untouched.
- **Composition root** (funcd.go:~369) builds the registry via `NewRegistry(...)` with the three
  capabilities + two sources and wires the composite `EntityProvider` — errors wrapped.
- **Deps**: none added (`go mod verify` green).

## Definition of Done

8 / 8 ADR Review-checklist items hold (five suites unchanged; no per-capability branch; dispatch-by-ok +
assembled vocabulary + union built-ins; new capability registers without editing assembly; `NewRegistry`
validation; port/semantics unchanged; typed surface + ctx-first; every scenario a named passing test).
Generic DoD also met (build/lint/test/mod-verify green, real behavior no stubs, tree matches surface,
tracking correct). Misses: none. The two contract completions are `adr`-attributed and faithful — not DoD
misses.

## Model scorecard

Recorded: claude-fable-5 on ADR-0116 (implementation) → pass, 0/0/0, 0 model-attributed,
DoD 8/8. See docs/reviews/model-scorecard.md.

## Recommendation

Sign off. The refactor is behavior-preserving with the five suites as the unchanged regression guard, and
the abstraction is proven declarative by the dummy-capability test. The two contract completions are
mechanical `adr`-attributed corrections (a singular→plural entity-type field, and a Go-visibility fix for
the sealed interface's package) that preserve the decision — no superseding ADR needed; note them for any
future ADR that re-states the `Capability` contract. Stamp ADR-0116 `Reviewing → Implemented` and F84
`reviewing → implemented`. Infra/refactor ADR — no board card.
