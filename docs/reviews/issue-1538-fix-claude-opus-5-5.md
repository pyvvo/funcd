## Verdict: changes-requested — 0 blockers, 1 major, 0 minors  (issue #1538 fix, model: claude-opus-5-5)

This report reviews commit 7916b3db `fix(artifact): concurrent first uses of one artifact share one cache fill`, one
commit on `origin/main` 6cd1f353. Issue #1538: two `Materialize` calls for one digest, on a cache with no completed
pull, each pulled, and one removed, renamed over or read the dir that the other had just moved into place. The fix makes
the concurrent first uses of one cache dir join one fill through a `singleflight.Group` keyed by the cache dir.

This review ran in an agent separate from the session that wrote the fix, briefed with the issue, the commit and the
model only. Every check below was run, not read, in a detached worktree at 7916b3db. The probes were overlay test files
(`go test -overlay`) kept outside the worktree. Nothing was committed.

### Verification run

| Check | Command | Result |
|---|---|---|
| regression test, with the fix | `go test -race -run TestIssue1538_ -count=1 -v ./internal/artifact/` | PASS (1.05 s) |
| revert check | the same, with `origin/main`'s `internal/artifact/artifact.go` as an overlay | FAIL: `move pull into cache: rename …`. Without `-race`, 35 of 35 runs fail (`move pull into cache`, `remove unmarked cache dir: unlinkat …`); with `-race`, 11 of 11 |
| stability, with the fix | `go test -race -run TestIssue1538_ -count=10 ./internal/artifact/` | ok |
| the issue's own steps | an overlay test: 200 rounds of 4 concurrent calls, each round over a fresh cache | with the fix, 0 of 800 calls fail. On `origin/main`, 410 of 800 fail (`move pull into cache` 367, `holds no handler after a pull` 34, `remove unmarked cache dir` 9), close to the issue's 435 of 800 |
| one pull | an overlay test that counts pull dirs: 200 rounds of 8 calls | with the fix, every round made one pull |
| mutants | overlays of `artifact.go` | 2 of 2 killed (below) |
| package tests | `go test -race -count=1 ./internal/artifact/` | ok (37 s) |
| vet, lint, build | `go vet` on the host and with `GOOS=linux`; `golangci-lint run` on the host and with `GOOS=linux` (the host binary, as `scripts/agent/gate.sh` runs it); `GOOS=linux go build` | ok, `0 issues.` |

Mutants:

1. A new `singleflight.Group` for each call, so no call shares a fill: `TestIssue1538_ConcurrentFirstUsesShareOneFill`
   fails in 3 of 3 runs.
2. The fill's error is dropped: `TestIssue697_StalledRegistryBodyEnds` and `TestPullRefusesSingleFileTitleOutsideDir`
   fail. The stalled pull ends with `holds no handler after a pull`, and the refused title's kind is `internal`
   instead of `invalid`.

Not run, by the brief: the e2e suite, which holds the failure the issue cites (`TestScenarioAppHookRepeatsAfterRestart`),
and `scripts/agent/gate.sh`. They run once on the PR.

### 🔴 Blockers

None.

### 🟡 Major 1 — a call that joins another call's fill takes that call's context and ref, and ignores its own context  ·  attribution: model

The shared fill runs `fillCache(ctx, op, ref, digest, cacheDir)` with the first caller's `ctx` and `ref`
(`internal/artifact/artifact.go:672-674`), and `Do` blocks every other caller until that fill returns. Three overlay
probes ran on 7916b3db and on `origin/main`. Each holds the first fill at its first traced step with
`artifact.TraceDisk` (a cache root that does not exist yet makes the `SyncDir` of `MkdirAll` come before the pull), and
then starts a second call:

| Probe | With the fix | On `origin/main` |
|---|---|---|
| The first call's context is cancelled; the second call's context is live | the second call fails: `artifact.Pull: resolve target: … context canceled` | the second call succeeds |
| The second call has a 300 ms deadline; the first fill is held for 1.5 s | the second call returns after 1.6 s, with no error | it returns after 100 ms |
| Two Functions pin one digest; the first one's ref holds no layout | the second call, whose ref holds the artifact, fails: `no OCI layout at …` | the second call succeeds |

- ADR-0002 Decision 5 (Implemented) says that every blocking call honors cancellation and deadlines
  (`docs/adr/0002-source-code-conventions-and-patterns.md:196`), and its `context-cancellation` scenario says that a
  port call returns promptly with a `ctx.Err()`-derived error (`:51`). `Materialize` is the ADR-0030 port. A call that
  joins a fill no longer does either. No ADR, behavior row, issue or board card speaks of a shared fill's context.
- It is on the issue's own path. After a restart, the boot materializes under the reconcile context
  (`internal/function/function.go:1944`), and a hook call's contract check materializes under
  `context.WithTimeout(base, timeout)`, with the hook Function's `spec.timeout` (`internal/app/hooks.go:401`, through
  `internal/sensor/invoker.go:77` and `internal/function/contracts.go:129`). When the hook call leads and its deadline
  ends during the fill, the boot pass fails with that deadline: `StartFailed`, retried after a backoff, since `failPass`
  never writes Failed (`internal/function/function.go:849`). When the boot leads, the hook call waits past its own
  deadline until the fill ends. A data-plane call whose client leaves can also lead.
- The two other single-flight waits in the codebase avoid this. In the activator, "a caller whose own request ctx is
  cancelled returns promptly without aborting the shared activation" (`internal/activator/activator.go:329-330`), and
  the shared work runs under the activator's own bounded context (`:363-366`). The OIDC key set reads the keys "under a
  context no caller's end cancels" (`internal/edge/oidc/keyset.go:96-100`).
- The regression test covers none of the three cases: all its calls share one `context.Background()` and one ref.

What must hold, and `/fix` chooses how: each caller returns when its own context ends, with a `ctx.Err()`-derived
error, and no caller fails for a reason that belongs to another caller (that caller's context or its ref). One way is
`DoChan` with a `select` on the caller's context, where a caller whose context is still live starts its own fill once a
shared fill ended with the first caller's failure. Another way is a fill under a context that no caller ends, as the
key set does; that fill then outlives a stop, bounded by `registryResponseTimeout` per wait on a registry. Add a
deterministic test for each probe above: `artifact.TraceDisk` on a cache root that does not exist yet holds the first
fill before its pull.

### Minors

None.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the reported reason, and reliably**: 46 of 46 runs on the reverted
  code. It is a stress reproduction (20 rounds of 8 calls), not an injected interleaving. With the fix, its assertions
  do not depend on the schedule: only the first flight renames into the cache.
- **The cause is fixed, not masked.** Within one materializer, two fills of one cache dir no longer overlap. The
  platform builds one `OrasMaterializer` (`pkg/funcd/funcd.go:692`) and gives it to the Function reconciler, which
  serves both the boot and `ContractOf`, so the boot and a hook call's contract check share its group. A call that
  reaches the group after a fill ended still finds the marker in `fillCache` and keeps the dir
  (`internal/artifact/artifact.go:747-749`).
- **Scope and reuse.** Two files change: the group and its use, and the test beside the #1244 cache tests, which reuses
  `layoutRef`, `goodBundle`, `mkFunction` and `artifact.TraceDisk`. `singleflight` comes from `golang.org/x/sync`,
  already a direct dependency, and `internal/edge/oidc/keyset.go` uses it the same way. The change adds no dependency,
  helper or seam.
- **It is a bug.** ADR-0031 caches an artifact per digest, immutable
  (`docs/adr/0031-oci-artifact-distribution-oras.md:138`), and the comment in `fillCache` means to survive a concurrent
  call. The `docs/behavior/functions.md` row "A worker's artifact not yet cached" (ADR-0161 Consequences) still reads
  true. No issue (searched for the three error texts, `fillCache`, `singleflight` and the e2e test's name) and no board
  card holds concurrent fills.
- **No sibling.** `fillCache` is the only writer of the shared artifact cache. A Site's pull and a template's pull go
  into a dir that the caller owns (`internal/site/reconcile.go:333`, `internal/artifact/site.go`,
  `internal/artifact/template.go`). No design spec models the cache.
- **Shape.** A `fix(artifact):` subject, `Fixes #1538` and the attribution trailer, in one commit.

### Definition of Done

11 of the 12 items that apply hold; item 13 does not apply. The miss is item 9, conventions (ADR-0002 Decision 5), from
Major 1 (`model`).

### Model scorecard

Recorded: `claude-opus-5-5` on issue #1538 (fix) → changes-requested, 0 Blocker, 1 Major, 0 Minor, 1 model-attributed,
DoD 11/12. See `docs/reviews/model-scorecard.md`.

### Recommendation

Back to `/fix` for Major 1: keep the shared fill, let each caller's own context and ref decide its outcome, and test the
three probes. Everything else stands.
