# Fix review — issue #511 (model: claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #511 fix, model: claude-opus-5-5)

The issue: `Activator.forward` built an `httputil.ReverseProxy` with an `ErrorHandler` but no `ErrorLog`,
so ReverseProxy's own `logf` lines (a failed body copy) went to the stdlib `log` package. The change
(commit b85f048, `internal/activator/activator.go` + `internal/activator/activator_test.go`) sets
`rp.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelWarn)` over the activator's logger scoped with
`upstream`, and makes the existing `ErrorHandler` use the same scoped logger.

### 🔴 Blockers

None.

### 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With b85f048 reverted (test file kept),
  `TestIssue511_BodyCopyErrorLogsThroughSlog` fails on "nothing is logged through the stdlib log package":
  the captured stdlib output holds `httputil: ReverseProxy read error during body copy: unexpected EOF`,
  the exact line the issue names.
- **Passes with the fix** under `-race`, un-skipped. The worktree was reset to the starting HEAD and is clean.
- **Mutants (3/3 killed)**, run by overlay on `activator.go`:
  1. `rp.ErrorLog` line deleted → fails ("nothing is logged through the stdlib log package").
  2. `ErrorLog` built over `a.logger` instead of the upstream-scoped logger → fails ("the line carries the activator's attributes").
  3. `ErrorLog` level `LevelWarn` → `LevelDebug` → fails (the line is dropped by the Info-level handler).
- **Cause, not symptom.** The nil `ErrorLog` named in the issue's root cause is the line fixed; no error is
  swallowed and no test is weakened.
- **Scope.** Two hunks: the `ErrorLog` assignment (plus its doc-comment sentence) and the `ErrorHandler` moved
  onto the same scoped logger, which is the same attribute set it logged before (`upstream`, `error`). The
  `TestIssue141_…` test that covers the `ErrorHandler` still passes.
- **Reuse.** The fix uses the codebase's established idiom, `slog.NewLogLogger(<logger>.Handler(), slog.LevelWarn)`,
  already used by the data-plane proxy (`internal/dataplane/dataplane.go`), the catalog PEP proxy
  (`internal/catalog/gateway/proxy.go`), the worker-node and catalog servers and the platform's HTTP servers
  (`pkg/funcd/funcd.go`). No new helper, type or dependency; the test reuses the package's `newActivator`,
  `serve`, `fakeEndpoints` and `fakeScaler` harness.
- **Conventions.** slog only (ADR-0002 §6), top-level imports, a short doc comment that cites the issue, the
  `TestIssue<N>_…` name. The test is correctly not parallel because it swaps the stdlib `log` output, and it
  restores it in `t.Cleanup`.
- **ADRs.** No ADR file touched; the change brings the code in line with the existing `forward` doc comment and ADR-0002.
- **Checks (touched package).** `go test -race ./internal/activator/...` ok; `go vet` ok; `golangci-lint run ./internal/activator/...` 0 issues.
  Repo-wide tests, Linux lint and e2e are left to the group gate, as instructed.
- **Shape.** `fix(activator): …` subject, Cause/Fix/Test body, `Fixes #511`, attribution trailer, one issue in one commit.

### Definition of Done

11/11 items hold (item 8 verified on the touched package and host only; the group gate runs the repo-wide, Linux and e2e checks).

### Model scorecard

| model | verdict | blockers | majors | minors | model-attributed | DoD |
|---|---|---|---|---|---|---|
| claude-opus-5-5 | pass | 0 | 0 | 0 | 0 | 11/11 |

### Recommendation

Pass. Hand back to `/fix` Step 8; the group gate runs the repo-wide checks before the PR.
