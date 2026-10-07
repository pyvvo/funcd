# Fix review: issue #830 (claude-opus-5-5, wave 12)

**Issue**: #830, "Four daemon components log through Go's default logger, not the platform's"
**Change**: branch `fix/w12-i830`, commit a265b04f `fix(observability): log the process runtime, local API, catalog proxy and KV migration through the platform logger`
**Producing model**: claude-opus-5-5
**Verdict**: **pass**
**Checklist**: 12 of 12

## Decision judged against

The decider asked for the platform logger to reach the four sites through a constructor parameter or option,
for every component that `funcd.New` builds to receive the platform logger, for no `slog.SetDefault` in
production code, and for no change to any message, key or level.

## Verification run

| Check | Result |
|---|---|
| Revert overlay: the `origin/main` bodies of `process.go`, `local.go`, `proxy.go`, `kvmigration.go` and `presets.go`, with the new parameter accepted and ignored, run against the five `TestIssue830_*` tests | All five FAIL for the issue's reason: the platform buffer does not contain the line (`worker output dropped`, `http: panic serving`, `catalog engine call failed`, `kvstore marked for its workflow`), because the line went to slog's default handler |
| With the fix, `-race -count=5 -run TestIssue830` | ok in all five packages |
| Touched non-test packages, `go test -race -count=1` (process, workernode/local, catalog/gateway, workflow, pkg/funcd, cmd/funcd, cmd/funcdctl, testkit/realshim) | all ok |
| `go build ./...`; `go vet` on the touched packages, the test-only ones included (function, gc, provider, controlplane, tests/chaos), plus `-tags e2e,chaos` for the e2e files | clean |
| `golangci-lint run` on the same packages | 0 issues |
| Mutant M1: `funcd.New` builds the InMemory runtime with `process.New(nil)` | killed (`TestIssue830_InMemoryRuntimeLogsThroughThePlatformLogger`) |
| Mutant M2: `process.Open` prefers `slog.Default()` over the given logger | killed (`TestIssue830_OpenedDriverLogsThroughItsLogger`) |
| Mutant M3: `MarkKVStoresOnce` prefers `slog.Default()` over the given logger | killed (`TestIssue830_KVMigrationLogsThroughItsLogger`) |
| Worktree after the review | clean |

E2e, repo-wide tests, Linux lint and the lanes are left to the group gate, as instructed.

## Blockers

None.

## Majors

None.

## Minors

1. **`funcdctl dev` still builds its process runtime over `slog.Default()`** (`cmd/funcdctl/dev.go:685`, and
   `devengine.New(slog.Default(), …)` at line 776). funcdctl passes no `WithLogger`, so `funcd.New` builds its
   own text logger on standard output while the dev runtime's warnings go to Go's default handler on standard
   error. The behavior is unchanged from `main` (the call now names `slog.Default()` explicitly), and the issue
   and the decision are scoped to the daemon and to what `funcd.New` builds, so this is a possible follow-up,
   not a defect of this fix. Attribution: `issue` (scope), not scored.
2. **A small test helper is written twice.** `fileLogger` appears in both `internal/runtime/process/capture_test.go`
   and `internal/workernode/local/local_test.go` with the same eight lines. It is trivial and lives in two
   test packages, so a shared `internal/testkit` helper is optional. Attribution: `model`.

## Verified correct

- **Cause, not symptom.** Each of the four direct `slog.Default()` calls is replaced by a logger that the caller
  passes (`cmp.Or(logger, slog.Default())`, the same nil fallback as the other constructors). `cmd/funcd` passes
  its configured logger to `process.Open`, and `funcd.Run` passes `p.logger` to `MarkKVStoresOnce`.
- **InMemory ordering.** `InMemory()` now sets a `processRuntime` flag, and `funcd.New` builds `process.New(cfg.logger)`
  after the options and after the default-logger step, so a `WithLogger` placed after `InMemory()` also reaches
  the runtime. A `WithRuntime` still wins. `validate` accepts the flag in place of a runtime, a failed `New`
  closes a nil runtime safely (`closeDriver` checks for nil), and no other option reads `c.runtime`.
- **Every component that `funcd.New` builds receives `p.logger`.** This was checked across the cedar PDP, the
  secrets resolver, the KV and blob facades, the local invoke manager, the egress gateway, the s3gateway, the
  catalog manager, the function, sensor, route, site, KV, identity, provider and catalog reconcilers, the
  dispatcher, the activator, eventing, the controller, gc, funclog sinks and the compactor, the control-plane
  server and the data-plane chain. `cmd/funcd` passes its logger to containerd and to `network.New`.
- **No `slog.SetDefault` in production code.** Only the tests swap the default, and each restores it with
  `t.Cleanup`. None of those tests is `t.Parallel`.
- **No log message, key or level changed.** The `component=` attributes are kept.
- **Tests.** Each regression test captures the default and the platform output separately and asserts both
  directions. The goroutine-written sinks use files or the existing `lockedWriter`, so there is no data race.
  The socket test uses `os.MkdirTemp`.
- **Scope.** The other hunks are mechanical call-site updates for the new signatures (`process.New(nil)`,
  `Open(..., nil)`). No test was weakened, and the two `TestShutdown_KeepsEgressFence*` wrappers now build their
  inner runtime explicitly.
- **Conventions.** ADR-0002 (slog only, ctx-first) holds, the imports are at the top level, and the doc comments
  are short. No Accepted or Implemented ADR was edited or contradicted.
- **Shape.** The subject is `fix(observability):`, the body has `Fixes #830` and the attribution trailer, and the
  commit covers one issue.

## Recommendation

Pass. Hand the fix back to `/fix` Step 8. The `funcdctl dev` logger is a possible follow-up if the decider wants
the dev CLI's lines to follow the same rules.
