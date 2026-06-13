> **This is an index, not a second source of truth.**
> The binding source is [ADR-0002](adr/0002-source-code-conventions-and-patterns.md).

# Code conventions (funcd)

## Construction

- **Public facade** (`pkg/funcd`): functional options — `func New(opts ...Option) (*Platform, error)`
- **Internal components**: explicit deps struct — `func New(d Deps) (*T, error)`
- **Driver constructors**: return the port interface (so the facade can swap them)

## Ports & drivers

- A **port** is an interface in its own dependency-light package (e.g. `internal/store`)
- A **driver** is **one file in its own subpackage** (e.g. `internal/store/memory/memory.go`)
- The subpackage exists only for dependency isolation, not for file fan-out
- The port package owns the **contract suite** (`<port>contract`)

## Errors — `api/fault`

- One error taxonomy: `api/fault` (package `fault`, stdlib-only)
- Build: `fault.NotFoundf(...)`, `fault.Invalidf(...)`, `fault.Wrapf(err, kind, op, ...)`\n- Recover: `fault.KindOf(err) Kind`
- Inner layers wrap-and-return; the API edge logs once + maps to RFC 9457 problem+json

## Typed surface

- Typed enums (`type Phase string` + `Validate()`/`String()`)
- Typed IDs (`NamespaceName`, `FunctionName`, `RevisionID`)
- No `interface{}`/`any`/`map[string]any` in hand-written exported or port signatures

## Context & globals

- `ctx context.Context` first on every blocking/IO method; never store ctx in a struct
- No package-level mutable globals or `init()` side effects

## Logging

- `log/slog` only — no other logging API
- Each component receives a named child logger via its `Deps` struct

## Testing

- No mock frameworks — in-memory drivers + contract suites per port
- One named test per ADR Scenario (`TestScenario_<Name>`), running and passing

## Import graph (depguard-enforced)

```
api/**            → stdlib only; imports nothing from internal/** or pkg/**
internal/platform → leaf kernel; imports no other internal/**
pkg/**            → may import api/** and internal/**
tests/e2e/**      → imports only pkg/** + api/**
```
