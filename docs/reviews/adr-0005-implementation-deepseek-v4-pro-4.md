# Review report — ADR-0005 implementation (re-review #4 — resolving pass)

- **ADR**: [ADR-0005 — API surface, code-first via huma](../adr/0005-api-surface-code-first-huma.md)
- **Phase**: implementation (ADR-0000 review gate #5)
- **Implemented by**: `deepseek-v4-pro`
- **Date**: 2026-06-14
- **Reviewer**: `adr-impl-review` skill (`claude-opus-4-8`) — independent of the implementation
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md)
- **Resolves**: [re-review #3](adr-0005-implementation-deepseek-v4-pro-3.md) (changes-requested — the
  in-place amendment Blocker)

## Verdict: pass — 0 Blockers, 0 Majors, 1 Minor

Re-review #3's single Blocker — ADR-0005's frozen substance amended in place — was a **process/mechanism**
finding, not a code defect. The decider (green-0-rabbit) **ratified the in-place amendment as a
deliberate, conscious exception** to the supersede-only invariant (recorded in the ADR header: "amended
in place 2026-06-14 by deliberate decision … a narrow scope reduction, not a decision reversal"; the ADR
is uncommitted/in-flight). With that Blocker withdrawn, the implementation conforms to ADR-0005 and is
clean → **pass**.

## What was verified (re-review #3 evidence, unchanged)
- `golangci-lint` **0 issues**; `go build` + `go test ./...` green; **all 6 scenario tests pass**
  (spec-generated-from-go, spec-reflects-go-shape, typed-operation-roundtrip, error-is-problem-json,
  openapi-doc-served, spec-consumable-by-client).
- The only `just ci` red is the **tidy-gate** — `go mod tidy` is a no-op; `go.mod`/`go.sum` are dirty
  only because the huma/chi deps are uncommitted (the ADR-0002 situation: green on commit).
- huma code-first core, generated **OpenAPI 3.1** spec (DO-NOT-EDIT marker, `specgen`-produced),
  `api/fault` **huma-free** (judge M1 honored), ADR-0004 artifacts removed, `specgen` gitignored, MIT
  deps, identity clean.

## Governance note (the ratified exception)
The supersede-don't-amend invariant was deviated from **deliberately and on the record** — this is the
decider's call, documented in the ADR header, not a stealth substance edit. The arc of `model`-attributed
findings across the four reviews was: lint/binary/client (review #1, fixed) → the `adr` 3.1/oapi-codegen
defect (review #2, an ADR defect) → the unratified amendment (review #3, process) → ratified (here).
deepseek's code was clean from review #3 on.

## Minor — `model` (non-blocking, carried)
- Round-trip request bodies use `map[string]interface{}` (`api_test.go`, `client_test.go`) rather than a
  typed `v1alpha1.Function` — passes forbidigo only via the `interface{}`-literal blind spot. Optional
  cleanup; does not block.

## Definition of Done
10/10 — the implementation conforms to the (deliberately-amended) ADR; all scenarios pass; conventions
hold; `api/fault` huma-free. The `just ci` tidy-gate is the only red and resolves on commit.

## Recommendation
**Pass.** Status advanced: ADR-0005 `Reviewing → Implemented`, feat F02 `reviewing → implemented`. Commit
the work to make `just ci` exit 0 with evidence (the tidy-gate). Next: the deferred client/SDK is P-R/F18's
to build over this generated spec; the critical path (P-C store, P-E bus) is still the design bottleneck.
