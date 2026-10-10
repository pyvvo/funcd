# Implementation conventions (distilled from ADR-0001 + ADR-0002)

The authoritative source is [ADR-0002](../../../../docs/adr/0002-source-code-conventions-and-patterns.md);
read it. This is the checklist + the code-shape rules every implementation must satisfy to pass
`just ci`. If this file and the ADR ever disagree, the ADR wins.

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

## Lint-clean implementation rules (the linters reject the obvious shortcuts)

These constraints hold for real code just as they did for skeletons — the same linters enforce them:

- **Never `panic`** outside `package main` — `forbidigo` bans it. Return a typed `api/fault` error
  instead; surface unexpected states as errors, not panics.
- **Error-returning func** → implement the behavior and return a real `api/fault` error on failure
  (`fault.NotFoundf(...)`, `fault.Invalidf(...)`, `fault.Wrapf(...)`), never a bare
  `errors.New("not implemented")` in the shipped path.
- **`Validate()`** → implement the real rule the ADR specifies (e.g. the DNS-label check), and the
  Scenario test that exercises it runs and passes — not skipped.
- **Test bodies** → exercise the real behavior and assert the outcome; the contract suite carries
  real assertions every driver runs. Name each test after its `scenario: <name>` for traceability.
- **e2e scenario tests stay fast** (CLAUDE.md, Known pitfall 5): `t.Parallel()`, short pacing values that keep the
  rule the ADR states (its durations are the rule, not the test's length), `require.Eventually` instead of fixed
  sleeps. Never raise `go test -timeout`; report the new scenarios' `--- PASS` times.
- **Avoid unexported funcs/types nothing references** — `staticcheck`'s `unused` check fails the
  build on dead unexported code. Don't leave helpers behind that no live path calls.
- **`fmt.Print*` is banned** (forbidigo) — log through `log/slog`, never print.

## go.mod

- Library deps: `go get <module>@<version>` (pin; the project commits `go.sum`).
- Codegen/lint tools: `go get -tool <module>@<version>` (Go 1.24 `tool` directive — ADR-0001).
- Record every resolved version in the implementation report. Never add a dep the ADR didn't
  sanction; every dep must be Apache-2.0/MIT-compatible (the ADR checked this at acceptance).

## Pre-handoff checklist (run before declaring done)

- [ ] Every file in the ADR's Implementation plan exists; nothing extra invented.
- [ ] `just build` passes (or `go build ./...`).
- [ ] `just lint` passes — depguard import graph, forbidigo (no `any` in APIs, no `panic`, no
      `fmt.Print*`), errorlint, gochecknoglobals/inits all clean. Known-bad fixtures still fail.
- [ ] `just test` / `go test ./...` passes; every Scenario test is present, named, and **passing**
      (none skipped); the contract suite runs against every driver.
- [ ] `just ci` exits 0 end-to-end.
- [ ] Real behavior implemented — no `not implemented` placeholders left in the shipped path.
- [ ] Code shapes match ADR-0002 (options vs deps-struct, one-file drivers, `api/fault`, typed
      surface, ctx-first, no globals, slog-only).
- [ ] The ADR's own *Review checklist* items are satisfied.
- [ ] No local username/paths leaked; module path is `github.com/pyvvo/funcd`.
- [ ] The ADR is set to `Reviewing` and the realizing `docs/feat/` row is set to `reviewing`.
