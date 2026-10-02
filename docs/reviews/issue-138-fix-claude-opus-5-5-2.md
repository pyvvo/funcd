## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #138 fix, model: claude-opus-5-5, re-review round 2)

Change: branch `fix/i138`, commits 8569aaa `fix(egress): evict expired DNS correlation records in the forwarder`
and 5794bb4 `fix(egress): address review of #138` (`internal/network/egress/forwarder.go`,
`internal/network/egress/egress_test.go`).

### Round-1 findings — status
- **Major 1 (sweep expiry predicate unpinned) · model — resolved.** 5794bb4 records a long-TTL correlation before
  the churn in both subtests (`api.example.com` → `198.51.100.7` for 1h; `pinned.example.com` on the shared dst for
  24h) and asserts `DomainsFor` still returns it after the sweeps. The round-1 survivor mutant
  (`if !now.Before(exp) {` → `if now.Before(exp) || true {`) now fails both subtests:
  `expected: []string{"api.example.com"} actual: []string{}` and the `one-ip-many-names` equality.
- **Minor (bound wording) · model — resolved.** The `sweep` doc comment now reads "twice the live count at the last
  sweep (floor minSweep)", which matches `c.sweepAt = max(2*c.records, minSweep)`.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: with `forwarder.go` restored to `origin/main` (test kept),
  `TestIssue138_…` fails in both subtests: `"10001" is not less than or equal to "4000"` (distinct-ips, the issue's
  5×2000 probe plus the pinned record) and `"50001" is not less than or equal to "2000"` (one-ip-many-names). The
  literal `git revert` of both commits also removes the test (`[no tests to run]`), so the non-test file overlay is
  the meaningful check. Worktree reset to 5794bb4 afterwards; clean.
- **Passes with the fix under `-race`**: `go test -race -count=1 -run TestIssue138 -v` → both subtests PASS, not skipped.
- **Mutants killed (3/3)**: (M1) sweep deletes live records → both subtests fail on the pinned assertions;
  (M2) emptied keys kept (`delete(c.entries, k)` removed) → distinct-ips fails `10001 > 4000`;
  (M3) never reschedule (`sweepAt = 1<<30`) → both fail (`10001 > 4000`, `48979 > 2000`).
- **Cause, not symptom**: `record` added and nothing deleted; now `record` counts new (key, domain) pairs and
  triggers an amortized sweep that removes expired pairs and emptied keys, under the existing `mu`. No TTL change,
  no cap that drops live data, no read-path change.
- **Scope**: two files, every hunk serves the issue; 5794bb4 only strengthens the test and fixes a comment. No test
  weakened or deleted.
- **Reuse**: no TTL-map/eviction helper in `internal/platform`, `internal/testkit` or neighbours; the indirect
  `golang-lru/v2` expirable cache has one TTL per cache, not per-answer DNS TTLs. Built-in `max` used. Nothing duplicated.
- **Conventions**: no `any`, no new logging, top-level imports, comments state the why without narration, naming
  matches the file.
- **ADRs**: consistent with ADR-0117 (the "short TTL" mitigation now bounds retention); no ADR file touched.
- **Checks (touched package)**: `gofmt -l` clean; `go vet ./internal/network/egress/` OK;
  `go test -race -count=1 ./internal/network/egress/...` → ok; `golangci-lint run ./internal/network/egress/...` →
  `0 issues.` Linux lint, e2e and lanes are left to the group gate.
- **Shape**: `fix(egress):` subjects, `Fixes #138` on the fix commit and `Refs #138` on the review follow-up,
  attribution trailers. The two commits for one issue are expected to fold into one when the group PR is assembled.

### Definition of Done
11 / 11 items hold (fix checklist). Item 8 counted on the touched-package checks; Linux lint, e2e and lanes are
deferred to the group gate.

### Model scorecard
Not recorded here (the orchestrator records it): claude-opus-5-5 on issue #138 (fix, round 2) → pass, 0/0/0,
0 model-attributed, DoD 11/11.

### Recommendation
Pass. Both round-1 findings are resolved; hand back to `/fix` Step 8 (fold the two commits into one for the PR).
