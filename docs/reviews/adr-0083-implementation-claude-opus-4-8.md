# ADR-0083 implementation review — function-log OTLP-JSONL → Parquet compaction (`claude-opus-4-8`)

- **ADR**: [0083](../adr/0083-funclog-compaction-otlp-jsonl-to-parquet.md) — time-windowed raw→compacted compaction (FEAT-0004/F53)
- **Phase**: implementation · **Model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met; no Blockers/Majors; one env-attributed pre-existing test failure, unrelated) · 2026-06-29

## Verification (evidence — run, not eyeballed)

| check | result |
|---|---|
| `go build ./...` (darwin) | **OK** |
| `go test ./internal/funclog/compact/...` | **ok** — 8 scenario tests pass (each named `TestScenario<Name>` ↔ an ADR `scenario:`) |
| `go test ./internal/funclog/...` | **ok** (compact + the ADR-0081 funclog suite, unchanged) |
| `go test ./pkg/funcd/...` | **ok** (44.6s; incl. `TestScenarioDisabledConfigNoCompactor` — the daemon-side disabled-config half) |
| `go test ./...` | green **except** `internal/testkit/bench` `TestPythonPoolSmoke` (py pool shim "did not become ready") — **`env`-attributed**: a python-runtime readiness timeout, the bench package has **zero** references to funclog/compact (grep-verified), so it cannot be caused by this change |
| `go tool golangci-lint run ./internal/funclog/... ./pkg/funcd/...` | **0 issues** |
| `go mod verify` | **all modules verified** |
| **new deps** (license gate) | `github.com/parquet-go/parquet-go v0.30.1` (**Apache-2.0**) + transitives `parquet-go/bitpack` (Apache-2.0), `parquet-go/jsonlite` (MIT), `twpayne/go-geom` (BSD-2), `andybalholm/brotli` (MIT), `pierrec/lz4/v4` (BSD-3) — **all permissive, pure-Go, no cgo** |
| **identity/path grep** | clean on all 8 changed/new files (no local username / abs home path / email) |

## Conformance to the ADR Review checklist

1. **Raw discovered via `List("logs/")`, `*.otlp.jsonl` only, `*.parquet` skipped** — ✓ `CompactOnce` lists `logsPrefix`; `parseRawKey` rejects non-`.otlp.jsonl` (`keys.go`).
2. **Windowing `floor(sealNano/Window)*Window`; compact only when `now ≥ windowStart+Window`** — ✓ `windowStartNano` + the `nowNano < start+window ⇒ continue` guard; proven by `recent-raw-preserved`.
3. **One `Row` per LogRecord; typed columns + valid `attrs_json`** — ✓ `appendRows`; `parquet-roundtrips` asserts every column + that `attrs_json` excludes `funcd.source`/`inv` and carries the rest.
4. **Compacted via `GenericWriter[Row]`, one object per `(ns,fn,window)`, deterministic Hive key** — ✓ `writeCompacted` + `compactedKey`; `partitioned-by-ns-fn-date` asserts the exact key.
5. **Raw deleted only after the Parquet `Put`; deterministic key ⇒ retry overwrites (no dup/loss)** — ✓ ordering in `CompactOnce`; `crash-safe-no-loss` (a `Delete`-failing bucket) proves the Parquet is durable before delete and pass 2 overwrites the same key (one object, 2 rows, no loss).
6. **Retention deletes compacted older than `Retention`; `0` keeps forever** — ✓ `retention-prunes-compacted` (prunes old, keeps fresh) + `disabled-config` no-op (retention 0 keeps a 1000h-old compacted).
7. **Default-on; `WithoutLogCompaction()` ⇒ no goroutine; options-only surface (no YAML)** — ✓ `TestScenarioDisabledConfigNoCompactor`: default InMemory wires a compactor, `WithoutLogCompaction()` wires none; no `internal/platform/config` change (grep-verified).
8. **Reads/writes only through `blob.Bucket`; no DuckDB/cgo; parquet-go Apache-2.0; deps recorded** — ✓ (see table).
9. **`Run` stops on `ctx` cancel; wired like the funclog sink** — ✓ `Run` selects on `ctx.Done()`; `funcd.go` adds it as a conditional `Platform.Run` goroutine drained by `wg.Wait()`.
10. **One passing acceptance test per Scenario** — ✓ 8 scenarios → 8 tests (7 in `compact_test.go` + the daemon-side disabled-config in `pkg/funcd`).

## ✅ Verified correct (what's strong — keep it)

- **The crash-safety property is real and tested, not asserted.** `failDeleteBucket` injects a `Delete` failure: pass 1 leaves the Parquet durably written with the raw still present (no loss); pass 2 re-Puts the *same* deterministic `compactedKey` (gocloud `WriteAll` overwrites) and finishes the delete — exactly one compacted object, both rows intact. This is the load-bearing risk the judge flagged, and the test pins it.
- **Seal-time windowing invariant is honored and documented** — windowing is on the raw key's seal nanos (monotonic, ≤10s behind data), so `now ≥ windowStart+Window` closes safely with no grace; the package doc + `keys.go` flag it for F51/F52 to preserve.
- **Schema discipline** — fixed typed columns + a single `attrs_json`, with `funcd.source`/`inv` lifted to columns (the F54-relevant ones) and the remainder JSON-encoded; the `Row` struct is the stable contract F54 will read.
- **Options-only config** mirrors ADR-0081's funclog exactly (the M1 judge fix) — no spurious new YAML tier.
- **Deterministic ordering** of windows (sorted) keeps passes reproducible and tests stable.

## Findings

- **Blockers / Majors**: none.
- **Minor (follow-ons, recorded — not defects):**
  - **m1 — full-`logs/` list per pass.** Each pass lists the whole prefix (no cursor) — the ADR's own *Temporary workaround* with an exit criterion (a per-fn list / persisted cursor) measured by the homebox bench. Acceptable for the ~100-agent target.
  - **m2 — `Run` does its first pass after one `Interval`** (ticker, no immediate pass). Fine for an always-on loop; a startup pass could be added if first-compaction latency ever matters.

## Recommendation

**pass.** The compactor folds closed-window raw into deterministic, Hive-partitioned Parquet through `blob.Bucket`, deletes raw only after the durable write, prunes by retention, and is loss-/dup-safe under crash — all verified by 8 green scenario tests, 0 lint issues, a clean license gate, and a clean identity scan. The single suite failure (`TestPythonPoolSmoke`) is an environmental python-shim-readiness timeout in a package that does not reference this code. Stamp ADR-0083 `Reviewing → Implemented` and FEAT-0004/F53 → `implemented`.
