# ADR-0009 Implementation Review — Observability logger root (model: claude-opus-4-8)

**Verdict**: **pass** — Definition of Done met, zero Blockers/Majors. The implementation faithfully
realizes the ADR's Contracts; all six scenarios have named, un-skipped, passing tests; conventions
hold. One non-blocking Minor (untested `FormatText` branch).
**Reviewed against**: ADR-0009 Contracts/Scenarios/Review-checklist/DoD · ADR-0002 (§1/§3/§5/§6) ·
blueprint "Platform logging" · FEAT-0000/F17a.
**Date**: 2026-06-14

## Verification run (evidence)

| Check | Command | Result |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./internal/observability/...` | exit 0 |
| Lint (full tree) | `go tool golangci-lint run ./...` | **0 issues**, exit 0 |
| Tests (no cache) | `go test -count=1 -v ./internal/observability/...` | 6/6 PASS, exit 0 |
| Tree vs ADR surface | `git status --porcelain internal/observability/` | exactly `logger.go` + `logger_test.go` — nothing missing, nothing extra |
| Stubs/skips | `grep "not implemented\|t.Skip\|TODO\|panic("` | none |
| Dep promotion | `git diff go.mod` | `otel/trace v1.43.0` indirect→**direct**; no SDK/exporter added |
| Identity | identity grep (username / `/Users/` paths / email) | clean |

`just ci` fails **only** on the `git diff -- go.mod go.sum` commit-gate (the legitimate, uncommitted
dep promotion) — the documented commit-gate behavior, not an implementation defect; the four
substantive sub-checks (build/lint/test/mod-verify) all pass.

## 🔴 Blockers
None.

## 🟡 Major
None.

## Minor
- **`FormatText` branch untested** — `internal/observability/logger.go:88-92` selects
  `slog.NewTextHandler` when `Format == FormatText`, but every scenario test uses `FormatJSON`
  (the `level-and-format` scenario is JSON-specific). The text branch (the dev default) is
  exercised by no test. *Attribution: `model`.* Non-blocking — the ADR's Scenarios don't require a
  text-format case and the branch is a one-line stdlib handler swap. A one-line subtest asserting a
  text record renders `component=` would close it.

## ✅ Verified correct — keep it
- **All 9 Review-checklist items hold.** `logger.go` defines `Logger`/`Config`/`Format`(+`Validate`)/
  `NewLogger`/`Root`/`Component`/`SetLevel`/`Level` + unexported `traceHandler`; **no package-level
  global** (`gochecknoglobals` clean in the full-tree lint).
- **`traceHandler` implements all four `slog.Handler` methods** (`Enabled`/`Handle`/`WithAttrs`/
  `WithGroup`, `logger.go:123-143`) and adds `trace_id`/`span_id` **iff** the span context is valid,
  add-then-forward with no `Record` retention — exactly the Contract. `trace-correlation` test proves
  both the present-span and absent-span paths.
- **Runtime level switch is real**: `runtime-level-switch` constructs the child *before* `SetLevel`
  and proves the change takes effect on the existing child (shared `*slog.LevelVar`), both lowering
  and raising. `isolated-instances` proves two loggers share no global level state — the no-global
  guarantee verified behaviorally, not just by lint.
- **Typed error path**: `Format.Validate()` returns `fault.Invalidf(...)`; `invalid-format-rejected`
  asserts `fault.KindOf == fault.Invalid` and a nil `*Logger`. Matches ADR-0002 §3.
- **Scope discipline**: only the OTel **trace API** promoted to direct (Apache-2.0); no SDK,
  exporter, audit channel, or OTLP bridge leaked in from P-F2's scope.
- **Test hygiene**: black-box `observability_test` package; decodes into a typed `logLine` struct
  (no `any`/`map[string]any`, respecting forbidigo §4); `t.Parallel()` throughout; testify (no mock
  framework).

## Conventions spot-check
slog-only ✓ · ctx-first on `Handle`/`Enabled` and the `*Context` log paths ✓ · no `any` in signatures ✓ ·
no globals/`init` ✓ · `api/fault` for the typed error ✓ · one package, one file (+ test) — no premature
split ✓ · import graph clean (observability imports `api/fault` + OTel trace API only) ✓.

## DoD
ADR Review-checklist + DoD: **9/9** items hold. Scenarios: 6/6 named, un-skipped, passing.

## Recommendation
**pass** → stamp ADR-0009 `Reviewing → Implemented`, feat F17a → `implemented`. The lone Minor
(text-branch coverage) is optional polish, not a rework trigger.
