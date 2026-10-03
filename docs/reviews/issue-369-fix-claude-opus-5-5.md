# Fix review — issue #369 (DNS correlator has no ceiling on live entries)

- **Change**: branch `fix/i369`, commit `edc80ad fix(egress): cap the DNS correlator's live pairs per worker`
- **Files**: `internal/network/egress/forwarder.go`, `internal/network/egress/egress_test.go`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**

## Summary

The correlator now keeps a per-source view (`bySrc`) of its (dst, domain) pairs and caps each worker source
at `maxPairsPerSource` (1024) live pairs. When a worker reaches the cap, `shed` drops that worker's expired
pairs, then its earliest-expiring live pairs, until three quarters of the cap remain. Then the new pair is
added. A shed pair fails closed: `DomainsFor` returns nothing for it, so a domain rule cannot permit the
connection. One worker's flood displaces only that worker's own records. This change removes the cause named
in the issue: live pairs had no upper bound. Memory is now bounded per worker on the subnet.

## Verification run

| Check | Result |
|---|---|
| Revert (`git revert --no-commit edc80ad`, keeping the new test) | `TestIssue369_CorrelatorCapsLivePairsPerSource` FAILS: `"20001" is not less than or equal to "1024"` ("one worker's live pairs are capped"). This is the issue's reason. |
| Fix restored (`git reset --hard edc80ad`) | `go test -race -count=1 ./internal/network/egress/` passes (`ok`) |
| `go vet ./internal/network/egress/` | clean |
| `golangci-lint run ./internal/network/egress/...` | `0 issues.` |
| Mutant M1: cap check disabled (`if false && sp.pairs >= …`) | killed by the TestIssue369 assertion "one worker's live pairs are capped" |
| Mutant M2: shed order reversed (latest-expiring first) | killed by TestIssue369 and by `TestIssue138_…/distinct-ips` ("a record live through the sweeps survives them") |
| Mutant M3: `drop` does not decrement `c.records` | killed by the TestIssue369 assertion "the pair count matches the map" |
| Worktree state after review | at `edc80ad`, clean |

The worktree was not modified by the review. Repo-wide tests, Linux lint, e2e and lanes are left to the
group gate, as the task instructs.

## Findings

### Blocker
None.

### Major
None.

### Minor
None.

## Verified correct

- **Root cause, not symptom.** The issue reports that `record` had no upper bound on live entries. The fix
  adds a hard per-source bound. It does not rely on a timeout or a longer sweep. The #138 sweep still bounds
  expired retention, and the new `drop` helper keeps `records`, `entries` and `bySrc` consistent. `sweep`
  now uses `drop` and also removes emptied sources.
- **Choice at the cap.** The issue left one choice open: evict, or stop recording. The fix evicts the
  flooding worker's earliest-expiring pair, never its newest record and never another worker's record. A
  shed pair fails closed, which keeps the trust-anchor property: only domains that funcd's own forwarder
  resolved are attested. The test asserts each of these properties.
- **Cost.** `shed` runs only at the cap and then frees a quarter of the cap. The sort costs O(cap log cap)
  once per cap/4 new pairs, which gives amortized O(log cap) per record. This matches the doc comment.
- **Scope.** Every hunk serves the issue. No existing test was weakened or deleted. `TestIssue138_…` still
  passes unchanged.
- **Reuse.** The new code uses `slices.SortFunc` and `time.Time.Compare` from the standard library. No
  existing helper in the egress package, `internal/platform` or `internal/testkit` already does this.
  `hashicorp/golang-lru/v2` is only an indirect dependency, and its expirable LRU uses one TTL for the whole
  cache, so it cannot replace the per-entry expiry and reverse index that the correlator needs.
- **Conventions.** Imports are at the top level. Doc comments state the why (the trust and memory
  constraints) without narrating each line. Naming follows the surrounding correlator code. The change adds
  no new port and no new error path, so ADR-0002 is not affected.
- **ADRs.** No ADR file was edited. The change contradicts no Decision or Contract of ADR-0117, and it
  extends §4a's bounded-memory intent to the correlator.
- **Shape.** The commit subject is `fix(egress): …`, the body contains `Fixes #369` and the attribution
  trailer, and the commit covers one issue.

## Observation (not scored)

The eviction order is earliest expiry first, so a worker that floods long-TTL names can evict its own
short-TTL policy records. This harms only that worker, and the evicted record fails closed. This is
acceptable under the issue's "evict the oldest pair" option.

## Recommendation

Pass. Hand back to `/fix` Step 8. The group gate still has to run the repo-wide checks and Linux lint.

## Fix checklist

10 of 10 applicable items hold. For item 8, Linux lint and e2e are deferred to the group gate, and the
touched package's build, vet, lint and `-race` tests pass.
