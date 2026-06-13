# Definition of Done — the bar a review checks against

A review measures the work against a *bar*. There are two layers, and both must hold:

1. **The ADR's own bar** — every funcd ADR carries a **Definition of done** and a **Review
   checklist** section (per ADR-0000). Those are the *specific*, authoritative items for this ADR;
   read them first and check each. `--dod-total` in the scorecard = the count of those items;
   `--dod-passed` = how many actually hold (with evidence).
2. **The generic DoD below** — what *any* ADR's implementation must satisfy even where the ADR was
   silent. Use it to catch gaps the ADR's own checklist forgot.

A finding is the *gap* between the work and this bar. Tie each to evidence (a captured command +
exit code, or `file:line`) — never "looks fine".

## Generic DoD — the implement gate (ADR-0000 gate #5)

The implementation turns the Accepted ADR into working code with passing scenario tests.

- [ ] **Full suite green**: `just ci` exits 0 — fmt-check, lint, unit, contract, and (where the
      harness exists) e2e. (Empty-module trap: `golangci-lint`/`go vet`/`go test` error on a module
      with *zero* `.go` files — a real implementation has Go files so this should not bite; if a
      legacy bootstrap ADR's DoD is unsatisfiable as written, that's **`adr`-attributed**, not the
      model's fault. See review-method.md.)
- [ ] **Every ADR Scenario passes**: one named test per **Scenario**, **un-skipped and passing**, at
      the right level (contract/unit pre-harness, e2e once the harness exists). None *weakened or
      deleted* to go green — silently relaxing an assertion is a Blocker. No scenario silently
      dropped; a Scenario that is inherently e2e before the harness exists has its test deferred and
      the deferral noted (attributed to sequencing, not the model).
- [ ] **Real behaviour, no stubs in the shipped path**: bodies do the work the Contracts describe; no
      `errors.New("not implemented…")` placeholders left live; `panic`/`fmt.Print*` banned by
      forbidigo outside `main`.
- [ ] **Contracts honoured**: the implemented interfaces/types match the ADR's Contracts exactly
      (signatures, error kinds, resource shapes).
- [ ] **Contract suites pass against every driver** of a port (the in-memory driver proves
      equivalent to the real one).
- [ ] **Tree matches the ADR's *Repository surface***: nothing missing, nothing unexplained-extra.
- [ ] **Conventions (ADR-0002)** hold: no `any` in exported/port APIs, import graph respected,
      `api/fault` for errors, one-file drivers, functional options on the facade / deps-structs
      internally, ctx-first, no globals, `log/slog` only.
- [ ] **Deps**: only those the ADR sanctioned; `go.mod`/`go.sum` tidy; resolved versions recorded.
- [ ] **No scope creep**: the work implements *this* ADR, not a neighbouring decision (that's a new
      ADR).
- [ ] **Hygiene + tracking**: no local username/paths leaked; module path
      `github.com/green-0-rabbit/funcd`; the realized `docs/feat/` row at `reviewing` and the ADR at
      `Reviewing` with its substance unchanged (the builder's only permitted ADR edit is the
      `Accepted → Reviewing` status bump — any other diff is a Blocker). On a review **pass**, the
      review gate then advances both to `implemented` / `Implemented`.

## How "DoD pass rate" feeds the scorecard

Count the union of the ADR's own Review-checklist/DoD items plus the applicable generic items above
→ `--dod-total`. Count how many genuinely hold (with evidence) → `--dod-passed`. The scorecard
reports the average DoD pass rate per model. A low DoD rate driven by **`adr`-attributed** gaps is a
signal about the *ADR*, not the model — keep the attribution honest (review-method.md §attribution).
