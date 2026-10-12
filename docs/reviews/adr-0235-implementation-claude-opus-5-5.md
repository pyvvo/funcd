## Verdict: changes-requested — 0 blockers, 1 major, 2 minors  (ADR-0235 implementation, model: claude-opus-5-5)

Scope: the uncommitted working tree of `feat/sql-provider` against `4a7dc252` (ADR-0234, Implemented). The new code is
`internal/services/sql/changefeed.go` (the feed: token, listener, stream decoder, watermark, holds), the `sql:` kind in
`api/types/v1alpha1/eventsource.go` and `internal/eventing/eventing.go` (`reconcileSQL`), the `cdc/` tenant in
`internal/eventing/eventstore/eventstore.go`, the epoch and `cdc.json` in `cmd/funcd-rqlite/main.go` and
`internal/services/sql/engineargs/engineargs.go`, the reconciler's endpoint env and watermark delete in
`internal/services/sql/reconcile.go`, `sql.changeFeedPort` and the internal allow list entry in `cmd/funcd/main.go`, the
wiring in `pkg/funcd`, and lane step S8 (`e2e/sql.venom.yml`, `scripts/lanes.yaml`, fixtures from funcd-functions
v0.11.0, pinned in `go.mod`). The scenario tests are in `pkg/funcd/sql_changes_internal_test.go`.

**Not stamped.** The ADR stays `Reviewing` and the FEAT-0011 F132 row stays `reviewing` until Major 1 is fixed.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...`; `GOOS=linux go build ./...` | exit 0 × 2 |
| modules | `go mod verify` | `all modules verified` |
| hygiene | `just check-hygiene` | `hygiene: clean` |
| touched packages, race | `go test -race -count=1` on `api/types/v1alpha1`, `cmd/funcd-rqlite`, `cmd/funcd`, `internal/eventing/...`, `internal/platform/config`, `internal/services/sql/...`, `pkg/funcd` (e2e tagged out) | all `ok`, exit 0 |
| vet | `go vet` on the same packages | exit 0 |
| lint | `go tool golangci-lint run` on the same packages | `0 issues.` |
| gofmt | `gofmt -l` on the changed `.go` files | empty |
| OpenAPI | `specgen` into a scratch file, compared with `api/openapi/funcd.v1alpha1.yaml` | identical |
| lane guards | `go test -run Lane ./internal/testkit/langmod/` | `ok` |
| real-engine lane | the `sql` lane log of the builder's run on containerd (not rerun) | `final status: PASS`, S1 to S8 |
| engine log | rqlite 10.5.3 started with a CDC config whose endpoint refuses, an insert committed, 25 s of output read | the engine logs its service ID and leadership, never the endpoint or its token |
| boot window | a scratch test through `go test -overlay` (the tree is unchanged): 2001 Sensors and the EventSource seeded, `Run` started, a valid delivery posted to the feed's real listener until it answers | answered 200 after about 3 ms with **0** Fanout subscriptions; watermark raised to 4; 1 s later 2001 subscriptions (Major 1). With one Sensor the race is won |

### 🔴 Blocker
None.

### 🟡 Major

1. **A delivery at boot can be acknowledged before any Sensor subscribes, and its events are lost** (`model`). The feed
   is served from `Platform.Run` beside the controller (`pkg/funcd/funcd.go:1568-1595`), with no ordering between
   them. Sensors subscribe to the Fanout in their reconcile (`internal/sensor/sensor.go:261`), and the Fanout drops a
   firing with no subscriber (`internal/eventing/fanout.go:10-14`). The feed then raises the watermark
   (`changefeed.go:337`) and answers 200, so rqlite prunes the change and never sends it again. The scratch test shows
   this with 2001 Sensors: the first valid delivery after a start got 200 with no subscription in place. Decision 4's
   boot gate exists for this case, and the scenario says "the events fire once the sources are loaded". The builder
   reads "loaded" as "readable from the metastore", which removes only the store window.
   `TestScenarioChangesWaitForSourcesAtBoot` cannot see the hole: `init` subscribes its recorder before it posts
   (`sql_changes_internal_test.go:58-65`), and no 503 is ever asserted. Fix, within Decision 4's wording: answer 503
   to every valid token until the controller's first pass over the Sensors (and EventSources) is done after a start,
   then serve. Extend the boot scenario with a delivery inside that window that gets 503 and fires after the load.

### Minor

1. **A commit's changes are held whole in memory** (`model`). `groups` decodes each group's changes into `pending`
   (`changefeed.go:296`, `:319`) and publishes only after the group's closing brace. Each change keeps up to 256 KiB of
   `before`/`after`, including changes of tables no event names. A commit of many large rows is therefore held whole,
   up to 256 KiB per row, which contradicts the doc comment at `changefeed.go:244-246`. The engine's own memory bound
   limits the size in practice. Fix: drop the rows of unmatched tables while decoding (copy into a nil buffer), and
   publish each matching change as it is decoded, since `index` precedes `events`. The watermark is still raised after
   the group.
2. **Under containerd without `catalog.proxyHost`, no engine reaches the feed** (`adr`). Decision 2 binds the feed on
   "the catalog proxies' publish host", and the code falls back to `127.0.0.1` (`pkg/funcd/funcd.go:1102`). A worker
   netns cannot reach that address (`internal/platform/config/config.go:304-310`). A containerd node that never set
   `proxyHost` because it runs no catalog still gets the endpoint env on every engine (Decision 1). Every engine then
   keeps every change in its on-disk queue, and nothing warns. Fix: a superseding note names the bridge gateway source
   when no `proxyHost` is set. Until then, log a warning at start when the runtime is containerd and `proxyHost` is
   empty.

### ✅ Verified correct (keep)

- **Decision 1**: the entrypoint writes `/data/epoch` once (128 random bits, hex) and reuses it. It writes
  `/tmp/cdc.json` with `endpoint`, `service_id`, `max_batch_size` 100 and `transmit_timeout` 10 s in integer
  nanoseconds, as rqlite 10.5.3 expects. It adds `-cdc-config` with `-cdc-as funcd`, the user rqlite needs for the
  high-watermark broadcast under `-auth`. The reconciler always sets the endpoint env, whatever EventSources exist.
- **Decision 2**: the token is HMAC-SHA256 of the node master over `funcd-sql-cdc/<ns>/<name>`, base64url, 43
  characters, distinct from the engine password. It is compared with `hmac.Equal` against every database's recomputed
  token. An unknown token is 404 before the body is read. Neither the path nor the token reaches a funcd log, and the
  engine does not log its endpoint (checked on the real image). The listener retries each second until the bridge
  address exists and warns once. `cmd/funcd` adds `<proxyHost>:<port>` to the internal allow list when isolation is on.
- **Decision 3**: the watermark is keyed `cdc/<ns>/<db>/<epoch>`. It is read and raised under one per-database mutex
  that is held from the source lookup to the response, so overlapping posts of one commit fire once
  (`TestFeedRedeliveryAndEpochs`, `TestScenarioRedeliveryFiresOnce`). It is raised after the group's events are
  published. It is deleted with the database in `deleted()`. It lives in the event store's Badger in both modes, and
  `TestChangeWatermarks` carries it through Snapshot and Load in all four mode pairs, including a database whose name
  extends another's.
- **Decision 4, holds**: a database that some EventSource names is 503 while the platform or its namespace is held. A
  database that none names is drained and acknowledged. Draining before the 200 is right: Go closes a connection
  whose large body is unread, and the engine would resend the batch for ever (`TestFeedDrainsUnwatched`, 20 MiB).
- **Decision 5**: the CloudEvent `type`, `source`, `subject` (new optional field), `id`
  `<ns>/<db>/<epoch>/<index>/<event>/<n>` and data `{op, table, rowId, before, after, index, commitTimestamp}` match
  the Decision. The decoder is a token stream, so a 9 MiB value fires truncated and the next commit still fires
  (`TestFeedTruncatesLargeRows`). The captured payload from the real engine decodes with blobs, nulls and floats
  intact. A missing database gives NotReady `DatabaseNotFound` and a recheck (`TestSQLSourceFollowsItsDatabase`). The
  `setBlobNotReady` → `setNotReady` rename is justified by the second caller. The existing eventing tests are
  unchanged.
- Types and validation: exactly one source kind, DNS-1123 database and event names, unique names, a non-empty table,
  `on` among the three values; `Normalize` defaults on a copy in the feed, so the user's spec is never rewritten.
- One `TestScenario…` per scenario, all `t.Parallel()` and fast. The boot scenario uses a short `os.MkdirTemp` data
  dir.
- No scope creep: no bus feed, no column filters, no change to Sensors.

### Definition of Done

11 / 12 items hold (the ADR's 6 Review-checklist items and 6 Definition-of-done items; the repo-wide gate runs once per
PR, and the touched packages' tests, vet, lint, the Linux build, `go mod verify` and `just check-hygiene` are green).
Miss: "the boot gate answers 503 to every valid token" (Major 1, `model`).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0235 (implementation) → changes-requested, 0/1/2, 2 model-attributed, DoD 11/12.
See `docs/reviews/model-scorecard.md`.

### Recommendation

Back to `adr-impl` for Major 1: a boot gate that answers 503 until the Sensors are subscribed after a start, and a
scenario test that posts inside that window. Minor 1 can go in the same rework. Minor 2 needs a short superseding note,
or a start-up warning as a stopgap. The re-review is that test, the `internal/services/sql` and `pkg/funcd` tests and
lint; then stamp ADR-0235 `Implemented` and F132 `implemented`.
