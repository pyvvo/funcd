## Verdict: pass — 0 blockers, 0 majors, 3 minors  (issue #129 fix + shim-release pin, model: claude-opus-5-5)

Branch `fix/shim-releases` against `origin/main`: 6bc80a7 (pin funcd-typescript v0.4.1, funcd-python v0.3.2),
69f2959 (`fix(contract)`: push rejects a format outside the ADR-0058 profile, Refs #129), 68a502f (regression
tests through the embedded shims), 3c55434 (the language-repo fix reviews under `docs/reviews/`).

### 🟡 Minor 1 — `countLogBodies` hand-rolls the OTLP-JSONL decode  ·  attribution: model
`pkg/funcd/shim_regression_e2e_test.go` `countLogBodies` splits each `logs/` object on `\n`, runs
`plog.JSONUnmarshaler` per line and walks ResourceLogs → ScopeLogs → LogRecords by hand. The package already
exports that exact per-line decode: `compact.DecodeJSONL` (`internal/funclog/compact/compact.go:312`, used by
`internal/funclog/logread/logread.go:181`), which returns `[]compact.Row` with a `Body` field. Fix: decode with
`compact.DecodeJSONL` and count rows whose `Body` has the prefix; the triple loop goes away.

### 🟡 Minor 2 — #154 has no regression test and no recorded reason  ·  attribution: model
The pin commit lists #154 (funcd-bundle keeps a stem manifest's implicit handler), but 68a502f adds no
`TestIssue154_…` and neither commit body says why. The reason is real: `funcd-bundle` is a Python CLI that funcd
never embeds (only `shim.Extract` is imported); it runs only as a lane build step (`scripts/lanes.yaml:98`,
`uv run --locked funcd-bundle`), so a funcd-side test needs `uv` and a Lima lane, and the behavior is covered
by funcd-python's `test_issue_154_…`. Fix: state this in the PR description's untested list (no code change).

### 🟡 Minor 3 — the Python regression tests skip in CI  ·  attribution: env (not scored)
8 of the 13 tests (`TestIssue81_PooledPython…`, `TestIssue82_Python…`, `TestIssue129_…`, `TestIssue131_…`,
`TestIssue183_…`, `TestIssue188_…`) skip without Python ≥ 3.12 (3.14 for the pool host) plus fastjsonschema.
The flake's dev shell has no Python and the `e2e` job in `.github/workflows/ci.yml` installs none, so in CI
they never run. The skip is documented in the commit body and the tests do fail when an interpreter is present
(below). Follow-up, not this PR: a pinned Python with fastjsonschema in the flake, or a CI step that sets
`FUNCD_PYTHON`.

### ✅ Verified correct (keep it)
- **#129 revert check**: overlaying `origin/main`'s `internal/contract/contract.go` and `schema.go`
  (`go test -overlay`), `TestIssue129_CheckRejectsOutOfProfileFormat` fails in all 10 rejection subtests
  (exit 1); with the fix it passes under `-race`.
- **#129 mutants, 3/3 killed**: (M1) `date` added to the string list → `string_date` fails; (M2) the default
  branch returns nil → `number_double` and `format_without_a_type` fail; (M3) the integer branch admits any
  format → `integer_uuid` fails.
- **ADR-0058 conformance**: the admitted set is exactly the profile table (ADR-0058 lines 192–193: string
  `date-time/uuid/email/uri`, integer `int32/int64`, no format on number). `checkFormat` runs at the top of
  `walk`, so nested properties, array items, union members and nullable lists are all covered; it uses
  `primaryType()` (so `["string","null"]` is a string) and the package's `unsupported` → `fault.Invalid`.
  No committed contract in this repo or in the pinned modules' examples uses another format.
- **Cause, not symptom**: the gate ignored `format` (`schema.go` did not decode it); it now decodes and checks
  it, so an out-of-profile format fails at push (`cmd/funcdctl` and `internal/artifact/bundle.go` both call
  `contract.Check`) instead of at the Python worker's validator compile.
- **Regression tests fail on the old pin and pass on the new**, run through scratch modfiles (`-modfile`, the
  committed `go.mod`/`go.sum` untouched, confirmed with `git diff --quiet`), `-tags e2e`, Python 3.14 with
  fastjsonschema 2.21.2 (the runtime image's version):
  - new pins, `-race`: 13/13 PASS (`ok pkg/funcd 23.0s`).
  - funcd-typescript v0.4.0: the 7 Node tests FAIL for the issues' reasons — #81 1990/2000 records persisted;
    #82 `attrs.args` is `[{}]`; #130 and #132 a 503 `upstream call failed: EOF` (worker died); #133 a bad
    email gets 200; #185 a CloudEvent with no data gets 422; #186 NaN goes out as `{"n":null}` with 200. The
    6 Python tests pass.
  - funcd-python v0.3.0: the 6 Python tests FAIL for the issues' reasons — #81 1978/2000 records; #82
    `attrs.args` missing; #129 the function never reconciles to Ready; #131 503 instead of a 500 JSON reply;
    #183 `refused` is 0; #188 503. The 7 Node tests pass.
  Each test fails only on its own language's old pin, so none is vacuous.
- **No duplication of harness helpers**: `waitReady` is reused from `invoke_e2e_test.go`; `shortDataDir`
  exists only in `cmd/funcd` (another package), so the one here is not a redefinition; the rig is the
  repo's usual explicit `funcd.New` assembly (as `funclog_e2e_test.go`, `pooling_e2e_test.go`) and is the
  first that needs the bucket, both pool shims and a counting logger together. `langmod.PythonShim` is the
  Python counterpart of `NodeShim`/`PoolShim`. The platform uses `shortDataDir`, not `t.TempDir()`.
- **Hygiene**: `just check-hygiene` → `hygiene: clean`; only `go.mod`/`go.sum` name a language-module
  version (the one mention in `docs/reviews/issue-131-…` is in the excluded review history).
- **Checks** (touched packages): `go test -race` on `internal/contract`, `internal/testkit/langmod` and the
  `contract.Check` caller `internal/artifact` → ok; `go vet` (host, and `GOOS=linux` with `-tags e2e`) → ok;
  `golangci-lint` host and Linux, with and without `--build-tags e2e` → 0 issues; `gofmt -l` → clean.
- **Conventions**: top-level imports, no YAML added, comments explain the why (issue and ADR), not the what.
  No Accepted/Implemented ADR edited.

### Definition of Done
10 / 11 items hold. Miss: item 10 (reuse) — Minor 1, attribution model. Item 11 holds for the commits
(`fix(contract):` and `fix(deps):` subjects, trailers, one topic per commit); they say `Refs #N` because #81
and #82 close only with both halves — the PR description must carry `Fixes #N` for each issue it closes.
Lanes not run (out of scope for this gate).

### Model scorecard
Not recorded here (the orchestrator records the ledger row): claude-opus-5-5 on issue #129 (fix) → pass,
0/0/3, 2 model-attributed, DoD 10/11.

### Recommendation
Ready to open the PR. In its description, list #154 as untested with the reason above, and say that the
Python tests skip without an interpreter. Minor 1 can be done in the same PR or as a follow-up.
