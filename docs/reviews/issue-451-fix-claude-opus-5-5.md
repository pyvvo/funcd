# Fix review — issue #451 (`funcdctl index` creates an empty OCI layout at a mistyped path)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #451 fix, model: claude-opus-5-5)

Change: branch `fix/i451`, one commit `d672756 fix(artifact): stop funcdctl index from creating a layout at a
mistyped path` (`internal/artifact/platform.go` +8/−3, `internal/artifact/platform_test.go` +29).

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

Note (no finding): for a registry ref, `resolveReadTarget` falls back to `resolveTarget`, so `PushIndex` now builds
the remote repository client twice. Both calls are local (no network round trip), so the cost is negligible.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** I reverted `internal/artifact/platform.go` only and kept the new
  test. `TestIssue451_IndexOfMissingLayoutWritesNothing` then fails in both subtests. The `absent` subtest fails
  with "index created a layout at the missing path", because the directory now exists. The `empty` subtest fails
  because the directory now holds `blobs/`, `index.json` and `oci-layout`. This is the stray layout the issue
  describes. Afterwards I reset the worktree to `d672756` and left it clean.
- **Passes with the fix**: `go test -race ./internal/artifact/` → `ok` (19.7 s). The test is not skipped.
- **User-visible behavior**: I built `funcdctl` and ran `funcdctl index oci-layout://./regsitry:app
  oci-layout://./regsitry:app-amd64`. It fails with `artifact.resolveReadTarget: no OCI layout at "./regsitry"`,
  and `./regsitry` does not exist afterwards.
- **Cause, not symptom**: the issue names the writing `resolveTarget` call (`MkdirAll` plus `oci.New`) made before
  the source checks. The fix reads and validates the sources through the read-only `resolveReadTarget` (#361). It
  opens the writable target only after every rule has passed, just before `Push`/`Tag`. The doc-comment contract
  "every rule is checked before anything is written" now holds.
- **Mutants** (overlay, `-run 'TestIssue451|PushIndex|Index'`):
  - M1: the source reader opened with `resolveTarget` → killed (`TestIssue451` fails).
  - M2: the first resolve error re-kinded to `fault.Internal` → killed (the `fault.NotFound` assertion fails).
  - A third mutant, which kept the reader unused, did not compile and was dropped.
- **Scope**: both hunks serve the issue. The existing PushIndex tests are unchanged and still pass, so the happy
  path (push and tag through the second, writable `resolveTarget`) is covered. No test was weakened.
- **Reuse**: no new helper. The fix reuses `resolveReadTarget` and `resolveTarget`, the read/write pair from #361
  and #97. `readIndexSource` already took an `oras.ReadOnlyTarget`, so its signature did not change. The test
  follows the shape of the #361 tests: `require.NoDirExists`, an empty-directory check, and the `fault.NotFound`
  kind together with the "no OCI layout" message.
- **Conventions (ADR-0002)**: errors use `api/fault`, keep the wrapped error's kind, and keep the `op` naming;
  `ctx` comes first; imports are at the top level. The comment cites the issue in the house style (as #361's
  comment does) and is not bloated. The test touches no `funcd.New`, so `t.TempDir()` is fine.
- **ADRs**: the change keeps the behavior of ADR-0145 Decision 2 (`funcdctl index`) and edits no ADR file.
- **Checks (the touched package)**: `go vet ./internal/artifact/` is clean, and `golangci-lint run
  ./internal/artifact/...` reports 0 issues. The group gate runs the repo-wide checks, Linux lint and e2e.
- **Shape**: the subject is `fix(artifact): …`, the body gives the cause, the fix and the test, and the commit
  carries `Fixes #451` and the attribution trailer. It covers one issue in one commit.
- **Repository hygiene**: the check found nothing.

### Definition of Done
11 of 11 applicable items hold. For item 8, I covered the touched package (host build, vet, lint and tests with
`-race`); Linux lint and e2e are left to the group gate.

### Model scorecard
- claude-opus-5-5 · fix · pass · 0 blockers / 0 majors / 0 minors · 0 model-attributed · DoD 11/11.

### Recommendation
Pass. Hand back to `/fix` Step 8 to open the PR (or to the group PR).
