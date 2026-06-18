# Review — ADR-0044 (Worker pooling: `worker_threads` multi-tenant shim + bench)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0044 implementation, model: claude-opus-4-8)

The pooled `worker_threads` host is implemented, all five Scenarios have un-skipped passing
tests that exercise **real** worker threads (real `process.exit`, real `resourceLimits` OOM),
and the headline density deliverable is real and honest: **pooled ≈ 20 MB/function vs marginal
≈ 57 MB/function → a 2.8× density gain** — exactly the ADR's "honest, with isolation" claim
(not the rejected single-loop ~10×). The shim.ts→runtime.ts refactor is behavior-preserving
(all 11 single-shim tests + the full Go suite stay green). No new runtime dependency.

## Evidence (captured)

| Check | Command | Result |
|---|---|---|
| typecheck | `npm run typecheck` | EXIT 0 |
| shim+pool tests | `npm test` | EXIT 0 — **15 pass / 0 fail** (11 shim + 4 pool) |
| build | `npm run build` | EXIT 0 — `shim.mjs` 119.6kb + `pool.mjs` 124.0kb; `pool.mjs` has `worker_threads` + jtd/`maxOldGenerationSizeMb` inlined; rebuild idempotent |
| no runtime deps | `package.json` | only `devDependencies` (Hono/jtd/esbuild bundled) — no new runtime dep |
| go build | `go build ./...` | EXIT 0 |
| go vet | `go vet ./internal/bench/... ./cmd/funcd-bench/...` | EXIT 0 |
| lint | `go tool golangci-lint run ./internal/bench/... ./cmd/funcd-bench/...` | **0 issues** |
| go mod | `go mod verify` | all modules verified |
| bench smoke (node-gated) | `go test ./internal/bench/ -run TestBenchSmoke -count=1` | `ok 4.420s` — asserts `PooledPerFunctionMB > 0` and `< PerSandboxMB` |
| full suite | `go test ./... -count=1` | all green — no regression in `internal/function`, `pkg/funcd`, `tests/e2e` |
| headline | `go run ./cmd/funcd-bench --concurrency 6 --duration 2s --density 8` | EXIT 0 — see numbers below |
| identity grep | `grep -rnE "green-0-rabbit\|/Users/\|/home/…" <changed files>` | clean (EXIT 1) |

### Headline (`funcd-bench`, density 8, real numbers)
```
| marginal MB/function | 57.6 | 57.3 |
| pooled MB/function (worker_threads) | 20.2 | 20.2 |
| pool density gain (×) | 2.8× | 2.8× |
```
The density gain (~2.8×) is honest and materially below the marginal per-function cost, with
real per-handler isolation — exactly as the ADR framed the trade vs the rejected single-loop ~10×.

## Scenario → test mapping (all passing, none weakened)

- **pool-routes** → `routes each function to its own worker; unknown → 404`: 200 + correct
  per-handler body, `nope`→404, `/health/readiness`→200. ✓
- **pool-isolation** → `a faulting handler is isolated; siblings keep serving`: a throwing
  handler→**500** (worker survives), a `process.exit(1)` handler→**503** (worker exits then
  restarts), sibling `ok`→**200** with the process alive. Real status codes, not "no panic". ✓
- **pool-quota** → `a handler over its memory quota OOMs its thread`: `createPool(…, {maxOldMB:16})`,
  a genuinely allocating handler `r.status !== 200` (its thread OOMs via real `resourceLimits`),
  sibling `ok`→**200**. Real OOM, real isolation. ✓
- **pool-contract** → `enforces its embedded eventSchema (422 on mismatch)`: matching data→200,
  `{hello:123}`→**422** with `/contract/` error (handler not reached). ✓
- **pool-density-bench** → `TestBenchSmoke`: `require.Greater(PooledPerFunctionMB,0)` +
  `require.Less(PooledPerFunctionMB, PerSandboxMB)`. ✓

## Contracts conformance

- `createPool(manifest, limits?)` test seam present and used by every pool test. ✓
- Host routes `POST /function/<name>` → object→200 · none→204 · throw→500 · contract→422 ·
  unknown→404 · `GET /health/liveness`→200 · `GET /health/readiness`→200-once-all-loaded /
  503-otherwise; boot `exit(3)` → `ready` rejects (readiness fails fast). ✓
- `isMainThread`-branched single bundle (`pool.mjs`); per-handler `new Worker(thisFile,
  {workerData, resourceLimits:{maxOldGenerationSizeMb,maxYoungGenerationSizeMb}})`. ✓
- `FUNCD_POOL_MANIFEST` (JSON `[{name,artifact,handler?}]`), `FUNCD_PORT`/`FUNCD_PORTFILE`,
  `FUNCD_POOL_MAX_OLD_MB` (64) / `FUNCD_POOL_MAX_YOUNG_MB` (16) all honored. Artifact resolved
  via `pathToFileURL`, realpath entry-guard like the single shim. ✓
- Go: `Report.PooledPerFunctionMB float64` added; `Run(ctx, shimPath, poolShimPath, cfg)`
  takes the pool shim path; report renders the pooled row + a `pool density gain (×)` ratio;
  `--pool-shim` flag (default `shim/nodejs/pool.mjs`, `""` to skip). ✓

## ✅ Verified correct (keep it)

- **Real isolation, not mocked.** The pool tests spawn actual `worker_threads`; the OOM and the
  `process.exit` are real and the asserted recovery (sibling 200, restart) is real behavior.
- **Honest density number.** 2.8× with isolation, not a misleading single-loop figure — the ADR's
  "measure, don't assume" driver is satisfied with captured data.
- **Behavior-preserving refactor.** `runtime.ts` factors `resolveHandler`/`resolveSchema`/`validate`
  out of `shim.ts` with no run-guard collision; both shims re-use it; 11 single-shim tests +
  the Go launch paths (`internal/function`, `tests/e2e`) unchanged-green.
- **Conventions.** Go uses `api/fault` throughout (`Wrapf`/`Unavailablef`), `log/slog` for the
  best-effort warns, ctx-first, no `any`, no `panic`; the one `//nolint:gosec` on the `node`
  exec is justified (`poolShimPath` is platform-internal, not user input). TS has no bare `any`.
- **No new runtime dep.** `node:worker_threads` is stdlib; `package.json` carries no runtime
  `dependencies`; both bundles committed.
- **Boundary correctly deferred.** The host enforces no namespace boundary (documented in the
  file header + the ADR); enforcement is explicitly the placement follow-up ADR's job. No
  blueprint sync owed — this is a runtime experiment + bench, not a tenancy-model change.
- **Hygiene.** No identity/path leak; module path `github.com/green-0-rabbit/funcd`.

## Minor (non-blocking)

- **Contract prose vs code naming** · attribution: **adr** (prose) — the ADR Contracts line writes
  the host→worker message as `{id, data}`, but the implementation sends `{id, event}` (the full
  CloudEvent) and the worker validates `event.data`. The implementation is *richer/more correct*
  (the handler needs the whole event, not just `data`); the ADR prose is the looser of the two.
  No behavior gap; tests pass. Not the model's fault — the code is the better artifact.
- **Feat-row intermediate states skipped** · attribution: **model/process** — the F28 row went
  `idea → adr` and was never stamped `accepted`/`reviewing` even though the ADR reached
  `Reviewing`. The end state after this pass (`implemented`) is correct, so this is cosmetic
  tracking drift, not a correctness defect. Noted; corrected by the pass advancing the row.
- **Worker-side `none` reply lacks `status` symmetry** — purely stylistic; the host maps `none`
  before the status branches, so it is unambiguous. Keep.

## Definition of Done

**ADR Review checklist: 5/5 hold.**
1. Routes to the right worker, unknown→404, health gates on all-loaded — ✓.
2. Throw/exit/OOM isolated, siblings serve, process survives — ✓ (`pool-isolation`, `pool-quota`).
3. Each handler keeps the JTD contract (200/204/500/422) — ✓ (`pool-contract`).
4. Bench reports `PooledPerFunctionMB` + ratio; `worker_threads` stdlib, no new dep — ✓.
5. One-namespace-per-pool documented, enforcement deferred, no identity/path leak — ✓.

**Generic DoD: applicable items hold** — full suite green, every Scenario un-skipped+passing,
real behavior (no stubs), Contracts honored, conventions (ADR-0002) clean, deps tidy, no scope
creep (placement correctly deferred), hygiene + tracking correct.

DoD passed: **12 / 12** (5 ADR checklist + 7 applicable generic). Misses: none model-attributed.

## Model scorecard

Recorded: claude-opus-4-8 on ADR-0044 (implementation) → pass, 0 blockers / 0 majors /
3 minors, **0 model-attributed**, DoD 12/12. See docs/reviews/model-scorecard.md.

## Recommendation

**Pass — sign off.** Stamp ADR-0044 `Reviewing → Implemented` and advance F28 → `implemented`.
The three minors are non-blocking: the contract-naming one is adr-attributed prose (the code is
the better artifact, no superseding ADR needed for a runtime experiment); the feat-row drift is
resolved by this pass; the rest is style. No work loops back to the builder.
