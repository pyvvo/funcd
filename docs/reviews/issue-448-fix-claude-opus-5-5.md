## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #448 fix, model: claude-opus-5-5)

Change: branch `fix/i448`, commit 3567769 `fix(function): say "list workers" when a runtime List fails`.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **The regression test's doc comment contradicts itself** · attribution: model.
  `internal/function/function_test.go:291` reads `Issue #448: the error a failed runtime List returns says
  "list workers", not "list workers".` A blanket replace of `workeres` also rewrote the misspelled word
  that the comment quotes, so the comment now states nothing. Fix: write the old spelling in the comment
  (for example `not "list workeres"`), or drop the `not …` clause.

### ✅ Verified correct (keep it)
- **The test fails without the fix, for the issue's reason.** With the `origin/main` version of
  `internal/function/function.go` in a `go test -overlay`, `TestIssue448_ListFailureSaysListWorkers` fails:
  `Error "function.instances: list workeres: test.List: runtime down" does not contain "function.instances: list workers: test.List: runtime down"`.
  (A plain `git revert --no-commit` of the commit also removes the test, so the overlay was used for the
  fail half; the worktree was then reset to 3567769 and left clean.)
- **It passes with the fix** under `-race` (`ok internal/function`).
- **Mutants are all killed** (overlay, `-run TestIssue448`):
  - `"list workers"` → `"list worker"`: fails on the message.
  - `fault.KindOf(err)` → `fault.Internal`: fails on `expected "unavailable", actual "internal"`.
  - wrap removed (`return nil, err`): fails on the message.
- **Root cause fixed**: the literal in `namedInstances` (`internal/function/function.go:1174`) is
  corrected; the error kind is still propagated with `fault.KindOf(err)`.
- **The test drives the real path**: reconciling a missing Function takes the NotFound → `teardown` →
  `namedInstances` → `runtime.List` path, and asserts both the full wrapped message and the
  `fault.Unavailable` kind.
- **Scope**: every hunk corrects the same misspelling, which the issue explicitly invites (comments in
  `function.go`, `activator.go`, `gateway/embedded/embedded.go`; test messages in `function_test.go` and
  `containerd_linux_test.go`). `git grep workeres` is now empty. No test was weakened; four assertion
  messages changed text only.
- **Reuse**: the test uses the existing `newHarness(t, opts ...func(*function.Deps))` option seam and the
  embed-and-override runtime pattern that `supervision_test.go` already uses (`createCounter`,
  `readinessListFailer`). The new `listFailer` is a four-line unconditional variant; the existing
  `readinessListFailer` fails only for a specific caller, so it does not fit this path.
- **Conventions**: `api/fault` errors (`fault.Unavailablef`, `fault.Wrapf`), ctx-first, top-level imports,
  no comment bloat. No ADR file was touched, and no Accepted ADR is contradicted.
- **Checks (touched packages)**: `go test -race -count=1` ok for `internal/function`, `internal/activator`,
  `internal/gateway/embedded`; `go vet` clean on those plus `internal/runtime/containerd`; `golangci-lint`
  reports `0 issues.` on the same set.
- **Commit shape**: `fix(function):` subject, Cause/Fix/Test body, `Fixes #448`, attribution trailer, one
  issue per commit.

### Definition of Done
11 / 11 items hold. Item 8 was checked for the touched packages on the host only (tests with -race, vet,
lint); Linux lint, the repo-wide tests and e2e are left to the group gate, as this review was scoped.

### Model scorecard
Ledger fields: claude-opus-5-5 on issue #448 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.
Not recorded here; the caller records the ledger row.

### Recommendation
Ship. Optionally correct the self-contradicting test comment (the Minor) before the group PR.
