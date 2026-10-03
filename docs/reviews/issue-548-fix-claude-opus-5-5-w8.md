## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #548 fix, model: claude-opus-5-5)

Change: branch `fix/w8-i548`, commit 1483522 `refactor(services): share one TTL binding cache between the kv and blob resolvers`.
Issue kind: task. The target is its "Done when" section. The decided scope is a refactor with no behavior change: the kv and
blob resolver tests pass unchanged, the shared cache keeps the amortized sweep, and kv `Resolve` is back under funlen.

### Minor 1 — mutant on the shared cache key survives  ·  attribution: model
Mutant: `bindingKey` in `internal/services/bindcache.go` drops `alias` from the key (overlay, kv and blob packages).
Result: `ok internal/services/kv`, `ok internal/services/blob`. No test resolves two aliases of the same function inside
one TTL, so a key that merges aliases (which would hand one alias's cached binding to another) goes undetected. The gap
already existed for the two `resolverKey` copies on `origin/main`; the refactor keeps it. A small unit test of
`BindingCache` (two aliases of one function map to distinct entries) would close it. Not a behavior change and not a
blocker under the decided "tests pass unchanged" scope.

### Verified correct (keep it)
- **Done when — shared cache**: `internal/services/bindcache.go` adds `BindingCache[V]` (`Get`, `Put`, `Len`); both
  `kv/resolver.go` and `blob/resolver.go` hold `*services.BindingCache[Binding]`; the per-package `cachedBinding`,
  `minSweepLen`, `resolverKey`, `mu`, `sweepAt` and `ttl` are gone.
- **Done when — funlen and clone**: the audit's lint config (`scripts/agent/audit.golangci.yml`) on
  `internal/services`, `kv` and `blob` reports no funlen issue and no resolver clone; the only `dupl` hits are in the
  untouched `kv/reconcile_test.go` (pre-existing).
- **Done when — tests unchanged**: the diff touches only `export_test.go` accessors (`NewResolverTTL`, `CacheLen`), no
  test function. `go test -race -count=1 ./internal/services/...`: all six packages `ok`, including
  `TestIssue170_ResolverCacheEvictsExpiredEntries` in kv and blob.
- **No behavior change**: `Get` still checks `time.Now().Before(expiry)` under the lock and drops an expired entry;
  `Put` keeps the sweep trigger `len >= sweepAt`, the `!now.Before(expiry)` delete and `sweepAt = max(2*len, 64)`;
  the key is the same `ns\x00fn\x00alias`; a Forbidden miss is still never put (ADR-0073 default-deny); the 5 s default
  TTL is unchanged. The test-only `NewResolverTTL` now swaps in a cache with the given TTL, which is equivalent to the
  old `ttl` field write.
- **Mutants** (overlay of `bindcache.go`, kv and blob tests): expiry check forced true → `TestIssue170_…` FAIL in both
  packages; sweep disabled → `TestIssue170_…` FAIL in both. Alias dropped from the key → survives (Minor 1).
- **Import graph**: `kv` and `blob` already imported `internal/services` on `origin/main`; no new edge, no cycle
  (`go build ./...` ok).
- **Reuse**: no existing generic TTL cache in the repo. `internal/network/egress/forwarder.go` uses the same
  amortized-sweep idiom on a different structure (record counts, not a keyed TTL map), so it is not a duplicate; the
  `hashicorp/golang-lru/v2` module is only an indirect dependency and is not the same semantics (size-bounded LRU, not
  sweep-on-insert TTL).
- **Checks**: `go build ./...` ok; `go vet ./internal/services/...` ok; `golangci-lint run ./internal/services/...`
  0 issues; race tests above ok.
- **Conventions**: ctx-free pure type, no `any` in signatures (constraint `comparable`), typed `v1.NamespaceName` /
  `v1.ObjectName` keys, top-level imports, doc comments name #223 and ADR-0073 without narration.
- **ADRs**: no ADR file touched; the ADR-0073 default-deny (misses never cached) holds.
- **Shape**: `refactor(services):` subject fits a task, `Fixes #548`, attribution trailer, one issue in one commit;
  worktree left clean after the overlay mutants.

### Recommendation
Pass. Optionally add a `BindingCache` unit test for alias isolation in a follow-up (Minor 1).
