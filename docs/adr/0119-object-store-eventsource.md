# ADR-0119: Object-store EventSource — a `blob:` source kind (reactive ingestion, F83)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0157](0157-blob-event-seen-list.md) (2026-10-05) — Decision §3 cursor/new-object rule, Watermark/Cursor contracts, §5 never-drops, sizing.
- **Date**: 2026-07-10
- **Accepted**: 2026-07-10
- **Reviewing**: 2026-07-10
- **Implemented**: 2026-07-10
- **Acceptance note**: judge changes-requested folded (3 Majors + minors) — **M1** poll-vs-push re-argued honestly (writer-agnostic over one `List` seam + survives a real-external-S3 backend swap + the `blob.Bucket` port has no notification seam; the false "external writes bypass funcd" claim is removed — external PUTs do go through funcd's s3gateway); **M2** the CloudEvent `data.etag` → `data.version` (an honest `(ModTime,Size)` fingerprint, not a content ETag; a real ETag would arrive as a NEW field, not a silent swap); **M3** the watermark persists via `internal/kvstore.KV`, not the non-existent `store.Update(func(Txn))`. Minors: the ModTime-monotonicity soundness assumption + boundary stated, LIST is O(objects) (drain expectation), a missing `Bucket` ⇒ NotReady, `on:[Created]` defaulted in decode not the pure `Validate`.
- **Deciders**: green-0-rabbit
- **Tags**: eventing, eventsource, blob, object-store, cloudevents, ingestion, polling, data-platform
- **Realizes**: [FEAT-0003/F83](../feat/0003-feat-data-platform.md) (object-storage EventSource kind — reactive ingestion)
- **Relates to**: [ADR-0108](0108-eventsource-v2-named-events.md) (EventSource v2 — the kind-keyed source +
  named-events + Publisher/Fanout seam this **extends** with a new source kind), [ADR-0109](0109-sensor-event-action-binder.md)
  (the Sensor that binds the emitted event to a `WorkflowRun` — **unchanged**, reused as-is), [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md)
  (the S3 frontend — one of several writers — whose writes land in the same `blob.Bucket` this polls), [ADR-0007](0007-blob-storage-layer-port.md)
  (the `blob.Bucket` port — `List(prefix)` is the poll surface), [ADR-0118](0118-eventing-dead-letter-queue.md)
  (the eventing dead-letter queue — F83 reuses it for downstream-action reliability; adds **no** DLQ of its own).

## Context & Need

FEAT-0003 runs a lakehouse on funcd: functions and DuckDB/DuckLake write Parquet into the `blob` substrate
over the ADR-0080 S3 frontend, and a workflow DAG does the ELT. Today the only way to *start* that DAG is a
**timer** ([ADR-0108](0108-eventsource-v2-named-events.md) `timer:` source) — scheduled ingestion. The
missing half is **reactive** ingestion: *drop a file → the DAG runs*. When an object lands under a watched
`Bucket` prefix (a raw drop zone, a partner upload, a DuckLake checkpoint), funcd should emit a named event
so a Sensor starts the ingest `WorkflowRun`.

[ADR-0108](0108-eventsource-v2-named-events.md) already built the machinery: an `EventSource` is a
**kind-keyed union of source kinds**, each hosting **named events** that fire as CloudEvents onto a
`Publisher`/`Fanout` seam a [Sensor](0109-sensor-event-action-binder.md) subscribes to. The union was
explicitly designed to grow (`timer:` shipped; `webhook:` deferred). This ADR does the **producer half of
F83**: add a **`blob:` source kind** that watches a `Bucket` prefix and publishes a named CloudEvent per new
object — **no new eventing machinery**, just a new source kind + a blob-notification watcher. The
consumer half (Sensor → run) is [ADR-0109](0109-sensor-event-action-binder.md), reused **unchanged**.

**Detection is a polling watcher, not a push seam.** funcd **lists-and-diffs** the watched prefix on an
interval, tracking a persisted **watermark** (per object) so each object emits exactly once. Polling is
chosen because it is **writer-agnostic** — one watcher on the substrate covers the ADR-0080 S3 gateway, the
blob-facade/SDK write path, and S3-backed secret/config drivers with a single seam, where a push hook must
instrument every writer — and it **survives a backend swap** to a real external S3 written out-of-band (the
FEAT-0003/F47 "external-S3 stays a swap" world), where funcd's own gateway is bypassed and no in-process
hook can fire; the ADR-0007 `blob.Bucket` port also exposes **no notification seam** to hook. (The S3
gateway *does* compute a real content ETag at write-completion — `internal/blob/s3gateway/backend.go`
`PutObject`→`sub.Put` — so a future gateway-emit path is a latency follow-on, not V1.) The cost is detection
latency (one poll interval) and a periodic `LIST`.

**Purpose / callers.** Whoever declares an `EventSource` with a `blob:` kind (`funcdctl apply`) gets
reactive ingestion; the [ADR-0109](0109-sensor-event-action-binder.md) Sensor is the consumer, projecting
`${{ event.data.key }}` into the run input. It tells the implementer what to test: an object under the
prefix fires the named event once, a Sensor starts the run, and neither a re-list nor a restart re-fires.

## Scenarios

Each becomes a named acceptance test.

- `object-created-emits-event` — Given a Ready `blob:` EventSource watching `Bucket` `raw` prefix `drop/`
  with event `arrived`, When an object `drop/a.parquet` lands, Then **one** named CloudEvent is published
  (`source == funcd://<ns>/eventsource/<name>`, `type == arrived`, `data == {bucket,key,size,version,time}`),
  and a Sensor `do: [{on: dep, workflow: ingest, input: {file: "${{ event.data.key }}"}}]` starts a
  `WorkflowRun` of `ingest` with `input.file == "drop/a.parquet"`.
- `external-s3-write-detected` — Given the same source, When `drop/b.parquet` is written over the ADR-0080
  S3 gateway by an external client (SigV4), Then the poll detects it and fires `arrived` once — proving
  detection rides `List` alone and is **writer-agnostic** (it never depends on which path wrote the bytes).
- `dedup-no-refire` — Given `drop/a.parquet` already emitted, When the next poll re-`LIST`s the prefix and
  the object is unchanged, Then **no** event is emitted (the watermark suppresses the re-seen object).
- `prefix-scoped` — Given the source watches `drop/`, When an object `other/c.parquet` lands outside the
  prefix, Then **no** event is emitted.
- `restart-no-replay` — Given a source that has already emitted for every object under `drop/`, When funcd
  restarts and polls, Then the persisted watermark is reloaded and **no** object re-emits (a restart is not
  a replay of the whole prefix).
- `eventsource-validate` — Given a spec with **both** `blob:` and `timer:` set, Then `Validate` rejects it
  (exactly one source kind); Given a `blob:` with an empty `bucket`, Then `Validate` rejects it; Given a
  `blob:` event with an empty `on`, Then the decode/normalize step defaults it to `[Created]` (`Validate`
  stays pure — it does not mutate, per ADR-0108).

## Scope

**In:**
- The `blob:` source kind on `EventSourceSpec` (union with `timer:`, **exactly one**): `BlobSource{Bucket,
  Events}`, each `BlobEvent{Name, Prefix, On}` (default `On == [Created]`), plus the `Validate` extension.
- A **poll watcher** component (`internal/eventing`): per Ready `blob:` EventSource it `List`s each event's
  prefix on an interval, diffs against the **persisted watermark**, and publishes a named CloudEvent per new
  object through the existing `Publisher`. A single platform **poll-interval** config knob.
- The **watermark store** (a small `Watermark` port; the V1 driver persists over `internal/kvstore.KV`, the
  durable in-tree key/value substrate — one small JSON record per `(ns,source,event)`) — dedup within a run
  and across restarts.
- The blob CloudEvent shape `data == {bucket, key, size, version, time}` and its flow Publisher → Fanout →
  Sensor (the ADR-0109 machinery, unchanged).
- `on: [Created]` only (object-created).

**Out (named follow-ons):**
- **`Removed` / `Updated` events.** V1 fires only on object-created. Delete/overwrite detection needs a
  tombstone/version diff and a richer `data`; a follow-on ADR. *(The `On []BlobEventType` field is already
  the growth seam — no reshape.)*
- **Push/notification-driven detection.** A bucket-native change feed (S3 event notifications, a driver
  webhook) would cut latency, but the V1 `blob.Bucket` port exposes no such seam and a per-writer hook is
  not writer-agnostic (below). Deferred behind the same `blob:` kind (a driver swap, no spec change).
- **A per-object content ETag.** The ADR-0007 `blob.Attributes` surface exposes `{Key, Size, ModTime}` — no
  ETag. V1 fingerprints an object version as `(ModTime, Size)` and reports it as `data.version`; a real
  per-object content ETag is a follow-up. (gocloud's `List` carries only an `MD5`, not a per-object ETag, so
  a true ETag at list time costs N extra round-trips — an `Attributes`/gateway-emit follow-up, open q.)
- **A per-object DLQ.** Downstream-action reliability (a failed Sensor action retried + dead-lettered) is
  [ADR-0118](0118-eventing-dead-letter-queue.md)'s job; F83 emits and **reuses** that DLQ, it adds none.
- **The consumer/action side** — the [ADR-0109](0109-sensor-event-action-binder.md) Sensor, reused unchanged.

## Constraints & Decision drivers

- **Extend the ADR-0108 union; add no new eventing machinery.** A new source *kind* on the existing
  kind-keyed union, emitting through the existing `Publisher`/`Fanout` to the existing Sensor. The producer
  changes; the delivery seam and the consumer do not.
- **Writer-agnostic detection.** The lakehouse's writers are heterogeneous — the ADR-0080 S3 gateway, the
  blob facade/SDK, S3-backed secret/config drivers, and (on a backend swap) a real external S3 written
  out-of-band. Detection must not depend on which path wrote the bytes, or reactive ingestion silently
  misses drops. Polling `List` observes them all with one seam; a push hook would instrument each writer.
- **New-object soundness + its boundary.** The `ModTime > watermark` rule is sound because funcd's single
  blob substrate assigns `ModTime` monotonically at write-completion with list-after-write consistency; its
  boundary is coarse (second-granular S3 `LastModified`) ModTime resolution and clock non-monotonicity. An
  overwrite that bumps `ModTime` re-fires a `Created` on a non-creation — acceptable under at-least-once ingest.
- **`List` cost is O(objects-under-prefix), not O(arrivals).** gocloud `List(prefix)` has no since/marker, so
  each poll re-lists the **whole** prefix; the watermark bounds retained **state**, not **list** cost. Expect
  the drop zone to be drained/rotated; a marker-based incremental list is a follow-on.
- **Missing bucket ⇒ NotReady.** A `blob:` source whose `Bucket` does not exist in the namespace is
  **NotReady** with a condition (mirroring the Route `BackendNotFound` pattern), not Ready-but-silently-not-polling.
- **Driver-independent.** Detection rides the ADR-0007 `blob.Bucket` port (`List`), which every backend
  (memory/file/S3) satisfies — no per-backend notification integration, and the in-memory backend tests it
  without an S3 endpoint.
- **At-least-once with per-object dedup.** A re-`LIST` must not re-emit a seen object (`dedup-no-refire`),
  and a restart must not replay the prefix (`restart-no-replay`) — so the watermark is **persisted**.
- **Reuse the in-tree KV substrate for persistence.** The watermark is small durable state; persisting it
  over `internal/kvstore.KV` (already in-tree, with its own backup path) beats a second persistence mechanism
  (a bespoke state file to locate, fsync, and back up). It is **low-volume/bounded** — one small record per
  event, rewritten per poll — so the KV/metastore is right here, unlike ADR-0118's high-volume DLQ
  operational records that earn a dedicated Badger instance.
- **Reliability rides ADR-0118.** F83 does not re-solve downstream failure handling; it cross-references the
  eventing DLQ.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Push-on-write seam** (funcd emits from inside a write path) | Zero poll latency and no `LIST` cost, and the S3 gateway even has a real content ETag at write-completion (`s3gateway PutObject`→`sub.Put`) — **but it is not writer-agnostic**: it must hook every writer (gateway, blob facade, S3-backed drivers) and still cannot fire when the backend is swapped to a real external S3 written out-of-band. **Rejected** — fails the writer-agnostic constraint. (Kept as a gateway-emit *latency* follow-on behind the same kind.) |
| **Backend-native notifications** (S3 event notifications / a gocloud subscription) | Lower latency and no polling — but the ADR-0007 `blob.Bucket` port exposes no notification seam, it is per-backend (memory/file have none), and it couples eventing to a specific driver. Polling over `List` is uniform across memory/file/S3 and driver-independent. Deferred as a driver-level optimization behind the `blob:` kind (no spec change). |
| **Timestamp-only high-water mark** (remember only `max(ModTime)`; emit anything newer) | Smallest state, but two objects sharing the newest `ModTime` (common on batch writes) tie: one is remembered, the other silently dropped or re-emitted forever. The watermark keeps the **key set at the max timestamp** to break ties correctly. |
| **A pure unbounded seen-set** (persist every key+fingerprint ever seen) | Correct but grows without bound on a busy drop zone. The watermark **compacts** to `(maxModTime, keysAtMax)` — bounded to the objects sharing the newest timestamp — which is sufficient for `Created`-only detection. |
| **Persist the watermark in the EventSource `status`** | Reuses the resource, but a growing seen-set in `status` churns `resourceVersion` on every poll and bloats the object. A dedicated `kvstore.KV` record keyed by `(ns, source, event)` is cheap and out of the reconcile write path. |
| **A bespoke state file under the data dir** | Works (mirrors the s3gw master.key), but adds a second durable artifact to fsync, locate, and back up. `internal/kvstore.KV` already gives durable get/put + the existing backup path — reused behind a `Watermark` port that keeps the file an alternative driver. |
| **A separate `BlobEventSource` resource kind** | A second near-identical resource to the `EventSource`; the ADR-0108 kind-keyed union exists precisely so a new source slots in as a `spec.<kind>:` key, not a new CRD. |

## Decision

Add a **`blob:` source kind** to `EventSource` and a **poll watcher** that emits a named CloudEvent per new
object through the existing ADR-0108 `Publisher`; the ADR-0109 Sensor consumes it unchanged.

1. **Resource (`api/types/v1alpha1`).** Extend the ADR-0108 kind union additively — the union was built to
   grow, so this adds a pointer, it does not reshape:
   ```yaml
   apiVersion: funcd.dev/v1alpha1
   kind: EventSource
   metadata: { name: drops, namespace: lake }
   spec:
     blob:                      # exactly one source kind (union with `timer:`)
       bucket: raw              # the Bucket resource (ADR-0080) to watch
       events:
         - name: arrived        # the CloudEvent `type`
           prefix: drop/        # only objects under this key prefix fire
           on: [Created]        # V1 default; the only supported type
   ```
   `Validate` (extending the existing exactly-one-kind check, **pure/non-mutating** per ADR-0108): exactly
   one of `timer:`/`blob:` set; a `blob:` has a non-empty `bucket` (DNS-1123 label) and ≥1 event with unique
   DNS-1123 `name`s; each event's `on` may only contain `Created` in V1. Defaulting an empty `on` to
   `[Created]` happens in the **decode/normalize** step, not in `Validate`. A spec with an unknown key is
   rejected at the schema edge (`additionalProperties:false` → 422), as for `timer:`.
2. **Poll watcher (`internal/eventing/blobwatch.go`, new).** The **KindEventSource reconciler is still the
   sole reconciler** (ADR-0015 one-per-gvk): `Source.Reconcile` gains a `blob:` branch that registers the
   source's events on a `BlobWatcher` (and deregisters on delete / kind-change), setting `Ready`. The
   `BlobWatcher` owns a **side `Run` loop** (started by the `pkg/funcd` lifecycle, exactly as the timer
   `Source.Run` is): every poll interval, for each registered `(ns, source, event)` it resolves the
   `Bucket` to a scoped `blob.Bucket` (reusing the ADR-0080 `s3BucketFor` resolver — the **same** bucket
   external S3 writes land in), calls `List(ctx, prefix)`, loads the persisted watermark, and for each
   object **newer than the watermark** builds a blob CloudEvent and calls `Publisher.Publish`. It then saves
   the advanced watermark. Emitting to nobody is a no-op (ADR-0108) — a source with no Sensor is valid.
3. **Watermark + dedup (`Watermark` port).** Per `(ns, source, event)` the watermark is a compact cursor
   `{MaxModTime, KeysAtMax []string}`. **New-object rule** for a listed object `o`: new iff
   `o.ModTime > MaxModTime`, **or** (`o.ModTime == MaxModTime` **and** `o.Key ∉ KeysAtMax`). After a poll,
   advance: `MaxModTime = max ModTime seen`, `KeysAtMax =` the keys at that timestamp. This is **bounded**
   (only the tie-set at the newest timestamp is retained), suppresses re-listed objects (`dedup-no-refire`),
   and, because it is **persisted**, survives restart (`restart-no-replay`). The V1 `Watermark` driver
   persists over `internal/kvstore.KV` — `Get`/`Put` one JSON record per key `<ns>/<source>/<event>`. The
   object *version* fingerprint in `data.version` is `(ModTime, Size)` because `blob.Attributes` exposes no
   content ETag in V1 (open q).
4. **CloudEvent (`internal/eventing/cloudevent.go`).** A `NewBlobEvent(ns, source, event, BlobEventData)`
   companion to `NewNamedEvent`: same envelope (`source == funcd://<ns>/eventsource/<name>`, `type ==
   <event>`), but `data == {"bucket","key","size","version","time"}` (JSON) instead of `{}`. It flows through
   the **unchanged** Fanout (routes by `ParseSourceURI(source)+type`) to the **unchanged** Sensor, which
   projects `${{ event.data.key }}` (F73) into the run input — no Sensor change.
5. **Reliability.** A downstream Sensor action that fails is retried and dead-lettered by
   [ADR-0118](0118-eventing-dead-letter-queue.md); F83 adds no DLQ. The watermark advances on **emission**
   (at-least-once): a crash between publish and save re-emits the object next poll (the Sensor/ADR-0118
   handle a duplicate), never drops it.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **`Created` only** (`Removed`/`Updated` deferred) | delete/overwrite detection needs tombstones + a richer diff | the `Removed`/`Updated` follow-on ADR — the `On []BlobEventType` field already carries them, no reshape |
| **`data.version` is a `(ModTime,Size)` fingerprint, not a content ETag** | ADR-0007 `blob.Attributes` exposes `{Key,Size,ModTime}`, no ETag | `data.version` is the honest name now; a real content ETag (once `Attributes` exposes a digest, or via a gateway-emit path) arrives as a **new** `data.etag` field — never a silent semantics swap under one name |
| **Detection latency = one poll interval** | polling is the price of seeing external writes | a backend change-feed driver behind the same `blob:` kind (a driver swap, no spec change) if latency matters |
| **In-process, single-owner watcher** | the V1 daemon runs one watcher (as it runs one timer loop) | a bus-backed / sharded watcher rides ADR-0108's bus-driver evolution (V2), same seam |

## Contracts

### Resource (`api/types/v1alpha1/eventsource.go`)

```go
// EventSourceSpec is the ADR-0108 kind-keyed union, EXTENDED with the blob source kind (ADR-0119, F83).
// Exactly one source-kind pointer is non-nil.
type EventSourceSpec struct {
	Timer *TimerSource `json:"timer,omitempty"` // ADR-0108
	Blob  *BlobSource  `json:"blob,omitempty"`  // ADR-0119: watch a Bucket prefix, fire on object-created
}

// BlobSource hosts the blob kind's named events over one Bucket (ADR-0119).
type BlobSource struct {
	Bucket ObjectName  `json:"bucket"`           // the ADR-0080 Bucket resource to watch (this namespace)
	Events []BlobEvent `json:"events"`           // ≥1; unique names
}

// BlobEvent is one named object-store event: a DNS-1123 name, a key prefix, and the object events it fires on.
type BlobEvent struct {
	Name   ObjectName      `json:"name"`
	Prefix string          `json:"prefix,omitempty"` // only objects under this key prefix fire ("" = whole bucket)
	On     []BlobEventType `json:"on,omitempty"`     // default [Created]; only Created supported in V1
}

// BlobEventType is an object lifecycle event. V1: Created only.
type BlobEventType string

const BlobCreated BlobEventType = "Created"

// EventSource.Validate (extended): exactly one source kind (timer|blob); a blob source has a DNS-1123
// `bucket`, ≥1 event with unique DNS-1123 names; each event.on defaults to [Created] and may contain only
// Created. A legacy/unknown key is rejected at the schema edge (additionalProperties:false → 422).
```

### Poll watcher (`internal/eventing`)

```go
// BlobEventData is the `data` payload of a blob CloudEvent (ADR-0119): what landed, for a Sensor to project.
type BlobEventData struct {
	Bucket  string    `json:"bucket"`
	Key     string    `json:"key"`
	Size    int64     `json:"size"`
	Version string    `json:"version"` // V1: a (ModTime,Size) fingerprint of the observed object version (no content ETag yet)
	Time    time.Time `json:"time"`
}

// NewBlobEvent builds a named CloudEvent for a landed object: same envelope as NewNamedEvent, data = the object.
func NewBlobEvent(ns v1.NamespaceName, source, event v1.ObjectName, d BlobEventData) (CloudEvent, error)

// BucketLister resolves a Bucket resource to its scoped blob listing surface (ADR-0007/0080). The V1 driver
// reuses pkg/funcd's s3BucketFor(shared, store) — the SAME blob.Bucket external S3-frontend writes land in.
type BucketLister interface {
	List(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName, prefix string) ([]blob.Attributes, error)
}

// Watermark persists per-event dedup state so a re-list and a restart never re-emit a seen object (ADR-0119).
// The V1 driver stores one JSON record per (ns,source,event) over internal/kvstore.KV (Get/Put).
type Watermark interface {
	Load(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName) (Cursor, error)
	Save(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName, c Cursor) error
}

// Cursor is the compact, bounded watermark: the newest ModTime seen + the key set AT that timestamp (ties).
type Cursor struct {
	MaxModTime time.Time `json:"maxModTime"`
	KeysAtMax  []string  `json:"keysAtMax"`
}

// BlobWatcher polls each registered blob EventSource, diffs against the Watermark, and publishes new objects.
// Registration is driven by the KindEventSource reconciler (ADR-0015 one-per-gvk); Run is a side loop
// (pkg/funcd lifecycle), mirroring eventing.Source.Run for timers.
type BlobWatcher struct{ /* lister BucketLister; publisher Publisher; marks Watermark; interval time.Duration; ... */ }

func NewBlobWatcher(lister BucketLister, pub Publisher, marks Watermark, interval time.Duration, log *slog.Logger) (*BlobWatcher, error)
func (w *BlobWatcher) Register(ns v1.NamespaceName, source v1.ObjectName, bs *v1.BlobSource) // (re)register a source's events
func (w *BlobWatcher) Deregister(ns v1.NamespaceName, source v1.ObjectName)                  // delete / kind-change
func (w *BlobWatcher) Run(ctx context.Context) error                                        // poll loop until ctx done
```

### Example — the unchanged ADR-0109 Sensor that consumes it

```yaml
apiVersion: funcd.dev/v1alpha1
kind: Sensor
metadata: { name: ingest-on-drop, namespace: lake }
spec:
  on:
    - { name: dropped, source: drops, event: arrived }   # the blob EventSource + event above
  do:
    - name: run-ingest
      on: dropped
      workflow: ingest
      input: { file: "${{ event.data.key }}" }           # F73 projection — unchanged machinery
```

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| the ADR-0007 `blob.Bucket` `List(prefix)` + ADR-0080 `s3BucketFor` bucket resolution (writer-agnostic — depends only on `List`) | a `blob:` source kind on `EventSource` + reactive object-created named events |
| the ADR-0108 `Publisher`/`Fanout` seam + CloudEvent envelope (unchanged); the ADR-0109 Sensor (unchanged) | the `BlobEventData` CloudEvent `data`, projectable via `${{ event.data.key }}` |
| `internal/kvstore.KV` for the persisted `Watermark`; a platform poll-interval config knob | the `BlobWatcher` + `Watermark` port; at-least-once emission with per-object dedup |
| [ADR-0118](0118-eventing-dead-letter-queue.md) for downstream-action reliability (no new DLQ) | (no `go.mod` change — pure-Go over existing ports) |

## Implementation plan

- **Files (production)**: `api/types/v1alpha1/eventsource.go` (`BlobSource`/`BlobEvent`/`BlobEventType` +
  the extended `Validate` + `On` defaulting); `internal/eventing/cloudevent.go` (`NewBlobEvent` +
  `BlobEventData`); `internal/eventing/eventing.go` (`Source.Reconcile` gains the `blob:` branch →
  `BlobWatcher.Register`/`Deregister`; a `BlobWatcher` dep on `Source`, optional/nil when no blob kind is
  used); `internal/eventing/blobwatch.go` (**new** — `BlobWatcher`, the `List`-diff-publish `Run` loop, the
  new-object rule); `internal/eventing/watermark.go` (**new** — the `Watermark` port + the
  `internal/kvstore.KV` driver, one JSON record per `(ns,source,event)`); `pkg/funcd/funcd.go` (wire `BucketLister` from
  `s3BucketFor`, the `Watermark` driver, and start `BlobWatcher.Run` in the lifecycle beside the timer
  `Source.Run`); `pkg/funcd/options.go` (`WithBlobPollInterval(d time.Duration)`, default 15s). OpenAPI regen
  (the extended spec). **No `go.mod` change** (pure-Go over existing ports).
- **Config knob**: one platform-level poll interval (`WithBlobPollInterval`, default **15s**) — a single
  cadence for all blob sources in V1 (a per-source override is an open question, not V1).
- **Test plan** — one named test per Scenario: `internal/eventing` units over a fake clock + a fake
  `BucketLister` (returning scripted `blob.Attributes`) + a capturing `Publisher` + an in-memory `Watermark`
  (`object-created-emits-event` incl. the `data` shape, `dedup-no-refire`, `prefix-scoped`,
  `restart-no-replay` via a reloaded persisted cursor); an `external-s3-write-detected` unit that feeds the
  lister an object it never wrote (the watcher is source-agnostic — it only lists); `api/types` validate
  matrix (`eventsource-validate`: blob+timer both set → rejected, empty bucket → rejected, empty `on` →
  defaults `[Created]`) + a schema-edge test. **In-process e2e** (`pkg/funcd`): a real `blob:` EventSource
  + an ADR-0109 Sensor `workflow:` action → an object `Put` under the prefix (via the blob facade) starts a
  `WorkflowRun` with the projected key. **Venom** (the S3/containerd lane): an **external** S3 `PUT` through
  the ADR-0080 frontend triggers the run in-VM — the external-write proof on real bytes.
- **Definition of done**: every scenario test green; `go build/test/lint/mod` green across the tree; the
  in-process e2e + the S3 Venom lane green; OpenAPI regenerated; the F83 row advanced; the ADR-0109 Sensor
  code **unchanged**; no identity/path leak.

## Review checklist

- [ ] `EventSourceSpec` gains `Blob *BlobSource` as a union member (exactly one of `timer:`/`blob:`); the
      ADR-0108 timer path is untouched.
- [ ] `Validate` rejects blob+timer both set, an empty `bucket`, and duplicate/invalid event names; `on`
      defaults to `[Created]` and rejects non-`Created` in V1; unknown keys 422 at the schema edge.
- [ ] The `BlobWatcher` polls each Ready `blob:` source, `List`s the prefix, and publishes **one** named
      CloudEvent per new object (`data == {bucket,key,size,version,time}`) through the **existing** Publisher.
- [ ] Detection is **polling over `blob.Bucket.List`** and reuses the ADR-0080 bucket resolution — an
      external S3-gateway write is detected (a test proves detection depends only on `List`, not any write path).
- [ ] A `blob:` source whose `Bucket` is missing is **NotReady** with a condition (Route `BackendNotFound`
      pattern), not Ready-but-not-polling; `on` defaults to `[Created]` in decode/normalize, not `Validate`.
- [ ] The `Watermark` suppresses a re-listed object (dedup) and, being **persisted**, prevents restart
      replay; the cursor is bounded (`MaxModTime` + tie key-set), stored over `internal/kvstore.KV`.
- [ ] The CloudEvent flows Publisher → Fanout → Sensor with **no change** to ADR-0108/0109 code; a Sensor
      projects `${{ event.data.key }}` into a `WorkflowRun`.
- [ ] Downstream reliability defers to [ADR-0118](0118-eventing-dead-letter-queue.md); F83 adds no DLQ.
      `Created`-only, the `(ModTime,Size)` `data.version` fingerprint, and poll latency are documented
      workarounds with exits. No `go.mod` change; no identity/path leak; OpenAPI regenerated.

## Consequences

- **(+)** *Drop a file → the DAG runs* — the reactive complement to the timer trigger, closing FEAT-0003's
  ingestion story with **no new eventing machinery** (a source kind + a watcher on existing seams).
- **(+)** **Writer-agnostic.** Because it polls the `blob.Bucket`, one watcher covers every writer — the S3
  gateway, the blob facade, S3-backed drivers, and a swapped external-S3 backend — where a push seam would
  hook each. Reactive ingestion of the actual data plane, whatever writes it.
- **(+)** **Driver-independent + testable.** Detection rides the ADR-0007 `List` port, so it works over
  memory/file/S3 and the in-memory backend tests it without an S3 endpoint.
- **(+)** **Reuses the Sensor and the DLQ unchanged** — the consumer (ADR-0109) and reliability (ADR-0118)
  are untouched; F83 is purely additive on the producer side.
- **(−)** **Detection latency + periodic `LIST`** — an object is seen at most one poll interval late, and
  each poll costs a `LIST` per watched prefix. Acceptable for ingest cadence; a change-feed driver is the
  latency exit.
- **(−)** **Created-only, `(ModTime,Size)` `data.version` in V1** — overwrite/delete and a content ETag are
  follow-ons; an overwrite that bumps `ModTime` re-fires `Created` (fine under at-least-once).
- **(−)** **At-least-once** — a crash between publish and watermark-save re-emits an object; downstream
  dedup/idempotency (Sensor + ADR-0118) absorbs the duplicate, but the workflow author must tolerate it.

## Open questions

| Question | Where it gets answered |
|---|---|
| A **per-source poll interval** override vs the single platform knob | revisit if drop zones need different cadences; V1 ships one `WithBlobPollInterval` (default 15s) |
| Exposing a per-object content **ETag** (a **new** `data.etag`, not replacing `data.version`) | an additive [ADR-0007](0007-blob-storage-layer-port.md)/gateway-emit follow-up; `List` carries only `MD5`, so a true per-object ETag at list time costs N extra round-trips |
| `Removed` / `Updated` object events (and their richer `data`) | the object-lifecycle follow-on ADR — the `On []BlobEventType` field already carries them |
| A **bus-backed / sharded** watcher for HA (multi-owner) | rides [ADR-0108](0108-eventsource-v2-named-events.md)'s bus-driver evolution (V2); the `BlobWatcher` seam is unchanged |
| Watermark **GC** for events whose prefix churns keys forever at distinct timestamps | the cursor is already bounded to the max-timestamp tie-set; revisit only if a pathological write pattern needs a floor |

## References

- [ADR-0108](0108-eventsource-v2-named-events.md) — the kind-keyed EventSource + named-events + Publisher/Fanout seam this extends.
- [ADR-0109](0109-sensor-event-action-binder.md) — the Sensor that binds the emitted event to a WorkflowRun (unchanged, reused).
- [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) — the S3 frontend whose external writes land in the polled `blob.Bucket`.
- [ADR-0007](0007-blob-storage-layer-port.md) — the `blob.Bucket` port (`List(prefix)`, `Attributes`) the watcher polls.
- [ADR-0118](0118-eventing-dead-letter-queue.md) — the eventing dead-letter queue reused for downstream-action reliability.
- FEAT-0003/F83 (object-storage EventSource kind — reactive ingestion); F47 (the S3 frontend / blob substrate).
</content>
</invoke>
