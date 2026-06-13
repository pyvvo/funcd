# Scaffold conventions (distilled from ADR-0001 + ADR-0002)

The authoritative source is [ADR-0002](../../../../docs/adr/0002-source-code-conventions-and-patterns.md);
read it. This is the checklist + the stub rules that keep a *no-logic* scaffold passing `just lint`.
If this file and the ADR ever disagree, the ADR wins.

## The import graph (depguard — getting this wrong fails the build)

```
api/**            → stdlib only. Imports NOTHING from internal/** or pkg/**.
                    api/fault and api/types are stdlib-only leaves, imported "up" by everyone.
internal/platform → leaf kernel: imports no other internal/**; may import api/**.
internal/features/* → never import a sibling feature (they talk via the bus).
pkg/**            → may import api/** and internal/** (it is the composition surface).
tests/e2e/**      → imports only pkg/** + api/**.
everywhere        → logging is log/slog only; NO mock framework (gomock/testify-mock) anywhere.
```

Direction to remember: **everything imports *up* into `api/`; nothing imports *down* out of it.**

## Code-shape rules

- **Construction**: functional options (`New(opts ...Option)`, `WithX`) only on the public facade
  `pkg/funcd` and on driver constructors meant for selection. Internal components take an explicit
  `Deps`/`Config` struct: `New(d Deps) (*T, error)`. No `With*` inside `internal/`.
- **Return concrete, accept interfaces** — except port-driver constructors, which return the port
  interface so the facade can swap them.
- **Ports & drivers**: the port interface + shared types + contract suite live in the port package
  (`internal/store`, `internal/blob`). Each driver is **one file in its own subpackage**
  (`internal/blob/gocloud/gocloud.go`) — the subpackage exists only to keep the driver's third-party
  deps out of the port package; it is never licence to fan out into many files.
- **Errors**: one taxonomy in `api/fault` (package `fault`). Construct with `fault.NotFoundf(...)`
  etc.; recover with `fault.KindOf(err)`; wrap with `fault.Wrapf(err, kind, op, …)` or `%w`. Inner
  layers wrap-and-return and do NOT log. Never define a second error-kind enum.
- **Typed surface**: typed enums (`type Phase string` + `Validate()`/`String()`), typed IDs/names
  (`NamespaceName`, `FunctionName`, …). No `interface{}`/`any`/`map[string]any` in hand-written
  exported or port signatures — use a typed struct or `json.RawMessage`/`[]byte` at genuine
  pass-through points.
- **Context-first**: `ctx context.Context` is the first param of every blocking/IO method; never
  store a ctx in a struct.
- **No package-level mutable state**: no singletons, no `init()` side effects, no global loggers.
  Allowlisted globals: sentinel errors, enum constants, `regexp.MustCompile`.
- **Naming**: short lowercase package names, the name is part of the API (`store.Store`, not
  `store.StoreInterface`). No `util`/`common`/`helpers`.
- **One file until it outgrows one.** Don't pre-split into file-per-type. Split by responsibility
  only when a file is genuinely large.

## Not-implemented stub rules (so a no-logic scaffold still lints + builds)

These exist because the linters the same project enforces will reject the obvious shortcuts:

- **Never `panic("not implemented")`** — `forbidigo` bans `panic` outside `package main`.
- **Error-returning func** → `return <zeroes>, errors.New("not implemented: ADR-NNNN")`
  (stdlib `errors` is fine; import only what the stub uses or the build fails on unused imports).
- **Value-only func** that the scaffold needs to compile → return the zero value; if that would make
  a linter complain about an always-same return, prefer giving the method an `error` return in the
  port (most port methods have one anyway).
- **`Validate()` stubs** → `return nil` is acceptable for the scaffold (the real rule lands at
  implementation; the scenario test that checks validation is skipped for now).
- **Test bodies** → first line `t.Skip("scaffold ADR-NNNN — scenario: <name>")`. The test compiles,
  references the real signatures (so it breaks loudly if the contract drifts), and is reported skipped.
- **Avoid unexported funcs/types nothing references** — `staticcheck`'s `unused` check fails the
  build on dead unexported code. Scaffold the exported API + skipped tests; add unexported helpers
  only when something calls them.
- **`fmt.Print*` is banned** (forbidigo) — scaffolds don't print.

## go.mod

- Library deps: `go get <module>@<version>` (pin; the project commits `go.sum`).
- Codegen/lint tools: `go get -tool <module>@<version>` (Go 1.24 `tool` directive — ADR-0001).
- Record every resolved version in the scaffold report. Never add a dep the ADR didn't sanction;
  every dep must be Apache-2.0/MIT-compatible (the ADR checked this at acceptance).

## Pre-handoff checklist (run before declaring done)

- [ ] Every file in the ADR's Scaffold plan exists; nothing extra invented.
- [ ] `just build` passes (or `go build ./...`).
- [ ] `just lint` passes — depguard import graph, forbidigo (no `any` in APIs, no `panic`, no
      `fmt.Print*`), errorlint, gochecknoglobals/inits all clean. Known-bad fixtures still fail.
- [ ] `just test` / `go test ./...` passes; every Scenario test is present, named, and **skipped**.
- [ ] No business logic: bodies are not-implemented stubs or skipped tests.
- [ ] Code shapes match ADR-0002 (options vs deps-struct, one-file drivers, `api/fault`, typed
      surface, ctx-first, no globals, slog-only).
- [ ] The ADR's own *Review checklist* items that apply at scaffold time are satisfied.
- [ ] No local username/paths leaked; module path is `github.com/green-0-rabbit/funcd`.
- [ ] The realizing `docs/feat/` row is set to `scaffolded`.
