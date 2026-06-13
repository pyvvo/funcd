# Review report — ADR-0002 implementation

- **ADR**: [ADR-0002 — Source-code conventions & architecture patterns](../adr/0002-source-code-conventions-and-patterns.md)
- **Phase**: implementation (ADR-0000 review gate #5)
- **Model reviewed**: `deepseek-v4-pro`
- **Date**: 2026-06-14
- **Reviewer**: `adr-impl-review` skill
- **Realizes**: [FEAT-0000/F25](../feat/0000-feat-v1.md)
- **Ledger entry**: [model-ledger.json](model-ledger.json) · rollup: [model-scorecard.md](model-scorecard.md)

## Verdict: changes-requested — 1 blocker, 1 major, 1 minor

The error kernel and conventions enforcement are genuinely well-built — `api/fault` is correctly
stdlib-only, the facade uses functional options returning a concrete `*Platform`, `just ci` is
green, and `golangci-lint` reports **0 issues**. But the ADR's **executable proof of its lint
rules is absent**: three of seven Scenarios (`lint-blocks-cross-feature-import`,
`lint-blocks-any-leak`, `no-mock-framework`) have no test, and the `.golangci.yml` under-enforces
the conventions ADR §4/§7 specify — notably the bare `any` keyword is not forbidden. "Green lint"
therefore partly reflects *missing rules*, not only clean code. Not a clean pass; the ADR stays
`Reviewing` and the `model` findings loop back to `adr-impl`.

## Verification run (evidence)

| Check | Command | Result |
|---|---|---|
| toolchain | `go version` | `go1.26.4 darwin/arm64` |
| packages | `go list ./...` | 6 pkgs: `api/fault`, `api/types/v1alpha1`, `internal/platform/clock`, `internal/store`, `internal/store/storecontract`, `pkg/funcd` |
| full gate | `just ci` | exit **0** — `tidy → fmt → lint → test → build → mod verify` all clean |
| lint | `golangci-lint run ./...` (via `just ci`) | **0 issues** (full set active) |
| tests | `go test ./...` | `api/fault` ok · `api/types/v1alpha1` ok · `storecontract` ok · `pkg/funcd` ok · `clock`/`store` `[no test files]` |
| `api/fault` imports | `awk` import block | `errors`, `fmt`, `net/http` — **stdlib-only** ✓ (ADR §3) |
| facade shape | read `pkg/funcd/{options,funcd}.go` | `type Option func(*config) error`; `New(opts ...Option) (*Platform, error)` returns concrete struct; `Run`/`Shutdown` ctx-first |
| scenarios | `grep "scenario:" **/*_test.go` | 2 real passing (`facade-missing-dep` ×2, `error-maps-to-problem`); 1 `t.Skip` (`driver-conformance-parity`); **3 absent** (lint-fixture scenarios); `context-cancellation` absent |
| tree | `find api internal pkg`; `find tests` | matches the ADR scaffold-plan file list; **`tests/` holds only `.gitkeep`** (no lint fixtures) |
| identity | `grep green-0-rabbit / /Users/ / /home/` | **no leaks** in `api/ internal/ pkg/ docs/conventions.md` |
| status | ADR header + feat row | ADR `Reviewing` ⟷ F25 `reviewing` ✓ (precondition met) |

## 🔴 Blocker

- **B1 — Three Scenarios have no test; the ADR-mandated known-bad lint fixtures are missing** ·
  attribution: **`model`**. ADR §scaffold-plan item 8 ([lines 357-359](../adr/0002-source-code-conventions-and-patterns.md)),
  DoD ([line 366](../adr/0002-source-code-conventions-and-patterns.md)) and Review checklist
  ([lines 384, 386](../adr/0002-source-code-conventions-and-patterns.md)) require known-bad
  fixtures under `tests/` whose `just lint` **fails**, covering `scenario: lint-blocks-cross-feature-import`,
  `scenario: lint-blocks-any-leak`, and `scenario: no-mock-framework`. `find tests/` → only
  `.gitkeep`; no fixtures, no tests. Three scenarios silently dropped and a DoD item unmet. The
  *enforcement rules* exist (so conventions hold today), but the executable negative-path proof the
  ADR makes a deliverable does not. **Achievable** (a `testdata/` fixture + a test asserting
  non-zero `golangci-lint` exit), so it's the model's gap, not the ADR's.

## 🟡 Major

- **M1 — `.golangci.yml` under-enforces the conventions ADR §4/§7 specifies** ·
  [.golangci.yml](../../.golangci.yml) · attribution: **`model`** (with one `adr`/sequencing
  sub-item). "0 issues" is partly the absence of rules, not only clean code:
  - **(a) bare `any` not forbidden** — `forbidigo` patterns cover `interface{}` and
    `map[string]any` but **not the `any` keyword**. ADR §4 ([line 187](../adr/0002-source-code-conventions-and-patterns.md))
    bans "`interface{}`/`any`" and `scenario: lint-blocks-any-leak` wants `any` flagged, so
    `func F(x any)` — the commonest form — passes lint today. `[model]`
  - **(b) generated-file exemption absent** — no `**/generated/**` / `*.gen.go` exclusion, which
    §7 ([lines 215-217](../adr/0002-source-code-conventions-and-patterns.md)) and checklist
    [line 379](../adr/0002-source-code-conventions-and-patterns.md) require. Inert today (no
    generated code until F02) but the checklist item is unmet. `[model]`
  - **(c) `log/slog`-only depguard rule absent** — §7 ([line 223](../adr/0002-source-code-conventions-and-patterns.md))
    lists "no logging library but `log/slog`"; no depguard rule denies other logging libs. Code is
    slog-only so nothing fires, but the *rule* is missing. `[model]`
  - **(d) feature-isolation depguard rule absent** (`internal/features/*` no sibling imports, §7
    [line 220](../adr/0002-source-code-conventions-and-patterns.md)) — but no `internal/features/*`
    exists yet, and the ADR's own workaround ([line 244](../adr/0002-source-code-conventions-and-patterns.md))
    says it is listed explicitly once features appear. **`adr`/sequencing — not scored.**

## Minor

- **m1 — `driver-conformance-parity` is `t.Skip`'d and `context-cancellation` has no test** ·
  [contract_test.go:14](../../internal/store/storecontract/contract_test.go) · attribution:
  **`adr`/sequencing**. ADR-0002 *itself* defers both to the store ADR / first real port
  ([lines 360, 364](../adr/0002-source-code-conventions-and-patterns.md)). Under the new no-skip
  generic DoD this is a deviation, but the ADR's specific bar authorizes it, so it does **not**
  count against the model. (Closes when the store ADR / F05 lands.)

## ✅ Verified correct (keep it)

- **Full gate green honestly where it counts**: `just ci` → exit 0; `golangci-lint` 0 issues with
  the full set — `govet staticcheck errcheck ineffassign misspell gochecknoglobals gochecknoinits
  forbidigo depguard errorlint`, **no `revive`** (ADR §7).
- **`api/fault` is genuinely stdlib-only** (`errors`, `fmt`, `net/http`) — the import-graph
  linchpin holds; `api/types/v1alpha1` imports only *up* into `api/fault`. depguard correctly
  encodes api-boundary, platform-leaf, e2e-boundary, and no-mock-framework.
- **Construction conventions exact**: functional options on the facade (`Option func(*config)
  error`, `WithLogger`), `New(opts ...Option) (*Platform, error)` returns the **concrete**
  `*Platform` (never an interface), `Run`/`Shutdown` take `ctx` first.
- **Real passing scenario + kernel coverage**: `facade-missing-dep` (×2) and `error-maps-to-problem`
  are real, un-skipped, passing; plus `KindOf`-through-wrapping, `Error`/`Unwrap`, problem headers,
  every `Validate()` (`NamespaceName`/`FunctionName`/`RevisionID`/`Phase`), `Phase.IsTerminal`,
  `TestAllConstructors`.
- **`internal/store/store.go`** is a clean **one-file** port skeleton, ctx-first
  (`Ready(ctx context.Context)`), explicitly labelled "real interface arrives with the store ADR" —
  correctly scoped, **not** scope creep.
- **Hygiene**: no identity leak; tree matches the scaffold-plan file list; ADR `Reviewing` ⟷ feat
  F25 `reviewing` (precondition correct).

## Definition of Done

**9 / 12** Review-checklist items hold (✓ 1,2,3,4,6,7,8,9,12). Misses: **#5** (`any`/generated
exemption — `model`), **#10** (full depguard rules + generated exemption + known-bad fixtures —
`model`, except feature-isolation = `adr`), **#11** (3 scenarios dropped — `model`).

## Recommendation

**Loop back to `adr-impl`** for the three `model` findings: (1) add the known-bad lint fixtures +
their scenario tests for cross-feature-import / any-leak / no-mock-framework; (2) add a `forbidigo`
pattern for the bare `any` keyword; (3) add the generated-file exemption and the `log/slog`-only
depguard rule to `.golangci.yml`. Leave the **deferred** items as-is (driver-conformance and
context-cancellation are ADR-sanctioned to land with the store ADR; the feature-isolation depguard
rule waits for the first feature). No superseding ADR needed — these are implementation gaps, not
decision defects. Re-run gate #5 after the fixes; the ADR stays `Reviewing` until it passes.
