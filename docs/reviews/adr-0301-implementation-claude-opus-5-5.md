## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0301 implementation, model: claude-opus-5-5)

This report reviews commit 9acab03c on the branch `feat/adr-batch-0301-0303` against ADR-0301's Contracts, Scenarios,
Review checklist and Definition of done. The builder reviewed its own work, as the decider's standing rule asks (no
review agents), by running every check below.

### Minor
- **The format checks sit in their own file** (`model`): the plan names `internal/contract/rules.go` for the
  Contracts; the six checks of Decision 1 are in `internal/contract/formats.go`, beside it. No change needed.
- **A variant's tag values come from the compiled schema** (`model`): the plan reads each variant's tag `const` or
  `enum` from the side's JSON; `collectUnions` takes the `discriminator` from the side's JSON and the tag values from
  the compiled `Const` and `Enum` of the same schema, so the values are the same. No change needed.
- **`WithMatchTimeout` has no test of its own** (`model`): every test runs the 100 ms default. The option sets one
  field, which `ecmaScript` copies to each compiled pattern.

### Verified correct (keep)
- **Decision 1, the formats:** `profileFormats` registers funcd's own `date-time`, `uuid`, `email`, `uri`, `int32` and
  `int64` with `RegisterFormat`, under `AssertFormat`. `TestFormatTable` runs the 195 cases of the
  JSON-Schema-Test-Suite's draft 2020-12 `optional/format` files at `89e83a7`, vendored in `testdata/jsts` with their
  MIT license, and every verdict matches. The integer formats read a number's digits (`integerValue`), so `1e999999999`
  is refused at once instead of being expanded.
- **Decision 2, patterns:** `ecmaScript` compiles with regexp2/v2 `ECMAScript|Unicode` and sets `MatchTimeout`; a
  timed-out match is a non-match. `TestScenarioPatternFollowsECMA262` passes with `\d` and `\w` ASCII, `$` at the end of
  the input and a lookahead; `TestScenarioARunawayPatternMatchFailsTheValue` fails the value well under its 1 s bound.
- **Decision 3, details:** `leaves` walks `Causes`, gives a missing and an extra property one entry each at its own
  RFC 6901 pointer, and sorts by path in byte order, then message. `tagged` reports only the variant a value's tag
  names; a missing or unknown tag, a tag two variants name, two variants that accept a value, and a tagged variant with
  no leaf each give one entry at the union's path.
- **Decision 4:** `Check` compiles each side after `walk`, so `[z-a]` fails it, naming `properties/a`, and
  `^(?!admin$)[a-z]+$` passes (`TestScenarioPushGatesPatterns`). The callers' tests pass unchanged.
- **Decision 5 and the scope:** no shim, gateway or `CheckInput` code changed. `go.mod` moves regexp2/v2 v2.2.1 to the
  direct block; `go.sum` is unchanged.
- **Scenarios:** each of the ten has one test named `TestScenario` plus its name, with its `// scenario:` line, and
  passes; `TestValidateConcurrent` passes under `-race`.
- **Mutant checks:** each fails its tests. `Check` skipping `Compile`: `TestScenarioPushGatesPatterns`. The engine's own
  formats: `TestFormatTable`, the URI and integer scenarios. RE2 patterns: the pattern and push scenarios. Every
  union variant reported: `TestScenarioAUnionReportsItsTaggedVariant`. A leap second at any minute: `TestFormatTable`.
  A missing property not split: `TestScenarioEveryErrorIsListed`.
- **Tracking:** ADR-0301's substance is unchanged since its Proposed commit; its status is `Reviewing` and F88's
  contract-rules part is at `reviewing`.

### Checks
- `just ci` at 9acab03c: exit 0 (130 packages ok, `golangci-lint` 0 issues with and without `-tags dev`,
  `go mod verify` all modules verified).
- `go test -race ./internal/contract/`: ok.
- `go test` on `internal/artifact`, `pkg/sdk` and `cmd/funcdctl -tags dev`, the callers of `Check`: ok.

### Definition of Done
16 / 16 items hold (the ADR's 7 Review-checklist items and 9 applicable generic items; there is no port, so no
contract suite).

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0301 (implementation) → pass, 0/0/3, 3 model-attributed, DoD 16/16. See
docs/reviews/model-scorecard.md.

### Recommendation
Pass: stamp ADR-0301 `Implemented` and F88's contract-rules part `implemented`.
