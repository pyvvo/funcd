## Verdict: changes requested — 0 blockers, 1 major, 1 minor  (issue #138 fix, model: claude-opus-5-5)

Change: branch `fix/i138`, commit 8569aaa `fix(egress): evict expired DNS correlation records in the forwarder`
(`internal/network/egress/forwarder.go`, `internal/network/egress/egress_test.go`).

### 🟡 Major 1 — the sweep's expiry predicate is unpinned: a sweep that drops live records passes every test  ·  attribution: model

Mutant on `forwarder.go` `sweep`: `if !now.Before(exp) {` → `if now.Before(exp) || true {` (the sweep deletes
every record, live or not). Result: `go test -count=1 ./internal/network/egress/...` → `ok` — the whole package
passes, including `TestIssue138_CorrelatorEvictsExpiredRecords`.

Cause: in `distinct-ips` the asserted live record (`last`, recorded at the end of round 4) is written after the
final sweep, so it never passes through `sweep`; the message "a live record survives eviction" claims a property
the test does not exercise. In `one-ip-many-names` the live name is likewise the last write. So the one line that
separates "evict expired" from "evict everything" has no test. Wiping live correlations would deny legitimate
domain-policy egress intermittently (DomainsFor returns empty after each sweep) — a regression the gate must catch.

Fix (builder): add a record with a long TTL before the churn (or assert after a sweep has run with it present), and
assert `DomainsFor` still returns it after the sweeps triggered by the expired churn.

### Minor
- **Doc/commit wording of the bound** · model · `forwarder.go` sweep doc comment and the commit body say memory
  "stays within twice the live records". The real bound is `max(2 × live-at-last-sweep, 1024)` records: if the live
  set shrinks after a sweep, retention tracks the earlier live count until the next trigger. Still bounded by the
  live set (the issue's ask), so cosmetic; tighten to "twice the live count at the last sweep (floor 1024)".

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: with `forwarder.go` reverted to `origin/main`,
  `TestIssue138_…` fails in both subtests: `"10000" is not less than or equal to "4000"` (distinct-ips — the issue's
  own 5×2000 probe) and `"50000" is not less than or equal to "2000"` (one-ip-many-names — the reporter's rotating-
  answer case inverted). Worktree reset to 8569aaa afterwards; clean.
- **Passes with the fix under `-race`**: `go test -race -count=1 -run TestIssue138 -v` → both subtests PASS, not skipped.
- **Mutants killed**: (M1) keep emptied keys (`delete(c.entries, k)` removed) → distinct-ips fails `10000 > 4000`;
  (M2) never reschedule (`sweepAt = 1<<30`) → both subtests fail (`10000 > 4000`, `48977 > 2000`).
- **Cause, not symptom**: the issue's cause — `record` adds, nothing deletes — is removed: `record` now counts
  (key, domain) pairs and triggers an amortized sweep that deletes expired pairs and emptied keys. No TTL change,
  no cap that drops live data, no read-path change. `records` is incremented only on a new (key, domain) and
  decremented only in `sweep`, the sole deleter, so the counter stays exact; a refreshed existing domain does not
  double-count. Sweep runs under the existing `mu`, so it is race-free (confirmed by `-race`).
- **Scope**: two files, every hunk serves the issue; no test weakened or deleted.
- **Reuse**: no existing TTL-map / eviction helper in `internal/platform`, `internal/testkit` or neighbours
  (`activator.lastActive` is an idle tracker, not a TTL cache). `hashicorp/golang-lru/v2` is only an indirect
  dependency and its `expirable` LRU uses one TTL per cache, not the per-answer DNS TTL here — promoting it would add
  a direct dependency for a worse fit. The in-place sweep is the right size.
- **Conventions**: ctx-free pure struct as before, no `any`, no new logging, top-level imports (`fmt` in the test),
  comments state the why (daemon-lifetime forwarder) without narration; naming matches the file.
- **ADRs**: consistent with ADR-0117 (the "short TTL" mitigation now also bounds retention); no ADR file touched.
- **Checks (touched package)**: `gofmt -l` clean; `go vet ./internal/network/egress/` OK;
  `go test -race -count=1 ./internal/network/egress/` → ok; `golangci-lint run ./internal/network/egress/...` →
  `0 issues.` Linux lint, e2e and lanes are left to the group gate.
- **Shape**: `fix(egress):` subject, `Fixes #138`, attribution trailer, one commit for one issue.

### Definition of Done
10 / 11 items hold (fix checklist). Miss: item 4 (mutating the fix's key line — the sweep's expiry predicate —
fails no test) · model. Item 8 counted on the touched-package checks; Linux lint/e2e deferred to the group gate.

### Model scorecard
Not recorded here (the orchestrator records it): claude-opus-5-5 on issue #138 (fix) → changes-requested, 0/1/1,
2 model-attributed, DoD 10/11.

### Recommendation
Add a long-TTL record that lives through the sweeps and assert it survives, so the expiry predicate is pinned;
optionally tighten the bound wording. The code fix itself is correct and can stay as is — back to `/fix`.
