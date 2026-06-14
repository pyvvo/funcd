# Review report — ADR-0005 implementation

- **ADR**: [ADR-0005 — API surface, code-first via huma](../adr/0005-api-surface-code-first-huma.md)
- **Phase**: implementation (ADR-0000 review gate #5)
- **Implemented by**: `deepseek-v4-pro` (assumed — the implement model for this session; not explicitly
  given at review time; re-key if wrong)
- **Date**: 2026-06-14
- **Reviewer**: `adr-impl-review` skill (`claude-opus-4-8`) — authored/judged the ADR but **did not
  write** the `internal/controlplane` code, so this scores the implementer independently
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md)

## Verdict: changes-requested — 1 Blocker, 1 Major, 2 Minor

The code compiles, 5/6 scenarios pass, and the hard parts are right (the spec is genuinely generated
OpenAPI 3.1, `api/fault` stays huma-free). But **`just ci` fails on 11 real lint issues** and a
**scoped deliverable (the generated client + its scenario) is missing** — so the DoD is not met.
Everything is `model`-attributed; the ADR contract is sound (no `adr`/`env` findings).

## Verification run (captured evidence)

| Check | Command | Result |
|---|---|---|
| compiles | `go build ./...` | exit 0 |
| tests | `go test ./internal/controlplane/` | **ok** — 5 tests pass |
| full gate | `just ci` | **exit 1** — golangci-lint: **11 issues** (forbidigo 8, unused 1, staticcheck 2) |
| M1 (api/fault huma-free) | `grep` imports of `api/fault/*.go` | `errors`, `fmt`, `net/http` only — **huma-free ✓** |
| spec | `head api/openapi/funcd.v1alpha1.yaml` | `openapi: 3.1.0`, `# Generated … DO NOT EDIT`, written by `specgen` |
| client | `find api/openapi/generated` | **absent** |
| hygiene | identity grep; status | clean; ADR `Reviewing` ⟷ F02 `reviewing`; ADR-0004 `Superseded` |

## 🔴 Blocker — `just ci` fails on 11 lint issues · `model`
golangci-lint output:
- **forbidigo `any` ×8** — `controlplane.go:165` `func jsonMarshal(w io.Writer, v any)` + `:170`
  `jsonUnmarshal(data []byte, v any)`; `api_test.go` `map[string]any` ×6. Violates the **ADR-0002 §4
  no-`any` rule ADR-0005 inherits**. The round-trip test builds a `map[string]any` instead of a typed
  `v1alpha1.Function` (it should use the canonical type — that's the point of the test); the two JSON
  wrappers are the heuristic's gray area but are likely unnecessary (huma marshals) — drop them or use
  `json.RawMessage`.
- **unused ×1** — `routes.go:141 type replaceResourceGroupInput is unused` (dead code).
- **staticcheck ×2** — `api_test.go` QF1008 (remove the embedded `ObjectMeta` selector).

The DoD "`just ci` exits 0" fails. This blocks the gate.

## 🟡 Major — `client-generated-from-spec` deliverable + scenario missing · `model`
No `api/openapi/generated/`, no oapi-codegen client config, and **only 5 of 6 scenario tests** (the
ADR's own date note says "5 scenario tests"). But ADR-0005 scopes the **generated Go client IN**
(Scope §In, Decision §5, scenario `client-generated-from-spec`). Implement it (oapi-codegen client-only
on the committed spec + the round-trip test), or flag it as a deliberate deferral with rationale.

## Minor
- **Stray `specgen` binary** in the repo root — a Mach-O arm64 executable, **not gitignored** (would be
  committed on `git add -A`). Gitignore it or build to a temp path. `model`/hygiene.
- **Test naming** — `TestErrorIsProblemJSON` etc. rather than the `TestScenario_<name>` convention
  ADR-0002/0003 used. Tests clearly map to scenarios, so this is grep-traceability only. `model`, minor.

## ✅ Verified correct (keep it)
- **`api/fault` stays huma-free** — the judge's M1 fix is honored (the bridge lives in
  `internal/controlplane`, `api/fault` imports only stdlib). **Do not regress this.**
- **The spec is genuinely generated** — `openapi: 3.1.0`, `DO NOT EDIT` marker, produced by `specgen`
  wired into `just ci` (regenerated before lint). The supersession's whole point, working; `just
  generate` is reproducible and `TestSpecReflectsGoShape` proves derivation.
- **5/6 scenarios pass**: spec-generated-from-go, spec-reflects-go-shape, typed-operation-roundtrip,
  error-is-problem-json, openapi-doc-served. `go build` + `go test` green.
- The **obsolete ADR-0004 hand-authored files are removed** (api/openapi now holds only the generated
  spec); huma (MIT) + chi (MIT) added; identity clean; status bookkeeping correct (ADR `Reviewing`,
  F02 `reviewing`, ADR-0004 `Superseded`).

## Definition of Done
~6/10 ADR review-checklist items hold (huma framework, generated 3.1 spec, no-hand-spec, problem+json
via huma-free `api/fault`, validation split, MIT deps). Misses — **all `model`**: `just ci` green
(lint), the generated client + its scenario, the client round-trip. No `adr`/`env` findings.

## Recommendation
**changes-requested** — advance nothing (ADR stays `Reviewing`). Loop back to the builder
(`/adr-impl 0005`): (1) fix the lint — use a typed `v1alpha1.Function` in the round-trip test, drop the
`any` JSON wrappers, delete the unused type, clear the staticcheck nits; (2) generate the client +
add `client-generated-from-spec`; (3) gitignore the `specgen` binary. The ADR contract is sound — no
superseding ADR needed.
