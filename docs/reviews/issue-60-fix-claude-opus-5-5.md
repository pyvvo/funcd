## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #60 fix, model: claude-opus-5-5)

Change: branch `fix/i60`, commit 6e5d2e1 `fix(store): stamp creationTimestamp on create and ignore the client's value`
(`internal/store/store.go`, `internal/store/store_test.go`; 55 insertions, 1 deletion).

The issue: `store.Create` set `uid`, `generation` and `resourceVersion` but never `creationTimestamp`, and
`Update` copies the stored value forward. Every object therefore kept either the zero time or the
timestamp the client sent. ADR-0048 makes the field "server-set; ignored on input", owned by the store.

The fix: `createOnce` now sets `meta.CreationTime = time.Now().UTC()` beside the other server-set fields,
unconditionally, so a client value is overwritten. `Update` is unchanged and keeps the stored value
(`meta.CreationTime = curMeta.CreationTime`). The `Store.Create` interface comment now lists the field.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **The stamp reads the wall clock directly instead of the `internal/platform/clock` port.** (`model`)
  `internal/platform/clock` is the ADR-0002 "canonical injected-dependency example" and is already used by
  the activator, funclog and the workflow engine. The store has no clock option, so the fix calls
  `time.Now()`, and the regression test has to bracket the result with `[before, after]` rather than assert
  an exact value. This matches the 13 other direct `time.Now().UTC()` calls in `internal/`, and adding a
  `WithClock` option would widen the fix's scope, so it does not block. A later change that makes the store
  time-dependent in more places should inject `clock.Clock`.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** With `git revert --no-commit 6e5d2e1`
  and the new test kept, `go test -run TestIssue60 ./internal/store/` fails:
  `unset: Create returned creationTimestamp 0001-01-01 00:00:00 +0000 UTC, want the server time in [...]`.
  That is the issue's zero timestamp.
- **It passes with the fix under `-race`.** After `git reset --hard` to the starting HEAD,
  `go test -race ./internal/store/...` is green (`store`, `store/badger`, `store/memory`). The worktree was
  left at that HEAD and clean.
- **The test covers the whole contract.** It creates one object with no timestamp and one with a forged
  timestamp, checks that `Create` returns a server time and that `Get` returns the same stored value, then
  sends a forged timestamp on `Update` and checks that the stored value survives.
- **Mutants: 2 of 2 killed.**
  - Stamp only when the client left the field unset (`if meta.CreationTime.IsZero()`): killed by the
    `forged` case (`Create returned creationTimestamp 2001-02-03 04:05:06 +0000 UTC`).
  - Drop `meta.CreationTime = curMeta.CreationTime` from `Update`: killed (`unset: Update changed
    creationTimestamp to 2001-02-03 04:05:06 +0000 UTC`). The fix relies on this pre-existing line, and the
    test now guards it.
  - Deleting the new line is the revert check above.
- **Cause, not symptom.** The stamp sits in the single store write path that both engines share, next to the
  other server-set fields. `.UTC()` also drops the monotonic reading, so the value round-trips through the
  codec unchanged (the `Get` equality check confirms this).
- **Scope.** Two hunks in `store.go` (the import and the stamp, plus the interface comment) and one new test.
  No test was weakened or deleted.
- **Reuse.** No new helper, type or dependency. The test reuses the package's `store.New(memory.New())`
  harness and `v1.NewObject`. Nothing else in the repo assigns `CreationTime`, so nothing is duplicated.
- **Conventions.** Top-level import, no comment bloat (one short test doc comment that cites the issue and
  ADR-0048), and the naming and style of the surrounding tests.
- **ADRs.** The fix implements ADR-0048's field table row and contradicts no Accepted or Implemented ADR.
  No ADR file was edited.
- **Checks (touched packages).** `gofmt -l internal/store` is empty, `go vet ./internal/store/...` is clean,
  `golangci-lint run ./internal/store/...` reports `0 issues.`, and the `-race` tests pass. No golden or
  testdata file in `internal/`, `cmd/` or `pkg/` expects the zero time. The repo-wide tests, Linux lint and
  e2e are left to the group gate.
- **Shape.** The subject is `fix(store): …`, the body has `Fixes #60` and the attribution trailer, and the
  commit covers one issue.

### Recommendation

Pass. The fix is minimal and correct, and the regression test catches both the revert and the two
mutants. The clock-injection Minor can be picked up when the store next gains time-dependent behavior.

Checklist: 11 of 11 items hold. Item 8 was checked on the host for the touched packages; the Linux lint and
e2e runs are left to the group gate. Item 11 was checked on the commit, since no PR exists yet.
