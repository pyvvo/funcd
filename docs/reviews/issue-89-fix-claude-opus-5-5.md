## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #89 fix, model: claude-opus-5-5)

Change: branch `fix/i89`, commit 362811ef `fix(edge): bound the rate limiter's key memory under key: function`
(`internal/edge/limit/limit.go`, `internal/edge/limit/limit_test.go`). Governing ADR: ADR-0112 (Implemented),
Constraint "Bounded memory" and Consequence "no memory-DoS".

### Minor 1 — no test drives the LRU past MaxKeys and checks the map entry is deleted  ·  attribution: model
Evidence: mutant B replaced `delete(rl.entries, tail.Value.(*bucket).key)` with `delete(rl.entries, bucketKey{})`.
The map then keeps every key and only the list is bounded. `go test -count=1 ./internal/edge/limit/` still printed
`ok`, so the mutant survived. `TestIssue89_FunctionKeyMemoryBounded` sends exactly `maxKeys` (64) requests, so it
never evicts. `TestRateLimiterLRUBound` checks only that the hot bucket is kept, not that the map shrinks. The gap
existed before this fix, but the commit message says memory "is bounded by MaxKeys alone", and no test checks that
claim. Fix: in the issue-89 test, send more than `maxKeys` long-path requests (for example `4*maxKeys`) and keep the
same heap bound. The mutant would then fail the test. Non-blocking.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit 362811ef` with the new test file restored.
  Both subtests FAIL with `"4707872" is not less than "524288"` (single-segment) and `"4691936" is not less than "524288"`
  (function-name), which is about 64 × 64 KiB of retained path keys, the reported mechanism. Afterwards
  `git reset --hard 362811ef` left the worktree clean at that HEAD.
- **Passes with the fix, under -race**: `go test -race -count=1 -run TestIssue89 -v`. Both subtests PASS. The whole
  package passes under `-race` (`ok … internal/edge/limit 1.308s`), and no test was skipped.
- **Root cause, not symptom**: the map key is now `bucketKey [sha256.Size]byte`, a SHA-256 of `keyFor(r)`, so each
  entry costs a fixed size whatever the path length. Bucket semantics do not change: identical heads share a bucket,
  and a collision is not a practical risk. The fix covers both path shapes from the issue (`/<long>` and
  `/function/<long>/x`). The `clientIP` key goes through the same code, which is harmless. The transient
  `functionHead` string becomes garbage after hashing and is not retained.
- **Mutant A** (hash a constant instead of the key): `TestScenarioRateKeyClientIP` and `TestScenarioRateKeyFunction`
  FAIL, so the hashed key line is covered.
- **Scope**: both hunks serve the issue. The `rateLimiter.entries` type, the `bucket.key` type, `allow`, and the
  `getLocked` signature are the only changes. No test was weakened or deleted.
- **Reuse**: the fix uses the standard library `crypto/sha256` and the existing `container/list` LRU. It adds no new
  dependency or helper, and it does not duplicate an existing one. The test reuses the package's existing `do` and
  `counter` helpers. `liveHeap` is a small local helper and does not duplicate anything in `internal/testkit`.
- **Conventions (ADR-0002 / CLAUDE.md)**: imports are at the top level, and the only comment explains why the type
  exists (with a pointer to ADR-0112). The code has no `any` in signatures, no logging changes, and no YAML.
  `gofmt -l` prints nothing, `go vet` passes, and `golangci-lint run ./internal/edge/limit/...` reports `0 issues.`
- **ADRs**: this fix implements ADR-0112's "Bounded memory" constraint and contradicts nothing in it. No ADR file was
  edited.
- **Shape**: the subject is `fix(edge): …`, the body has `Fixes #89` and names the regression test, there is one
  issue per commit, and the attribution trailer is present.

### Recommendation
Pass. Optionally fold Minor 1 into the test, by sending more than MaxKeys requests, before the PR. Repo-wide tests,
the Linux lint and e2e are left to the group gate, as scoped.
