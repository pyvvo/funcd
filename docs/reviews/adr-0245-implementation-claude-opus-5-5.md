## Verdict: pass — 0 blockers, 0 majors, 3 minors fixed before merge  (ADR-0245 implementation, model: claude-opus-5-5)

This reviews the whole implementation of ADR-0245, built in four PRs:

- pyvvo/funcd#1075, merged as `3a056f65`: `envresolve.Resolve`, and the two key lists in solo and pooled workers;
- pyvvo/funcd-typescript#77, released as `@funcd-dev/shim` 0.17.0 (tag `v0.17.0`);
- pyvvo/funcd-python#90, released as `funcd-shim` 0.12.0 (tag `v0.12.0`);
- pyvvo/funcd#1086, head `c5fc9dfc`: the shim pins, `TestScenarioCtxEnvUnchanged`, `TestScenarioCtxDev` and the status
  bumps. It merged as `a9abaf6a` after a rebase that touched only the feat rows; every reviewed file is unchanged.

The first three were reviewed before they merged. That review found three Minors and two Nits, all attributed to the
model, and all were fixed in the same PRs before the merges:

- the call-time test covered only `config`, so a `secret` or `catalog` client that copied its environment passed in
  both shims;
- `config.ts` re-declared `NodeJS.ProcessEnv`;
- `ctx_env_test.go` rebuilt the pool worker's name and manifest path instead of using the harness helpers;
- a comment cited a stale line, and `embed.go` had a 187-character line.

They are counted below as the three Minors, with the two Nits left out. No finding is open.

### ✅ Verified correct (keep it)

- **Checks**, run on `#1086`'s head `c5fc9dfc` through `scripts/agent/d`:
  - `go build ./...` and `go build -tags dev,e2e ./...` exit 0; `go mod verify` reports all modules verified.
  - `go tool golangci-lint run --build-tags dev,e2e` on `internal/envresolve`, `internal/function`, `pkg/funcd` and
    `cmd/funcdctl` reports 0 issues.
  - With `-race`, `TestScenarioCtxSecretShadowsConfig`, `TestWorkerSpecWritesBindingKeyLists`,
    `TestScenarioCtxPooledMember`, `TestScenarioCtxEnvUnchanged` (`-tags e2e`, 0.41 s) and `TestScenarioCtxDev`
    (`-tags dev`, 0.51 s) pass. The e2e test ran rather than skipped.
  - The builder's `scripts/agent/gate.sh` log shows every step passed on `60e7f484`: audit, `just ci-full` (547 s),
    linux, linux-tests and tree-unchanged. `c5fc9dfc` changes only the ADR's status text. On #1086, `ci` and
    `e2e` passed on `c5fc9dfc`.
- **Shims at their release tags**:
  - funcd-typescript `v0.17.0`: `node --test test/ctx.test.ts` passes 8 of 8, none skipped.
  - funcd-python `v0.12.0`: `pytest tests/test_ctx.py tests/test_pool.py::test_ctx_pooled_member` passes 9 of 9.
  - Both release commits' CI is green, and npm serves `@funcd-dev/shim@0.17.0` and PyPI `funcd-shim==0.12.0`.
- **Mutants**, built with `go test -overlay`, so the tree did not change. All three are killed:
  - a list written when it is empty fails `TestWorkerSpecWritesBindingKeyLists`;
  - an overridden config key left in `ConfigKeys` fails `TestScenarioCtxSecretShadowsConfig`;
  - no `FUNCD_SECRET_KEYS` fails `TestScenarioCtxPooledMember` and `TestScenarioCtxEnvUnchanged`.
- **Decisions in the code**:
  - **Decision 1**: `Resolve` records each kind's keys through `addKeys`, which applies the same `IsReservedKey`
    guard as `MergeEnvGuarded`, and then removes the keys a secret overrides from `ConfigKeys`
    (`internal/envresolve/envresolve.go`). `addBindingEnv` writes each list, sorted and comma-joined, only when it
    is not empty, after the guarded merge, so no ConfigMap or Secret key can forge it (`internal/function/secrets.go`).
    Solo workers on both paths and `poolSharedEnv` all call it.
  - **Decisions 2 and 3**: `ctx.config` and `ctx.secret` read the environment through their own list in both shims.
    TypeScript's `SecretClient` returns Promises; Python's clients are synchronous.
  - **Decision 4**: `ctx.catalog.get` returns `{url, token}` and throws an `Error` or raises `KeyError` naming the
    alias.
  - **Decision 5**: the call-time test now covers all three clients in both shims, and pooled members read their own
    copy (`ctx-pooled-member` runs a real two-member pool in each shim).
- **Contracts**: `Resolved` and `Resolve` match the ADR's Go signatures, and `ResolveEnv` wraps `Resolve`. Its one
  caller, `internal/provider/env.go:34`, is unchanged. `ConfigClient`, `SecretClient`, `CatalogEndpoint` and
  `CatalogClient` match the TypeScript contract. Python has `ConfigClient`, `SecretClient`, `CatalogClient` and
  `CatalogEndpoint(TypedDict)`, and the `FunctionContext` Protocol gains `config`, `secret` and `catalog`.
- **Scenarios**: each of the eight has its named test where the plan puts it:
  - the six shim scenarios in both shims;
  - `ctx-secret-shadows-config` in `internal/envresolve`;
  - `ctx-pooled-member` in `internal/function` too;
  - `ctx-env-unchanged` as a `pkg/funcd` e2e test, with `t.Parallel()`;
  - `ctx-dev` under `-tags dev`.
- **Reuse**:
  - The e2e test lives in an internal test file, to reuse `toggleEngine` and `asleepRig`
    (`pkg/funcd/issue769_internal_test.go`).
  - It skips without `node`, as six other e2e tests of the package do.
  - The pool test uses the harness's `poolWorkerOf` and `poolManifest`.
- **Scope**: the work stays within the plan.
  - Two additions are explained: Python's `embed.go` lists the three new modules, which its test requires, and the
    shared list helpers live in `config.ts` and `config.py`.
  - `go.mod` moves only the two pins. funcd-typescript goes from v0.15.0 to v0.17.0, which also brings ADR-0244's
    v0.16.0.
- **Tracking**: ADR-0245 is at `Reviewing`, and only its status line changed; F158 is at `reviewing`. #1086's
  description carries `Fixes #1070`.

### Definition of Done

18 / 18 items hold:

- the ADR's Review checklist, 7 of 7;
- its done-when, 2 counted: the funcd checks (build, lint, `-tags e2e` and `-tags dev` tests, `go mod verify`), and
  both language repos' CI;
- the generic DoD, 9 applicable: suite green, scenarios, real behavior, Contracts, tree, conventions, dependencies,
  scope, tracking. The per-driver contract suites do not apply, since no port changed.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0245 (implementation) → pass, 0/0/3, 3 model-attributed (fixed before merge),
DoD 18/18. See docs/reviews/model-scorecard.md.

### Recommendation

#1086 merged and closed #1070. This review stamps ADR-0245 `Implemented` and F158 `implemented`.
