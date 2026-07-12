# embedengine — the process-mode DuckDB+Quack catalog engine (dev-only)

`funcdctl dev` (the `-tags dev` fat client, ADR-0125 / FEAT-0001 F90) serves a **catalog** with no
container: it `go:embed`s the DuckDB **engine** — the standalone `duckdb` CLI plus the pinned
`httpfs`/`ducklake`/`quack`/`sqlite` extensions — and runs it as a host **subprocess** exposing the
Quack endpoint. This is the dev analogue of the prod `CatalogService`, which supervises the curated
`funcd/runtime-duckdb` **container** (ADR-0086); process-dev runs no containers, so it needs the
engine as native binaries.

## The placeholder pattern (same as `internal/runtime/embedimg`)

`engine.tar.gz` committed here is a **tiny (<1 KiB) labeled placeholder**, not a real engine — it
exists only so the `go:embed` directive has a file at compile time. `go build` / `just ci` are green
on it, and `Bundled()` reports `false`, so `funcdctl dev` reports *catalog-unavailable* (with a
build-the-engine remediation) instead of launching a non-existent binary.

The **real ~130 MB per-arch bundle** (duckdb CLI + the 4 extensions) is fetched from duckdb.org and
overwrites the placeholder, **skip-worktree'd** so the multi-MB blob never dirties the tree:

```bash
just catalog-engine-pin                 # once per clone: skip-worktree the placeholder
just build-catalog-engine darwin arm64  # fetch + bundle the engine for this platform
go build -tags dev ./cmd/funcdctl       # embeds the real engine
```

CI builds it per target in `.github/workflows/release.yml` (runnable locally via `act`), exactly as
`build-runtime-images` produces the embedded runtime-image tars.

## Layout of the bundle

```
duckdb                              # the executable CLI
extensions/httpfs.duckdb_extension
extensions/ducklake.duckdb_extension
extensions/quack.duckdb_extension
extensions/sqlite.duckdb_extension
VERSION                            # "duckdb <ver>\nplatform <os>/<arch>"
```

`Extract(dir)` unpacks it and returns the `duckdb` path + the extension dir; the dev catalog driver
launches `duckdb` with the extension directory pinned and `CALL quack_serve(...)`.

The **live Quack query** through this engine is exercised by the deferred `dev-catalog-query` lane
(ADR-0125 M2); the committed test covers embed + extract + the placeholder/real distinction.
