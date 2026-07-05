# ADR-0095 implementation review — reference engine on goja (FEAT-0005/F73)

- **Reviewed**: 2026-07-05
- **Producing model**: claude-opus-4-8
- **Verdict**: **pass**
- **Work**: `internal/expr/{expr.go,check.go,eval.go}` + `expr_test.go`, branch `v1.1.0`

## Verification (run, not eyeballed)

| Check | Command (`nix develop -c …`) | Result |
|---|---|---|
| build | `go build ./...` | exit 0 |
| test | `go test ./internal/expr/` | exit 0 — 17/17 named scenarios + `FuzzParse` PASS |
| lint | `go tool golangci-lint run ./internal/expr/` | **0 issues** |
| mod | `go mod verify` | all modules verified |
| fuzz | `go test -fuzz=FuzzParse -fuzztime=5s` | ~780k execs, no panic |

`TestPythonPoolSmoke` fails in the full-suite run — pre-existing environmental
(Python shim readiness), unrelated to this change; not attributed.

## Scenarios (ADR §Scenarios → named tests, all passing)

select-projects-path · condition-evaluates · bare-reference-rejected-in-condition ·
static-type-mismatch-rejected · unknown-path-rejected · default-substituted ·
optional-without-default-rejected · guard-allows-optional · operators-compose ·
membership-typed · compute-arithmetic · compute-string · compute-array-sum ·
reference-operands-typed · divide-by-zero · grammar-error-rejected ·
roots-are-context-scoped — **17/17 present, un-skipped, passing.**

## Review checklist (ADR §Review checklist)

- [x] `${{ … }}` wrapper enforced, whole-value only, parsed by `goja/parser` (`Parse`).
- [x] Admitted subset enforced at Check; banned constructs rejected (`==`/`!=`, assignment, arrow fn, `new`, non-root ids, truthiness operands) — `TestGrammarErrorRejected`, `TestOperatorsCompose`.
- [x] Defaults rule + native `!== undefined` guard (positional short-circuit) — `TestDefaultSubstituted`, `TestGuardAllowsOptional`, `TestOptionalWithoutDefaultRejected`.
- [x] Condition requires a boolean expression (bare reference rejected); Select returns the value.
- [x] Context-scoped roots, longest-first, `Roots()` — `TestRootsAreContextScoped`.
- [x] `api/fault` errors with positions; no panic on any input (fuzz-backed).
- [x] goja the only new dependency; imports are stdlib + `api/fault` + `goja`/`goja/parser`.

## ✅ Verified correct — keep

- **Checker/evaluator split is clean**: `check.go` walks goja's AST (the differentiator — coercion/typos rejected at reconcile), `eval.go` only runs already-validated programs. The subset boundary (allow/deny) is enforced in one place per node kind.
- **Guard rule** implemented exactly to spec: `X !== undefined && …` exempts references extending `X` in the right operand, positional, short-circuit-safe (`TestGuardAllowsOptional` proves absent-field → false, no error).
- **Dotted-root binding**: roots like `step.stats.output` are rebuilt as nested globals (`setNested`) so JS member access resolves; prefix-sharing roots merge. A subtle correctness point, handled.
- **No `any` in the public API**; `interface{}` confined to eval-time JSON↔goja plumbing (forbidigo-clean, and lint confirms 0 issues).
- **Contracts honored**: `Mode`/`Field`/`Resolver`/`Parse`/`Check`/`Roots`/`Eval`/`EvalBool` match the ADR; godoc on every exported symbol.

## Findings

### 🔴 Blocker — None.
### 🟡 Major — None.
### Minor
- **[model] Allow/deny coverage is scenario-driven, not an exhaustive matrix.** The ADR checklist mentions "an allow/deny test matrix"; the banned-construct coverage lives across `TestGrammarErrorRejected` (==, assignment, arrow, new) and `TestOperatorsCompose` (truthiness) rather than a single table enumerating every rejected node kind (e.g. `this`, template literals, object literals, `for`). Adequate for the scenarios; a follow-up could add a table test enumerating each banned construct. Non-blocking.

### `adr`-attributed (recorded, not scored)
- **divide-by-zero semantics**: JS `x/0` = `Infinity` (no throw), so the checker rejects a *literal* `/0` and Eval faults on a non-finite *Select* result; division inside a Condition comparison follows JS. This diverges from the prior hand-rolled evaluator's fault-on-any-`/0` intent — a consequence of adopting a real JS engine, captured by an authorized in-place ADR note (Consequences). The `divide-by-zero` scenario passes as written (Select mode). Not a model defect.

## Recommendation

**Pass.** DoD met, no Blockers/Majors, all 17 scenarios green, lint clean, goja the sole
new dependency. Stamp ADR-0095 `Reviewing → Implemented` and FEAT-0005 F73 →
`implemented`. The one Minor (a dedicated banned-construct table test) is a cheap
future hardening, not a gate.
