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

## The current workflow (ADR-0000 gates 0–5)

| # | Gate | Skill | Your model | Status moves it makes |
|---|---|---|---|---|
| 0 | Scope the version | `roadmap-planner` | — | (sequences ADRs; writes roadmap) |
| 1–2 | Brainstorm → Draft | [adr](.claude/skills/adr/SKILL.md) | Opus 4.8 MAX | ADR `Draft → Proposed`; feat `idea → adr` |
| 3 | **Judge gate** (the ADR *document*) | [adr-judge](.claude/skills/adr-judge/SKILL.md) | Opus 4.8 Extra | none — it only reports; **you** then accept |
| — | **Acceptance** (human call) | — | you | ADR `→ Accepted`; **blueprint + roadmap synced here**; feat `→ accepted` |
| 4 | **Implement** (ADR + blueprint → working code) | [adr-impl](.claude/skills/adr-impl/SKILL.md) | Sonnet 4.6 medium | ADR `Accepted → Reviewing`; feat `→ reviewing` |
| 5 | **Review gate** (the *code*) | [adr-impl-review](.claude/skills/adr-impl-review/SKILL.md) | Opus 4.8 Extra | **pass** → ADR `Reviewing → Implemented`, feat `→ implemented`; **else** → report, advances nothing |

**The loop** (exactly as you said): review `changes-requested`/`fail` → ADR stays `Reviewing` → back to gate 4 (`adr-impl`) to fix the `model`-attributed findings → re-review → repeat until the review gate passes and stamps `Implemented`. (`adr`-attributed findings don't loop here — they break out to a *new superseding ADR*.)


## Repositories

funcd is split across the [pyvvo](https://github.com/pyvvo) org
([ADR-0141](docs/adr/0141-repo-split-pyvvo-pinned-language-modules.md)):

| Repo | Holds |
|---|---|
| [pyvvo/funcd](https://github.com/pyvvo/funcd) | the Go platform: daemon, API, CLI, SDK, runtime images, providers, e2e tests, ADRs |
| [pyvvo/funcd-typescript](https://github.com/pyvvo/funcd-typescript) | the Node shim and the TypeScript examples; npm [`@funcd-dev/shim`](https://www.npmjs.com/package/@funcd-dev/shim) |
| [pyvvo/funcd-python](https://github.com/pyvvo/funcd-python) | the Python shim and the Python examples; PyPI [`funcd-shim`](https://pypi.org/project/funcd-shim/) |
| [pyvvo/funcd-functions](https://github.com/pyvvo/funcd-functions) | real functions deployed on the platform |

funcd pins the two language repos as Go modules in `go.mod`, embeds their shims, and runs their
committed examples in its e2e tests and Lima lanes. To take a new language release:

```bash
go get github.com/pyvvo/funcd-typescript@<tag>
```

To work on a shim and funcd together, clone the language repo next to funcd and add a local
`go.work`, which git ignores:

```bash
go work init . ../funcd-typescript
```

## Status

See [docs/PROJECT-SUMMARY.md](docs/PROJECT-SUMMARY.md) for what is built and the decision log.

## License

[Apache-2.0](LICENSE) — Copyright 2026 The funcd Authors.
