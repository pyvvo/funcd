# ADR-0304: the shims read no contract, and funcd names none to them

- **Status**: Proposed
- **Date**: 2026-10-30
- **Deciders**: green-0-rabbit
- **Tags**: contract, runtime, shim
- **Realizes**: [FEAT-0001/F88](../feat/0001-feat-v1.1.md) (advertised == enforced: funcd is the one reader of a contract)
- **Supersedes in part**: [ADR-0303](0303-funcd-checks-every-call-against-its-contract.md) Decision 7 (the shims get an
  open contract) and its scenario `the-shims-stop-checking`; [ADR-0123](0123-runtime-compiled-io-validators.md)
  Decisions 2 (the shim's warm-up compile), 3 (the Node shim ships AJV), 6 and 7 (that compile's eval and cold-start
  bounds), and Decision 4's `FUNCD_CONTRACT_PATH`. The delivered blob stays in the bundle, where funcd reads it.
- **Relates to**: [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (a shim change is a release, then a pin) ·
  [ADR-0071](0071-python-runtime-ships-fastjsonschema.md) (the python314 image keeps fastjsonschema) ·
  [ADR-0243](0243-http-functions.md) (the Node shim's exit 3 for an http Function given a contract) · issue
  [#1346](https://github.com/pyvvo/funcd/issues/1346), which this ADR's implementation closes

## Context & Need

ADR-0303 moved every contract check into funcd and gave each shim an open contract, so the shims' validators accept
everything (Decision 7). The shim changes pyvvo/funcd-typescript#96 and pyvvo/funcd-python#111 remove those validators,
their engines (ajv, fastjsonschema), the module-baked fallback and the build-time bake (#1346). Pinning them breaks
ADR-0303's scenario `the-shims-stop-checking`, which expects each pinned shim to answer 422 with the delivered contract,
and leaves funcd writing a file and an environment variable that no shim reads.

The purpose: funcd is the only reader of a Function's contract. A shim gets none and checks nothing.

## Scenarios

- `scenario: the-shims-read-no-contract` — Given a handler on the Node shim and the Python shim funcd pins, solo and
  pooled, whose module also exports a validator that refuses everything, and a contract whose `email` field has
  `type: string`, When `{"email": 5}` reaches the shim directly, with `FUNCD_CONTRACT_PATH` (solo) or the manifest's
  `contract` (pooled) naming that contract, Then the shim runs the handler and answers 200.
- `scenario: funcd-names-no-contract` — Given a contracted cloudevent Function, When its worker starts solo in process
  mode, solo in container mode and as a pool member, Then the worker's env has no `FUNCD_CONTRACT_PATH`, the member's
  manifest row has no `contract`, and the bundle root holds the delivered blob and no `.funcd-contract-open.json`.

## Scope

In: the pins of the two shim releases; the reconciler and the pool manifest name no contract; the two scenarios.

Out:
- the shims' code: ADR-0303 decided that they stop checking, and each repo releases its change (ADR-0141);
- the delivered blob, `ContractOf` and the checks (ADR-0303 Decisions 1 to 6), which stay;
- fastjsonschema in the python314 image (ADR-0071; an artifact built by an older `funcd_shim.build` imports it when it
  loads), in the dev shell (`bench/contract-validation` uses it) and in the Linux test VM;
- the Node shim's exit 3 for an http Function given `FUNCD_CONTRACT_PATH` (ADR-0243): it stays in the shim, and funcd
  never sets the variable.

## Constraints & Decision drivers

- ADR-0303: funcd checks every call; nothing about the checks changes.
- ADR-0141: a shim change is a release in its repo, then a pin; only `go.mod` and `go.sum` name the versions.
- The pins and the reconciler change must land together. With the pinned shims of today, a worker without
  `FUNCD_CONTRACT_PATH` falls back to the module-baked validators (ADR-0303's third alternative); with the new pins
  and today's reconciler, funcd writes a file and a variable that nothing reads.
- The decider's choice (2026-10-30): pin the new shims and name no contract.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **Pin the new shims and name no contract** | One reader of the contract; no dead file or variable | Waits for two releases | **chosen** |
| Pin the new shims, keep writing the open contract | No reconciler change | A file and a variable that no shim reads; the scenario changes anyway | rejected |
| Keep today's pins | No change | Every worker compiles an empty schema with an engine it never needs; the build exports keep baking validators | rejected |

## Decision

1. **funcd pins shims that read no contract**: the first releases of pyvvo/funcd-typescript and pyvvo/funcd-python
   that include #96 and #111. Those shims read no `FUNCD_CONTRACT_PATH`, no manifest `contract` and no module-baked
   validator, and they answer no 422 and no contract 500; their build exports bake no validator.
2. **funcd names no contract to a shim**: the reconciler sets no `FUNCD_CONTRACT_PATH` and writes no
   `.funcd-contract-open.json`, and a pool manifest row has no `contract` key. The materializer still delivers the
   blob (`__funcd_contract.json` in a bundle, `.funcd-contract.json` beside a single file), and `ContractOf` reads it
   there (ADR-0303 Decision 2).
3. **One change**: the pins and Decision 2 land in the same commit on main.

## Design model

None: no new actor or ordering; the reconciler only stops writing a file.

## Temporary workarounds

None.

## Contracts

```go
// internal/function: removed — writeOpenContract, openContractName, addContractEnv, and the pool manifest row's
// Contract field (json:"contract"). contractFileIn stays: ContractOf finds the delivered blob with it.
```

| Consumes | Exposes |
|---|---|
| `github.com/pyvvo/funcd-typescript` and `github.com/pyvvo/funcd-python` at their new tags | A worker env without `FUNCD_CONTRACT_PATH`; a pool manifest row `{name, artifact, handler, env}` |
| The delivered blob in the bundle root (unchanged) | — |

## Implementation plan

1. `go get github.com/pyvvo/funcd-typescript@<tag> github.com/pyvvo/funcd-python@<tag>`, the first tags that include
   #96 and #111, then `go mod tidy`.
2. `internal/function`: remove `writeOpenContract`, `openContractName`, `addContractEnv` and its two calls, and the
   pool manifest's `Contract` field with its writer; the comments of `contractDeliveryNames` and `contractFileIn`
   name `ContractOf` as the reader.
3. Tests, one per Scenario:
   - `TestScenarioTheShimsReadNoContract` replaces `TestScenarioTheShimsStopChecking`
     (`internal/function/openshim_internal_test.go`): the pinned shims through `internal/testkit/langmod`, solo and
     pooled, with a refusing module-baked validator and a contract that refuses the data, answer 200.
   - `TestScenarioFuncdNamesNoContract` replaces `TestScenarioContractEnvProcessMode`,
     `TestScenarioContractEnvContainerMode` (`bundleenv_internal_test.go`) and `TestPoolManifestNamesTheOpenContract`
     (`poolopen_test.go`); `TestScenarioNoContractNoEnv` stays.
   - `internal/testkit/bench/pypool_test.go` drops its fastjsonschema skip: the Python shim no longer imports it.
4. `just ci` green and the e2e suite green with the new pins. Definition of done: both Scenario tests pass, ADR-0303's
   other scenario tests pass unchanged, and no code in funcd writes `FUNCD_CONTRACT_PATH` or
   `.funcd-contract-open.json`.

## Review checklist

- [ ] `go.mod` pins the first releases that include #96 and #111; no `go.work` and no pseudo-version is committed.
- [ ] No worker env carries `FUNCD_CONTRACT_PATH`, no pool manifest row carries `contract`, and no
      `.funcd-contract-open.json` is written.
- [ ] The materializer still delivers the blob, and ADR-0303's scenario tests pass.
- [ ] Each Scenario has its named, passing test; the replaced tests are removed, not skipped.
- [ ] No new config key, API field, CLI flag or dependency.

## Consequences

- Positive: funcd is the one reader of a contract; no worker compiles a schema; the Node shim's `shim.mjs` shrinks
  from 428 KB to 146 KB and `pool.mjs` from 434 KB to 151 KB; the Python shim needs only the standard library; #1346
  closes.
- Negative: none new. As under ADR-0303, a call that reaches a shim without funcd's chain is not checked.
- Risks accepted: an artifact built by an older `funcd_shim.build` keeps its baked validators; the shim no longer runs
  them, and the python314 image keeps fastjsonschema, so the artifact still loads.

## Open questions

- Whether the python314 image, the dev shell and the Linux test VM drop fastjsonschema: a later ADR on ADR-0071.

## References

- Issue [#1346](https://github.com/pyvvo/funcd/issues/1346); pyvvo/funcd-typescript#96; pyvvo/funcd-python#111.
- A local run on 2026-10-30, with funcd at the branch of #1347 and a `go.work` naming both shim branches:
  `TestScenarioTheShimsStopChecking` failed only at its 422 check, the dev-mode contract tests passed, and the e2e
  suite of `pkg/funcd` passed (397 s).
- fastjsonschema's `compile_to_code` output imports `JsonSchemaValueException` from fastjsonschema (checked
  2026-10-30).
