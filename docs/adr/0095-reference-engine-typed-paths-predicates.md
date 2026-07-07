# ADR-0095: Reference engine — typed native-JavaScript expressions on goja (one grammar for the platform)

- **Status**: Implemented
- **Date**: 2026-07-05 (implemented 2026-07-05 → `internal/expr` on goja, review passed: 17/17 scenarios, lint clean, goja sole new dep. Accepted 2026-07-05; judged twice — 1 Blocker + 3 Majors folded: the guard rule, scalar-only equality + scalar-item membership, root longest-first matching, scenario count. **In-place update 2026-07-05 (process bypass, decider-authorized):** the engine is now **goja** (`github.com/dop251/goja`, pure-Go ECMAScript) with **native JavaScript syntax** instead of a hand-rolled `${{ … }}` postfix grammar. A bench (`bench/expr-engine`, RESULTS.md) drove this: goja beats the hand-rolled evaluator on parse/cold/warm/throughput, native operators are 2.5× faster than injected methods, and `exists()` **cannot** be a method on an absent field (`TypeError`) — so the guard is native `!== undefined`. The differentiator is preserved: funcd keeps a **type-checker over goja's public parser AST** (`goja/parser`), so a bad condition still fails at **reconcile**, not runtime. The public Go contract (`Mode`/`Field`/`Resolver`/`Parse`/`Check`/`Roots`/`Eval`/`EvalBool`) is unchanged — only the grammar and the internals change. The prior hand-rolled `internal/expr` implementation is superseded in place and re-implemented on goja.)
- **Deciders**: green-0-rabbit
- **Tags**: expression, templating, contracts, workflow, sensor
- **Realizes**: [FEAT-0005/F73](../feat/0005-feat-workflow-engine.md)
- **Relates to**: [ADR-0094](0094-workflow-engine-core.md) (first Condition-mode consumer: `when.condition`) · [ADR-0059](0059-contract-as-oci-metadata.md)/[ADR-0090](0090-mandatory-single-io-schema.md) (the JSON-Schema contracts it checks against) · future F69 Sensor (first Select-mode consumer) · future F65 gate (supplies the schemas it resolves against)

## Context & Need

Three platform surfaces need to reference values inside JSON documents whose shapes are known
statically from contracts: workflow conditions (`when.condition`, ADR-0094), Sensor input
construction (F69, `${{ event.data.… }}`), and later computed field-mapping. Building each its own
mini-language guarantees drift; letting arbitrary JavaScript run *unchecked* defeats the
platform's differentiator — a typo'd field (`input.pubish === true`) or a coercing comparison
(`region > 0`) is silently `false` at runtime, the Argo/n8n failure mode funcd rejects. This ADR
defines the **one** engine: **goja** (pure-Go ECMAScript) parses and evaluates, and funcd runs a
**type-checker over goja's parser AST** that admits only a **typed subset** of JS and rejects the
rest at reconcile — so every expression is statically checkable against a schema *before anything
runs*, in a language users already know. Two modes: **Select** (a reference or computed value,
returned by Eval) and **Condition** (a boolean expression, returned by EvalBool). Documents are
`json.RawMessage`; schemas come from a context-scoped `Resolver`.

## Scenarios

- `select-projects-path` — Given document root `event` and expression `${{ event.data.order.lines }}`, When evaluated in Select mode, Then the referenced value (an array) is returned whole; a reference is always the entire value, never string-interpolated.
- `condition-evaluates` — Given `${{ step.stats.output.rows > 0 }}` and a stats output with `rows: 3`, Then Condition mode returns true; with `rows: 0`, false.
- `bare-reference-rejected-in-condition` — Given `${{ input.publish }}` in Condition mode, Then Check fails: a condition must be an explicit boolean expression (use `=== true`), not a bare reference.
- `static-type-mismatch-rejected` — Given `${{ input.name > 0 }}` on a string-typed `name`, Then Check fails naming the path and its schema type (comparison needs numbers).
- `unknown-path-rejected` — Given a member path not present in the resolved schema (e.g. `input.pubish`), Then Check fails naming the missing segment (never a silent `undefined`).
- `default-substituted` — Given an optional field with schema `default: 0`, When the field is absent at evaluation, Then it is bound to the default and the expression evaluates against it.
- `optional-without-default-rejected` — Given a reference to an optional field with no schema default and no guard, Then Check fails directing to a `!== undefined` guard or a default.
- `guard-allows-optional` — Given `${{ input.a.b !== undefined && input.a.b === "x" }}` on an optional `a.b`, Then Check passes; When `b` is absent, Then the expression is false without error (`&&` short-circuits before the guarded reference is read).
- `operators-compose` — Given `&&`/`||`/`!` over boolean sub-expressions, Then evaluation follows JS precedence and short-circuits; non-boolean operands (truthiness) are Check errors.
- `membership-typed` — Given `${{ [1,2].includes(input.name) }}` on a string `name`, Then Check fails: the array element type must match the tested value's type.
- `compute-arithmetic` — Given `${{ step.stats.output.rows + step.audit.output.rows > 100 }}`, Then both fields type-check as numbers and the sum feeds the comparison.
- `compute-string` — Given `${{ event.time.slice(0,10) }}` in Select mode, Then a string result (the date part) is produced; `.slice` on a number is a Check error.
- `compute-array-sum` — Given `${{ sum(step.items.output.prices) }}` where `prices` is a numeric array, Then the total is produced; `sum(...)` on a non-numeric array is a Check error.
- `reference-operands-typed` — Given a comparison whose other operand is a reference of the wrong type (e.g. `input.name === input.count`), Then Check fails naming the operand path and expected type.
- `divide-by-zero` — Given `/ 0` with a literal zero, Then Check rejects it; Given a reference divisor that evaluates to zero, Then Eval returns a fault and the consumer fails fast (never a silent Infinity/NaN).
- `grammar-error-rejected` — Given malformed input (`${{ a..b }}`, unbalanced parens) or a banned construct (`==`, a `for` loop, an arrow function), Then Parse or Check fails with a position-carrying `fault.Invalid`.
- `roots-are-context-scoped` — Given a Condition context exposing roots `step.<parent>.output` and `input` only, Then an expression rooted anywhere else (e.g. `event`, a non-parent step) fails Check.

## Scope

**In**: the `${{ … }}` wrapper + the admitted native-JS subset (two modes), the type-checker over
goja's parser AST, goja evaluation with bound roots, the pluggable `Resolver` API, error mapping to
`api/fault`, the whitelisted helper set (e.g. `sum`). **Out**: string interpolation, user functions,
loops, the field-mapping *wiring* into workflow `params` (a follow-up consumer ADR); the consumers'
own wiring (ADR-0094 workflows, F69 Sensors, F65 schema supply).

## Constraints & Decision drivers

Pure Go (goja is pure Go — no cgo, no wasm blob, no external toolchain); every expression
checkable at reconcile time against schemas by walking goja's parser AST; the admitted subset is
**total** (no loops/functions — `Runtime.Interrupt` is a backstop, not the primary defence); no
`any` in the engine's own exported API; `api/fault` errors; deterministic evaluation (a fresh or
pooled `goja.Runtime` with no host bindings beyond the bound root documents). One well-maintained
dependency family (goja — MIT; Grafana's `sobek` fork is the drop-in successor if upstream stalls).

## Alternatives considered

**Chosen: goja + a type-checker over its parser AST.** Its parser (`goja/parser`) is a public
package, so funcd reuses a battle-tested ES parser and keeps only the ~500-line checker that is the
actual differentiator; goja is pure-Go (no cgo, no wasm), and the bench (`bench/expr-engine`)
measured it *faster* than the hand-rolled evaluator it replaces (0.54 µs warm vs 1.1 µs). Losers:

| Option | Why it lost |
|---|---|
| **Hand-rolled `${{ … }}` postfix grammar** (this ADR's original decision) | Zero deps, but reimplements a lexer/parser/evaluator (~1000 lines funcd must own), a bespoke syntax users must learn, and — measured — *slower* than goja. `exists()` worked only because we controlled evaluation; on any real JS engine it can't be a method. Superseded in place by goja. |
| **QuickJS on wazero** (rquickjs / mquickjs / Javy) | Same JS capability as goja but every operational cost is higher: a ~1 MB wasm blob committed to the tree, a Rust/C→wasm build in the flake, the wazero runtime dep, and MB-class memory per VM instance — versus goja's pure-Go source and ~3.5 KB per pooled Runtime. Benched (`run.sh`), rejected. |
| **Untyped JS** (goja/CEL/expr with no checker) | Runs the expression but can't reject `input.pubish === true` (typo → silently `false`) or `region > 0` (coercion) until runtime — the exact Argo/n8n failure funcd rejects. The checker-over-AST is what makes JS safe here. |
| `github.com/expr-lang/expr` | Static type-checking assumes a *fixed compile-time Go struct* env; funcd's contracts are per-Workflow dynamic JSON Schemas, so expr's checker degrades to runtime-only for our case. |
| `github.com/google/cel-go` | Genuinely fits schema-checked expressions (k8s uses it), but drags antlr + protobuf into a minimal-binary daemon; goja gives the same capability with a lighter dependency and plain-JS syntax. |
| `go-playground/validator/v10` | Struct-tag validation of Go values, not an expression engine (also rejected for spec validation in ADR-0094). |

## Decision

One package, `internal/expr`, exposing parse → check → eval over context-scoped roots. `Parse`
wraps `goja/parser.ParseFile`; `Check` walks the returned AST enforcing the subset + type rules;
`Eval`/`EvalBool` run a compiled `goja.Program` on a `goja.Runtime` with the root documents bound
as globals.

### Surface — the `${{ … }}` wrapper + native JavaScript

- The `${{ … }}` delimiter is kept (GitHub-Actions style: unquoted-safe in block YAML — verified —
  and visually distinct from shell `${VAR}`). Its contents are now **native JavaScript**, not a
  bespoke grammar. The whole scalar must be a single `${{ … }}` — no string interpolation in V1.
- **Select mode** admits a member expression (a reference) or a computed value and returns it.
  **Condition mode** requires a boolean-typed expression (`&&`, `||`, `!`, comparisons) — a bare
  reference is a Check error even if boolean-typed (`${{ input.publish }}` → use
  `${{ input.publish === true }}`), so a condition is always an explicit predicate.
- **Root matching**: goja parses `step.stats.output.rows` as a member chain; Check matches the
  leading identifiers **longest-first** against the roots the Resolver declares (root
  `step.stats.output`, path `rows`). `Roots()` and the `docs` map are keyed by the matched root.

### The admitted subset (what Check allows; everything else is rejected at reconcile)

| Category | Allowed | Result |
|---|---|---|
| references | member access `a.b.c`, index `a[0]`, roots only from the Resolver | the field's type |
| comparison | `===` `!==` (same-type scalars) · `<` `<=` `>` `>=` (numbers) | boolean |
| membership | `["a","b"].includes(x)` (scalar-item array literal or ref, type-matching `x`) | boolean |
| logic | `&&` `||` `!` — **operands must be boolean** (no truthiness) | boolean |
| ternary | `cond ? a : b` — `cond` boolean, `a`/`b` same type | branch type |
| arithmetic | `+` `-` `*` `/` (numbers; `/` by a literal `0` rejected at Check) | number |
| string ops | `+` (concat), `.toUpperCase()` `.toLowerCase()` `.trim()` `.length` `.slice(a,b)` `.replaceAll(a,b)` `.startsWith(s)` `.endsWith(s)` `.includes(s)` | string / integer / boolean |
| array ops | `.length` · `.includes(x)` · `.reduce((a,b)=>a+b,0)` **not allowed** (arrow funcs banned); use whitelisted `sum(arr)` helper | integer / boolean |
| **rejected** | `==` `!=` (coercing), assignment, `for`/`while`, function/arrow expressions, `new`, `this`, any identifier not a declared root, member access on a non-object/array | Check error |

Type rules mirror the prior tier tables: comparisons need numbers on both sides, `===` needs
same-type scalars, `&&`/`||` need booleans, `.includes` element type must match the array's
`items`. Rejecting `==`/`!=` is what closes the coercion hole — goja never evaluates a coercing
comparison because Check refuses the tree. The subset has no loops or user functions, so it is
**total**. (A small whitelisted helper set — e.g. `sum(arr)` for numeric arrays — is injected as
Go functions for aggregations that would otherwise need banned arrow callbacks; each is a typed,
checkable call, not a language extension.)

### The defaults rule + native guard (no null semantics)

A reference to a field is allowed only if the schema marks it **required**, declares a
**`default`**, or the reference is **guarded**; otherwise Check fails, pointing at the `!==
undefined` guard or a default. At evaluation, an absent field with a default is bound to its
default before the program runs.

**The guard rule**, native form: inside the right operand of a `X !== undefined && …`, references
whose path **equals or extends X** are exempt from the defaults rule — `&&` short-circuits, so an
exempted reference is never read while X is absent (`${{ a.b !== undefined && a.b === "x" }}` is
valid on an optional `a.b`). The exemption is *positional* (that `&&`'s right operand and
expressions nested within it). Consequence: evaluation never reads "missing" outside a guard — no
three-valued logic, no coercion of `undefined`. (This replaces the prior `X.exists().and(…)`
method form, which is impossible on goja: `exists()` cannot be called on an absent field — it
throws `TypeError` — as the bench's `inject` lane confirmed.)

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
// context-scoped: Roots reports the root documents exposed (Check matches leading
// member segments against it, longest-first); Resolve reports the Field at a path.
type Resolver interface {
	Roots() []string                                    // the exposed root documents
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
| `github.com/dop251/goja` + `goja/parser` (the ES parser + evaluator); stdlib; `api/fault` | the frozen public surface above (stable for V2 growth) |

## Temporary workarounds

| Workaround | Exit criterion |
|---|---|
| Select mode ships with unit tests only (no in-tree consumer until F69) | the F69 Sensor ADR wires it |
| Computed values usable in conditions/Select, but not yet wired into workflow `params` mapping | the field-mapping consumer ADR (extends ADR-0094's `params` to accept expressions) |

## Implementation plan

Files: `internal/expr/{expr.go,check.go,eval.go}` (Parse wraps `goja/parser`; check.go walks the
AST enforcing the subset + type rules — the type logic ports from the prior check.go; eval.go
compiles a `goja.Program` and runs it on a pooled `goja.Runtime` with bound roots + the helper
set). Add `github.com/dop251/goja` via `go get`. Test plan: one named test per Scenario (17) over
documents + a schema-backed fake Resolver, plus an allow/deny matrix for the subset and a fuzz
test over Parse+Check (no panic on any input). Definition of done: all scenario tests + fuzz
corpus green; the four sub-checks green; goja is the only new dep; godoc on every exported symbol.

## Review checklist

- [ ] `${{ … }}` wrapper enforced; whole-value only (no interpolation); contents parsed by `goja/parser`.
- [ ] The admitted subset is enforced at Check — every allowed row typed, every banned construct (`==`/`!=`, loops, functions, `new`, `this`, non-root identifiers, truthiness operands) rejected, with an allow/deny test matrix.
- [ ] Defaults rule + native `!== undefined` guard enforced (positional short-circuit exemption); evaluation binds defaults for absent fields.
- [ ] Condition mode requires a boolean expression (bare reference rejected); Select mode returns the value.
- [ ] Context-scoped roots: Check rejects roots the Resolver doesn't expose; longest-first matching; `Roots()` supports the parent-edge rule.
- [ ] Errors are `api/fault` with positions; no panics on any input (fuzz over Parse+Check).
- [ ] goja is the only new dependency; the checker imports stdlib + `api/fault` + `goja`/`goja/parser` only.

## Consequences

- (+) One engine + syntax (plain JS) for conditions today and Sensor projections/mapping tomorrow — no drift, nothing bespoke for users to learn.
- (+) Everything fails at reconcile with named-path diagnostics; runtime evaluation cannot hit "missing".
- (+) One pure-Go dependency (goja/sobek); funcd owns only the ~500-line checker, not a parser/evaluator; benched faster than the hand-rolled engine.
- (−) Deliberately a *subset* of JS — no loops, user functions, interpolation, or truthiness; the checker rejects the rest at reconcile. Arbitrary user compute has its home in a Function step, not here.
- Risk: the subset-checker must reject every unsafe construct — mitigated by a table-driven allow/deny test matrix and a fuzz test over Parse+Check (no panic on any input).
- (−) JS division semantics: `x/0` is `Infinity` in JS (no throw), so the checker rejects a *literal* `/0` and Eval faults on a non-finite **Select** result; a division buried in a Condition comparison follows JS (`Infinity > 1` = true — arithmetically reasonable). This is the one place goja's semantics differ from the prior hand-rolled evaluator (implementation note).

## Open questions

- Field-mapping wiring (expressions inside workflow `params` / Sensor beyond `input`) — a consumer ADR on this substrate.
- CloudEvent envelope roots for Select mode (`event.time` vs `event.data.…` handling) — fixed in the F69 Sensor ADR that constructs that Resolver.
- Default declaration syntax in the JS/Python contract toolchain — carried by the F65 gate ADR.

## References

- [ADR-0094](0094-workflow-engine-core.md) `when.condition` (Condition-mode consumer contract).
- FEAT-0005 category H (the shared-component rationale) and the condition examples in its capability map.
- Prior art: Argo Events trigger parameterization (`dataKey`/JSONPath — the runtime-failure model this rejects); CEL (the general-language alternative, rejected above).
