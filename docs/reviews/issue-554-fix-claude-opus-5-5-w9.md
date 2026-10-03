# Fix review — issue #554 (split the kvstore config checks out of buildKVStore)

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #554 fix, model: claude-opus-5-5)

Change: branch `fix/w9-i554`, commit 03c1312 `refactor(funcd): split the kvstore config checks out of buildKVStore`, one file (`cmd/funcd/main.go`, +82/-59). The issue is a `kind/task`: its "Done when" is the target, and it adds no `TestIssue554` test, so the revert check does not apply. The person decided on a refactor with no behavior change.

### 🟡 Minor 1 — a pre-existing bucket leak was moved, not fixed  ·  attribution: model
When backup and CDC are both enabled, `kvBackup` opens the gocloud bucket, and then `kvCDC` can fail (no bus configured, or a malformed `kvstore.cdc.retention`). `buildKVStore` returns that error without closing the bucket (`cmd/funcd/main.go`, the `kvCDC` error branch); only the `OpenWithSeamsFor` failure path closes it. `origin/main` has the same leak, so this is not a regression. The defect sits next to the moved code, and the person's decision asks for such a defect to be fixed in its own commit with its own test. The impact is low because the process exits on that startup error.

### 🟡 Minor 2 — the `kvCDC` nil-bus guard is untested  ·  attribution: issue (pre-existing gap)
Mutant m2 (the `theBus == nil` guard in `kvCDC` disabled) survives the whole `cmd/funcd` package test run. No test sets CDC on with a nil bus. The gap exists on `origin/main` too; the refactor only moved the guard. A small test would close it, possibly together with Minor 1.

### ✅ Verified correct (keep it)
- **Done when — limits**: the audit lint config (`scripts/agent/audit.golangci.yml`: funlen 80 lines / 50 statements, gocyclo 20, gocognit 30) reports no finding for `buildKVStore`, `checkKVStoreConfig`, `kvBackup` or `kvCDC` on the branch (the 3 remaining issues in the package are in other functions).
- **Done when — tests unchanged**: `go test -race ./cmd/funcd/...` passes on the branch; no test file changed. The same suite also passes with the `origin/main` version of `main.go` overlaid, so the suite agrees on both sides.
- **No behavior change**: the checks run in the original order (backup target, CDC sink, memory-mode warnings, non-badger rejections, then the plain-Badger fast path, backup, CDC). Every error message, its `buildKVStore` op and both warnings are byte-identical. The memory/non-badger paths both return `kvmemory.New()` as before.
- **Mutants** (overlay, restored): m1 (memory mode reports `false`) is killed by `TestIssue191_MemoryFlagKeepsKVOffDisk` and `TestIssue303_KVSeamsNotSilentlyIgnoredOffBadger`; m3 (non-badger backup rejection disabled) is killed by `TestIssue303_KVSeamsNotSilentlyIgnoredOffBadger`; m2 survives (Minor 2).
- **Shape follows the issue**: the helper names and signatures match the ones the issue proposes; the doc comments are one line each and cite ADR-0067 / ADR-0068.
- **Reuse**: the helpers reuse `parseDurationOr`, `gocloud.Open` and `fault.Invalidf`. Nothing new is reinvented, and the change adds no dependency.
- **Checks**: `go vet ./cmd/funcd/` clean; `golangci-lint run ./cmd/funcd/...` 0 issues; tests green under `-race`.
- **ADRs**: no ADR file is touched, and ADR-0061/0067/0068 behavior is unchanged.
- **Commit shape**: `refactor(funcd):` (fits a task), `Fixes #554`, attribution trailer, one issue per commit.

### Definition of Done
Applicable items 4–11 (items 1–3 cover a regression test, which a refactor task does not add): 7 of 8 hold. Item 4 is partial because a moved guard is not covered by any test (Minor 2, pre-existing).

### Model scorecard
claude-opus-5-5: clean, exact refactor that meets both "Done when" lines with no behavior drift. It missed a small defect next to the code it moved (Minor 1).

### Recommendation
Pass. Merge as is. Optionally, a follow-up commit can close the bucket when `kvCDC` fails and add a test for CDC with a nil bus.
