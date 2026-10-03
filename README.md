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

## How funcd is built

funcd is written mostly by AI agents under a fixed method: a person decides, and agents draft and judge each
design (an ADR), build it with its scenario tests, review it by running it, attack the shipped system in chaos
campaigns, fix what breaks test-first, and audit whether the fixes made the code worse. Each step is a skill in
`.claude/skills/`, and every review is recorded per model in `docs/reviews/`.

**[docs/method/README.md](docs/method/README.md)** describes the whole method. [ADR-0000](docs/adr/0000-adr-process.md)
holds the design-process rules and [.claude/CLAUDE.md](.claude/CLAUDE.md) the working agreement.

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
