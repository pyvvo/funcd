## Verdict: pass — 0 blockers, 0 majors, 2 minors  (pyvvo/funcd issue #129 fix, model: claude-opus-5-5)

This review covers a **pyvvo/funcd-python** change: branch `fix/129-py`, commit `2a82cfa`
`fix(shim): compile the uuid, int32 and int64 contract formats in the worker`
(`git diff origin/main...HEAD`: `shim/src/funcd_shim/contract.py` +10/−1, `shim/tests/test_contract.py` +30).

The issue: the ADR-0058 profile lists `string` + `format: uuid` and `integer` + `format: int32/int64`, the push
gate accepts them, but the worker compiles the delivered contract with `fastjsonschema.compile(schema)` and its
default format table, which has none of the three. The worker fails closed at warm-up (`ContractError`, exit 3)
and the Function goes to Failed / ShapeInvalid. The fix passes a `_PROFILE_FORMATS` table
(`uuid` regex, `int32`/`int64` empty pattern) to `fastjsonschema.compile(..., formats=...)` in `_compile_side`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **The uuid pattern's end anchor is untested** · attribution: `model` · evidence: mutant M2 removed `\Z` from the
  uuid pattern and `tests/test_contract.py` stayed green (`14 passed`). fastjsonschema applies a string format with
  `REGEX_PATTERNS[...].match(...)` (`fastjsonschema/draft04.py` `_generate_format`), so `\Z` is the only thing that
  rejects a valid uuid followed by trailing characters. The `bad` case (`"123e4567-e89b-12d3-a456"`) is a prefix
  and cannot catch it. Fix: add a `bad` value such as a valid uuid with a trailing suffix to the parametrized test.
- **The build-time compile path keeps the default format table** · attribution: `issue` (out of the issue's stated
  scope, not scored) · evidence: `shim/src/funcd_shim/build.py` `_validator_source` calls
  `fastjsonschema.compile_to_code(schema)` without `formats`. A probe in the shim venv:
  `compile_to_code({"type":"string","format":"uuid"})` → `JsonSchemaDefinitionException Unknown format: uuid`, while
  `compile_to_code(..., formats=contract._PROFILE_FORMATS)` compiles; a pydantic `uuid.UUID` field emits
  `"format": "uuid"`. So `funcd_shim.build` (public API, the `build` extra, push-box only, never in a worker) still
  rejects a pydantic `uuid.UUID` contract. The issue names only the worker path (`contract.py`) as the root cause,
  and this path fails at build time, not silently, so the fix is correct to leave it. Fix: a follow-up issue that
  makes `build.py` share the same format table.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 2a82cfa`, then the
  test file restored from HEAD (a plain revert also removes the test): `-k issue_129` → `3 failed, 1 passed`, each
  failure `ContractError ... failed to compile: Unknown format: uuid|int32|int64`, the issue's exact worker error.
  The fourth test (`duration` still fails closed) passes before and after, as a guard should.
  `git reset --hard 2a82cfa` restored the worktree clean.
- **It passes with the fix:** `-k issue_129` → `4 passed`, none skipped.
- **Mutants:** M1 (uuid pattern → `""`) → `1 failed`; M3 (`formats=_PROFILE_FORMATS` → `use_formats=False`) →
  `2 failed`; M2 survived (Minor above). Removing a table entry is covered by the revert run.
- **Cause, not symptom:** the defect is the missing format definitions, and the fix supplies them at the one worker
  compile site. Fail-closed is kept: any other unknown format (e.g. `duration`, out of profile) still raises
  `ContractError`, and a test pins it. No retry, no swallowed error, no `use_formats=False`.
- **int32/int64 with the empty pattern is sound:** `generate_format` wraps the check in
  `if isinstance(value, str)`, so a number format is never applied to an integer; `type: integer` still does the
  integer check (the test's `"7"` and `1.5` are rejected). The empty pattern only registers the name.
- **Scope:** both hunks serve the issue; no test was weakened or deleted; no version, `CHANGELOG.md` or lockfile edit.
- **Reuse:** it uses fastjsonschema's own `formats=` hook instead of a hand-rolled check or a new dependency, and
  keeps the existing `_compile_side` wrapper and the `_write` test helper.
- **Conventions (repo `CLAUDE.md`):** ruff format and lint clean, imports at the top, one three-line comment that
  explains why (fastjsonschema checks formats on strings only), stdlib-only shim preserved.
- **ADRs:** conforms to ADR-0058 (the profile table lists exactly uuid and int32/int64; date-time, email and uri are
  already in fastjsonschema's defaults) and ADR-0123 (warm-up compile with `fastjsonschema.compile`, fail-closed on
  a compile error, Decision 5's intent that an in-profile schema compiles in the worker). It does not touch the
  funcd ↔ shim contract (no `FUNCD_*` env, health endpoint, invoke socket, log capture or trace change), so no
  funcd ADR is needed. No funcd file was edited.
- **Checks:** `just ci` (through the cached pinned dev shell, `TMPDIR=/tmp`) → exit 0: ruff clean on every project;
  mypy and pytest green (shim 91 passed, bundle 11, each example 2); `go vet`, `go build`, `go test` of the embed
  package ok; the tree is clean afterwards. (`-race` does not apply to Python.)
- **Commit shape:** `fix(shim):` Conventional Commit subject, the body names the cause and the tests, the
  attribution trailer is present, one issue in one commit. It says `Refs pyvvo/funcd#129` instead of `Fixes`: right
  here, because the issue lives in funcd and closes only when funcd bumps the pinned module (and the issue's second
  facet, the push gate accepting out-of-profile formats such as `duration`, is funcd-side).

### Observations (not findings)

- The Node shim compiles with `new Ajv({ strict: false })` and no `ajv-formats`, so it ignores `format` entirely:
  after this fix Python rejects a malformed uuid that Node accepts. Python is the side that matches ADR-0058's
  "full validation (formats …)"; the Node gap belongs to funcd-typescript.
- Neither runtime enforces the 32-/64-bit range for `int32`/`int64`; ADR-0058 lists the formats but does not define
  range enforcement. If range checking is wanted, that is a funcd ADR question.

### Definition of Done

11 / 11 items hold (the fix checklist). The anchor survivor is recorded as a Minor test gap: the revert and the
mutants on the fix's key lines (the `formats=` argument and the table entries) all fail a test.

### Model scorecard

Recorded: claude-opus-5-5 on pyvvo/funcd issue #129 (fix, pyvvo/funcd-python) → pass, 0/0/2, 1 model-attributed,
DoD 11/11.

### Recommendation

Ship it. Optionally add the trailing-suffix uuid case before the PR. File a follow-up for `funcd_shim.build` so the
build-time compile path shares the format table. The funcd side (pin bump, and the push gate rejecting
out-of-profile formats) closes the issue.
