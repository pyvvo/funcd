# Fix review — issue #932 (TestStoreHeldConflict finds the backup target's lock still held)

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #932 fix, model: claude-opus-5-5)

Change: branch `fix/w12-i932`, commit d418a95e `fix(backup): release the target's lock on Close while a forked child holds it`.
Touched: `internal/backup/backup.go`, `internal/runtime/procreg/procreg.go`, their tests and `export_test.go` files.

Cause, as the fix states it: flock(2) ties the lock to the open file description. A child that another goroutine
forks holds a copy of every descriptor until it execs, close-on-exec ones included. `target.Close` only closed its
descriptor, so the lock outlived Close until that child exec'd, and the next `Ready` in the same process was refused
as a second writer. The fix calls `LOCK_UN` before the close, which releases the lock for every copy of the
description. `procreg.Registry`, whose lock the target's mirrors, gets the same change in `Close` and in `Open`'s
error paths. The decider asked for proof first and a product fix only if the cause is in product code. The cause is
in product code, and the fix is the small standard remedy, not a redesign.

### 🟡 Minor 1 — a hand-rolled `release` where the module's flock library already unlocks on close  ·  attribution: model

`github.com/gofrs/flock` v0.13.0 is already a dependency (`internal/artifact/artifact.go`). Its `Close`/`Unlock`
does `LOCK_UN` and then the close, which is what the new `procreg.release` and the inline `backup.target.Close`
hunk do. The two packages already hand-rolled the `LOCK_EX|LOCK_NB` side before this change, so moving both locks
onto the library would be a larger refactor than this fix. The new code also exists twice (an inline hunk and a
helper). This is trivial, so it is Minor.

### 🟡 Minor 2 — a sibling with the same cause is not mentioned  ·  attribution: model

Badger v4.9.2's `directoryLockGuard.release` (`dir_unix.go`) removes the pid file and then only closes the
descriptor, with no `LOCK_UN`. A Badger store that is closed and reopened in the same process while a child is
being forked has the same window. The store itself was not hit here, and the code is in a dependency, so funcd
cannot fix it in place. The fix's commit message should still name it so that an upstream report or a follow-up
issue exists. The `gofrs/flock` user (`internal/artifact`) is not affected.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** An overlay of the `origin/main` versions of `backup.go` and
  `procreg.go` makes both `TestIssue932_CloseReleasesLockWhileChildHoldsDescriptor` tests fail.
  Backup: `backup.Ready: backup.target: another process holds the lock of …`, which is the issue's message.
  Procreg: `procreg.Open: registry …/workers.json is held by another process`.
- **Passes with the fix**: `go test -race` on `./internal/backup/...` and `./internal/runtime/procreg/...` is
  ok. `-race -count=20 -run TestIssue932` is ok in both packages.
- **Mutants**: (1) dropping the `LOCK_UN` line in `target.Close` fails the backup test. (2) Replacing the `LOCK_UN`
  in `procreg.release` with a nil error fails the procreg test. No mutant survived.
- **The test models the real cause faithfully.** An `F_DUPFD_CLOEXEC` copy of the lock's descriptor shares the
  open file description, as a forked child's copy does before exec. This makes a timing-dependent flake
  deterministic without forking.
- **Cause, not symptom**: there is no retry, no longer timeout and no skip. The fix uses the flock(2) semantics.
  Go's `syscall/exec_unix.go` documents the fork window: a descriptor marked close-on-exec is still inherited
  until the child execs. `LOCK_UN` before the close is the established remedy (`gofrs/flock`'s `Unlock`, and
  `cmd/go`'s lockedfile, which the commit cites).
- **Scope**: every hunk serves the issue. Procreg is the same faulty construct in the code path that does the same
  job, and its owner, the process driver, forks a worker for each start. No test was weakened or deleted.
- **Conventions**: errors are joined (`errors.Join`). The `//nolint:gosec` lines carry a reason and match the
  existing `LOCK_EX` lines. Imports are at the top level. The two comments explain *why* and cite the issue.
  The test-only accessors live in `export_test.go`, following the package's `SetClock` precedent.
- **ADRs**: no ADR file was touched. Close still releases the lock for the next writer, as ADR-0202 and the
  backup target's single-writer contract expect.
- **Checks**: `go vet` is clean and `golangci-lint` reports 0 issues on the touched packages.
- **Shape**: the subject is `fix(backup):`, the body has `Fixes #932` and the attribution trailer, and the commit
  covers one issue.

Not rerun: the issue's own `TestStoreHeldConflict` lives in `internal/upgrade`, which is not on `origin/main` yet
(#920). The descriptor-copy test reproduces the mechanism behind its message. The group gate and CI cover the rest.

### Definition of Done

11 of 12 items hold. Item 10 (reuse) holds only in part (Minor 1). Item 8 is verified for the touched packages
(build, vet, lint, `-race` tests). The Linux lint and the e2e checks belong to the group gate.

### Model scorecard

claude-opus-5-5, fix phase: pass, 0 blockers, 0 majors, 2 minors, 2 attributed to the model.

### Recommendation

Merge as is. Follow-ups, outside this fix: (a) consider moving the `backup` and `procreg` locks onto `gofrs/flock`;
(b) file the Badger `release` window (no `LOCK_UN` before the close) upstream or as a tracked issue.
