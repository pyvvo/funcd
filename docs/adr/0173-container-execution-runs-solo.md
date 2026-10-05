# ADR-0173: Container execution runs every Function solo

- **Status**: Accepted (2026-10-05)
- **Date**: 2026-10-05 (drafted from issue #609; judged twice by three lenses)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, pooling, containerd, density, config
- **Realizes**: [FEAT-0000/F28](../feat/0000-feat-v1.md) (worker pooling — same-namespace density)
- **Supersedes in part**: [ADR-0050](0050-python-worker-pooling-subinterpreters.md) (Implemented), two points only:
  Decision 4's implication (lines 135-136) that the curated Python image ADR-0032 owns delivers pooled Python, and
  the Consequences sentence (lines 216-219) whose clause "the curated 3.14 **image** delivers the density win in
  container mode meanwhile" makes the gain "container-mode-or-upgrade-gated". ADR-0050's process-mode decisions (the
  subinterpreter host, `WithPoolShimFor`, the 3.14 floor for a process-mode `FUNCD_PYTHON`) stay, and so do line 18
  and line 71 (the image is still 3.14). ADR-0050 gets a "Superseded in part by ADR-0173" back-link at
  acceptance.
- **Relates to**: [ADR-0032](0032-curated-runtime-images-container-execution.md) (container execution) ·
  [ADR-0044](0044-worker-pooling-threads.md) (left `one sandbox = one pool` out of scope) ·
  [ADR-0046](0046-pooling-placement-policy.md) · [ADR-0149](0149-runtime-availability.md) and
  [ADR-0152](0152-runtime-worker-owner-kind.md) (both Proposed; see Decision 5)

## Context & Need

An embedder can call `funcd.New` with a pool shim (`WithPoolShim`, `pkg/funcd/options.go:223-231`, or
`WithPoolShimFor`, `:232-245`) and container execution (`WithContainerExecution`, `:385-391`) together (#609). Line
numbers are at origin/main 5dbb7fb. The pool worker that results cannot work:

- `createPool` (`internal/function/pool.go:353-376`) sets `Image: key.Runtime` (`:367`), a runtime name such as
  `nodejs22`, not an image reference from `imageFor` (`pkg/funcd/funcd.go:194-197`).
- It sets `Command` to the process-mode host launch, `node <data dir>/pool.mjs` (`:368`), and `FUNCD_POOL_MANIFEST`
  to a host temp path (`:369`). No mount carries the manifest, the member artifacts or the host into the container,
  and no `FUNCD_PORT` is set for the fixed netns port that container mode uses (`funcd.go:421-426`).
- `images/runtime/nodejs22/Dockerfile` has no `pool.mjs`. The `python314` image copies the `funcd_shim` package (with
  `pool.py`) from the pinned funcd-python module, but its entrypoint is the solo shim.

The `cmd/funcd` binary never hits this: its containerd mode returns only `WithRuntime` and `WithContainerExecution`
(`cmd/funcd/main.go:709`); only process mode adds `WithPoolShim` (`:733`) and `WithPoolShimFor("python", …)`
(`:763`). ADR-0046 Decision 4 specifies a process launch only, and ADR-0044 left `one sandbox = one pool` open. So
container-mode pooling was never designed. ADR-0050 still says the curated image delivers pooled Python in
container mode, and that claim is false.

The purpose: a configuration that cannot run is refused at start, and the documents state what container mode does.

## Scenarios

- `scenario: pool-shim-with-container-execution-refused` — Given options with `WithContainerExecution` and
  `WithPoolShim`, or `WithContainerExecution` and `WithPoolShimFor("python", …)`, When `funcd.New` runs, Then it
  returns a `fault.Invalid` error with op `funcd.New` and the message in Contracts, and starts nothing.
- `scenario: container-execution-without-pool-shim-accepted` — Given `WithContainerExecution` and no pool shim, When
  `funcd.New` runs, Then this check does not refuse it.
- `scenario: process-mode-pooling-unchanged` — Given `WithPoolShim` without container execution, When two Functions
  share a `spec.pooling.worker`, Then they run in one pool worker, as today.
- `scenario: pooled-function-runs-solo-in-container-mode` — Given a platform with `WithContainerExecution` and a
  Function that declares `spec.pooling.worker`, When it is applied and invoked, Then it runs in its own worker from
  `imageFor(runtime)`, as it does today with the `cmd/funcd` binary.

## Scope

In: the option check in `funcd.New`; the statement that container execution runs every Function solo.

Out: designing container-mode pooling (`one sandbox = one pool`; a Backlog card); pool host files in images.

## Constraints & Decision drivers

- Fail closed at start: a platform that cannot pool must not appear to accept a pool configuration.
- Behavior of the shipped binary does not change; it already runs solo in containerd mode.
- `funcd.New` already rejects an invalid configuration with `fault.Invalid` in `config.validate`
  (`pkg/funcd/funcd.go:268-285`, called at `:361`).

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **Refuse the combination at `New`** | Error at start, one place; matches the binary; small | An embedder loses an option combination that never worked | **chosen** |
| Ignore the pool shim in container mode and run solo | No embedder sees an error | A silently ignored option; the embedder believes pooling is on | rejected |
| Design container-mode pooling now | Pooling's density in container mode | A runtime-model decision (`one sandbox = one pool`, mounts, image entrypoints, port), far beyond #609 | deferred to the backlog |

## Decision

1. **`funcd.New` refuses a pool shim combined with container execution.** If `WithContainerExecution` is set and
   `WithPoolShim` or `WithPoolShimFor` is set, `New` returns a `fault.Invalid` error before it builds anything.
2. **Container execution runs every Function solo.** A Function that declares `spec.pooling.worker` on a
   container-execution platform runs in its own worker, as it does today with `cmd/funcd`.
3. **ADR-0050's container-mode claims are withdrawn** (the two clauses named in the header). Pooled Python exists
   in process mode only, with a 3.14 `FUNCD_PYTHON`.
4. **Container-mode pooling is not designed here.** It is a Backlog card on the board, to become its own ADR.
5. **This ADR owns the solo-in-container-mode decision.** ADR-0149 defers to it; while this ADR stands no containerd
   pool worker exists, so ADR-0152's Contracts row "Function pool" applies to process mode only.

## Temporary workarounds

None.

## Contracts

The check, in `config.validate` (`pkg/funcd/funcd.go`), after the required-dependency switch:

```go
if c.imageFor != nil && (len(c.poolShim) > 0 || len(c.poolShimsByFamily) > 0) {
	return fault.Invalidf(op, "worker pooling is not supported with container execution: WithPoolShim and WithPoolShimFor cannot be combined with WithContainerExecution")
}
```

| Options | Result |
|---|---|
| `WithContainerExecution` + `WithPoolShim` and/or `WithPoolShimFor` | `fault.Invalid`, op `funcd.New`, the message above |
| `WithContainerExecution` alone | accepted; every Function solo |
| `WithPoolShim` and/or `WithPoolShimFor`, no container execution | accepted; pooling as ADR-0046/0050 |

The check means "the option was given": any `WithPoolShimFor` call fills `poolShimsByFamily`, even with an
empty command; a bare `WithPoolShim()` leaves `poolShim` empty, never pools, and is out of scope on purpose.

No API type, CLI flag or `spec.pooling` field changes. The doc comments of `WithPoolShim`, `WithPoolShimFor` and
`WithContainerExecution` state the exclusion; `validate`'s comment covers invalid combinations too.

## Implementation plan

1. `pkg/funcd/funcd.go`: the check above; `pkg/funcd/options.go`: the three doc comments.
2. Tests, one per scenario, named `TestScenario<Name>`:
   - `pool-shim-with-container-execution-refused`: `pkg/funcd/funcd_test.go`, table over `WithPoolShim`,
     `WithPoolShimFor` and both; asserts `fault.KindOf(err)`, the op and the message.
   - `container-execution-without-pool-shim-accepted`: same file; `New` does not fail with this message.
   - `process-mode-pooling-unchanged`: the existing `pkg/funcd/pooling_e2e_test.go` (e2e-tagged) stays green.
   - `pooled-function-runs-solo-in-container-mode`: `pkg/funcd/funcd_test.go`, the InMemory preset plus
     `WithContainerExecution`, a recording fake runtime and a file materializer; two Functions share one
     `spec.pooling.worker` key and give two solo `Create` calls, each with `imageFor(runtime)` and no
     `FUNCD_POOL_MANIFEST`; no pool worker is created for the key.
3. Owed at draft, outside this file: the F28 row links ADR-0173 (`container mode runs solo`) and adds
   `container solo: adr` to its status; F28 is user-facing, so a tracking card "… — ADR-0173 / FEAT-0000 F28" goes
   to Backlog; the un-scoped container-mode pooling idea keeps its own Backlog card. At acceptance: the F28 entry
   moves to `accepted`, ADR-0050 gets its back-link, and the ADR-0173 card moves to In Progress.
4. Definition of done: each scenario has one passing test; `just ci` and `just ci-full` (the gate, for the e2e
   test) green; the PR carries `Fixes #609`.

## Review checklist

- [ ] The check sits in `config.validate`, returns `fault.Invalid` with op `funcd.New` and the exact message.
- [ ] Each of `WithPoolShim` and `WithPoolShimFor` alone triggers it with `WithContainerExecution`.
- [ ] Process-mode pooling and `cmd/funcd` behave as before.
- [ ] Each scenario has one named, passing test.
- [ ] The F28 row links ADR-0173; at acceptance it reads `accepted` and ADR-0050 carries the back-link.
- [ ] The implementing PR carries `Fixes #609`.

## Consequences

- Positive: an impossible configuration fails at start with a clear error; the documents match the code.
- Negative: pooling's density win is unavailable in container mode until the backlog design lands. Every pooled
  Function costs one worker there; on the homebox target (system Python 3.9) Python pools only in process mode with
  a 3.14 `FUNCD_PYTHON`. The `python314` image keeps `pool.py`, unused in container mode. ADR-0054's 3.14 base (lines
  24-25, 86) stays, but in container mode its pool reason no longer applies. ADR-0094's default co-location of workflow image
  steps in one pool (lines 114-119) holds in process mode only; in container mode those steps run solo.
- Risks accepted: an embedder that passed both options now gets an error instead of a broken pool worker.

## Open questions

None.

## References

- Issue [#609](https://github.com/pyvvo/funcd/issues/609); the implementing PR closes it (`Fixes #609`)
- ADR-0044 (out of scope: `one sandbox = one pool`); ADR-0046 Decision 4 (process launch only, line 118);
  ADR-0050 Decision 4 and Consequences; ADR-0054 Constraints (line 86); ADR-0094 Pooling & warmth (affected, not superseded)
- Sibling drafts: ADR-0149 (defers to this ADR on solo containerd mode), ADR-0152 (its "Function pool" row is
  process mode only here), ADR-0158 (pool member identity) and ADR-0168 (raw output pipes), whose pool-host aspects are process mode only
