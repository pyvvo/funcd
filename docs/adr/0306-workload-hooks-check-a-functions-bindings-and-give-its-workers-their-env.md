# ADR-0306: Workload hooks check a Function's bindings and give its workers their env

- **Status**: Reviewing (2026-11-03); Accepted (2026-11-03; judged three times under the adr-judge skill, last verdict
  accept; the decider accepted the choices the draft made: today's gate order, a ConfigMap may still move the S3
  endpoint, the Executor's own env stays outside the hooks, and `memberEnv`'s delete loop goes)
- **Date**: 2026-11-03
- **Deciders**: green-0-rabbit
- **Tags**: provider, function, worker-env, bindings, refactor
- **Realizes**: [FEAT-0001/F220](../feat/0001-feat-v1.1.md) (a binding provider's part in a Function's workers)
- **Relates to**: [ADR-0305](0305-providers-start-and-stop-through-one-lifecycle.md) (its open question: the
  workload hook) · [ADR-0302](0302-every-call-to-a-worker-runs-one-chain-of-invoke-plugins.md) (the hook idiom) ·
  the gates of [ADR-0248](0248-stores-at-the-root.md), [ADR-0262](0262-function-owned-volumes.md),
  [ADR-0057](0057-secret-injection-last-mile.md)/[ADR-0093](0093-function-configmap-consumption.md),
  [ADR-0121](0121-declarative-referential-integrity-admission.md) · the env of
  [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md), [ADR-0270](0270-derived-s3-key-pairs-rotate.md),
  [ADR-0091](0091-function-catalog-consumer-binding.md)/[ADR-0137](0137-per-caller-catalog-query-rbac.md) ·
  [ADR-0215](0215-built-in-health.md) (readiness after the worker starts)

## Context & Need

Before a Function's worker starts, each provider it binds checks the binding and gives the worker env. In
`internal/function` this is written by hand: `bindings()` runs five gates one after the other; the S3 and Lake env is
assembled three times (a solo worker in process mode, in container mode, and a pool), and the S3 key pair is derived in
two places; the resolved config env travels separately through seven functions. The rule for a key two providers set
differs: in a solo worker a bound config key overrides even the issued S3 key pair (`function.go:3398` writes config
last), while a pool member's issued pair sits in its manifest row (`pool.go:887`) apart from the shared env that holds
config (`pool.go:818`). ADR-0305 left this workload hook open.

Purpose: each binding provider's part in starting a Function's workers in one place, its check and its env, so a solo
worker and a pool member get the same env and a new provider adds one hook. Caller: the Function reconciler, once per
pass. An unmerged experiment (2026-11-02) built this decision with every test green.

## Scenarios

- **scenario: workload-hooks-hold-in-order** — *Given* a Function that binds a missing ConfigMap and a Lake that is not
  Ready, *When* it reconciles, *Then* it fails with `ConfigResolveFailed` and no worker starts; once the ConfigMap
  exists it waits Pending with `LakeNotReady`. Test: `TestWorkloadHooks_HoldInListOrder`.
- **scenario: a-worker-gets-each-providers-env** — *Given* a solo Function that binds a ConfigMap, a Bucket and a Lake,
  *Then* its worker's env holds the config keys with `FUNCD_CONFIG_KEYS`, the S3 region and endpoint, its own key
  pair, its Lake URL and its Lake token. Test: `TestWorkloadHooks_ASoloWorkerGetsEachProvidersEnv`.
- **scenario: a-pool-shares-only-what-is-shared** — *Given* two pooled Functions that bind alike, *Then* the pool
  worker's env holds the shared part (config keys, S3 region and endpoint, Lake URLs) and each member's manifest row
  holds its own Lake token and its issued S3 key pair. Tests: `TestScenarioCtxPooledMember`,
  `TestPoolManifestCarriesMemberEnv`.
- **scenario: issued-keys-win** — *Given* a bound ConfigMap that sets `AWS_REGION` and `AWS_ACCESS_KEY_ID` on a Function
  that binds a Bucket, *Then* its worker, solo or pooled, gets the config's `AWS_REGION` and the issued
  `AWS_ACCESS_KEY_ID`. Test: `TestWorkloadHooks_IssuedKeysWinOverConfig`.

## Scope

**In**: the workload hook interface and its optional own part; the six hooks and their order; how their env merges
and where it goes; one env builder for a solo worker; the Lake token derived once.

**Out**: the Executor's own env (artifact, handler, local API socket, volumes, bundle dir, record bound), which stays
in the worker spec builder; the shared hooks of ADR-0305's open questions (boot reclaims, hold, backup, health, dev);
moving the hooks onto ADR-0305's provider types; new providers; the readiness of a running worker's bindings, which
ADR-0215's dependency check covers once the worker has started (`DependencyNotReady`).

## Constraints & Decision drivers

- Behavior-preserving except Decision 3's rule for issued keys: every gate keeps its reason, its phase and its
  requeue, and the first hook that holds still wins.
- A pair is issued with its worker (ADR-0270 Decision 5): no signature or shared env holds it, and a pool row gets it
  only when the pool worker is created or restarted.
- The function does not choose its S3 secret (ADR-0085).
- The ADR-0302 idiom: a required interface, an optional one found by a type assertion; `internal/function` only, no
  new dependency.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep the hand-written gates | Three copies of the S3 and Lake env, two of the key derivation, and a precedence that differs between a solo worker and a pool. |
| One interface with `check` and `own` on every hook | Four of the six hooks give no env of a Function's own; they would carry an empty method. |
| Hooks on ADR-0305's provider types in `pkg/funcd` | The Function reconciler cannot import `pkg/funcd`, which builds it; the providers would need a new port into it. Revisit when the providers build their own parts. |
| A bound config key always wins | Today's solo rule: a ConfigMap could replace the platform-issued key pair without a word, against ADR-0085. |
| Refuse a config key a provider also sets | A ConfigMap's valid keys would depend on what else the Function binds: binding a Bucket later would fail a Function whose ConfigMap sets `AWS_REGION`, a legitimate override. |

## Decision

1. **A workload hook** is one binding provider's part in starting a Function's workers. Its `check` runs before any
   worker exists: it holds the Function (a gate), fails the pass (an error), or returns the env the Function's workers
   share with the members of a pool that bind alike. A hook that also gives env of the Function's own implements
   `own`: env stable across passes, and an `issue` func that makes a part anew for each worker (nil when none). The
   reconciler finds `own` by a type assertion.
2. **One list**, in the order the gates hold today: root entries (ADR-0248), volumes (ADR-0262), config and secrets
   (ADR-0057, ADR-0093), data references (ADR-0121), the S3 gateway (ADR-0085), Lakes (ADR-0091, ADR-0137). The first
   hook that holds stops the pass with its reason. An `own` that fails holds the Function with its hook's reason, the
   Lake's `LakeResolveFailed` as today; for a pool member the pool path wraps the gate's message as its error and
   fails the pass, as today.
3. **Which key wins.** In the shared env an earlier hook's key wins, so a bound config key overrides a provider's
   shared default (`AWS_REGION`). A worker's own and issued keys win over the shared env, in a solo worker and a pool
   member alike: a ConfigMap never replaces the issued S3 key pair or the Lake token. A solo worker changes: today its
   config wins. A ConfigMap may still move the S3 endpoint: SigV4 never sends the secret, and egress stays
   default-deny (ADR-0115).
4. **Where the env goes.** A solo worker's env is the Executor's own (artifact, handler, record bound, bundle dir,
   local API socket), then the hooks' shared env, own env and a freshly issued part, then its volumes'. A pool worker
   gets the shared env; each member's manifest row carries the member's own env and, once the pool worker is created,
   its issued part, which no signature includes (ADR-0270 Decision 5). A member's row wins over the shared env because
   each pool host merges it last (funcd-typescript `shim/pool.mjs`, funcd-python `shim/src/funcd_shim/pool.py`);
   changing that merge is a change to the funcd ↔ shim contract, so it needs a funcd ADR.
5. **Config joins the shared env**: the resolved config no longer travels through the reconciler on its own.
6. **One env builder for a solo worker**, in process and container mode alike; only the paths it is given differ (the
   artifact, the bundle root, the local API socket).
7. **The Lake hook derives a Function's token once**, in `own`; `resolveLakeEnv` returns the URLs only.

## Design model

None: the hooks run in sequence inside one reconcile pass and add no state, timer or actor (ADR-0272 Decision 1).

## Temporary workarounds

None.

## Contracts

```go
// internal/function, unexported.
type workerEnv struct {
	shared, own map[string]string        // shared: alike for a pool's members; own: this Function's, stable
	issue       func() map[string]string // anew for each worker; nil when none
}

type boundEnv struct {
	worker workerEnv
	gate   *gateFailure // the first hook that holds the Function; nil when none does
}

type workloadHook interface {
	check(ctx context.Context, fn *v1.Function) (shared map[string]string, gate *gateFailure, err error)
}

type ownWorkloadHook interface {
	workloadHook
	own(fn *v1.Function) (own map[string]string, issue func() map[string]string, gate *gateFailure)
}

func (r *Reconciler) workloadHooks() []workloadHook                         // Decision 2's list
func (r *Reconciler) bindings(ctx context.Context, fn *v1.Function) (boundEnv, error) // runs every check
func (r *Reconciler) ownWorkerEnv(fn *v1.Function) (map[string]string, func() map[string]string, *gateFailure)
func (w workerEnv) into(env map[string]string)                              // shared, then own, then issue
```

| Hook | Holds the Function with | Shared env | Own env | Issued env |
|---|---|---|---|---|
| root entries | its reasons today | — | — | — |
| volumes | its reasons today | — | — | — |
| config | `ConfigResolveFailed`, `SecretResolveFailed` (requeue on NotFound) | the config keys, `FUNCD_CONFIG_KEYS` | — | — |
| data references | its reasons today (Pending, requeue) | — | — | — |
| S3 | — | `AWS_REGION`, `AWS_ENDPOINT_URL_S3` | — | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` |
| Lake | `LakeResolveFailed`, `LakeNotReady` (Pending) | `FUNCD_LAKE_<ALIAS>_URL`, `DUCKDB_EXTENSION_DIRECTORY` (dev) | `FUNCD_LAKE_<ALIAS>_TOKEN` | — |

| Dependencies & I/O | |
|---|---|
| Consumes | the Reconciler's dependencies: store, secret resolver, S3 gateway injection, Lake master secret and proxies |
| Exposes | the worker env keys of today; only Decision 3 changes which value a solo worker gets |
| Config keys, dependencies | none new |

## Implementation plan

1. `internal/function/workerenv.go`: the types, the list, `bindings`, `ownWorkerEnv` and the six hooks, as the
   experiment built them (commit `4e71a184`, unmerged) with `binder` renamed `workloadHook`, `into` writing shared,
   then own, then issue, and `own` returning its hook's gate.
2. `function.go` and `pool.go`: `bindings()` moves to `workerenv.go`; `convergeSolo`, `switchSolo`, `convergeRevision`,
   `workerSpec`, `convergePooled`, `ensurePool` and `poolSharedEnv` lose the resolved config parameter; `workerSpec`
   builds a solo worker's env with one builder for both modes; `memberEnv` no longer drops a member's keys that the
   shared env also holds (`pool.go:790-792` on main), since own keys win (Decision 3). No row changes: config cannot
   set a reserved `FUNCD_` key (ADR-0092).
3. `lake.go`: `resolveLakeEnv` returns the URLs; the Lake tests that read its token read the hook's own env instead.
4. Tests in `internal/function`: `TestWorkloadHooks_HoldInListOrder`,
   `TestWorkloadHooks_ASoloWorkerGetsEachProvidersEnv`, `TestWorkloadHooks_IssuedKeysWinOverConfig` (solo and pooled;
   fails on today's solo order),
   `TestWorkloadHooks_AContainerWorkerDialsItsSocketInside` (the env builder), with `TestScenarioCtxPooledMember` and
   `TestPoolManifestCarriesMemberEnv` unchanged; a `// scenario:` line above each Scenario's tests.
5. Run `scripts/agent/d go test -race ./internal/function/...`, lint, `scripts/agent/gate.sh`,
   `just lima-example-all` and `just lima-example app-todo`.

Done when the gate, the 13 lanes and the app-todo lane pass.

## Review checklist

- [ ] One list in Decision 2's order; each own part asserted at compile time; no hook wrapped.
- [ ] Every gate keeps its reason, phase and requeue; the first hook that holds wins.
- [ ] A solo worker and a pool member keep their issued and own keys over a config key of the same name.
- [ ] No issued key in a manifest row's signature or in the shared env.
- [ ] No function carries the resolved config on its own.
- [ ] One env builder for process and container mode; a container worker's socket is the in-container path, mounted.
- [ ] The Lake token is derived in one place.

## Consequences

- **Positive** (measured in the experiment): 33 fewer production lines; the resolved config left seven function
  signatures; the S3 key pair and the worker env are each built in one place; a new binding provider adds one hook.
- **Negative**: a solo worker whose ConfigMap sets an issued key's name now gets the platform's key (no deployment
  exists to break); reading a worker's env means reading the hooks.
- **Risks**: a hook placed out of order changes which gate a Function reports; the list and its test pin the order.

## Open questions

- The Executor's own env as hooks → the ADR that moves the hooks onto ADR-0305's provider types.
- The shared hooks (boot reclaims, hold, backup, health, dev) → a later ADR, after their experiments.

## References

- [ADR-0305](0305-providers-start-and-stop-through-one-lifecycle.md) (the lifecycle; its workload hook question) ·
  ADR-0302 (the point interfaces) · ADR-0085 (the function does not choose its secret) · ADR-0270 Decision 5 (a pair
  is issued with its worker).
- Prior art: wasmCloud v2 host plugins (`on_workload_bind`).
