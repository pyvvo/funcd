# ADR-0081 implementation review — function-log capture via a console-intercept side channel (`claude-opus-4-8`)

- **ADR**: [0081](../adr/0081-function-log-capture-side-channel-blob.md) — Path B log capture → OTLP-JSONL via the blob port
- **Phase**: implementation · **Model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met; no Blockers/Majors; 3 minor follow-ons recorded) · 2026-06-29

## Verification (evidence — run, not eyeballed)

| check | result |
|---|---|
| `go build ./...` (darwin) + `GOOS=linux GOARCH=arm64 go build ./internal/runtime/containerd/...` | **OK** (both; the containerd UDS path cross-compiles for the lane) |
| `go test -race ./internal/funclog/...` | **ok** — 9 scenario tests + validation, `-race` stable |
| `go test ./internal/runtime/process/...` | **ok** — `TestSetLogCaptureDeliversFd3` (a child writing fd 3 reaches the hook) + no-capture-when-unset |
| `go test ./pkg/funcd/` (incl. the e2e) | **ok** — `TestScenarioE2EFunclogCapturesBurst`: a real Node fn emits 115 logs → **≥100 OTLP records in blob** (process driver, darwin) |
| `go tool golangci-lint run` (changed pkgs) · `go mod verify` | **0 issues** · all modules verified |
| **shim suites** | Node 19/19 + funclog 3 · Python 47 + funclog 4 (the per-language harness capture contract) |
| **new dep** | `go.opentelemetry.io/collector/pdata v1.61.0` (Apache-2.0; transitively json-iterator MIT, modern-go BSD, featuregate/proto-slim Apache-2.0) — all license-clean; **pure-Go, no cgo** |
| **Lima containerd e2e** (`just lima-example-funclog`) | **all 4 testcases PASS** on real containerd: JS + Python `log-burst` deploy, invoke (emitted ≥100), and funcd captured **≥100 OTLP records each** over the **UDS** path (JS 200, Python 100) |

## Conformance to the ADR Review checklist

1. **Path B captures `console` args before `util.format`** — ✓ (`shim/nodejs/funclog.ts`; the e2e's captured records show `attrs.args: ["processing item", {"i":0,…}]` — structure preserved).
2. **Harness capture contract per language** — ✓ Node patches `console.*`, Python installs a `logging.Handler` (`print`→Path A), both channel-only (no double-capture); per-language unit tests pass.
3. **No durable buffer inside the function (freeze-safe)** — ✓ `scenario: freeze-safe-no-loss` (a blocking `Reader` pauses `Pump`, loses nothing).
4. **Both transports** — ✓ **fd 3** (process driver, `capture_test.go` + in-process e2e) **and UDS** (containerd, `setupLogChannel` + the Lima e2e), both verified.
5. **Records carry time/severity/attrs/inv/trace/span** — ✓ `scenario: correlated` + the captured records.
6. **Persist via `blob.Bucket`, OTLP-JSONL, one object/segment; no new `Blob` type, no `minio-go`** — ✓.
7. **Resource identity-tagged, `source=function`** — ✓ captured records carry `namespace/function/replica/tenant` + `source=function`.
8. **Storage centralized** — ✓ `logs/<ns>/<fn>/…` prefix.
9. **wazero `console` joins the same `Entry` pipeline in-host** — ✓ at the pipeline level (`scenario: wazero-host-console`); the wazero *driver* hook is a follow-on (see below).
10. **Pure-Go, zero-cgo, deps recorded** — ✓.
11. **One passing test per Scenario** — ✓ (Go scenarios + 2 shim scenarios + 2 e2es: in-process + containerd).

## ✅ Verified correct (what's strong — keep it)

- **The freeze-safe streaming model is real and proven** — the host drains each instance's channel continuously; a blocking `Read` pauses the `Pump` (tested), and the Lima e2e shows logs landing on real containerd without any in-function durable buffer.
- **`blob.Bucket` reuse** — no second blob abstraction, no `minio-go`; OTLP-JSONL via `pdata/plog` round-trips (the e2e reads it back with `plog.JSONUnmarshaler`).
- **The `LogCapturer` optional capability** keeps `runtime` free of the observability concern; both drivers implement it identically (fd 3 / UDS), feeding one composition-root hook.
- **The background age-flusher** (added during impl) fixes a real correctness gap (idle segments would otherwise sit in memory until shutdown) — surfaced by the e2e, not just the test.

## Findings

- **Blockers / Majors**: none.
- **Minor (follow-ons, recorded — not defects):**
  - **m1 — Path A host-pump not wired.** `NewRawReader` (Path A, raw fd 1/2) is implemented + tested, but the host doesn't yet Pump the runtime's stdout/stderr log file into funclog as Path A — only Path B (the structured channel) is host-wired. Raw output still lands in the runtime's log file as before; wiring it into funclog is a thin follow-on.
  - **m2 — daemon env-config keys deferred.** The funcd library options `WithFunclog`/`WithoutFunclog` exist (and the e2e uses them); the daemon (`internal/platform/config` + `cmd/funcd`) does not yet map `funclog.*` env/file keys, so capture is default-on with sink defaults (10s / 8 MiB). A small operability follow-on.
  - **m3 — wazero driver hook deferred.** The `Entry` pipeline accepts in-host records (tested), but no wazero runtime *capture wiring* exists yet (the wazero execution path itself is out of ADR-0081's verified scope).

## Recommendation

**pass.** The ADR's core — loss-safe Path B capture, both transports (fd 3 + UDS), both languages, OTLP-JSONL via `blob.Bucket` — is implemented and **verified on both the process driver (in-process e2e) and real containerd (Lima lane)**. The three Minors are honest follow-on increments, not defects. Stamp ADR-0081 `Reviewing → Implemented` and FEAT-0004/F50 → `implemented`.
