# ADR-0084 implementation review — thin pure-Go function-log reader + `funcdctl logs` (`claude-opus-4-8`)

- **ADR**: [0084](../adr/0084-funclog-read-funcdctl-logs.md) — log read path (reader → control-plane route → SDK → CLI), FEAT-0004/F54
- **Phase**: implementation · **Model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met; no Blockers/Majors; one env-attributed pre-existing test failure, unrelated) · 2026-06-29

## Verification (evidence — run, not eyeballed)

| check | result |
|---|---|
| `go build ./...` (darwin) | **OK** |
| `go test ./internal/funclog/logread/...` | **ok** — 5 reader scenarios (reads-compacted-and-tail, filter-since, filter-severity, limit-returns-most-recent, empty-when-none) |
| `go test ./internal/funclog/compact/...` | **ok** — ADR-0083's suite **still green** (the `DecodeJSONL` refactor is the regression guard for the "additive" claim) |
| `go test ./internal/controlplane/...` | **ok** — `tenant-scoped-own-namespace` (dev team-a → A 200 / B **403**), `operator-sees-any-namespace` (admin → 200), `logs-route-absent-when-unset` (nil reader → **404**), **and the OpenAPI golden test** (`TestSpecGeneratedFromGo`) passes with the route now in the committed spec |
| `go test ./cmd/funcdctl/...` | **ok** — `funcdctl-logs-prints`: the **full vertical** (seeded compacted Parquet → `logread` → control-plane route → SDK → CLI render), asserting `[INFO]`/`[WARN]` + oldest-first order |
| `go test ./pkg/sdk/... ./pkg/funcd/... ./api/openapi/...` | **ok** |
| `go test ./...` | green **except** `internal/testkit/bench` `TestPythonPoolSmoke` (py pool shim "did not become ready") — **`env`-attributed** (python-runtime readiness; that package has zero references to F54 code), not a regression |
| `go tool golangci-lint run` (changed pkgs) · `go mod verify` | **0 issues** · all modules verified |
| **new deps** | **none** (`parquet-go` + `pdata/plog` already direct) |
| **OpenAPI** | regenerated (`just generate`) — `getFunctionLogs` now in `api/openapi/funcd.v1alpha1.yaml`; the route is documented, not just served |
| **identity/path grep** | clean on all 17 changed/new files |

## Conformance to the ADR Review checklist

1. **Merges compacted `.parquet` (`parquet.Read[compact.Row]`) + raw `.otlp.jsonl` (`compact.DecodeJSONL`) under `logs/<ns>/<fn>/`; sorted ascending; tail `rows[len-Limit:]`** — ✓ `logread.go` `Read`; `reads-compacted-and-tail` + `limit-returns-most-recent` (asserts m7..m9, the tail not the head).
2. **Since / MinSeverityNumber / Limit honored; Limit defaulted (1000) + capped (10000)** — ✓ `filter-since`, `filter-severity`, the `DefaultLimit`/`MaxLimit` clamps.
3. **empty-when-none ⇒ empty slice, not error** — ✓ `empty-when-none`.
4. **Authorizes `auth.Request{Identity, VerbGet, KindFunction, namespace}` BEFORE reading; dev/viewer bound-ns only, admin all; no client-asserted ns** — ✓ `authorizeLogs` runs first; the two tenant scenarios prove A 200 / B 403 / admin all. The readable ns is the typed `{namespace}` path param (DNS-label-validated by huma — traversal impossible).
5. **Route registered only when `Deps.Logs != nil`; absent ⇒ 404, not crash** — ✓ `logs-route-absent-when-unset`.
6. **`pkg/sdk.Logs` + `funcdctl logs` print lines; `--since` accepts RFC3339 or duration** — ✓ `parseSince` (both forms); `funcdctl-logs-prints`.
7. **Pure-Go, no cgo, no DuckDB; no new deps; reads only through `blob.Bucket`** — ✓.
8. **`compact.DecodeJSONL` wraps the existing per-line loop unchanged; `readRaw` calls it; ADR-0083 tests stay green** — ✓ (verified: the compact suite is unchanged-green).
9. **One passing acceptance test per Scenario** — ✓ 8 scenarios → 8 tests across `logread`/`controlplane`/`funcdctl`.

## ✅ Verified correct (what's strong — keep it)

- **Tenant isolation is enforced and proven, not asserted.** `authorizeLogs` runs the same RBAC PEP as CRUD *before* any blob touch; `tenant-scoped-own-namespace` pins dev-team-a → team-b = **403**, and the namespace is the huma-validated path param (never a client-asserted filter). This is the highest-stakes axis and it is airtight.
- **The DecodeJSONL refactor is genuinely additive** — the compactor's per-line loop moved verbatim into an exported function `readRaw` now calls; ADR-0083's frozen behavior is unchanged (its suite is the regression guard, and it stays green).
- **Tail semantics correct** — `rows[len-Limit:]` returns the most-recent N oldest-first; the test asserts `m7,m8,m9` (not `m0..m2`), pinning the easy-to-invert detail the judge flagged.
- **The OpenAPI gap was closed properly** — rather than leave the served route undocumented, `RegisterStubLogs` puts it in the committed spec (specgen + golden test) while runtime mounting stays gated on a blob substrate.
- **Live tail** — merging the not-yet-compacted raw JSONL means a just-emitted log is visible before compaction (`reads-compacted-and-tail`).

## Findings

- **Blockers / Majors**: none.
- **Minor (follow-ons, recorded — not defects):**
  - **m1 — bounded full-prefix scan, no index.** Each read lists `logs/<ns>/<fn>/` and reads all matching objects, then filters/caps — the ADR's own *Temporary workaround* with a concrete exit (date-prefix pruning from `Since`, Parquet pushdown), measured on the homebox bench.
  - **m2 — `-f`/follow deferred** (per the ADR scope) — point-in-time only; streaming is a follow-on.
  - **m3 — `pkg/sdk.Logs` returns the internal `[]logread.Line`.** Per the ADR contract + verified import discipline (pkg→internal is allowed here); an external caller can range the result but can't name the type. A future public DTO is a thin follow-on if the SDK's surface is hardened.

## Recommendation

**pass.** The read path is implemented end to end on funcd's own primitives — reader (parquet-go + plog over `blob.Bucket`, no DuckDB/cgo) → RBAC-scoped control-plane route → SDK → `funcdctl logs` — with 8 green scenario tests, the security model proven (403/200/operator-all), the F53 regression guard green, lint 0, no new deps, the OpenAPI spec regenerated, and a clean identity scan. The single suite failure is the environmental python-pool-shim flake, unrelated to this code. Stamp ADR-0084 `Reviewing → Implemented` and FEAT-0004/F54 → `implemented`.
