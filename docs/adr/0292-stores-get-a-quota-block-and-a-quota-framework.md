# ADR-0292: Stores get a quota block, enforced by one quota framework

- **Status**: Proposed (2026-10-27)
- **Date**: 2026-10-27
- **Deciders**: green-0-rabbit
- **Tags**: storage, limits, quota
- **Realizes**: [FEAT-0016/F207](../feat/0016-feat-workload-limits.md) (a store's total size)
- **Answers**: the decider's refinement of 2026-10-27: a `quota` block (`maxSize`, or `maxBytes`) on Bucket, each
  Bucket prefix (a Lake's cap), KVStore and SQLDatabase, holding the per-item caps too, 507 past it; one quota
  framework shaped like IAM; Cedar quota policies opt-in, later; #1314
- **Supersedes (in part)**: [ADR-0073](0073-kv-bindings-and-subdomains.md) and
  [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md): where the per-item caps live (`spec.maxKeyBytes`,
  `spec.maxValueBytes`, `spec.maxObjectBytes` move into `spec.quota`); their 413 (ADR-0148) is unchanged
- **Relates to**: [ADR-0291](0291-a-functions-workers-get-cpu-and-memory-limits.md) (F206, split from this ADR) ·
  [ADR-0251](0251-catalogservice-declares-its-stores.md) (inline stores) · [ADR-0266](0266-volume-sizes-with-units.md)
  (`ByteSize`) · [ADR-0275](0275-a-bucket-prefix-owner-names-its-kind.md) (a Lake's prefix) ·
  [ADR-0293](0293-a-lakes-catalog-lives-in-a-platform-catalog-store.md) (a Lake's catalog, outside its prefix) ·
  [ADR-0018](0018-api-server-authn-rbac-admission.md) (V2 quotas)

## Context & Need

A Bucket, a KVStore and a SQLDatabase cap one object, key, value or database count (ADR-0073, ADR-0080, ADR-0148),
but nothing caps what a store holds in total, so one store can grow until it crowds out every other workload. A
Lake's files live under a Bucket prefix its owner declares (ADR-0275). This ADR gives every store one `quota` block
and enforces it through one decision point that every storage write asks, as every PEP asks the one Cedar PDP, so the
next quota (per namespace, V2) is a new source on the same framework.

## Scenarios

- **scenario: bucket-total-refused**: Given a Bucket with `quota.maxSize: 1MiB` that holds 1 MiB, When a Function's
  `ctx.blob` put, an S3 client's put or a Site's build adds an object, Then it gets 507 (the S3 error
  `QuotaExceeded`), the object is not written, and a read or a delete still works.
- **scenario: lake-total-refused**: Given a Lake whose prefix has `quota.maxSize: 1MiB` and holds 1 MiB, When a client
  commits an `INSERT`, Then the engine's Parquet write gets 507 and the commit fails, while a query still works: the
  catalog saves outside the prefix (ADR-0293).
- **scenario: overwrite-not-refused**: Given a Bucket within 1 KiB of its quota that holds a 4 KiB object, When an S3
  client overwrites it with 4 KiB, Then the put waits on one fresh measure and is admitted.
- **scenario: kv-total-refused**: Given a KVStore at its quota, When a Function puts a key, Then it gets 507, and a get
  or a delete still works.
- **scenario: sql-delete-at-cap**: Given a SQLDatabase at its quota, When its owner executes an `INSERT`, Then it gets
  507, and so does `DELETE FROM t WHERE 0; INSERT …`; When it executes only `DELETE` or `DROP` statements, Then they
  run, and after the next measure an `INSERT` lands again.
- **scenario: no-cap-unchanged**: Given a store whose quota sets no size, When it is written, Then nothing is measured.
- **scenario: per-item-cap-moved**: Given a Bucket with `quota.maxObjectBytes: 1024`, When an object of 2 KiB is put,
  Then it gets 413 as today (ADR-0148); a manifest with `spec.maxObjectBytes` at the root is refused with 422.

## Scope

**In**: the quota framework (decision point, enforcement points, sources); the `quota` block on Bucket, Bucket prefix,
KVStore and SQLDatabase, with the per-item caps moved into it.
**Out**: Function CPU and memory (ADR-0291); per-namespace budgets (V2, F208), keyed by what is consumed (`compute`,
`volumes` and each store kind), which register into the framework; Cedar quota policies (opt-in, a later ADR); a
store's usage in its status; funclog sizes; a Restore's loads and the Bucket purger, which write through raw prefixes
and stay uncapped.

## Constraints & Decision drivers

- One check: every storage write asks one decision point (`auth.Authorizer` is the precedent,
  `internal/auth/authorizer.go:123`); a new cap is a new source, never a new check in a write path.
- A write path measures a store only at its first capped write and to confirm a refusal; an uncapped store pays a map
  lookup.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. One quota framework shaped like IAM, one `quota` block per store** | One check; a new cap is a new source | Usage errs high between measures | **Chosen** |
| B. A size check coded in each write path | No new package | One copy per path; namespace quotas would add more | Rejected: not DRY |
| C. Exact counters updated on every write | No error margin | An extra read on every overwrite and delete; drift after a crash | Rejected |
| D. SQLite's `PRAGMA max_page_count` for SQL | Exact; deletes always work | Per connection inside the engine, unverified for rqlite | Rejected for now |
| E. A size field on the Lake itself | Close to the Lake | Repeats its prefix's cap; a second enforcement path | Rejected: the prefix caps it once |

## Decision

1. **The decision point.** `internal/platform/quota` (NEW) defines `Checker`, the quota counterpart of
   `auth.Authorizer`: `Admit` before a write, `Settle` after it. `Book` implements it; `quota.None` admits everything
   and is the default of every enforcement point, as `hold.NeverCall` is for the hold. The daemon builds one `Book`.
2. **Sources.** The feature that owns a kind registers a `Source`: `Limit` reads the subject's cap from its quota (no
   size ⇒ no cap), `Measure` reads its usage: a Bucket, the sum of its listing's sizes; a prefix, the same under it
   (for a Lake, its files; its catalog is outside, ADR-0293); a KVStore, its engine's NEW `Usage`; a SQLDatabase, `(page_count -
   freelist_count) * page_size` through its engine. F208 adds the namespace as a parent the `Book` derives from
   `Subject.Namespace`, with no write-path change.
3. **Usage.** `Admit` reserves the write's full bytes; after the store, `Settle` turns them into settled bytes, or
   releases them when the store failed. Usage is the last measure plus the settled and the reserved bytes, so for a
   write of known size it never counts less than the store holds. A background refresh per subject, started at its
   first use and repeated every minute (the constant `refreshInterval`), re-reads its limit and measure off the write
   path; the first write waits on it, and a measure that lands keeps the bytes settled while it ran. A failed refresh
   keeps the last values and logs; while a subject's first refresh fails, its writes get 503.
4. **The refusal.** When a write would take usage past the limit and bytes were settled since the last measure,
   `Admit` waits on one shared fresh measure. On that usage, the size the write replaces (`Request.Replaces`: one stat
   of the key for a Bucket, the old value's length for a KVStore) is credited to the decision, never to the count,
   since a concurrent delete can falsify it. A write still past the limit gets the NEW `fault.InsufficientStorage`
   (507, `urn:funcd:problem:insufficient-storage`) naming the subject, its usage and its limit. An Execute, of unknown
   size, is refused once usage is at the limit, unless it only deletes: each statement text holds one statement (only
   comments and whitespace after its first `;`) whose first keyword is `DELETE` or `DROP`; anything else, a `WITH …
   DELETE` included, is a write. A delete trigger may still write a bounded amount.
5. **Enforcement points.** Bucket: a decorator in `s3BucketFor` inside `blob.Capped` (`pkg/funcd/funcd.go:2446`), so
   a per-item 413 comes before any reservation, the one view behind the S3 gateway, `ctx.blob` and Sites; it charges the key's prefix when that prefix has a cap, then
   the Bucket, and a refusal by the Bucket releases the prefix's reservation; the gateway's `mapBlobErr` answers
   `QuotaExceeded` with 507. KVStore: the KV facade's Put. SQLDatabase: the SQL facade's Execute. Reads and deletes
   never ask.
6. **The quota block.** Every cap of a store sits in its `spec.quota` (NEW), under the component it caps: `BucketSpec`,
   `BucketPrefix`, `KVStoreSpec` and `SQLDatabaseSpec`. Its size is `maxSize` (a `ByteSize`, ADR-0266) or `maxBytes`
   (a byte count), at most one, as `volumeBudget` and `volumeBudgetBytes` are; none means no cap. The per-item caps
   move into it unchanged, with no compatibility path, as the resource-shape refactor did (#1072):
   `quota.maxObjectBytes` on a Bucket, `quota.maxKeyBytes` and `quota.maxValueBytes` on a KVStore; one at the spec's
   root is a 422, and so is a size that does not parse or both forms. A stored object loses a root cap at the upgrade
   until its owner applies it again under `quota` (none is deployed; the decider accepted breaking changes). The inline entries embed these specs (ADR-0251),
   so a quota is written once, wherever the store is declared: alone, or in an App, a Function, a Workflow, a Lake or
   a Site. A Lake has no quota of its own; its prefix's quota is its cap.

## Design model

- **Spec**: [`docs/design/storage/quota-usage.fizz`](../design/storage/quota-usage.fizz) · state machine
  [`quota-usage.md`](../design/storage/quota-usage.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `UsageNeverBelowTotal` | always | `bucket-total-refused`, `lake-total-refused`, `kv-total-refused` | `TestBookUsageNeverBelowTotal` |
  | `CapHolds` | always | the same: the cap's own promise | `TestBookCapHolds` |
  | `RefusalsRestOnAFreshMeasure` | always | `overwrite-not-refused` | `TestScenarioOverwriteNotRefused` |
- **Bounds**: one capped subject, two writes over two keys and two sizes, one delete, two measures; 10 actions, at
  which each mutant fails.
- **Faults**: a failed store, which releases its reservation; a delete racing the writes and the measure.
- **Mutants**: `forget-settled-while-measuring` and `count-the-credit` (a credit that counts, which a concurrent delete
  falsifies; the model found it) fail `UsageNeverBelowTotal`; `refuse-on-stale-measure` fails
  `RefusalsRestOnAFreshMeasure`.
- **Result**: pass, 53,446 states, 12 s, FizzBee v0.5.4.
- **Left to tests**: a paged listing, modeled as one read (safe: a write stored once a measure has started settles
  into it); `Replaces`' stat outside the lock (safe: a credit never counts); a waiter's progress
  (`…OverwriteNotRefused`); SQL's unknown-size Execute and the deletes rule (`…SQLDeleteAtCap`); a write charged to a
  prefix and its Bucket (`…LakeTotalRefused`).

## Temporary workarounds

None.

## Contracts

```go
// Package quota (internal/platform/quota), NEW.
type Subject struct {
	Kind      v1.Kind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
	Prefix    string // a Bucket prefix; empty for the whole store
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
type Book struct{ /* sources by kind; per-subject lock, limit, measure, settled and reserved bytes */ }
func NewBook(clk clock.Clock) *Book
func (b *Book) Register(kind v1.Kind, src Source)

// Package v1alpha1 (api/types/v1alpha1), NEW: BucketSpec.Quota BucketQuota; BucketPrefix.Quota and
// SQLDatabaseSpec.Quota SizeQuota; KVStoreSpec.Quota KVStoreQuota.
type SizeQuota struct {
	MaxSize  ByteSize `json:"maxSize,omitempty"`  // or
	MaxBytes int64    `json:"maxBytes,omitempty"` // at most one
}
type BucketQuota struct {
	SizeQuota      `json:",inline"`
	MaxObjectBytes int64 `json:"maxObjectBytes,omitempty"` // moved from BucketSpec
}
type KVStoreQuota struct {
	SizeQuota     `json:",inline"`
	MaxKeyBytes   int   `json:"maxKeyBytes,omitempty"`   // moved from KVStoreSpec
	MaxValueBytes int64 `json:"maxValueBytes,omitempty"` // moved from KVStoreSpec
}

// Package kvstore (internal/kvstore), NEW method on the KV port, by both engines and in the contract suite.
Usage(ctx context.Context, prefix string) (int64, error)

// Package fault (api/fault), NEW kind (507).
const InsufficientStorage Kind = "insufficient_storage"
func InsufficientStoragef(op, format string, a ...any) *Error
```

| Consumes | Exposes |
|---|---|
| Each store's and prefix's `quota`; the engines' measures | 507 on a capped write (S3: `QuotaExceeded`); 413 per item as today; 422 at admission |

## Implementation plan

- `internal/platform/quota`, tested with a fake clock (refresh, single flight, a fresh measure before a refusal, the
  credit, a failed refresh, `Deletes`, a kind without a source), with `oracle:` markers for the spec's assertions.
- `api/types/v1alpha1`: the quota types and their validation; `api/fault`: the kind, its problem and builder; the
  OpenAPI spec and SDK types regenerated.
- The sources and enforcement points; `Usage` on the KV port and both engines; `pkg/funcd` builds the `Book`.
- The move: every reader of a per-item cap (`s3BucketFor`, `pkg/funcd/funcd.go:2444`; the KV resolver through
  `KVStoreSpec.EffectiveMax*`, `internal/services/kv/resolver.go:118-119`) reads `spec.quota`; the five example manifests of
  funcd-typescript and funcd-python move first, in their own releases, then funcd bumps them (ADR-0141).
- Tests, one per scenario: `TestScenarioBucketTotalRefused` (`ctx.blob`, the gateway, a Site), `…LakeTotalRefused`,
  `…OverwriteNotRefused`, `…KVTotalRefused`, `…SQLDeleteAtCap`, `…NoCapUnchanged`, `…PerItemCapMoved`.
- Docs: the Bucket, KVStore, SQLDatabase and Lake references, the blueprint, the changelog's note on the move.
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean;
  `scripts/agent/gate.sh` passes; `just check-hygiene check-specs` passes.

## Review checklist

- [ ] No size check outside `Book`; each enforcement point asks `Admit` before it stores and `Settle` after; reads and
      deletes never ask; the default is `quota.None`; an uncapped subject is never measured.
- [ ] A credit never lowers the count; a refusal follows a fresh measure when bytes were settled since the last one.
- [ ] Admission refuses a bad size, both `maxSize` and `maxBytes`, and a per-item cap at the spec's root, with 422.
- [ ] `fault.InsufficientStorage` maps to 507, and the S3 gateway answers `QuotaExceeded` with 507; every scenario has
      its named test.

## Consequences

- **Positive**: a store can no longer grow without end; all of a store's caps sit in one block, written once wherever
  the store is declared; the next quota is a new source.
- **Negative (accepted)**: a manifest with a per-item cap at the spec's root must move it into `quota` (the changelog
  says so); near its cap, a store that overwrites is re-measured before each such write, one at a time; a write can be
  refused up to a minute after deletes made room; a SQLDatabase can grow up to a minute of writes past its cap; a
  Restore can load a store past its cap, after which only deletes run until it is under.

## Open questions

- Cedar quota policies (opt-in, the decider's choice): a later ADR has `Book.Admit` ask the PDP after its own check,
  with `usage`, `bytes` and `limit` in the context, a deny by a `@quota` policy answering 507.
- A store's usage in its status: a later ADR, if owners ask for it.

## References

- The decider's refinement of 2026-10-27; FEAT-0016; #1314, tracker #1315.
- `internal/auth/authorizer.go:123`, `pkg/funcd/funcd.go:2433-2447` (the shared Bucket view),
  `api/types/v1alpha1/namespace.go:27-31` (`volumeBudget` beside `volumeBudgetBytes`).
