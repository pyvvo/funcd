# ADR-0112 implementation review — ingress protection (rate/size/concurrency, F75)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0112 implementation, model: claude-opus-4-8)

The implementation realizes ADR-0112's Contracts, Scenarios, and Definition of Done. All Go
verification is green, every non-deferred Scenario has a named, un-skipped, passing test, and each
judge-folded item (reject-before-wake, Content-Length-is-the-true-413-site, `key: function` head,
true LRU, `Retry-After`, additive `fault`) is present and proven. Two Minor findings only, neither
blocking (one model-attributed test-placement nit; one adr-attributed frozen-ADR prose fold-miss).

### Verification run (evidence)

| Check | Command (via `nix develop -c`) | Exit |
|---|---|---|
| build | `go build ./...` | `0` |
| tests | `go test ./internal/edge/limit/... ./api/fault/... ./internal/platform/config/ ./cmd/funcd/ ./pkg/funcd/ -count=1` | `0` (all 5 packages `ok`) |
| lint | `go tool golangci-lint run ./internal/edge/limit/... ./api/fault/... ./pkg/funcd/... ./internal/platform/config/... ./cmd/funcd/...` | `0` — `0 issues` |
| mod | `go mod verify` | `0` — `all modules verified` |

`golang.org/x/time v0.15.0` is present in the **direct** require block of `go.mod` (BSD-3-Clause),
as the ADR sanctioned (promote the previously-indirect dep). The lint `ld:` macOS-version warnings
are the toolchain linker, not the code.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor

- **The `api/fault` unit test named in the ADR Test plan was not added.** · attribution: **model** ·
  evidence: `api/fault/problem_test.go` `TestScenario_ErrorMapsToProblem` and
  `api/fault/fault_test.go` `TestAllConstructors` still enumerate only the pre-existing kinds — neither
  table includes `ResourceExhausted`→429 or `PayloadTooLarge`→413. The ADR Test plan lists
  "`api/fault` unit: the two kinds map to 429/413 in the problem table." The mapping is nonetheless
  **verified**: `internal/edge/limit/limit_test.go` asserts `429` (`TestScenarioOverRate429NoWake`)
  and `413` (`TestScenarioOverSize413`) through `fault.WriteProblem`, the Go e2e re-asserts both on the
  real listener, and the additive rows are present by inspection (`api/fault/problem.go:30-31`,
  constructors `api/fault/fault.go:107-114`). So the contract holds and is tested — just not co-located
  in the `api/fault` package the plan named. Non-blocking; fix is a two-row table extension by the
  builder, no design change.

- **Frozen-ADR Scope-prose fold-miss.** · attribution: **adr** · evidence: the ADR **Scope "In"**
  paragraph (`docs/adr/0112-...:52-56`) still prose-says the limiter is `keyed clientIP|path` and frames
  the size cap as "`http.MaxBytesReader` + a `Content-Length` fast-path". The **authoritative**
  Decision (§2/§3) and Contracts correctly changed `path`→`function` (the `/function/<name>` head) and
  made the **Content-Length fast-path the true 413 site** (`MaxBytesReader` demoted to defense-in-depth).
  The code follows the authoritative Decision/Contracts, not the stale Scope prose
  (`limit.go`: `KeyFunction`/`functionHead`, and the CL fast-path returns 413 at `limit.go:71-75` before
  `MaxBytesReader` wraps the body at `:77`). Because the ADR froze at Accepted, correcting the Scope
  prose would require a superseding ADR — disproportionate for an internal prose lag that contradicts
  nothing in the shipped behavior. Recorded, **not** model-attributed.

### ✅ Verified correct (keep it)

- **Reject-before-wake (the core property).** `pkg/funcd/funcd.go:657-658` inserts `limit.Chain(c.limits)`
  as the **last** vararg to `gateway.Chain`; `internal/gateway/middleware.go:20` wraps from the last
  vararg inward, so runtime order is `Recover → RequestID → limit → dataplane.Handler` — limit is the
  innermost middleware, executing immediately before the handler that calls the activator.
  `TestScenarioOverRate429NoWake` asserts `next.n == 2` (only the burst reaches `next`; the 4 rejects
  never do → zero wake). Rejects stay panic-guarded and `X-Request-Id`-correlated.
- **Content-Length is the true 413 site.** `limit.go:71-75` returns `413` on `r.ContentLength >
  MaxBodyBytes` **before** `next`; `MaxBytesReader` (`:77`) is defense-in-depth only.
  `TestScenarioOverSize413` drives it via `ContentLength` and asserts `next.n == 1` (oversized never
  reached `next`).
- **`key: function` keys on the `/function/<name>` head, not the raw path.** `functionHead` takes the
  first two segments; `TestScenarioRateKeyFunction` proves a rest-varying flood (`/function/x/AAA`,
  `/BBB`, `/CCC`) shares **one** bucket, while `/function/y` gets its own — no per-request bucket, no
  map churn.
- **True LRU (not FIFO) via `container/list`.** `getLocked` `MoveToFront` on hit and evicts `Back()`
  past `maxKeys`; `TestRateLimiterLRUBound` proves a hot key survives 8 cold-key evictions (its spent
  bucket is preserved, still `429`s).
- **`Retry-After` set before `WriteProblem`.** `limit.go:66` sets it from
  `math.Ceil(Reserve().Delay().Seconds())` before writing the 429; the reservation is `Cancel()`ed on
  reject so a future token isn't consumed (`:142-145`). Asserted non-empty in unit + Go e2e.
- **Additive `fault` extension.** Two kinds (`api/fault/fault.go:31,33`) + two problem rows
  (`api/fault/problem.go:30-31`) + two constructors — no existing kind, row, or constructor changed;
  they map to 429/413. `503` reuses the existing `Unavailable`.
- **Contracts honoured, conventions clean.** `Config`/`Key`/`Chain` match the ADR signatures; no `any`
  in exported sigs; zero `Config` is a genuine pass-through (`TestScenarioLimitsDisabledPassthrough`,
  100 calls all reach `next`); `503` semaphore is non-blocking acquire/`defer` release
  (`TestScenarioOverConcurrency503`); lint clean, no `panic`/`fmt.Print*` in the shipped path.
- **Config wired end-to-end.** `internal/platform/config/config.go:54-61` (`server.limits` block, with
  `key` validated `oneof=clientIP function`) → `cmd/funcd/main.go:214-223` → `funcd.WithLimits`
  (`pkg/funcd/options.go:233`). Absent ⇒ pass-through.
- **Real-containerd F75 coverage exists.** `examples/js/env-echo/funcdconfig.yaml` sets
  `server.limits.maxBodyBytes: 1024`; `e2e/env-echo.venom.yml` adds the `F75 ... oversized body is
  413'd` case (`ShouldEqual 413`). Rate-limiting (429 + `Retry-After`) is proven on the **real**
  data-plane listener by `pkg/funcd/limits_e2e_test.go` (`TestScenarioE2ELimitsRateLimit`,
  `TestScenarioE2ELimitsBodySize`), which ran green as part of the `pkg/funcd` pass above.
- **Tracking.** F75 feat row at `reviewing` (`docs/feat/0006-...:240`); ADR at `Reviewing`; the
  working-tree surface is exactly the F75 files (no unrelated extras). ADR substance is internally
  consistent with a Reviewing bump (the file is new/uncommitted as batch item C, so verified by
  inspection rather than git-diff).

### Note on the DoD Venom item (weighed, accepted)

The DoD's Venom bullet imagined *both* halves in the lane (429 loop + 413 POST). The implementation
proves **413 deterministically** in the shared `env-echo` containerd lane and moves **rate-limiting**
(timing-fragile in a Lima VM, and a risk to the lane's spaced-out assertions) to the real-listener Go
e2e. This satisfies the intent — a real containerd lane exercises F75, and the timing-sensitive 429
path is covered by a deterministic real-listener test rather than a flaky VM loop. Judged adequate;
the reported `just lima-example env-echo → final status: PASS` (incl. the F75 413 case) is taken as
evidence for the containerd lane, which was not re-run in this review (Lima/colima — env).

### Definition of Done

**6 / 6** ADR Review-checklist items hold (Config/Key/Chain + zero-passthrough; chain order + last
vararg; no-wake + CL-413; token bucket + true LRU + function head; RFC 9457 429/413/503 + Retry-After;
additive fault + config wiring + e2e + feat row advanced). All 7 Scenarios have named, un-skipped,
passing tests. The only miss is a supplementary `api/fault`-package unit test named in the Test plan
(Minor, model-attributed) whose behavior is verified elsewhere — it does not fail a checklist item.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0112 (implementation) → pass, 0 blockers / 0 majors / 2 minors,
1 model-attributed, DoD 6/6. See `docs/reviews/model-scorecard.md`.

### Recommendation

Sign off — **pass**. Stamp ADR-0112 `Reviewing → Implemented` and advance the F75 feat row. The one
model-attributed Minor (add the two rows to the `api/fault` problem-table + constructor tests) is a
cheap, non-blocking follow-up for the builder. The adr-attributed Scope-prose lag would only ever be
corrected by a future superseding ADR, not an in-place edit — leave it.
