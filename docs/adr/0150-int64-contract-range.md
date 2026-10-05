# ADR-0150: The int64 contract format — the JSON safe-integer range on every runtime

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: contract, shim, validation, json
- **Realizes**: [FEAT-0001/F88](../feat/0001-feat-v1.1.md) (runtime-compiled I/O validators — advertised == enforced)
- **Refines**: [ADR-0058](0058-contract-codegen-from-code-types.md)'s supported-type profile: it lists
  `integer (format: int32/int64)` without a range; this ADR fixes the range `int64` enforces. Nothing else in
  ADR-0058 changes.
- **Relates to**: [ADR-0123](0123-runtime-compiled-io-validators.md) (advertised == enforced) ·
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (the shims live in the language repos)

## Context & Need

A contract field `{"type": "integer", "format": "int64"}` accepts any integer on both shims (#518): 2^70 passes.

- Node (funcd-typescript `shim/src/contract.ts`): ajv-formats checks `int64` with `Number.isInteger` only.
- Python (funcd-python `shim/src/funcd_shim/contract.py`): `int64` maps to an empty pattern, and fastjsonschema
  applies a format to strings only. `int32` is bounded by `_with_int32_range` (funcd-python#24); `int64` is not.

The full signed 64-bit range cannot be enforced while a Node handler receives a JS `number`: `JSON.parse` reads every
number as a double, so an integer above 2^53 is rounded before the handler sees it (9007199254740993 arrives as
…992). Python keeps it exact. A range one runtime holds exactly and the other holds rounded is not one contract.

The purpose: `int64` means the same on every runtime, no out-of-range JSON integer passes, and every accepted
integer is exact.

## Scenarios

- `scenario: int64-safe-max-accepted` — Given a function whose input contract has an `int64` field `n`, When it is
  invoked with `n` = 9007199254740991 (2^53 − 1), or −9007199254740991, Then the handler runs and receives exactly
  that value, on `nodejs22` and `python314`.
- `scenario: int64-over-safe-range-rejected` — Given the same function, When it is invoked with `n` =
  9007199254740992 (2^53), 9007199254740993, or 2^70, Then the call fails with 422 and the handler never runs, on
  both runtimes.
- `scenario: int64-under-safe-range-rejected` — Given the same function, When `n` = −9007199254740992, Then 422 on
  both runtimes.
- `scenario: int64-output-over-safe-range-is-500` — Given a function whose output contract has an `int64` field,
  When the handler returns 2^60 (a JS `number` on Node) in it, Then the call fails with 500, on both runtimes.

## Scope

In: the range the `int64` format enforces in both shims' runtime-compiled validators, input and output.

Out:
- the `int32` range (unchanged);
- a fractional JSON number that the parser rounds to an integer (|x| ≥ 2^52, or more than ~17 significant digits):
  a `type: integer` limit shared by both runtimes and every integer field;
- the module-baked fallback validators, used only when `FUNCD_CONTRACT_PATH` is unset (dev/legacy; int32 has the
  same gap);
- funcd's Go `CheckInput` (`api/types/v1alpha1/contract_check.go`), which checks JSON types only and no format;
- a new format (for example a string-encoded 64-bit integer).

## Constraints & Decision drivers

- ADR-0058 Decision 2: the profile maps cleanly to every runtime, so a contract never advertises a shape a peer
  runtime cannot honor. ADR-0123: the validator compiles the exact advertised schema.
- A handler receives a JS `number` on Node (ADR-0058 maps `integer` to `number`; `funcdctl types` follows it);
  integers are exact only within ±(2^53 − 1).
- RFC 7493 (I-JSON) §2.2: an integer outside ±(2^53 − 1) is not interoperable in JSON.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **`int64` = ±(2^53 − 1) on both shims** | Identical on every runtime; every accepted integer is exact; survives every float64 hop; matches I-JSON | A real 64-bit value above 2^53 must travel as a string | **chosen** |
| `int64` = −2^63..2^63 − 1, checked on a JS `number` | Matches the OpenAPI meaning of `int64` | Node checks an already-rounded double: a changed value passes, and the runtimes disagree near the bounds | rejected |
| `int64` = −2^63..2^63 − 1, Node reads the source text (`JSON.parse` reviver `context.source`) and hands int64 fields over as BigInt | Exact full range on Node too | Breaks ADR-0058's `number` mapping and `funcdctl types`; needs a BigInt serializer on output; other hops still round (Go `interface{}` in `internal/expr/eval.go`) | rejected |
| `int64` is an unchecked annotation | No code change | The contract claims a check that never runs (breaks ADR-0123) | rejected |
| Bound Node only, leave Python exact to 2^63 − 1 | Python keeps the full range | The same contract accepts different values per runtime (breaks ADR-0058 Decision 2) | rejected |

## Decision

1. **`int64` enforces −(2^53 − 1) ≤ n ≤ 2^53 − 1** on every runtime, in the input and the output validator. Outside
   it, input fails with 422 and output with 500, as for any contract mismatch (ADR-0058).
2. **Node**: the shim overrides ajv-formats' `int64` with `Number.isSafeInteger`. A JSON integer outside the range
   parses to a double of magnitude ≥ 2^53, which is never a safe integer, so it is refused.
3. **Python**: the shim bounds `int64` the way it bounds `int32` today, with `minimum`/`maximum` added before
   compiling. Python could hold the full range exactly; it is bounded anyway so a contract means the same in
   both languages.
4. **A value beyond the range is a string.** A contract that carries large IDs (a snowflake, a database
   `bigint`) declares the field `{"type": "string"}`, optionally with a `pattern`.

## Temporary workarounds

None.

## Contracts

No funcd type changes. The two shim changes:

```ts
// funcd-typescript shim/src/contract.ts — after addFormats(ajv) and before any ajv.compile on that instance
// (Ajv caches a format's function per name on first compile).
ajv.addFormat('int64', { type: 'number', validate: (n: number) => Number.isSafeInteger(n) });
```

```python
# funcd-python shim/src/funcd_shim/contract.py — replaces _INT32_RANGE and _with_int32_range
_INT_RANGES = {
    "int32": {"minimum": -(2**31), "maximum": 2**31 - 1},
    "int64": {"minimum": -(2**53 - 1), "maximum": 2**53 - 1},
}

def _with_int_ranges(schema: Any) -> Any:
    """Return a copy of *schema* in which every int32/int64 subschema also carries ``allOf: [<its range>]``."""
```

The contract an author writes is unchanged:

```yaml
input:
  type: object
  properties:
    count:
      type: integer
      format: int64
    orderId:
      type: string
      pattern: ^[0-9]+$
  required:
    - count
    - orderId
```

| Consumes | Exposes |
|---|---|
| The ADR-0059 contract blob (`FUNCD_CONTRACT_PATH`) | 422 (input) / 500 (output) for an `int64` value outside ±(2^53 − 1) |

## Implementation plan

1. **funcd-typescript**: the `addFormat` override in `shim/src/contract.ts`; tests in `shim/test/contract.test.ts`,
   one per scenario titled `'scenario int64-safe-max-accepted: …'` (and so on), validator-level and through the
   shim's HTTP app (the output test returns a `number`); `just build` and commit `shim/shim.mjs` and `shim/pool.mjs`.
   Release.
2. **funcd-python**: `_with_int_ranges` in `shim/src/funcd_shim/contract.py` (int32 behavior unchanged); HTTP-level
   tests in `shim/tests/test_shim.py` (its `serve`/`post` helpers), one per scenario named
   `test_scenario_int64_safe_max_accepted` and so on; the existing int32 test stays green. Release.
3. **funcd**: one PR that `go get`s both new tags, carries `Fixes #518`, and updates the F88 row to
   `[ADR-0123] (+ [ADR-0150] int64 range)` with status `runtime validation: implemented · int64 range: <status>`
   (precedent F13, F57); `just ci` green; the Lima lanes green. Pin ordering (per ADR-0147's three-step
   rollout): if the funcd-typescript tag carries ADR-0147's `invoke.maxNestedInFlight` in
   `examples/fn-to-fn/funcdconfig.yaml`, this PR merges only after ADR-0147's funcd PR that adds that config key;
   otherwise the strict config decoder rejects the staged example config and the fn-to-fn lane fails.
4. Definition of done: each scenario has one passing test in each language repo; funcd pins both releases.

## Review checklist

- [ ] Node: `int64` validates with `Number.isSafeInteger`, registered before any compile, on input and output.
- [ ] Python: `int64` subschemas carry `minimum: -(2**53 - 1)` and `maximum: 2**53 - 1`; `int32` bounds unchanged.
- [ ] ±(2^53 − 1) passes and ±2^53 fails on both runtimes, input (422) and output (500).
- [ ] Each scenario has one named, passing test in each language repo.
- [ ] funcd's `go.mod` pins the two releases; no other funcd code changes.

## Consequences

- Positive: one meaning of `int64` everywhere; no out-of-range JSON integer passes, and every accepted value is
  exact, including through funcd's own float64 hops (the workflow expression evaluator).
- Negative: a Python function that returns, or a caller that sends, an `int64` value above 2^53 now gets 500 or 422;
  such values must move to a string field.
- Risks accepted: `int64` no longer matches the OpenAPI meaning of the name; this ADR is the reference.

## Open questions

None.

## References

- Issue [#518](https://github.com/pyvvo/funcd/issues/518); precedent pyvvo/funcd-python#24 (int32 range)
- RFC 7493 (I-JSON) §2.2; ECMAScript `Number.isSafeInteger`; `JSON.parse` reviver `context.source` (Node 22)
- ajv-formats `int64` (`Number.isInteger` only); fastjsonschema formats apply to strings only
