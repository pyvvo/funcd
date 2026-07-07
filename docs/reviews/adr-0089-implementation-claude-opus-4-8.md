# ADR-0089 Implementation Review — Function dependency bundling (deployment-package model)

**Verdict**: **pass** — conforms to the ADR's Decision + Contracts; every non-e2e scenario has a named passing
test; the four sub-checks are green (verified independently). **Producing model**: claude-opus-4-8 ·
**Reviewed against**: ADR-0089 Contracts/Scenarios/DoD · ADR-0090 (mandatory contract) · ADR-0031/0032/0049 · ADR-0002.

## Verification (run, real exit codes)
- `go build ./...` → **0** · `go mod verify` → **0** · `go tool golangci-lint run` (artifact+funcdctl+function) → **0 issues** · `go test ./internal/artifact/... ./cmd/funcdctl/... ./internal/function/...` → **ok**.
- Full `go test ./...` → only `--- FAIL: TestPythonPoolSmoke` (internal/testkit/bench). **Pre-existing/`env`** — an unrelated Python-3.14 subinterpreter pool smoke, untouched by this change, fails identically on the clean tree. Not a regression.
- `internal/runtime/embedimg/*.tar` unchanged (no accidental real-image commit).

## Conformance (verified against the code)
- **`internal/artifact/bundle.go`** (new): `PackBundle` deterministic (sorted entries, zeroed mtime/uid/gid) → reproducible digest (ADR-0035 holds); `VerifyBundleContract` requires `__funcd_contract.json` with **both** keys (ADR-0090) **and** the entry defining the matching baked validator symbols, promoting via the existing `ContractBlob`; `PushBundle` gates *before* packing (fail-fast) and adds the `BundleTarMediaType` layer + `BundleEntryAnnotation` + the ADR-0059 contract layer. ✅
- **Security — traversal guard** (`safeJoin` + `untarBundle`): **fail-closed** — rejects absolute paths, any `..` segment, a defense-in-depth `root`-prefix check after join, and rejects every tar entry type except regular-file/dir (symlink/device escape refused). Proven by `TestScenarioPullRejectsPathTraversal` (forges a `../escape.txt` layer, pushes to a real layout, pulls, asserts `fault.Invalid` + nothing written outside root). This is the highest-risk surface and it is correct. ✅
- **`Pull` + materializer** (`artifact.go`): a `BundleTarMediaType` layer untars to `dir/<entry>` (digest verified); `OrasMaterializer.Materialize` cache-hit reads a `.funcd-entry` sidecar (single-file keeps `entries[0]`). ✅ closes the judge's m3 cache-hit gap.
- **`cmd/funcdctl/cli.go`**: a directory `<path>` → `PushBundle` + `--entry` (default `handler.py`); a file → the ADR-0090 single-file `--schema` path (unchanged). ✅
- **`internal/function/function.go`**: `addBundleEnv` sets `FUNCD_BUNDLE_DIR` (both modes) + `PYTHONPATH` (python family via `isPythonFamily` = `HasPrefix "python"`); single-file/non-python unaffected. Shim unchanged (ADR-0049 preserved). ✅

## Scenario → test
`push-directory-bundles-tar`, `push-single-file-unchanged`, `packbundle-deterministic`, `push-gates-bundle-contract` (4 subtests), `pull-untars-bundle` (+ `pull-rejects-path-traversal`), `pull-single-file-unchanged` (artifact); workerSpec env process/container/non-python/single-file (function). All named + passing.

## Deferred (recorded, legitimate — inherently e2e, need Docker + curated image + live containerd/venom)
`bundle-imports-vendored-dep`, `bundle-locates-native-assets`, `build-matches-runtime-platform`, `backward-compat-single-file-runs` (live invoke), and the venom third testcase invoking `catalog-reader`. The reference `build.py` (hermetic in-image `pip install --target` + offline `INSTALL quack/httpfs` into `duckdb-ext/` + `__funcd_contract.json` + baked validators) and the `python314`+bundle `consumer.yaml` are implemented; `just lima-example-duckdb` not run. `env`/e2e-attributed.

## Findings
- 🔴 Blocker: **None.** · 🟡 Major: **None.**
- Minor: the live lane lands with the deferred e2e (tracked in the ADR's Scenarios as e2e + the F48 consumer follow-ups) — not a defect here.

## ✅ Verified correct — keep it
The fail-closed traversal guard (reject, not clamp) and the deterministic tar are the crux and are implemented exactly right — a later "simplification" to path-clamping or unsorted tar would silently reintroduce a security hole / digest drift. The contract gate reuses `ContractBlob` (one contract path, no divergence) and enforces ADR-0090's both-keys — the bundle can't ship half a contract.
