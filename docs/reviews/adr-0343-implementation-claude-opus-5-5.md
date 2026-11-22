## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0343 implementation, model: claude-opus-5-5)

This report reviews the commit `feat(funclog): under funcdctl dev, a Lake's and a SQLDatabase's engines keep their
lines (ADR-0343)` on the branch that ships FEAT-0020. By the decider's standing rule of no review agents for
implementation work, the session that wrote the code ran this review, so it is not independent; an independent judge
reviewed the ADR. Every check below was run, not read.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Routed (not this work's)

- **The `adr` skill's list for a partial supersession** leaves out Temporary workarounds and Open questions, which
  ADR-0272 Decision 7 asks for; the judge found the missing Open questions here and it is folded. A one-line skill
  fix, for the decider.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`: `go test -race` on `internal/provider`, `internal/lake/devengine` and
  `internal/sql/devengine` (`-tags dev`) and the engine-logs tests of `pkg/funcd`; `go test -tags dev` on the scenario
  in `cmd/funcdctl`; `go build ./...` and `go vet`, with and without `-tags dev`; `go tool golangci-lint run`, also
  with `--build-tags dev`, 0 issues; `just check-behavior`; `just check-hygiene`.
- **The scenario** `engine-logs-dev-lake-sql`: `TestScenarioEngineLogsDevLakeSQL` passed on a SQL engine built here
  with `just build-sql-engine darwin arm64` (rqlited v10.5.3), reading todo-db's lines with `EngineLogs` as `stderr`,
  none of them in dev's terminal; it skips on the committed placeholder, as the ADR says. The Lake half:
  `TestRuntimeHandsTheEnginesStderrLinesOn` on the runner's fake engine in its `fail` mode, and
  `TestDevLakeAndSQLLinesAreKeptUnderTheirKind` on the capture; `TestPrefixLogWriterHandsEachLineOn`,
  `TestEngineOutputHandsEachLineOn` and `TestLineHook` on the parts.
- **The mutants**, each caught:

  | Mutant | Caught by |
  |---|---|
  | dev's SQL runner hands no line on | `TestScenarioEngineLogsDevLakeSQL`, on the built engine |
  | dev's Lake runner hands no line on | `TestRuntimeHandsTheEnginesStderrLinesOn` |

- **The contracts**: `provider.LineHook` (`FollowLines`, `LinesOf`), embedded by dev's Lake and SQL engine runners and
  by dev's Docker runtime in place of its own func, which keeps ADR-0342's tests passing; `followDevLines` fills the
  kind when a spec names none; `lakeDeps` and `sqlDeps` carry the capture from `funcd.go`, as `addonDeps` does.
- **The behavior row**: `funcdctl-dev.md`, "What dev's Lake and SQL engines print".

### Definition of Done

13 of 13 items hold: the 3 Review-checklist items, the scenario on a bundled SQL engine, the touched packages'
checks (the gate runs `just ci-full`), the behavior row, and 7 generic items.

### Model scorecard

To record: claude-opus-5-5 on ADR-0343 (implementation) → pass, 0 blockers, 0 majors and 0 minors. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Nothing goes back to the builder; ADR-0343 is stamped `Implemented`, and F269 turns `implemented`. The PR that ships
it names issue #1519.
