# Fix review — issue #153 (funclog config block), model claude-opus-5-5

- **Issue**: #153 — the daemon config has no `funclog` block, so ADR-0081's documented keys fail startup.
- **Change**: branch `fix/i153`, commit `4ac5810` `fix(config): accept the funclog block and map it onto the daemon`.
- **Touched**: `internal/platform/config/config.go` (+test), `cmd/funcd/main.go` (+test), `examples/funcdconfig.yaml`.
- **Verdict**: **pass** — 0 Blocker, 0 Major, 1 Minor (model). Checklist 11/11.

## Verification run

| Check | Result |
|---|---|
| Revert check (`git revert --no-commit 4ac5810`, test file restored from HEAD) | `TestIssue153_FunclogConfigBlockLoadsAndMaps` FAILS on all 3 subtests with `json: unknown field "funclog"` — the exact error in the issue |
| With the fix, `go test -race -count=1 ./cmd/funcd ./internal/platform/config` | `ok` both packages |
| `go vet` (both packages) | clean |
| `golangci-lint run ./cmd/funcd/... ./internal/platform/config/...` | `0 issues.` |
| Mutant M1: drop `WithoutFunclog()` on `enabled: false` | killed (`TestIssue153…/disabled`) |
| Mutant M4: drop the `Funclog.Enabled = true` default | killed (`TestScenarioZeroConfigDefaults`) |
| Mutant M2: drop `WithoutFunclogTraces()` on `traces: false` | **survived** |
| Mutant M3: pass `WithFunclog(0, 0)` instead of the configured size/age | **survived** |
| Worktree after the review | at `4ac5810`, clean |

E2E, the Linux lint and the Lima lanes were not run here; the group gate runs them.

## 🔴 Blockers

None.

## 🟡 Majors

None.

## Minor

1. **The traces mapping and the segment size/age pass-through have no test** (`model`). Evidence: mutants M2
   and M3 above survive. The `documented-keys` subtest sets `traces: false`, `segmentMaxBytes` and
   `segmentMaxAge`, but it asserts only that the capture hook is installed (`cmd/funcd/main_test.go`, the
   `require.Equal(t, tc.capture, spy.installed, …)` line). The issue says an operator "cannot disable
   function-log capture or tune it", and only the disable half is proved. The mapping is straight-line code
   that mirrors its neighbours, and `pkg/funcd` already tests the options themselves, so this does not block.
   A follow-up could assert the option effects, for example through the trace-sink presence or a sealed
   segment's size.

## ✅ Verified correct

- **The cause is fixed, not masked**: `Config` gains a `Funclog` struct, so the strict decode accepts the
  block. `buildOptions` maps `enabled`, `segmentMaxBytes`, `segmentMaxAge` and `traces` onto the existing
  options `WithFunclog`, `WithoutFunclog` and `WithoutFunclogTraces`. Both root causes named in the issue
  are fixed.
- **ADR conformance**: the keys match ADR-0081's Config contract and Dependencies table
  (`enabled/segmentMaxBytes/segmentMaxAge/bucket`), and `traces` is ADR-0101's documented
  `funclog.traces` key. The defaults keep zero-config behavior unchanged: capture and traces stay on, and a
  zero size or empty age falls through to the sink defaults, because `internal/funclog/sink.go` and
  `tracesink.go` treat `<= 0` as the default. No ADR file was edited.
- **`bucket`**: no library option exists for it. The fix accepts the key and validates it as
  `omitempty,eq=funcd-system`, the only bucket the sink writes to, and covers that in `TestValidateMatrix`.
  This honors the contract without inventing a new option.
- **A bad duration fails startup** with `parse funclog.segmentMaxAge …`, instead of being silently ignored.
  The `bad-segment-max-age` subtest covers this.
- **Reuse**: the duration parsing copies the inline pattern of its neighbours (`workflow.*`,
  `eventing.*`) and correctly does not use `parseDurationOr`, which swallows errors. The new env names follow
  the `FUNCD_<SECTION>_<KEY>` convention, and two of them are covered in `TestEnvVarsMapToFields`. The test
  spy embeds `runtime.Runtime` and overrides only `SetLogCapture`. No new dependency was added.
- **Conventions**: the example YAML is block style and commented out, like the other optional sections.
  The imports are at the top level, and the comments name their ADRs without restating the code.
- **Scope and shape**: every hunk serves #153. No test was weakened. The commit has a `fix(config):`
  subject, `Fixes #153` and the attribution trailer, in one commit for one issue.

## Notes

- ADR-0083 (Implemented, frozen) says that the daemon config has no `funclog:` YAML block. After this fix,
  that sentence describes the state before the fix. The ADR cannot be edited, so this is recorded here and
  is not a finding (`adr`, not scored).

## Recommendation

Pass. Hand back to `/fix` Step 8. The Minor test gap may be closed in the same PR or in a follow-up.
