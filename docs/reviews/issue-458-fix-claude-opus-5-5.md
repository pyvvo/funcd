# Fix review — issue #458 (claude-opus-5-5)

- **Issue**: #458 — the `provider.Runtime` interface doc and the `provider` package doc say Teardown only stops the engine.
- **Change**: branch `fix/i458`, commit `12cf090` `fix(provider): say Teardown removes every engine replica in the Runtime docs`.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Counts**: 0 Blocker · 0 Major · 0 Minor (0 model-attributed)
- **Fix checklist**: 11 / 11

## Summary

The `Runtime` interface comment (`internal/provider/runtime.go`) and the package doc
(`internal/provider/spec.go`) now say that Teardown "stops and removes every engine replica" and removes any
programmed route. This matches the method's own comment, which #373 already updated. The issue's "Done when"
asks for exactly this. A new test, `TestIssue458_TeardownDocsSayReplicasAreRemoved`, parses the embedded sources
and requires the phrase in all three comments: the interface doc, the package doc and the method doc.

## Verification run

| Check | Result |
|---|---|
| Literal `git revert --no-commit 12cf090` | removes the test as well, so the run reports `no tests to run` (the test and the fix are in one commit) |
| Regression test without the fix: `origin/main` `runtime.go` + `spec.go` overlay (`go test -overlay`) | **FAIL**: the Runtime interface doc still says "Teardown stops the engine … and removes any programmed route", which is the issue's reason |
| With the fix, `-race` | `./internal/provider/...` ok |
| Mutant 1: interface doc back to "stops every engine replica" | FAIL (Runtime interface) |
| Mutant 2: package doc back to "stops the engine" | FAIL (package spec.go) |
| Mutant 3: method doc at `runtime.go:161` drops "and removes" | FAIL (engineRuntime.Teardown) |
| `go vet ./internal/provider/...` | clean |
| `golangci-lint run ./internal/provider/...` | 0 issues |
| Worktree after the revert check | reset to `12cf090`, clean |

Not run here by design: repo-wide tests, the e2e suite, Linux lint and Lima lanes. The group gate runs them.

## Findings

### Blocker
None.

### Major
None.

### Minor
None.

## Verified correct (keep)

- **Cause, not symptom.** The issue names the cause: the #373 fix left `runtime.go:21-22` and `spec.go:13-14` stale.
  The commit edits exactly those two comments, and their wording now matches the method comment and ADR-0143 (to retire
  an engine is to stop and remove it).
- **Scope.** The change touches three files: two comment edits and one new test file. It changes no code, and it
  weakens or deletes no test. The only other "Teardown stops the engine" comment is on `internal/catalog/devengine`.
  That comment describes the dev subprocess runtime, which only stops a process, so it is correctly left alone.
- **Reuse.** The test follows the established doc-test precedent: an embedded source parsed with `go/parser`, as in
  `internal/function/doc_test.go` (#352) and `cmd/funcdctl/dev_doc_test.go` (#321). The embed lets an overlay revert
  check see the old source. No shared helper exists for this pattern, and the test adds no new dependency.
- **Guards the drift that caused the issue.** The test also pins the method comment. If anyone later edits only one of
  the three comments, the test fails.
- **Conventions.** Top-level imports, a single "why" comment on the embed, and whitespace-normalised matching, so
  rewrapping the comment does not break the test. ADR-0087 and ADR-0143 are not edited and are not contradicted.
- **Shape.** The subject is `fix(provider):`, the body has Cause/Fix/Test, `Fixes #458` and the attribution trailer.
  The commit covers one issue.

## Recommendation

Pass. The fix is ready for the group PR.
