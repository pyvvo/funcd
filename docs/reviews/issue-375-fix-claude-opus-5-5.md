# Fix review — issue #375 (file blob substrate aliases `__0x..__` keys)

- **Change**: branch `fix/i375`, commit `636c1fe` — `fix(blob): refuse file-backend keys that hold a __0x escape sequence`
- **Producing model**: claude-opus-5-5
- **Files**: `internal/blob/gocloud/gocloud.go` (+8/-2), `internal/blob/gocloud/gocloud_test.go` (+36)
- **Governing ADRs**: ADR-0007 §1 (opaque keys stay distinct on every driver), ADR-0002 (conventions)
- **Verdict**: **pass**

## Blockers

None.

## Majors

None.

## Minors

None.

## Verified correct

- **The regression test fails without the fix, for the issue's reason.** `git revert --no-commit 636c1fe` also removes the test, because the fix and the test are in one commit. So the pre-fix code was overlaid instead (`git show origin/main:internal/blob/gocloud/gocloud.go`, `go test -overlay`). The result: `TestIssue375_EscapeSequenceKeysNeverAlias/file` FAILS with `Get(a/__0x2f__b) read the object of a//b: "v:a//b"`, which is the aliasing that the issue reports. `/memory` passes, as expected.
- **It passes with the fix** under `-race`, together with `TestIssue160_UnstorableKeysAreInvalidAndNeverAlias`. The worktree was reset to `636c1fe` and left clean.
- **Cause, not symptom.** The issue names the cause: `checkKey` does not reject a raw escape sequence, fileblob's `escapeKey` leaves a raw `__0x..__` unchanged, and `HexUnescape` decodes every such sequence on List. A read of gocloud.dev v0.46.0 (`blob/fileblob` `escapeKey`/`unescapeKey`, `internal/escape` `HexEscape`/`HexUnescape`) confirms this cause. The fix refuses any key that contains `__0x` on the file backend with `fault.Invalid`, at the same guard and in the same shape as the #160 cases. The memory backend is unchanged (`k.file` gate). The check is slightly broader than the set of valid escapes (`__0xZZ` is also refused), and that is the conservative side of the port promise.
- **Mutants (3, all killed)** on the new lines, each run as a `-overlay` with `-run TestIssue375`:
  1. `strings.Contains` → `strings.HasPrefix`: FAIL (`a/__0x2f__b` is not a prefix match).
  2. The check is guarded to `op == "blob.Put"` only: FAIL (Get still aliases).
  3. `fileEscapePrefix = "__0X"` (wrong token): FAIL.
- **Scope.** Both hunks serve the issue: one constant, one guard, an updated doc comment and one new test. No existing test is weakened.
- **Reuse.** The guard extends the existing `checkKey` (the #160 helper) rather than adding a new one, and it reuses `fault.Invalidf` and the sibling `fileAttrsSuffix` pattern. gocloud's `internal/escape` is not importable, so a local constant is the right call. The test reuses the #160 test's mem/file table shape and does not duplicate a harness.
- **Conventions.** `api/fault` errors, ctx-first, no `any`, top-level imports. The comments state the why (the fileblob behavior) without narration. The naming follows `fileAttrsSuffix`.
- **ADRs.** The fix conforms to ADR-0007 §1. No ADR file was touched.
- **Checks (touched packages).** `go test -race ./internal/blob/...` ok (gocloud, s3gateway). `go vet ./internal/blob/...` is clean. `golangci-lint run ./internal/blob/...` reports 0 issues. A first lint run with the shared lint cache reported two issues in files of a different worktree. That is an `env` artifact of the shared cache, not this change, and a run with a fresh cache was clean. The Linux lint, the repo-wide tests and e2e are left to the group gate.
- **Shape.** The subject is `fix(blob):`, the body has `Fixes #375` and the attribution trailer, and the commit covers one issue.

## Checklist

10 of 10 applicable items hold. Item 8 holds for the host checks on the touched packages, and its Linux and repo-wide parts are deferred to the group gate.

## Recommendation

Pass. Hand back to `/fix` Step 8.
