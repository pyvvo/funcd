# ADR-0124 implementation review — claude-opus-4-8

**Verdict: pass.**

## Verification (captured)

- `nix develop -c go build ./...` / `go vet ./...` / `go tool golangci-lint run ./...` / `go test ./...` /
  `go mod verify` → all exit 0. `go test ./cmd/funcdctl/...` → `ok`.
- Each of the 7 migrated manifests: `nix develop -c go run ./cmd/funcdctl types -f <path> -o /tmp/adr124` → exit 0.

## ✅ Verified correct — keep

- **Resolver** (`cmd/funcdctl/manifest.go` `resolveManifest`): for a single-file push it tries
  `<dir>/<stem>.funcdctl.yaml` before the generic `<dir>/funcdctl.yaml`; a directory (bundle) resolves only the generic
  name; absent ⇒ `(nil, "", nil)`. Signature unchanged; `Manifest` shape unchanged (ADR-0124 Decision 3 honored).
- **Tests** (3, passing): stem file wins over the generic **and** over a sibling function's file
  (`per-function-manifest-resolved`); generic fallback preserved (`generic-fallback`); stem-vs-generic precedence.
  Covers ADR-0124's scenarios.
- **Migrated manifests** (block-style, beside existing files, contracts verbatim from each `<name>.schema.json`):
  `fn-to-fn/{front,greeter}.funcdctl.yaml` (front carries its `spec.links` binding to greeter),
  `workflow/{hi,ingest,lo,report,score}.funcdctl.yaml` (no bindings — verified none of the `.mjs` use
  `context.blob/kv/invoke`; report preserves the hi/lo fan-in composite contract). All parse + generate types.
- Backward-compatible: the 8 already-migrated single-function examples (only `funcdctl.yaml`) still resolve.

## Findings

**None model-attributed.**

- **`env`/`adr` (deferral, not a defect): `examples/python/s3-lakehouse/` skipped** (all 3 functions). The directory is a
  **skeleton** — only a README + 4 CRDs (`bucket`/`ingest`/`transform`/`report`.yaml); there is **no handler source, no
  `src/`, no `.schema.json`, and no inline CRD contract** in the repo, so there is nothing to bake a contract from. The
  CRDs declare `runtime: python314` + `handler: handle` but no code exists. ADR-0124's DoD/plan optimistically listed
  s3-lakehouse; that migration is blocked on the **example being implemented** (handlers + contracts), not on this
  change. The two *real* multi-function examples (fn-to-fn, workflow) — those with actual handler code — are fully
  migrated. Tracked as an example-completeness follow-up.

## Attribution

0 model-attributed. The s3-lakehouse deferral is example-incompleteness (env/adr), not an implementation defect.
