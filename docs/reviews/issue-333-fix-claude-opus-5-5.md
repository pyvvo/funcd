# Fix review — issue #333 (negative workflow and eventing durations)

- **Change**: branch `fix/i333`, commit `e38d9e0` `fix(funcd): reject negative workflow and eventing durations at startup`
- **Producing model**: claude-opus-5-5
- **Files**: `cmd/funcd/main.go`, `cmd/funcd/main_test.go`
- **Verdict**: **pass** — 0 Blocker, 0 Major, 1 Minor (model). Checklist 11 of 11.

## Summary

`buildOptions` parsed `workflow.defaultStepTimeout`, `workflow.retention`, `eventing.deadletter.retention` and
`eventing.blobPollInterval` with a bare `time.ParseDuration`. A negative value loaded and silently disabled the
setting, and a malformed value failed with a plain `fmt.Errorf`. The fix routes all four keys through
`parseDuration(key, s, def, zeroOK)`, a generalization of the #190 `parseDurationOr` helper. The helper rejects a
malformed or negative value with `fault.Invalid` that names the key, and accepts `0` when `zeroOK` is set.
`parseDurationOr` now delegates to it with `zeroOK=false`, so the kvstore keys behave as before.

## Verification (run)

| Check | Result |
|---|---|
| Regression test without the fix (overlay of the `origin/main` `cmd/funcd/main.go`) | **FAIL** for the issue's reason: every `-3h` subtest gets `An error is expected but got nil`, and every `bogus` subtest gets kind `internal` instead of `invalid` (8 of 8 subtests fail) |
| Regression test with the fix, `go test -race -count=1 ./cmd/funcd/` | `ok` (whole package) |
| `go vet ./cmd/funcd/` | clean |
| `golangci-lint run ./cmd/funcd/` | `0 issues.` |
| Mutant 1: `d > 0 \|\| d == 0 && zeroOK` → `d > 0` (0 rejected on the four keys) | killed by `TestIssue333…/zero` |
| Mutant 2: → `d != 0 \|\| zeroOK` (negatives accepted) | killed by `TestIssue333` and `TestIssue190` |
| Mutant 3: `parseDurationOr` passes `zeroOK=true` (kvstore keys accept 0) | killed by `TestIssue190_InvalidKVDurationRejected` |
| Worktree after the review | at `e38d9e0`, clean |

The overlay was used instead of a whole-commit `git revert`. Reverting the whole commit would also remove the
test, so the overlay is what isolates the fix.

## Blocker

None.

## Major

None.

## Minor

1. **The fault op for the kvstore keys changed from `buildKVStore` to `buildOptions`** (`cmd/funcd/main.go:501`,
   attribution: **model**). The shared helper now hard-codes `"buildOptions"`. `parseDurationOr` is called only
   from `buildKVStore` (`main.go:423`, `:427`, `:452`), and its sibling errors in that function still use
   `"buildKVStore"` (`main.go:421`, `:447`, `:450`). So a bad `kvstore.backup.interval` now reports the wrong
   operation. The key name is in the message, so the error stays actionable, and no test asserts the op. A
   neutral op (for example `"config"`), or an op passed by the caller, would label both call sites correctly.

## ✅ Verified correct — keep

- **The cause is removed, not masked.** The sign check sits at the single parse point, and malformed values now
  return `fault.Invalid` as the issue expects. Both defects in the issue are fixed.
- **Reuse.** The change extends the existing #190 helper instead of adding a second parser. Four
  copy-pasted `if s != "" { time.ParseDuration … }` blocks are gone, and there is no new type or dependency.
  The kvstore semantics (0 is rejected) are preserved, and mutant 3 shows that a test pins them.
- **0 keeps its documented meaning.** The `zero` subtest sets `0s` on all four keys and requires
  `buildOptions` to succeed. This matches the issue's expected behavior and the defaults in
  `internal/platform/config/config.go` and ADR-0094 (`workflow.defaultStepTimeout` 300s,
  `workflow.retention` 720h).
- **The test is table-driven over all four keys**, with both a negative and a malformed value, and asserts
  the kind (`fault.Invalid`) and the key name. It uses `shortDataDir` and memory storage, and it closes the
  executor it gets back.
- **Scope.** Every hunk serves the issue. No test was weakened, and no ADR file was touched.
- **ADRs.** The fix matches ADR-0061 (startup config validation with `fault.Invalid`) and contradicts no
  Contract of ADR-0094, ADR-0118 or ADR-0119. Those ADRs define the defaults and say nothing that permits a
  negative value.
- **Conventions.** It uses `api/fault`, places imports at the top level, adds no YAML and keeps comments
  short (one *why* comment per helper). The commit subject is `fix(funcd):`, the body has `Fixes #333`, and
  the attribution trailer is present.

## Checklist (Definition of Done)

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue333_…` reproduces the issue | yes |
| 2 | Fails on the pre-fix code for the reported reason | yes |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Reverting or mutating the key lines fails a test | yes (3 of 3 mutants killed) |
| 5 | The root cause is fixed | yes |
| 6 | Scope only, no test weakened | yes |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build, vet, lint and tests green (touched package; the group gate runs Linux lint and e2e) | yes |
| 9 | Conventions | yes (one cosmetic Minor) |
| 10 | Reuse, no duplication | yes |
| 11 | Commit shape | yes |

## Recommendation

**Pass.** The fix can go to the PR as it is. The op-label Minor can be fixed in the same PR or as a follow-up.
It does not block the merge.
