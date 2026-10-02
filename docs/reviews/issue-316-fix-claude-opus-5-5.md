## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #316 fix, model: claude-opus-5-5)

Change: branch `fix/i316`, commit `0872ebb` — `fix(funcdctl): name the document a server rejection belongs to in apply`.
Touched: `cmd/funcdctl/cli.go`, `cmd/funcdctl/cli_test.go`, `pkg/sdk/sdk.go`.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **The new decoder keeps the old op name** · attribution: model · `pkg/sdk/sdk.go:318` —
  `DecodeManifestDocuments` declares `const op = "sdk.DecodeManifests"`, so its decode faults name the
  wrapper, not the function that raised them. This is harmless: the faults keep their existing text, which
  the `TestIssue62` assertions rely on. A rename is optional; no action is needed for sign-off.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 0872ebb` with the new test kept,
  then `go test -run TestIssue316 ./cmd/funcdctl/` → FAIL at `cli_test.go:407`: the error was the bare
  `sdk: controlplane.authz: update ConfigMap in "team-b" denied: …`, which `does not contain
  "document 3 (ConfigMap \"md-rejected\")"`. This is the unwrapped error the issue reports.
- **Passes with the fix under -race.** `go test -race -count=1 ./cmd/funcdctl/ ./pkg/sdk/` → `ok` for both
  packages. The worktree was reset to `0872ebb` and is clean.
- **The user-visible message matches the issue's "Done when" item.** The error now reads
  `funcdctl apply: document 3 (ConfigMap "md-rejected"): sdk: controlplane.authz: … denied: …`. It names the
  document and the object, and keeps the server's message and fault kind (`Forbidden`, asserted).
- **Cause, not symptom.** The defect was the unwrapped `return err` after `c.Apply`
  (`cmd/funcdctl/cli.go`). It is now `fault.Wrapf(err, fault.KindOf(err), …)`, so the kind still maps to the
  same exit and HTTP semantics. No error is swallowed and no retry or timeout was added.
- **Document numbering matches the decode errors.** The number comes from the decoder's own loop counter, so
  skipped empty and comment-only documents still count, as they do in the `manifest document %d` decode
  faults. The test checks this on purpose: the comment-only document 2 makes the rejected document number 3.
- **Mutants (3/3 killed):**
  1. `Number: n` → `Number: len(docs) + 1` (`pkg/sdk/sdk.go`) → FAIL, `document 2 (…)` does not contain `document 3`.
  2. `fault.KindOf(err)` → `fault.Internal` in the apply wrap → FAIL on the `fault.Forbidden` assertion.
  3. Help text reworded to drop "stops at the first error" → FAIL on the help assertion.
- **Scope.** Every hunk serves the issue: the wrap, the decoder that carries document numbers, and the `Long`
  help that the issue's "Done when" asks for. The asserts in `TestIssue62` and the `pkg/sdk` manifest tests
  are unchanged and pass.
- **Reuse, no duplication.** Decoding was not copied. `DecodeManifests` now delegates to
  `DecodeManifestDocuments` and projects the objects, and `DecodeManifest` still goes through
  `DecodeManifests`. The wrap reuses `fault.Wrapf` and `fault.KindOf`, in the same form that the pre-flight
  error a few lines above uses. The test reuses the existing harness (`newClient`, `execCLI`,
  `writeManifest`, `configMapDoc`). No dependency was added.
- **Conventions.** The errors use `api/fault`, there is no `any` in the new signatures, the imports are at the
  top level and no import was added, the comments carry the why, and the new names follow the package idiom
  (`ManifestDocument`, `DecodeManifestDocuments`). The new exported SDK surface is additive, so no existing
  caller breaks.
- **ADRs and living docs.** No ADR file is touched. The change is additive to `pkg/sdk` and contradicts no
  Accepted or Implemented decision. No living doc describes apply's error text.
- **Checks (touched packages).** `go vet ./cmd/funcdctl/ ./pkg/sdk/` passes. `golangci-lint run
  ./cmd/funcdctl/... ./pkg/sdk/...` reports `0 issues.` The tests are green under `-race`, as above.
- **Shape.** The subject is `fix(funcdctl): …`, the body gives the cause, the fix and the test, and it ends
  with `Fixes #316` and the attribution trailer. The commit fixes one issue.

### Definition of Done
10 / 10 items that apply hold. Item 8 (repo-wide tests, Linux lint, e2e, lane) is left to the group gate and
is not counted here; its touched-package part (build, vet, lint and `-race` tests) is green. Misses: none.

### Model scorecard
Ledger fields, not yet recorded: claude-opus-5-5 on issue #316 (fix) → pass. Counts 0 blockers, 0 majors
and 1 minor; 1 finding is model-attributed. DoD 10/10.

### Recommendation
Sign off. Hand back to `/fix` for the PR. The op-name minor is optional polish.
