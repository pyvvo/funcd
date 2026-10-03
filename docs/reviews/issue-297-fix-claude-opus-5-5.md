# Fix review — issue #297 (model: claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #297 fix, model: claude-opus-5-5)

Change: branch `fix/i297`, commit 13e2bd6 `fix(ci): lint and test the -tags dev packages in just ci`
(2 files: `justfile` +6, new `tests/lint-fixtures/devtag_test.go` +176). Governing ADR: ADR-0125
(`funcdctl dev`, built only with `-tags dev`). The issue's "Done when": CI builds, lints and tests the dev
build tag. CI runs `just ci` (`.github/workflows/ci.yml`), so adding the dev steps to `just ci` meets it.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### 🟡 Minor 1 — the newly exercised dev suite has a flaky test that CI will now hit  ·  attribution: issue

Running the new step myself (`go test -tags dev ./cmd/funcdctl ./internal/catalog/devengine
./internal/catalog/embedengine`) showed `TestScenarioDevPersistSurvivesRestart`
(`cmd/funcdctl/dev_phase2_test.go:142`) failing intermittently with
`funcdctl dev: apply Function "002": sdk: store.Update: Function "002" resourceVersion mismatch` on the
second `startDev --persist` boot. Measured: 4 of 5 isolated runs fail under `-race`, 1 of 8 isolated runs
without `-race`; 2 of 2 whole-package runs without `-race` (the form `just ci` uses) passed, and 1 of 2
whole-package runs under `-race` failed. The dev code is untouched by this change, so the defect is
pre-existing. It is exactly the class of hidden failure the issue describes, and the fix correctly
surfaces it. It is out of this fix's scope, but once merged it makes `just ci` (and the merge queue)
fail on some runs. **Before the group merges, file a separate `kind/flake` (or `kind/bug`, `area/cli`)
issue for the restart-apply resourceVersion race and fix it in the same group or immediately after.**
Not scored against the model.

### 🟡 Minor 2 — the non-dev `cmd/funcdctl` tests run twice per CI run  ·  attribution: model

`go test -tags dev ./cmd/funcdctl` runs the whole package again, including every test that is not
dev-constrained, which already ran in the untagged `go test ./...` step (about 7 s on this host). This is a
small cost and follows the issue's own suggestion. Filtering with `-run` would be fragile, so the current
form is acceptable. This finding does not block the fix.

### ✅ Verified correct (keep it)

- **Root cause fixed.** The issue's cause is that `just lint`, `just test` and `just ci` never pass the dev
  tag and `.golangci.yml` sets none. All three recipes now run `golangci-lint run --build-tags dev` and/or
  `go test -tags dev` over the dev packages. `just --evaluate _dev-packages` and `just --dry-run lint`
  show the expanded commands. The list (`./cmd/funcdctl ./internal/catalog/devengine
  ./internal/catalog/embedengine`) equals the set of directories with a `//go:build ...dev...` file in the
  module.
- **The dev code is now really linted and tested.** `golangci-lint run --build-tags dev` over the three
  packages reports 0 issues; `go test -tags dev` passes `devengine` and `embedengine` and compiles and
  runs the `cmd/funcdctl` dev tests (one pre-existing flake, Minor 1).
- **The regression test fails without the fix, for the issue's reason.** `git revert --no-commit 13e2bd6`
  with the test file restored: `TestIssue297_JustRecipesCoverDevBuildTag` fails with "just lint never runs
  golangci-lint run --build-tags dev", "just test never runs go test -tags dev" and the same two lines for
  `just ci`. After `git reset --hard 13e2bd6` it passes under `-race`. The worktree is left at 13e2bd6,
  clean.
- **Three mutants, all killed.** (1) Drop `./internal/catalog/embedengine` from `_dev-packages`: the test
  names the uncovered package for all four recipe/command pairs. (2) Delete the `go test -tags dev` line in
  `ci`: "just ci never runs go test -tags dev". (3) Remove `--build-tags dev` from the `lint` recipe:
  "just lint never runs golangci-lint run --build-tags dev".
- **The test guards drift, not only the current list.** It discovers dev-constrained packages by parsing
  each file's `//go:build` line with the standard library's `go/build/constraint` and walks the expression
  tree, so a new dev-tagged package that is not added to `_dev-packages` fails the test. It also expands
  justfile variables, so the single `_dev-packages` definition is checked where it is used.
- **Reuse.** The test reuses the package's existing `repoRoot` helper (`tests/lint-fixtures/lintrules_test.go`)
  and the standard library for constraint parsing; no repo code parses the justfile or build constraints
  already. The justfile names the package list once and references it four times.
- **Scope.** Both hunks serve the issue; no test was weakened or deleted; no ADR file was touched, and the
  change contradicts no Accepted ADR (ADR-0125 keeps the dev code behind `-tags dev`; this only exercises it).
- **Conventions.** Top-level imports, a short doc comment on the test and on the two non-obvious helpers, a
  one-line justfile comment naming ADR-0125; no comment bloat; no YAML touched.
- **Checks on the touched package.** `go vet ./tests/lint-fixtures/` clean, `golangci-lint run
  ./tests/lint-fixtures/` 0 issues, `go test -race -run TestIssue297` passes.
- **Shape.** `fix(ci):` subject, a body that names the cause and the regression test, `Fixes #297`, the
  attribution trailer, one issue in one commit.

### Recommendation

Pass. File the restart-apply flake (Minor 1) as its own issue before the group PR merges, so that the
merge queue's first red run on `TestScenarioDevPersistSurvivesRestart` is a known, tracked defect and not
a surprise. The group gate still runs the repo-wide checks, the Linux lint and CI.
