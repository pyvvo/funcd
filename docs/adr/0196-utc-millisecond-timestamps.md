# ADR-0196: One timestamp form — RFC3339 UTC with milliseconds

- **Status**: Accepted (2026-10-07)
- **Superseded in part by**: [ADR-0197](0197-log-durations-in-ms.md) (2026-10-07) — the logger hook's name: `replaceAttr` is exported as `observability.ReplaceAttr` (still unexported outside the module), shared by both ADRs' branches.
- **Date**: 2026-10-07
- **Deciders**: green-0-rabbit
- **Tags**: api, types, openapi, observability, logging, eventing, funcdctl, breaking-change
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; joined as a further entry)
- **Supersedes (in part)** (back-links `Superseded in part by: ADR-0196` added at acceptance; lines at ec274537):
  [ADR-0100](0100-run-step-lineage.md) Decision 3, "`RunStepStatus` gains `StartedAt`, `EndedAt int64`" (113–114), and
  its Contracts comment (150); `runstate.StepState` keeps its int64 fields (145–147).
  The `time.Time` field types in [ADR-0003](0003-resource-model-and-api-typing.md) (331–332, 430),
  [ADR-0023](0023-eventing-core.md) (185), [ADR-0084](0084-funclog-read-funcdctl-logs.md) (154),
  [ADR-0118](0118-eventing-dead-letter-queue.md) (229), [ADR-0119](0119-object-store-eventsource.md) (255),
  [ADR-0121](0121-declarative-referential-integrity-admission.md) (89) and ADR-0143 plan step 3 (278).
  [ADR-0048](0048-dto-validation-reference.md) row 100, "server-set; ignored on input": a value not in the form is
  now refused. The [ADR-0143](0143-redeploy-by-revision-switch.md) example `drainingSince: "2026-10-02T09:30:00Z"`
  (259) reads `"2026-10-02T09:30:00.000Z"`.
- **Relates to**: [ADR-0009](0009-observability-logger-root.md) (gains a `ReplaceAttr` hook), ADR-0194 (durations; the
  same clean break), ADR-0195 (reads v0.7.3 backup manifests), ADR-0197 (the duration branch of the same hook).
  **Conforms to** [ADR-0182](0182-timer-schedule-anchored-on-creation.md) (a millisecond-aligned grid anchor) and
  [ADR-0146](0146-workflowrun-drive-model.md). **Blueprint**: no change; it fixes no timestamp form.

## Context & Need

funcd writes an instant in three forms (#821). WorkflowRun `status.steps[].startedAt`/`endedAt` are int64 unix
nanoseconds (`api/types/v1alpha1/workflowrun.go:111-112`). Every other instant is a `time.Time` written as
RFC3339Nano: 6 fractional digits on macOS, up to 9 on Linux, trailing zeros trimmed. `lastTransitionTime`
(`status.go:39`, `:47`) and `drainingSince` (`internal/function/function.go:1491`) are stamped with the host's local
offset (`+02:00` beside a `Z` in the same object), and the JSON store keeps that offset (`internal/store/store.go:569`,
`:597`). The kv-migration ConfigMap holds whole seconds (`internal/workflow/kvmigration.go:68`), daemon log lines carry
the local offset, and a zero `time.Time` is written as `0001-01-01T00:00:00Z` in request bodies.

Purpose: one `Timestamp` type is the only form an instant takes in funcd's own JSON and log lines, so every reader
parses one fixed-length string. Callers: `funcdctl`, `pkg/sdk`, raw HTTP clients, Sensor expressions and log readers.

## Scenarios

- **scenario: step-times-are-timestamps** — Given a WorkflowRun whose step `a` succeeded, When
  `funcdctl get workflowrun <run> -o json` reads it, Then `status.steps[0].startedAt` and `endedAt` are strings such
  as `"2026-10-07T20:03:35.965Z"` with `endedAt` not before `startedAt`, `funcdctl workflow describe` prints the step's
  duration, and a Pending step carries neither key.
- **scenario: every-timestamp-utc-millisecond** — Given a Function with a condition and a drain, an Invocation, a
  WorkflowRun, a DLQ record, a log-read line, a timer and a blob CloudEvent, a KV backup manifest and the kv-migration
  ConfigMap, When each is read, Then every timestamp matches `TimestampPattern`; a value stamped at
  `20:03:35.965999999` reads `20:03:35.965Z`.
- **scenario: local-clock-stamps-utc** — Given a daemon whose local zone is UTC+2, When a condition changes and a
  Function switches revision, Then `lastTransitionTime` and `drainingSince` end in `Z` and name the same instant.
- **scenario: zero-time-omitted** — Given a typed object with no `creationTimestamp`, as `funcdctl apply` and
  `pkg/sdk` build it, When it is marshalled, Then the body has no `creationTimestamp` key; no unset timestamp is
  written as `0001-01-01T00:00:00Z`.
- **scenario: log-line-time-fixed-form** — Given the daemon logger in JSON format, in text format and the audit
  stream, When a record is written on a UTC+2 host, Then its `time` is `"time":"2026-10-07T20:03:35.965Z"` or
  `time=2026-10-07T20:03:35.965Z`.
- **scenario: non-fixed-input-refused** — Given a raw HTTP create body whose `metadata.creationTimestamp` is
  `"2026-10-07T20:03:35Z"`, `"2026-10-07T22:03:35.965+02:00"` or `"2026-10-07T20:03:35.965123Z"`, When it is sent,
  Then the server answers 422 at `body.metadata.creationTimestamp` naming the form and stores nothing;
  `sdk.DecodeManifest` refuses the same manifest. The fixed form is accepted (and, being server-set, ignored).

## Scope

**In**: the form, the type, its codec and schema; every field in the Contracts table and its stamp site; the time of
every daemon and audit log line; the kv-migration `COMPLETED_AT` string; `funcdctl` output; the OpenAPI file.
**Out**: formats a standard fixes (HTTP `Date`/`Last-Modified`, S3 XML `LastModified`/`CreationDate`, OTLP
`timeUnixNano`, the Parquet `ts` column, the OCI `org.opencontainers.image.created` annotation); the shim-to-daemon
NDJSON `ts`/`start`/`end` integers; opaque strings that embed an instant (blob `data.version`, the static-site ETag,
object keys, the DLQ ULID `id`, the ADR-0157 `SeenList` versions); runstate int64 fields, which no client reads; the
`time` of an externally posted CloudEvent, which funcd passes through unparsed (`internal/dataplane/normalize.go:46-50`);
the logs `since` parameter (#824); the `funcdctl dev` clock column (`cmd/funcdctl/dev.go:300`, an in-process value);
stored-data conversion. The log hook rewrites only a line's own top-level `time`: a time-valued attribute (none today)
and the `slog.Default()` line written before the logger exists (`cmd/funcd/main.go:58`) keep the old form.

## Constraints & Decision drivers

- The decider fixed (#821, 2026-10-07): RFC3339 in UTC with a `Z` suffix and exactly 3 fractional digits; every
  timestamp in funcd's own JSON and log lines; standards keep their format; no legacy form (the #816 clean break).
  At acceptance (2026-10-07) the decider also fixed truncation, strict decoding with the manifest exception, and the
  `funcdctl` output (Decisions 2, 4, 10 and 11).
- One source for the form: the pattern the schema publishes is the pattern the decoder applies (precedent `DNSLabel`,
  `api/types/v1alpha1/ids.go:16-22`; ADR-0194 `DurationPattern`). No new dependency.
- `internal/platform/**` may import `api/**`; `api/**` imports no `internal/**` (`.golangci.yml` depguard).
- Probe (Go 1.26.4, scratch program): `.000` truncates (`…35.965999999` → `…35.965Z`); `omitzero` uses `IsZero`; an
  unquoted fixed-form YAML scalar reaches `UnmarshalJSON` as a string; a string `time` from `ReplaceAttr` prints as shown.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| Keep `time.Time` (RFC3339Nano) | no change | the defect in #821 | rejected |
| Stamp `UTC().Truncate(ms)`, keep `time.Time` | no type change | RFC3339Nano trims zeros (`.960` → `.96`); no fixed length | rejected |
| Struct wrapper `struct{ time.Time }` (Kubernetes `metav1.Time`) | promoted methods, fewer conversions | also promotes `MarshalText`, `AppendFormat`, `GobEncode`, which write other forms (slog's text handler uses `MarshalText`) | rejected |
| Named type `type Timestamp time.Time` | only the methods it defines; the ADR-0194 pattern | a conversion wherever Go code computes with a field | **chosen** |
| Round to the nearest millisecond | nearest value | can move a stamp past `now` or into the next second; Go layouts and slog truncate | rejected by the decider |
| Lenient decode (any RFC3339), strict schema only | v0.7.x data loads | the decider chose no legacy form; `pkg/sdk` would accept other forms silently | rejected by the decider, except the KV backup manifest |
| Unix-millisecond integers | no parsing | the decider chose timestamps like the other API fields | rejected |
| Change runstate `StartedAt`/`EndedAt` to `Timestamp` | one type everywhere | a persisted Badger record and the timeout arithmetic (`engine.go:1623`) change for no wire gain | rejected; convert at the mirror |

## Decision

1. **The form.** `YYYY-MM-DDTHH:MM:SS.sssZ`: `TimestampLayout` (`2006-01-02T15:04:05.000Z07:00`) applied to a UTC time,
   always 24 characters, years 0000–9999. `TimestampPattern` is its RE2 form, the single source for decoder and schema.
2. **One type.** `v1alpha1.Timestamp` is `type Timestamp time.Time`. `NewTimestamp(t)` returns
   `t.UTC().Truncate(time.Millisecond)`: the decider chose truncation over rounding. Every stamp site goes through it,
   so the value in memory equals the one a reader decodes.
3. **Encoding.** `MarshalJSON` and `String` write the form, applying UTC and truncation again, so a converted value
   cannot escape it; a year outside 0000–9999 is an error.
4. **Decoding.** `UnmarshalJSON` accepts only a JSON string that matches `TimestampPattern`, then `time.Parse` with
   `TimestampLayout` (which refuses a month 13 or a second 60); `null` leaves the value unchanged; anything else,
   another precision or an offset included, is `fault.Invalid` naming the form. As the decider decided, stored
   metastore, DLQ and CloudEvent payload records use the same codec; nothing reads the old forms.
5. **Zero.** Each optional value field takes `omitzero`; `deletionTimestamp` and `drainingSince` stay pointers with
   `omitempty`. Required fields (`failedAt`, log `time`, CloudEvent `time`, blob `data.time`) are always stamped.
6. **Schema.** `Timestamp.Schema` publishes `type: string`, `format: date-time`, `pattern: TimestampPattern` and a
   `patternDescription`, as a new `*huma.Schema` on each call (huma sets per-field `Description` and `Nullable` on it).
   huma consults a `SchemaProvider` before its `time.Time` case (v2.38.0 `schema.go:770-781`), so a body value off the
   pattern gets 422 before decoding.
7. **Stamp sites** convert as listed in the Contracts table; `lastTransitionTime` and `drainingSince` become UTC
   there. The log read filters and sorts on the nanosecond value (`logread.go:138-147`), then builds `Line.Time`, so
   lines within one millisecond keep their order. The backup prefix keeps `started.UnixNano()` (`backup.go:150`, #808).
8. **Step times.** `runstate.StepState` keeps int64 nanoseconds. The mirror (`reconcile_run.go:667`) converts with
   `stepTime`: 0 gives a zero `Timestamp` (omitted), any other value `NewTimestamp(time.Unix(0, ns))`.
9. **Log lines.** `replaceAttr` in `internal/platform/observability/logger.go` (the name ADR-0197 shares) rewrites the
   built-in time (`len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime`) to
   `slog.String(slog.TimeKey, v1.NewTimestamp(a.Value.Time()).String())`. `NewLogger` sets it on both handlers
   (`logger.go:83`), and `NewAuditRecorder` on its handler (`audit.go:53`). This ADR owns the `time` branch, ADR-0197
   the `slog.KindDuration` branch; the ADR implemented first adds the hook.
10. **KV backup manifest.** funcd writes `at` in the form; reading it accepts any RFC3339 value. The manifest is a
    storage record in the backup target, not API input; v0.7.3 manifests hold RFC3339Nano, ADR-0195 reads them, and a
    strict read fails `loadManifest` (`backup.go:313`), and with it Ship, Restore and `untilRebaseline` fail. A
    local `manifestTime` type does this; the decider made it the one exception to Decision 4.
11. **funcdctl.** `get -o json` and `describe` print `MarshalJSON` output. `workflow describe` prints `endedAt −
    startedAt`, with ADR-0194's formatter once it lands (both edit `cmd/funcdctl/workflow.go:338-339`).
    As the decider decided, `funcdctl logs` text prints `l.Time.String()` instead of whole seconds (`logs.go:69`),
    and `funcdctl dev` keeps `15:04:05` (`dev.go:300`).

## Temporary workarounds

None.

## Contracts

```go
package v1alpha1 // api/types/v1alpha1/timestamp.go

// TimestampLayout is the one timestamp form (ADR-0196), applied to a UTC time.
const TimestampLayout = "2006-01-02T15:04:05.000Z07:00"

// TimestampPattern is TimestampLayout's RE2 form: the single source for UnmarshalJSON and the OpenAPI schema.
const TimestampPattern = `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$`

// Timestamp is an instant: a time.Time in memory, TimestampLayout in UTC on the wire (ADR-0196).
type Timestamp time.Time

// NewTimestamp returns t in UTC, truncated to the millisecond.
func NewTimestamp(t time.Time) Timestamp
func (t Timestamp) IsZero() bool
func (t Timestamp) String() string
func (t Timestamp) MarshalJSON() ([]byte, error)
func (t *Timestamp) UnmarshalJSON(b []byte) error
func (Timestamp) Schema(huma.Registry) *huma.Schema // a new schema on each call
```

```go
// internal/workflow/reconcile_run.go
func stepTime(ns int64) v1.Timestamp // 0 → zero; else v1.NewTimestamp(time.Unix(0, ns))

// internal/platform/observability/logger.go (ADR-0197 adds its branch to the same function)
func replaceAttr(groups []string, a slog.Attr) slog.Attr

// internal/kvstore/badger/backup.go: MarshalJSON as Timestamp; UnmarshalJSON takes any RFC3339 (time.Time's
// decoder), then NewTimestamp; IsZero for omitzero.
type manifestTime v1.Timestamp
```

| Field (file:line at ec274537) | Go type now → new | Stamp site → new |
|---|---|---|
| `metadata.creationTimestamp` (`metadata.go:145`) | `time.Time` → `Timestamp`, `omitzero` | `store.go:331` `NewTimestamp(time.Now())` |
| `metadata.deletionTimestamp` (`metadata.go:146`) | `*time.Time` → `*Timestamp` | none sets it (`handlers.go:287-290`) |
| `conditions[].lastTransitionTime` (`status.go:24`) | `time.Time` → `Timestamp`, `omitzero` | `status.go:39`, `:47` `NewTimestamp(time.Now())` |
| Function `status.drainingSince` (`function.go:183`) | `*time.Time` → `*Timestamp` | `function.go:1491` `NewTimestamp(r.clock.Now())` |
| Invocation `status.startTime`/`endTime` (`invocation.go:17-18`) | `time.Time` → `Timestamp`, `omitzero` | `sensor.go:491-492` |
| WorkflowRun `status.steps[].startedAt`/`endedAt` (`workflowrun.go:111-112`) | `int64` → `Timestamp`, `omitzero` | `reconcile_run.go:667` `stepTime` |
| DLQ `failedAt` (`deadletter.go:36`) | `time.Time` → `Timestamp` (required) | `sensor.go:353`, `:411` |
| log-read `time` (`logread.go:36`) | `time.Time` → `Timestamp` (required) | `logread.go:138` |
| CloudEvent `time` (`cloudevent.go:34`) | `time.Time` → `Timestamp` (required) | `cloudevent.go:52`, `:87` |
| blob event `data.time` (`cloudevent.go:67`) | `time.Time` → `Timestamp` (required) | `blobwatch.go:283` |
| KV backup manifest `at` (`backup.go:70`) | `time.Time` → `manifestTime`, `omitzero` | `backup.go:159` `manifestTime(NewTimestamp(started))` |
| ConfigMap `COMPLETED_AT` (`kvmigration.go:68`) | `string` (unchanged type) | `NewTimestamp(time.Now()).String()` |

| Direction | What |
|---|---|
| Consumes | `regexp`, `time` (stdlib); huma `SchemaProvider` (precedent `ids.go:51-60`); no new dependency |
| Exposes | `Timestamp`, `NewTimestamp`, `TimestampLayout`, `TimestampPattern` to `internal/**`, `pkg/**`, `cmd/**` |
| Wire and schema | a 24-character JSON string; OpenAPI `type: string`, `format: date-time`, `pattern`, `patternDescription` |
| Logs | daemon (JSON, text) and audit `time` in the form, via `replaceAttr` |

## Implementation plan

1. `api/types/v1alpha1/timestamp.go` and `timestamp_test.go` (the Contracts block).
2. Change the fields and stamp sites in the Contracts table, `stepTime`, `manifestTime` and `replaceAttr`; the log read
   sorts on nanoseconds before it builds `Line`.
3. Every site the compiler reports converts with `time.Time(x)`.
4. `just generate` regenerates `api/openapi/funcd.v1alpha1.yaml` (`justfile:340`; drift test
   `internal/controlplane/api_test.go:141`).
5. The PR carries `Fixes #821`, `!` and `BREAKING CHANGE:`. No language-repo change: neither shim parses an instant.

**Test plan** — one test per scenario, named after it, plus the contract tests:

| Scenario | Where |
|---|---|
| step-times-are-timestamps | `internal/workflow` (mirror over the fake dispatcher); `cmd/funcdctl` describe render |
| every-timestamp-utc-millisecond | each package's marshal test (`v1alpha1`, `deadletter`, `logread`, `eventing`, `badger` backup, `kvmigration`) with a `…965999999` clock |
| local-clock-stamps-utc | `Conditions.Set` with `time.Local` at UTC+2 (not parallel); `internal/function` drain with a UTC+2 `clock.Fake` |
| zero-time-omitted, log-line-time-fixed-form | `v1alpha1` marshal of zero objects and the `pkg/sdk` apply body; the logger (JSON, text) and audit recorder on a buffer |
| non-fixed-input-refused | humatest bodies as `TestSchemaRejectsInvalidDTO` (`api_test.go:216`); `sdk.DecodeManifest` |
| contract (`timestamp_test.go`) | `NewTimestamp` truncation and zone; `MarshalJSON` for years 0 and 9999 and the 10000 error; `UnmarshalJSON` table (fixed form; no fraction, 6 digits, offset, lowercase `z`, number, month 13 refused; `null`); pattern and parser agree; `Schema` |
| manifest read (Decision 10) | `badger` backup: a v0.7.3 RFC3339Nano `at` loads; a write is in the form |
| Decisions 7 and 11 | `logread`: two objects whose rows fall in one millisecond in reverse object order keep time order; `cmd/funcdctl/logs_test.go`: the text time is in the form |

**Definition of done**: `just ci` green; no `time.Time`, `*time.Time` or int64 instant field with a JSON tag remains
in `api/types/v1alpha1`, `deadletter`, `logread`, `eventing/cloudevent.go` or the backup `segment`; the generated spec
has no integer instant and every `date-time` carries the pattern; `grep -rn 'time.RFC3339' --include='*.go'` outside
tests finds only `artifact.go:117` (OCI) and `controlplane/logs.go:308` (`since`). Window assertions truncate their
lower bound to the millisecond (`internal/store/store_test.go:350`).

## Review checklist

- [ ] `TimestampPattern` is the only copy of the form; `UnmarshalJSON` and `Schema` use it; the table test holds.
- [ ] Every stamp site in the table calls `NewTimestamp`; optional timestamps are `omitzero` or nil-omitted pointers.
- [ ] `replaceAttr` holds the `time` branch and is set on the text, JSON and audit handlers.
- [ ] Runstate keeps int64; `stepTime` maps 0 to an omitted field.
- [ ] The manifest decodes RFC3339Nano and writes the form; every other decoder refuses non-fixed input.
- [ ] Every scenario has a same-name test; the spec is regenerated.

## Consequences

- (+) One fixed-length form in every object, event and log line; text order equals time order.
- (−) A v0.7.x data dir, DLQ record or stored CloudEvent payload no longer decodes, and old clients cannot read new
  objects, or the reverse. Accepted: no installation runs funcd.
- (−) Sub-millisecond detail leaves the wire; instants within one millisecond compare equal (`adoptableAtUpgrade`,
  `kvmigration.go:110`; the order of `workflow list`); two log lines in one millisecond keep their order.

## Open questions

None.

## References

- Issue #821 and the decider's comments of 2026-10-07; the read-only sweep of every instant funcd exposes behind them.
- huma v2.38.0 `schema.go:770-781`, `validate.go:204-215`; RFC 3339; Go `time` layouts; Kubernetes `metav1.Time`.
