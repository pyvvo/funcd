# Fix review — issue #455 (egress forwarder dead `servers` field and mutex)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #455 fix, model: claude-opus-5-5)

Change: branch `fix/i455`, commit 8cd9d19 `fix(egress): drop the forwarder's dead servers field and mutex`
(`git diff origin/main...HEAD`: `internal/network/egress/forwarder.go` −7, `internal/network/egress/forwarder_test.go` +12).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### 🟡 Minor 1 — the regression test pins the whole field list, in order  ·  attribution: model

`TestIssue455_ForwarderKeepsNoServerState` asserts `require.Equal(t, []string{"listen", "upstream", "corr"}, fields)`
over `reflect.TypeFor[forwarder]()`. It catches the reintroduction of `mu`/`servers` (verified below), but it
also fails on any future legitimate field, or on a reorder of the existing three. A narrower assertion (no field
named `servers`/`mu`, or no field of type `[]*dns.Server` / `sync.Mutex`) would guard the issue without
freezing the struct. For a dead-code issue a structural test is the only kind possible, so this is a test-design
nit, not a gap.

### ✅ Verified correct (keep it)

- **The fields were dead.** On `origin/main`, `f.servers` is written once under `f.mu` in `Serve` and never read;
  a search of `internal/network/egress/` finds no other reference to `.servers` or `f.mu`. `Serve` starts and
  shuts down the servers through its local `servers` slice, which the change keeps untouched.
- **Cause, not symptom**: the issue's "Done when" (remove both fields and the locked write) is met exactly — the
  struct is now `listen`, `upstream`, `corr`, and the lock/store block in `Serve` is gone.
- **Fails without the fix**: with the `origin/main` `forwarder.go` checked over the fix's test, the test fails
  for the issue's reason — actual fields `[listen upstream corr mu servers]` vs the expected three.
  (A plain `git revert --no-commit 8cd9d19` also removes the test, which lives in the same commit, so it reports
  "no tests to run"; the non-test-file revert is the meaningful check.)
- **Passes with the fix**: `go test -race -count=1 ./internal/network/egress/` → `ok`, the new test un-skipped.
- **Mutants** (each restored afterwards):
  - M1: re-add a `servers` field to `forwarder` → the test FAILS.
  - M2: re-add a `mu sync.Mutex` field to `forwarder` → the test FAILS.
- **Scope**: two hunks, both serve the issue; no test weakened or deleted; the `sync` import stays (still used
  further down the file), so no stray import churn.
- **Reuse**: nothing new is added beyond the test; `reflect.TypeFor` has precedent in the repo
  (`internal/controlplane/inline_schema_test.go`), and testify `require` is the package's existing assertion lib.
- **Conventions**: top-level imports, a single why-comment on the test, naming `TestIssue<N>_…`; ADR-0002 is
  untouched (no signature, error or port change).
- **ADRs**: ADR-0117 (the egress forwarder) is not contradicted — the forwarder's behavior is unchanged; no ADR
  file was edited.
- **Checks** (touched package): tests with `-race` green, `go vet` clean, `golangci-lint` → `0 issues`.
- **Shape**: `fix(egress):` subject, `Fixes #455`, attribution trailer, one issue in one commit.
- Worktree left at HEAD 8cd9d19, clean.

### Definition of Done

11 / 11 applicable items hold (Linux lint, e2e and lanes are deferred to the group gate, per the run's scope;
the issue has no user-visible steps to rerun).

### Model scorecard

blockers 0 · majors 0 · minors 1 · model-attributed 1 · DoD 11/11.

### Recommendation

Pass. Optionally loosen the regression test to assert the absence of the two dead fields rather than the exact
field list.
