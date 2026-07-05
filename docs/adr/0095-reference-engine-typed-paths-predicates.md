# ADR-0095: Reference engine — typed `${{ … }}` paths, predicates, and computation (one grammar for the platform)

- **Status**: Accepted
- **Date**: 2026-07-05 (accepted 2026-07-05; judged twice — 1 Blocker + 3 Majors folded: the `exists()` guard rule, scalar-only equality + scalar-item `contains`, root longest-first matching, scenario count; a build prerequisite of ADR-0094, implemented first)
- **Deciders**: green-0-rabbit
- **Tags**: expression, templating, contracts, workflow, sensor
- **Realizes**: [FEAT-0005/F73](../feat/0005-feat-workflow-engine.md)
- **Relates to**: [ADR-0094](0094-workflow-engine-core.md) (first Condition-mode consumer: `when.condition`) · [ADR-0059](0059-contract-as-oci-metadata.md)/[ADR-0090](0090-mandatory-single-io-schema.md) (the JSON-Schema contracts it checks against) · future F69 Sensor (first Select-mode consumer) · future F65 gate (supplies the schemas it resolves against)

## Context & Need

Three platform surfaces need to reference values inside JSON documents whose shapes are known
statically from contracts: workflow conditions (`when.condition`, ADR-0094), Sensor input
construction (F69, `${{ event.data.… }}`), and later computed field-mapping. Building each its own
mini-language guarantees drift; a general expression language (CEL) defeats the platform's
differentiator — every reference statically checkable against a schema before anything runs. This
ADR defines the **one** engine: a deliberately small, typed, dependency-free reference grammar
with two modes — **Select** (a reference or computed value) and **Condition** (an expression that
must end in a boolean predicate) — evaluated against `json.RawMessage` documents, checked against
JSON-Schema contracts.

## Scenarios

- `select-projects-path` — Given document root `event` and expression `${{ event.data.order.lines }}`, When evaluated in Select mode, Then the referenced value (an array) is returned whole; a reference is always the entire value, never string-interpolated.
- `condition-evaluates` — Given `${{ step.stats.output.rows.greaterThan(0) }}` and a stats output with `rows: 3`, Then Condition mode returns true; with `rows: 0`, false.
- `bare-reference-rejected-in-condition` — Given `${{ input.publish }}` in Condition mode, Then Check fails: every condition must end in a predicate method (`isTrue()`/`isFalse()` for booleans).
- `static-type-mismatch-rejected` — Given `greaterThan(0)` applied to a string-typed field, Then Check fails naming the path, its schema type, and the method.
- `unknown-path-rejected` — Given a path not present in the resolved schema, Then Check fails naming the missing segment.
- `default-substituted` — Given an optional field with schema `default: 0`, When the field is absent at evaluation, Then the predicate evaluates against the default.
- `optional-without-default-rejected` — Given a predicate on an optional field with no schema default, Then Check fails directing to `exists()` or a default.
- `exists-guards-optional` — Given `${{ a.b.exists().and(a.b.equals("x")) }}`, When `b` is absent, Then the expression is false without error (short-circuit).
- `combinators-compose` — Given `.and()`/`.or()`/`.not()` chains with bare-chain arguments, Then evaluation is left-to-right postfix with no precedence surprises.
- `literal-args-typed` — Given `in(1,2)` on a string field, Then Check fails: literal argument types must match the field type.
- `compute-arithmetic` — Given `${{ step.stats.output.rows.plus(step.audit.output.rows).greaterThan(100) }}`, Then both referenced fields type-check as numbers and the sum feeds the predicate.
- `compute-string` — Given `${{ event.time.substring(0,10) }}` in Select mode, Then a string result (the date part) is produced; `substring` on a number is a Check error.
- `compute-array-sum` — Given `${{ step.items.output.prices.sum() }}` where `prices` is a numeric array, Then the numeric total is produced; `sum()` on a non-numeric array is a Check error.
- `reference-args-typed` — Given a method whose argument is a reference chain of the wrong type, Then Check fails naming the argument's path and expected type.
- `divide-by-zero` — Given `dividedBy(0)` with a literal zero, Then Check rejects it; Given a reference divisor that evaluates to zero, Then Eval returns a fault and the consumer fails fast (never a silent NaN).
- `grammar-error-rejected` — Given malformed input (`${{ a..b }}`, unbalanced parens, an unknown method), Then Parse fails with a position-carrying `fault.Invalid`.
- `roots-are-context-scoped` — Given a Condition context exposing roots `step.<parent>.output` and `input` only, Then an expression rooted anywhere else (e.g. `event`, a non-parent step) fails Check.

## Scope

**In**: the grammar (two modes), the type system with the predicate **and computation** method
tiers, static checking against JSON-Schema contracts, the evaluator over `json.RawMessage`, the
pluggable root/context API, error mapping to `api/fault`. **Out**: string interpolation
(`concat` is the explicit form), filters, user-defined functions, the field-mapping *wiring*
into workflow `params` (a follow-up consumer ADR on this substrate); the consumers' own wiring
(ADR-0094 workflows, F69 Sensors, F65 schema supply).

## Constraints & Decision drivers

Pure Go, zero new dependencies (hand-rolled lexer + recursive-descent parser — the grammar is
regular enough that this is small); every expression checkable at reconcile time against schemas;
no `any` in exported APIs; `api/fault` errors; deterministic evaluation (no reflection surprises,
no truthiness coercion).

## Alternatives considered

| Option | Why it lost |
|---|---|
| `github.com/google/cel-go` | A full language (arithmetic, macros, dynamic typing) — static schema-checking becomes best-effort, and it is a heavyweight dependency for a platform whose pitch is "fails at reconcile, not runtime". |
| `github.com/expr-lang/expr` | Same class: general evaluator, reflection-based envs, no schema-native checking. |
| JSONPath (any dialect) | Selection only — no typed predicates; the dialect zoo (RFC 9535 vs legacy) invites drift; still needs a second language for conditions. |
| `text/template` | String-oriented interpolation — exactly what this engine forbids in V1; untyped. |
| QuickJS-on-wazero (JS expressions, built-ins disabled, whitelisted functions — e.g. an rquickjs-built WASM) | wazero is pure-Go (no-cgo OK), but real JS defeats static schema-checking (analyzable-subset ⇒ writing a parser/checker anyway), reintroduces coercion/`undefined` semantics the defaults rule exists to eliminate, needs fuel/interrupt machinery for a non-total language, and adds a Rust/C→wasm supply-chain artifact + VM memory to the daemon. Arithmetic/string computation lands instead as typed method rows (the computation tier); arbitrary user computation already has a home — a Function step. An **inline script step** (data-plane, contract-carrying) is the legitimate future use of this stack — a board idea, not this engine. |
| `go-playground/validator/v10` | Struct-tag validation of Go values, not an expression/reference engine; also rejected for resource-spec validation in ADR-0094 (duplicates huma-schema + `Validate()`). |
| Per-consumer mini-parsers | The drift this ADR exists to prevent. |

## Decision

One package, `internal/expr`, exposing parse → check → eval over context-scoped roots.

### Grammar (V1 — complete)

```
expression := "${{" chain "}}"
chain      := reference | predicate | combinator
reference  := root ("." segment)*            # segment: identifier or array index [n]
predicate  := reference "." method "(" args? ")"
combinator := predicate combinePart+                        # args are BARE chains — delimiters appear once
combinePart:= ".and(" chain ")" | ".or(" chain ")" | ".not()"
args       := arg ("," arg)*
arg        := literal | chain                # reference/computed arguments are type-checked too
literal    := "\"" string "\"" | number | true | false
```

- Delimiters are `${{ … }}` (GitHub-Actions style): unquoted-safe in block YAML (verified — a bare
  `{{ …` is a YAML flow-mapping parse error), visually distinct from shell/compose `${VAR}` env
  substitution, and immune to Helm-chart `{{ }}` escape collisions if manifests are ever templated.
- A `${{ … }}` expression is always the **whole** YAML/JSON scalar value — embedding it inside a
  larger string is a Parse error (no interpolation in V1). Combinator arguments are bare chains
  (`.and(input.publish.isTrue())`), never re-delimited.
- **Select mode** accepts a bare `reference` and returns its value. **Condition mode** requires
  the chain to end in a predicate/combinator — a bare reference is rejected at Check
  (booleans use `isTrue()`/`isFalse()`).
- Combinators are postfix and left-associative: there is **no operator precedence** to learn or
  implement. `.and()`/`.or()` short-circuit left-to-right.
- **Root matching**: Parse is root-agnostic (an expression is a plain dotted chain); at Check,
  the leading segments are matched **longest-first** against the root set the Resolver declares
  (so `step.stats.output.rows` resolves root `step.stats.output`, path `rows`). `Roots()` and the
  `docs` evaluation map are keyed by the matched root, exactly as declared.

### Predicate tier — types and boolean methods

| Schema type | Methods | Result |
|---|---|---|
| any type | `exists()` | boolean |
| scalar (`string` / `number` / `integer` / `boolean`) | `equals(x)` · `notEquals(x)` — receiver AND argument scalar (no deep equality) | boolean |
| `string` · `number` / `integer` | `in(x, …)` · `notIn(x, …)` | boolean |
| `number` / `integer` | `greaterThan(n)` · `greaterThanOrEqual(n)` · `lessThan(n)` · `lessThanOrEqual(n)` | boolean |
| `boolean` | `isTrue()` · `isFalse()` | boolean |
| `array` | `contains(x)` — **scalar-item arrays only** (arg literal or reference, type-matching `items`; no deep equality via object items) | boolean |
| `object` | `exists()` only — no deep equality in V1, including via reference args | boolean |
| boolean expression | `.and(expr)` · `.or(expr)` · `.not()` | boolean |

Rules: `equals`/`notEquals`/`in`/`notIn`/`contains` arguments (literal or reference) must
type-match the field's schema type; numeric comparison on non-numbers, `isTrue()` on
non-booleans, etc. are Check errors.

### Computation tier — typed value methods

Value-producing methods extend the same registry and AST; every row is statically typed, and a
Condition-mode chain must still **terminate in a boolean predicate** (computed values feed
predicates or Select results — a bare computed value is not a condition).

| Receiver | Methods | Result |
|---|---|---|
| `number` / `integer` | `plus(n)` · `minus(n)` · `times(n)` · `dividedBy(n)` · `abs()` · `round()` | number (`round()` → integer) |
| `string` | `concat(s)` · `upper()` · `lower()` · `trim()` · `replace(a, b)` · `substring(from, len)` · `length()` | string (`length()` → integer) |
| `array` | `length()` · `sum()` (numeric items) · `join(sep)` (string items) | integer / number / string |
| `number` · `boolean` | `toString()` | string |

Computation rules: arguments may be literals or typed reference chains (checked like any
reference, defaults + guard rules included); **string interpolation stays out** — `concat` is the
explicit, checkable form; the language stays **total** (no loops, no recursion). Semantics pinned
for the implementer: `substring` indexes **runes**, not bytes; `dividedBy` always yields
`number` (no integer truncation); `replace` replaces **all** occurrences. The only runtime
failure class is arithmetic (a reference divisor evaluating to zero) — Eval returns a fault and
the consumer fails fast (a literal `dividedBy(0)` is rejected at Check).

### The defaults rule (no null semantics)

A predicate may reference a field only if the resolved schema marks it **required** or declares a
**`default`**; otherwise Check fails, pointing at `exists()` (the guard) or adding a default. At
evaluation, an absent field with a default evaluates against the default.

**The guard rule** (what makes `exists()` a guard, not just a probe): inside the right-hand chain
of an `.and(…)` whose left side is exactly `X.exists()`, references whose path **equals or
extends X** are exempt from the required-or-default rule — `.and()` short-circuits left-to-right,
so an exempted reference is never evaluated while X is absent. The exemption is *positional*
(that `.and()` argument and combinators nested within it) — an unguarded sibling expression gets
no exemption. Consequence: evaluation can never encounter "missing" outside `exists()` — no
three-valued logic, no null propagation.
(The contract toolchain — ADR-0058 family — gains a way to declare defaults from code types; noted
as the F65 ADR's carry-along.)

### Static checking

`Check` walks the expression against a `Resolver` that answers path lookups from JSON-Schema
contracts (in ADR-0094: the contract graph cached in `Workflow.status`; in F69: the target
workflow's contract). Errors name the path, the schema type, the offending method, and the
position. Roots are **context-scoped**: each consumer constructs the resolver with exactly the
roots it permits (Condition in ADR-0094: `step.<direct-parent>.output`, `input`; Select in F69:
`event`). An expression using any other root fails Check — the direct-parents-only rule is
enforced here, not by convention.

### Contracts

```go
package expr // internal/expr

type Mode int

const (
	Select    Mode = iota // bare reference allowed; returns the referenced value
	Condition             // must terminate in a predicate; returns a boolean
)

// Field is what a Resolver reports for a resolved path.
type Field struct {
	Type       string          // "string" | "number" | "integer" | "boolean" | "array" | "object"
	Items      string          // element type when Type == "array"
	Required   bool
	HasDefault bool
	Default    json.RawMessage
}

// Resolver answers path lookups against the consumer's schemas. Implementations are
// context-scoped: they expose exactly the roots the consumer permits.
type Resolver interface {
	Resolve(root string, path []string) (Field, error) // fault.NotFound / fault.Invalid
}

type Expr struct{ /* unexported: parsed AST + source + mode */ }

func Parse(src string, mode Mode) (*Expr, error)           // grammar errors: fault.Invalid + position
func (e *Expr) Check(r Resolver) error                      // static: paths, types, methods, defaults rule
func (e *Expr) Roots() []string                             // referenced roots (e.g. for parent-edge checks)
func (e *Expr) Eval(docs map[string]json.RawMessage) (json.RawMessage, error) // Select mode
func (e *Expr) EvalBool(docs map[string]json.RawMessage) (bool, error)        // Condition mode
```

`docs` maps each **matched root** (see Root matching) to its document (e.g. `"input"` → the run
input, `"step.stats.output"` → that step's recorded output). Eval on an unchecked or
mode-mismatched expression returns `fault.Internal` — consumers always Parse+Check at reconcile
and Eval at runtime.

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| JSON-Schema contracts via the consumer's `Resolver` | `Parse` / `Check` / `Eval` / `EvalBool` / `Roots` |
| `json.RawMessage` documents at evaluation | position-carrying `fault.Invalid` diagnostics |
| nothing else — stdlib only, no config keys | the grammar contract above (frozen surface for V2 growth) |

## Temporary workarounds

| Workaround | Exit criterion |
|---|---|
| Select mode ships with unit tests only (no in-tree consumer until F69) | the F69 Sensor ADR wires it |
| Computed values usable in conditions/Select, but not yet wired into workflow `params` mapping | the field-mapping consumer ADR (extends ADR-0094's `params` to accept expressions) |

## Implementation plan

Files: `internal/expr/{expr.go,lex.go,parse.go,check.go,eval.go}` + table-driven tests. No
`go.mod` additions. Test plan: one named test per Scenario (17) — pure unit tests (documents +
schema-backed fake Resolver), plus a fuzz test over Parse (stdlib fuzzing) since this is a parser
handling user input. Definition of done: all scenario tests + fuzz corpus green; the four
sub-checks green; no exported `any`; godoc on every exported symbol states its mode behavior.

## Review checklist

- [ ] Grammar exactly as specified: whole-value only, postfix combinators, no interpolation, no precedence.
- [ ] Method/type matrix enforced at Check — every cell above tested, every off-matrix use rejected.
- [ ] Defaults rule enforced (required-or-default, `exists()` exempt); evaluation substitutes defaults.
- [ ] Condition mode rejects bare references; Select mode returns whole values.
- [ ] Context-scoped roots: Check rejects roots the Resolver doesn't expose; `Roots()` supports the parent-edge rule.
- [ ] Errors are `api/fault` with positions; no panics on any malformed input (fuzz-backed).
- [ ] Zero new dependencies; package imports stdlib + `api/fault` only.

## Consequences

- (+) One grammar for conditions today and Sensor projections/mapping tomorrow — no drift by construction.
- (+) Everything fails at reconcile with named-path diagnostics; runtime evaluation cannot hit "missing".
- (+) Dependency-free and small; the V2 growth path (computation) extends the same AST.
- (−) Deliberately less expressive than CEL — no interpolation, filters, or user functions; arithmetic/string/array computation is typed method rows, and the thread-through idiom (ADR-0094) still covers parameter flow until the mapping ADR.
- Risk: hand-rolled parsers invite edge-case bugs — mitigated by the fuzz test and the tiny grammar.

## Open questions

- Field-mapping wiring (expressions inside workflow `params` / Sensor beyond `input`) — a consumer ADR on this substrate.
- CloudEvent envelope roots for Select mode (`event.time` vs `event.data.…` handling) — fixed in the F69 Sensor ADR that constructs that Resolver.
- Default declaration syntax in the JS/Python contract toolchain — carried by the F65 gate ADR.

## References

- [ADR-0094](0094-workflow-engine-core.md) `when.condition` (Condition-mode consumer contract).
- FEAT-0005 category H (the shared-component rationale) and the condition examples in its capability map.
- Prior art: Argo Events trigger parameterization (`dataKey`/JSONPath — the runtime-failure model this rejects); CEL (the general-language alternative, rejected above).
