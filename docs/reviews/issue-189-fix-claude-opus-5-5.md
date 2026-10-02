## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #189 fix, model: claude-opus-5-5)

Change: branch `fix/i189`, one commit `4f0ce54 fix(config): resolve a relative storage.dataDir to an absolute path`
(`cmd/funcd/main_test.go` +28, `internal/platform/config/config.go` +10/-1).

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

Observations (not findings):
- Explicitly set relative sub-directories (`storage.metastoreDir`, `kvstore.dataDir`, `workflow.dataDir`,
  `eventing.deadletter.dataDir`, the containerd paths) still stay relative. None of them is built into a
  URL, so none of them hits the issue's defect; normalizing them would be a scope extension, not part of #189.
- The regression test sets `storage.dataDir` through the file. The `FUNCD_DATA_DIR` path from the issue is
  covered by the same code, because the new step runs after `env.Parse`; there is no separate env test.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 4f0ce54` with the new test kept,
  then `go test -run TestIssue189 ./cmd/funcd/` → `FAIL`, at `main_test.go:96`:
  `open file blob: gocloud.Open: open bucket "file://data/blob": stat /blob: no such file or directory`,
  the exact error in the issue. `git reset --hard` back to `4f0ce54`; the worktree is clean.
- **Passes with the fix under -race.** `go test -race -count=1 ./cmd/funcd/ ./internal/platform/config/` →
  `ok` for both; `-v -run TestIssue189` → `--- PASS: TestIssue189_RelativeDataDirOpensFileSubstrate`, not skipped.
- **The user-visible path is exercised.** The test runs the real `config.Load` → `substrateOptions` →
  `funcd.New(Production, …)` → `Shutdown`, and asserts `<cwd>/data/blob` exists. That is the daemon's own
  startup sequence for the file substrate, not a stub.
- **Mutants (overlay, 3 of 3 killed by TestIssue189):**
  1. drop the assignment (`_ = abs`) → FAIL;
  2. invert the guard (`== ""`) → FAIL;
  3. move the Abs block after the derived defaults (so `MetastoreDir` stays relative) → FAIL on the
     `MetastoreDir` assertion.
- **Root cause, not symptom.** The issue names two sites: the `"file://"+blobDir` URL and `Load` doing no
  `filepath.Abs`. The fix normalizes once in `Load`, before every `filepath.Join(c.Storage.DataDir, …)`
  default, so the blob URL, the bus, the metastore and the containerd paths share one absolute root. This
  also closes the issue's "split root" edge case. No retry, no swallowed error. The issue explicitly
  accepts this remedy ("config.Load makes the path absolute").
- **Scope.** Two hunks: the normalization step (plus a matching doc-comment update on `Load`), and the
  regression test. No test was weakened or deleted.
- **Reuse.** Uses the standard library (`filepath.Abs`) at the existing derivation point in `Load`. No new
  helper, type, harness or dependency. The test reuses the harness pieces that `TestDaemonSubstrate` in the
  same file already uses (`substrateOptions`, `store.New(memory.New())`, `process.New()`, `funcd.WithDevAuth`).
- **Conventions (ADR-0002, CLAUDE.md).** The error is `fault.Invalidf(op, "config key %q: …", "storage.dataDir", …)`,
  so it names the key, as ADR-0061/0062 require and as the neighbouring `Validate` errors do. The comments
  explain why, in two lines each, with no bloat. Imports are at the top level. `t.Chdir` + `t.TempDir` keep
  the test hermetic.
- **ADRs.** No ADR file changed. ADR-0062's Load pipeline (defaults → file → env → flag → derive → Validate)
  still holds; the new step sits inside it, before "derive", and contradicts no Decision or Contract. No
  living doc states that `dataDir` must be absolute, so no doc goes stale.
- **Checks (touched packages).** `gofmt -l` is clean; `go vet ./cmd/funcd/ ./internal/platform/config/` is OK;
  `golangci-lint run ./cmd/funcd/... ./internal/platform/config/...` → `0 issues`; tests are green under -race.
  Linux lint, e2e and lanes are deferred to the group gate by this review's scope.
- **Shape.** Subject `fix(config): …`, body explains the cause, `Fixes #189`, the attribution trailer, one
  issue in one commit.

### Definition of Done
11 / 11 fix-checklist items hold. Item 8 was verified on the host for the touched packages; Linux lint, e2e
and lanes are left to the group gate as this gate's scope directs.

### Model scorecard
To record: claude-opus-5-5 on issue #189 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Sign off. Hand back to `/fix` Step 8 (open the PR) once the group gate runs the repo-wide, Linux and e2e checks.
