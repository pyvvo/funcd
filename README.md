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

0. **Scope the version** — `docs/feat/` lists what a version must contain, high level.
1. **Brainstorm** a topic from that list (component, feature, cross-cutting concern).
2. **ADR** captures the decision and its contracts; mark it `Proposed` when ready.
3. **Judge gate**: the `adr-judge` skill reviews the ADR *itself* against the blueprint,
   feat row, and related ADRs — flags inconsistency, bias, and scope creep, names the
   strengths to keep; the human weighs the verdict and accepts. The feat row advances.
4. **Scaffold** from the ADR + blueprint: interfaces, API facades, dependencies, and
   test skeletons (one e2e skeleton per ADR scenario) — no logic.
5. **Review gate**: a high-capability model/reviewer validates the scaffold against the ADR.
6. **Implement** the feature; make the test skeletons pass.
7. **Validate**: full test suite green — every ADR scenario now passes.
8. **LLM judge** (optional): an independent model audits code + tests against the ADR.

## Status

Pre-scaffold — blueprint + ADR phase.


## Useful links

- [Garage Standalone: Your Lightweight S3-Compatible Object Storage Journey](https://medium.com/@kryukz/garage-standalone-your-lightweight-s3-compatible-object-storage-journey-5073bd51b566)

## License

TBD.
