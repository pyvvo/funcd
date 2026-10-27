# ADR-0291: Functions get CPU and memory limits, and stores get a total size

- **Status**: Proposed (2026-10-27)
- **Date**: 2026-10-27
- **Deciders**: green-0-rabbit
- **Tags**: runtime, storage, limits
- **Realizes**: [FEAT-0016/F206](../feat/0016-feat-workload-limits.md) (Function CPU and memory limits) and F207 (a
  store's total size)
- **Answers**: the decider's refinement of 2026-10-27: `spec.resources` on a Function, a node default
  (`functions.defaultResources`, 1 CPU and 512Mi), `maxBytes` on Bucket, KVStore and SQLDatabase, 507 past it
- **Relates to**: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) (`ResourceSpec`) ·
  [ADR-0233](0233-sql-access-from-functions.md) (SQLDatabase `resources`) · [ADR-0044](0044-worker-pooling-threads.md) and
  [ADR-0046](0046-pooling-placement-policy.md) (pool workers) · [ADR-0018](0018-api-server-authn-rbac-admission.md)
  (per-namespace quotas, V2) · [ADR-0262](0262-function-owned-volumes.md) (volume sizes)

## Context & Need

A Function has no CPU or memory field, and `internal/function/function.go:3387` builds its worker with no
`runtime.Limits`, so one Function can take a node's CPU and memory. Lake and SQLDatabase engines already get theirs
from `spec.resources` (`internal/provider/runtime.go:394`), which containerd applies as cgroup limits
(`internal/runtime/containerd/containerd_linux.go:1231-1236`). A Bucket, a KVStore and a SQLDatabase cap one object,
key, value or database count, but nothing caps what a store holds in total. This ADR adds both limits; summing them
per namespace stays V2 (ADR-0018).

## Scenarios

- **scenario: function-limits-applied**: Given a Function with `resources.cpu: "0.5"` and `resources.memory: 256Mi` on
  containerd, When its worker starts, Then its cgroup allows half a CPU and 256 MiB, and a worker past the memory is
  OOM-killed and restarted.
- **scenario: function-default-limits**: Given `functions.defaultResources` of `cpu: "1"` and `memory: 512Mi`, When a
  Function without `resources` starts a worker, Then the worker gets those limits.
- **scenario: invalid-resources-refused**: Given `resources.cpu: "lots"`, or `resources` together with
  `pooling.worker`, When the Function is applied, Then admission refuses it with 422 and names the field.
- **scenario: bucket-total-refused**: Given a Bucket with `maxBytes: 1MiB` that holds 1 MiB, When a Function's
  `ctx.blob` put or an S3 client's put adds an object, Then it gets 507 (the S3 error `QuotaExceeded`), the object is
  not written, and a read or a delete still works.
- **scenario: kv-total-refused**: Given a KVStore at its `maxBytes`, When a Function puts a key, Then it gets 507, and
  a get or a delete still works.
- **scenario: sql-total-refused**: Given a SQLDatabase at its `maxBytes`, When its owner calls Execute, Then it gets
  507, and Query still works.
- **scenario: space-freed-writes-again**: Given a store refused at its `maxBytes`, When deletes free space and the next
  measure runs, Then a write lands.
- **scenario: no-cap-unchanged**: Given a store without `maxBytes`, When it is written, Then nothing is measured or
  refused.

## Scope

**In**: `FunctionSpec.Resources` and its default, admission of both, the worker's limits; `maxBytes` on the three
stores, their usage measure, the refusal on each write path a workload uses.
**Out**: per-namespace quotas (V2, F208); a pool's per-Function heap cap (a shim change; a pool keeps its
`FUNCD_POOL_MAX_OLD_MB`); enforcement under the process runtime, which has no cgroup; a usage field in a store's
status; funclog and Lake sizes beyond the Bucket that holds a Lake's files.

## Constraints & Decision drivers

- A limit must hold under load, so it is the kernel's (cgroups) for CPU and memory, and a check before the write for
  storage.
- A write path must not list or scan a store, so usage is measured in the background and kept in memory.
- A store without a cap pays nothing.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. Function `resources` + node default; `maxBytes` per store, measured in the background** | Reuses `ResourceSpec` and the containerd limits; no scan on a write | Usage errs high until the next measure | **Chosen** (the decider, 2026-10-27) |
| B. Exact usage counters updated on every write | No error margin | An extra read on every overwrite and delete, and drift on a crash | Rejected |
| C. Per-namespace quotas now | One knob per tenant | Admission over every kind (ADR-0018 V2); bigger than the need | Deferred (F208) |

## Decision

1. **Function limits.** `FunctionSpec.Resources` is the `ResourceSpec` that Lake and SQLDatabase use: `cpu` is a
   positive decimal number of CPUs, `memory` a byte size. The Function reconciler fills the worker's
   `runtime.Limits` from it the way `limitsFrom` does (`internal/provider/runtime.go:394`). A field set to `"0"` means
   no limit. The process runtime has no cgroup and ignores the limits.
2. **The default.** `functions.defaultResources` in `funcdconfig.yaml` (NEW key), `cpu: "1"` and `memory: 512Mi` by
   default, fills each field a Function leaves empty.
3. **Pools.** Admission refuses a Function that sets both `resources` and `pooling.worker`; a pooled Function keeps
   its pool's per-thread heap cap, and the default does not apply to it.
4. **Admission.** A `cpu` that does not parse as a number at least 0, or a `memory` that is not a byte size, is a 422
   naming the field; so is a `maxBytes` that is not a byte size.
5. **Store totals.** `maxBytes` (a `ByteSize`, `"10GiB"` or a byte count) on `BucketSpec`, `KVStoreSpec` and
   `SQLDatabaseSpec`; unset means no cap.
6. **Usage.** A capped store's usage is its last measure plus the bytes written through funcd since; deletes and
   overwrites lower it only at the next measure, so usage errs high. A measure runs every minute (the constant
   `usageInterval`) and at the first write: a Bucket sums the sizes its listing returns, a KVStore asks its engine's
   NEW `Usage`, a SQLDatabase runs `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`
   through its engine.
7. **The refusal.** A write whose size would take usage past `maxBytes` (an Execute: usage already at it) is refused
   before anything is stored, with the NEW `fault.InsufficientStorage` (507,
   `urn:funcd:problem:insufficient-storage`) naming the store, its usage and its cap. The writes checked are the blob
   facade's Put, the S3 gateway's PutObject and CompleteMultipartUpload (the S3 error `QuotaExceeded` with 507), the
   KV facade's Put and the SQL facade's Execute. Reads and deletes are never refused.

## Design model

None: each write compares two numbers; the measure is a periodic read with no protocol of its own.

## Temporary workarounds

None.

## Contracts

```go
// Package v1alpha1 (api/types/v1alpha1), NEW fields; the other fields are unchanged.
type FunctionSpec struct {
	// Resources bounds each solo worker (ADR-0291); empty fields take functions.defaultResources.
	Resources ResourceSpec `json:"resources,omitempty"`
}
type BucketSpec struct{ MaxBytes ByteSize `json:"maxBytes,omitempty"` }
type KVStoreSpec struct{ MaxBytes ByteSize `json:"maxBytes,omitempty"` }
type SQLDatabaseSpec struct{ MaxBytes ByteSize `json:"maxBytes,omitempty"` }

// Package fault (api/fault), NEW kind: a store is at its total size (507).
const InsufficientStorage Kind = "insufficient_storage"
func InsufficientStoragef(op, format string, a ...any) *Error

// Package usage (internal/platform/usage), NEW: one capped store's usage against its cap.
type Measure func(ctx context.Context) (int64, error)
type Cap struct{ /* limit, last measure, bytes since, measured at */ }
func NewCap(limit int64, measure Measure, clk clock.Clock) *Cap
// Admit refuses n more bytes with fault.InsufficientStorage when usage plus n passes the limit; it measures first when
// the last measure is older than usageInterval.
func (c *Cap) Admit(ctx context.Context, n int64) error
// Wrote adds n bytes written to usage.
func (c *Cap) Wrote(n int64)

// Package kvstorecontract (internal/kvstore/kvstorecontract), NEW method on the engine port, by both engines.
Usage(ctx context.Context, prefix string) (int64, error) // the bytes of every key and value under prefix
```

| Consumes | Exposes |
|---|---|
| `functions.defaultResources` (NEW); each store's `maxBytes`; the engines' measures | A worker's cgroup limits; 507 on a capped write; 422 at admission |

## Implementation plan

- `api/types/v1alpha1`: the four fields and their validation; `api/fault`: the kind, its problem entry and builder;
  regenerate the OpenAPI spec and the SDK types.
- `internal/platform/config`: `Functions.DefaultResources` with its default and validation.
- `internal/function`: limits on the containerd and process `WorkerSpec`s from the spec or the default; admission of
  `resources` with `pooling.worker`.
- `internal/platform/usage`: `Cap`, with a fake clock in its tests.
- The four write paths above, each holding one `Cap` per capped store, created and dropped as the store's `maxBytes`
  is set, changed or removed; `Usage` on the memory and Badger KV engines and in the contract suite.
- Tests, one per scenario: `TestScenarioFunctionLimitsApplied` (the containerd spec carries the limits),
  `…FunctionDefaultLimits`, `…InvalidResourcesRefused`, `…BucketTotalRefused` (the blob facade and the S3 gateway),
  `…KVTotalRefused`, `…SQLTotalRefused`, `…SpaceFreedWritesAgain`, `…NoCapUnchanged`; the OOM restart of
  function-limits-applied runs in the Linux tests (`just test-linux`).
- Docs: the Function, Bucket, KVStore and SQLDatabase references, `funcdconfig.yaml`'s reference, the changelog's
  note that existing Functions get the default limits.
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean;
  `scripts/agent/gate.sh` passes; `just check-hygiene check-specs` passes.

## Review checklist

- [ ] A solo worker's `runtime.Limits` come from `resources`, else the default; `"0"` means none; process mode ignores
      them.
- [ ] Admission refuses a bad `cpu`, `memory` or `maxBytes`, and `resources` with `pooling.worker`, with 422.
- [ ] Each of the five write paths admits before it stores and records what it wrote; reads and deletes never ask.
- [ ] A store without `maxBytes` has no `Cap` and is never measured.
- [ ] `fault.InsufficientStorage` maps to 507; the S3 gateway answers `QuotaExceeded` with 507.
- [ ] Every scenario has its named test.

## Consequences

- **Positive**: one Function can no longer take a node's CPU or memory, and one store can no longer grow without end;
  an owner sizes both in the spec.
- **Negative (accepted)**: existing Functions get the default 1 CPU and 512 MiB at upgrade, and one that needs more must
  set `resources` (the changelog says so); usage errs high until the next measure, so a write can be refused up to a
  minute after deletes made room; a capped Bucket is listed once a minute; `funcdctl dev` enforces no CPU or memory
  limit.

## Open questions

- A store's usage in its status, for `funcdctl get`: a later ADR, if owners ask for it.
- A pool's per-Function heap cap from `resources.memory`: needs a TypeScript shim change; its own ADR.

## References

- The decider's refinement of 2026-10-27; FEAT-0016.
- `internal/provider/runtime.go:394` (`limitsFrom`), `internal/runtime/containerd/containerd_linux.go:1231-1236`,
  `internal/pooling/pooling.go:124` (opt-in pools).
