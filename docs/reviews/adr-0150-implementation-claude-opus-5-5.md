# ADR-0150 implementation review — claude-opus-5-5 (loop 1)

**Verdict: pass** for the language-repo work. 0 Blockers, 0 Majors, 0 Minors. Review checklist 4/5. Item 5 (funcd
pins the two releases) waits for the releases. Plan step 3 puts it in the funcd PR that follows them, so it is not
a defect of this work. Do not stamp `Implemented` until that PR is in and its gate is green.

## Scope reviewed

| Repo | Branch | Diff vs `origin/main` |
|---|---|---|
| pyvvo/funcd | `feat/adr-0150-int64-range` | no commits (the pin PR comes after the releases) |
| pyvvo/funcd-typescript | `feat/adr-0150-int64-range` | 1 commit: `shim/src/contract.ts` (+4), `shim/test/contract.test.ts` (+65), rebuilt `shim/shim.mjs`, `shim/pool.mjs` (+1 each) |
| pyvvo/funcd-python | `feat/adr-0150-int64-range` | 1 commit: `shim/src/funcd_shim/contract.py` (+19/−13), `shim/tests/test_shim.py` (+59) |

Both language branches are based on the current `origin/main`, and every tree was clean after every check.

## Verification run (captured)

| Check | Result |
|---|---|
| funcd-typescript `scripts/agent/d just ci` (install, biome lint, tsc, tests, build, Go vet/build/test) | exit 0. Shim tests: 91 pass, 0 fail, all 4 `scenario int64-…` tests ✔. The tree is clean after `build`, so the committed `shim.mjs`/`pool.mjs` match the source. |
| funcd-python `scripts/agent/d just ci` (ruff format/check, mypy, pytest for all 7 projects, Go check) | 1st run: exit 1. Two tests outside this diff (`test_funclog.py::test_uds_channel_captures_lines`, `test_tracespan.py::…[uds]`) failed with `AF_UNIX path too long` under the long worktree TMPDIR (**env**). Rerun with a short TMPDIR: exit 0. shim 180 passed, bundle 20, examples 7/2/2/2/10. All ruff checks pass; mypy reports no issues. |
| funcd with a local `go.work` over both language worktrees (`go list -m` resolved both modules to the worktrees) | `go build ./...` OK; `go vet ./...` OK; `go test -race -count=1` passed: `cmd/funcd` ok, `cmd/funcdctl` ok, `internal/testkit/langmod` ok, `internal/contract` ok (the packages that import or read the language modules; `tests/e2e` excluded as instructed) |
| funcd lint | `go tool golangci-lint run ./...`: 0 issues, exit 0 |
| funcd Linux | `GOOS=linux go vet ./...`: exit 0. `GOOS=linux golangci-lint run ./...`: 0 issues, exit 0 (run with the host-built tool binary, because `GOOS=linux go tool …` builds the tool itself for Linux and cannot run it: **env**) |
| `go.work` | created only for the review, ignored by git, removed afterwards |

### Mutants (each in an isolated copy; the work was not edited)

| Side | Mutant | Result |
|---|---|---|
| TS | T1: delete the `addFormat('int64', …)` override (the code before the fix) | **killed**: over, under and output fail |
| TS | T2: `Number.isSafeInteger(n)` → `Math.abs(n) <= 2 ** 53` (off by one) | **killed**: over and under fail |
| TS | T3: register the format with `type: 'string'` (never applied to numbers) | **killed**: over, under and output fail |
| Py | P1: drop the `int64` entry from `_INT_RANGES` (the code before the fix) | **killed**: 3 of 4 scenario tests fail |
| Py | P2: int64 bounds ±2**53 (off by one) | **killed**: over and under fail |
| Py | P3: int64 bounds −2**63..2**63−1 (the OpenAPI meaning) | **killed**: 3 of 4 fail |

6 of 6 mutants were killed. The safe-max test passing under every mutant is expected: each mutant only widens the
range.

### Direct boundary probe (output side, which the scenario tests cover only at 2^60)

Each shim loaded a contract with `n: {type: integer, format: int64}` on both sides. 9007199254740991 and
−9007199254740991 were valid on input and output. 9007199254740992 and −9007199254740992 were invalid on input and
output, on both runtimes. On Python, int32 still accepts 2**31−1 and −2**31 and rejects 2**31 and −2**31−1.

## Contracts

- **Node** (`shim/src/contract.ts:35`): `ajv.addFormat('int64', { type: 'number', validate: (n: number) =>
  Number.isSafeInteger(n) })` is the ADR's Contracts line verbatim. It comes right after `addFormats(ajv)` and before
  `compileSide`, which is the only place that compiles. Input and output both go through `compileSide`, and the
  rebuilt `pool.mjs` carries the same line. The module-baked fallback in `shim/src/build.ts` is out of scope by the
  ADR and was left untouched.
- **Python** (`shim/src/funcd_shim/contract.py:48-51, 64-81, 89`): `_INT_RANGES` matches the ADR exactly.
  `_with_int_ranges` replaces `_with_int32_range` and appends the range to `allOf` for any subschema whose `format` is
  a string key of `_INT_RANGES`. The `isinstance` guard keeps a non-string `format` from raising. `_compile_side`
  (used solo and in the pool) compiles the rewritten schema. The data keywords (`const`/`enum`/`default`/`examples`)
  are still copied as they are.
- **funcd**: no type or code changes, as the ADR requires.

## Review checklist

| # | Item | Status |
|---|---|---|
| 1 | Node `int64` validates with `Number.isSafeInteger`, registered before any compile, on input and output | ✅ code plus T1–T3 plus the probe |
| 2 | Python `int64` carries `minimum: -(2**53 - 1)` / `maximum: 2**53 - 1`; `int32` bounds unchanged | ✅ code plus P1–P3 plus the int32 probe; `test_issue_r24_int32_format_enforces_its_range` green |
| 3 | ±(2^53 − 1) passes and ±2^53 fails on both runtimes, input 422 and output 500 | ✅ input: scenario tests over HTTP. Output: the 2^60 → 500 tests plus the direct ±2^53 probe |
| 4 | Each scenario has one named, passing test in each language repo | ✅ TS `scenario int64-safe-max-accepted: …` (and so on, as plan step 1 names them); Py `test_scenario_int64_*` (as plan step 2 names them); 8/8 pass |
| 5 | funcd `go.mod` pins the two releases; no other funcd code changes | ⏳ pending the releases (plan step 3). "No other funcd code changes" holds: the funcd branch has no diff. |

## Scenarios → tests

| Scenario | funcd-typescript (`shim/test/contract.test.ts`) | funcd-python (`shim/tests/test_shim.py`) |
|---|---|---|
| int64-safe-max-accepted | ±MAX_SAFE at the validator (input and output) and over HTTP. The handler sees the exact values, and the echoed body text is byte-identical | `test_scenario_int64_safe_max_accepted`: HTTP 200 for both values, the handler sees exactly ±(2**53−1), and the output side is validated |
| int64-over-safe-range-rejected | 2^53 and 2^70 at the validator; `9007199254740992`, `9007199254740993` and `1180591620717411303424` as raw body text → 422, 0 handler calls | 2**53, 2**53+1 and 2**70 → 422, 0 handler calls |
| int64-under-safe-range-rejected | −2^53 at the validator and over HTTP → 422, 0 calls | −2**53 → 422, 0 calls |
| int64-output-over-safe-range-is-500 | the handler returns `{ n: 2 ** 60 }` (a JS number) → 500 | the handler returns `{"n": 2**60}` → 500 |

## safeBeforeFuncd claims

- **pyvvo/funcd-typescript: true (verified).** The change stays inside the shim's validator. It adds no config key and
  does not change the funcd ↔ shim protocol. funcd stays on its current pins (`v0.4.4`) until it bumps, so a release
  changes nothing in funcd before then. The `go.work` run shows the bump itself is green in the non-e2e funcd
  packages. `origin/main` does not contain ADR-0147's `invoke.maxNestedInFlight` (grep: none), so the pin-order
  clause of plan step 3 does not apply to the next tag. No example contract uses `int64` with values beyond the range;
  the only hit is a comment about a Workflow schedule interval in `examples/workflow/schedule-source.yaml`, which is
  not a function contract.
- **pyvvo/funcd-python: true (verified).** The same reasoning applies: only the validator changes, and funcd pins
  `v0.3.5` until it bumps. The existing `int64` tests (`tests/test_contract.py:89` and `tests/test_build.py:273`)
  use 2**40, which is inside the range, and stay green. No example uses large `int64` values.

## ✅ Verified correct — keep it

- Both shims enforce one identical range, and output enforcement runs through the same compile path as input, so
  the two cannot drift.
- The TS HTTP tests send the over-range values as raw body text. This is the only way to test 2^53 + 1 on Node,
  where a JS number literal would already be rounded. The safe-max test checks that the echoed text is
  byte-identical, which proves the value reaches the handler exactly.
- Python generalizes the int32 precedent (funcd-python#24) instead of adding a second walker. The int32 behavior is
  unchanged, and its regression test is still green.
- The bundles were rebuilt and are reproducible: `just ci`'s build step left the tree clean.
- The comments state only the why (the ADR, why ajv-formats is not enough, why registration must come before any
  compile). The commits are scoped `fix(shim)`, reference pyvvo/funcd#518 and carry the attribution trailer.

## Notes (not scored)

- **process**: the ADR is still `Accepted`, and the F88 cell reads `int64 range: accepted`. Under plan step 3, the
  `Reviewing` bump, the F88 status, the `go get` of both new tags and `Fixes #518` belong in the funcd PR after the
  releases. That PR then needs its own gate (`just ci`, the Linux checks and the Lima lanes) before anything is
  stamped `Implemented`.
- **env**: the funcd-python suite has two tests outside this diff that bind a Unix socket under pytest's `tmp_path`.
  They fail whenever TMPDIR is long (the same Unix socket-path limit as #41). This fragility existed before this
  change and could be filed separately.
- The scenario tests check the output side only at 2^60, which is what the ADR's scenario specifies. The exact ±2^53
  boundary on output was checked with the direct probe above. Input and output share one compile function, so no
  test change is needed.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0150",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 0,
  "model_attributed": 0,
  "dod_passed": 4,
  "dod_total": 5,
  "report": "docs/reviews/adr-0150-implementation-claude-opus-5-5.md",
  "notes": "both shims match the Contracts verbatim; 8/8 scenario tests pass (4 TS, 4 Py); both language just ci green (Py needed a short TMPDIR: 2 unrelated UDS tests fail on long paths, env); funcd with go.work over both branches: build/vet/race tests in cmd/funcd, cmd/funcdctl, langmod, contract plus lint and Linux vet/lint green; 6/6 mutants killed; output ±2^53 boundary probed directly; safeBeforeFuncd true for both repos (no ADR-0147 key on ts main); checklist item 5 (funcd pins the releases, F88 row, Reviewing bump) pending the releases (process)"
}
```
