# Fix review — issue #318 (`funcdctl types` skips the contract profile gate)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #318 fix, model: claude-opus-5-5)

Change: branch `fix/i318`, one commit `d749830 fix(funcdctl): gate the manifest contract in funcdctl types like push`
(`cmd/funcdctl/manifest.go`, `cmd/funcdctl/manifest_test.go`; +63/-7).

The issue names the cause: `typesCmd` called `sdk.LoadManifest` and then `sdk.GenerateTypes` without
`contract.Check`, while `pushFromManifest` ran it. The fix moves the `ContractSides` + two `contract.Check`
calls out of `pushFromManifest` into `gateManifestContract(op, m)` and calls it from both paths. `types`
now fails with the same `fault.Invalid` "outside the funcd profile" error as `push` and writes no file.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit d749830`, test file restored
  from HEAD, `go test -race -run TestIssue318 ./cmd/funcdctl/`: both subtests (`anyOf input`,
  `unquoted null output`) fail with "An error is expected but got nil" — `types` exits 0 on an
  out-of-profile contract, which is exactly the reported behavior. The worktree was then reset to `d749830`
  and is clean.
- **Passes with the fix** under `-race`, un-skipped, together with the existing `TestScenario_types_command`
  and the push tests (`TestScenarioCLIPushPullRoundtrip`, `TestScenarioCLIPushSite`,
  `TestCLIPushPlatformAndIndex`, `TestCLIPushPlatformRefusals`).
- **User-visible behavior.** The test drives the real cobra command (`execCLI ... "types" -f ... -o ...`),
  asserts `fault.Invalid`, the push error text, and an empty output directory — this is the issue's CLI
  repro, not only the SDK call.
- **Mutants (overlay, `go test -run 'TestIssue318|Push|Scenario'`)** — all killed:
  1. output-side `contract.Check` disabled → `TestIssue318…` fails (the `unquoted null output` case);
  2. input-side `contract.Check` disabled → `TestIssue318…` and `TestScenario_out_of_profile_rejected` fail;
  3. the `types` gate's error ignored → `TestIssue318…` fails.
  Mutant 1 shows the new test also closes a pre-existing gap: no push test covered the output-side gate.
- **Cause, not symptom.** The gate now runs before `sdk.GenerateTypes`, which is the precondition the
  comment in `pkg/sdk/types_gen.go` (`typeSchema`) already assumes. `sdk.LoadManifest` correctly stays
  gate-free (its doc comment: `internal/contract` must not be imported from `pkg/sdk`), so the CLI layer is
  the right place.
- **Reuse, no duplication.** `internal/contract` exposes only `Check`; there is no existing doc-level gate
  to reuse. The fix extracts the push path's existing pair into one helper instead of copying it into
  `typesCmd`, so push and types cannot drift. The similar pairs in `cmd/funcdctl/cli.go` (`--schema` file)
  and `internal/artifact/bundle.go` (bundle contract file) gate a different source with source-specific
  messages and are out of this issue's scope.
- **Scope.** Every hunk serves the issue; push behavior and messages are byte-identical; no test was
  weakened or deleted.
- **Conventions.** `api/fault` errors with the caller's `op` (`"funcdctl push"` / `"funcdctl types"`),
  one short doc comment citing ADR-0058, block-style YAML in the test fixtures, top-level imports, a
  table-driven test in the surrounding style.
- **ADRs.** Realizes ADR-0122 Decision 4 (`funcdctl types`) and ADR-0058 (contract profile) as the issue
  cites; no ADR file touched.
- **Checks (touched packages).** `go test -race -count=1 ./cmd/funcdctl/ ./pkg/sdk/` ok; `go vet
  ./cmd/funcdctl/` clean; `golangci-lint run ./cmd/funcdctl/...` 0 issues. Repo-wide, Linux lint and e2e
  are left to the group gate, per the run's instructions.
- **Shape.** `fix(funcdctl):` subject, cause/fix/test body, `Fixes #318`, attribution trailer, one issue
  in one commit.

### Definition of Done

10 of 10 applicable items hold (1–7, 9–11). Item 8 is verified for the touched packages only (build, vet,
host lint, `-race` tests); the Linux lint and the repo-wide set run in the group gate and are not counted here.

### Model scorecard

claude-opus-5-5 — fix phase: pass, 0/0/0, 0 model-attributed findings.

### Recommendation

Pass. Hand back to `/fix` Step 8 for the group PR.
