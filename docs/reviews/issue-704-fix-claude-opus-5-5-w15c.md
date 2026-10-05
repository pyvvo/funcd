# Fix review — issue #704 (edge observability records a relayed 1xx as the final status)

- Change: branch `fix/w15c-i704`, commit 2a74bf32 `fix(edge): record the final status, not a relayed 1xx, in edge observability`
- Base: `origin/main` a394c6f1 (the merge base; the regression test was run against it with no fix)
- Producing model: claude-opus-5-5
- Governing ADRs: ADR-0114 (edge observability and shaping), ADR-0002 (conventions)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #704 fix, model: claude-opus-5-5)

### Minor 1 — the 101 carve-out is not pinned by a test  ·  attribution: model

Evidence: an overlay mutant that changes the recorder guard in `internal/edge/observ/observ.go` from
`!gateway.Interim(code)` to `code >= 200` survives (`ok internal/edge/observ`). With it, a WebSocket
upgrade (101) is no longer recorded, so the recorder keeps its default and the access log, metrics and
span report 200 / `2xx` for an upgrade. A second mutant that makes `gateway.Interim` treat 101 as
interim (drop `code != http.StatusSwitchingProtocols`) survives in all three touched packages; that
part of the gap predates this change (`shape.interim` and `commitWriter` had no 101 test on main), but
the fix gives the recorder a new 101 decision and leaves it untested.
Fix: add a 101 case (an upgrade through the observ chain, or a direct recorder test) asserting
`status_class="1xx"` and `status=101`. Not blocking: the behavior is correct today.

### ✅ Verified correct (keep it)

- **Proof on current main (user rule).** With an overlay of the `origin/main` versions of `observ.go`,
  `shape.go` and `middleware.go`, `TestIssue704_InterimStatusNotRecordedAsFinal` fails in both subtests
  for the issue's reason: `expected: []string{"5xx"}`, `actual: []string{"1xx"}` for `early-hints` (103)
  and `expect-continue` (100).
- **Passes with the fix** under `-race`, and stays green at `-count=10 -race`.
- **Both cases of the issue are tested**, each through the production middleware order
  (`Recover → RequestID → observ → shape`) behind a real `httputil.ReverseProxy` with `FlushInterval=-1`;
  the test first proves the 1xx actually reached the client (`Got1xxResponse`), then asserts the metric,
  the span and the access log all carry the final 500.
- **Cause, not symptom.** The recorder now records a status and sets `wrote` only for a final code,
  exactly the cause named in the issue. No timeout, retry or swallowed error.
- **Mutant on the shared helper's range** (`Interim` limited to 100–102) fails both
  `TestIssue704_…` and `TestIssue305_CompressionDecidesAtFinalStatusAfter1xx`.
- **Reuse.** The fix does not add a third copy of the interim rule: it moves `shape.interim` to
  `gateway.Interim` and uses it from the observ recorder, shape's `headerWriter`/`gzipWriter`, and
  `gateway.commitWriter` (whose inline `code >= 200 || code == 101` was the same rule). The test reuses
  the package's existing harness pieces (`ManualReader`, `SpanRecorder`, `observability.NewFromProviders`).
- **Siblings.** The only `WriteHeader` wrappers in non-test code are the four above; all now share one
  interim rule. No other status recorder exists.
- **Scope.** Every hunk serves the issue; no test was weakened or removed.
- **ADRs.** ADR-0114's per-request `status` / `status_class` contract is now honored for relayed 1xx;
  no ADR file was touched. `shape` importing `gateway` adds no cycle (observ already depended on it).
- **Checks (touched packages).** `go test -race` ok for `internal/edge/observ`, `internal/edge/shape`,
  `internal/gateway`; `go vet` clean; `golangci-lint` 0 issues. Repo-wide, Linux and e2e checks are left
  to the group gate.
- **Shape.** `fix(edge):` subject, `Fixes #704`, the attribution trailer, one issue in one commit.
- **Conventions.** Top-level imports, short why-comments only; no dev-machine references.

### Definition of Done

11 of 12 hold. Item 4 (reverting or mutating the key lines fails a test) holds for the revert and one
mutant but not for the 101 mutants (Minor 1). Item 8 holds for the touched packages; the Linux lint and
e2e are the group gate's.

### Model scorecard

claude-opus-5-5 · fix · pass · 0 blockers / 0 majors / 1 minor · 1 model-attributed · DoD 11/12.

### Recommendation

Merge with the group. Optionally add a 101 upgrade case to pin the carve-out (Minor 1).
