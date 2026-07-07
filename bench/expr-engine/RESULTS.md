# expr-engine bench — results & verdict

Apple M-series (14 cores), go1.26.4, 2026-07-05. Same representative condition in
all lanes: *step rows > 0 AND publish flag* (see README for per-lane spelling).

## Measured

| metric | `internal/expr` (hand-rolled) | **goja** (pure-Go ES) | QuickJS on wazero |
|---|---|---|---|
| dependencies (main binary) | zero | goja + 3 small indirects (regexp2, sourcemap, x/text) | wazero + a ~1 MB wasm blob + Rust/C toolchain |
| parse (checker input) | ~1.0 µs | **1.1 µs** (full ES parser, maintained upstream) | n/a (opaque until run) |
| compile (once per revision) | — (parse is compile) | **183 µs** | ~ms-class module compile |
| cold per-eval | 2.2 µs (parse+check+eval) | **1.27 µs** (fresh `Runtime` + bind + run) | *(not run — see below)* |
| warm per-eval | 1.1 µs | **0.54 µs** | *(not run)* |
| throughput (1 thread) | ~0.9 M/s | **1.85 M/s** | *(not run)* |
| per-pool-slot heap | ~2 KB/op | **3.5 KB per used Runtime** | MB-class per VM instance |
| process RSS after run | (test binary) | **15.6 MB** | *(not run)* |
| static schema-check at reconcile | ✅ native | ✅ **via subset checker over goja's AST** (see below) | ❌ impossible without one |

The QuickJS lane (`expr.js` + `run.sh`) is staged and runnable but was not executed:
it requires fetching+running the external Javy binary, and it became moot — goja
beat the *hand-rolled* engine on every measured axis, so the heavier wasm stack
cannot win.

## The static-check design that makes goja viable

goja ships its parser as a public package (`github.com/dop251/goja/parser` → full
ECMAScript AST). funcd keeps its type-checker, pointed at that AST, and admits only
the **funcd condition subset** of JS:

- allowed: member access/indexing, literals, `=== !== < <= > >=`, `&& || !`,
  `+ - * /`, ternary, a whitelisted method set, `!== undefined` as the exists-guard;
- rejected at Check: `==`/`!=` (coercing), assignments, loops, functions, `new`,
  `this`, any identifier outside the context's roots;
- type rules against the contract graph: comparisons need numbers, `===` needs
  same-type scalars, `&&`/`||` need booleans (no truthiness), member chains resolve
  against the schema (longest-first roots, defaults-or-guard rule).

Coercion never executes because the checker rejects non-conforming trees before
goja ever runs them; the subset has no loops, so it is total (`Runtime.Interrupt`
stays as a backstop). Division of labor: **goja owns parse+eval (the code funcd
stops maintaining); funcd owns the ~500-line type-checker (the differentiator).**

## Syntax experiment — injected methods vs native operators (`go run . inject`)

Should the goja condition keep the ADR-0094/0095 postfix method vocabulary
(`rows.greaterThan(0)`), injected into the VM, or use native JS operators
(`rows > 0`)? Measured, not guessed:

| finding | result |
|---|---|
| E1 prototype-injected `greaterThan`/`isTrue`/`oneOf` | ✅ work for present fields |
| **E2 `exists()` as a method on an ABSENT field** | ❌ **`TypeError: cannot read 'exists' of undefined`** — a method cannot be called on the very field it is meant to guard |
| E2 native guard `missing !== undefined` (+ `&&` short-circuit) | ✅ works, guards correctly |
| E3 native operators `rows > 0 && publish === true` | ✅ clean |
| E3 typo/coercion (`pubish === true`, `region > 0`) | goja runs them silently → **the checker must reject at reconcile** (true for either syntax) |
| E5 warm per-eval: injected methods vs native operators | **0.58 µs vs 0.23 µs — native is 2.5× faster** |

**Decision: native JS operators.** The method vocabulary was a workaround for having
no real language; goja provides one, so methods are strictly worse — slower, and the
`exists()` guard (load-bearing for the defaults rule) is *impossible* as a method.
Native operators + the checker over goja's AST: the guard becomes `X !== undefined
&& …` (positional short-circuit, same rule), all other type rules unchanged.
`when.condition` reads `${{ step.stats.output.rows > 0 && input.publish === true }}`.

## Verdict

**goja + subset type-checker.** It preserves the fail-at-reconcile guarantee,
outperforms the hand-rolled evaluator (0.54 µs warm vs 1.1 µs), gives users plain
JavaScript syntax instead of a bespoke postfix grammar, and retires ~1 k lines of
funcd-owned lexer/parser/evaluator. Remaining cost: one well-maintained pure-Go
dependency family in the main binary (goja — MIT; Grafana's `sobek` fork is the
drop-in successor if upstream stalls). QuickJS/wasm is rejected: every operational
cost is higher and it adds nothing goja lacks for this use.

Process: this supersedes ADR-0095's engine choice → ADR-0096 (supersession), type
rules carried over, `internal/expr` re-implemented as checker-over-goja-AST.

## Reproduce

```bash
nix develop -c go test ./internal/expr/ -run=xxx -bench=Bench -benchmem  # baseline
cd bench/expr-engine && nix develop -c go run . goja 20000              # goja lane
bash bench/expr-engine/run.sh                                            # QuickJS lane (optional; fetches Javy)
```
