## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #160 fix, model: claude-opus-5-5)

Change: `bd9b56a fix(blob): reject keys the file substrate cannot store, and stop ./ keys aliasing`
(`internal/blob/gocloud/gocloud.go`, `internal/blob/gocloud/gocloud_test.go`).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minors
- **Read and delete paths on the file backend answer `Invalid` for a key that cannot exist** · attribution: `model`.
  `checkKey` runs on `Get`, `Exists`, `GetRange` and `Delete`, and `mapErr` maps `ENAMETOOLONG`/`ENOTDIR`
  on a stat to `Invalid`. On `mem://` the same calls answer `NotFound`/`false`. Through the gateway
  (`internal/blob/s3gateway/backend.go`), `HeadObject`/`GetObject` of such a key is a 400 instead of a 404,
  `DeleteObject` is a 400 instead of S3's idempotent 204, and `DeleteObjects` reports that entry as
  `InternalError` (backend.go:309 only tolerates `NotFound`). This is a large improvement on the pre-fix
  500s and aliasing, and every write path is correct; but the issue asks that behavior not depend on the
  substrate. Fix (optional, follow-up): on read and delete paths, report an unstorable key as `NotFound`
  (it can never have been written) and keep `Invalid` for `Put`.
- **Pre-existing data race in the s3gateway test harness** · attribution: `env` (not scored).
  `go test -race ./internal/blob/s3gateway/` failed intermittently in `TestScenarioOwnerWrites`:
  `WARNING: DATA RACE` with a write in `(*Server).Close()` (s3gateway.go:182) from the `newGateway` cleanup.
  Reproduced with the `origin/main` version of `gocloud.go` overlaid (1 of 6 runs fails), and the harness
  uses `mem://`, where the fix is a no-op. Unrelated to this change; it should be filed as its own issue.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit bd9b56a` with the new test kept →
  `TestIssue160_UnstorableKeysAreInvalidAndNeverAlias/file` fails in all 10 file subtests: the 4 key-shape
  cases and the 2 prefix/object collisions give `expected: "invalid"` / `actual: "internal"`, and the
  4 alias cases give `Get` of the other key returning data (`expected: "not_found"`, `actual: ""`). The
  memory subtests pass in both states, which shows the test isolates the file substrate. The worktree was
  reset to `bd9b56a` and left clean.
- **Passes with the fix**: `go test -race -count=1 ./internal/blob/gocloud/` → `ok`; the test is not skipped.
- **The issue's own steps, end to end**: a scratch gateway probe (overlay only, not committed) PUT the
  issue's keys over `mem://` and `file://`. On file, `bronze/x.attrs`, the 1024-byte key, the 307-byte key,
  `bronze` over `bronze/y`, and `bronze/./dot` now each answer **400 within 1 ms**. Before the fix they
  answered 500 after about 20 s of SDK retries. On mem, all of these PUTs answer 200.
- **Cause, not symptom**: the change fixes both causes the issue names. First, `checkKey` rejects, before
  gocloud is called, the `.attrs` suffix that fileblob reserves and the key shapes that `filepath.Join`
  cleans onto another key (a `.` segment, a trailing `..`, a leading `/`). This matches fileblob's
  `escapeKey`, which already escapes `../`, `//` and a trailing `/`, so a middle `..` and an empty middle
  segment cannot alias. Second, `mapErr` maps the OS errnos to `fault.Invalid`, which the gateway already
  maps to `ErrInvalidRequest`. Nothing is retried, timed out or swallowed.
- **Mutants (3/3 killed)**:
  M1, removing the `s == "."` condition, fails `alias_bronze/./dot` and `alias_./dot`.
  M2, removing the `ENAMETOOLONG` arm, fails the 300-byte, 1024-byte and 1999-byte subtests.
  M3, forcing `file` to false, fails the attrs subtest and all 4 alias subtests.
- **Scope**: only the two gocloud driver files change, and every hunk serves the issue. No test was
  weakened, and `TestScenario_DriverConformanceParity` still passes.
- **Reuse**: the check uses `fileblob.Scheme` and `fault.Invalidf`/`fault.Wrapf`. No key validator exists
  elsewhere in `internal/blob`, `api/fault` or the gateway. fileblob does not export its key-to-path rules,
  so a small local check is justified. The errno test uses `errors.Is` from the standard library.
- **Conventions**: `api/fault` kinds, ctx-first methods unchanged, imports at top level, short comments
  that explain why, and the existing `require` and table-test idiom. The file-only gate stays in the
  driver and does not leak into the port (ADR-0007 §1, ADR-0002).
- **ADRs**: no ADR file touched. The change strengthens ADR-0007's opaque, driver-independent key
  promise and its §3 error mapping.
- **Checks (touched packages)**: `go vet ./internal/blob/...` is clean; `golangci-lint run ./internal/blob/...`
  reports `0 issues.`; `gofmt -l` is clean; `go test -race` passes for gocloud and for s3gateway (s3gateway
  apart from the env race above). Linux lint, e2e and the lanes are left to the group gate, as the task
  specifies.
- **Shape**: the subject is `fix(blob): …`, the body has `Fixes #160` and the attribution trailer, and the
  change is one commit for one issue.

### Definition of Done
11 / 11 items hold for the scope reviewed here. Item 8 was checked only on the host and only for the
touched packages; Linux lint and e2e are left to the group gate. Misses: none.

### Model scorecard
Not recorded by this gate (ledger fields returned to the caller): claude-opus-5-5 on issue #160 (fix) → pass,
0/0/2, 1 model-attributed, DoD 11/11.

### Recommendation
Ship. As an optional follow-up, make read and delete paths answer `NotFound` for an unstorable key, which
gives full substrate parity. File the pre-existing s3gateway `Server.Close` race separately.
