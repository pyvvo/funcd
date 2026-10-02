# Fix review — issue #325 (funcdctl inspect splits a layout path at any '@')

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #325 fix, model: claude-opus-5-5)

Change: branch `fix/i325`, commit `57a457d fix(funcdctl): split an inspect ref only at a trailing @<digest>`
(4 files: `cmd/funcdctl/cli.go`, `cmd/funcdctl/cli_test.go`, `internal/artifact/artifact.go`,
`internal/artifact/platform.go`).

### 🔴 Blockers

None.

### 🟡 Major / Minor

None.

Observation (not scored, pre-existing): a mutant that keeps the split but drops the digest
(`return arg[:i], ""`) survives the `inspect` tests, because every test inspects a digest that its own tag
already points to. That return line is unchanged by this fix and the gap predates it; it is not a defect
of this change.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** Reverting the non-test hunks (test kept) makes
  `TestIssue325_InspectKeepsAtSignInLayoutPath` fail with `artifact.Inspect: fetch artifact b:tag: not found`
  — exactly the reported symptom (layout `dir/a`, digest `b:tag`).
- **Passes with the fix** under `-race`, un-skipped.
- **User-visible behavior** is covered end to end: the test drives the real `push` and `inspect` cobra
  commands through `execCLI`, checks both the bare layout ref and the `@<digest>` form, and asserts that no
  layout is created at the path's prefix (the issue's second symptom).
- **Cause, not symptom.** `splitRefDigest` now splits only when the suffix is a digest — the rule its own
  doc comment states and the rule `parseLocalRef` already applies.
- **Mutants.** Revert of the condition → killed. Negated condition (`!artifact.IsDigest`) → killed.
  `strings.Index` instead of `strings.LastIndex` → survives, but is equivalent in practice: an unsplit
  `…@<digest>` ref is still resolved by `parseLocalRef`/the registry ref parser downstream.
- **Reuse, no duplication.** The fix reuses the artifact package's existing digest check
  (`isDigest` → exported `IsDigest`, a wrapper over oras `registry.Reference.ValidateReferenceAsDigest`)
  instead of writing a second one; the three internal callers were renamed accordingly. No new helper,
  type, harness or dependency; the test reuses `writeSchemaFile` and `execCLI`.
- **Scope.** Every hunk serves the issue; the rename hunks in `platform.go` are the mechanical consequence
  of the export. No test weakened or deleted.
- **Conventions.** ADR-0002 idiom holds; no comment bloat (one-line issue comment on the test, the doc
  comment updated for the export); imports unchanged and at top level; no YAML touched.
- **ADRs.** Consistent with ADR-0059 (inspect reads `<ref>[@<digest>]`) and ADR-0031; no ADR file edited.
- **Checks (touched packages).** `go build ./...` ok; `go test -race ./cmd/funcdctl/ ./internal/artifact/`
  ok; `go vet` ok; `golangci-lint` 0 issues. Repo-wide, Linux lint and e2e are left to the group gate.
- **Shape.** `fix(funcdctl):` subject, Cause/Fix/Test body, `Fixes #325`, attribution trailer, one issue
  in the commit.

### Definition of Done

10 of 10 applicable items hold (1 regression test, 2 fails pre-fix, 3 passes with -race, 4 mutants killed,
5 root cause, 6 scope, 7 ADRs, 9 conventions, 10 reuse, 11 shape). Item 8 (host and Linux lint, e2e) is
partly delegated to the group gate and is not counted here.

### Model scorecard

claude-opus-5-5 · issue #325 · fix · pass · 0/0/0 · model-attributed 0 · DoD 10/10.

### Recommendation

Pass. Hand back to `/fix` Step 8; the group gate runs the repo-wide checks.
