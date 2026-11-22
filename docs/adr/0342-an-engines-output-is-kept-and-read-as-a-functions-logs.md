# ADR-0342: An engine's output is kept and read as a Function's logs

- **Status**: Proposed (2026-11-22)
- **Date**: 2026-11-22
- **Deciders**: green-0-rabbit
- **Tags**: observability, logs, engines, addons, lake, sql
- **Realizes**: [FEAT-0020/F267](../feat/0020-feat-engine-logs.md) (an engine's logs are read as a Function's)
- **Relates to**: [ADR-0081](0081-function-log-capture-side-channel-blob.md) (funclog's OTLP segments),
  [ADR-0083](0083-funclog-compaction-otlp-jsonl-to-parquet.md) (compaction and retention),
  [ADR-0084](0084-funclog-read-funcdctl-logs.md) (the logs read and its tenant scope),
  [ADR-0168](0168-raw-output-pipes-and-record-bound.md) (raw output and the record bound),
  [ADR-0276](0276-a-lake-shows-when-its-catalog-saves-fail.md) Decision 4 (an engine's output in the daemon log,
  kept) and its Scope (funclog storage of engine output, left out there and decided here),
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (add-on kinds served at their plural),
  [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md) (dev's add-on containers)

## Context & Need

funcd runs an engine for each add-on object, Lake and SQLDatabase. The output capture sends every line an engine writes
to funcd's own log only (`pkg/funcd/executor.go:239`, ADR-0276 Decision 4); a Function's lines go to funclog, which
keeps them as OpenTelemetry log records and serves them to `funcdctl logs` (ADR-0081, ADR-0084). Under `funcdctl dev`,
an add-on's engine writes into its Docker container, where funcd reads nothing (ADR-0334). So a developer whose
IdentityProvider stays `EngineNotReady`, or whose Lake refuses a query, cannot read the engine's reason without the
host. Purpose: whoever may read an object reads its engine's output with `funcdctl logs`, kept and bounded as a
Function's logs are. The decider chose logs first and metrics later, on the same identity (FEAT-0020 F268).

## Scenarios

- `scenario: engine-logs-addon-object` — Given IdentityProvider `login` in `default`, whose engine writes a line to
  stderr, When a developer bound to `default` runs `funcdctl logs identityprovider/login -n default` once the segment
  is sealed, Then the line shows at INFO with source `stderr`, and funcd's own log still holds its `engine output`
  record.
- `scenario: engine-logs-lake-and-sql` — Given Lake `sales` and SQLDatabase `orders`, When `funcdctl logs lake/sales`
  and `funcdctl logs sqldatabase/orders` run, Then each shows its own engine's lines only.
- `scenario: engine-logs-scope` — Given a developer bound to `team-a` only, When they read
  `identityprovider/login -n default`, Then 403; an admin reads it.
- `scenario: engine-logs-shared-name` — Given Function `login` and IdentityProvider `login` in one namespace, When
  `funcdctl logs login` and `funcdctl logs identityprovider/login` run, Then each shows only its own lines.
- `scenario: engine-logs-no-engine` — Given the settings kind `RauthyPasswordPolicy`, or a kind no Addon registers,
  When `funcdctl logs rauthypasswordpolicy/strict` runs, Then 404.
- `scenario: engine-logs-funclog-off` — Given `funclog.enabled: false`, When an engine writes a line, Then
  `funcdctl logs <kind>/<name>` shows nothing new, and funcd's own log holds the line.
- `scenario: engine-logs-dev` — Given `funcdctl dev` runs IdentityProvider `login`'s engine in Docker, When the engine
  writes a line, Then dev's terminal shows it as an `engine output` line, and `funcdctl logs identityprovider/login`
  against dev returns it.

## Scope

**In**: the stdout and stderr of every engine worker funcd runs for an object (add-on objects, Lakes, SQLDatabases),
kept by funclog; a logs route per engine kind; `funcdctl logs <kind>/<name>`; dev's add-on containers.
**Out**: metrics and traces of engines (F268); sending any log to `telemetry.endpoint`; settings kinds' objects (no
engine); a change to the daemon log copy (ADR-0276 Decision 4 stays); `funcdctl logs` flags beyond those it has.

## Constraints & Decision drivers

- One path for every engine kind: each engine worker carries its owner kind (`pkg/funcd/addon_provider.go:86`,
  `pkg/funcd/binding_providers.go:479`, `:647`) and meets one output hook.
- One store: funclog's records, segments, record bound, compaction and retention, no second log system.
- The read rule of a Function's logs: authorized at query time in the path's namespace (ADR-0084).
- No engine key may meet a Function's: both live in one namespace prefix.
- F268's metrics reuse the identity chosen here.

## Alternatives considered

- **Do nothing, document `journalctl` and `docker logs`**: a developer without the host reads nothing. Lost on the
  purpose.
- **Read funcd's own log through the API**: funcd writes its log to stdout and keeps none; a reader would need a
  buffer lost at each restart, and the daemon log has no tenant scope. Rejected.
- **A prefix of its own (`engines/<ns>/<kind>/<name>/…`)**: a second layout for the compactor and the reader to list.
  Rejected for an owner segment under `logs/<ns>/` that no Function name can take.
- **stderr at ERROR, as a Function's raw stderr (ADR-0168)**: engines write routine lines to stderr (ADR-0276
  Decision 4), so `--severity error` would show them all. Rejected.
- **The engine pushes OTLP itself (`OTEL_EXPORTER_OTLP_ENDPOINT` to a funcd receiver)**: only engines with an
  OpenTelemetry SDK, and an engine's egress to funcd. Kept for F268's metrics, which need such a path anyway.

## Decision

1. **An engine's lines are funclog records.** Whether or not funclog runs, the output capture still sends each line an
   engine worker writes to funcd's log (ADR-0276 Decision 4). When funclog runs, it also appends the line to funclog's
   logs sink as a raw record, as a Function's raw output (ADR-0168), with its source `stdout` or `stderr` and the
   severity INFO for both streams. The record bound (`funclog.maxRecordBytes`) and the output queue's drop (ADR-0168)
   apply as to a Function.
2. **The identity.** An engine record's resource holds `namespace`, `kind` (the kind its object is served as: `Lake`,
   `SQLDatabase`, or the add-on's kind, such as `IdentityProvider`), `name` and `replica`; `kind` and `name` are new
   keys, and F268's metrics carry the same four. Its segments live at `logs/<ns>/<plural>.<name>/<date>/…`, where
   `<plural>` is the kind in lower case with an `s` (`v1.AddonPlural`): an object name is a DNS label with no dot, so
   no Function's segment `logs/<ns>/<fn>/` can be the same. Compaction (ADR-0083) and retention treat the segment as a
   Function's; the compacted row gains `kind` and `name`, empty on a Function's rows.
3. **The read.** `GET /apis/funcd.io/v1alpha1/namespaces/{namespace}/{plural}/{name}/logs` serves `lakes`,
   `sqldatabases` and each add-on kind's plural, with the Function route's query (`since`, `severity`, `limit`) and
   answers. It authorizes before reading, as a read of the object: `get` on `Lake`, `SQLDatabase` or `AddonObject` in
   the namespace (an admin in any namespace, a developer or a viewer in a bound one), else 403. It reads what is
   stored, so an object's logs outlive it until retention, as a deleted Function's do. A plural no Addon registers, and
   a settings kind's, are 404.
4. **The command.** `funcdctl logs <kind>/<name>` reads an engine's logs, the kind in any case (`identityprovider`,
   `IdentityProvider`, `lake`, `sqldatabase`), with the flags of `funcdctl logs`. A bare name is a Function, as
   before.
5. **Dev.** Under `funcdctl dev`, the Docker and nerdctl providers follow each engine container's stdout and stderr
   from its start and hand them to the same capture, so dev's terminal shows each line as an `engine output` line
   and dev's funclog keeps it.

## Design model

None: the decision adds no lifecycle, race, timer or conditional write. Lines take the capture and funclog's sink as
they are; a stream ends with its worker or its container.

## Behavior rows

- **workers.md**: "A line an engine writes" (in funcd's log at once, in `funcdctl logs <kind>/<name>` once its segment
  is sealed, `funclog.segmentMaxAge` 10 s), `decided`.
- **funcdctl.md**: "`funcdctl logs <kind>/<name>`" (a bare name is a Function; an unknown or engine-less kind is
  404), `decided`.
- **funcdctl-dev.md**: "An add-on engine's output under dev" (dev's terminal and funclog), `decided`.

## Temporary workarounds

None.

## Contracts

```go
// internal/funclog
type Resource struct {
	Namespace string
	Function  string  // a Function's name; "" for an engine
	Kind      string  // new: the kind an engine's object is served as; "" for a Function
	Name      string  // new: the engine's object
	Replica   string
	Tenant    string
}

// internal/funclog/logread
type Query struct {
	Namespace string
	Function  string
	Kind      string // new: with Name, reads logs/<ns>/<plural>.<name>/; set only when Function is ""
	Name      string // new
	// Since, MinSeverityNumber, Limit, TraceID: unchanged.
}

// internal/runtime
type WorkerSpec struct {
	// ...unchanged fields...
	ServedKind v1alpha1.Kind // new: the kind its object is served as, when it is not OwnerKind (an add-on object's kind)
}

// internal/provider
type ProviderSpec struct {
	// ...unchanged fields...
	ServedKind v1.Kind // new: copied into each engine's WorkerSpec; the add-on provider sets the object's kind
}

// pkg/sdk
func (c *Client) EngineLogs(ctx context.Context, ns v1.NamespaceName, kind v1.Kind, name v1.ObjectName,
	opts LogsOptions) ([]logread.Line, error)
```

dev's providers (`internal/provider/docker`, `internal/provider/nerdctl`) implement `runtime.OutputCapturer`; the
add-on provider sets the executor's capture on them as on a runtime driver.

| Dependency or output | What |
|---|---|
| Consumes | the output capture (`runtime.OutputCapturer`), funclog's logs sink, `funclog.enabled`, `funclog.maxRecordBytes`, `funclog.segmentMaxAge`, the authorizer |
| Exposes | `GET …/namespaces/{ns}/{lakes,sqldatabases,<add-on plural>}/{name}/logs`; `funcdctl logs <kind>/<name>`; segments under `logs/<ns>/<plural>.<name>/` |
| Config keys | none new |

## Implementation plan

- `internal/funclog`: `Resource.Kind`, `Name` and the owner segment in `segmentKey`; the resource keys; `compact`'s
  owner and `Row.Kind`, `Row.Name`; `logread.Query`'s prefix.
- `pkg/funcd/executor.go`: the engine branch also pumps each stream to funclog with the engine's `Resource`
  (`funclog.Pump`, `funclog.NewRawReader`, severity INFO).
- `internal/runtime`, `internal/provider`: `ServedKind`; set by `pkg/funcd/addon_provider.go`; Lake and SQLDatabase
  leave it empty.
- `internal/controlplane/logs.go`: the routes of `lakes` and `sqldatabases`; `internal/controlplane/addon.go`: the
  add-on kinds' `/{plural}/{name}/logs`; their answers and cause rows.
- `pkg/sdk`, `cmd/funcdctl/logs.go`: `EngineLogs` and the `<kind>/<name>` form.
- `internal/provider/docker`, `internal/provider/nerdctl`: follow each engine container's streams into the capture.
- Docs: a "Read its engine's logs" line in the guide's Add-ons, Lake and SQL Usage pages; the behavior rows.
- Tests, one per Scenario, named after it: the add-on and Lake/SQL ones in `pkg/funcd` against the process runtime;
  `engine-logs-shared-name` and `engine-logs-no-engine` there too; the scope one in `internal/controlplane`; the dev
  one in `cmd/funcdctl` against Docker when a socket answers. Unit tests: the owner segment never equals a Function's,
  an old compacted file reads with empty `kind` and `name`, the CLI's forms.
- A lane case on containerd: `addon-rauthy` reads `funcdctl logs identityprovider/login` and finds rauthy's start
  line.
- Done when `just ci` passes, the lane case passes, and `just check-behavior` lists the rows.

## Review checklist

- [ ] Each Scenario has a passing test of its name.
- [ ] An engine's line reaches funcd's log with funclog on and off (ADR-0276's test unchanged).
- [ ] Engine segments live under `logs/<ns>/<plural>.<name>/`; a Function's keys and rows are unchanged.
- [ ] The logs routes authorize before reading, with the object's kind; 403 outside a bound namespace.
- [ ] A settings kind's plural and an unregistered plural answer 404.
- [ ] `funcdctl logs <name>` still reads a Function.
- [ ] Dev's providers follow a container from its start, and the stream ends with it.
- [ ] The `addon-rauthy` lane case passed on containerd.

## Consequences

- (+) One store and one read rule for every workload's output; a developer debugs an engine without the host.
- (+) F268 has its identity: the four resource keys.
- (−) The funcd-system bucket grows with chatty engines, bounded by funclog's record bound, queue drop and retention.
- (−) A workflow run's read, which scans `logs/<ns>/` for its trace (ADR-0106), also lists the engine segments, whose
  records carry no trace.
- Risk accepted: an engine's output reaches everyone who may read its object, as a Function's output reaches everyone
  who may read the Function.

## Open questions

- How engines give metrics, by OTLP push or by a scrape: F268's ADR.
- Whether funcd sends logs, Functions' and engines', to `telemetry.endpoint`: a later decision, with the decider.

## References

- FEAT-0020 F267; feature issue #1518, under the epoch's tracker #1517.
- ADR-0081, ADR-0083, ADR-0084, ADR-0106, ADR-0168, ADR-0276, ADR-0332, ADR-0334.
- Code on main d5bef540: `pkg/funcd/executor.go:239`, `pkg/funcd/engineoutput.go:16`, `internal/funclog/sink.go:352`,
  `internal/funclog/compact/compact.go:51`, `internal/controlplane/logs.go:70`, `internal/controlplane/addon.go:324`.
