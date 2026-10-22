## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1175 fix, model: claude-opus-5-5)

Branch `fix/1175-testown-group`, commit `8504b45d` (`test(hold): give TestOwn's temp dir the user's own group`),
on `origin/main` `19aa4457`. Issue #1175: `TestOwn` (`internal/platform/hold`) fails on macOS when it runs
through `nix develop`, with "lchown …: operation not permitted".

The change touches only a test: three added lines in `internal/platform/hold/hold_test.go`. `TestOwn` first gives
its `t.TempDir()`, the reference it hands to `hold.Own`, the user's primary group (`os.Chown(ref, -1, os.Getgid())`).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
None.

### ✅ Verified correct (keep it)

Every run goes through `scripts/agent/d` unless noted. "Wheel TMPDIR" is a fresh directory inside macOS's system
temp directory, whose group is `wheel`; the reviewing user is not in `wheel`. The revert overlay replaces
`hold_test.go` with `git show origin/main:internal/platform/hold/hold_test.go`.

- **The issue reproduces, for its reason, and only in its environment.**
  - Revert overlay, wheel TMPDIR, `go test -count=1 -race -run '^TestOwn$'` → `--- FAIL: TestOwn` with
    `hold.Own: give …/store the owner of …: lchown …/store/sub/000001.vlog: operation not permitted`, the
    issue's error.
  - The issue's own step, `nix develop -c go test -count=1 -run '^TestOwn$' ./internal/platform/hold/`, with the
    revert overlay → the same failure.
  - Revert overlay, the default TMPDIR (the user's temp folder) → `PASS`, as the issue says.
- **It passes with the fix, on the path that matters.** Wheel TMPDIR, `-race -count=3 -v` → three
  `--- PASS: TestOwn`, and the log "only the no-op path ran" is absent, so the group-change branch ran. The
  default TMPDIR under `-race` → `PASS`. The issue's step under `nix develop -c` → `PASS`, again without the
  no-op log.
- **It stays valid on Linux.** The package's test binary, cross-compiled for linux/arm64 and run in an
  `alpine:3.20` container (without `-race`):
  - as a non-root user with a second group (`--user 1000:1000 --group-add 2000`): the fixed test passes 3 of 3
    with the change path run, and main's test passes 3 of 3 too. On Linux a new directory takes the process's
    group, not its parent's (the container's temp dir has gid 0, the test's directory the user's), so the added
    `Chown` changes nothing there;
  - as root: the fixed test passes.

  CI runs on `ubuntu-latest` with no container (`.github/workflows/ci.yml`), which is the non-root case above.
- **Mutants on `hold.Own` (2, both killed by the fixed test).** The fix touches only the test, so the mutants
  check that the test still catches a broken `Own`, which is its intent:
  - M1: `Own` compares only the uid (`got.Uid != want.Uid`) → `FAIL`, the file keeps the other group; in the
    wheel TMPDIR and in the default one.
  - M2: `Own` keeps the group (`os.Lchown(p, int(want.Uid), -1)`) → `FAIL` in the wheel TMPDIR.
  - The revert of the fix fails as shown above.
- **The cause is in the test, and the fix removes it.** The test's doc comment states its premise: without root, a
  process can move a file only between its own groups. On macOS a new directory takes its parent's group, so the
  test before the fix took its reference group from wherever TMPDIR lives (`wheel` in the system temp directory)
  and asked a non-root `hold.Own` to move a file into a group the user is not in. The fix restores the premise; it
  adds no timeout, retry or skip and swallows no error.
- **`hold.Own` should not change.** Its doc comment (`internal/platform/hold/hold.go`) and ADR-0206 Decision
  step 5 ("`hold.Own` gives all the restore created `<storage.dataDir>`'s owner") make it the hand-back from a
  restore run as root to the data directory's owner, the ADR-0026 §4 `funcd` unit user. Its other callers
  (`cmd/funcd/restore.go`, `internal/restore/run.go`, `internal/upgrade/upgrade.go`,
  `internal/safemode/safemode.go`) use it the same way. As root, `lchown` to any group succeeds. A non-root caller
  that cannot give an entry the reference's group gets a `fault.Internal` that names the `lchown` error, which is
  the right outcome when the tree cannot match its reference. Ignoring that error would change an Accepted
  decision, and the issue does not call for it.
- **No sibling has the same cause.** A grep of the `_test.go` files for `Chown`, `Lchown`, `Getgroups` and
  `Getgid`, read but not run (this review runs only the fixed package): `otherOwner` (`cmd/funcd/restore_test.go`),
  `ownerFor` (`cmd/funcd/upgrade_test.go`) and `ownTo` (`internal/upgrade/upgrade_test.go`) take their target
  group from `os.Getgroups()` minus `os.Getgid()`, so it is always one of the user's groups, and set it on the data
  directory themselves before `hold.Own` runs. None of them takes the target group from a temp dir.
  `TestConverge_chownsTheLeafAsRoot` (`internal/provider/storage_test.go`) skips unless it runs as root.
- **Scope, reuse, conventions.** One file, three added lines; no assertion changed or removed. No new helper:
  `os.Getgid()` as "the user's own group" is the idiom the three sibling helpers already use. The new comment says
  why (the platform behavior and the issue number), not what.
- **Checks** (`internal/platform/hold` only): `go test -race -count=1` → `ok` with the default TMPDIR and with the
  wheel TMPDIR; `go vet` → clean on darwin and with `GOOS=linux`; `golangci-lint run` → `0 issues.` on darwin and
  with `GOOS=linux`, run the way `scripts/agent/gate.sh` runs it (the binary from `go tool -n golangci-lint`;
  `GOOS=linux go tool golangci-lint` builds a Linux binary that the host cannot run). The repo-wide gate runs
  once on the PR.
- **ADRs**: no ADR file is touched, and ADR-0206's `hold.Own` contract is unchanged.
- **Shape**: one commit, `Fixes #1175`, and the attribution trailer. The subject is `test(hold):`, not the
  checklist's `fix(<scope>):`. For a change to a test alone, `test` is the correct Conventional type: it matches
  the repository's earlier test-only fixes (#1103, #1168), and it keeps release-please from cutting a patch
  release that would ship nothing.

### Definition of Done
12 / 12 items hold. Item 1 holds in substance: the defect is in `TestOwn` itself, so `TestOwn` run in the issue's
environment is the regression test, and there is no separate `TestIssue1175_…`; the new comment names #1175, so
the link between the issue and the test stays searchable. Item 8: e2e and the lanes do not cover this unit test.

### Model scorecard
Recorded: claude-opus-5-5 on issue #1175 (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation
Pass. Hand back to `/fix` Step 8: open the PR with `Fixes #1175` in its description.
