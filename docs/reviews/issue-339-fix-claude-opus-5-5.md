## Verdict: pass — 0 blockers, 0 majors  (issue #339 fix, model: claude-opus-5-5)

Change: branch `fix/i339`, commit 6085442 `fix(fault): always write valid JSON from WriteProblem`
(`api/fault/problem.go`, `api/fault/problem_test.go`; +27 / -51).

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With 6085442 reverted and the new test kept,
  `go test -run TestIssue339 ./api/fault/` fails at `problem_test.go:123`: "problem body is not valid JSON",
  and the body shows the raw `\x01` and `\x1f` bytes the old `jsonEscape` let through.
- **Passes with the fix**: `go test -race -count=1 ./api/fault/` → `ok`; the `TestIssue339_…` test runs un-skipped.
- **Cause, not symptom.** The issue names the hand-rolled concatenation plus the partial `jsonEscape`
  (`api/fault/problem.go:71`, `:98-118`). The fix removes both and encodes the existing `Problem` struct,
  whose RFC 9457 JSON tags were already there, with `encoding/json`. That is the remedy the issue proposes.
  It escapes every character below U+0020 and also covers `Type`/`Title`, which the old code wrote unescaped.
- **Mutants (overlay, `-run TestIssue339`)**: all 3 killed.
  (1) a trailing raw `\x01` after the encoded body → fails `json.Valid`;
  (2) the detail cleared before encoding → fails the decoded-equals-`ToProblem` check;
  (3) the old concatenation pattern brought back → fails `json.Valid`.
- **Scope**: both hunks serve the issue. Removing `itoa` and `jsonEscape` deletes dead helpers: a repo-wide
  search finds no other caller. The only other `itoa`, in `internal/network/egress/gateway_linux.go`, is
  its own unexported copy, and the body it writes is a constant, so it does not have this bug. No test was
  weakened or deleted; `TestWriteProblem_SetsHeaders` still passes.
- **Reuse**: the fix uses the standard library instead of a hand-rolled encoder, and reuses `Problem`
  and `ToProblem`. It adds no new helper, type or dependency. The test uses `httptest` and `json.Valid`
  from the standard library.
- **Conventions**: the `WriteProblem(w http.ResponseWriter, err error)` signature is the one listed in
  ADR-0002, and `ToProblem` remains the single place that sets the status. The `encoding/json` import is at
  the top level. The stale explanatory comment block is gone, and no new comment was added.
  The test name follows `TestIssue<N>_…`.
- **ADRs**: the change contradicts no Accepted or Implemented ADR (ADR-0002 requires RFC 9457 problem+json
  on the wire, and the output is still that). No ADR file was touched.
- **Checks (touched package)**: `go test -race` → ok, `go vet ./api/fault/` → clean,
  `golangci-lint run ./api/fault/...` → 0 issues. The repo-wide, Linux-lint and e2e runs are left to the group gate.
- **Shape**: the subject is `fix(fault): …`, the body cites the cause and the regression test, and the
  commit carries `Fixes #339` and the attribution trailer. It is one commit for one issue.
- **Behavior notes (not defects)**: `json.Encoder` adds a trailing newline and HTML-escapes `<`, `&`, `>`
  as `<` and similar. Both are valid JSON and decode to the same strings, as the test's round-trip check shows.

### Definition of Done
11 / 11 hold (fix checklist). Item 8 was checked on the touched package on the host only. The Linux lint
and repo-wide runs are deferred to the group gate by design. Misses: none.

### Model scorecard
Ledger fields (not recorded here; the orchestrator records them): claude-opus-5-5 on issue #339 (fix) → pass,
0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Sign off. Hand back to `/fix` Step 8 for the PR, which goes through the group gate.
