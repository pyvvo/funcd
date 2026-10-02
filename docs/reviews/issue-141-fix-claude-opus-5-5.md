## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #141 fix, model: claude-opus-5-5)

Change: branch `fix/i141`, commit 9f594c0 `fix(activator): answer a failed worker call with problem+json logged via slog`
(`internal/activator/activator.go` +9/-2, `internal/activator/activator_test.go` +34). Checklist: 11 of 11.

### 🟡 Major
None.

### Minor
- **The sibling site the issue names is not fixed** · attribution: `issue` · `internal/gateway/embedded/embedded.go:59`
  builds `httputil.NewSingleHostReverseProxy` with no `ErrorHandler`. The issue mentions it only as "code reading
  (not run)" and scopes its title and Root cause to the activator, so leaving it out keeps this fix in scope. File
  it as a separate issue (or fold it into the error-reporting group) so it is not lost.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 9f594c0` with the test file restored,
  then `go test -race -run TestIssue141 ./internal/activator/`: `FAIL TestIssue141_WorkerFailureIsProblemJSONViaSlog`,
  `expected: 503 actual: 502` (the ReverseProxy default bare 502). Reset to 9f594c0; the worktree is clean.
- **Passes with the fix under -race.** `go test -race -count=1 ./internal/activator/`: `ok` (whole package, 1.5 s).
- **Mutants, all killed** (`-run TestIssue141`): M1 drop the `a.logger.WarnContext` line → fails
  `"" does not contain "level=WARN"`; M2 replace `fault.WriteProblem` with `w.WriteHeader(http.StatusBadGateway)` →
  fails on the status; M3 `fault.Unavailable` → `fault.Internal` → fails on the status (500 vs 503).
- **Cause, not symptom.** The root cause in the issue is the missing `ErrorHandler`; the fix sets one, so the
  stdlib default handler (bare 502, stdlib `log` line) is never reached. The test checks every part of the issue's
  expected behavior: 503, `application/problem+json`, the `urn:funcd:problem:unavailable` type, nothing written
  through the stdlib `log` package, and a WARN line through slog. The backend hijacks and closes the connection,
  which is the same EOF failure the chaos probe saw.
- **Reuse.** The fix follows the existing pattern in `internal/dataplane/dataplane.go` `serveUpstream`
  (`proxy.ErrorHandler` → `fault.WriteProblem` with an Unavailable fault), as the issue suggests. It uses
  `fault.Wrapf`, keeping the proxy error in the chain, and the activator's existing component logger. It adds no
  helper, type or dependency. Two ErrorHandler closures of three lines do not justify a shared helper.
- **Conventions (ADR-0002).** The status comes from `api/fault` (Unavailable → 503, problem+json); logging is slog
  only, with `WarnContext(r.Context(), …)` matching the file's other warnings; `const op` is the file's idiom and now
  also serves the existing invalid-upstream branch. Imports stay at the top level; the comments state the why.
- **ADRs.** No ADR file changed. The change matches ADR-0002 §3 and ADR-0016 (activator errors are problem+json);
  ADR-0142's supervision lag is left alone, as the issue requires.
- **Scope.** Every hunk serves the issue; no test was weakened or deleted. The new test is not parallel because
  it swaps the global `log` output, and it restores it in `t.Cleanup`.
- **Checks (touched package).** `go vet ./internal/activator/`: clean. `golangci-lint run ./internal/activator/`:
  `0 issues.` `gofmt -l`: clean. Repo-wide tests, Linux lint and the e2e lanes are left to the group gate.
- **Shape.** `fix(activator):` subject, `Fixes #141`, the attribution trailer, one issue in one commit.

### Recommendation
Pass. Hand back to `/fix` to open the PR, and file the `internal/gateway/embedded` ErrorHandler gap as its own issue.
