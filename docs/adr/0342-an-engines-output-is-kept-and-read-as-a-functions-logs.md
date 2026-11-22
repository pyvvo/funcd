# ADR-0342: An engine's output is kept and read as a Function's logs

- **Status**: Reviewing (2026-11-22, implemented); Accepted (2026-11-22), by the decider's go-ahead ("Don't wait for me ... We should have this
  implemented, and the related lanes"), after three rounds of an independent judge, every Blocker and Major folded
- **Date**: 2026-11-22
- **Deciders**: green-0-rabbit
- **Tags**: observability, logs, engines, addons, lake, sql
- **Realizes**: [FEAT-0020/F267](../feat/0020-feat-engine-logs.md) (an engine's logs are read as a Function's)
- **Relates to**: [ADR-0081](0081-function-log-capture-side-channel-blob.md) (funclog's OTLP segments),
  [ADR-0083](0083-funclog-compaction-otlp-jsonl-to-parquet.md) (compaction, retention and the stable `Row`, kept),
  [ADR-0084](0084-funclog-read-funcdctl-logs.md) (the logs read and its tenant scope),
  [ADR-0106](0106-run-scoped-log-read.md) (a run's read),
  [ADR-0168](0168-raw-output-pipes-and-record-bound.md) (raw output; its Consequence "engine raw output not stored",
  open question 15, answered here),
  [ADR-0255](0255-funcdctl-dev-runs-the-dev-loop.md) (dev's log folder and its start line, reused) and
  [ADR-0256](0256-funcdctl-dev-prints-output-that-led-to-no-build.md) Decision 3 (a watch command's bursts in dev's
  terminal, which an engine does not take),
  [ADR-0276](0276-a-lake-shows-when-its-catalog-saves-fail.md) Decision 4 (an engine's output in the daemon log,
  kept) and its Scope (funclog storage of engine output, left out there),
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (add-on kinds served at their plural),
  [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md) (dev's add-on containers),
  [ADR-0340](0340-an-addon-declares-settings-kinds.md) (settings kinds, which run no engine). It lands after
  FEAT-0019's PR, which holds ADR-0340.

## Context & Need

funcd runs an engine for each add-on object, Lake and SQLDatabase. The output capture sends every line an engine writes
to funcd's own log only (`pkg/funcd/executor.go:239`, ADR-0276 Decision 4); ADR-0168 stored a Function's raw output and
left an engine's unstored (its open question 15). A Function's lines go to funclog, which keeps them as OpenTelemetry
log records and serves them to `funcdctl logs` (ADR-0081, ADR-0084). Under `funcdctl dev`, an add-on's engine writes
into its Docker container, where funcd reads nothing (ADR-0334). So a developer whose IdentityProvider stays
`EngineNotReady`, or whose Lake refuses a query, cannot read the engine's reason without the host. Purpose: whoever may
read an object reads its engine's output with `funcdctl logs`, kept and bounded as a Function's logs are, while dev's
terminal shows none of it: the decider asked that add-on engines, which can write many lines, not bloat it. The decider
chose logs first and metrics later, on the same identity (FEAT-0020 F268).

## Scenarios

- `scenario: engine-logs-addon-object` — Given IdentityProvider `login` in `default`, whose engine writes a line to
  stderr, When a developer bound to `default` runs `funcdctl logs identityprovider/login -n default` once the segment
  is sealed, Then the line shows at INFO with source `stderr`, kind `IdentityProvider` and name `login`, and funcd's
  own log still holds its `engine output` record.
- `scenario: engine-logs-lake-and-sql` — Given Lake `sales` and SQLDatabase `orders`, When `funcdctl logs lake/sales`
  and `funcdctl logs sqldatabase/orders` run, Then each shows its own engine's lines only.
- `scenario: engine-logs-scope` — Given a developer and a viewer bound to `team-a` only, When they read
  `identityprovider/login -n default`, Then 403; a viewer bound to `default` and an admin read it.
- `scenario: engine-logs-shared-name` — Given Function `login` and IdentityProvider `login` in one namespace, When
  `funcdctl logs login` and `funcdctl logs identityprovider/login` run, Then each shows only its own lines.
- `scenario: engine-logs-no-engine` — Given the settings kind `RauthyPasswordPolicy`, or a kind no Addon registers,
  When `funcdctl logs rauthypasswordpolicy/strict` runs, Then 404; `funcdctl logs bucket/b` is refused before any
  call, and `funcdctl logs function/login` reads the Function `login`.
- `scenario: engine-logs-funclog-off` — Given `funclog.enabled: false`, When an engine writes a line, Then
  `funcdctl logs <kind>/<name>` shows nothing new, and funcd's own log holds the line.
- `scenario: engine-logs-dev` — Given `funcdctl dev` runs IdentityProvider `login`'s engine in Docker, When the engine
  writes lines, Then dev's terminal shows none of them, dev printed the engine's log file once as the container
  started, the file holds them, and `funcdctl logs identityprovider/login` against dev returns them.

## Scope

**In**: the stdout and stderr of the engine workers of Lakes, SQLDatabases and add-on objects, kept by funclog; a logs
route per engine kind; `funcdctl logs <kind>/<name>`; dev's add-on containers.
**Out**: metrics and traces of engines (F268); sending any log to `telemetry.endpoint`; settings kinds' objects (no
engine); the engines of other kinds, such as ADR-0281's AIGateway (unbuilt), which keep the daemon log only until
their own ADR decides; dev's own Lake and SQL engines, subprocesses that log at Debug
(`internal/lake/devengine/devengine.go:471`, `internal/sql/devengine/devengine.go:509`), which store nothing; a change
to the daemon log copy on a server (ADR-0276 Decision 4 stays); `funcdctl logs` flags beyond those it has.

## Constraints & Decision drivers

- One path for every engine kind: each engine worker carries its owner kind (`provider.NewRuntime`'s `OwnerKind`, in
  the add-on provider's `build` and in `pkg/funcd/binding_providers.go`) and meets one output hook.
- One store: funclog's records, segments, compaction and retention, and ADR-0083's stable compacted schema.
- The read rule of a Function's logs: authorized at query time in the path's namespace (ADR-0084).
- No engine key may meet a Function's: both live in one namespace prefix.
- Dev's terminal shows no engine line, unlike a `dev.watch` command's bursts (ADR-0256), as the decider asked.
- F268's metrics reuse the identity chosen here.

## Alternatives considered

- **Do nothing, document `journalctl` and `docker logs`**: a developer without the host reads nothing. Lost on the
  purpose.
- **Read funcd's own log through the API**: funcd writes its log to stdout and keeps none; a reader would need a
  buffer lost at each restart, and the daemon log has no tenant scope. Rejected.
- **A prefix of its own (`engines/<ns>/<kind>/<name>/…`)**: a second layout for the compactor and the reader to list.
  Rejected for an owner segment under `logs/<ns>/` that no Function name can take.
- **`kind` and `name` columns in the compacted `Row`**: ADR-0083 keeps that schema stable, with new attributes in
  `attrs_json`, never new columns. Rejected: the key's owner segment already names them, and the reader fills them.
- **stderr at ERROR, as a Function's raw stderr (ADR-0168)**: engines write routine lines to stderr (ADR-0276
  Decision 4), so `--severity error` would show them all. Rejected.
- **Dev prints engine lines, as it prints a Function's, or as a watch command's**: an engine may write a line per
  request, and one that owns no build would print every burst under ADR-0256 Decision 3; the decider refused that
  bloat. Rejected. So is printing a failed container's last 20 lines (ADR-0255 Decision 3): dev re-creates a failed
  engine at each pass, so a crash loop would print every pass. Printing them once, at the first failure since the
  engine was last Ready, is left out: the object's `EngineNotReady`, its log file and `funcdctl logs` already show
  why; a later ADR can add it if dev proves too quiet.
- **The engine pushes OTLP itself (`OTEL_EXPORTER_OTLP_ENDPOINT` to a funcd receiver)**: only engines with an
  OpenTelemetry SDK, and an engine's egress to funcd. Kept for F268's metrics, which need such a path anyway.

## Decision

1. **An engine's lines are funclog records.** For the engine workers of Lakes, SQLDatabases and add-on objects, one
   loop per stream reads each line, logs it to funcd's log as today (ADR-0276 Decision 4), and, when funclog runs,
   appends it to funclog's logs sink as a raw record with its source `stdout` or `stderr`, at INFO for both streams;
   the segment flushes at the stream's end. A line is bounded as a Function's raw line is (ADR-0168). The one loop is
   the cost: a slow segment Put delays the daemon log line too, and a line the output queue drops (ADR-0168) misses
   both. No engine line is above INFO, so `--severity warn` matches none.
2. **The identity.** An engine record's resource holds `source` `engine`, `namespace`, `kind` (the kind its object is
   served as: `Lake`, `SQLDatabase`, or the add-on's kind, such as `IdentityProvider`), `name`, `replica` and `tenant`,
   with no `function`; `kind`, `name` and the value `engine` are new, and F268's metrics carry the same resource. Its
   segments live at `logs/<ns>/<plural>.<name>/<date>/…`, where `<plural>` is the kind in lower case with an `s`
   (`v1.AddonPlural`): an object name is a DNS label with no dot, so no Function's segment `logs/<ns>/<fn>/` is the
   same. Compaction and retention treat the segment as a Function's, and the compacted `Row` stays as it is: an engine
   row's `function` is empty, and the read fills `kind` and `name` from its query, whose route knows them (`Lake`,
   `SQLDatabase`, or the Addon's kind). A run's read (ADR-0106) skips the owner segments with a dot: an engine record
   carries no trace.
3. **The read.** `GET /apis/funcd.io/v1alpha1/namespaces/{namespace}/{plural}/{name}/logs` serves `lakes`,
   `sqldatabases` and each add-on kind's plural, with the Function route's query (`since`, `severity`, `limit`). It
   authorizes before reading, as a read of the object: `get` on `Lake`, `SQLDatabase` or `AddonObject` in the namespace
   (an admin in any namespace, a developer or a viewer in a bound one), else 403. It reads what is stored, so an
   object's logs outlive it until retention, as a deleted Function's do. A plural that is no Addon's kind, or is a
   settings kind's, is 404, so an Addon's deletion hides its objects' logs before their retention ends. Each line of
   the answer carries `kind` and `name`.
4. **The command.** `funcdctl logs <kind>/<name>` resolves `<kind>` as `funcdctl get` does (`sdk.KindFromToken`: a
   plural, a singular or an alias, in any case), with the flags of `funcdctl logs`. `function/<name>` is the bare
   form; `lake`, `sqldatabase` and a kind that is none of funcd's (an add-on's) read the engine route; any other kind
   of funcd's is refused before any call, `workflowrun` naming `funcdctl workflow logs`. A bare name is a Function, as
   before.
5. **Dev.** Under `funcdctl dev`, the Docker and nerdctl runtimes follow each engine container's stdout and stderr
   from its start, splitting a line longer than 64 KiB as a server's worker pipe does (`workerpipe.DefaultLineBytes`).
   Each line goes to the container's log file, `<process dir>/logs/<namespace>/<plural>.<name>.log` beside the
   `dev.watch` logs, emptied when dev starts, and to dev's funclog store, past dev's printing tee; a failed append is
   dropped unlogged. None reaches dev's terminal or dev's own log: dev prints only `engine <kind>/<name>: <log file>`, once, as the container
   first starts, as it prints a watch command's file (ADR-0255). This departs from a watch command's rule on purpose
   (ADR-0256 Decision 3 prints every burst of a command that owns no build). Lines written while dev stops are lost:
   dev stops the containers after the platform (ADR-0334 Decision 7).

## Design model

None: the decision adds no lifecycle, race, timer or conditional write. Lines take the existing capture, one loop per
stream, and funclog's sink; a stream ends with its worker or its container.

## Behavior rows

- **workers.md**: "A line an engine writes" (in funcd's log at once, in `funcdctl logs <kind>/<name>` once its segment
  is sealed, `funclog.segmentMaxAge` 10 s), `decided`.
- **services.md**, changed: "A Lake with `s3gateway.enabled` false" (row 36) and "An engine that exits at start"
  (row 37) name `funcdctl logs lake/<name>` beside the daemon log's `engine output`.
- **funcdctl.md**: "`funcdctl logs <kind>/<name>`" (a bare name or `function/` is a Function; an unregistered or
  engine-less add-on plural is 404; another kind of funcd's is refused), `decided`.
- **funcdctl-dev.md**: "An add-on engine's output under dev" (its log file and dev's store, never the terminal),
  `decided`.

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

type Line struct {
	// ...unchanged fields; Function's doc becomes "The Function whose worker wrote the line; empty for an engine's".
	Kind string `json:"kind,omitempty" doc:"The kind of the object whose engine wrote the line; empty for a Function's."` // new
	Name string `json:"name,omitempty" doc:"The object whose engine wrote the line; empty for a Function's."`            // new
}

// internal/runtime
type WorkerSpec struct {
	// ...unchanged fields...
	ServedKind v1alpha1.Kind // new: the kind its object is served as, when it is not OwnerKind (an add-on object's kind)
}

// internal/provider
type ProviderSpec struct {
	// ...unchanged fields...
	ServedKind v1.Kind // new: copied into each engine's WorkerSpec
}

// internal/provider (new): a Runtime that reads its engines' output itself, as dev's do
type LineFollower interface {
	FollowLines(fn func(spec ProviderSpec, stream, line string))
}

// internal/provider/docker and internal/provider/nerdctl, on their Engine (new): from the container's start
Follow(ctx context.Context, name string) (stdout, stderr io.ReadCloser, err error)

// internal/provider/docker.Runtime, which nerdctl's embeds (new)
func (r *Runtime) SetLogs(dir string, writef func(format string, args ...any) error) // dev's log folder and printer
func (r *Runtime) FollowLines(fn func(spec provider.ProviderSpec, stream, line string)) // provider.LineFollower

// pkg/sdk
func (c *Client) EngineLogs(ctx context.Context, ns v1.NamespaceName, kind v1.Kind, name v1.ObjectName,
	opts LogsOptions) ([]logread.Line, error)
```

Under dev, `cmd/funcdctl` calls `SetLogs` with `<process dir>/logs` and its printer, and the runtime writes each log
file and prints its one line. The add-on provider sets `FollowLines` to the executor's append-only capture: funclog's
inner sink, never the daemon log. The runtime's follow goroutines end with their container and are not under the
drain (`pkg/funcd/logroutes.go`), so dev's stop waits for none of them.

| Dependency or output | What |
|---|---|
| Consumes | the output capture (`runtime.OutputCapturer`), funclog's logs sink, `funclog.enabled`, `funclog.segmentMaxAge`, the authorizer |
| Exposes | `GET …/namespaces/{ns}/{lakes,sqldatabases,<add-on plural>}/{name}/logs`; `funcdctl logs <kind>/<name>`; segments under `logs/<ns>/<plural>.<name>/`; dev's `<process dir>/logs/<namespace>/<plural>.<name>.log` |
| Config keys | none new; `funclog.enabled`'s doc names the engines' logs too |

## Implementation plan

- `internal/funclog`: `Resource.Kind`, `Name`, the owner segment in `segmentKey`, the engine resource in
  `marshalOTLP`; `compact`'s owner key; `logread`'s `Query`, `Line` and the run read's skip.
- `pkg/funcd/engineoutput.go`: one loop per stream that logs a line and appends it at INFO for the three kinds;
  `pkg/funcd/executor.go` hands it the inner sink.
- `ServedKind`: set in `internal/services/addon/reconcile.go` (`engineSpec`), copied in `internal/provider/runtime.go`
  (`workerSpec`); Lake and SQLDatabase leave it empty.
- `internal/controlplane/logs.go`: the routes of `lakes` and `sqldatabases`; `internal/controlplane/addon.go`: the
  add-on kinds' `/{plural}/{name}/logs`; their answers and cause rows; the OpenAPI spec and the stubs regenerated.
- `pkg/sdk`, `cmd/funcdctl/logs.go`: `EngineLogs` and the `<kind>/<name>` form.
- `internal/provider/docker`, `internal/provider/nerdctl`: `Follow`, `SetLogs` and `FollowLines`;
  `cmd/funcdctl/dev_addon.go` calls `SetLogs`; the add-on provider's `build` sets `FollowLines`.
- `internal/platform/config`: `funclog.enabled`'s doc.
- Docs: a "Read its engine's logs" line in the guide's Add-ons, Lake and SQL Usage pages; the behavior rows.
- Tests, one per Scenario, named after it: the add-on and Lake/SQL ones in `pkg/funcd` against the process runtime;
  `engine-logs-shared-name` and `engine-logs-no-engine` there too; the scope one in `internal/controlplane`; the dev
  one in `cmd/funcdctl` against Docker when a socket answers. `TestEngineWorkerRawOutputIsNotStored` is rewritten: it
  now finds the engine's segment under `logs/default/<plural>.engine/`. Unit tests: the owner segment never equals a
  Function's, a run's read skips it, an old compacted file reads with empty `kind` and `name`, the CLI's forms.
- A lane case on containerd: `addon-rauthy` reads `funcdctl logs identityprovider/login` and finds rauthy's start
  line; `duckdb` and `sql` read their engine's lines the same way.
- Done when `just ci` passes, the lane cases pass, and `just check-behavior` lists the rows.

## Review checklist

- [ ] Each Scenario has a passing test of its name.
- [ ] An engine's line reaches funcd's log with funclog on and off, from one reader per stream.
- [ ] Engine segments live under `logs/<ns>/<plural>.<name>/`; a Function's keys and `Row` are unchanged.
- [ ] The logs routes authorize before reading, with the object's kind; 403 outside a bound namespace.
- [ ] A settings kind's plural and an unregistered plural answer 404; a non-engine kind of funcd's is refused locally.
- [ ] `funcdctl logs <name>` still reads a Function.
- [ ] Dev's terminal and dev's own log show no engine line; the log file and dev's store hold them.
- [ ] Dev's stop waits for no follower, and a follower splits a line longer than 64 KiB.
- [ ] The lane cases passed on containerd.

## Consequences

- (+) One store and one read rule for every workload's output; a developer debugs an engine without the host.
- (+) F268 has its identity: the engine resource.
- (−) The funcd-system bucket grows with chatty engines, bounded by retention and `storage.minFreeSize`.
- (−) One loop serves both copies of a line (Decision 1).
- (−) Under dev, the store is in memory unless `--persist`, so neither retention nor `storage.minFreeSize` bounds an
  engine's lines within a session, and its log file grows with them.
- Risk accepted: an engine's output reaches everyone who may read its object, viewers included. An engine gets its
  bound Secrets, keys and auth files (`secretEnv` in `internal/services/addon/reconcile.go`), which a viewer may not read
  (ADR-0171 Decision 9); an engine that prints one shows it to them, as a Function that prints one does.

## Open questions

- How engines give metrics, by OTLP push or by a scrape: F268's ADR.
- Whether funcd sends logs, Functions' and engines', to `telemetry.endpoint`: a later decision, with the decider.

## References

- FEAT-0020 F267; feature issue #1518, under the epoch's tracker #1517.
- ADR-0081, ADR-0083, ADR-0084, ADR-0106, ADR-0168, ADR-0171, ADR-0255, ADR-0256, ADR-0276, ADR-0281, ADR-0332,
  ADR-0334, ADR-0340.
- Code: the executor's `captureLogs` and `logEngineOutput` (`pkg/funcd`), funclog's `segmentKey`, compact's `Row`,
  `RegisterLogs` in `internal/controlplane/logs.go`, `addonObjectOperations` and `kindFor` in
  `internal/controlplane/addon.go`.
