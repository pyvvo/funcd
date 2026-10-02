# Fix review — issue #317 (`funcdctl dev` decodes a Workflow file without the #63/#64 fixes)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #317 fix, model: claude-opus-5-5)

Change: branch `fix/i317`, commit `5223c6b fix(funcdctl): decode a dev Workflow file through sdk.DecodeManifest`
(`cmd/funcdctl/dev.go` +9/-4, `cmd/funcdctl/dev_phase3_test.go` +21). Governing ADR: ADR-0125 (Decision 8,
`funcdctl dev workflow.yaml`); the decode contract is `sdk.DecodeManifest` (`pkg/sdk/sdk.go`), already used by
`funcdctl apply` (`cmd/funcdctl/cli.go`, `sdk.DecodeManifests`).

### 🟡 Minor 1 — the #64 assertion also matches the file name  ·  attribution: model

`TestIssue317_DevDecodesWorkflowStrictly` checks the unknown-key rejection with
`require.ErrorContains(t, err, "bogus")`, but the file is named `bogus.yaml` and `detectWorkflow` puts the path
in its error message. Any error on that file satisfies the assertion.

Evidence: mutant M2 changed the error to `fault.Invalidf(op, "parse workflow %q", path)` (the decoder's
message dropped) and the test still passed (`ok github.com/pyvvo/funcd/cmd/funcdctl`). The test still fails when
the strict decode is removed (the revert, and mutant M1), so the defect is covered; only the message check is
weak. Fix: name the file without the key (for example `unknown.yaml`), or assert on the decoder's own text
(`unknown field "bogus"`).

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With `git revert --no-commit 5223c6b` and the new test
  restored: `--- FAIL: TestIssue317_DevDecodesWorkflowStrictly`, `expected: {"n":2, "y":1}`,
  `actual: {"false":2, "true":1}` — exactly the #63 symptom the issue reports. The worktree was then reset to
  `5223c6b` and is clean.
- **Passes with the fix** under `-race` (`-tags dev`), together with the existing `TestDetectWorkflow`.
- **Cause, not symptom.** The issue names `yaml.Unmarshal` at `cmd/funcdctl/dev.go:979`; the fix replaces it with
  `sdk.DecodeManifest`, the decode `funcdctl apply` uses, so the #63 key quoting and the #64 strict decode now
  apply on this path. The lenient `kind` probe stays, so a non-Workflow file still falls through to the function
  resolver as before (ADR-0125 Decision 8).
- **Mutants.** M1 (swallow the decode error, return "not a workflow") → fails (`An error is expected but got
  nil`). M3 (lenient `yaml.Unmarshal` for the result, strict decode only to surface unknown-key errors) → fails
  on the `y`/`n` keys. M2 survived — see Minor 1.
- **Scope.** Two hunks, both for the issue; no test weakened or removed.
- **Reuse.** It reuses `sdk.DecodeManifest` (already imported in `dev.go`) instead of re-implementing quoting or
  strict decoding. `DecodeManifest` also rejects a multi-document file with a clear error, which fits a
  single-Workflow argument.
- **Conventions.** `api/fault` errors with the caller's `op`; the type assertion guards against a non-Workflow
  result with a `fault.Invalid`; one short *why* comment; no YAML style or import issues.
- **ADRs.** No ADR file touched; behaviour now matches the `apply` decode contract.
- **Checks (touched package).** `go vet -tags dev ./cmd/funcdctl/` clean; `golangci-lint run --build-tags dev
  ./cmd/funcdctl/...` → `0 issues.`; `go test -race ./cmd/funcdctl/` green with and without `-tags dev`.
  One run of the full `-tags dev` package had `TestScenarioDevPersistSurvivesRestart` fail; it passed alone and
  on the rerun of the whole package. It does not exercise `detectWorkflow` — attribution `env` (load on the
  shared host), not scored.
- **Shape.** `fix(funcdctl):` subject, Cause/Fix/Test body, `Fixes #317`, attribution trailer, one commit.

### Definition of Done

11 of 11 items hold. Item 8 was checked on the host for the touched package only; Linux lint, the repo-wide tests
and e2e run at the group gate.

### Model scorecard

claude-opus-5-5 — pass; 0 blockers, 0 majors, 1 minor (model); DoD 11/11.

### Recommendation

Pass. Optionally rename the fixture file or tighten the assertion (Minor 1) before the PR; no re-review needed.
