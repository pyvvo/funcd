# ADR-0292: Stores get a quota block, enforced by one quota framework

- **Status**: Proposed (2026-10-27)
- **Date**: 2026-10-27
- **Deciders**: green-0-rabbit
- **Tags**: storage, limits, quota
- **Realizes**: [FEAT-0016/F207](../feat/0016-feat-workload-limits.md) (a store's total size)
- **Answers**: the decider's refinement of 2026-10-27 (a `quota` block on every store and Bucket prefix, the per-item
  caps inside it, 507 past it; one framework shaped like IAM; Cedar quota policies opt-in, later); #1314
- **Supersedes (in part)**: [ADR-0073](0073-kv-bindings-and-subdomains.md) and
  [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md): where the per-item caps live (`spec.maxKeyBytes`,
  `spec.maxValueBytes`, `spec.maxObjectBytes` move into `spec.quota`); their 413 (ADR-0148) is unchanged ·
  [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md) Decision 2: a new generation no longer tears the engine
  down; the provider runtime replaces an engine whose spec changed (#1195)
- **Relates to**: [ADR-0291](0291-a-functions-workers-get-cpu-and-memory-limits.md) (F206, split from this ADR) ·
  [ADR-0251](0251-catalogservice-declares-its-stores.md) (inline stores) · [ADR-0266](0266-volume-sizes-with-units.md)
  (`ByteSize`) · [ADR-0275](0275-a-bucket-prefix-owner-names-its-kind.md) (a Lake's prefix) ·
  [ADR-0293](0293-a-lakes-catalog-lives-in-a-platform-catalog-store.md) (a Lake's catalog, outside its prefix) ·
  [ADR-0018](0018-api-server-authn-rbac-admission.md) (V2 quotas)

## Context & Need

A Bucket, a KVStore and a SQLDatabase cap one object, key, value or database count (ADR-0073, ADR-0080, ADR-0148),
but nothing caps what a store holds in total, so one store can crowd out every other workload; a Lake's files live
under a Bucket prefix (ADR-0275). This ADR gives every store one `quota` block, enforced through one decision point
that every storage write asks, as every PEP asks the one Cedar PDP: the next quota (per namespace, V2) is a new source.

## Scenarios

- **scenario: bucket-total-refused**: Given a Bucket with `quota.maxSize: 1MiB` that holds 1 MiB, When a Function's
  `ctx.blob` put, an S3 client's put or a Site's build adds an object, Then it gets 507 (the S3 error
  `QuotaExceeded`), the object is not written, and a read or a delete still works.
- **scenario: lake-total-refused**: Given a Lake whose prefix has `quota.maxSize: 1MiB` and holds 1 MiB, When a client
  commits an `INSERT` too large for DuckLake to inline, Then its Parquet write gets 507 and the commit fails, while a
  query still works: the catalog saves outside the prefix (ADR-0293).
- **scenario: overwrite-not-refused**: Given a Bucket within 1 KiB of its quota that holds a 4 KiB object, When an S3
  client overwrites it with 4 KiB, Then the put waits on one fresh measure and is admitted.
- **scenario: kv-total-refused**: Given a KVStore at its quota, When a Function puts a key, Then it gets 507, and a get
  or a delete still works.
- **scenario: sql-delete-at-cap**: Given a SQLDatabase at its quota, When its owner executes an `INSERT`, Then it gets
  507, and so does `DELETE FROM t WHERE 0; INSERT …`; When it executes only `DELETE` or `DROP` statements, Then they
  run, and after the next measure an `INSERT` lands again.
- **scenario: no-cap-unchanged**: Given a store whose quota sets no size, When it is written, Then nothing is measured.
- **scenario: quota-change-keeps-the-engine**: Given a Ready SQLDatabase, When its owner changes only its quota, Then
  its engine keeps running and its calls are served throughout.
- **scenario: per-item-cap-moved**: Given a Bucket with `quota.maxObjectBytes: 1024`, When an object of 2 KiB is put,
  Then it gets 413 as today (ADR-0148); a manifest with `spec.maxObjectBytes` at the root is refused with 422.

## Scope

**In**: the quota framework (decision point, enforcement points, sources); the `quota` block on Bucket, Bucket prefix,
KVStore and SQLDatabase, with the per-item caps moved into it.
**Out**: Function CPU and memory (ADR-0291); per-namespace budgets (V2, F208), new sources on this framework; Cedar
quota policies (opt-in, later); a store's usage in its status; funclog sizes; a Restore's loads and the purger.

## Constraints & Decision drivers

- One check, as `auth.Authorizer` (`internal/auth/authorizer.go:123`): a new cap is a new source, never a new check.
- Only a capped store is measured, and only when a decision needs it. A Bucket never tracks an uncapped subject; an
  uncapped KVStore or SQLDatabase costs a map lookup per write and a read of its spec a minute while written.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. One quota framework shaped like IAM, one `quota` block per store** | One check; a new cap is a new source | Usage errs high between measures | **Chosen** |
| B. A size check coded in each write path | No new package | One copy per path; namespace quotas would add more | Rejected: not DRY |
| C. Exact counters updated on every write | No error margin | An extra read on every overwrite and delete; drift after an unclean stop | Rejected |
| D. SQLite's `PRAGMA max_page_count` for SQL | Exact; deletes always work | Per connection inside the engine, unverified for rqlite | Rejected for now |
| E. A size field on the Lake itself | Close to the Lake | Repeats its prefix's cap; a second enforcement path | Rejected: the prefix caps it once |

## Decision

1. **The decision point.** `internal/quota` (NEW; outside `internal/platform`, whose leaf rule bars its clock) defines
   `Checker`, the quota counterpart of `auth.Authorizer`: `Admit` before a write returns the write's `Settle`, called
   once after it. `Book` implements it; `quota.None` admits everything and is the default of every enforcement point,
   as `hold.NeverCall` is for the hold. The daemon builds one `Book` and runs its refresh. The `Book` keeps its own
   copy of a subject, whose strings the S3 gateway reads from a buffer the next request reuses (the lanes showed it).
2. **Sources.** The feature that owns a kind registers a `Source`: `Limit` reads the subject's cap (no size ⇒ no cap),
   `Measure` its usage: a Bucket's or a prefix's listed sizes (a Lake's catalog is outside, ADR-0293); a KVStore's
   keys and values, through `kvstore.Usager` (NEW, a driver capability beside `DropPrefix`); a SQLDatabase's
   `(page_count - freelist_count) * page_size`, through its engine. F208 adds the namespace as a parent.
3. **Usage.** `Admit` reserves the write's full bytes; `Settle` settles or releases them. Usage is the last measure
   plus the settled and the reserved bytes, so for a write of known size it never counts less than the store holds; a
   measure that lands keeps the bytes settled while it ran. A subject's first write reads its limit and, when capped,
   its usage; `Book.Run` reads both again every minute, and drops a subject unused for ten minutes. A failed refresh
   keeps the last values; while a first one fails, writes get 503. A quota change applies from the next refresh; a
   newly capped subject is measured before its next decision. A capped subject's stored write of unknown size (a SQL
   Execute) starts a measure, so the next write sees what it added.
4. **The refusal.** A delete through an enforcement point reports itself (`Request.Deletes`, always admitted). When a
   write would pass the limit and bytes were settled or deleted since the last measure, `Admit` waits on one shared
   fresh measure. On that usage, the size the write replaces (`Request.Replaces`: a stat of the key, or the old value's
   length) is credited to the decision, never to the count, and read again if a measure lands before the decision. A
   write still past the limit gets the NEW `fault.InsufficientStorage` (507, `urn:funcd:problem:insufficient-storage`)
   naming the subject, its usage and its limit. An Execute, of unknown size, needs room left, unless it only deletes:
   each statement text holds one statement (only comments and whitespace after its first `;`) whose first keyword is
   `DELETE` or `DROP`; anything else, a `WITH … DELETE` included, is a write; a delete trigger may write a little.
5. **Enforcement points.** Bucket: `blob.Quoted` (NEW), in `s3BucketFor` inside `blob.Capped`
   (`pkg/funcd/funcd.go:2446`), so a per-item 413 comes before any reservation; it is the one view behind the S3
   gateway, `ctx.blob` and Sites. It charges the key's prefix when the Bucket declares it with a size, then the
   Bucket when it sets one, so a key segment no quota names is never tracked; a refusal by the Bucket releases the
   prefix's reservation. It reports a delete to the same subjects and refuses a presigned PUT under a cap (403), as
   `blob.Capped` does under a per-object cap. The gateway answers `QuotaExceeded` with 507; a Site is `NotReady`, reason
   `QuotaExceeded`. KVStore: the KV facade's Put and Delete. SQLDatabase: the SQL facade's Execute. Reads never ask.
6. **The quota block.** Every cap of a store sits in its `spec.quota` (NEW), under the component it caps: `BucketSpec`,
   `BucketPrefix`, `KVStoreSpec` and `SQLDatabaseSpec`. Its size is `maxSize` (a `ByteSize`, ADR-0266) or `maxBytes`
   (a byte count), at most one, as `volumeBudget` and `volumeBudgetBytes` are; none means no cap. The per-item caps
   move into it unchanged, with no compatibility path, as the resource-shape refactor did (#1072):
   `quota.maxObjectBytes` on a Bucket, `quota.maxKeyBytes` and `quota.maxValueBytes` on a KVStore; one at the spec's
   root is a 422, and so is a size that does not parse or both forms; a stored one is lost at the upgrade (breaking,
   accepted). The inline entries embed these specs (ADR-0251), so a quota is written once, wherever the store is
   declared. A Lake has no quota of its own: its prefix's quota caps its files, and `lake.catalogMaxSize` its catalog,
   inlined rows included (ADR-0293). A quota change reaches no engine: the SQL reconciler no longer tears one down at
   each generation; the provider runtime replaces an engine whose spec changed (#1195).

## Design model

- **Spec**: [`docs/design/storage/quota-usage.fizz`](../design/storage/quota-usage.fizz) · state machine
  [`quota-usage.md`](../design/storage/quota-usage.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `UsageNeverBelowTotal` | always | `bucket-total-refused`, `lake-total-refused`, `kv-total-refused` | `TestBookUsageNeverBelowTotalAndCapHolds` |
  | `CapHolds` | always | the same: the cap's own promise | `…UsageNeverBelowTotalAndCapHolds`, `TestStaleCreditReadAgain` |
  | `RefusalsRestOnAFreshMeasure` | always | `overwrite-not-refused`, a delete making room | `TestScenarioOverwriteNotRefused`, `TestDeleteMakesRoomAtOnce`, `TestDeleteDuringAMeasureCounts` |
- **Bounds and faults**: one capped subject, two writes over two keys and two sizes, a failed store, one delete, two
  measures; 9 actions (10 pass too: 137,648 states, 33 s). **Result**: pass, 80,459 states, 19 s, FizzBee v0.5.4.
- **Mutants**: `forget-settled-while-measuring` and `count-the-credit` (the model found it) fail `UsageNeverBelowTotal`;
  `refuse-on-stale-measure`, `deletes-not-reported` and `forget-a-delete-while-measuring` fail
  `RefusalsRestOnAFreshMeasure`, which reads the ground truth: changes since the last landed measure's read.
- **Left to tests**: a paged listing (one read in the model); the credit's stat outside the lock
  (`TestStaleCreditReadAgain`); a waiter's progress; SQL's unknown-size Execute and its deletes rule
  (`…SQLDeleteAtCap`); a prefix and its Bucket charged together (`…LakeTotalRefused`); a subject capped only now
  (`TestNewlyCappedSubjectMeasuredFirst`); a subject's reused request strings (`TestSubjectKeptAsACopy`).

## Temporary workarounds

None.

## Contracts

```go
// Package quota (internal/quota), NEW.
type Subject struct {
	Kind      v1.Kind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
	Prefix    string // a Bucket prefix; empty for the whole store
}
type Request struct {
	Subject  Subject
	Bytes    int64                                     // the write's size, or Unknown
	Replaces func(ctx context.Context) (int64, error) // the size it overwrites, asked only before a refusal; nil: 0
	Deletes  bool                                      // a delete: always admitted, reported once stored
}
const Unknown int64 = -1      // a size known only once the write ran (a SQL Execute); 0 is a known size
type Settle func(stored bool) // ends an admitted write: settles its bytes, or releases them
// Checker is the quota counterpart of auth.Authorizer.
type Checker interface {
	Admit(ctx context.Context, r Request) (Settle, error) // reserves r.Bytes, or fault.InsufficientStorage
}
var None Checker // admits everything
type Source struct {
	Limit   func(ctx context.Context, s Subject) (int64, bool, error)
	Measure func(ctx context.Context, s Subject) (int64, error)
}
type Book struct{ /* sources by kind; per subject: limit, measure, settled, reserved, deleted-since */ }
func NewBook(clk clock.Clock, logger *slog.Logger) *Book
func (b *Book) Register(kind v1.Kind, src Source)
func (b *Book) Run(ctx context.Context)     // Refresh every minute
func (b *Book) Refresh(ctx context.Context) // every subject's limit and usage, read again

// Package blob (internal/blob), NEW: the Bucket enforcement point.
func Quoted(inner Bucket, chk quota.Checker, bucket quota.Subject, capped func(prefix string) bool) Bucket

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

// Package kvstore (internal/kvstore), NEW driver capability, by both engines and in the contract suite (RunUsage).
type Usager interface{ Usage(ctx context.Context, prefix string) (int64, error) }

// Package fault (api/fault), NEW kind (507).
const InsufficientStorage Kind = "insufficient_storage"
func InsufficientStoragef(op, format string, a ...any) *Error
```

| Consumes | Exposes |
|---|---|
| Each store's and prefix's `quota`; the engines' measures | 507 on a capped write (S3: `QuotaExceeded`); 413 per item as today; 422 at admission |

## Implementation plan

- `internal/quota`, tested with a fake clock (refresh, single flight, a fresh measure before a refusal, the
  credit, a failed refresh, `Deletes`, a kind without a source), with `oracle:` markers for the spec's assertions.
- `api/types/v1alpha1`: the quota types and their validation; `api/fault`: the kind, its problem and builder; the
  OpenAPI spec and SDK types regenerated.
- The sources and enforcement points; `Usager` on both KV engines; `pkg/funcd` builds the `Book` and runs it.
- The move: every reader of a per-item cap (`pkg/funcd/funcd.go:2444`, `internal/services/kv/resolver.go:118-119`)
  reads `spec.quota`; the five example manifests of funcd-typescript and funcd-python move first, in their own
  releases, then funcd bumps them (ADR-0141).
- Tests, one per scenario: `TestScenarioBucketTotalRefused` (`ctx.blob`, the gateway, a Site), `…LakeTotalRefused`,
  `…OverwriteNotRefused`, `…KVTotalRefused`, `…SQLDeleteAtCap`, `…NoCapUnchanged`, `…PerItemCapMoved`,
  `…QuotaChangeKeepsTheEngine`; on real engines, a case each in the duckdb, kv and sql Lima lanes.
- Docs: the Bucket, KVStore, SQLDatabase and Lake references, the blueprint, the changelog's note on the move.
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean;
  `scripts/agent/gate.sh` passes; `just check-hygiene check-specs` passes.

## Review checklist

- [ ] No size check outside `Book`; each enforcement point asks `Admit` before it stores and `Settle` after, a delete
      reports itself, reads never ask; the default is `quota.None`; an uncapped subject is never measured, and a
      Bucket never asks for one.
- [ ] A credit never lowers the count and is read again after a landed measure; a refusal follows a fresh measure when
      bytes were settled or deleted since the last one; a newly capped subject is measured before its next decision.
- [ ] Admission refuses a bad size, both `maxSize` and `maxBytes`, and a per-item cap at the spec's root, with 422.
- [ ] `fault.InsufficientStorage` maps to 507, and the S3 gateway answers `QuotaExceeded` with 507; every scenario has
      its named test.

## Consequences

- **Positive**: no store grows without end; a store's caps sit in one block; the next quota is a new source.
- **Negative (accepted)**: a manifest with a per-item cap at the spec's root must move it into `quota` (the changelog
  says so); near its cap, a store that overwrites or deletes is re-measured before the next refusal, one measure at a
  time; a quota change, and a delete made outside the enforcement points (a Restore's load, the purger), count from
  the next refresh, up to a minute later; a SQLDatabase can pass its cap by the Executes admitted before the measure
  that follows them lands, one Execute of any size included; a Restore can load a store past its cap, after which
  only deletes run until it is under.

## Open questions

- Cedar quota policies (opt-in): a later ADR has `Admit` ask the PDP with `usage`, `bytes` and `limit` in the context.
- A store's usage in its status: a later ADR, if owners ask for it.

## References

- The decider's refinement of 2026-10-27; FEAT-0016; #1314, tracker #1315; `internal/auth/authorizer.go:123`,
  `pkg/funcd/funcd.go:2433-2447` (the shared Bucket view), `api/types/v1alpha1/namespace.go:27-31` (`volumeBudget`).
