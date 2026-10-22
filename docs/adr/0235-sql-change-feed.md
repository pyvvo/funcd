# ADR-0235: SQL change feed

- **Status**: Implemented (2026-10-12, review passed). Accepted (2026-10-12, self-accepted under the decider's batch delegation; judged three times: the first
  round keyed the watermark by engine epoch, added the namespace hold, the boot gate, per-database serialization and the
  token's protections; the second made the boot gate answer every valid token and removed the size refusal; the third
  found no Blocker or Major)
- **Superseded in part by**: [ADR-0260](0260-a-loopback-listener-owns-its-port.md) (2026-10-22) — Decision 2: on loopback, an unset `sql.changeFeedPort` is the port a bind of `127.0.0.1:0` gets.
- **Date**: 2026-10-12
- **Deciders**: green-0-rabbit
- **Tags**: sql, eventing, cdc
- **Realizes**: [FEAT-0011/F132](../feat/0011-feat-sql.md) (SQL change feed)
- **Relates to**: [ADR-0108](0108-eventsource-v2-named-events.md) (named events, extended with a source kind) ·
  [ADR-0109](0109-sensor-event-action-binder.md) (Sensors) · [ADR-0119](0119-object-store-eventsource.md) (the blob
  source, the model) · [ADR-0201](0201-event-store.md) (the event store, which keeps the watermark) ·
  [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md) (the engine and its entrypoint) ·
  [ADR-0206](0206-restore-and-held-boot.md) / [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) Decision 8
  (the platform and namespace holds) · [ADR-0068](0068-kv-opt-in-cdc.md) (the KV change feed, a precedent) · source:
  [design report](../reports/sql-provider-design.md)

## Context & Need

Apps react to new and changed records: a notification on an insert, a projection on an update. Without a feed, a
function polls its database. rqlite streams every committed change to an HTTP endpoint, at least once, with a Raft
index per commit and the row before and after (design report). This ADR turns those changes into named events that
Sensors already bind to functions and workflows (ADR-0108, ADR-0109), through a new EventSource kind `sql`.

## Scenarios

- `scenario: insert-starts-action` — Given an EventSource of kind `sql` on `todo-db` with event `todo-added` on table
  `todos`, op `Insert`, and a Sensor that invokes `todo-notify` on it, When a function inserts a row into `todos`,
  Then `todo-notify` is invoked once with the row in `event.data.after`.
- `scenario: update-and-delete-events` — Given events on `Update` and `Delete`, When a row is updated then deleted,
  Then each starts its action with `before` and `after` as rqlite reports them.
- `scenario: other-tables-ignored` — When a row of another table changes, Then no event fires.
- `scenario: redelivery-fires-once` — Given a commit already turned into events, When the engine delivers it again,
  also while the first delivery is still being processed, Then no event fires a second time.
- `scenario: recreated-database-fires` — Given `todo-db` fired events up to a high Raft index, When it is deleted and
  recreated, or restored into a new engine, and a row is inserted, Then the event fires.
- `scenario: changes-wait-for-release` — Given the platform is held, or the database's namespace is held, When a row
  changes, Then no event fires until the release, and then it fires once.
- `scenario: changes-wait-for-sources-at-boot` — Given a daemon restart, When the engine redelivers before the
  EventSources are loaded, Then the delivery is answered 503 and the events fire once the sources are loaded.
- `scenario: forged-delivery-refused` — When anything but the database's engine posts a change to the feed's endpoint,
  Then funcd answers 404 and fires nothing.

## Scope

**In**: the EventSource kind, its events, validation and status; the engine's CDC configuration; the daemon's
receiving endpoint; the watermark; the event's shape.

**Out**: Sensors and their actions (unchanged); a change feed to the bus; filtering on column values.

## Constraints & Decision drivers

- At-least-once delivery from rqlite, retried after a timeout even while funcd still processes the first request; a
  redelivered commit must not fire twice (FEAT-0011 exit criterion).
- A Raft index is only meaningful within one engine directory: a recreated or restored engine starts again at 1.
- rqlite's CDC config has no header for a credential, only an endpoint URL and TLS (design report).
- A held platform or namespace starts no side effect (ADR-0206, ADR-0222 Decision 8).
- The engine is created once and adopted afterwards, so its CDC setting must not depend on which EventSources exist.
- rqlite keeps undelivered changes on disk; with an endpoint that keeps refusing, the queue grows (CDC guide).

## Alternatives considered

1. **Poll a changes table written by triggers**: works with any engine, but adds triggers to every app schema and lag.
   Rejected.
2. **Turn on CDC only for databases an EventSource names**: less traffic, but adding the first EventSource would need
   an engine restart. Rejected: every engine sends its changes; funcd drops those no EventSource wants.
3. **mTLS to authenticate the engine**: rqlite supports client certificates, but funcd would run a CA for engines.
   Rejected for a capability URL derived from the node master.
4. **A watermark keyed by database name**: simple, but a recreated or restored engine's low indexes would be dropped as
   duplicates without notice. Rejected for an epoch per engine directory.

## Decision

1. **The engine sends every change.** The entrypoint of ADR-0232 writes `/tmp/cdc.json` (never on the host's disk)
   with `endpoint` from `FUNCD_SQL_CDC_ENDPOINT`, `service_id` the engine's epoch, and an explicit `transmit_timeout`
   of 10 s and a `max_batch_size` of 100, and adds `-cdc-config /tmp/cdc.json`. The epoch is a random 128-bit value the entrypoint writes into
   `/data/epoch` when that file is absent and reads afterwards. The SQL reconciler always sets the
   endpoint env.
2. **The endpoint.** The daemon serves `POST /v1/changes/<token>` on one listener, port `sql.changeFeedPort` (default
   7075, fixed), bound on the bridge gateway address under containerd (the catalog proxies' publish host) and on
   loopback in process mode, bound once the bridge exists, retried as the catalog proxies' `Listen` is. `cmd/funcd` adds the port to the network's internal allow list, so engines reach it; any
   worker can then reach it too, and the token is the guard. `<token>` is HMAC-SHA256 of the node master over
   `funcd-sql-cdc/<ns>/<name>`, base64url, 256 bits; the daemon recomputes the tokens of the databases that exist and
   compares with `hmac.Equal`. The token is checked before the body is read: an unknown token answers 404 and its body
   is never read. A valid sender's body is decoded as a stream, so a commit of any size neither fills memory nor blocks
   the feed. The path is never logged.
3. **The watermark.** For each `(namespace, database, epoch)` the event store keeps the highest Raft index whose events
   were published; an unknown epoch starts at 0. A group at or below the watermark is acknowledged and dropped. Requests
   for one database are serialized from the check to the raise, so a retry that overlaps its first delivery waits and is
   then dropped. The watermark is raised after its events are published, so only a daemon crash between the two can
   repeat a group; the CloudEvent `id` lets a function drop it (Sensors do not dedupe). Deleting the `SQLDatabase`
   deletes its watermarks.
4. **Holds and boot.** Until the EventSources are loaded after a start, every delivery with a valid token answers 503.
   Afterwards, while the platform or the database's namespace is held, a delivery for a database that some EventSource
   names answers 503 and rqlite retries; a delivery for a database that none names is acknowledged and dropped, so the
   engine's queue does not grow.
5. **The source kind.** `EventSource.spec.sql` names a database of its namespace and named events, each with a
   non-empty `table` and `on` (`Insert`, `Update`, `Delete`; `Normalize` fills all three when empty). A missing
   database makes the EventSource NotReady `DatabaseNotFound`. A matching change publishes a CloudEvent with `type` the
   event name, `source` the EventSource URI, `subject` the table, `id` `<ns>/<db>/<epoch>/<index>/<event>/<n>` (`n`
   counts the changes of the group, from 0), and
   data `{op, table, rowId, before, after, index, commitTimestamp}`, `op` being `Insert`, `Update` or `Delete`. When
   the data would pass 256 KiB, `before` and `after` are dropped while decoding and `truncated: true` is set.

## Temporary workarounds

None.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: EventSource
metadata:
  name: todo-changes
  namespace: default
spec:
  sql:
    database: todo-db
    events:
      - name: todo-added
        table: todos
        on:
          - Insert
```

```go
// api/types/v1alpha1/eventsource.go
type SQLSource struct {
	Database ObjectName `json:"database"`
	Events   []SQLEvent `json:"events"`
}
type SQLEvent struct {
	Name  ObjectName      `json:"name"`
	Table string          `json:"table"`
	On    []SQLChangeType `json:"on,omitempty"`
}
type SQLChangeType string // "Insert", "Update", "Delete"
// EventSourceSpec.SQL *SQLSource `json:"sql,omitempty"`

// internal/services/sql/changefeed.go
func ChangeToken(master []byte, ns v1.NamespaceName, name v1.ObjectName) string

// internal/eventing/eventstore
// Store.ChangeWatermarks() gives Get(ns, db, epoch) (uint64, error), Set(ns, db, epoch, index uint64) error and
// Delete(ns, db) error; the tenant is in the store's snapshot and load.
```

| Endpoint call | Answer |
|---|---|
| unknown token | 404 |
| a valid token before the EventSources load | 503 (rqlite retries) |
| a database some EventSource names, while held | 503 (rqlite retries) |
| a database no EventSource names | 200, dropped |
| groups at or below the watermark of their epoch | 200, nothing fired |
| new groups | events published, watermark raised, 200 |

## Implementation plan

1. Types, validation and `Normalize`, `just generate`; the entrypoint's epoch and `cdc.json`; the reconciler's env;
   config `sql.changeFeedPort`; the internal allow list entry in `cmd/funcd`.
2. `internal/services/sql/changefeed.go`: the listener, token check, decode, match, publish onto the eventing fanout,
   the per-database lock, the holds and the boot gate; `eventstore`: the watermark tenant; the EventSource status.
3. Tests: units for decoding a payload captured from the real engine (the guide's example carries comments), matching,
   the epoch-keyed watermark, two concurrent posts of one group, the token, a commit whose rows pass 8 MiB (it fires
   truncated and the next commit still fires); one `TestScenario<Name>` per scenario in
   `pkg/funcd`, posting payloads as the engine would; the `sql` lane gains an insert that invokes a function through a
   Sensor on the real engine, and checks that the single node's high-watermark update works with ADR-0232's
   permissions (`all` covers `cdc-hwm-update`, else the auth file gains it).

**Definition of done**: the tests pass; the eventing tests pass unchanged; the lane step passes once; the gate passes;
no username or absolute path in a changed file; neither the token nor the endpoint appears in a log.

## Review checklist

- [ ] Every engine gets the CDC config; no change to it depends on EventSources.
- [ ] The token is derived, compared in constant time, never stored on the host or logged; an unknown token is 404.
- [ ] The watermark is keyed by epoch, checked and raised under one per-database lock, and deleted with the database.
- [ ] The boot gate answers 503 to every valid token; the holds answer 503 only for databases some EventSource names.
- [ ] The token is checked before the body is read; a valid body is stream-decoded, never refused for its size.
- [ ] One test per scenario, named for it.

## Consequences

- Functions and workflows react to row changes with the Sensors they already use.
- Every engine posts every change to the daemon, used or not.
- During a long hold, a database that an EventSource names keeps its changes in its engine's queue, on its disk.
- A daemon crash between publish and watermark can repeat a change; the function dedupes by event id.

## Open questions

None.

## References

- rqlite CDC guide (design report, Sources); [ADR-0108](0108-eventsource-v2-named-events.md),
  [ADR-0119](0119-object-store-eventsource.md), [ADR-0201](0201-event-store.md).
