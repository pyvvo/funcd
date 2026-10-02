## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #305 fix, model: claude-opus-5-5)

Change: branch `fix/i305`, commit 5295632 `fix(edge): decide edge gzip at the final status, not at a relayed 1xx`.
Touched: `internal/edge/shape/shape.go` (+5/-2), `internal/edge/shape/shape_test.go` (+68).

### 🟡 Minor
- **The 101 clause of the new guard is untested** · attribution: model · evidence: mutant m1 changes
  `if code < 100 || code > 199 || code == http.StatusSwitchingProtocols {` to `if code < 100 || code > 199 {`, and
  `go test -count=1 ./internal/edge/shape/` still reports `ok`. The mutant is not equivalent. net/http treats a
  `WriteHeader(101)` as final, so with the mutant a handler that writes after a 101 would reach `decide(200)`
  in `Write` after the headers have gone out. The fix keeps the pre-fix behavior for 101 (it decides there),
  so nothing regresses, but no test pins it. Fix: add a subtest that calls `WriteHeader(http.StatusSwitchingProtocols)`
  through `shape.Chain(shape.Config{Compression: true})` and asserts that no `Content-Encoding` is set and that the body is not gzipped.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 5295632` also removes the test (same
  commit), so the check used the skill's overlay method: `origin/main`'s `shape.go` overlaid with `go test -overlay … -run TestIssue305`.
  Both subtests fail with `Should be empty, but was gzip` / "a 1xx never carries the gzip choice". That is the
  issue's mechanism: the gzip choice rides out on the interim response. The worktree was reset to 5295632 and is clean.
- **Passes with the fix under -race.** `go test -race -count=1 ./internal/edge/shape/` → `ok` (whole package).
- **User-visible behavior.** The test is the issue's own reproduction steps. A real `httputil.NewSingleHostReverseProxy`
  sits behind `shape.Chain(Compression: true)`, and the client uses `DisableCompression: true` with `Accept-Encoding: gzip`.
  It covers both triggers, 103 Early Hints and `Expect: 100-continue` → 100 Continue. It asserts that the 1xx is
  relayed, that the 1xx carries no `Content-Encoding`, and that the final 200 declares gzip and decodes to the payload.
- **Cause, not symptom.** The root cause named in the issue was `WriteHeader` calling the one-shot `decide` on every
  code (shape.go:167-170). A 1xx other than 101 now skips `decide`, so the decision is made at the final
  `WriteHeader` or the first `Write`, with the final headers. 101 still decides, which keeps the existing
  upgrade passthrough (`streaming := code == http.StatusSwitchingProtocols`). Nothing is masked: there is no retry, no timeout and no swallowed error.
- **Mutants.** m2 (`code > 199` → `code > 200`) is killed by both TestIssue305 subtests. m3 (the guard narrowed to
  `code != http.StatusEarlyHints`) is killed by the `expect-continue` subtest. m1 survives (see Minor).
- **Scope.** Every hunk serves #305: the guard, a one-line doc-comment update on `gzipWriter`, and the regression
  test. No existing test was weakened or deleted.
- **Reuse.** No new helper, type or dependency. The test uses the standard library (`httputil`, `httptrace.Got1xxResponse`,
  `httptest`) and the package's public `shape.Chain`/`shape.Config`, following the pattern of `TestIssue162_…`.
  The file has no other 1xx helper to reuse, and the inline range check is the idiomatic form (net/http uses the same test).
- **Conventions.** Imports are at the top level, there is one short *why* comment citing #305, and no comment bloat.
  The naming matches `TestIssue162_…`. There are no ADR-0002 surface changes (no exported API, error or logging change).
- **ADRs.** The change fits ADR-0114 (compression skips streaming and upgrades): 101 and event streams still pass
  through. No ADR file was touched.
- **Checks (touched package).** `go vet ./internal/edge/shape/` is clean, `golangci-lint run ./internal/edge/shape/`
  reports `0 issues.`, `gofmt -l` is clean, and the tests pass with -race. Linux lint, the repo-wide tests and e2e were deferred to the group gate.
- **Shape.** The subject is `fix(edge): …`, the body explains the cause, and the commit carries `Fixes #305`, the
  `Co-Authored-By` trailer, and one issue per commit.

### Definition of Done
10 / 11 items hold. Miss: item 4 (reverting or mutating the key lines fails a test). The revert check and two
of the three mutants are killed, but the 101 clause survives (Minor, model). Item 8 holds for the host checks of the
touched package; the Linux lint, the repo-wide tests and the lanes belong to the group gate.

### Model scorecard
To record: claude-opus-5-5 on issue #305 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation
Sign off. Optionally, before the PR, add a 101 subtest to pin the upgrade clause. This is not required for this fix.
