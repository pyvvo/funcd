# funcd

A lightweight, modular, single-binary serverless platform for Linux — faasd-like,
Kubernetes-inspired internals, designed for the agent era.

- **[blueprint.md](blueprint.md)** — the platform blueprint: purpose, components,
  architecture, resource model, repository structure. The single architectural reference.
- **`docs/adr/`** — Architecture Decision Records. Every concrete decision (project setup,
  ports & drivers, gateway mechanics, …) is captured as an ADR: need, constraints,
  alternatives, final solution, and the contracts (interfaces, resource definitions,
  inputs/outputs) an implementer needs. Features are scaffolded and implemented **from
  ADRs**, gradually — no big-bang implementation.

## Workflow

1. **Brainstorm** a topic (component, feature, cross-cutting concern).
2. **ADR** captures the decision and its contracts.
3. **Scaffold** from the ADR + blueprint: interfaces, API facades, dependencies — no logic.
4. **Review gate**: a high-capability model/reviewer validates the scaffold against the ADR.
5. **Implement** the feature and its tests.

## Status

Pre-scaffold — blueprint + ADR phase.

## License

TBD.
