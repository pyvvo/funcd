# ADR-0164 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: ADR-0164, rate limit per resolved target and a bucket table that never resets a drained bucket
  (`Realizes: FEAT-0006/F75`)
- **Work**: branch `feat/adr-0164-rate-key-per-target`, one commit `b730e74b` on `origin/main` 1193be63
  (18 files, +476/−153)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**. No Blockers or Majors. There is one Minor finding (attributed to the model): a test-coverage gap.
- **Status**: not advanced here. The wave's docs PR stamps the ADR and the F75 row.

## Verification run (worktree, `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` touched packages (`internal/edge/limit`, `internal/dataplane`, `internal/platform/config`, `cmd/funcd`, `internal/function`, `pkg/funcd`) | exit 0 |
| `GOOS=linux go vet`, the same packages | exit 0 |
| `golangci-lint run`, the same packages | `0 issues.`, exit 0 |
| `GOOS=linux golangci-lint run` (host binary through `go tool -n`, as in `scripts/agent/gate.sh`) | `0 issues.`, exit 0 |
| `gofmt -l` on the changed Go files | empty |
| `go test -race -count=1` on `internal/edge/limit`, `internal/dataplane`, `internal/platform/config`, `internal/function` | all `ok`, exit 0 |
| `go test -race -count=1` on `pkg/funcd` and `cmd/funcd` (non-e2e) | `ok` (7.9 s, 11.4 s), exit 0 |
| e2e (`TestScenarioE2ELimitsRateLimit`, `TestIssue87_FnToFnInvokeBypassesIngressLimits`) | not run: this run excludes e2e, and the PR gate's `just ci-full` runs these tests (see the note below) |

### Overlay mutants (`go test -overlay` with mutated copies of the files; the worktree is unchanged)

| # | Mutation | Expected killer | Result |
|---|---|---|---|
| A | `internal/dataplane/dataplane.go`: `if !internal && s.limiter.Throttle(…)` → `if s.limiter.Throttle(…)` | `TestScenarioFnToFnNotRateLimited` | **killed** (expected 200, actual 429) |
| B | `internal/edge/limit/limit.go`: the full-table guard `if root.fullAt.After(now)` → `if false && …` (true-LRU eviction) | the Decision 3 tests | **killed** by `TestScenarioFullMapRefusesNewKey`, `TestScenarioClientIPDrainedBucketSurvivesIPFlood` and `TestBucketTableEvictsOnlyRefilledBuckets` |
| D | `limit.go`: `chain` rate step `cfg.Key != KeyFunction` dropped | `TestChainSkipsRateUnderFunctionKey` | **killed** (expected 200, actual 429) |
| C | `limit.go`: `heap.Fix(&t.byFull, b.index)` after a take on a known bucket → no-op | — | **survived** (whole `limit` package `ok`), see Minor 1 |

Three of the four mutants were killed. Mutant C was a probe for a test gap, and it survived.

## 🔴 Blockers

None.

## 🟡 Majors

None.

## Minor

1. **No test fails when `heap.Fix` is removed after a take on a known bucket** (attribution: `model`). Evidence: mutant
   C survives the whole `internal/edge/limit` suite. The code is correct (`limit.go`, `take`: `fullAt` is
   recomputed from `TokensAt`, then `heap.Fix`; this is Review checklist item 3). However, no test arranges two buckets
   so that a re-taken bucket must move down the heap. Without the fix, a stale root can refuse a new key with
   "no free bucket" while another bucket has already refilled. The result is spurious 429s for new keys. No drained
   bucket is reset, because eviction still checks the root's own `fullAt`. To kill the mutant, add one table case:
   `burst: 2`, `ratePerMin: 60`, `maxKeys: 2`, a manual clock; take A at t0, take B at t0+0.5 s, take A at t0+0.6 s
   (A's `fullAt` moves to t0+2.0 s); then a new key C at t0+1.6 s must be served (B refilled at t0+1.5 s).

## ✅ Verified correct (keep it)

- **Contracts match the code.** `KeyClientIP`/`KeyFunction` comments; `Config.MaxKeys` (`≤ 0 ⇒ 4096` through
  `defaultMaxKeys`); `Chain` signature unchanged; `TargetLimiter struct{ t *bucketTable }`; `NewTargetLimiter`
  returns nil unless `RatePerMin > 0 && Key == KeyFunction`; `Throttle(w, ns, name) bool` with a nil-receiver
  pass-through; `bucketTable`/`bucket`/`fullHeap` fields as specified; `take(key) (time.Duration, error)`;
  `dataplane.Handler(st, act, rtr, enf, lim, stat, defaultTimeout, logger)` follows the ADR-0151 ordering the Contracts
  allow; `pkg/funcd/funcd.go` `dpCore` passes `limit.NewTargetLimiter(c.limits)`; the config field carries
  `json:"maxKeys,omitempty" env:"FUNCD_LIMITS_MAX_KEYS" validate:"min=1"`, `defaults()` sets the literal 4096
  (no `internal/platform` → `internal/edge` import), and the section comment is rewritten as specified;
  `cmd/funcd` `limitsConfig` is a pure mapping that carries `MaxKeys`; `examples/funcdconfig.yaml` has the line.
- **Placement (Decision 1, checklist item 2).** In `serveFunction`, `Throttle` runs after the PEP block and the
  `store.Get` check (and after the observ target fill, so a 429 carries the target label, as Consequences states). It runs
  before the ADR-0134 body read, the ADR-0151 deadline and `activator.WithFunction`. The static and Upstream
  branches of `ServeHTTP` return before `serveFunction`. Internal calls skip the check, and
  `internal/workernode/local/invoker.go` marks fn-to-fn calls with `dataplane.WithInternal`. The key is
  `<namespace>/<function>` and contains no path or host.
- **Bucket table (Decision 3).** For a new key, the table evicts only when it is full, and only the heap root, and only
  when `root.fullAt ≤ now`. Otherwise the request gets 429 with the root's remaining time. A refused take calls
  `CancelAt(now)` and returns before `fullAt` or the heap changes. The code has one mutex, a map lookup and a heap
  pop/fix, and no scan. `container/list` is gone from the package (grep finds nothing).
- **The 429 (Decision 4).** One helper, `writeTooMany`, serves both modes. It sets `Retry-After` (rounded up to
  whole seconds) before `fault.WriteProblem`. Both details match the ADR word for word. `TestScenarioFullMapRefusesNewKey`
  asserts `Retry-After: 1`, the detail and the `resource-exhausted` URN.
- **Every scenario has one passing `TestScenario<Name>`.** The tests are route-paths-share-target-bucket,
  tenants-do-not-share-bucket, junk-names-create-no-bucket, drained-bucket-survives-flood, full-map-refuses-new-key,
  client-ip-drained-bucket-survives-ip-flood, max-keys-from-config (file, env, `0`/`-1` → `fault.Invalid` naming
  `server.limits.maxKeys`) and fn-to-fn-not-rate-limited. The extras follow the plan:
  `TestBucketTableEvictsOnlyRefilledBuckets` (drained, partly refilled and refilled cases, on a manual clock through
  `export_test.go`), `TestLimitsConfigCarriesMaxKeys`, and `TestIssue89_FunctionKeyMemoryBounded`, moved to
  `Throttle` with 64 KiB names. `TestScenarioRateKeyFunction` and `TestRateLimiterLRUBound` are removed as planned.
- **The junk test really pins Decision 2.** It uses `maxKeys: 2` at 1 token/min, so if a junk 404 minted a bucket,
  f1 would get the full-table 429.
- **#87 / E2E rate test, by reading.** `TestScenarioE2ELimitsRateLimit` uses `KeyClientIP`, so it still goes through
  `Chain`, with the same semantics. The `rate` subtest of `TestIssue87_FnToFnInvokeBypassesIngressLimits` uses
  `KeyFunction`: the external greeter call takes greeter's token, and the nested invoke is internal and skips
  `Throttle`. Both should stay green. The PR gate's `ci-full` confirms this.
- **Conventions.** The two `//nolint:forbidigo` lines on `Push`/`Pop` give the `container/heap.Interface` reason. The
  code has no `panic` and no unchecked type assertion (`b, _ := x.(*bucket)`). The package doc and the comments cite
  ADR-0164. The commit message is a conventional commit that lists the scenario tests.

## Notes (not scored)

- `drained-bucket-survives-flood` under `key: function` holds because of Decision 2 alone (junk names never reach the
  table). The function-mode Decision 3 path is covered by `TestBucketTableEvictsOnlyRefilledBuckets`, which runs on
  `TargetLimiter`, and the clientIP flood scenario covers Decision 3 end to end.
- `Config.Validate()` is exported for hand-built configs. A zero `Config` that skips `defaults()` now fails on
  `server.limits.maxKeys` (`min=1`), as the ADR specifies. No caller in the tree other than `Load` does this.

## Recommendation

Pass. You can merge this with the wave's PR. Fix Minor 1 (one table case) while the branch is open, or in a follow-up. It does not
block the merge. The wave docs PR moves ADR-0164 `Reviewing → Implemented`, moves F75 → `implemented`, and adds the
ADR-0112 `Superseded in part by: ADR-0164` back-link.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0164",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 14,
  "dod_total": 14,
  "report": "docs/reviews/adr-0164-implementation-claude-opus-5-5.md",
  "notes": "all contracts match; Throttle after PEP+store.Get, before body read/deadline/activator, internal and static/Upstream skip it; heap table evicts only refilled buckets; 8/8 scenario tests pass under -race; build/vet/lint green on host and Linux; 3/3 key-line mutants killed; heap.Fix after a known-bucket take not pinned by any test (surviving probe mutant) (model); #87/E2E rate tests verified by reading only, e2e left to the PR gate (env)"
}
```
