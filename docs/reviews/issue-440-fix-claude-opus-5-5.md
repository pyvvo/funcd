## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #440 fix, model: claude-opus-5-5)

Change: branch `fix/i440`, commit 16e50cd `fix(dataplane): log edge upstream failures through the data plane's slog logger`
(`internal/dataplane/dataplane.go` +4/-1, `internal/dataplane/upstream_test.go` +57).

Issue #440: the edge upstream proxy (ADR-0138) answered a failed upstream call with a 503 problem but logged
nothing, and its ReverseProxy had no `ErrorLog`, so its own errors went through the stdlib `log` package.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The mid-body case pins a stdlib-internal log line count** · attribution: model ·
  `internal/dataplane/upstream_test.go` asserts `warns: 2` for the truncated-body upstream. Without the fix the
  stdlib output shows the two lines: `httputil: ReverseProxy read error during body copy: unexpected EOF` and
  `suppressing panic for copyResponse error in test; copy error: unexpected EOF`. The second line exists only
  because the request has no server context (an `httptest.NewRecorder` call), and it is ReverseProxy's internal
  wording. A Go release that drops or merges that line breaks the exact count, though the fix still holds.
  Fix: assert at least one WARN line and that every line names the upstream, or drive the request through an
  `httptest.Server`. Not blocking: the toolchain is pinned by the flake.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 16e50cd` with the new test kept:
  both subtests fail. `"" does not contain "connection refused"` (no log line for the failed call), and
  `"" does not contain "read error during body copy"`, plus `Should be empty, but was … httputil: ReverseProxy
  read error during body copy …` (the error went to the stdlib logger). Then `git reset --hard 16e50cd`; the
  worktree is clean.
- **Passes with the fix under -race.** `go test -race -count=1 -run TestIssue440_ -v ./internal/dataplane/` →
  both subtests PASS, `ok`.
- **Mutants (3/3 killed)** by overlay on `dataplane.go`, running `TestIssue440_|TestScenarioDataPlaneUpstream`:
  removing the `proxy.ErrorLog` line fails the mid-body subtest. Removing the `WarnContext` line in
  `ErrorHandler` fails the unreachable subtest. Replacing `s.logger.With("upstream", …)` with `s.logger` fails
  both subtests (the upstream attribute is missing).
- **Cause, not symptom.** The fix removes both causes the issue names (`dataplane.go:239-242`): `ErrorHandler`
  now logs `perr` once at WARN, and `ErrorLog` routes ReverseProxy's own errors through the same slog handler.
  The response contract is unchanged: still a `fault.Unavailable` problem (503), and
  `TestScenarioDataPlaneUpstreamUnreachable` still passes.
- **Reuse, no duplication.** The change uses the existing `Server.logger` (component `dataplane`) and the
  standard library's `slog.NewLogLogger`. It follows the exact pattern already in
  `internal/catalog/gateway/proxy.go` (`rp.ErrorLog = slog.NewLogLogger(log.Handler(), slog.LevelWarn)`) and the
  activator's `ErrorHandler` (`internal/activator/activator.go`, `WarnContext(…, "upstream call failed",
  "upstream", …, "error", perr)`), which is what the issue asks for (#378, #379). No new helper, type or
  dependency. The three call sites are each one or two lines, so a shared helper would not be worth it.
- **Conventions (ADR-0002, CLAUDE.md).** slog only, ctx-first logging (`WarnContext(pr.Context(), …)`), `api/fault`
  for the response, top-level imports in the test, one short "why" doc comment on the test, no comment bloat.
  The test is deliberately not parallel because it swaps `log.SetOutput`, and it restores the output in
  `t.Cleanup`. Both test servers are closed.
- **Scope.** Every hunk serves #440. No test was weakened or deleted, and no ADR file was touched. ADR-0138's
  decision (a trusted in-daemon upstream, no edge PEP, 503 on failure) is unchanged.
- **Checks (touched package).** `go test -race -count=1 ./internal/dataplane/` → `ok`. `go vet
  ./internal/dataplane/` → clean. `golangci-lint run ./internal/dataplane/...` → `0 issues`, exit 0. The
  repo-wide set, the Linux lint and the e2e suite are left to the group gate.
- **Shape.** The subject is `fix(dataplane): …`, the body has `Fixes #440`, the commit has the Co-Authored-By
  trailer, and there is one issue per commit.

### Definition of Done
11 / 11 items hold. Item 8 holds for the touched package (host build, vet, lint and `-race` tests); the Linux
lint, the repo-wide tests and e2e are deferred to the group gate by design. No misses.

### Model scorecard
To record: claude-opus-5-5 on issue #440 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ship it with the group. The Minor (an exact WARN count tied to a stdlib-internal log line) is optional
hardening and can be folded in if `/fix` touches this test again.
