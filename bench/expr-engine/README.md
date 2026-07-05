# expr-engine bench — funcd expression engine on goja

This benchmark decided how funcd evaluates workflow conditions (`when.condition`)
and Sensor projections: a **hand-rolled pure-Go typed evaluator** versus embedding a
**JavaScript engine**. It measured both, on the same representative condition
(*step rows > 0 AND publish flag*), and settled two questions:

1. **Which engine?** → **goja** (`github.com/dop251/goja`, pure-Go ECMAScript). It
   beat the hand-rolled evaluator on every axis *and* lets funcd keep static
   type-checking by walking goja's public parser AST at reconcile. QuickJS-on-wazero
   was assessed and rejected — heavier on every axis, nothing goja lacks. (That lane
   has been removed from this module now that the question is answered; see the
   RESULTS.md history.)
2. **Which syntax?** → **native JS operators** (`rows > 0 && publish === true`), not
   an injected method vocabulary — because `exists()` cannot be a method on an absent
   field, and native operators are 2.5× faster. See the `inject` lane.

`RESULTS.md` holds the numbers and the verdict.

## Lanes

```bash
# perf: parse (AST) / compile / cold+warm per-eval / per-Runtime heap / RSS
nix develop -c go run . goja 20000

# design: injected prototype methods vs native operators (the exists() finding)
nix develop -c go run . inject 200000
```

## Baseline (the hand-rolled engine it replaces)

```bash
nix develop -c go test ./internal/expr/ -run=xxx -bench=Bench -benchmem
```

## Why a separate module

Like `bench/badger`, this is its own Go module so its evaluation dependency (goja)
is measured in isolation. If funcd adopts goja in the main binary, the dependency
moves there deliberately, on the evidence in RESULTS.md.
