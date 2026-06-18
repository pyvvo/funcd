## Verdict: pass — 0 blockers, 0 majors  (ADR-0041 implementation, model: claude-opus-4-8)

Data-plane upstream connection pooling. Both data-plane reverse proxies — the activator's
`forward` (the hot path) and the gateway embedded driver — now share a tuned `*http.Transport`
(`MaxIdleConnsPerHost=256`, `MaxIdleConns=512`, `IdleConnTimeout=90s`, dial keep-alive 30s),
cloned from `http.DefaultTransport`, so upstream connections to function sandboxes are reused
instead of dialed-and-discarded. Verified by build/vet/lint/test and a re-bench that closed the
ADR-0040 port-exhaustion finding.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None. One observation (not a finding): the activator has no *dedicated* SSE streaming test of
its own — `FlushInterval=-1` streaming is exercised by the gateway's
`TestScenarioStreamingPassthrough`, and the activator forward path by
`TestScenarioColdStartBufferAndForward`. This matches the ADR plan ("the existing activator
forward/cold-start + gateway routing/streaming tests still pass"); both proxies share the same
flush mechanism, so the streaming-preserved scenario has evidence. Adequate, not a gap.

### ✅ Verified correct (keep it)
- **Build / vet**: `go build ./...` → exit 0; `go vet ./internal/activator/... ./internal/gateway/...` → exit 0.
- **Lint**: `go tool golangci-lint run ./internal/activator/... ./internal/gateway/...` → `0 issues.` (exit 0).
- **Tests**: `go test ./internal/activator/ ./internal/gateway/... -count=1` → all `ok`. Streaming/forward/routing verbose run:
  `TestScenarioStreamingPassthrough` PASS (gateway, FlushInterval=-1 preserved),
  `TestScenarioColdStartBufferAndForward`, `TestScenarioWakeWarmReturnsUpstream`,
  `TestScenarioWakeColdScalesAndReturns` PASS (activator). Pooling tests
  `TestPooledTransport` (activator) + `TestUpstreamPooled` (gateway) PASS — both assert
  `MaxIdleConnsPerHost > http.DefaultMaxIdleConnsPerHost` (= 2) and that the transport is used
  (the gateway test confirms every compiled route's `proxy.Transport == d.transport`).
- **Regression (node available, v23.11.1 — real, not skipped)**: `go test ./pkg/funcd/ ./tests/e2e/ -count=1` → both `ok`. Data-plane invoke + cold-wake work with the pooled transport.
- **`.Clone()` not struct copy** (ADR-0002 / mutex safety): both sites use `http.DefaultTransport.(*http.Transport)` then `.Clone()` — `internal/activator/activator.go:87-88`, `internal/gateway/embedded/embedded.go:39-40`. Correct: `http.Transport` holds an internal mutex; a struct copy would `go vet`-fail / be a bug. Confirmed not flagged by vet/lint.
- **Both proxies pooled** (not just one): activator `forward` sets `rp.Transport = a.transport` (`activator.go:291`), built in `New()` (`:142`) via `newPooledTransport()` (`:86`); gateway `ProgramRoutes` sets `rp.Transport = d.transport` per route (`embedded.go:64`), built in `New()` (`:38-45`).
- **`FlushInterval=-1` retained on both**: `activator.go:290`, `embedded.go:62`. Set before `Transport` on both — pooling does not buffer streams.
- **No global** (ADR-0002): transport is a struct field on each driver (`Activator.transport`, `driver.transport`), built in `New()` — no package-level var.
- **No new dependency**: stdlib `net` + `net/http` only (already imported); `go mod verify` → `all modules verified` (exit 0). `go.mod` unchanged.
- **Tenant-isolated (Multi-tenancy & security analysis sound)**: `http.Transport` keys its idle-conn pool by `scheme://host:port`. Confirmed `internal/function/function.go:435-453` `upstreamFor` yields `http://<host>:<port>` where host is the per-replica `in.IP` (or synthetic per-function host) and port is the replica's resolved port — per-replica unique. The pool key *is* the tenant boundary; a pooled connection to function A is never handed to function B. Analysis is correct, including the scale-to-zero re-dial / TIME_WAIT caveats.
- **No contract / routing / isolation change**: `ProgramRoutes`, `serve`, `matchPrefix`, `stripPrefix`, `ServeHTTP`, `Wake`, `activate` unchanged in behavior; only `Transport` assignment added. Gateway/activator interfaces unchanged.
- **Re-bench (node-gated, headline claim)**: `go run ./cmd/funcd-bench --concurrency 8 --duration 3s --density 3 --out /tmp/rev41` → `[memory] 21439 req/s` vs `[file] 21711 req/s` (within ~1.3% → **file ≈ memory**), p99 ~1ms, `fits=true`. `grep -c "can't assign requested address" /tmp/rev41/report.md` → `0`. The ADR-0040 port-exhaustion finding is closed and the "file-was-disk-bound" hypothesis disproven.
- **Sustainability report** (`docs/reports/v1-sustainability.md`): well-authored, numbers consistent with the captured re-bench, correctly attributes the fix to ADR-0041, notes the tenant-safe host-keyed pool, and honestly flags caveats (process RSS proxy, Node-only, dev machine).
- **Hygiene**: identity grep (`green-0-rabbit|green-0-rabbit|/Users/`) over all changed files → no matches (exit 1). Module path `github.com/green-0-rabbit/funcd` throughout.
- **Tracking / ADR honesty**: feat rows F10 + F11 both link ADR-0041 and stay `implemented` (multi-ADR rows — correctly not walked back). The ADR's Date/status note honestly records that scope was *completed during implementation* (activator added pre-commit, multi-tenancy analysis added, re-bench validated) — and the broadened ADR is internally consistent (Scope, Decision, Contracts, Scenarios, Multi-tenancy section, Review checklist all cover both proxies). No blueprint sync owed (tuning, not architecture).

### Definition of Done
9 / 9 hold (ADR Review-checklist 5 + applicable generic DoD). Both proxies build a shared
`*http.Transport` with `MaxIdleConnsPerHost ≫ 2` and use it ✅; `FlushInterval=-1` retained,
streaming + activator + gateway tests pass ✅; isolation unchanged (host-keyed per-replica pool)
✅; no global / no new dep, re-bench shows port-exhaustion gone + file≈memory ✅; no identity/path
leak ✅. Generic: full sub-suite green, every scenario has a passing un-skipped test, real
behavior (no stubs), contracts honoured (no interface change), conventions (Clone not copy, no
global, slog, ctx-first), hygiene + tracking ✅. Misses: none.

### Model scorecard
Recorded: claude-opus-4-8 on ADR-0041 (implementation) → pass, 0/0/0, 0 model-attributed,
DoD 9/9. See docs/reviews/model-scorecard.md.

### Recommendation
Sign off. The implementation matches the broadened ADR exactly on both data-plane proxies,
preserves streaming and isolation, adds no dependency, and the node-gated re-bench gives direct
evidence the ADR-0040 finding is closed (file≈memory, 0 port errors). Stamp ADR-0041
`Reviewing → Implemented`; F10 + F11 stay `implemented`.
