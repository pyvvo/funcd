# Review — ADR-0048 (DTO validation reference) implementation

## Verdict: pass — 0 blockers, 0 majors  (ADR-0048 implementation, model: claude-opus-4-8)

The implementation realizes ADR-0048's normative validation sweep faithfully: both
mechanisms (huma schema at the edge → 422, admission `Validate()` for cross-field) are
wired end-to-end and verified by passing scenario tests; the "one constraint, one layer"
discipline is honored (presence stays the shape gate's job); the OpenAPI carries every
declared constraint and is not stale. All verification commands exit 0.

## Verification (captured exit codes)

| Check | Command | Exit |
|---|---|---|
| build | `go build ./...` | 0 |
| full suite | `go test -p 1 ./...` | 0 (38 `ok`, 0 `FAIL`) |
| race | `go test -race ./api/types/... ./internal/controlplane/...` | 0 |
| lint | `go tool golangci-lint run ./...` | 0 (`0 issues.`) |
| mod | `go mod verify` | 0 (`all modules verified`) |
| spec-not-stale | `go test ./internal/controlplane/ -run TestSpecGeneratedFromGo` | 0 (PASS, fresh `-count=1`) |

Scenario tests run fresh (`-count=1`, un-skipped, real assertions):
- `TestSchemaRejectsInvalidDTO` (internal/controlplane) → PASS — asserts 422 for bad name,
  `replicas:-1`, `replicas:16`, bad runtime, bad handler.
- `TestValidate_CrossFieldAndConditional` (api/types/v1alpha1) → PASS — Scaling
  minReplicas>maxReplicas; Service type↔sub-spec + binding; EventSource type↔timer + target.
- `TestScenarioShapeInvalidBlocksReady` (internal/function) → PASS — the shape gate still
  owns presence (ADR-0020), unbroken by the admission changes.

## ✅ Verified correct (keep it)

- **Both mechanisms wired end-to-end.** Schema constraints reach the OpenAPI: digest
  `^sha256:[a-f0-9]{64}$`, DNS-1123 pattern, `handlerPattern ^[A-Za-z_][A-Za-z0-9_.]*$`,
  `replicas maximum:15`, duration `maximum:86400000000000`, interval `minimum:100000000`
  (100ms), `number minimum:1`, and the closed enums (ServiceType, SecretType,
  EventSourceType, the 7-value Phase). The edge rejects bad bodies 422 before the handler
  (`TestSchemaRejectsInvalidDTO`).
- **One constraint, one layer.** `FunctionSpec.Validate()` (function.go:107-114) checks only
  the cross-field `minReplicas ≤ maxReplicas`; it does NOT re-check
  runtime/handler/artifact presence — that is the shape gate (ADR-0020), and the shape-gate
  test still passes. No `Validate()` rule duplicates a schema-enforced format.
- **No `uriPattern`.** `ArtifactRef.URI` (function.go:63) carries no pattern tag; the OpenAPI
  `uri` field is bare `type: string` — bare registry refs are accepted as the ADR requires.
- **Reusable fragments, single declaration.** `DNSLabel` const + `dnsLabel` regexp +
  `dnsLabelSchema()` + `enumSchema()` + `toAny()` all declared once in ids.go; every leaf
  `Schema()` references the shared fragment. The handler/digest patterns are single literals.
- **`any`/forbidigo discipline.** The only `any` is `toAny()` (ids.go:35-41), the huma
  `Schema.Enum []any` boundary, with a justified `//nolint:forbidigo`; lint reports `0 issues`.
- **Contracts honored.** The SchemaProvider surface matches the ADR's Contracts block
  verbatim (ObjectName/NamespaceName/ResourceGroupName/FunctionName/RuntimeName →
  `dnsLabelSchema()`; ServiceType/SecretType/EventSourceType/Phase → `enumSchema(...)`).
- **Mechanical ripples are clean.** `string(fn.Spec.Runtime)` at the three worker-spec sites
  (function.go) and `pooling.go` `KeyOf`, after `FunctionSpec.Runtime`/`RevisionSpec.Runtime`
  were retyped `string → RuntimeName`. No behavior change.
- **Fixture fixes preserve intent, weaken nothing.** eventing/services/pooling/roundtrip/
  server test fixtures were updated to satisfy the now-stricter rules (a function target on
  the HTTP EventSource; a valid blob service rather than a mismatched type; typed
  `RuntimeName`; a non-empty artifact URI). The dispatcher test still exercises "a type with
  no registered handler" — its assertion is intact.
- **`ids.go` "stdlib-only" note corrected** (package doc now states it imports huma; the
  depguard rule — api/** imports no internal/**/pkg/** — still holds; huma is third-party).
- **Deps**: `go.mod`/`go.sum` unchanged — huma `v2.38.0` was already present; no new module.
- **Hygiene**: identity grep over all 17 changed files + the 2 untracked files (validate_test.go,
  the ADR) → no local username / path / email leak. Module path `github.com/green-0-rabbit/funcd`.
- **ADR substance unchanged**: ADR-0048 at `Reviewing`; the Accepted body (Context/Scenarios/
  Decision/Contracts/tables) matches the implementation (the reconciliation notes were folded
  at acceptance, not during implementation).

## ADR table ⟷ code spot-check (5 rows)

| ADR row | Code | OpenAPI | Match |
|---|---|---|---|
| ObjectMeta name → DNS-1123, schema | `(ObjectName) Schema → dnsLabelSchema()` | `pattern ^[a-z0-9]…` | ✓ |
| FunctionSpec replicas → 0≤≤15, tag | `minimum:"0" maximum:"15"` | `maximum: 15` | ✓ |
| FunctionSpec handler → handlerPattern, tag | `pattern:"^[A-Za-z_][A-Za-z0-9_.]*$"` | `pattern ^[A-Za-z_]…` | ✓ |
| ArtifactRef uri → non-empty only, no pattern | no pattern tag | `uri: {type: string}` only | ✓ |
| TimerSpec interval → 100ms–24h, tag | `minimum:"100000000" maximum:"86400000000000"` | `minimum: 100000000`, `maximum: 86400000000000` | ✓ |

## Definition of Done

ADR Review-checklist (5 items) + applicable generic DoD — all hold:
1. Every schema/tag row in the OpenAPI; violating body 422 at the edge (`TestSchemaRejectsInvalidDTO`) ✓
2. Every Validate row enforced at admission (`TestValidate_CrossFieldAndConditional`) ✓
3. Each reusable fragment one Go declaration, leaves reference it ✓
4. `ids.go` note corrected; api/** depguard green (lint 0 issues) ✓
5. No Validate rule duplicates a schema constraint; suite green; no new module ✓
Generic: build/lint/test/race/mod all exit 0; scenarios un-skipped+passing; no stubs; conventions
held; tree matches surface (modifies existing api/types + adds validate_test.go — expected); hygiene clean.
**DoD: 10 / 10 hold.**

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0048 (implementation) → pass, 0/0/0, 0 model-attributed,
DoD 10/10. See docs/reviews/model-scorecard.md.

## Recommendation
Sign off. On this pass the gate stamps ADR-0048 `Reviewing → Implemented` (2026-06-16) and
links ADR-0048 from its realized feat row (F03; also extends F07) — those rows were already
`implemented` from ADR-0003/ADR-0018, and ADR-0048 is an extending decision appended as a
parenthetical (the same convention ADR-0047/ADR-0046 use on F05/F28). No code change needed.
