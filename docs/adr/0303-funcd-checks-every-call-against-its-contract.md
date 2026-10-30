# ADR-0303: funcd checks every call against its contract, and the shims stop checking

- **Status**: Proposed
- **Date**: 2026-10-30
- **Deciders**: green-0-rabbit
- **Tags**: contract, validation, gateway, runtime
- **Realizes**: [FEAT-0001/F88](../feat/0001-feat-v1.1.md) (advertised == enforced: the check at call time)
- **Supersedes in part**: [ADR-0123](0123-runtime-compiled-io-validators.md) Decision 2 (the shim enforces 422, 500 and
  204) and Decision 4's rule that the shim gets the exact contract blob: funcd enforces the contract in two
  invokePlugins, and the shim gets an open contract. The delivered blob stays in the bundle, and funcd compiles it.
- **Relates to**: [ADR-0301](0301-funcd-defines-each-contract-rule-once.md) (the rules) ·
  [ADR-0302](0302-every-call-to-a-worker-runs-one-chain-of-invoke-plugins.md) (the chain) ·
  [ADR-0058](0058-contract-codegen-from-code-types.md) (the answers) · [ADR-0143](0143-redeploy-by-revision-switch.md)
  and [ADR-0190](0190-run-bound-to-its-revision.md) (which revision serves) · issue
  [#1339](https://github.com/pyvvo/funcd/issues/1339), which this ADR's implementation closes

## Context & Need

Each shim checks a call's input and the handler's result with its own engine (ADR-0123 Decision 2), so the same
contract accepts different data on Node and on Python (#1339). ADR-0301 defined one meaning for each rule in
`internal/contract`, and ADR-0302 gave every call to a worker one chain of built-in steps. This ADR runs the check in
that chain and stops the shims' checks.

The purpose: a contract means the same on every runtime and every path, because funcd checks it once, the input
before the worker and the result after it, for every caller of a cloudevent Function.

The check brings two costs into funcd's shared process, and the Decision bounds both. funcd holds a body in memory to
check it. And a check can be made slow: ADR-0301 bounds one pattern match, not a document, and Go's `big.Rat` parses a
number with an exponent near 10^6 in about 21 ms (measured on the dev host, 2026-10-30), as the engine does for every
integer and every bound.

## Scenarios

- `scenario: a-bad-input-never-reaches-the-worker` — Given a cloudevent Function scaled to zero whose input contract
  has an `email` field with `format: email`, When an external call sends `{"email": "a b@example.com"}`, Then the
  caller gets 422 `{"error": "event data does not match the input contract", "details": [{"path": "/email", …}]}`
  and the Function is never scaled up.
- `scenario: every-path-checks-the-input` — Given the same Function, When another Function, a workflow step, a sensor
  action and an App hook each send that input, Then each gets that 422 (the step fails permanently) and the worker
  never runs.
- `scenario: a-bad-result-becomes-500` — Given an output contract with `message: string`, When the handler returns
  `{"message": 42}`, Then the caller gets 500 `{"error": "handler result does not match the output contract",
  "details": [{"path": "/message", …}]}`.
- `scenario: a-void-result-is-checked` — Given the output contract `{"type": "null"}`, When the worker answers 204,
  Then the caller gets 204; When it answers 200 with `{"x": 1}`, Then the caller gets 500.
- `scenario: the-shim-gets-an-open-contract` — Given a nodejs22 and a python314 Function with the same contract, When
  each worker starts, Then its `FUNCD_CONTRACT_PATH` names a contract whose sides are both `{}`, and a call the
  contract refuses gets the same 422 from funcd on both.
- `scenario: a-contract-that-cannot-compile-stops-the-revision` — Given an artifact whose contract has the pattern
  `[z-a]`, When its revision deploys, Then the Function is Failed with reason `ContractInvalid` and no worker of that
  revision starts.
- `scenario: a-slow-check-is-cut` — Given an input contract whose pattern `^(a+)+$` applies to each of 1 000 strings
  of 40 `a` and a `!`, When the call arrives, Then the caller gets 422 within 3 s and the worker never runs.
- `scenario: a-body-over-the-limit-is-refused` — Given a contracted Function, When a call sends 2 MiB of input, Then it
  gets 413; When the worker answers 2 MiB, Then the caller gets 500; When a number in the input has the exponent
  `1e999999`, Then the caller gets 422 at that number's path.

## Scope

In: the two plugins and where they find a revision's contract; the bounds of one check; a contract that does not
compile; the 422 and 500 answers; the open contract the shims get.

Out:
- the rules (ADR-0301) and the chain (ADR-0302);
- http Functions, which carry no contract (ADR-0243);
- Workflow `CheckInput` and App values schemas (ADR-0301's Scope);
- removing the shims' validator code and their engines (ajv, fastjsonschema), which the open contract leaves idle: a
  cleanup task in each language repo;
- RFC 9457 for the 422 and 500: they stay ADR-0058's `{error, details}` (ADR-0302 Decision 6).

## Constraints & Decision drivers

- ADR-0058 and ADR-0123: advertised == enforced with no fail-open, and the 422, 500 and 204 answers keep their status
  and `error`; `details` takes ADR-0301's shape.
- ADR-0302: the check is two plugins of the one chain, and a refusal before the worker wakes nothing.
- funcd is one shared process: a check is bounded in memory and in CPU, since the call now spends funcd's resources,
  not its own cgroup's ([ADR-0291](0291-a-functions-workers-get-cpu-and-memory-limits.md)).
- [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md): a shim change is a release in its repo, then a pin
  here; the decision should hold with the shims funcd pins today.
- The decider's answers (2026-10-30): the gateway is the front; one shared check; the shims stop checking.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **Two plugins; the shim gets an open contract** | One check on every path, with the shims pinned today | The shims compile an empty schema they never need | **chosen** |
| A new env var that a shim release honors, to skip its check | No idle validator | The shims keep disagreeing until two releases and a pin | rejected |
| Stop setting `FUNCD_CONTRACT_PATH` | No new file | A shim then falls back to module-baked validators (Node `shim.ts:248`, Python `shim.py:365`), and funcd-python's `build.py:77-82` bakes them | rejected |
| Read each revision's contract from the registry (`artifact.InspectContract`) | No bundle read | `funcdctl dev`'s file artifacts have no registry; a second source beside the bundle's blob | rejected |
| No bounds beyond ADR-0301's match timeout | Simpler | A 1 MiB call can spend minutes of funcd's CPU | rejected |

## Decision

1. **Two invokePlugins**, appended to ADR-0302's chain after `deadline`: `contract-input` at Before and
   `contract-output` at After. They apply to every caller of a cloudevent Function, never to an http one.
2. **The contract is the one of the revision that serves the call**: a pinned step's revision (ADR-0190), else the
   Function's `status.servingRevision` (ADR-0143), else its `status.currentRevision`. The function reconciler serves
   it: it materializes that revision's artifact with its pinned digest (the bundle its worker gets, from the
   materializer's cache), reads the delivered blob (ADR-0123 Decision 4), and compiles both sides with
   `contract.Compile`, once per artifact digest.
3. **A contract that does not compile fails the revision**: the reconciler compiles it when it materializes the
   artifact, and a failure fails the generation with reason `ContractInvalid` (new), as a missing contract does with
   `ContractMissing` (issue #1084).
4. **The input check** reads the call's body as a CloudEvent envelope and checks its `data` (an absent `data` is
   `null`). A body that is not a JSON object is not checked: both shims refuse it with 400, as today. A failure
   answers 422 `{"error": "event data does not match the input contract", "details": […]}` as `application/json`.
5. **The output check** checks a 200 answer's body, and `null` for a 204; any other status passes unchanged. A
   failure answers 500 `{"error": "handler result does not match the output contract", "details": […]}`.
6. **The bounds of one check:**
   - a body over 1 MiB, the limit external bodies and fn-to-fn calls already have, is not checked: an input gets 413
     and a result 500, both RFC 9457 problems;
   - a number whose decimal exponent exceeds 1 000 in magnitude fails at its path, before the engine reads any number;
   - pattern matching stops after 1 s per check: each pattern asked after that fails its value, so the check still
     answers with details.
7. **The shims get an open contract.** For a contracted worker, solo or pooled, the reconciler writes
   `.funcd-contract-open.json`, the blob with both sides `{}` (ADR-0058's Json form), beside the delivered blob in the
   bundle root, and points `FUNCD_CONTRACT_PATH` at it. A shim's validators then accept everything, so funcd's
   plugins are the one check, with the shims funcd pins today. The void rule needs no shim: a null result answers 204,
   and the output check refuses a body a void contract forbids.

## Design model

None: the checks run in sequence inside one call (ADR-0302), and the compile cache, keyed by an immutable digest, adds
no ordering between actors (ADR-0272 Decision 1).

## Temporary workarounds

None: the decision holds with the shims funcd pins today.

## Contracts

```go
// internal/contract (new Option; ADR-0301's defaults are unchanged).

// WithCallBounds bounds each Validate call: pattern matching stops after budget, and each pattern asked later fails
// its value; a number whose decimal exponent exceeds maxExponent in magnitude fails at its path before the engine
// reads any number.
func WithCallBounds(budget time.Duration, maxExponent int) Option

// internal/contractcheck (new): the two invokePlugins.
package contractcheck

// Pair is one revision's compiled contract.
type Pair struct{ Input, Output *contract.Validator }

// Source returns the compiled contract of the revision that serves a call to ref, or nil when it delivers none. fn is
// the Function the data plane read, or nil on a client path.
type Source interface {
	ContractOf(ctx context.Context, ref activator.FunctionRef, fn *v1.Function) (*Pair, error)
}

// Plugins returns contract-input (Before) and contract-output (After).
func Plugins(src Source) []invoke.Plugin

// internal/function (new): the reconciler is the Source.
func (r *Reconciler) ContractOf(ctx context.Context, ref activator.FunctionRef, fn *v1.Function) (*contractcheck.Pair, error)
```

| Consumes | Exposes |
|---|---|
| The delivered blob of the serving revision's bundle; ADR-0301's `Compile` | The 422 and 500 answers above; reason `ContractInvalid` |
| — | `.funcd-contract-open.json` in each contracted bundle root, named by `FUNCD_CONTRACT_PATH` |

## Implementation plan

1. `internal/contract/rules.go`: `WithCallBounds`. A bounded `Validator` keeps a free list of compiled instances, each
   with its own deadline, which its pattern engine reads; `Validate` takes one, sets the deadline and puts it back. The
   exponent check walks the decoded document before the engine runs.
2. `internal/contractcheck/contractcheck.go`: the plugins. The input plugin reads up to 1 MiB + 1 of the body and puts
   it back; the output plugin reads with `Answer.Body(1 MiB)`. Both answer with a `Refusal` that has its own status,
   `Content-Type: application/json` and body (ADR-0302 Decision 6).
3. `internal/function`: `ContractOf` (the revision rule of Decision 2, `revisionTemplate`, `materialize`, the blob
   read, a digest-keyed cache); `materialize` compiles the blob and returns a `ContractInvalid` gate failure;
   `addContractEnv` and the pool manifest write and name the open contract.
4. `pkg/funcd/funcd.go`: append `contractcheck.Plugins(fnReconciler)` to the chain.
5. Tests: one per Scenario named `TestScenario` plus its name in CamelCase. `every-path-checks-the-input` has one test
   of that name per entry (`internal/dataplane`, `internal/workflow`, `internal/sensor`, Function and App hook callers
   included); `the-shim-gets-an-open-contract` checks both runtimes' worker environments in `internal/function`;
   `contractcheck` unit tests cover the bounds; `TestCallBounds` in `internal/contract`.
6. `just ci` green, and the e2e contract tests of `pkg/funcd/invoke_e2e_test.go` still pass. Definition of done:
   every Scenario test passes and #1339's three cases answer the same on both runtimes.

## Review checklist

- [ ] The plugins sit after `deadline`, apply to every caller of a cloudevent Function, and never to an http one.
- [ ] The contract comes from the pinned, serving or current revision, compiled once per digest; `ContractInvalid`
      fails a revision whose blob does not compile.
- [ ] The 422 and 500 bodies are `{error, details}` as `application/json`; a body over 1 MiB, a large exponent and the
      1 s budget answer as Decision 6 says.
- [ ] Every contracted worker, solo or pooled, gets `.funcd-contract-open.json` as its `FUNCD_CONTRACT_PATH`.
- [ ] Each Scenario has its named, passing test(s); no new config key, API field, CLI flag or dependency.

## Consequences

- Positive: one meaning for every contract on every runtime and path, with the shims pinned today; a bad input never
  wakes a sleeping Function; #1339 closes.
- Negative: the check spends funcd's CPU and memory, within Decision 6's bounds; the shims compile an empty schema they
  never use, until their validator code is removed.
- Risks accepted: during a revision switch, a call checked against the serving revision may be answered by the next
  one; both revisions' contracts were pushed through the same rules. A pattern's 1 s budget is wall-clock, so a check
  near it can pass on an idle host and fail on a loaded one.

## Open questions

- The cleanup of the shims' validators and engines: a task in each language repo, filed under the tracker of #1339.

## References

- Issue [#1339](https://github.com/pyvvo/funcd/issues/1339) and its refine brief of 2026-10-30.
- The shims' answers: funcd-typescript `shim/src/shim.ts:110-159` at v0.23.0; funcd-python
  `shim/src/funcd_shim/shim.py:269-301` at v0.18.1.
- Go `math/big`: `Rat.SetString` expands a decimal exponent.
