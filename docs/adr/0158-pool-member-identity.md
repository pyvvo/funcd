# ADR-0158: Pool member identity — the pool host names the member on every channel, and same access shares a pool

- **Status**: Implemented (2026-10-05)
- **Superseded in part by**: [ADR-0190](0190-run-bound-to-its-revision.md) (2026-10-05) — lines 164-165 (the restart window): a manifest rebuild drains.
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: pooling, shim, identity, local-api, logs, traces, security
- **Realizes**: [FEAT-0000/F28](../feat/0000-feat-v1.md) (worker pooling — same-namespace density)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0158` back-link at acceptance:
  - [ADR-0044](0044-worker-pooling-threads.md) Decision 2 ("`GET /health/readiness` 200 once all workers loaded"),
    Decision 4's first sentence ("On a boot-time worker `exit(3)` … the host fails pool readiness fast"), and the same
    rule in Contracts' readiness row and Review checklist ("health gates on all workers loaded") — Decision 4;
  - [ADR-0046](0046-pooling-placement-policy.md), wherever it keys a pool by `(namespace, runtime, worker-id)` alone:
    Scope In and Out ("never … migrates running functions"), Constraints ("no implicit moves"), Decisions 2, 3
    ("never splits the worker id"; PoolFull "worker `<id>` is full") and 5 (`upstreamFor` by pool key), scenarios
    `same-worker-co-locates` and `pool-cap-guard`, Contracts (`Pooling.Worker` doc, three-field `PoolKey`,
    `Assign(fn, sameKey, limit)`), Dependencies & I/O, Definition of done, Review checklist, and Consequences'
    "(+) Predictable + reversible … no hidden migration" — Decision 5;
  - [ADR-0050](0050-python-worker-pooling-subinterpreters.md) Decision 2 ("Pool key = (namespace, runtime, worker-id)"),
    scenario `py-pool-colocates` (now for the same access), Contracts' Per handler row ("host exits 3 on a bad member")
    and Routes row ("`GET /health/{readiness,liveness}` → 200"), Review checklist ("a bad member → host exits 3");
  - the fixed caller identity ("the request never names a caller", "no client-asserted identity"), on a pool socket:
    [ADR-0064](0064-fn-to-fn-rpc-links.md) Decision 3 and Review checklist; [ADR-0069](0069-kv-data-plane.md)
    Constraints, Decision 4, Definition of done, Review checklist; [ADR-0127](0127-context-blob-data-plane.md)
    Constraints, Definition of done, Review checklist — Decision 1;
  - [ADR-0081](0081-function-log-capture-side-channel-blob.md) Dependencies & I/O, Consumes ("the connection-scoped
    caller identity for the `Resource`/`inv` tag"), [ADR-0101](0101-trace-capture-invocation-span.md) Decision, Trace
    context ("storage scoping … is host-stamped from `WorkerSpec`, never client-asserted") — Decision 3;
  - [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md) Constraints, scenario `cannot-forge-peer`'s premise,
    Alternatives and Consequences ("a function holds only its own derived secret, so it can act only as itself"),
    between pool mates — Decision 6;
  - [ADR-0093](0093-function-configmap-consumption.md) the pooled gate ("A pooled function declaring `spec.config` or
    `spec.secrets` fails closed"): Scope, Decision 3 and its heading, scenario `pooled-config-solo-gated`, Constraints,
    Contracts, Review checklist — Decision 2;
  - [ADR-0094](0094-workflow-engine-core.md) Pooling & warmth ("same-runtime image steps co-locate in one worker pool"):
    only those with the same access; [ADR-0143](0143-redeploy-by-revision-switch.md) Decision 8 ("S follows C once the
    pool worker is ready": now once the member reads `ready`) and Scope ("the resolver routes by the current spec's pool
    key": now by `status.pool`).
- **Refines** (additions only): ADR-0044 Decisions 2, 4 and Contracts; ADR-0050 Decision 1 and Contracts; ADR-0081 and
  ADR-0101 records (`funcd.member`).
- **Relates to**: ADR-0011 · 0089 · 0091 · 0121 · 0136 · 0137 · 0141 · 0142 · 0160 · 0161 (counts a pooled member's
  worker by its `/health/members` entry) · 0162 (pooled catalog consumers) · 0168 (Path A; reads `PoolMembers`' names) ·
  0175 (S3 keys carry the owner kind) · 0177 (a Policy governs only its own namespace). Merged by whichever lands
  second: ADR-0149 and 0152 edit `createPool`/`poolHostFor`; ADR-0152's pool ID row gains `__<access>` and its
  `reclaimOrphanPools` OwnerKind filter sits beside `poolKeyFor`; ADR-0172 filters `sameKeyFunctions` by
  `servingMember`; `makeInvoke`'s options object (Python: keyword-only) carries `member` (here) and ADR-0165's `sink`.
- **At acceptance**: FEAT-0000 row F28's "keyed by `(namespace, runtime, worker-id)`" (feat `:87`) gains the access;
  `blueprint.md`'s Function state machine (`:657-669`) gains `Ready --> Failed : a gate fails on a later pass (a new
  revision's pooled member cannot load, …)` and from `Degraded`; the later-accepted of this ADR and ADR-0169 adds
  `Failed --> Ready` (ADR-0169's text); local API identity (`:95`, `:505`): "connection-scoped; on a pool socket, the
  member in `X-Funcd-Member`, checked (ADR-0158)".

## Context & Need

A pool worker is one process with one identity (`__pool__<runtime>__<worker>`, `internal/function/pool.go:470-476`);
nothing a member sends back names it (main 1193be6). **#42**: the local API socket exists only on the solo path
(`addInvokeSocket`, `function.go:1600`, called only at `:1752`), so `context.kv`/`blob`/`invoke` fail in a Ready pool;
pooled config/secrets fail closed (`secrets.go:36-41`); catalog, S3 and `FUNCD_BUNDLE_DIR` env are dropped
(`function.go:497-501`). **#70**: a member with no `handle` export makes either host exit 3 (`pool.ts:94`,
`pool.py:171-176`) and respawn each period while siblings stay Deploying, or go Degraded once serving; hosts listen only
after every member loads, and funcd judges the pool worker as a solo replica (boot limit, `stopNeverReady`). **#80**:
member logs and spans are stored as the pool worker. Need: a pooled Function behaves like a solo one on every channel.

## Scenarios

- `scenario: pooled-member-kv` — Given `a` and `b` with `pooling.worker: agents` binding unowned table `t` holding a
  value, When `a` calls `context.kv.get` on `t`, Then it returns the value, on `nodejs22` and `python314`.
- `scenario: pool-refuses-outsider` — Given a pool of `a`, `b` and `c` in another pool, When a request on the pool
  socket names `c` or no member, Then 403 and nothing is read, written or invoked; naming `b`, it is served as `b`.
- `scenario: pooled-member-s3-identity` — Given pooled `a` and `b` binding prefix `raw` of Bucket `k` (owned by neither)
  and object `raw/x`, When each handler returns its env S3 credentials, Then `a`'s are `s3gateway.DeriveKeypair` for kind
  Function and `a` (ADR-0175), `b`'s likewise for `b`, and a GET of `raw/x` via the S3 gateway succeeds with each.
- `scenario: pooled-member-logs` — Given pooled `a`, `b`, When `a` logs `hello-a` via Path B (Node `console.log`, Python
  `logging`), Then `a`'s logs hold it and its span is under `a`; `b`'s not, and nothing under a pool worker's name.
- `scenario: pool-member-load-failure` — Given pooled `a`, `b`, `c` of one access, `b` with no `handle` export, When
  deployed, Then `b` is Failed, Ready=False/ShapeInvalid, ShapeValid=False with its error, RevisionReady=False/
  ShapeInvalid; `a` and `c` are Ready and answer calls; once the pool's manifest holds `a`, `b` and `c`, the pool
  process keeps its PID across two supervision periods.
- `scenario: pool-splits-by-access` — Given `a` and `b` with `pooling.worker: agents` on `nodejs22`, only `a` binding
  table `t`, When both are Ready, Then they run in two pool workers with distinct `status.pool` and no error condition.
- `scenario: pool-splits-by-grant` — Given pooled `a` and `b` of one access, When a RolesAssignment grants `b` a role,
  Then `b` gets its own pool and `a` naming `b` gets 403; one granting both one role and scope keeps them together.
- `scenario: workflow-steps-split-by-secrets` — Given a default-shared Workflow, `s1` bound to Secret `db`, `s2` to
  none, When applied, Then both are Ready, only `s1`'s env holds `db`'s keys, and their `status.pool` differ.

## Scope

In: naming a member on the local API, credentials, records, load state; member env; the manifest; both hosts; the
fingerprint and re-keying; `status.pool`; no pooled gate. Out: adding a member live; Path A output (ADR-0168);
`PYTHONPATH` (ADR-0089); isolation between mates and credential rotation (Decision 6).

## Constraints & Decision drivers

One process, "fault/resource isolation, not security" (ADR-0044/0050); default-shared steps must work (ADR-0094);
a pool stop stops every member (ADR-0142); two shim releases, a pin, no older-shim support (ADR-0141); process mode only.

## Alternatives considered

| Option | Outcome |
|---|---|
| **One socket per pool; the host names the member per call; funcd checks it against the set** | **chosen** (builds ADR-0064) |
| A socket per member | rejected: a member can dial a sibling's socket, so it isolates nothing more; N listeners |
| Fail closed with a pooled gate for kv/blob/links/catalogs | rejected: default-shared steps with bindings never Ready; fixes neither #70 nor #80 |
| A telemetry channel per member | rejected: members are threads writing one fd 3 |
| Name the failing member, keep the whole-pool exit(3) | rejected: siblings still go down, the host respawns |
| One pool per worker id; refuse mixed access | rejected: owners hand-split; mixed-access workflows fail |
| Fingerprint the six binding fields, not ownership | rejected: a pool mate could write as an owner of a bound table or any owned prefix |

## Decision

1. **Local API: one socket per pool, the member named on every call.** A pool worker's `FUNCD_INVOKE_SOCKET` is the
   pool's listener (`PoolSocketFor`). Each request carries `X-Funcd-Member: <Function name>`, served as `Ref{namespace,
   member}` (invoke, KV, blob) if the name is in the member set, else 403 (absent header included); solo ignores it.
2. **Member env.** S3 (ADR-0085/0175: kind Function) and catalog (ADR-0091/0137) identities derive from the name.
   - **Per member**, in the manifest row `env`, set in that member's env only: `AWS_ACCESS_KEY_ID`,
     `AWS_SECRET_ACCESS_KEY` (when binding `spec.blob`), `FUNCD_CATALOG_<ALIAS>_TOKEN` per catalog alias,
     `FUNCD_BUNDLE_DIR`. The manifest is `0600` in a funcd-owned `0700` `PoolManifestDir`, named with the access,
     deleted by `reclaimOrphanPools`; rows enter `manifestSignature`, stable as both credentials are deterministic.
   - **Shared** (config, secrets, `FUNCD_CATALOG_<ALIAS>_URL`, `AWS_REGION`, `AWS_ENDPOINT_URL_S3`,
     `DUCKDB_EXTENSION_DIRECTORY`): from `self`, the member being reconciled, whose already-resolved `secretEnv` and
     `catalogEnv` pass through `convergePooled` → `ensurePool` → `createPool` into the process env (catalog `_URL` keys
     only); never from a manifest member, which may sit at an older serving revision. Config/secrets resolve as the
     namespace's developer (ADR-0057), so `self`'s resolution holds for the key; `poolManifest` resolves nothing.
   - **No pooled gate**: each member's own pass resolves config and secrets like solo and fails its own gate.
   - **Precedence** is solo's: a row omits any key the shared env sets; the reserved-`FUNCD_` guard holds.
3. **Logs and spans name their member** with `"funcd.member": "<name>"`. For a pool worker funcd stores a record under
   that Function when it is in the set (namespace, replica stay host-stamped); `createPool` records the set before
   `Create` and the capture hook snapshots it per process at `Start`, so first-start records and a removed member's
   drained ones are kept. Others are dropped, counted, logged at Warn at most once a minute per pool. Solo ignores it.
4. **One member fails alone; the pool worker is never stopped for a member.** The host listens at once and loads members
   independently, each bounded by `FUNCD_POOL_LOAD_TIMEOUT_MS` (`runtime.bootTimeout`, ADR-0163, in ms). `GET
   /health/members` reports `loading`, `ready`, `failed` (first load in this process failed, or `load timed out`; not
   retried in-process, no exit, calls get 503) or `restarting` (Node only: faulted after boot, restarted with backoff up
   to 10 s; a failed restart is another fault, never `failed`). `convergePooled` maps the member's entry, like solo:
   `ready` → ready (S follows C, ADR-0143); `loading`, `restarting`, no entry, a failed probe, or `failed` in a pass
   that started serving → not ready (Degraded once serving); `load timed out` → Ready=False/CrashLoopBackOff (counted on
   ADR-0160's `bootBackoff` under `runtime.NewInstanceID(ns, name, rev, 0)`, once per pool `CreatedAt`, so ADR-0169's
   `forget` drops it too), whose ADR-0160 wait only re-reads `/health/members` (no reload before the next pool start,
   Open question 2); a load error before serving → `shapeFailed` (Failed, Ready=False/ShapeInvalid, ShapeValid=False,
   RevisionReady=False/ShapeInvalid), requeued after the supervision period. A later pool start that reads `ready` makes
   it Ready with no spec change. funcd judges the pool worker on its own liveness (`markPoolLive`), never on member
   state or through `stopNeverReady`: an exited one is recreated on ADR-0142's backoff (#603); a running one silent on
   `/health/liveness` for `runtime.bootTimeout` since its last answer (else `CreatedAt`) is restarted.
5. **Same access = same pool.** `PoolKey` gains `AccessHash` (first 16 hex of SHA-256 of canonical `accessDoc`; nil =
   empty; `pooling.AccessHashOf` is pure). Counted: `spec.kv`, `spec.blob`, `spec.catalogs` (whole entries),
   `spec.links` (alias, target), sorted by alias; `spec.secrets`, `spec.config` (declared order); every KV table the
   Function both owns and binds (a table is reached only through its binding alias, and an owner writes where a binder
   reads); every Bucket prefix of its namespace it owns, bound or not (an owner writes it through the S3 gateway).
   **Grants outside the spec** (decided 2026-10-05), each counted by what it grants, never by its principal, so mates
   holding the same grant stay together: `group/<metadata.resourceGroup>`; per RolesAssignment (ADR-0136) entry whose
   principal is this Function, `rolesassignment/<name>@` SHA-256 of `{roleRef, scope}`, both defaults applied
   (duplicates removed; a Role's actions are not counted, as mates holding one `roleRef` change together); per
   EgressPolicy (ADR-0117) whose `appliesTo` lists it, `egresspolicy/<name>@` SHA-256 of `spec.rules` (an empty
   `appliesTo` grants all alike); per statement of a user `Policy` (ADR-0074) that references `Function::"<ns>/<name>"`
   anywhere (a cedar-go v1.8.0 AST walk), `policy/<ns>/<name>#<j>@` SHA-256 of the statement, the PDP's own ID; the
   PDP's policies compiled from EgressPolicies and RolesAssignments count only by their own rules. Not counted: scaling,
   replicas, handler, image. Workflow steps may share a pool until the materializer patches their KV bindings.
   - **Reads**: `pooled` decides with no I/O; then the pass builds one access index: one List each of the namespace's
     KVStores, Buckets, RolesAssignments, EgressPolicies and user Policies (`policySource`'s first List, this namespace
     only: per ADR-0177 a Policy governs only its own, and the PDP compiles one PolicySet per namespace). An absent
     referent adds nothing (ADR-0121 holds a bound one Pending); a List error fails the pass, reclaiming nothing.
   - **Re-keying**: KVStore, Bucket, RolesAssignment, EgressPolicy watches (`MapAccess`) queue the namespace's Functions
     declaring `pooling.worker` (Idle included); a Policy change queues every pooled Function of its namespace
     (ADR-0177; a watch sees only the new text); a key unlike `status.pool` runs `reclaimOrphanPools`. Until the moved
     member's pass, mates may still name it on the old socket, and a former mate's pass may rebuild the old pool without
     it: its calls get 404, then 503 while the new pool loads it (ADR-0046's membership restart window).
   - **Effect**: different access → different pool workers, no error; the cap counts per key; PoolFull names the pool.
     `status.pool` = `<runtime>/<worker>/<access>`, set before the first gate (rejected and gate-failed members
     included), empty when solo; the call path reads it via `pooling.ParsePool`.
6. **Trust rule: only separate processes isolate, and a credential once shared stays shared.** A member can name any
   pool mate on every channel and read every mate's S3 keypair and catalog token from the manifest (ADR-0137's
   `forged-function-token-denied` still holds). Neither credential rotates (ADR-0085, ADR-0137), so leaving the pool, a
   later ownership or grant, or going solo revokes nothing; only the pool socket stops serving a departed member. The
   principal carries no attribute beyond Decision 5's today; a new one joins the key. A Function not to be impersonated
   never pools: no `pooling.worker`, or `pooling.mode: isolated`.
7. **Release.** One release each shim repo, pinned only by or after this ADR's funcd PR (ADR-0141); a sibling (ADR-0151,
   0165, 0168) pinning first takes an earlier tag, as a pre-0158 funcd reads an unloaded member as Ready.

## Temporary workarounds

None (no deployment exists).

## Contracts
**Dependencies & I/O** (wire):
```
manifest row     {name, artifact, handler, contract? (ADR-0123), env?}  new: env (Decision 2)
process env      FUNCD_POOL_MANIFEST · FUNCD_PORT | FUNCD_PORTFILE · FUNCD_INVOKE_SOCKET (pool socket, new)
                 · FUNCD_POOL_LOAD_TIMEOUT_MS (runtime.bootTimeout in ms, new) · the shared binding env (new)
member load      first load past the bound → failed, "load timed out"; restart past it → another fault (Node)   new
GET  /health/members    200 [{"name", "state": "loading"|"ready"|"restarting"|"failed", "error"?}]               new
GET  /health/readiness  200 once no member is loading (was: once every member loaded)
GET  /health/liveness   200 while the host is up; the host listens before any member loads                       new
POST /function/<name>   unchanged; a member that is not ready → 503 {"error": …}
local API request       header X-Funcd-Member: <member>                                                           new
telemetry record        "funcd.member": "<member>", from the pool's name passed to capture and tracespan          new
                        (TS installConsoleCapture(env, sink, member?) → buildLine; Python _poolworker.init(…, name) →
                        install_log_capture(channel, member=name)); a solo shim passes none, omitting the field
Node host↔worker boot   worker → {ready} | {failed: "<error>"} then exit(3)                                       new
```
```go
// internal/pooling
type PoolKey struct { Namespace v1.NamespaceName; Runtime, Worker string; AccessHash string /* AccessHashOf */ }
func (k PoolKey) String() string // "<runtime>/<worker>/<access>", the status.pool form
// ParsePool: exactly three "/"-segments (a worker id is a DNS-1123 label, Function.Validate); ok false otherwise.
func ParsePool(ns v1.NamespaceName, s string) (key PoolKey, ok bool)
// accessDoc: KV, Blob, Catalogs, Links sorted by alias; Secrets, Config in declared order; Owned, Grants sorted.
type accessDoc struct {
	KV []v1.FunctionKV `json:"kv"`; Blob []v1.FunctionBlob `json:"blob"`; Catalogs []v1.FunctionCatalog `json:"catalogs"`
	Links []accessLink `json:"links"`; Secrets []v1.ObjectName `json:"secrets"`; Config []v1.ObjectName `json:"config"`
	Owned []string `json:"owned"` // "kv/<store>/<table>" owned and bound; "blob/<bucket>/<prefix>" owned
	Grants []string `json:"grants"` // "group/<rg>", "<kind>/<object>[#<j>]@<sha256>", by what is granted (Decision 5)
}
type accessLink struct { Alias string `json:"alias"`; Target v1.ObjectName `json:"target"` }
func AccessHashOf(spec v1.FunctionSpec, owned, grants []string) string
func KeyOf(fn *v1.Function, owned, grants []string) (key PoolKey, ok bool)
// PoolFull: "pool <runtime>/<worker>/<access> is full (<limit>); use another pooling.worker".
type Assigner interface { Assign(fn *v1.Function, key PoolKey, sameKey []*v1.Function, limit int) (Assignment, error) }
// api/types/v1alpha1 FunctionStatus: Pool string `json:"pool,omitempty"` — "<runtime>/<worker>/<access>"; empty when solo
// internal/workernode/local (also on internal/function's InvokeSocketProvider); pool is the instance name
// __pool__<runtime>__<worker>__<access>, not the status.pool form; Remove(ns, pool) stops it; idempotent
const MemberHeader = "X-Funcd-Member"
func (m *Manager) PoolSocketFor(ns v1.NamespaceName, pool v1.ObjectName, members []v1.ObjectName) (string, error)
// internal/funclog — wireRecord and spanWire gain Member string `json:"funcd.member"`
func RoutePool(ctx context.Context, r io.Reader, sinks Sinks, pool Resource, isMember func(name string) bool, log *slog.Logger) error
// internal/function — poolManifestEntry gains Env map[string]string `json:"env,omitempty"`. Deps gains
// PoolManifestDir string: <dataDir>/pool (funcd.WithPoolManifestDir, set by cmd/funcd), else a private temp dir New
// creates and Shutdown removes; New empties it before the first reconcile. writePoolManifest writes 0600 via an
// O_CREATE|O_EXCL temp file and rename into a funcd-owned 0700 non-symlink dir (created when absent, else refused).
// pooled is the no-I/O pooling check of steadyState, upstreamForFn, endpoints.Upstream, runtimeUnavailable.
// convergePooled, ensurePool, createPool and restartPool gain self's resolved secretEnv and catalogEnv.
func writePoolManifest(dir string, key pooling.PoolKey, manifest []poolManifestEntry) (string, error)
func (r *Reconciler) pooled(fn *v1.Function) bool
type accessIndex struct{ owned, grants map[v1.ObjectName][]string } // one List per kind of Decision 5, per pass
func (r *Reconciler) accessIn(ctx context.Context, ns v1.NamespaceName) (accessIndex, error)
func (r *Reconciler) poolKeyFor(fn *v1.Function, idx accessIndex) (key pooling.PoolKey, pooled bool)
func (r *Reconciler) MapAccess(ctx context.Context, obj v1.Object) []controller.Request
func (r *Reconciler) poolLastLive(key pooling.PoolKey) time.Time
func (r *Reconciler) markPoolLive(key pooling.PoolKey, at time.Time)
func (r *Reconciler) PoolMembers(ns v1.NamespaceName, worker v1.ObjectName) (
	members []v1.ObjectName, isMember func(name string) bool, ok bool) // the set snapshot's names, for ADR-0168
```

## Implementation plan

1. **funcd-typescript** — `shim/src/pool.ts`: Decision 4; workers get `env: {...process.env, ...spec.env}` and post
   `{failed}` before `exit(3)`; `kv.ts`, `blob.ts` (`makeKV/makeBlob(member?)`), `invoke.ts` (`member` in `makeInvoke`'s
   new options object) send the header; `funclog.ts`, `tracespan.ts` stamp `funcd.member`; rebuild `shim/*.mjs`.
   `shim/test/pool.test.ts`: `pooled-member-kv`, `pooled-member-logs`, `pool-member-load-failure`, hung import → `load
   timed out`, post-boot fault → `restarting`, member env invisible to a sibling. Release.
2. **funcd-python** — `pool.py`: serve at once, load concurrently, bound by `FUNCD_POOL_LOAD_TIMEOUT_MS`, then shutdown
   `wait=False, cancel_futures=True`; `_poolworker.init` keeps the load error text (`BrokenInterpreterPool` loses it)
   and applies the member `env` after `_isolate_process_state()` (writes skip `putenv`, so stay in that interpreter); no
   exit 3; `/health/members` (no `restarting`); `kv.py`, `blob.py`, `invoke.py` (keyword-only `member`) send the header,
   `funclog.py`, `tracespan.py` stamp. `shim/tests/test_pool.py`: the scenarios, hung import, member env. Release.
3. **funcd**, one PR that `go get`s both tags: the Contracts in `internal/pooling`, `api/types/v1alpha1` (docs drop the
   pooled gate; `just generate`), `internal/workernode/local`, `internal/funclog`, `internal/function` (Decisions 2, 4,
   5; adds the pooled shape-failure case to ADR-0169 Decision 4's `requeueFor(Failed)` rule, the supervision period;
   `convergePooled` keeps `ensurePool`'s `revisionPass.retryAt`, so a pool worker in its backoff is not read as failed;
   `readyReplicas` probes pool liveness with no boot limit; `poolKeyFor` serves `assign`, `sameKeyFunctions`,
   `admittedMembers`, `reclaimOrphanPools` (the delete path builds its own index); the shim harness serves
   `/health/members`), `internal/testkit/bench` (`startNodeShim`, `startPyShim` poll readiness to 200, fail on a
   `failed` member before sampling RSS), `pkg/funcd` (`RoutePool` via `PoolMembers`, the five `MapAccess` watches,
   `WithPoolManifestDir`).
   - New tests: `TestScenarioPoolRefusesOutsider`, `TestScenarioPoolSplitsByAccess`, `TestScenarioPoolSplitsByGrant`
     (a subtest per grant kind and the group); units for every rule of Decisions 1–5 (key order and nil, owned table
     vs prefix, five Lists, `restarting` vs pool liveness, `failed` → `ready`, `failed` serving → Degraded, no stale
     socket or manifest, per-row credentials, shared env from `self`, stable signature, manifest refusals, `status.pool`,
     `RoutePool` records).
   - Rewritten: `TestScenarioPooledConfigSoloGated` (+ `pooled-fails-closed`: Ready with config and secrets);
     `TestIssue69_…` (own gates, another pool); `TestIssue72_…` (`restarting` sibling, `good` Ready); `TestIssue355_…`
     (load error → ShapeInvalid, requeued; `load timed out` → CrashLoopBackOff); `TestIssue422_…` (silent liveness →
     recreated); every `__pool__nodejs22__<w>` name (incl. `TestIssue70_…`, `pooling_e2e_test.go`) gains `__<access>`.
   - e2e on `nodejs22` and `python314`: `TestScenarioPooledMember{KV,S3Identity,Logs}`,
     `TestScenarioPoolMemberLoadFailure`, `TestScenarioWorkflowStepsSplitBySecrets`.
4. **Done**: all scenario tests pass; `just ci-full` green; `go.mod` pins both releases; it closes #42, #70, #80.

## Review checklist

- [ ] Decisions 1–5 hold as stated (shared env from `self` only, per-row credentials, a `0600` manifest in a `0700` dir,
      no pooled gate, 403 for any non-member, nothing stored under a `__pool__` name, no member state stops the pool
      worker, keys count exactly Decision 5's fields, grants included).
- [ ] Every test the plan names asserts its behavior; no absolute path or username in any changed file.

## Consequences

- Positive: pooled Functions get their bindings, telemetry and own load failure; default-shared steps work.
- Negative: more pool processes (owners and granted Functions pool alone); shims move with funcd; derived credentials
  on disk; a failed member reads `loading` each start; an access change re-runs the pooled passes it can affect (five
  Lists, one Policy parse each); member env is language-level; a hung Python import holds its thread.
- Not isolated: a load that kills the process fails no member alone (ADR-0044's blast radius). Risks: Decision 6.

## Open questions

- Should funcd bind derived credentials to a pool or rotate them on a split? A follow-up to ADR-0085 and ADR-0137.
- Should the host reload a `load timed out` member on ADR-0160's wait, rather than at the next pool start?

## References

- Issues #42, #70, #80 (verified on main 1193be6; #603, #605 since); the ADRs linked above; funcd-typescript v0.4.4,
  funcd-python v0.3.5; CPython 3.14 `InterpreterPoolExecutor`; cedar-go v1.8.0.
