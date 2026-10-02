## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #170 fix, model: claude-opus-5-5)

Change: branch `fix/i170`, commit 296003c `fix(services): evict expired entries from the KV and blob
binding-resolver caches` (`git diff origin/main...HEAD`: 6 files, +179/−12, all under
`internal/services/kv` and `internal/services/blob`).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **The eviction logic is copied into both resolvers** · attribution: model · evidence: the
  lookup-time `delete(m.cache, key)`, the `sweepAt` field, `minSweepLen`, and the amortized sweep loop are
  identical in `internal/services/kv/resolver.go` and `internal/services/blob/resolver.go`. The two
  `export_test.go` files (`NewResolverTTL`, `CacheLen`) are also identical. The TTL cache was already
  duplicated before this change, and the fix copies the same pattern into both files. No shared TTL-map
  helper exists in `internal/platform` to reuse. The only library option in the module graph is
  `hashicorp/golang-lru/v2/expirable`, which is an indirect MPL-2.0 dependency and would need approval
  before it became a direct one. The duplicated code is small, so this does not block the fix.
  · fix (optional follow-up): extract a small generic TTL cache that both resolvers use.

### ✅ Verified correct (keep it)
- **The test fails without the fix, for the issue's reason.** I restored the `origin/main` versions of both
  `resolver.go` files and kept the new tests. `go test -race -run TestIssue170` then failed in both
  packages: `Should be zero, but was 1` / "a Forbidden re-resolve must drop the deleted function's expired
  entry". These are the stale entries that the issue reports.
- **The test passes with the fix.** At HEAD, `go test -race -count=1 ./internal/services/kv/
  ./internal/services/blob/` passes (`ok` for both packages). The test is not skipped.
- **The root cause is fixed, not hidden.** The issue says entries are only ever inserted. The fix removes
  an expired entry when a lookup finds it, which covers the Forbidden miss on a deleted function. It also
  sweeps expired entries during an insert once the cache has doubled since the last sweep (floor 64). The
  cache stays bounded by about twice the live bindings, and each insert costs amortized O(1). The TTL
  default and the rule that a miss is never cached (ADR-0073) are unchanged.
- **Mutants** (each was run with `-run 'TestIssue170|TestResolver'` and then restored; all were killed):
  1. kv: disable the insert sweep (`if false && …`). Fails with "expired entries must be evicted".
  2. blob: drop the lookup-time `delete(m.cache, key)`. Fails with "a Forbidden re-resolve must drop…".
  3. kv: invert the sweep predicate (`now.Before`). Fails with "expired entries must be evicted".
- **Scope.** Every hunk serves #170. No existing test was weakened or deleted, and
  `TestResolverDanglingBindingForbidden` still passes.
- **Concurrency.** The lookup-time delete and the sweep both run under `m.mu`, and the `-race` run is
  clean.
- **Conventions.** The code is ctx-first and uses `api/fault` kinds, with no `any`, no new dependencies and
  top-level imports. The comments explain why, not what. The test seams are in `export_test.go`, so no
  production API was added. The deterministic negative TTL replaces sleeping past the TTL. The blob test's
  `metaReader` adapter mirrors the existing kv one. Each package has its own external test package, so the
  two cannot share it.
- **ADRs.** No ADR file is touched. ADR-0073's "cached (low-churn)" resolver contract and the rule that a
  miss is never cached still hold.
- **Checks (touched packages).** `go vet` is clean. `golangci-lint run ./internal/services/kv/...
  ./internal/services/blob/...` reports `0 issues.`, and `gofmt -l` reports nothing. Repo-wide tests, Linux
  lint and e2e were left to the group gate, as instructed.
- **Commit shape.** The subject is `fix(services): …`, the body has `Fixes #170` and the attribution
  trailer, and the commit covers one issue.
- **Revert check housekeeping.** After the checks, the worktree was reset to 296003c and left clean.

### Definition of Done
10 / 11 items hold. Miss: item 10, the duplicated eviction logic across the two resolvers (Minor,
model). Item 8 was checked for the touched packages on the host only. The Linux lint and e2e run is
deferred to the group gate.

### Model scorecard
To record: claude-opus-5-5 on issue #170 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation
Sign off. The regression test, the revert and the mutants all show that the root cause is fixed in both
resolvers. Extracting a shared TTL cache can be a later cleanup and does not need to be part of this fix.
