# Fix review — issue #332 (empty `storage.dataDir` passes config validation)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #332 fix, model: claude-opus-5-5)

Change: branch `fix/i332`, one commit `41311dc fix(config): reject an empty storage.dataDir at load, naming the key`.
Two hunks: `validate:"required"` on `Storage.DataDir` (`internal/platform/config/config.go:104`) and
`TestIssue332_EmptyDataDirRejected` (`internal/platform/config/config_test.go`).

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 41311dc` with the test file restored
  from HEAD: `TestIssue332_EmptyDataDirRejected` FAILS — `expected: "invalid"`, `actual: ""` (Load returned no
  error for `storage.dataDir: ""`, exactly the issue's scratch-test observation). Worktree then reset to `41311dc`, clean.
- **Passes with the fix under `-race`**: `go test -race -count=1 ./internal/platform/config/` → `ok`.
- **User-visible behavior fixed.** The issue's own steps against a freshly built `cmd/funcd`: a config holding only
  `storage.dataDir: ""` now exits at load with
  `config.Config.Validate: config key "storage.dataDir" has invalid value "" (want required)` — the key is named,
  `fault.Invalid`, instead of the vague `create data dir : mkdir : …` at `cmd/funcd/main.go:120`.
- **Cause, not symptom.** The issue's named cause is the missing validate tag on `Storage.DataDir`; the fix adds it.
  `Validate()` runs after the merge and derivation in `Load`, and derivation never rewrites `Storage.DataDir`, so
  file, `FUNCD_DATA_DIR` env and hand-built configs all hit the same gate (ADR-0062's single value gate). The
  `if c.Storage.DataDir != ""` guard before `filepath.Abs` stays correct: without it `Abs("")` would silently
  become the working directory and mask the error.
- **Required in every mode is right.** `cmd/funcd/main.go` uses `Storage.DataDir` unconditionally (mkdir, artifacts,
  invoke sockets, TLS, shims) in memory mode too, so an empty value is invalid regardless of `storage.mode`.
- **Mutants (2), both killed:**
  1. `validate:"required"` → `validate:"omitempty"` → `TestIssue332` FAIL.
  2. `Load` skips `c.Validate()` → `TestIssue332` FAIL.
- **Scope**: both hunks serve the issue; no test weakened or deleted; the existing `TestValidateMatrix` and the
  whole package suite still pass (the zero-config default `/var/lib/funcd` satisfies `required`).
- **Reuse**: no new helper, type or dependency — it reuses the existing go-playground/validator tag mechanism and
  `Config.Validate`'s yaml-key error naming, and the test reuses the package's `writeCfg` helper and `fault.KindOf`.
- **Conventions**: `api/fault` kind asserted, block-style YAML in the test fixture, top-level imports, one short
  *why* comment on the test; tag style matches the neighbouring fields.
- **ADRs**: conforms to ADR-0061/ADR-0062 (invalid value → `fault.Invalid` naming the key); no ADR file touched.
- **Checks (touched package)**: `go test -race` ok, `go vet` clean, `golangci-lint` `0 issues`, `go build ./...` ok.
  Repo-wide, Linux lint and e2e are left to the group gate.
- **Shape**: `fix(config):` subject, `Fixes #332`, attribution trailer, one issue in one commit.

### Notes (not findings)

- `TestValidateMatrix`'s header says it enumerates every `validate`-tagged field; `storage.dataDir` could get a row
  there (`{"", false}`, `{"/x", true}`). Existing tagged fields (`server.limits.*`, `site.defaultIndex`) are also
  absent from it, so this is not a convention the change breaks; the Load-level issue test covers the path end to end.

### Definition of Done

11 of 11 applicable items hold (item 8 verified for the touched package; the repo-wide/Linux/e2e part runs once at
the group gate).

### Model scorecard

claude-opus-5-5 · fix · pass · 0 blockers · 0 majors · 0 minors · 0 model-attributed · DoD 11/11.

### Recommendation

Pass. Hand back to `/fix` Step 8 for the group PR.
