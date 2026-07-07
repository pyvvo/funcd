# ADR-0106: Run-scoped log read — `funcdctl workflow logs <run>` (the full error/logs by run)

- **Status**: Implemented
- **Date**: 2026-07-07
- **Implemented**: 2026-07-07
- **Deciders**: green-0-rabbit
- **Tags**: workflow, observability, logs, traces, funclog, read, funcdctl, troubleshooting
- **Acceptance note**: judge returned no Blockers/Majors; three Minors folded — (1) the reader guard stated explicitly (`Function=="" ⇒ TraceID required`, else Invalid, so the namespace-wide mode can never be an unfiltered dump); (2) the `--step`→function taxonomy tightened (`Image`⇒`<workflow>-<step>`, `Ref`⇒the referenced function, undefined for `builtin:`/`workflow:` steps) + `--step` keeps the trace filter; (3) `WorkflowRunLogQuerier` field-ownership spelled out. Plus the `limit`/`severity`-don't-bound-the-scan nit and the ADR-0104 link.
- **Realizes**: [FEAT-0005/F67](../feat/0005-feat-workflow-engine.md) — the run-scoped log read the
  troubleshooting `describe` (ADR-0100) points at
- **Relates to**: [ADR-0100](0100-run-step-lineage.md) (the sibling: `describe` mirrors the run `trace_id` to
  `status` + prints the `funcdctl workflow logs <run>` pointer this delivers) · [ADR-0084](0084-funclog-read-funcdctl-logs.md)
  (the funclog reader + `funcdctl logs <fn>` this extends with a `TraceID` filter + namespace-wide mode) ·
  [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md)–[ADR-0105](0105-nested-dag-span-parenting.md) (every
  log line of a run carries the run's `trace_id` — the key that makes a run-scoped read one query;
  [ADR-0104](0104-cross-subworkflow-trace-linking.md) is the load-bearing one: a sub-workflow child shares the
  parent's trace, so one query captures the whole composition)

## Context & Need

**Purpose**: give the operator the **full error + all of a run's logs from the CLI in one command** —
`funcdctl workflow logs <run>` — instead of the capped one-liner in `describe` (ADR-0100) or a per-function
`funcdctl logs <fn>` grep across a run's steps. **Callers**: whoever troubleshoots a workflow run.

**Why now**: ADR-0100 makes `describe` show *which step / why (capped) / how long* and prints a
`funcdctl workflow logs <run>` pointer — but that command doesn't exist. The read path (ADR-0084/F54) queries
**one function** by time/severity and has **no `trace_id` filter**. The tracing arc (ADR-0102–0105) now stamps
**the run's `trace_id` on every log line of every step** (and every sub-workflow child step — same trace), so
"all logs for run X" is a **single `trace_id`-filtered query** across the run's functions, not a per-function
grep. This ADR adds that filter + the CLI that uses it.

## Scenarios

Each becomes a named acceptance test.

- **scenario: reader-filters-by-traceid** — *Given* logs from two runs of the same function, *When* the reader
  is queried with `TraceID=T`, *Then* only records whose `trace_id == T` are returned.
- **scenario: namespace-wide-by-traceid** — *Given* a `Query` with **no `Function`** and `TraceID=T`, *Then* the
  reader reads across **all** functions in the namespace (`logs/<ns>/`) and returns only the `T` records,
  time-ordered — so a run's logs across all its step functions come back in one read.
- **scenario: run-logs-resolves-trace** — *Given* `funcdctl workflow logs <run>`, *Then* the server resolves the
  run's `status.traceId` (ADR-0100) and returns the namespace-wide `trace_id`-filtered logs — the whole
  composition (parent **and** sub-workflow child steps share the trace).
- **scenario: run-logs-filters** — *Given* `funcdctl workflow logs <run> --severity error --limit N`, *Then* the
  existing time/severity/limit filters compose with the run scope.
- **scenario: run-logs-step** — *Given* `funcdctl workflow logs <run> --step <name>`, *Then* the read narrows to
  that step's function (the `describe`-hint drill-down into one failing step's full error).
- **scenario: rbac-scoped** — *Given* a caller not authorized on the run's namespace, *Then* the route returns
  403 (query-time tenant scoping, ADR-0084's model — the namespace is the caller's authorized one).

## Scope

**In**: (a) `logread.Query` gains **`TraceID string`**; `BlobReader.Read` filters records by it, and — when
`Function == "" && TraceID != ""` — reads the **whole `logs/<ns>/` prefix** (namespace-wide), still merging
compacted Parquet + the raw JSONL tail, time-ordering, severity/since/limit-filtering; (b) a control-plane
route **`GET …/namespaces/{ns}/workflowruns/{name}/logs`** that resolves the run's `status.traceId` and does the
namespace-wide `trace_id` read (RBAC: authorize on the namespace, ADR-0084's model); (c) `pkg/sdk` +
**`funcdctl workflow logs <run> [--step <name>] [--since --severity --limit]`**.

**Out** (unchanged / follow-ons): the per-function `funcdctl logs <fn>` (unchanged — this adds a **workflow**
verb + an optional `TraceID` param to the reader); `-f`/follow streaming (ADR-0084's deferral); traces/metrics
reads (F51/F52 follow-ons); the public OTLP/Grafana endpoint (F55); full-text search. A **retention** caveat:
like all funclog reads, only logs still in the store (not yet swept) are returned.

## Constraints & Decision drivers

- **Reuse the F54 reader, add one filter.** The `trace_id` is already on `compact.Row`/`logread.Line` (F51);
  filtering is a predicate, and the namespace-wide mode is a prefix change — no new store, no new schema.
- **One query per run via the trace-id.** "All logs for run X" = `trace_id == status.traceId` — which spans the
  run's step functions **and** its sub-workflow child steps (ADR-0104: they share the trace). No per-function
  fan-out, no enumerating the DAG.
- **Query-time tenant scoping (ADR-0084).** The readable namespace is the caller's RBAC-authorized one; the
  route confines the prefix to that namespace. A `trace_id` is not a capability — it only selects, never grants.
- **Additive / non-breaking.** `Query.TraceID` is optional (empty ⇒ ADR-0084 behavior); the new route + verb are
  additive; `go.mod` unchanged.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **CLI fans out per step function** (enumerate `status.steps`, query each `<wf>-<step>` with the trace filter, merge client-side) | Works for top-level steps but **misses sub-workflow child steps** (not in the parent's `status.steps`), needs N round-trips, and re-derives the run→functions mapping. Rejected: the shared `trace_id` makes a **namespace-wide** server-side read capture the whole composition in one call. |
| **A dedicated per-run log store / index** | A second store keyed by run — but the funclog blob already holds every line tagged with the run's `trace_id`; a filtered read is enough. Rejected (no new store, per the funclog ethos). |
| **Only extend `funcdctl logs <fn>` with `--trace`** | Gives the filter but not the run-scoped ergonomics (the operator would still resolve the trace-id + function names by hand). We ship both: the reader `TraceID` filter **and** the `workflow logs <run>` verb that resolves them. |
| **Full-text / structured search** | Bigger scope (a query engine); the trace-id + severity/time/limit filters cover the troubleshooting need. Deferred. |

## Decision

Add a `trace_id` filter + a namespace-wide read mode to the funclog reader, expose a run-scoped control-plane
route that resolves the run's trace-id, and ship `funcdctl workflow logs <run>`.

1. **Reader (`internal/funclog/logread`).** `Query` gains `TraceID string`. `Read` filters each decoded row by
   `q.TraceID` when set (`row.TraceID == q.TraceID`), alongside the existing severity/since/limit. When
   `q.Function == "" && q.TraceID != ""`, the prefix is `logs/<ns>/` (all functions) rather than
   `logs/<ns>/<fn>/`; the merge/time-order/limit logic is unchanged. **Guard** (replacing ADR-0084's
   `Function`-required check): require `q.Namespace != ""` and `q.Function != "" || q.TraceID != ""` — a bare
   namespace scan with **no** trace filter stays `Invalid` (the namespace-wide mode is only reachable *with* a
   trace-id, so it can never become an unfiltered full-namespace dump).
2. **Control plane (`internal/controlplane`).** A `WorkflowRunLogQuerier` (or the run reconciler's store) backs a
   new route `GET …/namespaces/{ns}/workflowruns/{name}/logs`: it `Get`s the run, reads `status.traceId`
   (ADR-0100), and calls the reader with `Query{Namespace: ns, TraceID: traceId, …filters}`. Empty
   `status.traceId` (a legacy/traceless run) ⇒ an empty result with a clear message, not an error. RBAC:
   `authorize(get, WorkflowRun, ns)` — query-time tenant scoping (ADR-0084/0018).
3. **SDK + CLI.** `pkg/sdk` gains `RunLogs(ctx, ns, run, LogsOptions)`; `funcdctl workflow logs <run>` renders the
   lines (reusing the `funcdctl logs` line format), with `--since/--severity/--limit` and an optional
   `--step <name>` that narrows the read to that step's function — the `describe`-hint drill-down into one
   step's full error. **`--step`→function resolution** (read from the run's pinned workflow): a `FunctionStep`
   with `Image` set ⇒ the materialized `<workflow>-<step>`; a `FunctionStep` with `Ref` set ⇒ the referenced
   function. `--step` is **undefined for `builtin:` steps** (no function, no logs) and for a `workflow:` step
   (its logs are a *child run* spanning many functions — already the full trace-wide result). A `--step` read
   sets `Query.Function` **and still carries the trace filter**, so it returns *this run's* lines for that
   function, not every run of it.

## Temporary workarounds

- **Namespace-wide scan cost.** The run-scoped read lists `logs/<ns>/` (all functions) and filters by trace-id.
  Only `since` prunes objects read; `severity` filters post-decode and `limit` caps the *returned* set, not the
  scan (ADR-0084's own caveat). It is a troubleshooting query (not hot-path). *Exit*: a trace-id index if read
  volume ever makes the scan material (measured, not pre-optimized).
- **Retention-bounded.** Only unswept logs return (all funclog reads share this). *Exit*: durable audit (a named
  F67 follow-on).

## Contracts

```go
// logread.Query gains (ADR-0106):
TraceID string // "" ⇒ no trace filter (ADR-0084 behavior); set ⇒ only records with this trace_id.
// When Function == "" && TraceID != "", Read scans the whole logs/<ns>/ prefix (namespace-wide, run-scoped).

// internal/controlplane — a run-scoped querier backing GET …/workflowruns/{name}/logs:
type WorkflowRunLogQuerier interface {
	// RunLogs resolves the run's status.traceId and returns the namespace-wide, trace-filtered logs.
	// Field ownership: the querier sets q.Namespace = ns and q.TraceID = the resolved status.traceId; the
	// caller supplies the filter fields (Since / MinSeverityNumber / Limit, and Function for the --step
	// drill-down). A --step read keeps the trace filter, so it returns only this run's lines for that function.
	RunLogs(ctx context.Context, ns v1.NamespaceName, run v1.ObjectName, q logread.Query) ([]logread.Line, error)
}

// pkg/sdk:
func (c *Client) RunLogs(ctx context.Context, ns v1.NamespaceName, run v1.ObjectName, o LogsOptions) ([]logread.Line, error)
```

**Route**: `GET /apis/funcd.io/v1alpha1/namespaces/{namespace}/workflowruns/{name}/logs` (query params
`since`, `severity`, `limit`, `step`), RBAC `authorize(get, WorkflowRun, namespace)`.

**CLI**: `funcdctl workflow logs <run> [-n <ns>] [--since] [--severity] [--limit] [--step <name>]`.

**Dependencies & I/O**

| Consumes | Exposes |
|---|---|
| the ADR-0084 funclog reader (`logread.BlobReader`, `compact.Row.TraceID`) · the ADR-0100 `status.traceId` · the ADR-0018 authorizer | a `TraceID` filter + namespace-wide read on the reader; `GET …/workflowruns/{name}/logs`; `funcdctl workflow logs <run>` |
| the ADR-0102–0105 run trace-id stamped on every log line | the full error + all a run's logs (parent + sub-workflow child steps), by run, from the CLI |

New deps: **none**; no `go.mod` change.

## Implementation plan

- **Files**: `internal/funclog/logread/logread.go` (`Query.TraceID`; the row filter; the namespace-wide prefix
  when `Function == ""`); `internal/controlplane/logs.go` (the `WorkflowRunLogQuerier` + the
  `…/workflowruns/{name}/logs` route + RBAC + the OpenAPI stub); `pkg/funcd/funcd.go` (wire the run-log querier —
  it needs the store for `Get(run)` + the reader); `pkg/sdk/logs.go` (`RunLogs`); `cmd/funcdctl/workflow.go`
  (the `workflow logs` subcommand). No `go.mod` change.
- **Test plan** (one named test per Scenario):
  - `logread` unit over a real in-memory `blob.Bucket`: `reader-filters-by-traceid` (two trace-ids in one
    function → only the queried one), `namespace-wide-by-traceid` (records under two functions, one trace → both
    returned when `Function==""`, others excluded).
  - control-plane unit: `run-logs-resolves-trace` (a run with `status.traceId` → the querier reads that trace),
    `rbac-scoped` (unauthorized namespace → 403), empty-trace run → empty result (not an error).
  - **in-process e2e** (`pkg/funcd`): a real workflow run that logs across ≥2 steps → `funcdctl workflow logs
    <run>` (via the SDK) returns the run's lines across both step functions, `--severity error` filters, `--step`
    narrows to one; a **sub-workflow** run → the child step's logs come back too (same trace).
  - **Venom** (the containerd workflow lane): `funcdctl workflow logs <run>` in-VM returns ≥1 line for a run and
    fewer with `--severity error` — the run-scoped read on real containerd.
- **Definition of done**: `go build/test/lint` + `go mod verify` green; every Scenario a passing test; the
  in-process e2e + the workflow Venom lane green; the ADR-0084 `funcdctl logs <fn>` still works (additive);
  feat F67 row advanced; identity/path grep clean; `just ci` green after commit.

## Review checklist

- [ ] `logread.Query.TraceID` filters records; empty ⇒ ADR-0084 behavior (per-function read unchanged).
- [ ] `Function == "" && TraceID != ""` reads namespace-wide (`logs/<ns>/`) and returns only that trace's lines,
      time-ordered, filtered by severity/since/limit.
- [ ] `GET …/workflowruns/{name}/logs` resolves `status.traceId`, RBAC-authorizes on the namespace, and returns
      the run-scoped logs (parent + sub-workflow child steps — same trace); an empty trace-id ⇒ empty, not error.
- [ ] `funcdctl workflow logs <run>` renders the lines with `--since/--severity/--limit` and `--step` drill-down.
- [ ] Additive: `Query.TraceID` optional, the route + verb new; `funcdctl logs <fn>` unchanged; `go.mod` unchanged.
- [ ] A trace-id selects, never grants — the read stays confined to the RBAC-authorized namespace.

## Consequences

- **(+)** **The full error + all a run's logs from one CLI command** — `funcdctl workflow logs <run>` — closing
  the loop `describe` (ADR-0100) opens: capped summary → one command → the whole story.
- **(+)** **The one-run-one-trace payoff, realized for reads** — because every line carries the run's trace-id
  (ADR-0102–0105), the run-scoped read is a single filtered query that spans steps **and** sub-workflow children.
- **(+)** **Reuses the F54 reader + funclog store** — one new filter + a prefix mode + a route + a verb; no new
  store, schema, or dependency.
- **(−)** **Namespace-wide scan** — the run-scoped read lists all the namespace's log objects and filters;
  bounded by the query filters, and a trace-id index is the documented exit if it ever matters.
- **(−)** **Retention-bounded** — only unswept logs return (shared with every funclog read; durable audit is the
  named follow-on).

## Open questions

- **`--step` name resolution** — mapping a step name to its function is `<workflow>-<step>` for image steps and
  the referenced function for `function:` steps; the CLI reads the run's workflow to resolve it. A display detail.
- **Cross-namespace compositions** — V1 scopes to one namespace (sub-workflows run in the parent's namespace,
  ADR-0099). A composition spanning namespaces would need a multi-namespace read; out of scope.

## References

- [ADR-0084](0084-funclog-read-funcdctl-logs.md) — the funclog reader + `funcdctl logs <fn>` this extends.
- [ADR-0100](0100-run-step-lineage.md) — the sibling: `status.traceId` + the `describe` pointer this delivers.
- [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md)–[ADR-0105](0105-nested-dag-span-parenting.md) — the
  run trace-id stamped on every log line (the key this filters on).
- [ADR-0018](0018-api-server-authn-rbac-admission.md) — the RBAC authorizer (query-time tenant scoping).
- FEAT-0005/F67 (run observability); FEAT-0004/F54 (the log read path extended here).
