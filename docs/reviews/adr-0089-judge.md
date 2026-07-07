# ADR-0089 Judge Report — Function dependency bundling (the deployment-package model)

**Verdict**: The decision is sound and well-scoped — the bundle-as-an-additive-OCI-layer + generic `PYTHONPATH`/`FUNCD_BUNDLE_DIR` env is the right shape and composes with the real artifact/function/shim code — but two Contracts diverge from the code they claim to extend (the CLI push contract surface and the `__funcd_contract.json` sourcing) and must be reconciled before Accepted.

**Judged against**: blueprint.md §single-binary / embed-first / pure-Go / provider model · FEAT-0003/F59 · ADR-0000 template · ADR-0031 · ADR-0032 · ADR-0030 · ADR-0035 · ADR-0049 · ADR-0058 · ADR-0059 · ADR-0086.

**ADR status**: Proposed

## Goal alignment

Serves F59 exactly: a function artifact becomes a bundle (handler + vendored deps + contract) that runs unchanged on stock `python314`, unblocking the F48 function consumer — the Python peer of the JS esbuild bundle, no new curated runtime. It holds one altitude (artifact + push/materialize + worker env) and correctly defers the `spec.catalogs` consumer binding, Node `node_modules` bundles, and any new runtime to named follow-ups. Nothing load-bearing is left undecided; the deferrals are the right cuts.

## Strengths — keep as-is

- **The additive-layer decision is the correct one and the media-type/annotation selection composes with real code.** `Push` selects layers by media type via `layerByMediaType` (`internal/artifact/artifact.go:80`, used at `:159`), so a new `BundleTarMediaType` layer alongside the single-blob `bundleMediaType` never shifts the bundle — exactly as `contractMediaType` does today. Do not let a later edit collapse this into "replace the blob layer."
- **The "dir is already bind-mounted; a bundle just populates it" claim is TRUE for container mode** (`internal/function/function.go:783-784` mounts `filepath.Dir(artifactPath)` → `containerArtifactDir`). The env-only delta (Decision §2) is genuinely minimal. Keep it.
- **`PYTHONPATH`-over-shim-`sys.path` is the right call and is real.** `PYTHONPATH` is honored by CPython at interpreter startup before the shim's `spec_from_file_location` runs (`shim/python/src/funcd_shim/runtime.py:39`), so the ADR-0049 stdlib-only shim stays untouched. The Alternatives rejection of "shim prepends `sys.path`" (lines 103-106) is correctly reasoned, not strawmanned.
- **`isPythonFamily` has a real hook.** `shimByFamily` keys on the family prefix `"python"` (`internal/function/function.go:171`, `shimFor` at `:719-730`; `python314`/`python312` are `HasPrefix(rt,"python")`), so `isPythonFamily(rt)` is a one-liner, not new machinery.
- **Determinism/traversal/hermetic-build drivers are correct and testable.** Sorted-entries + zeroed mtime/uid/gid yields a reproducible tar digest (so ADR-0035 pinning holds), and building inside the curated image is genuinely byte-for-byte glibc-matched. The path-traversal-on-untar assertion is called out as a named test (line 246).

## Findings

### Blockers

None.

### Major

- **M1 — the CLI push contract contradicts the real flag surface.** *Evidence*: the ADR (line 213-214, and "Consumes/Exposes" line 218) says "The `--input`/`--output` flags remain for the single-file path" and shows a bundle sourcing its contract. The real `pushCmd` (`cmd/funcdctl/cli.go:220-223`) exposes `--contract-input` / `--contract-output` (not `--input`/`--output`), reads schema *files*, gates them through `contract.Check` (`:237`), and assembles them via `artifact.ContractBlob` (`internal/artifact/artifact.go:55`). *Goal impact*: an implementer reading the ADR verbatim will rename or re-invent flags that already exist, breaking backward-compat with every existing push invocation and the F30 contract path. *Direction*: state the real flag names (`--contract-input`/`--contract-output`) and that the single-file contract path is unchanged; describe the bundle path as an *addition* that sources schemas from the bundle, not a replacement of that surface.

- **M2 — `__funcd_contract.json` and "`build.py` writes it / push verifies it" describe a mechanism that does not exist yet, stated as if it composes with ADR-0058/0059.** *Evidence*: Decision §3 (lines 136-142) + scenario `push-gates-bundle-contract` (lines 51-54) require `build.py` to write `<dir>/__funcd_contract.json` and push to `VerifyBundleContract(dir, entry)`. But today `build.py` (`shim/python/src/funcd_shim/build.py:49-78`) returns `runtime_source` + separate `input_schema`/`output_schema` dicts and writes **no** contract file; the schemas reach push via the `--contract-*` file flags (M1) and become the ADR-0059 blob through `ContractBlob` — there is no `__funcd_contract.json` artifact anywhere, and `VerifyBundleContract`'s "the entry file must carry the matching baked validator" is a *new* static check (grepping the baked `__funcd_validate_input/_output` symbols the AST-baker at `build.py:211-227` injects) with no existing counterpart. *Goal impact*: this is the ADR's highest-risk contract — it's presented as "composes with ADR-0058/0059" but is net-new build + push behavior; underspecifying it risks a `VerifyBundleContract` that can't be built as described or that duplicates/undercuts the existing gate. *Direction*: keep the decision (contract travels in the bundle) but say plainly it is *new*: `build.py` gains an emit-`__funcd_contract.json` step (ADR-0059 `{input?,output?,dialect}` shape), and `VerifyBundleContract` is a new function that (a) validates the JSON and (b) confirms the entry file defines the baked validator symbol for each declared side, then promotes the JSON via the existing `ContractBlob`/contract-layer path. Note that this does not reuse the `--contract-*` flags for bundles (they gate schema *files*, not a bundle-embedded JSON), resolving the tension with M1.

### Minor

- **m1 — "the shim loads that single entry file unchanged" slightly conflates entry-file and handler-export.** *Evidence*: Decision §2 (line 132) and the `FUNCD_HANDLER` line in the contract (line 206). The shim uses `FUNCD_ARTIFACT` for the entry *file path* and `FUNCD_HANDLER` for the *export name* (default `handle`, `shim/python/src/funcd_shim/shim.py:162`; loaded at `runtime.py:39`). The mechanism the ADR chose (set `FUNCD_ARTIFACT = <bundleRoot>/<entry>`) is correct — but `--entry` (a file) and `FUNCD_HANDLER` (an export) are orthogonal and the prose reads as if `--entry` feeds the handler. *Direction*: one sentence distinguishing entry file (→`FUNCD_ARTIFACT`) from handler export (→`FUNCD_HANDLER`, unchanged).
- **m2 — process-mode (ADR-0030) does not bind-mount a directory; it materializes a file path.** *Evidence*: contract comment (line 202) says `bundleRoot == Dir(artifactPath)` for process mode; that is true as a *directory-of-the-materialized-tree*, but process mode (`function.go:805-821`) has no `Mounts` — the untarred tree must land in the materializer's per-digest cache dir (`OrasMaterializer.Materialize`, `artifact.go:335`). *Goal impact*: none to the decision; the env still points at the right dir. *Direction*: note that in process mode `Pull` untars into the cache dir and `bundleRoot` is that dir (no mount involved), so the two modes converge on env, not on a mount.
- **m3 — `Pull` extension returns `dir/<entry>` but `OrasMaterializer.Materialize` returns `filepath.Join(cacheDir, entries[0].Name())` on a cache hit** (`artifact.go:336-337`). *Evidence*: a multi-file untarred tree makes `entries[0]` non-deterministic and likely not the entry file. *Direction*: flag that the cache-hit fast-path must be taught the entry (e.g. read it back from the annotation or a sidecar), else a cached bundle returns the wrong path. Small but real; call it out in the Implementation plan.

### Nits

- **n1** — line 211 "inferred to `index.mjs` for a Node bundle when no `handler.py`" quietly introduces Node bundle behavior the Scope (line 78) defers; either drop the inference from this ADR or note it as the single explicitly-in Node touch.
- **n2** — the "Empirically verified this session" claims (lines 30-32, 295-296) are good evidence but unreproducible from the repo; fine for Context, but the e2e scenarios (lines 58-66) are the durable proof and are correctly marked deferred.

## Template & scenario conformance

All ADR-0000 sections present and in order (Header → Context & Need → Scenarios → Scope → Constraints → Alternatives → Decision → Temporary workarounds → Contracts → Implementation plan → Review checklist → Consequences → Open questions → References). Every scenario maps to a named test in the Test plan (lines 244-251): the six contract/unit scenarios and the four e2e ones are each named, e2e correctly deferred to the Lima lane per roadmap test-sequencing. Concision is good — no bloated passages; the Alternatives (five options, each with a real losing reason) and Temporary workarounds are tight. `Realizes: FEAT-0003/F59` points at a real row (feat row F59, status `adr`).

## Recommendation

**Advance after fixing the two Major findings.** The decision itself is sound and should not be re-drafted; the fixes are contract-accuracy edits, not design changes. Single most important next step: reconcile M1 + M2 together — pin the real `--contract-*` flag surface for single-file, and specify `__funcd_contract.json` + `VerifyBundleContract` as explicitly *new* build/push behavior that promotes into the existing ADR-0059 contract-blob path — so the implementer builds against `internal/artifact` + `internal/function` + `build.py` as they actually are.
