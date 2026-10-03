# Fix review: pyvvo/funcd-python issue #24, model claude-opus-5-5

This report reviews a **pyvvo/funcd-python** change: branch `fix/r24-py`, commit `d0495f5`
"fix(shim): enforce the int32 range of the contract format", against
[pyvvo/funcd-python#24](https://github.com/pyvvo/funcd-python/issues/24) ("The Python shim does not enforce
the int32 range that the Node shim enforces"). The governing funcd ADRs are ADR-0058, ADR-0123, ADR-0071,
ADR-0049 and ADR-0050.

## Verdict: pass, 0 blockers, 0 majors, 2 minors (issue #24 fix, model: claude-opus-5-5)

### 🟡 Major / Minor

- **Minor 1: two of the schema walk's behaviors have no test.** Attribution: `model`.
  Evidence: of four mutants on `shim/src/funcd_shim/contract.py`, two survive `tests/test_contract.py`
  (15 passed). The first replaces `[*out.get("allOf", []), _INT32_RANGE]` with `[_INT32_RANGE]`, which drops
  an int32 subschema's own `allOf`. The second removes the `_DATA_KEYWORDS` branch, so the walk rewrites the
  contents of `const`/`enum`/`default`/`examples`. A probe shows that the shipped code handles both cases
  correctly: `{"type":"integer","format":"int32","allOf":[{"multipleOf":2}]}` still rejects 3, and
  `{"const":{"format":"int32"}}` still accepts its own value. Fix: in the regression test, add an int32
  field that has its own `allOf`, and add a `const` or `enum` value that contains `"format": "int32"`.
- **Minor 2: the commit closes nothing.** Attribution: `model`. Evidence: the commit body ends with
  `Refs #24`. The fix skill's commit shape (Step 6) requires `Fixes #24`. The PR body (Step 8) also carries
  `Fixes #N`, so the issue still closes on merge if the PR follows that step. Fix: use `Fixes #24` in the
  commit body.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** With the `origin/main`
  `contract.py` and the new test kept, `test_issue_r24_int32_format_enforces_its_range` fails with
  `AssertionError: {'default': 3000000000} must not satisfy the int32 contract` (`assert []`). This is the
  issue's reproduction: 3000000000 is accepted.
- **The test passes with the fix.** At `d0495f5`, `tests/test_contract.py` reports 15 passed, with nothing
  skipped. The worktree was left clean at `d0495f5`.
- **The revert and key-line mutants fail.** Reverting the source file fails the test (see above). Two
  mutants fail the test: `maximum` set to `2**31`, and the `_SCHEMA_MAPS` branch disabled, which treats the
  property name `default` as data and leaves the field unbounded.
- **The cause is fixed, not masked.** fastjsonschema applies a format to strings only (the cause named in
  the issue). The fix rewrites every int32 subschema to carry `allOf: [{minimum: -2**31, maximum: 2**31-1}]`
  before compiling. This is the remedy the issue proposes. Probes confirm that the bound applies through
  `$ref` into `$defs`, through `anyOf` and on `["integer","null"]` items. `True` is still rejected as a
  non-integer.
- **int64 parity holds.** ajv-formats checks only `Number.isInteger` for int64, and the type already
  enforces that check, so 2**70 passes on both runtimes. The case `number` + int32 (where Node would also
  reject 1.5) cannot reach a worker: the funcd profile gate (`internal/contract/contract.go`, `checkFormat`)
  admits int32/int64 on an `integer` only.
- **Scope.** Only `shim/src/funcd_shim/contract.py` (the transform, its constants and one comment) and one
  new test changed. No test was weakened or deleted. The commit does not touch `version.txt`, `CHANGELOG.md`
  or any package version.
- **ADRs.** The change makes the shim enforce what ADR-0058 already lists (`integer`, `format` int32) and
  what ADR-0123 requires (advertised == enforced). It does not change the funcd <-> shim contract: no env
  var, health endpoint, socket protocol, log format or span changed. No funcd file was edited. ADR-0071
  still holds: fastjsonschema is still the runtime validator, and no dependency was added.
- **Reuse.** fastjsonschema cannot check a format on a number (a format callable runs on strings only), so
  a schema rewrite is necessary. The only other schema walker in the shim, `build.py` `_close_records`,
  mutates in place and covers a different set of keywords, so it cannot be reused for this transform. The
  new walker copies the schema and leaves the delivered schema untouched.
- **Conventions.** Imports are at module top, the type hints are complete, and the comments explain why
  (the ajv-formats parity, why data keywords are skipped). ruff format and ruff lint are clean under
  `just ci`. The subject is a Conventional Commit (`fix(shim):`), with the attribution trailer and one
  issue per commit.
- **Checks.** `just ci` (through the cached pinned dev shell, `TMPDIR=/tmp`) exits 0: ruff, mypy, the shim
  and example pytest suites, `go vet`, `go build` and `go test` are all green.

### Definition of Done

10 / 11 items hold. Miss: item 11, commit shape (`Refs #24` instead of `Fixes #24`), attributed to `model`.
Item 3's `-race` clause does not apply to Python; the test runs un-skipped. Item 4 holds on the key lines
(the revert and two mutants fail the test). The two surviving mutants on secondary lines are Minor 1.

### Model scorecard

Recorded: claude-opus-5-5 on pyvvo/funcd-python#24 (fix) → pass, 0/0/2, 2 model-attributed, DoD 10/11.

### Recommendation

Ship it. Before the PR, optionally add the two missing test cases (an int32 field with its own `allOf`, and
a `const` or `enum` value that contains an int32 format) and change the trailer to `Fixes #24`.
