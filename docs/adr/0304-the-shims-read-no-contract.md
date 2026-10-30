# ADR-0304: the shims read no contract, and funcd names none to them

- **Status**: Proposed (judged twice: round 1's Blocker, the http refusal, and its Major, the supersession list,
  are folded; round 2 found no Blocker or Major, and its two Minors are folded; then widened at the decider's word,
  2026-10-30, so that no part of funcd keeps fastjsonschema)
- **Date**: 2026-10-30
- **Deciders**: green-0-rabbit
- **Tags**: contract, runtime, shim, image
- **Realizes**: [FEAT-0001/F88](../feat/0001-feat-v1.1.md) (advertised == enforced: funcd is the one reader of a contract)
- **Supersedes in part**, each to get a `Superseded in part by: ADR-0304` back-link:
  - [ADR-0303](0303-funcd-checks-every-call-against-its-contract.md) Decision 7 (the shims get an open contract) and
    its scenario `the-shims-stop-checking`;
  - [ADR-0123](0123-runtime-compiled-io-validators.md) Decisions 2 (the shim's warm-up compile), 3 (the nodejs22
    image ships AJV; the Python shim compiles with fastjsonschema at init), 6 and 7 (that compile's eval and
    cold-start bounds), Decision 4's `FUNCD_CONTRACT_PATH` and its "the shim refuses to serve", which funcd's
    `ContractMissing` gate (#1084) now carries, and the scenarios `runtime-compiles-validator`,
    `schema-tamper-evident`, `eval-over-trusted-input` and `cold-start-within-budget`;
  - [ADR-0125](0125-funcdctl-dev-local-run.md) Decision 3's `FUNCD_CONTRACT_PATH`: dev's 422 and contract 500 come
    from the gateway of its in-memory platform, which also checks the shim's 204 (ADR-0303);
  - [ADR-0150](0150-int64-contract-range.md) Decisions 2 and 3, the shims' int64 checks; Decision 1's range is
    `internal/contract`'s (ADR-0301);
  - [ADR-0158](0158-pool-member-identity.md) Contracts: the pool manifest row's `contract?`;
  - [ADR-0243](0243-http-functions.md) Decision 2's delivered-contract clause, and the matching parts of its scenario
    `http-mismatch` and its checklist: the reconciler refuses such a Function, not the shim (Decision 3);
  - [ADR-0071](0071-python-runtime-ships-fastjsonschema.md) Decision 2 (the python314 image ships fastjsonschema),
    its scenario `validator-imports-in-image`, and the fastjsonschema clause of its scenario `shim-stays-stdlib-only`.
- **Relates to**: [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (a shim change is a release, then a pin) ·
  issue [#1346](https://github.com/pyvvo/funcd/issues/1346), which this ADR's implementation closes

## Context & Need

ADR-0303 moved every contract check into funcd and gave each shim an open contract, so the shims' validators accept
everything (Decision 7). The shim changes pyvvo/funcd-typescript#96 and pyvvo/funcd-python#111 remove those validators,
their engines (ajv, fastjsonschema), the module-baked fallback and the build-time bake (#1346). Pinning them breaks
ADR-0303's scenario `the-shims-stop-checking`, which expects each pinned shim to answer 422 with the delivered contract.
It also leaves funcd writing a file that no shim reads and a variable that only the Node shim's http guard reads
(ADR-0243 Decision 2), and shipping fastjsonschema in the python314 image, the dev shell and the Linux test VM for
validators that no longer run.

The purpose: funcd is the only reader of a Function's contract. A shim gets none and checks nothing, and nothing in
funcd ships the shims' old engine.

## Scenarios

- `scenario: the-shims-read-no-contract` — Given a handler on the Node shim and the Python shim funcd pins, solo and
  pooled, whose module also exports input and output validators that refuse everything, and a contract whose input
  `email` has `type: string` and whose output `ok` has `type: boolean`, When `{"email": 5}` reaches the shim directly,
  with `FUNCD_CONTRACT_PATH` (solo) or the manifest's `contract` (pooled) naming that contract, and the handler
  returns `{"ok": "yes"}`, Then the shim answers 200 with `{"ok": "yes"}`.
- `scenario: funcd-names-no-contract` — Given a contracted cloudevent Function, When its worker starts solo in process
  mode, solo in container mode and as a pool member, Then the worker's env has no `FUNCD_CONTRACT_PATH`, the member's
  manifest row has no `contract`, and funcd writes no `.funcd-contract-open.json`.
- `scenario: an-http-function-with-a-contract-never-starts` — Given an http Function whose artifact carries a contract,
  When it deploys with one replica, Then it is `ShapeValid=False` with reason `ShapeInvalid` and a message that names
  `spec.protocol: http`, and no worker starts.
- `scenario: the-python-image-runs-without-fastjsonschema` — Given the python314 image built without fastjsonschema,
  When the `kv` lane runs its contracted Python Function `pycounter` on containerd, Then `pycounter` reaches Ready and
  serves its calls, and `import fastjsonschema` fails in the image.

## Scope

In: the pins of the two shim releases; funcd names no contract to a shim; the reconciler refuses an http Function
that carries one; fastjsonschema leaves the python314 image, the dev shell and the Linux test VM, and every message
that says the Python shim needs it; the four scenarios.

Out:
- the shims' code: ADR-0303 decided that they stop checking, and each repo releases its change (ADR-0141);
- the delivered blob, `ContractOf` and the checks (ADR-0303 Decisions 1 to 6), which stay;
- `bench/contract-validation`, which installs its own fastjsonschema;
- an http Function's API contract, an OpenAPI spec for example, which funcd has no way to embed today (the decider,
  2026-10-30).

## Constraints & Decision drivers

- ADR-0303: funcd checks every call; nothing about the checks changes.
- ADR-0141: a shim change is a release in its repo, then a pin; only `go.mod` and `go.sum` name the versions.
- Order: with the pinned shims of today, a worker without `FUNCD_CONTRACT_PATH` falls back to the module-baked
  validators (ADR-0303's third alternative), so Decision 2 never lands before the pins; with the pins but without
  ADR-0303's plugins, no call is checked anywhere. The python314 image (`--build-context shim`) and funcd's tests
  (`internal/testkit/langmod`) run the Python shim at its pinned version, and today's imports fastjsonschema when it
  loads, so Decision 4 never lands before the pins either.
- The decider's choices (2026-10-30): pin the new shims and name no contract; no part of funcd keeps fastjsonschema.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **Pin the new shims, name no contract, refuse an http Function with a contract in the reconciler, and ship no fastjsonschema** | One reader of the contract; no dead file, variable or package; the refusal comes before any worker boots | Waits for two releases; moves ADR-0243's delivered-contract start check into funcd | **chosen** |
| Pin the new shims, keep writing the open contract | No reconciler change; ADR-0243's guard keeps working | A file that no shim reads; the scenario changes anyway | rejected |
| Pin them and name no contract, but keep `FUNCD_CONTRACT_PATH` for an http worker whose bundle carries one | ADR-0243 unchanged | funcd stays a writer of the variable for one case; the refusal waits for a worker to boot and exit | rejected |
| Keep fastjsonschema in the python314 image (ADR-0071) | An artifact built by hand with an older `funcd_shim.build`, whose baked validators import it, still loads | A package that nothing in funcd imports: no funcd build path bakes validators since ADR-0123 | rejected |
| Keep today's pins | No change | Every worker compiles an empty schema with an engine it never needs; the build exports keep baking validators | rejected |

## Decision

1. **funcd pins shims that read no contract**: releases of pyvvo/funcd-typescript and pyvvo/funcd-python that include
   #96 and #111. They read no contract and no module-baked validator, and answer no 422 and no contract 500; the Node
   shim reads `FUNCD_CONTRACT_PATH` only to refuse an http Function (ADR-0243), which funcd no longer triggers. They
   keep the 400 for a body that is not a JSON object and the 204 for a null result, and their build exports bake no
   validator.
2. **funcd names no contract to a shim**: the reconciler sets no `FUNCD_CONTRACT_PATH` and writes no
   `.funcd-contract-open.json`, and a pool manifest row has no `contract` key. The materializer still delivers the
   blob (`__funcd_contract.json` in a bundle, `.funcd-contract.json` beside a single file), and `ContractOf` reads it
   there (ADR-0303 Decision 2).
3. **funcd refuses an http Function that carries a contract**: `materialize` fails an http Function whose bundle root
   holds a delivered blob, as a shape failure: `ShapeValid=False`, reason `ShapeInvalid`, a message that names
   `spec.protocol: http`, and no worker starts. No ADR then needs the Node shim's guard, so its repo may remove it.
4. **funcd ships no fastjsonschema**: the python314 image installs no package (its `requirements.txt` and pip step
   go), the dev shell's Python carries only PyYAML, and the Linux test VM installs no fastjsonschema wheel.
5. **Order**: the pins and Decisions 2 to 4 land in one commit, on a main that includes ADR-0303's plugins (#1347).

## Design model

None: no new actor or ordering; the reconciler stops writing a file, a variable and a manifest key, and refuses one
more case where it already refuses a missing contract.

## Temporary workarounds

None.

## Contracts

```go
// internal/function: removed — writeOpenContract, openContractName, addContractEnv, and the pool manifest row's
// Contract field (json:"contract"). contractFileIn stays: ContractOf, materialize's ContractMissing and
// ContractInvalid gates, and Decision 3 find the delivered blob with it.
```

| Consumes | Exposes |
|---|---|
| `github.com/pyvvo/funcd-typescript` and `github.com/pyvvo/funcd-python` at their new tags | A worker env without `FUNCD_CONTRACT_PATH`; a pool manifest row `{name, artifact, handler, env}` |
| The delivered blob in the bundle root (unchanged) | `ShapeValid=False` `ShapeInvalid` for an http Function that carries a contract |
| — | A python314 image with only the stdlib, its library closure and the shim |

## Implementation plan

1. `go get github.com/pyvvo/funcd-typescript@<tag> github.com/pyvvo/funcd-python@<tag>`, tags that include #96 and
   #111, then `go mod tidy`.
2. `internal/function`: remove `writeOpenContract`, `openContractName`, `addContractEnv` and its two calls, and the
   pool manifest's `Contract` field with its writer. In `materialize`, an http Function whose bundle root holds a
   delivered blob returns an error that maps, beside ContractMissing and ContractInvalid (`function.go:771-780`),
   to a `gateFailed` with reason `ShapeInvalid` and `shapeInvalid: true`. The comments of `contractDeliveryNames`
   and `contractFileIn` name their readers.
3. The messages that say the Python shim needs fastjsonschema: `cmd/funcd/main.go:1665` and `:1682`,
   `cmd/funcdctl/dev.go:1613`, and the e2e harness's `requirePython` (`pkg/funcd/shim_regression_e2e_test.go:138-162`)
   with the message `pkg/funcd/shim_python_ci_e2e_test.go:39` asserts. `internal/testkit/bench/pypool_test.go` drops
   its fastjsonschema skip. The fake-interpreter fixtures that give a missing fastjsonschema as the reason
   (`cmd/funcd/main_test.go:254-256` and `:278`, `cmd/funcdctl/dev_interpreter_test.go:49-59`,
   `cmd/funcdctl/dev_phase2_test.go:67`) take another reason.
4. Decision 4: `images/runtime/python314` loses `requirements.txt` and the Dockerfile's `COPY`, `pip install` and
   their comment; `flake.nix`'s dev-shell Python lists only `pyyaml`; `scripts/lima-linux-tests.yaml` drops the
   fastjsonschema install, its probe line and its comment. The lane guard tests pass (`go test -run Lane ./scripts/`).
5. Tests, one per Scenario:
   - `TestScenarioTheShimsReadNoContract` replaces `TestScenarioTheShimsStopChecking`
     (`internal/function/openshim_internal_test.go`): the pinned shims through `internal/testkit/langmod`, solo and
     pooled.
   - `TestScenarioFuncdNamesNoContract`, once in package `function` for the solo worker in process and container
     mode (it replaces `TestScenarioContractEnvProcessMode`, `TestScenarioContractEnvContainerMode` and the now
     redundant `TestScenarioNoContractNoEnv`), and once in package `function_test` for a pool member, its blob
     delivered through `pushHandler` and the ORAS materializer (it replaces `TestPoolManifestNamesTheOpenContract`).
   - `TestScenarioAnHTTPFunctionWithAContractNeverStarts` in package `function_test`, beside
     `TestIssue1084_OCIArtifactWithoutContractDoesNotStart`; ADR-0243's `TestScenarioHTTPMismatch` passes unchanged.
   - `the-python-image-runs-without-fastjsonschema`: `just lima-example kv` on the rebuilt runtime images, and
     `python3.14 -c 'import fastjsonschema'` fails in the python314 image.
6. `just ci` green and the e2e suite green with the new pins. Definition of done: the four Scenario tests pass,
   ADR-0303's other scenario tests and `TestScenarioHTTPMismatch` pass unchanged, no non-test code in funcd sets
   `FUNCD_CONTRACT_PATH` or writes `.funcd-contract-open.json` (only `TestScenarioTheShimsReadNoContract` sets them,
   to prove that the shims ignore them), and no file in funcd installs fastjsonschema but `bench/contract-validation`.

## Review checklist

- [ ] `go.mod` pins releases that include #96 and #111; no `go.work` and no pseudo-version is committed.
- [ ] No worker env carries `FUNCD_CONTRACT_PATH`, no pool manifest row carries `contract`, and funcd writes no
      `.funcd-contract-open.json`.
- [ ] An http Function whose bundle holds a delivered blob fails at `materialize` with `ShapeInvalid`, and no worker
      starts.
- [ ] The materializer still delivers the blob; ADR-0303's scenario tests and `TestScenarioHTTPMismatch` pass.
- [ ] No message, test harness, fixture, image or test environment says or makes the Python shim need fastjsonschema;
      the `kv` lane passes on the rebuilt python314 image.
- [ ] Each Scenario has its named, passing test; the replaced tests are removed, not skipped; no new config key, API
      field, CLI flag or dependency.

## Consequences

- Positive: funcd is the one reader of a contract; no worker compiles a schema; an http Function that carries a
  contract fails before any worker boots; `shim.mjs` shrinks from 428 KB to 146 KB and `pool.mjs` from 434 KB to
  151 KB; the Python shim and the python314 image need only the standard library; #1346 closes.
- Negative: each pool whose members carry a contract is rebuilt once after the upgrade, because its manifest signature
  changes (`manifestSignature`, `internal/function/pool.go`). An artifact built by hand with an older
  `funcd_shim.build` keeps baked validators that import fastjsonschema, so it fails `ShapeInvalid` when it loads, in
  the image and on any host Python; no funcd build path makes one since ADR-0123. As under ADR-0303, a call that
  reaches a shim without funcd's chain is not checked.
- Risks accepted: an image override (`--image runtime=<ref>`) or a custom image prefix
  (`internal/runtime/ctrmanager/manager.go`) can run an older shim, which then falls back to its module-baked
  validators, or fails to load without fastjsonschema; that image is the operator's choice.

## Open questions

- How an http Function carries its API contract, an OpenAPI spec for example: a later ADR.

## References

- Issue [#1346](https://github.com/pyvvo/funcd/issues/1346); pyvvo/funcd-typescript#96; pyvvo/funcd-python#111.
- A local run on 2026-10-30, with funcd at the branch of #1347 and a `go.work` naming both shim branches:
  `TestScenarioTheShimsStopChecking` failed only at its 422 check, the dev-mode contract tests passed, and the e2e
  suite of `pkg/funcd` passed (397 s), with today's reconciler, which still sets `FUNCD_CONTRACT_PATH`.
- The judge's run on 2026-10-30: with only that assignment removed, `TestScenarioHTTPMismatch` fails ("with-contract
  is ShapeValid=False"), which Decision 3 answers.
- fastjsonschema's `compile_to_code` output imports `JsonSchemaValueException` from fastjsonschema (checked
  2026-10-30).
