# ADR-0320: KV writes that hold only while a condition holds, and keys that expire

- **Status**: Implemented (2026-11-10), review passed in its second round with no finding open ([report](../reviews/adr-0320-implementation-claude-opus-5-5-2.md)); Reviewing (2026-11-10, implementation done); Accepted (2026-11-10), after four rounds of an independent judge (a subagent), whose two Majors and every Minor and Nit were folded
- **Date**: 2026-11-09
- **Deciders**: green-0-rabbit
- **Tags**: kv, kvstore, local-api, shims, backup, cdc, quota
- **Realizes**: [FEAT-0010/F234](../feat/0010-feat-apps.md) (claims, leases and idempotency keys in a Function's KV)
- **Supersedes (in part)**: [ADR-0066](0066-kv-service-durable-engine.md) — the gateway's one Badger txn per batch (a txn ends before a request for a key it already wrote when either is versioned), and its Contracts' `CDC.OnWrite`, which gains the expiry; [ADR-0068](0068-kv-opt-in-cdc.md) Decision 1 and its Contracts — the change record carries the key's expiry, and the sweep's delete is a record; [ADR-0209](0209-kv-backup-on-the-common-format.md) Decision 7 — each live key goes with its value and its expiry; [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) Decision 4's `kv/<store>/` row — a record's value is `EncodeKV`; [ADR-0288](0288-a-boot-reclaim-quarantines-data-no-object-names.md) Decision 2's KVStore row — each key is copied with its expiry; [ADR-0311](0311-each-provider-backs-up-and-restores-its-own-member-kind.md) Decision 4's KVStore row — the export carries each key's expiry; [ADR-0195](0195-kv-backup-delete-records.md) Decision 1 — a key whose value expired counts as existing, so its delete, the sweep's or a plain one, writes a record, as does the sweep's delete of a key compaction left no entry; [ADR-0312](0312-a-buckets-backup-member-skips-its-lakes-prefixes.md) Decision 8 — `Format` is 3
- **Relates to**: [ADR-0019](0019-service-facade-pattern-kv.md) (the port), [ADR-0067](0067-kv-opt-in-dr-backup.md) (the DR chain), [ADR-0069](0069-kv-data-plane.md) (the local API's KV verbs), [ADR-0073](0073-kv-bindings-and-subdomains.md) and [ADR-0136](0136-roles-and-role-assignments.md) (who writes a table), [ADR-0202](0202-platform-store-snapshot-and-timeline.md) (versions name a timeline), [ADR-0210](0210-api-optimistic-concurrency-if-match.md) (the control plane's `If-Match`), [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) Decision 8 (a per-namespace runner waits for its namespace's hold), [ADR-0292](0292-stores-get-a-quota-block-and-a-quota-framework.md) (a delete makes room), [ADR-0305](0305-providers-start-and-stop-through-one-lifecycle.md) Decision 4 (a loop is a Runner)

## Context & Need

A Function's KV has get, put, delete and list (ADR-0069). A put overwrites, nothing carries a version, and no key
expires (ADR-0073 left per-key TTL out). So two Functions cannot claim one thing through KV: each put succeeds and the
last one wins. Data Studio's workflows claim a run request with a conditional SQL `UPDATE` instead, because a workflow
step cut by a crash runs again (`docs/behavior/eventing.md`). The behavior registry has no KV row for a version, a
condition or an expiry; the S3 gateway's create-only PUT checks, then writes, so two racers both get 200 (#1179).

**Purpose.** KV offers claims, locks, leases and idempotency keys as an orchestration primitive: a write that stores
only while a condition on its key holds and says who won, and keys that expire, so a crashed holder's claim ends.
Experiment 0018 built it through the drivers, the facade, the local API and both shims, and raced it on a real funcd.

## Scenarios

- `scenario: claim-race-one-winner`: Given 32 writers that put-if-absent one key at once, Then exactly one stores and gets the new version; each other gets 412 with the winner's version as its `ETag`, and a read returns the winner's value.
- `scenario: compare-and-set-names-a-version`: Given a key at version v, When a write names v, Then it stores and returns a new version; a write naming any other version, or a key that does not exist, gets 412 with the current version or none.
- `scenario: delete-if-releases-only-its-own`: Given a key at version v, When a delete names v, Then it deletes the key; a delete naming another version keeps it and gets 412.
- `scenario: plain-put-overwrites`: Given a key with a TTL, When a plain put writes it, Then it overwrites as before, takes a new version and has no expiry.
- `scenario: ttl-expires-a-key`: Given a key put with `ttl=1`, Then it lives at least 1 s from when funcd took the write and less than 2 s, then is absent to get, list, the quota's measure and every condition; a put-if-absent claims it again, and a compare-and-set with its version loses.
- `scenario: renewal-moves-the-expiry`: Given a key held with a TTL, When a compare-and-set with its version and a new TTL renews it, Then the key outlives its first expiry; one with no TTL leaves it none.
- `scenario: lost-condition-fails-only-its-request`: Given one gateway batch holding a lost put-if-absent, a lost compare-and-set and plain writes, Then the losers write nothing and get their own result, and the plain writes commit.
- `scenario: version-names-one-write`: Given a versioned write and a plain write of one key in one batch, Then the plain write has a version of its own, and a compare-and-set naming the first version loses.
- `scenario: sweep-deletes-what-expired`: Given an expired key and one renewed before its expiry, When the sweep runs, Then it deletes the expired one, which the change feed publishes as a delete and the quota counts as room at once, and keeps the renewed one; a held namespace's expired keys wait for the release, and no index key outlives the entry it names.
- `scenario: sweep-deletes-what-compaction-removed`: Given a key whose value with a TTL replaced a plain value the chain holds, When it expires and Badger's compaction removes both before the sweep, Then the sweep still deletes the key: the change feed publishes the delete, and its delete record keeps a restore from bringing the plain value back.
- `scenario: restore-takes-a-new-timeline`: Given a lease shipped by the DR chain, When it is restored, Then it keeps its value and expiry under a new version: the holder's compare-and-set with the old version loses, one with the version it reads again wins.
- `scenario: store-backup-carries-the-expiry`: Given a KVStore backup holding a key with a TTL, When it is restored after another key's expiry passed, Then the first comes back with its expiry and the second does not.
- `scenario: quarantine-keeps-the-expiry`: Given a KVStore with a key that has a TTL, When a boot reclaim moves the store into the quarantine, Then the key keeps its expiry there.
- `scenario: two-functions-race-a-claim`: Given a JS and a Python Function on a real funcd, When they race a put-if-absent of one key 20 times, Then each round has one winner and the loser reads the winner's holder.

## Scope

- **In**: the port's two capabilities, both drivers, the gateway's rule, the version's timeline, the expiry index and the sweep, the facade's verbs, the local API's headers, `?ttl` and 412, the change record's expiry, the per-store export and load with workload `Format` 3, the quarantine's copy, `kvstore.sweepInterval`, both shims' verbs, the `kv-claim` examples, tests and lane.
- **Out**: conditional writes on the S3 gateway (#1179, its own decision); multi-key transactions; `If-Match: *`; a TTL maximum; a key's expiry in a GET's answer; a fencing token beyond the version; cross-node KV (FEAT-0002); the control plane's 409 (ADR-0210).

## Constraints & Decision drivers

- Decided 2026-11-09 by the decider: KV offers this anyway, as a primitive for claims, locks, leases and idempotency keys; experiment first; a lost condition answers 412 (RFC 9110, and the S3 gateway) rather than ADR-0210's 409; a per-store backup carries each key's expiry (ADR-0209 Decision 7 exports each live key, and ADR-0222 and ADR-0312 already carry a record's metadata); a sweep deletes expired keys through the gateway (ADR-0068 promises every change captured, #98 routed DropPrefix's deletes through the gateway for that reason, ADR-0292 Decision 4 lets a delete make room at once); a version is an opaque hash that carries no count; a lost condition's `ETag` goes to every caller that may write the table; FEAT-0010 F234 under #871; the shims ship first.
- Existing rules kept: a version names its timeline and a restore mints another (ADR-0202 Q6); the quarantine keeps what it moved (ADR-0288), a key with a TTL until its expiry; a per-namespace runner waits for its namespace's hold (ADR-0222 Decision 8); a table is written by its owner or a writer role (ADR-0073, ADR-0136); one writer goroutine commits every data key (ADR-0066); a loop is a provider's Runner (ADR-0305 Decision 4). No new dependency.

## Alternatives considered

| Decision | Chosen | Rejected (why) |
|---|---|---|
| Do nothing | — | Functions keep claims in SQL, which is atomic and durable; the decider wants KV to offer them |
| The version | A hash of Badger's commit version and the key, keyed with the instance's timeline | `<timeline>-<n>`, ADR-0202's form: the decider chose an opaque hash; a counter in the value changes every stored byte for each reader, export and consumer; Badger's version alone survives a restore, so a version of the lost history matches (ADR-0202's problem 2) |
| Where a condition is read | In the gateway's txn that writes, against the committed key | A read, then a write in the facade lets two racers pass (#1179); Badger's optimistic txns never conflict with one writer |
| A lost condition | 412 and the current version's `ETag` | 409, the control plane's answer (ADR-0210): RFC 9110 gives a failed `If-Match` or `If-None-Match` 412, as the S3 gateway answers |
| An expiry | Badger's `ExpiresAt`, the second rounded up | `WithTTL` rounds down, so a 1 s key can live 0.1 s (mutant `floor-expiry`); an expiry kept beside the value, out of Badger's sight, keeps compaction from removing the entry but makes every read, list and measure decode it |
| What sees an expiry | A sweep deletes each expired key through the gateway, one compaction removed included | Silence: no change record, the quota frees it at the next measure, within 1 min; a sweep of only the entries it finds misses what compaction removed (mutant `compaction-blind`) |
| Finding the expired keys | An expiry index in the reserved keys, read up to now; every write drops the index key of the entry it replaces | A scan of every key per pass |
| A per-store backup | Each record carries its expiry; a load skips one that passed | Leaving TTL keys out drops idempotency windows and supersedes Decision 7 further; a plain record makes a restored claim last forever |
| The shims' surface | One verb per condition; versions opaque strings | An options bag with `ifMatch` makes callers write HTTP; a number loses JS precision |

## Decision

1. **Port.** `kvstore.Conditional` (`GetEntry`, `PutIf`, `DeleteIf`) and `kvstore.Sweeper` (`Sweep`) are capabilities beyond `kvstore.KV`, as `Usager` is; the Badger and memory drivers offer both. `Put` and `Delete` are unchanged.
2. **Versions.** Each write gives its key a version: the first 16 bytes, as 32 hex characters, of HMAC-SHA256 over the write's Badger commit version (the memory driver's counter) and the key, keyed with the instance's timeline, 32 random bytes stored at `\x00kv/timeline` and minted when absent. A version is opaque and carries no count. The DR export leaves the timeline out, so a restored instance mints another and every version changes (ADR-0202 Q6); a restart keeps it. Callers compare versions for equality only.
3. **Conditions.** A put-if-absent stores while the key is absent (an expired key is), a compare-and-set while the key is at the version it names, a delete-if alike. A versioned write is a conditional write, a write with a TTL or the sweep's delete; a plain put or delete is not. The gateway reads each condition in the txn that writes, against the committed key: a txn ends before a request for a key the txn already wrote when either request is versioned. A lost condition is its request's result (not done, the current version), never the batch's error. A winner's version is read back from its key after the commit.
4. **Expiry.** A TTL is whole seconds from 1. A key's `ExpiresAt` is the first Unix second at or after the moment funcd takes its write plus the TTL, so it lives at least the TTL and less than a second more, and a holder that counts its lease from before it sends the write never counts past it. Each write sets its own expiry: one without a TTL, a compare-and-set or put-if-absent included, leaves the key with none; a renewal is a compare-and-set with a new TTL. An expired key is absent at once to every read, list, measure and condition.
5. **Sweep.** Each write with an expiry adds `\x00kv/exp/<20-digit second>/<key>`, and every write drops the index key of the entry it replaces, as a store's DropPrefix drops its keys' (read from the store's own keys, so the teardown stays O(store)), so the index holds one key per live key with an expiry. The sweep is the KV provider's Runner (ADR-0305 Decision 4): every `kvstore.sweepInterval` (default 1 s, at least 100 ms) it deletes the due keys through the gateway, 1024 per call while more are due, and drops each index key. It deletes a key with no live entry: the expired entry, or none when Badger's compaction removed it with every older version. The change feed records the delete. With the DR backup on, a delete of a key whose newest entry is a value, live or expired, writes ADR-0195's delete record, the sweep's and a plain one alike, as does the sweep's delete of a key compaction left no entry, since Badger reads an expired key as absent while the chain may hold an older value. The quota hears each store as of a delete (ADR-0292 Decision 4). A key of a held namespace waits for the release, as the namespace's timers and runs do (ADR-0222 Decision 8): the sweep changes the namespace's data and its feed. A quarantined key waits only for the platform's hold. The KV sweep is not the quarantine's (ADR-0288), whose entries leave by their period.
6. **Local API.** A GET answers `ETag: "<version>"`. A PUT with `If-None-Match: *` creates (201), with `If-Match: "<version>"` replaces (204), and one with only `?ttl=<seconds>` writes (204), each with the new version's `ETag`; `?ttl` gives any PUT a TTL, and a plain PUT answers 204 with none. A DELETE with `If-Match` deletes (204) and ignores `?ttl`. A lost condition answers 412 `urn:funcd:problem:precondition-failed` with the current version's `ETag` when the key exists. A weak tag, `If-Match: *`, `If-None-Match` other than `*`, two header lines, both conditions, `If-None-Match` on a DELETE or a TTL that is no whole number from 1 answers 400 before any write. Authorization, the caps and the quota are a put's or a delete's; a lost write is charged nothing.
7. **Change feed.** A change record gains `expiresAt` for a write with a TTL; a sweep's delete is an `op` delete record.
8. **Backups.** The DR chain keeps each key's expiry and version number in Badger's format. A per-store export carries each live key with its expiry: the record's value is `EncodeKV(expiresAt, value)`, workload `Format` 3, and `ReadManifest` refuses 2. A load leaves out a record whose expiry passed and gives the others theirs.
9. **Quarantine.** The KVStore move copies each key with its expiry, so a quarantined claim expires when it would have.
10. **Shims.** funcd-python: `get_entry`, `put_if_absent`, `compare_and_set`, `delete_if` and `put(..., ttl=)`; funcd-typescript: `getEntry`, `putIfAbsent`, `compareAndSet`, `delIf` and `put(..., { ttl })`. A win returns the new version, a loss `None`/`null` (`False`/`false` for a delete); any other answer that is not 2xx raises, as before. Their docs say a lease is counted from before the call and each write sets its own expiry.

## Design model

- **Spec**: [`docs/design/storage/kv-claims.fizz`](../design/storage/kv-claims.fizz) · state machine [`kv-claims.md`](../design/storage/kv-claims.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `OneHolder` | always | `claim-race-one-winner`, `ttl-expires-a-key` | `TestConditionalContract`, the Badger driver's `conditional` run, `TestScenarioE2EKVClaimRace` |
  | `VersionNamesItsWrite` | always | `version-names-one-write`, `restore-takes-a-new-timeline` | `TestGatewayVersionNamesOneWrite`, `TestRestoreTakesANewTimeline` |
  | `SweepSparesALiveKey` | always | `sweep-deletes-what-expired` | `TestConditionalContract`, the Badger driver's `conditional` run (`sweep`) |
  | `StaleVersionLoses` | always | `restore-takes-a-new-timeline` | `TestRestoreTakesANewTimeline` |
  | `ExpiredIsSwept` | always eventually | `sweep-deletes-what-expired` | `TestKVProviderSweepsExpiredKeys` |
  | `FeedAgrees` | always eventually | `sweep-deletes-what-compaction-removed` | `TestSweepDeletesWhatCompactionRemoved` |
- **Bounds**: 2 claimants, 2 claims, 1 renewal, 1 owner's put, 1 ship, 1 restore, T 2 ticks, clock to 3. Every action is atomic, so 1 concurrent action; 15 actions, the fewest that pass (14 cuts a run before its sweep, which fails a progress assertion), and 20 reaches the same states.
- **Faults**: Badger's compaction, which removes an expired entry with every older one; a restore of the one shipped point, which loses the claims made after it, whose holders then leave `OneHolder` (the chain's recovery point, ADR-0067).
- **Environment**: one clock for funcd and the claimants; a holder counts its lease from before it sends its write, so the model's claim step is the latest it can start; the rounding up is `kvstore.ExpiresAt`'s; a consumer of the change feed rereads the store after a restore, since a restored outbox republishes nothing at or below its cursor (ADR-0195's Scope).
- **Mutants**: `no-split` and `floor-expiry` fail `OneHolder`; `shared-version` and `keep-timeline` fail `VersionNamesItsWrite`; `sweep-by-name` fails `SweepSparesALiveKey`; `no-sweep` and `compaction-blind` fail liveness.
- **Result**: pass, 62,030 states, 31.8 s, FizzBee v0.5.4; wider (3 claims, 21 actions): pass, 118,778 states, 116 s.
- **Left to tests** (the spec's own list): the sweep's wait for a held namespace (`TestSweepWaitsForAHeldNamespace`) and its quota report (`TestSweepFreesQuotaAtOnce`), the delete record a sweep writes for the chain (`TestSweepDeletesWhatCompactionRemoved`), the index's size (`TestEveryWriteDropsTheIndexKeyItReplaces`), the readback of a winner's version (`TestGatewayVersionNamesOneWrite`), the per-store export's and the quarantine's expiries (`TestKVStores_CarryEachKeysExpiry`, `TestQuarantineKeepsAnExpiry`), and two Functions on containerd (the `kv-claim` lane).

## Behavior rows

In `docs/behavior/services.md`, each `decided` and citing its Decision and test: `A put-if-absent or compare-and-set raced by writers`, `A key written with a TTL`, `A key that expired`, `A version from before a restore`, `A KVStore backup and a key's expiry`, `A quarantined key's expiry`, `An expired key in a held namespace`.

## Temporary workarounds

None.

## Contracts

```go
// internal/kvstore
type Entry struct{ Value []byte; Version string; ExpiresAt uint64 }
type Write struct{ IfAbsent bool; IfVersion string; TTL time.Duration; ExpiresAt uint64 } // ExpiresAt: a copy's own
type Result struct{ Done bool; Version string } // not Done: the current version, "" absent
type Conditional interface {
	GetEntry(ctx context.Context, key string) (Entry, bool, error)
	PutIf(ctx context.Context, key string, value []byte, w Write) (Result, error)
	DeleteIf(ctx context.Context, key, version string) (Result, error)
}
type Sweeper interface {
	Sweep(ctx context.Context, now time.Time, limit int, skip func(key string) bool) (swept []string, more bool, err error)
}
func ExpiresAt(now time.Time, ttl time.Duration) uint64
func NewTimeline() string                      // 32 random bytes as 64 hex characters
func Version(timeline, key string, n uint64) string // hex(HMAC-SHA256_timeline(n, key))[:32]
// internal/services/kv: Facade.GetEntry/PutIf/DeleteIf, the alias's table as Get/Put/Delete; RunSweep(ctx, SweepDeps)
// internal/backup/workload: EncodeKV(expiresAt uint64, value []byte) []byte; DecodeKV; Format = 3
// internal/snapshot: Record gains ExpiresAt; api/fault: PreconditionFailed (412)
// badger CDC: OnWrite(txn *badger.Txn, key string, op Op, expiresAt uint64) error; changeRecord.ExpiresAt `json:"expiresAt,omitempty"`
// config: kvstore.sweepInterval, an ADR-0194 duration, default 1s, at least 100ms
// pkg/funcd: kvProvider is a lifecycle.Runner, whose Run runs kv.RunSweep on Run's context
```

The shims (funcd-python `funcd_shim.KVClient`, funcd-typescript `KVClient`), `ttl` in whole seconds:

| Python | TypeScript | Win | Loss |
|---|---|---|---|
| `get_entry(binding, key)` | `getEntry(binding, key)` | `KVEntry(value, version)` | `None` / `null`: absent |
| `put_if_absent(binding, key, value, *, ttl=None)` | `putIfAbsent(binding, key, value, opts?)` | the new version | `None` / `null` |
| `compare_and_set(binding, key, value, version, *, ttl=None)` | `compareAndSet(binding, key, value, version, opts?)` | the new version | `None` / `null` |
| `delete_if(binding, key, version)` | `delIf(binding, key, version)` | `True` / `true` | `False` / `false` |
| `put(binding, key, value, *, ttl=None)` | `put(binding, key, value, opts?)` | stored | — |

The local API, on `/kv/{binding}/{key}`:

| Request | Win | Loss | Refused |
|---|---|---|---|
| `GET` | 200, `ETag` | 404 | — |
| `PUT`, `If-None-Match: *` | 201, `ETag` | 412, the holder's `ETag` | 400 |
| `PUT`, `If-Match: "<v>"` | 204, `ETag` | 412, the current `ETag` or none | 400 |
| `PUT ?ttl=<s>`, no condition | 204, `ETag` | — | 400 |
| `DELETE`, `If-Match: "<v>"` | 204 | 412, the current `ETag` or none | 400 |

I/O: the gateway reads a key's committed entry and writes, in one txn, the key, its index key, its change record and its delete record; the sweep reads the index up to now and deletes through the gateway; the export reads each live key with its expiry.

## Implementation plan

1. `internal/kvstore`: the port; `badger`: the gateway's rule, `expiry.go` (timeline, the index and its cleanup by every write and by DropPrefix, `Sweep`), the sweep's delete with its record, the export's exclusion, `LoadPrefix` and `SnapshotPrefix` with expiries; `memory`: the same; `kvstorecontract.RunConditional`.
2. `internal/services/kv`: the facade's verbs, `sweep.go`, the quarantine's copy; `pkg/funcd`: the sweep as the KV provider's Runner, and its `run` hook in `TestPhases_ListTheProvidersWithTheirHooks`; `internal/platform/config` and `cmd/funcd`: the key; `examples/funcdconfig.yaml`.
3. `internal/workernode/local/kv.go`: the headers; `api/fault`: 412; `internal/backup/workload`: `EncodeKV`, `Format` 3; `internal/snapshot`: the record's expiry.
4. funcd-python and funcd-typescript: the verbs and their docs, their tests and `examples/kv-claim`, each released first; then `go get` of both tags here.
5. `pkg/funcd/kv_claim_e2e_test.go`; the `kv-claim` lane (`scripts/lanes.yaml`, `e2e/kv-claim.venom.yml`, `e2e/fixtures/kv-claim-writer.yaml`, the `e2e/README.md` row); the spec, with a note in `quarantine.fizz`; the behavior rows; F234.
6. At acceptance: the `Superseded in part by` back-links on ADR-0066, ADR-0068, ADR-0195, ADR-0209, ADR-0222, ADR-0288, ADR-0311 and ADR-0312; F234 → `accepted`; `blueprint.md`'s KV storage and `context.kv` lines take conditional writes and expiring keys.

**Test plan**:

| Scenario | Test |
|---|---|
| `claim-race-one-winner`, `compare-and-set-names-a-version`, `delete-if-releases-only-its-own`, `plain-put-overwrites`, `ttl-expires-a-key`, `renewal-moves-the-expiry` | `kvstorecontract.RunConditional` on both drivers (`TestScenarioKVDurableDriverRoundtrips/conditional`, `TestConditionalContract`); the local API's `TestScenarioKV*OverLocalAPI`; `TestScenarioE2EKVClaimRace` |
| `lost-condition-fails-only-its-request`, `version-names-one-write` | `TestGatewayLostConditionIsItsOwnResult`, `TestGatewayVersionNamesOneWrite`; the refusals, `TestKVConditionalRefusals` |
| `sweep-deletes-what-expired` | `RunConditional`'s `sweep`, `TestKVProviderSweepsExpiredKeys`, `TestCDCRecordCarriesExpiry`, `TestSweepFreesQuotaAtOnce`, `TestSweepWaitsForAHeldNamespace`, `TestRenewalKeepsOneIndexEntry`, `TestEveryWriteDropsTheIndexKeyItReplaces` |
| `sweep-deletes-what-compaction-removed` | `TestSweepDeletesWhatCompactionRemoved` |
| `restore-takes-a-new-timeline` | `TestRestoreTakesANewTimeline`, `TestVersionAndExpirySurviveRestart` |
| `store-backup-carries-the-expiry`, `quarantine-keeps-the-expiry` | `TestKVStores_CarryEachKeysExpiry`, `TestReadManifest_RefusesAnotherFormat`, `TestQuarantineKeepsAnExpiry` |
| `two-functions-race-a-claim` | lane `kv-claim`; the shims' `test_kv_conditional.py` and `kv-conditional.test.ts` |

**Done**: every test passes under `-race`; each mutant fails its assertion; reverting each Decision's key line fails a test; the `kv-claim` lane passes; both shims pass their `just ci`; `scripts/agent/gate.sh` passes.

## Review checklist

- [ ] Only the gateway reads a condition, in the txn that writes, and no txn holds two writes of a key when one is versioned.
- [ ] A version is a hash of the commit version and the key, keyed with the timeline, and shows no count; the DR export leaves the timeline out.
- [ ] An expiry rounds up; each write sets its own; an expired key is absent to every read and condition.
- [ ] Every write drops the index key of the entry it replaces, and a DropPrefix its store's.
- [ ] The sweep, the KV provider's Runner, deletes a key with no live entry, one compaction removed included; it publishes a delete, tells the quota, and waits for a held namespace. With the DR backup on, a delete of a key whose newest entry is a value, live or expired, writes a delete record, and so does the sweep's delete of a key compaction left no entry.
- [ ] A per-store export and load and the quarantine keep each key's expiry; `Format` is 3.
- [ ] The local API's statuses, `ETag` and refusals match Decision 6; authorization, caps and quota are a put's or a delete's.
- [ ] Every scenario has its test and each assertion its oracle marker; the spec's mutants fail.

## Consequences

- A Function claims, renews and releases a key with exactly one winner, from JS or Python.
- On one key, each versioned write commits alone: a compare-and-set loop on a key every writer shares costs a commit per round (experiment 0018's `gateway-compare-and-set-hot`). Writes of distinct keys still share commits; a plain write reads its key's entry first, to drop the index key of one with a TTL (`gateway-put-distinct`).
- A version does not survive a restore: a holder reads its key again after one. A restore to an earlier point loses the claims made after it; their holders count on them until a renewal loses (the chain's recovery point, ADR-0067).
- The index holds one key per live key with a TTL, and a pass with nothing due reads one key range. With the DR backup on, each key the sweep deletes writes a delete record, which a re-baseline prunes (ADR-0195).
- A lost condition tells a caller that may write the table whether the key exists and, when it does, its version, even without `kv::read`: a conditional write shows that by its nature, and the version allows nothing a plain put does not.
- A key expires on the host's clock: a clock stepped forward expires keys early.
- A workload backup of format 2 cannot be restored: no node holds one.
- A quarantined key with a TTL leaves the quarantine when it expires.

## Open questions

- The S3 gateway's create-only PUT (#1179): its own decision, which could read its condition in the write's own step as Decision 3 does.
- A TTL maximum, and a key's expiry in a GET's answer: when a workload asks for them.

## References

- The feature issue, #1424 (F234), under the Apps tracker #871.
- Experiment 0018, pyvvo/experiment#31 (private), at b3fb383: the final rules through the whole stack, raced on a real funcd, and the gateway's cost. Its first run, at 45161ce, predates the independent judge's findings.
- RFC 9110 §13.1.1 and §13.1.2 (`If-Match`, `If-None-Match`, 412); Badger v4.9.6's `Entry.ExpiresAt`, `Item.Version`, and its compaction (`levels.go`), which removes an expired entry with every older one.
- #98 (deletes through the gateway), #1179 (the S3 gateway's create-only PUT), `docs/behavior/services.md` and `eventing.md`.
