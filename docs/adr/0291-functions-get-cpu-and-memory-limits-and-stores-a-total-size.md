# ADR-0291: Functions get CPU and memory limits, and stores get a total size

- **Status**: Proposed (2026-10-27)
- **Date**: 2026-10-27
- **Deciders**: green-0-rabbit
- **Tags**: runtime, storage, limits, quota
- **Realizes**: [FEAT-0016/F206](../feat/0016-feat-workload-limits.md) (Function CPU and memory limits) and F207 (a
  store's total size)
- **Answers**: the decider's refinement of 2026-10-27: `spec.resources` on a Function, a node default
  (`functions.defaultResources`, 1 CPU and 512Mi), `maxBytes` on Bucket, KVStore and SQLDatabase, 507 past it; and
  the decider's ask that quotas be a framework, as IAM is
- **Relates to**: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) (`ResourceSpec`) ·
  [ADR-0233](0233-sql-access-from-functions.md) (SQLDatabase `resources`) · [ADR-0044](0044-worker-pooling-threads.md) and
  [ADR-0046](0046-pooling-placement-policy.md) (pool workers) · [ADR-0018](0018-api-server-authn-rbac-admission.md)
  (per-namespace quotas, V2) · [ADR-0262](0262-function-owned-volumes.md) (volume sizes)

## Context & Need

A Function has no CPU or memory field, and `internal/function/function.go:3387` builds its worker with no
`runtime.Limits`, so one Function can take a node's CPU and memory. Lake and SQLDatabase engines get theirs from
`spec.resources` through the provider's private `limitsFrom` (`internal/provider/runtime.go:394`), which containerd
applies as cgroup limits (`internal/runtime/containerd/containerd_linux.go:1231-1236`); `SQLDatabase.Validate` checks
those fields inline (`api/types/v1alpha1/sqldatabase.go:52-60`). A Bucket, a KVStore and a SQLDatabase cap one
object, key, value or database count, but nothing caps what a store holds in total. This ADR adds both limits, and
makes storage quotas a framework the next cap reuses: per-namespace quotas (V2, ADR-0018) are the first in line.

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

**In**: the quota framework (decision point, enforcement points, sources); `FunctionSpec.Resources`, its default, its
admission and the worker's limits; `maxBytes` on the three stores; one `ResourceSpec` validation and one limits
mapping shared with Lake and SQLDatabase.
**Out**: per-namespace quotas (V2, F208), which register into the framework; a pool's per-Function heap cap (a shim
change; a pool keeps `FUNCD_POOL_MAX_OLD_MB`); enforcement under the process runtime, which has no cgroup; a usage
field in a store's status; funclog sizes.

## Constraints & Decision drivers

- One check, written once: every write path that stores workload data asks the same decision point, as every PEP asks
  the one Cedar PDP (`auth.Authorizer`, `internal/auth/authorizer.go:123`).
- A new cap adds a source, not a check in each write path.
- A write path never lists or scans a store; a store without a cap pays a map lookup.
- CPU and memory are the kernel's to enforce (cgroups); funcd parses and validates them in one place each.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. A quota framework shaped like IAM: one decision point, enforcement points, sources per kind** | One check; the next cap is a new source; usage measured off the write path | Usage errs high until the next measure | **Chosen** |
| B. A size check coded in each of the five write paths | No new package | Five copies; namespace quotas would add five more | Rejected: not DRY |
| C. Exact counters updated on every write | No error margin | An extra read on every overwrite and delete; drift after a crash | Rejected |
| D. Per-namespace quotas now | One knob per tenant | Admission over every kind (ADR-0018 V2); bigger than the need | Deferred (F208), on this framework |

## Decision

1. **The decision point.** `internal/platform/quota` (NEW) defines `Checker`, the quota counterpart of
   `auth.Authorizer`: `Admit` before a write, `Record` after it. `Book` implements it; `quota.None` admits everything
   and is the default of every enforcement point without a `Checker`, as `hold.NeverCall` is for the hold. The daemon
   builds one `Book` and passes it to each enforcement point.
2. **Sources.** The feature that owns a kind registers a `Source` with the `Book`: `Limit` reads the subject's cap from
   its resource (none ⇒ no cap) and `Measure` reads its usage. This ADR registers Bucket, KVStore and SQLDatabase. A
   request charges its subject and every registered subject above it, in the order store, namespace, node; with only
   the store kinds registered, that is the store alone. A later cap registers its kind and changes no write path.
3. **Usage.** A capped subject's usage is its last measure plus the bytes recorded since; deletes and overwrites lower
   it only at the next measure, so usage errs high. A subject's limit and usage refresh at first use and when older
   than one minute (the constant `refreshInterval`). Bucket measures sum its listing's sizes; KVStore measures ask its
   engine's NEW `Usage`; SQLDatabase measures run `SELECT page_count * page_size FROM pragma_page_count(),
   pragma_page_size()` through its engine.
4. **The refusal.** `Admit` refuses a write whose bytes would take a subject past its limit (an Execute, of unknown
   size, once usage is at it) with the NEW `fault.InsufficientStorage` (507, `urn:funcd:problem:insufficient-storage`)
   naming the subject, its usage and its limit. The S3 gateway turns it into the S3 error `QuotaExceeded` with 507.
5. **Enforcement points.** The blob facade's Put, the S3 gateway's PutObject and CompleteMultipartUpload, the KV
   facade's Put and the SQL facade's Execute. Reads and deletes never ask.
6. **Function limits.** `FunctionSpec.Resources` is the `ResourceSpec` of Lake and SQLDatabase. `ResourceSpec.Validate`
   (NEW) holds the one check, a positive decimal `cpu` and a `memory` size, which SQLDatabase's inline checks become
   and Function and Lake call too. `runtime.LimitsFrom` (NEW, the provider's `limitsFrom` moved) maps it to
   `runtime.Limits` for the provider runtime and the Function reconciler alike. The process runtime has no cgroup and
   ignores the limits.
7. **The default.** `functions.defaultResources` in `funcdconfig.yaml` (NEW key), `cpu: "1"` and `memory: 512Mi` by
   default, fills each field a Function leaves empty; an operator who clears it leaves such Functions unlimited.
8. **Pools.** Admission refuses a Function that sets both `resources` and `pooling.worker`; a pooled Function keeps its
   pool's per-thread heap cap, and the default does not apply to it.
9. **Store caps.** `maxBytes` is a `ByteSize` (`"10GiB"` or a byte count, ADR-0266's parser) on `BucketSpec`,
   `KVStoreSpec` and `SQLDatabaseSpec`; unset means no cap; admission refuses one that does not parse with 422.

## Design model

None: `Admit` compares numbers under one lock per subject; a refresh is a read with no protocol of its own.

## Temporary workarounds

None.

## Contracts

```go
// Package quota (internal/platform/quota), NEW: the decision point every storage enforcement point asks (ADR-0291).
type Subject struct {
	Kind      v1.Kind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}
type Request struct {
	Subject Subject
	Bytes   int64 // the write's size; 0 when unknown (a SQL Execute)
}
// Checker is the quota counterpart of auth.Authorizer.
type Checker interface {
	// Admit refuses the write with fault.InsufficientStorage when it would take a capped subject past its limit.
	Admit(ctx context.Context, r Request) error
	// Record adds a stored write to its subject and the subjects above it.
	Record(r Request)
}
var None Checker // admits everything
// Source is what the feature owning a kind registers: its subject's cap (false: none) and its usage.
type Source struct {
	Limit   func(ctx context.Context, s Subject) (int64, bool, error)
	Measure func(ctx context.Context, s Subject) (int64, error)
}
type Book struct{ /* sources by kind; per-subject limit, measure, bytes since, refreshed at */ }
func NewBook(clk clock.Clock) *Book
func (b *Book) Register(kind v1.Kind, src Source)
func (b *Book) Admit(ctx context.Context, r Request) error
func (b *Book) Record(r Request)

// Package v1alpha1 (api/types/v1alpha1), NEW; the other fields are unchanged.
func (r ResourceSpec) Validate(op, path string) error
type FunctionSpec struct{ Resources ResourceSpec `json:"resources,omitempty"` }
type BucketSpec struct{ MaxBytes ByteSize `json:"maxBytes,omitempty"` }
type KVStoreSpec struct{ MaxBytes ByteSize `json:"maxBytes,omitempty"` }
type SQLDatabaseSpec struct{ MaxBytes ByteSize `json:"maxBytes,omitempty"` }

// Package runtime (internal/runtime), NEW home of internal/provider's limitsFrom.
func LimitsFrom(cpu, memory string) Limits

// Package fault (api/fault), NEW kind: a capped subject is full (507).
const InsufficientStorage Kind = "insufficient_storage"
func InsufficientStoragef(op, format string, a ...any) *Error

// Package kvstorecontract (internal/kvstore/kvstorecontract), NEW engine method, by both engines.
Usage(ctx context.Context, prefix string) (int64, error) // the bytes of every key and value under prefix
```

| Consumes | Exposes |
|---|---|
| `functions.defaultResources` (NEW); each store's `maxBytes`; the engines' measures | A worker's cgroup limits; 507 on a capped write; 422 at admission |

## Implementation plan

- `internal/platform/quota`: `Checker`, `None`, `Source`, `Book`, with a fake clock in its tests (the chain, refresh,
  the error, a kind without a source).
- `api/types/v1alpha1`: the four fields, `ResourceSpec.Validate` (SQLDatabase and Lake call it; SQLDatabase's inline
  checks go), `maxBytes` validation; `api/fault`: the kind, its problem entry and builder; regenerate the OpenAPI spec
  and the SDK types.
- `internal/runtime`: `LimitsFrom`; `internal/provider` calls it and drops `limitsFrom`.
- `internal/platform/config`: `Functions.DefaultResources`, its default and validation.
- `internal/function`: limits on the worker from the spec or the default; admission of `resources` with
  `pooling.worker`.
- The three sources, registered by their features; `Usage` on the memory and Badger KV engines and in the contract
  suite; each enforcement point gets the `Checker` through its deps (default `quota.None`); `pkg/funcd` builds the
  `Book`.
- Tests, one per scenario: `TestScenarioFunctionLimitsApplied` (the containerd spec carries the limits),
  `…FunctionDefaultLimits`, `…InvalidResourcesRefused`, `…BucketTotalRefused` (the blob facade and the S3 gateway),
  `…KVTotalRefused`, `…SQLTotalRefused`, `…SpaceFreedWritesAgain`, `…NoCapUnchanged`; the OOM restart runs in the
  Linux tests (`just test-linux`).
- Docs: the Function, Bucket, KVStore and SQLDatabase references, `funcdconfig.yaml`'s reference, and the changelog's
  note that existing Functions get the default limits.
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean;
  `scripts/agent/gate.sh` passes; `just check-hygiene check-specs` passes.

## Review checklist

- [ ] Every enforcement point asks `Admit` before it stores and calls `Record` after; reads and deletes never ask; a
      point without a `Checker` uses `quota.None`.
- [ ] No size check exists outside `Book`; no `ResourceSpec` check outside `ResourceSpec.Validate`; no limits mapping
      outside `runtime.LimitsFrom`.
- [ ] A subject without a cap is never measured; usage refreshes at first use and after `refreshInterval`.
- [ ] A worker's limits come from `resources`, else the default; process mode ignores them; admission refuses a bad
      `cpu`, `memory` or `maxBytes`, and `resources` with `pooling.worker`, with 422.
- [ ] `fault.InsufficientStorage` maps to 507; the S3 gateway answers `QuotaExceeded` with 507.
- [ ] Every scenario has its named test.

## Consequences

- **Positive**: one Function can no longer take a node's CPU or memory, and one store can no longer grow without end;
  the next quota, per namespace first, is a new source on the same framework.
- **Negative (accepted)**: existing Functions get the default 1 CPU and 512 MiB at upgrade, and one that needs more
  must set `resources` (the changelog says so); usage errs high until the next refresh, so a write can be refused up
  to a minute after deletes made room; a capped Bucket is listed once a minute; `funcdctl dev` enforces no CPU or
  memory limit.

## Open questions

- A store's usage in its status, for `funcdctl get`: a later ADR, if owners ask for it.
- A pool's per-Function heap cap from `resources.memory`: needs a TypeScript shim change; its own ADR.

## References

- The decider's refinement of 2026-10-27; FEAT-0016.
- `internal/auth/authorizer.go:123` (the decision point this framework mirrors), `internal/provider/runtime.go:394`,
  `api/types/v1alpha1/sqldatabase.go:52-60`, `internal/pooling/pooling.go:124` (opt-in pools).
