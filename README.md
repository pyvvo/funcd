# funcd

A lightweight, modular, single-binary serverless platform for Linux — faasd-like,
Kubernetes-inspired internals, designed for the agent era.

- **[blueprint.md](blueprint.md)** — the platform blueprint: purpose, components,
  architecture, resource model, repository structure. The single architectural reference.
- **`docs/feat/`** — feature-version documents: the initial need and the high-level
  feature/component list per version (V1, V2, …). What and why — never how.
- **`docs/adr/`** — Architecture Decision Records. Every concrete decision (project setup,
  ports & drivers, gateway mechanics, …) is captured as an ADR: need, constraints,
  alternatives, final solution, and the contracts (interfaces, resource definitions,
  inputs/outputs) an implementer needs. Features are scaffolded and implemented **from
  ADRs**, gradually — no big-bang implementation.

## Workflow

Each gate has a skill (`.claude/skills/`): **plan** → **decide** → **judge** → **build** →
**review**.

0. **Scope & sequence** — `docs/feat/` lists what a version must contain; the
   **`roadmap-planner`** skill sequences the ADRs into a computed delivery plan
   (`docs/roadmap/`: build waves, critical path).
1. **Brainstorm** a topic from that list with the **`adr`** skill (component, feature,
   cross-cutting concern).
2. **ADR** captures the decision and its contracts; mark it `Proposed` when ready.
3. **Judge gate** — the **`adr-judge`** skill reviews the ADR *document* against the
   blueprint, feat row, and related ADRs (inconsistency, bias, scope creep, strengths to
   keep); the human weighs the verdict and accepts. The feat row advances.
4. **Scaffold** — the **`adr-scaffold`** skill turns the ADR into a compiling skeleton:
   interfaces, facades, deps, one skipped test per scenario — no logic.
5. **Review gate** — the **`adr-impl-review`** skill *runs* the verification (build/lint/test,
   tree diff) against the ADR + Definition of Done, gives a severity-tiered verdict, and
   **records a per-model quality scorecard** (`docs/reviews/`).
6. **Implement** the feature; make the test skeletons pass.
7. **Validate**: full test suite green — every ADR scenario now passes.
8. **Implementation review** (recommended) — `adr-impl-review` again audits code + tests against
   the ADR and updates the model scorecard.

## Status

Scaffolded — ADR-0001 (project setup, file structure, Nix dev environment).
Blueprint + ADR phase, repository skeleton in place, CI bootstrapped.

## License

[Apache-2.0](LICENSE) — Copyright 2026 The funcd Authors.
