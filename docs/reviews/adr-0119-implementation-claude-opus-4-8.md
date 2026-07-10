# ADR-0119 implementation review — object-store EventSource (`blob:` kind, F83)

- **ADR**: [ADR-0119](../adr/0119-object-store-eventsource.md) — Object-store EventSource (reactive ingestion), FEAT-0003/F83
- **Phase**: implementation (ADR-0000 gate 5)
- **Producing model**: claude-opus-4-8
- **Reviewer**: adr-impl-review gate
- **Date**: 2026-07-10
- **Verdict**: **PASS** (advance to `Implemented` is the orchestrator's step; this gate records only)

## Verification (real exit codes, `nix develop -c`)

| Command | Exit |
|---|---|
| `go build ./...` | **0** |
| `go vet ./...` | **0** |
| `go tool golangci-lint run ./...` | **0** |
| `go test ./...` | 1 — **only** `TestPythonPoolSmoke` (`internal/testkit/bench`), the known pre-existing env flake (NOT attributed to this work) |
| `go mod verify` | **0** (all modules verified) |

`internal/eventing` and `api/types/v1alpha1` pass in full, including every ADR-0119 scenario test; `pkg/funcd`
(carrying the in-process e2e) passes (61s). No `go.mod`/`go.sum` change beyond the pre-existing ADR-0118 line
in the branch — this item adds **no** dependency (pure-Go over existing ports), as the ADR requires.

## Every Scenario → a named passing test

| Scenario | Test | Result |
|---|---|---|
| `object-created-emits-event` | `TestScenarioObjectCreatedEmitsEvent` (`blobwatch_test.go:84`) | PASS — one event; asserts `source`, `type`, and the full `data == {bucket,key,size,version,time}` |
| `external-s3-write-detected` | `TestScenarioExternalS3WriteDetected` (`blobwatch_test.go:176`) | PASS — writes `s3/lake/raw/drop/b.parquet` **straight into the shared gocloud substrate** via `blob.Prefixed(shared,"s3/<ns>/<bucket>/")` (the SAME view `s3BucketFor` builds), never through the watcher; the poll detects it over `List` alone — genuinely writer-agnostic, not a mock that trivially passes |
| `dedup-no-refire` | `TestScenarioDedupNoRefire` (`blobwatch_test.go:109`) | PASS — re-list twice, still one event |
| `prefix-scoped` | `TestScenarioPrefixScoped` (`blobwatch_test.go:127`) | PASS — `other/` never fires; only the in-prefix object does |
| `restart-no-replay` | `TestScenarioRestartNoReplay` (`blobwatch_test.go:149`) | PASS — a **fresh** watcher + `KVWatermark` over the **same** KV replays nothing |
| `eventsource-validate` | `TestScenarioEventSourceValidate` (`validate_test.go`) | PASS — both-set / empty-bucket / non-DNS-1123 bucket / no-events / dup-names / non-`Created`-`on` all rejected; empty `on` accepted (defaulted later); an explicit assertion that **`Validate` did not mutate** then `Normalize()` defaults `[Created]` |
| in-process e2e (drop→poll→Fanout→Sensor→run) | `TestScenarioBlobEventSourceEndToEnd` (`pkg/funcd/blob_eventsource_e2e_test.go:23`) | PASS — real running platform; a `Put` into `s3/default/raw/drop/a.parquet` (the external-write substrate key) drives EventSource-Ready → poll → publish → **Sensor** → `WorkflowRun ingest` Succeeds with `input.file == drop/a.parquet`; then asserts **dedup** (exactly one run after further polls) |
| tie-break (same-timestamp) | `TestBlobWatcherTieBreak` (`blobwatch_test.go:205`) | PASS — two objects at the newest ModTime both emit once; neither re-emits; a newer object advances the max |
| missing-bucket NotReady | `TestScenarioBlobSourceMissingBucketNotReady` (`eventing_test.go`) | PASS — the reconcile-side NotReady/BucketNotFound path |
| register/deregister | `TestBlobWatcherRegisterDeregister`, `TestScenarioDeregisterOnDelete` | PASS — prune-on-respec + delete |

## Conformance to Contracts (evidence-cited)

### ✅ Verified correct — the M1/M2/M3 judge folds are all honestly implemented

- **M2 — `data.version`, not `etag`.** `BlobEventData.Version string json:"version"` (`cloudevent.go:65`); the
  fingerprint is `versionOf` = `UnixNano(ModTime)+"-"+Size` (`blobwatch.go:220`) — an honest `(ModTime,Size)`
  pair, **never** named `etag`. All five `data` fields (`bucket,key,size,version,time`) present and asserted
  (`blobwatch_test.go:101-105`).
- **M3 — watermark over `internal/kvstore.KV`.** `KVWatermark` uses `kv.Get`/`kv.Put` of one JSON `Cursor` per
  `_eventing/blobwatch/<ns>/<source>/<event>` (`watermark.go:32,84-108`). **No** `store.Update(func(Txn))`
  anywhere. The `Cursor{MaxModTime,KeysAtMax}` new-object rule is correct and **bounded**: `advance`
  (`blobwatch.go:195`) resets `KeysAtMax` to only the keys **at** the (possibly advanced) max — it carries
  prior tie-keys **only** when `MaxModTime` is unchanged (`blobwatch.go:202-203`), so the tie-set never grows
  past the newest timestamp. `isNew` (`blobwatch.go:178`) applies `ModTime>max ∨ (==max ∧ key∉KeysAtMax)`.
- **M1 — one `List` seam over the real substrate view.** The watcher lists via `blobBucketLister` which wraps
  `s3BucketFor(c.blob, c.store)` (`funcd.go:539,1444-1457`) — the **identical** resolver the ADR-0080 S3
  frontend writes land in. Detection depends only on `blob.Bucket.List`; `external-s3-write-detected` proves
  it on a real gocloud bucket, not a stub.
- **`Validate` is pure.** `EventSource.Validate` (`eventsource.go:91`) delegates to `BlobSource.validate`
  (`eventsource.go:136`) which only reads — it rejects both-kinds-set, empty/non-DNS-1123 bucket, no/dup event
  names, and non-`Created` `on`, and **leaves an empty `on` untouched**. Defaulting lives in a separate
  `Normalize()` (`eventsource.go:74`), called in `reconcileBlob` (`eventing.go` decode step), not in `Validate`
  — with a test asserting the non-mutation directly.
- **Missing `Bucket` ⇒ NotReady + requeue.** `reconcileBlob` (`eventing.go:126`) resolves the Bucket via
  `store.Get(KindBucket…)`; on `fault.NotFound` it deregisters, sets `Ready=False/BucketNotFound`
  (Route-`BackendNotFound` pattern), and returns `RequeueAfter: 15s` — never Ready-but-silently-not-polling.
- **Sensor unchanged.** `git status` shows **no** file under `internal/sensor/` touched by this item; the blob
  CloudEvent flows the existing `NewBlobEvent → Fanout(ParseSourceURI+type) → Sensor` path, and the Sensor
  projects `${{ event.data.key }}` (asserted end-to-end in the e2e). ADR-0108 `NewNamedEvent`/Fanout are
  untouched — `NewBlobEvent` is an additive companion sharing the same envelope.
- **Union grew additively.** `EventSourceSpec` gained `Blob *BlobSource` beside `Timer` (`eventsource.go:21-28`);
  the timer path, `TimerSource`, and its validation are byte-for-byte unchanged.
- **Wiring + lifecycle.** `BlobWatcher.Run` is started as a side goroutine in `Platform.Run`
  (`funcd.go:888-896`), draining on ctx-cancel exactly beside the timer `Source.Run`; `WithBlobPollInterval`
  (default 15s) + a `FUNCD_EVENTING_BLOB_POLL_INTERVAL` config knob (`options.go`, `config.go`, `main.go`).
- **Conventions.** No `any` in exported/port signatures (`BucketLister`/`Watermark`/`BlobWatcher` are typed);
  `api/fault` for every error; ctx-first; `slog` structured logging; one-file-per-driver
  (`watermark.go`, `blobwatch.go`, `cloudevent.go`). OpenAPI regenerated (`BlobSource`/`BlobEvent` schemas
  present, `funcd.v1alpha1.yaml:34,62,493`). F83 feat row advanced to `reviewing`.

### Minor

- **[Minor · model]** `reconcileBlob` calls `es.Normalize()` and then, on the not-yet-Ready path, persists the
  object via `store.Update(ctx, es)` (`eventing.go` Ready branch) — so the controller writes the **defaulted**
  `on:[Created]` back into the stored spec on first reconcile. It is idempotent (settles after one write, no
  reconcile loop, `Created` is the only legal value) and matches the ADR's "default in decode/normalize"
  intent, but it is a mild controller-mutates-spec nuance rather than a pure read-time default. No functional
  impact; noted for the record.

## Venom lane — deferral is ACCEPTABLE (scope/env, not `model`)

The impl **authored** the reactive-ingestion case: `e2e/s3.venom.yml` gains *"an external S3 write into gold/
reactively starts an ingest WorkflowRun"* — it reuses the lane's existing s3-roundtrip **external SigV4 PUT**
into `gold/`, polls `funcdctl get workflowrun` (retry-as-wait, 20×3s covering the 3s poll), and asserts an
`ingest` run exists with `key=gold/…`. The trio of manifests (`ingest-workflow.yaml` builtin-`pass`,
`drop-source.yaml` blob EventSource with correctly-quoted `"on":`, `drop-sensor.yaml`) is added to
`scripts/lanes.yaml` apply-order, and `funcdconfig.yaml` sets `eventing.blobPollInterval: 3s`. The manifests and
lane are well-formed and map exactly to the reactive-ingestion scenario.

The **real** containerd/colima run is deferred (not feasible headless), relying on the in-process e2e —
identical to the ADR-0118 DLQ precedent. That in-process e2e (`TestScenarioBlobEventSourceEndToEnd`) genuinely
exercises the **full** drop → poll → Fanout → Sensor → `WorkflowRun` path on a real running platform, including
the projected key and dedup. The authored Venom case adds only the on-real-containerd/real-SigV4 proof, which
the in-process e2e substitutes without loss of coverage this ADR needs. **Deferral accepted; attributed to
scope/env, not `model`.**

## Definition of Done (ADR Review-checklist, 7 items)

1. `EventSourceSpec` gains `Blob` union member; timer path untouched — **PASS**
2. `Validate` rejects blob+timer both / empty bucket / dup+invalid names; `on` defaults `[Created]` in
   normalize (not `Validate`); rejects non-`Created`; unknown keys 422 at the schema edge — **PASS**
3. `BlobWatcher` polls each Ready source, `List`s, publishes **one** CloudEvent per new object with the full
   `data` shape through the existing Publisher — **PASS**
4. Detection is polling over `blob.Bucket.List` reusing ADR-0080 resolution; external write detected by a
   List-only test — **PASS**
5. Missing `Bucket` ⇒ NotReady+condition (+requeue); `on` defaults in decode, not `Validate` — **PASS**
6. `Watermark` suppresses re-list + prevents restart replay; bounded cursor over `kvstore.KV` — **PASS**
7. Flows Publisher→Fanout→Sensor with no ADR-0108/0109 change; `${{ event.data.key }}` projection; DLQ defers
   to ADR-0118; `Created`-only / `(ModTime,Size)` version / poll-latency documented; no `go.mod` change; no
   leak; OpenAPI regenerated — **PASS**

**DoD: 7 / 7 passed.** (The prose DoD's "S3 Venom lane green" is a justified env deferral per above.)

## Identity / path hygiene

No absolute OS path, local username, or personal email appears in any file changed by this item. (Silent
no-leak check: nothing to raise.)

## Verdict

**PASS.** The implementation faithfully realizes ADR-0119's Contracts and Scenarios: the union grows
additively, the emit seam and Sensor are reused unchanged, and the three judge folds (M1 one-`List`-seam over
the real substrate view, M2 `data.version` not `etag`, M3 watermark over `kvstore.KV`) are all honestly and
correctly implemented and tested. Every scenario has a named passing test; the bounded-cursor dedup and
restart-no-replay are proven over a persisted KV; the in-process e2e drives the full drop→run path. The sole
`go test` failure is the excluded env flake. One negligible Minor (model), zero Majors, zero Blockers.
