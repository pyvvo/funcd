# Implementation review — ADR-0123 (Runtime-compiled I/O validators, schema-only artifact)

- **Gate**: ADR-0000 gate 5 (adr-impl-review) — reviews the *code* after the implement gate.
- **ADR**: [ADR-0123](../adr/0123-runtime-compiled-io-validators.md) · **Realizes**: FEAT-0001/F88.
- **Producing model**: claude-opus-4-8.
- **Verdict**: **pass** — Definition of Done met, no Blockers, no Majors.

The review RAN the verification (exit codes + file:line cited below); it did not edit the code.

## Verification run (via `nix develop -c`)

| Check | Command | Result |
|---|---|---|
| Go build | `go build ./...` | **exit 0** |
| Go vet | `go vet ./...` | **exit 0** |
| Go lint | `go tool golangci-lint run ./...` | **0 issues, exit 0** |
| Go test | `go test ./...` | **all `ok`, 0 FAIL, exit 0** |
| Go mod | `go mod verify` | **exit 0** |
| Python lint | `uv run --python 3.14 ruff check .` | **All checks passed, exit 0** |
| Python types | `uv run --python 3.14 mypy` | **no issues in 23 files, exit 0** |
| Python tests | `uv run --python 3.14 pytest -q` | **74 passed, exit 0** |
| Node types | `npm run typecheck` (tsc --noEmit) | **exit 0** |
| Node tests | `npm test` (node --test test/*.test.ts) | **45 pass / 0 fail, exit 0** |
| Node build | `npm run build` (esbuild → shim.mjs/pool.mjs) | **exit 0; ajv inlined (160 refs in each .mjs)** |

A green claim is backed by a captured exit 0 above.

## Definition-of-Done / Review-checklist (10 items — all met)

1. ✅ **Artifact schema-only; shim compiles from `FUNCD_CONTRACT_PATH` at init + caches.** `shim/python/src/funcd_shim/contract.py:44` (`fastjsonschema.compile`), `shim/nodejs/src/contract.ts:31` (`ajv.compile`); validators cached per worker for its life.
2. ✅ **Enforcement unchanged (422/500/204; handler not called on bad input).** `_poolworker.py:93-100` (422 short-circuit before handler), `shim.py:132-140`, `shim.ts:57-63`, `pool.ts:93-99`. Wire-tested: `contract.test.ts:66-90`, `test_shim.py::test_runtime_compiled_validators_enforce_wire`.
3. ✅ **Schema delivered for single-file AND bundle; absent → fail closed.** `artifact.go:221` (single-file dotfile), `:186` (bundle); fail-closed raised in `contract.py:load_from_path`, `contract.ts:loadFromPath`. Tested: `TestScenarioSchemaDeliveredSingleFile`, `TestScenarioSchemaDeliveredBundle`, `test_pool_broken_contract_fails_closed`.
4. ✅ **Compiled bytes == digest-pinned advertised blob (advertised == enforced).** `deliverContract` (`artifact.go:232-245`) fetches the `contractMediaType` layer (the exact layer `Inspect` reads, `:342-347`), digest-verified by `content.FetchAll`. Asserted end-to-end by `require.JSONEq(advertised, delivered)` in both scenario tests (`artifact_test.go`).
5. ✅ **Compile runs BEFORE any handler import (bounded eval-free reversal), asserted.** Reorder real in all four shims: `shim.py:196-202` (load contract → then `load_module`), `_poolworker.py:49-50`, `shim.ts:118-127` (`loadValidators` before `import(artifact)`), `pool.ts:72-81`. Directly proven by `test_contract.py::test_main_fail_closed_before_handler_import` (a handler with an import side-effect marker is never written when the contract compile fails).
6. ✅ **Single-file uses `.funcd-contract.json` dotfile sidecar; the resolver skips dotfiles (no `entries[0]` collision — M1).** `artifact.go:464-467` (`Materialize` cache-hit skips `strings.HasPrefix(".")`). Tested: `TestScenarioSchemaDeliveredSingleFile` asserts the resolved path is the handler, `NotEqual(".funcd-contract.json")`, and the cache-hit re-resolves the handler.
7. ✅ **nodejs22 ships AJV; Python uses shipped fastjsonschema; no push-time toolchain / docker.** `ajv ^8.20.0` in `shim/nodejs/package.json:16`, esbuild-inlined into the embedded `shim.mjs`/`pool.mjs` (self-contained image entrypoint, 160 refs). No Go/Python deps added (`go mod verify` clean).
8. ✅ **`VerifyBundleContract` drops the validator-symbol check; keeps both-keys + `contract.Check`.** `bundle.go:168-198` — `entry` arg now `_` (unread), both-keys enforced (`:181-186`), per-side `contract.Check` added (`:189-194`). Dead validator-symbol consts removed (staticcheck `unused` would have failed; lint = 0 issues). Tested: `bundle_test.go` "no baked validator symbols still passes" + "out-of-profile schema fails contract.Check".
9. ✅ **Scale-from-zero cold-start benchmark present + within budget; fallback named.** `test_coldstart.py::test_warmup_compile_within_budget` (per-compile budget 250 ms tripwire) + `test_compiled_validator_reuse_is_free`.
10. ✅ **ADR-0060 + ADR-0058 back-links; FEAT-0001/F88 linked.** Present in the ADR header (added at acceptance).

## Findings

### ✅ Verified correct — keep

- **Fail-closed is genuinely closed, not open.** `contract.py:load_from_path` raises `ContractError` on a missing / unparseable / half document, and `shim.py:196-200` / `_poolworker.py` / `pool.py:await_ready` / `shim.ts:117-122` / `pool.ts:71-76` all turn that into **exit 3** (worker/pool refuses to serve). The env-*unset* path returns `None` and falls back to module-baked validators — this is the ADR's explicit transition back-compat (Decision 4), and for a **contracted** function the materializer always sets `FUNCD_CONTRACT_PATH` (`addContractEnv`, `function.go:875-879`, wired in both process `:929` and container `:894` modes, and the pool manifest `pool.go:235-240`), so the delivered-but-broken boundary is the one that matters and it fails closed. Not a fail-open. *(attribution: n/a — verified property)*
- **advertised == enforced holds by construction.** The materializer delivers the same `contractMediaType` layer bytes `Inspect` advertises; the `JSONEq(advertised, delivered)` assertions prove it for single-file and bundle. *(verified)*
- **Python imports stay at module top level** (the explicit standing directive): `fastjsonschema` is imported at `contract.py:25`; no new function-local imports were introduced in the contract path. *(verified)*
- **The compile-before-handler-import ordering is real, not merely claimed** — proven behaviorally by the import-marker test, not just by reading the code. *(verified)*

### Flagged items — attributed after inspection

- **(a) `tests/e2e/journey_test.go::TestE2EEventDataContract` rewrite — legitimate adaptation, NOT a gutted test.** The old test baked a JS `__funcdValidateInput` that enforced `hello:string` while advertising an `{}` (any) input schema. Under ADR-0123 the artifact is schema-only (no baked validator) and *advertised == enforced*, so the old baked constraint no longer exists — the constraint was correctly moved INTO the schema (a closed record `{hello:string, required, additionalProperties:false}`). The rewritten test pushes a **no-validator** handler + that schema, applies, and still asserts the full single-file path: `200` on `{"hello":"world"}` (handler ran) and `422` on `{"hello":123}` with a "contract" message (handler never ran). It exercises push → delivered sidecar → shim compile → 422, i.e. strictly *more* of the ADR-0123 path than the old baked-JS test. `journey_test.go:135-160`. *(attribution: adr — a faithful adaptation to the ADR's behavior change; not model-attributed)*
- **(b) `embed.go` go:embed fix + `pypool_test.go` skip — real, in-scope, correctly env-attributed.** The `//go:embed` list previously omitted `funclog.py`/`tracespan.py`/`invcontext.py`/`kv.py`, which the *extracted* solo+pool shim import at load — so the extracted pool would fail regardless of ADR-0123; the fix also adds `contract.py` (needed by this ADR). This is a correct, in-scope embed fix (`embed.go:22`). The added `TestPythonPoolSmoke` skip-on-missing-`fastjsonschema` (`pypool_test.go:26-28`) parallels the existing ≥3.14 gate: the shim now imports `fastjsonschema` at load, and a bare dev interpreter may lack it while the runtime image ships it (ADR-0071). Skipping (not failing) a host-environment gap is correct and does not hide a shim defect. *(attribution: env for the skip; the embed fix is a correct in-scope side effect — neither is model-attributed)*

### Deferral (recorded, correct)

The image-rebuild AJV-ship verification and the containerd/Lima venom e2e need colima and are legitimately deferred per the ADR test plan (`0123…md:195-196`). Every non-deferred path is genuinely tested (no skipped scenario assertions on the process/unit lanes).

## Tracking / hygiene

- ADR-0123 substance **unchanged** — the only edit is the `Accepted → Reviewing` status bump (this gate makes no further ADR edit unless it stamps Implemented). Context/Scenarios/Decision/Contracts untouched.
- FEAT-0001/F88 row: `accepted → reviewing`.
- Changed + untracked files grepped for an absolute home-path prefix, the local username, and a personal email — clean.

## Verdict

**pass.** All 10 Definition-of-Done items met with captured evidence; 0 Blockers, 0 Majors, 0 model-attributed findings. The two flagged items are a faithful ADR-driven test adaptation and a correct env/in-scope fix, not quality regressions. Security-critical properties (fail-closed, compile-before-handler-import, advertised==enforced, no `entries[0]` collision) hold in code and are behaviorally tested.
