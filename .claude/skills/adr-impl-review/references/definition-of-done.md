# Definition of Done — the bar a review checks against

A review measures the work against a *bar*. There are two layers, and both must hold:

1. **The ADR's own bar** — every funcd ADR carries a **Definition of done** and a **Review
   checklist** section (per ADR-0000). Those are the *specific*, authoritative items for this ADR;
   read them first and check each. `--dod-total` in the scorecard = the count of those items;
   `--dod-passed` = how many actually hold (with evidence).
2. **The generic phase DoD below** — what *any* ADR's work must satisfy even where the ADR was
   silent. Use it to catch gaps the ADR's own checklist forgot.

A finding is the *gap* between the work and this bar. Tie each to evidence (a captured command +
exit code, or `file:line`) — never "looks fine".

## Generic DoD — Scaffold phase (ADR-0000 gate #5)

The scaffold is the bare-minimum compiling skeleton: declarations, no behaviour.

- [ ] **Compiles**: `just build` (or `go build ./...`) exits 0.
- [ ] **Lints clean**: `just lint` exits 0 against the linter set the ADR/ADR-0002 mandate. (Note
      the empty-module trap: `golangci-lint`/`go vet`/`go test` error on a module with *zero* `.go`
      files — if that bites, it's usually an **`adr`-attributed** gap in the ADR's DoD, not the
      model's fault. See review-method.md.)
- [ ] **Tests present but skipped**: one named, skipped test per ADR **Scenario** (at the right
      level — contract/unit pre-harness, e2e once the harness exists). `go test ./...` is green with
      those reported `SKIP`. No scenario silently dropped.
- [ ] **No business logic**: bodies are not-implemented stubs (`errors.New("not implemented: ADR-…")`,
      never `panic` — forbidigo bans it), `t.Skip(...)` tests. Real logic at this phase is a defect.
- [ ] **Tree matches the ADR's *Repository surface***: nothing missing, nothing unexplained-extra.
- [ ] **Conventions (ADR-0002)** hold: no `any` in exported/port APIs, import graph respected,
      `api/fault` for errors, one-file drivers, functional options on the facade / deps-structs
      internally, ctx-first, no globals, `log/slog` only.
- [ ] **Deps**: only those the ADR sanctioned; `go.mod`/`go.sum` tidy; versions recorded.
- [ ] **Hygiene**: no local username/paths leaked; module path `github.com/green-0-rabbit/funcd`.
- [ ] **Tracking**: the realized `docs/feat/` row advanced to `scaffolded`; the Accepted **ADR file
      is unchanged** (a diff on an Accepted ADR is itself a Blocker — they're immutable).

## Generic DoD — Implementation phase (ADR-0000 gate #8)

The implementation makes the skeletons real.

- [ ] **Full suite green**: `just ci` exits 0 — fmt-check, lint, unit, contract, and (where the
      harness exists) e2e.
- [ ] **Every ADR Scenario passes**: each scenario test is now **un-skipped and passing** — and
      none was *weakened or deleted* vs the scaffold. Compare against the scaffold's skeletons:
      silently relaxing an assertion to go green is a Blocker, not a pass.
- [ ] **Contracts honoured**: the implemented interfaces/types match the ADR's Contracts exactly
      (signatures, error kinds, resource shapes).
- [ ] **Contract suites pass against every driver** of a port (the in-memory driver proves
      equivalent to the real one).
- [ ] **Conventions (ADR-0002)** still hold across the new code.
- [ ] **Hygiene + tracking**: no identity leak; feat row advanced to `implemented`; the Accepted
      ADR file unchanged.
- [ ] **No scope creep**: the work implements *this* ADR, not a neighbouring decision (that's a new
      ADR).

## How "DoD pass rate" feeds the scorecard

Count the union of the ADR's own Review-checklist/DoD items plus the applicable generic items above
→ `--dod-total`. Count how many genuinely hold (with evidence) → `--dod-passed`. The scorecard
reports the average DoD pass rate per model. A low DoD rate driven by **`adr`-attributed** gaps is a
signal about the *ADR*, not the model — keep the attribution honest (review-method.md §attribution).
