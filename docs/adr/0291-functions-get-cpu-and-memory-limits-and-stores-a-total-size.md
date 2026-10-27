# ADR-0291: Functions get CPU and memory limits, and stores get a total size

- **Status**: Proposed (2026-10-27)
- **Date**: 2026-10-27
- **Deciders**: green-0-rabbit
- **Tags**: runtime, storage, limits, quota
- **Realizes**: [FEAT-0016/F206](../feat/0016-feat-workload-limits.md) (Function CPU and memory limits) and F207 (a
  store's total size)
- **Answers**: the decider's refinement of 2026-10-27: `spec.resources` on a Function, a node default
  (`functions.defaultResources`, 1 CPU and 512Mi), `maxBytes` on Bucket, KVStore, SQLDatabase and Lake, 507 past it; storage
  quotas as a framework shaped like IAM; CPU and memory behind the runtime port so a driver can be swapped;
  `resources` and `pooling.worker` refused together at admission and blocked by the reconciler
- **Relates to**: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) (`ResourceSpec`) ·
  [ADR-0233](0233-sql-access-from-functions.md) (SQLDatabase `resources`) · [ADR-0044](0044-worker-pooling-threads.md) and
  [ADR-0046](0046-pooling-placement-policy.md) (pool workers) · [ADR-0018](0018-api-server-authn-rbac-admission.md)
  (per-namespace quotas, V2) · [ADR-0266](0266-volume-sizes-with-units.md) (`ByteSize`) ·
  [ADR-0279](0279-a-lake-answers-a-commit-once-its-catalog-save-covers-it.md) (a Lake's catalog saves)

## Context & Need

A Function has no CPU or memory field, and `internal/function/function.go:3387` builds its worker with no
`runtime.Limits`, so one Function can take a node's CPU and memory. Lake and SQLDatabase engines get theirs from
`spec.resources` through the provider's private `limitsFrom` (`internal/provider/runtime.go:394`), which containerd
applies as cgroup limits (`internal/runtime/containerd/containerd_linux.go:1231-1236`); `SQLDatabase.Validate` checks
those fields inline (`api/types/v1alpha1/sqldatabase.go:52-60`). A Bucket, a Lake, a KVStore and a SQLDatabase cap at
most one item or a count, never what a store holds in total. This ADR adds both limits behind ports:
storage quotas as a framework the next cap reuses (per-namespace quotas, V2, come first), CPU and memory on the
runtime port so another driver can enforce them.

## Scenarios

- **scenario: function-limits-applied**: Given a Function with `resources.cpu: "0.5"` and `resources.memory: 256Mi` on
  containerd, When its worker starts, Then its cgroup allows half a CPU and 256 MiB, and a worker past the memory is
  OOM-killed and restarted.
- **scenario: function-default-limits**: Given `functions.defaultResources` of `cpu: "1"` and `memory: 512Mi`, When a
  solo Function without `resources` starts a worker, Then the worker gets those limits.
- **scenario: invalid-resources-refused**: Given `resources.cpu: "lots"`, or `resources` together with
  `pooling.worker`, When the Function is applied, Then admission refuses it with 422 and names the field.
- **scenario: past-admission-blocked**: Given a Function written past admission, such as by a restore, with an
  unparsable `resources` or with `resources` and `pooling.worker`, When the reconciler converges it, Then no worker
  starts and the Function is `Failed` with reason `ResourcesInvalid` or `ResourcesWithPooling`.
- **scenario: bucket-total-refused**: Given a Bucket with `maxBytes: 1MiB` that holds 1 MiB, When a Function's
  `ctx.blob` put, an S3 client's put or a Site's build adds an object, Then it gets 507 (the S3 error
  `QuotaExceeded`), the object is not written, and a read or a delete still works.
- **scenario: overwrite-not-refused**: Given a Lake whose Bucket is within one catalog of its `maxBytes`, When a
  commit's catalog save overwrites `catalog.db`, Then it waits on one fresh measure and is admitted.
- **scenario: lake-total-refused**: Given a Lake with `maxBytes: 1MiB` that holds 1 MiB, When a client commits an
  `INSERT`, Then the engine's write gets 507 and the commit fails, as a refused save fails one (ADR-0279), while a query
  still works.
- **scenario: kv-total-refused**: Given a KVStore at its `maxBytes`, When a Function puts a key, Then it gets 507, and
  a get or a delete still works.
- **scenario: sql-delete-at-cap**: Given a SQLDatabase at its `maxBytes`, When its owner executes an `INSERT`, Then it
  gets 507, and so does `DELETE FROM t WHERE 0; INSERT …`; When it executes only `DELETE` or `DROP` statements, Then
  they run, and after the next measure an `INSERT` lands again.
- **scenario: no-cap-unchanged**: Given a store without `maxBytes`, When it is written, Then nothing is measured or
  refused.

## Scope

**In**: the quota framework (decision point, enforcement points, sources); the runtime port's limits contract and its
`LimitEnforcer` capability; `FunctionSpec.Resources`, its default, admission and the reconciler's block; `maxBytes`
on the four stores; one `ResourceSpec` validation and one limits mapping shared with Lake and SQLDatabase.
**Out**: per-namespace quotas (V2, F208), which register into the framework; Cedar quota policies (opt-in, the
decider's choice: a later ADR); pool workers, which keep no CPU or memory
limit and a pool-wide heap cap (`FUNCD_POOL_MAX_OLD_MB`); a store's usage in its status; funclog sizes; a Restore's
loads and the Bucket purger, which write through raw prefixes and stay uncapped.

## Constraints & Decision drivers

- One check of each kind: every storage write asks one decision point, as every PEP asks the one Cedar PDP
  (`auth.Authorizer`, `internal/auth/authorizer.go:123`); one `ResourceSpec` check and one limits mapping.
- Every limit sits behind a port with a swappable driver.
- A write path measures a store only at its first capped write and to confirm a refusal; a store without a cap pays a
  map lookup.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. A quota framework shaped like IAM; CPU and memory on the runtime port** | One check; a new cap is a new source; a new runtime is a new driver | Usage errs high between measures | **Chosen** |
| B. A size check coded in each write path | No new package | One copy per path; namespace quotas would add more | Rejected: not DRY |
| C. Exact counters updated on every write | No error margin | An extra read on every overwrite and delete; drift after a crash | Rejected |
| D. SQLite's `PRAGMA max_page_count` for SQL | Exact; deletes always work | Per connection inside the engine, unverified for rqlite | Rejected for now |

## Decision

1. **The decision point.** `internal/platform/quota` (NEW) defines `Checker`, the quota counterpart of
   `auth.Authorizer`: `Admit` before a write, `Settle` after it. `Book` implements it; `quota.None` admits everything
   and is the default of every enforcement point, as `hold.NeverCall` is for the hold. The daemon builds one `Book`.
2. **Sources.** The feature that owns a kind registers a `Source` with the `Book`: `Limit` reads the subject's cap from
   its resource (none ⇒ no cap), `Measure` reads its usage. This ADR registers Bucket (the sum of its listing's sizes),
   Lake (the same, under its location, catalog included), KVStore (its engine's NEW `Usage`) and SQLDatabase
   (`(page_count - freelist_count) * page_size` through its engine).
   The `Book` charges the subject alone; F208 adds the namespace as a parent the `Book` derives from
   `Subject.Namespace`, with no write-path change.
3. **Usage.** `Admit` reserves the write's full bytes; after the store, `Settle` turns them into settled bytes, or
   releases them when the store failed. Usage is the last measure plus the settled bytes since plus the reserved ones,
   so it never counts less than the store holds. A background refresh per subject, started at its first use and
   repeated every minute (the constant `refreshInterval`), re-reads its limit and measure off the write path; the
   first write waits on that first refresh, and a measure that lands keeps the bytes settled while it ran. A failed
   refresh keeps the last values and logs; while a subject's first refresh fails, its writes get 503.
4. **The refusal.** When a write's bytes would take usage past the limit and bytes were settled since the last
   measure, `Admit` waits on one shared fresh measure. On that usage, the size the write replaces
   (`Request.Replaces`: one stat of the key for a Bucket, the old value's length for a KVStore) is credited to the
   decision, never to the count, since a concurrent delete can falsify it. A write still past the limit gets the NEW
   `fault.InsufficientStorage` (507, `urn:funcd:problem:insufficient-storage`) naming the subject, its usage and its
   limit. An Execute, of unknown size, is refused once usage is at the limit, unless it only deletes: each statement
   text holds one statement (only comments and whitespace after its first `;`) whose first keyword is `DELETE` or
   `DROP`; anything else, a `WITH … DELETE` included, is a write. A delete trigger may still write a bounded amount.
5. **Enforcement points.** Bucket: a decorator in `s3BucketFor` beside `blob.Capped` (`pkg/funcd/funcd.go:2445`), the one
   view behind the S3 gateway, `ctx.blob` and Sites; the gateway's `mapBlobErr` answers `QuotaExceeded` with 507.
   Lake: the S3 gateway also charges the caller's own subject when its kind has a source, so a Lake engine's write,
   signed with its Lake's keypair (ADR-0270), charges its Lake and then its Bucket; the second's refusal releases the
   first's reservation. KVStore: the KV facade's Put. SQLDatabase: the SQL facade's Execute. Reads and deletes never
   ask.
6. **CPU and memory on the runtime port.** `runtime.WorkerSpec.Limits` is the contract, and enforcing it is the
   driver's job. A driver that enforces it implements the NEW capability `runtime.LimitEnforcer`, as `StopGracer`
   reports a grace (`internal/runtime/runtime.go:183`): containerd does, by cgroups; the process driver does not, and
   the Function reconciler and the provider runtime log once per Function or engine that its limits are not
   enforced. Another driver, a microVM's sizes
   or a cgroup for the process driver, plugs in by implementing it.
7. **Function limits.** `FunctionSpec.Resources` is the `ResourceSpec` of Lake and SQLDatabase. `ResourceSpec.Validate`
   (NEW) is the one check, a finite `cpu` of at least 0.01 and a `memory` in `ByteSize`'s grammar (ADR-0266), and
   SQLDatabase, Lake and Function call it. `runtime.LimitsFrom` (NEW, the provider's `limitsFrom` moved, on
   `ByteSize`) is the one mapping. `functions.defaultResources` (NEW key, `cpu: "1"`, `memory: 512Mi`) fills each field
   a solo Function leaves empty; cleared, it leaves them unlimited.
8. **Pools and the second layer.** A pool's heap cap is pool-wide, so admission refuses `resources` with
   `pooling.worker` (422). The reconciler blocks a Function written past admission, such as by a restore, before any
   placement: no worker, phase `Failed`, reason `ResourcesWithPooling` or, for a value that does not parse,
   `ResourcesInvalid`, as `VolumeInvalid` does (`internal/function/volumes.go:105`).
9. **Store caps.** `maxBytes` is a `ByteSize` on `BucketSpec`, `LakeSpec`, `KVStoreSpec` and `SQLDatabaseSpec`; unset means no cap;
   admission refuses one that does not parse with 422.

## Design model

[`docs/design/storage/quota-usage.fizz`](../design/storage/quota-usage.fizz) ([diagram](../design/storage/quota-usage.md)):
one capped subject, two writes, a delete and the background measure. It asserts `UsageNeverBelowTotal` and
`RefusalsRestOnAFreshMeasure`; its mutants show each guard matters: a landing measure that drops the bytes settled while
it ran, a refusal on a stale measure, and counting a credit, which a concurrent delete falsifies (the model found it).
A Lake engine's write keeps the protocol on each of its two subjects. Admission and the reconciler agree by
construction: both call `ResourceSpec.Validate`.

## Temporary workarounds

None.

## Contracts

```go
// Package quota (internal/platform/quota), NEW.
type Subject struct {
	Kind      v1.Kind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}
type Request struct {
	Subject  Subject
	Bytes    int64                                     // the write's size; 0 when unknown
	Replaces func(ctx context.Context) (int64, error) // the size it overwrites, asked only before a refusal; nil: 0
	Deletes  bool                                      // a SQL Execute that only deletes
}
// Checker is the quota counterpart of auth.Authorizer.
type Checker interface {
	Admit(ctx context.Context, r Request) error // reserves r.Bytes, or fault.InsufficientStorage past the limit
	Settle(r Request, stored bool)              // the reservation becomes settled bytes, or is released
}
var None Checker // admits everything
type Source struct {
	Limit   func(ctx context.Context, s Subject) (int64, bool, error)
	Measure func(ctx context.Context, s Subject) (int64, error)
}
type Book struct{ /* sources by kind; per-subject lock, limit, measure, bytes since */ }
func NewBook(clk clock.Clock) *Book
func (b *Book) Register(kind v1.Kind, src Source)

// Package runtime (internal/runtime), NEW: the capability of a driver that enforces WorkerSpec.Limits.
type LimitEnforcer interface{ EnforcesLimits() bool }
func LimitsFrom(cpu, memory string) Limits // the provider's limitsFrom, moved

// Package kvstore (internal/kvstore), NEW method on the KV port, by both engines and in the contract suite.
Usage(ctx context.Context, prefix string) (int64, error)

// Package v1alpha1 (api/types/v1alpha1), NEW.
func (r ResourceSpec) Validate(op, path string) error
// FunctionSpec.Resources ResourceSpec; BucketSpec, LakeSpec, KVStoreSpec and SQLDatabaseSpec .MaxBytes ByteSize

// Package fault (api/fault), NEW kind (507).
const InsufficientStorage Kind = "insufficient_storage"
func InsufficientStoragef(op, format string, a ...any) *Error
```

| Consumes | Exposes |
|---|---|
| `functions.defaultResources` (NEW); each store's `maxBytes`; the engines' measures | A worker's limits by its driver; 507 on a capped write; 422 at admission; `ResourcesInvalid`, `ResourcesWithPooling` |

## Implementation plan

- `internal/platform/quota` with a fake clock in its tests (refresh, single flight, refusal after a fresh measure,
  the credit, a failed refresh, `Deletes`, a kind without a source); they carry `oracle:` markers for the spec's two
  assertions (ADR-0272).
- `api/types/v1alpha1`: the fields, `ResourceSpec.Validate` (SQLDatabase's inline checks go; the `ResourceSpec` doc
  comment, `lake.go:84-86`, is rewritten); `api/fault`: the kind, its problem and builder; regenerate the OpenAPI spec
  and the SDK types.
- `internal/runtime`: `LimitEnforcer`, `LimitsFrom` (the provider calls it); containerd implements the capability; a
  `runtimecontract` case for any `LimitEnforcer`.
- `internal/platform/config`: `Functions.DefaultResources`; `internal/function`: limits, the default, admission and
  the reconciler's two reasons.
- The four sources and enforcement points, the gateway's caller subject included; `Usage` on the KV port and both engines; `pkg/funcd` builds the `Book`.
- Tests, one per scenario: `TestScenarioFunctionLimitsApplied` (the containerd spec carries the limits),
  `…FunctionDefaultLimits`, `…InvalidResourcesRefused`, `…PastAdmissionBlocked`, `…BucketTotalRefused` (`ctx.blob`,
  the gateway and a Site), `…LakeTotalRefused`, `…OverwriteNotRefused`, `…KVTotalRefused`, `…SQLDeleteAtCap`, `…NoCapUnchanged`; the OOM
  restart runs in the Linux tests (`just test-linux`).
- Docs: the Function, Bucket, Lake, KVStore and SQLDatabase references, `funcdconfig.yaml`'s reference, the changelog's notes.
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean;
  `scripts/agent/gate.sh` passes; `just check-hygiene check-specs` passes.

## Review checklist

- [ ] No size check outside `Book`, no `ResourceSpec` check outside `Validate`, no limits mapping outside
      `LimitsFrom`.
- [ ] Each enforcement point asks `Admit` before it stores and `Settle` after; reads and deletes never ask; the
      default is `quota.None`; a credit never lowers the count.
- [ ] A subject without a cap is never measured; a refusal follows a fresh measure when bytes were recorded since.
- [ ] Only a `LimitEnforcer` driver is trusted to enforce; the process driver logs once per Function.
- [ ] Admission refuses a bad `cpu`, `memory` or `maxBytes`, and `resources` with `pooling.worker`; the reconciler blocks
      either one written past admission.
- [ ] Every scenario has its named test.

## Consequences

- **Positive**: a solo Function can no longer take a node's CPU or memory, and a store can no longer grow without end;
  the next quota is a new source, the next runtime a new `LimitEnforcer`.
- **Negative (accepted)**: existing solo Functions get the default 1 CPU and 512 MiB at upgrade (the changelog says so);
  pool workers stay unlimited; a Lake whose `resources` was ignored until now gets 422 at its next apply if it does
  not parse; near its cap, a store whose writes overwrite is
  re-measured before each such write (one at a time); a write can be refused up to a minute after deletes made room, and a SQLDatabase can grow up to a minute
  of writes past its cap; a Restore can load a store past its cap, after which only deletes run until it is under;
  `funcdctl dev` enforces no CPU or memory limit.

## Open questions

- Cedar quota policies (opt-in, the decider's choice): a later ADR has `Book.Admit` ask the PDP after its own check,
  with `usage`, `bytes` and `limit` in the context, a deny by a `@quota` policy answering 507.
- A pool's per-Function heap cap (a TypeScript shim change) and a store's usage in its status: later ADRs.

## References

- The decider's refinement of 2026-10-27; FEAT-0016; its features #1313 (F206) and #1314 (F207), tracker #1315.
- `internal/auth/authorizer.go:123`, `internal/runtime/runtime.go:183`, `internal/provider/runtime.go:394`,
  `api/types/v1alpha1/sqldatabase.go:52-60`, `pkg/funcd/funcd.go:2422-2445` (the shared Bucket view).
