# ADR-0084: Thin pure-Go function-log reader + `funcdctl logs`

- **Status**: Implemented
- **Superseded in part by**: [ADR-0196](0196-utc-millisecond-timestamps.md) (2026-10-07) — the `time.Time` field type (154): the log-read `time` is RFC3339 UTC with exactly 3 fractional digits.
- **Date**: 2026-06-29 (Accepted 2026-06-29 after one judge pass — no Blockers; security model verified airtight
  [RBAC-derived namespace, never client-asserted; DNS-label path params block traversal] and kept verbatim. Folded
  the Major [made `compact.DecodeJSONL` provably wrap ADR-0083's *existing* per-line decode loop — an additive
  export, F53's tests the regression guard] + Minors [tail = `rows[len-Limit:]` not head-N; validated typed path
  params into `Query`; CLI renders a subset of the `Line` DTO] + Nits [`MaxLimit` rationale; `NewServer` captures
  the `huma.API`]. Blueprint synced: F54 moved from add-on provider → in-daemon reader. **Reviewing → Implemented
  2026-06-29** — review **pass** (DoD 9/9), see docs/reviews/adr-0084-implementation-claude-opus-4-8.md;
  `internal/funclog/logread` + `compact.DecodeJSONL` + the control-plane logs route + `pkg/sdk.Logs` + `funcdctl
  logs` + wiring, 8 scenario tests green [tenant scoping proven 403/200/operator-all], OpenAPI regenerated, lint 0,
  no new deps.)
- **Deciders**: green-0-rabbit
- **Tags**: observability, logs, serving, reader, parquet, funcdctl, tenant-scoping, daemon-internal
- **Realizes**: [FEAT-0004/F54](../feat/0004-feat-platform-observability.md)
- **Relates to**: [ADR-0083](0083-funclog-compaction-otlp-jsonl-to-parquet.md) (the compacted Parquet `Row` schema
  this reads) · [ADR-0081](0081-function-log-capture-side-channel-blob.md) (the raw OTLP-JSONL tail this also reads
  + the identity tags it scopes on) · [ADR-0007](0007-blob-storage-layer-port.md) (`blob.Bucket`) ·
  [ADR-0018](0018-api-server-authn-rbac-admission.md) (the authn/RBAC the query-time tenant scoping reuses) ·
  [ADR-0028](0028-platform-control-plane-wiring.md)/[ADR-0005](0005-api-surface-code-first-huma.md) (the control-plane
  huma server this adds a route to) · [ADR-0042](0042-cobra-cli-framework.md) (the `funcdctl` cobra CLI + `pkg/sdk`) ·
  [ADR-0065](0065-metastore-badger-engine.md) (pure-Go, no cgo)

## Context & Need

**Purpose**: let a tenant **read the logs of their functions** — `funcdctl logs <fn>` — over funcd's own
primitives, with **no DuckDB and no lakehouse**. A **thin pure-Go reader** merges the **compacted Parquet**
"silver" (ADR-0083) with the **recent raw OTLP-JSONL tail** (the open window F53 hasn't compacted yet, ADR-0081),
filters it (time / severity / limit), and returns it **tenant-scoped** to the caller's authenticated namespace.

**Callers**: `funcdctl logs` (a human/operator via the control-plane API), and — later — F55's public endpoint.
The read is served by the **control-plane** API server (authenticated, RBAC), the same surface `funcdctl` already
uses for CRUD.

**Why now**: F50 (capture) and F53 (compaction) are Implemented — the bytes exist on blob but nothing reads them
back. F54 closes the loop: it is the first **consumer** of the persisted logs, and the thing that makes the whole
observability lane usable. It is the refinement of the original FEAT-0004 F54 sketch (a DuckDB serving function):
**a thin in-daemon pure-Go reader**, not DuckDB-in-a-function — consistent with the no-cgo daemon (ADR-0065) and
the F53 reader-friendly Parquet schema.

## Scenarios

- **scenario: reads-compacted-and-tail** — *Given* function `f` has both compacted Parquet **and** a recent raw
  OTLP-JSONL tail, *When* its logs are read, *Then* records from **both** are merged into one time-ordered result.
- **scenario: filter-since** — *Given* `--since <t>`, *When* read, *Then* only records with `time ≥ t` are returned.
- **scenario: filter-severity** — *Given* `--severity warn`, *When* read, *Then* only records at severity **WARN or
  above** are returned.
- **scenario: limit-returns-most-recent** — *Given* `--limit N`, *When* more than N records match, *Then* the **N
  most-recent** are returned, oldest-first (a log tail).
- **scenario: empty-when-none** — *Given* a function with no stored logs, *When* read, *Then* an **empty** result is
  returned (not an error).
- **scenario: tenant-scoped-own-namespace** — *Given* a developer token bound to namespace `A`, *When* it reads
  `A`'s function logs, *Then* allowed; *When* it reads `B`'s, *Then* **403 Forbidden** (the mandatory scope is the
  authenticated identity, never client-asserted).
- **scenario: operator-sees-any-namespace** — *Given* an admin (operator) token, *When* it reads any namespace's
  logs, *Then* allowed (the platform-wide view).
- **scenario: funcdctl-logs-prints** — *Given* `funcdctl logs <fn> -n <ns>`, *When* the function has logs, *Then*
  they print one line per record (time · severity · replica · body).

## Scope

**In**: a new leaf package **`internal/funclog/logread`** — the pure-Go `Reader` (merge compacted Parquet + raw
JSONL tail over `blob.Bucket`, filter by since/severity/limit, time-order); a **control-plane logs route**
(`GET …/functions/{name}/logs`) wired in `controlplane.NewServer`, **query-time tenant-scoped** by reusing the
ADR-0018 RBAC `authorize(get, Function, ns)`; a **`pkg/sdk` `Logs` method**; the **`funcdctl logs`** command with
`--since/--severity/--limit/-n`; and the `pkg/funcd` wiring (build the reader over the blob substrate, pass it to
the control-plane server). One additive export on the ADR-0083 `compact` package (`DecodeJSONL`) so the raw-tail
decode is **shared, not duplicated**.

**Out**: **`-f` / follow (streaming)** — point-in-time query only; follow is a focused follow-on (it needs
long-poll/SSE and a cursor). Traces/metrics reads (F51/F52). The **public OTLP/Grafana/Prometheus endpoint** (F55).
DuckDB / any SQL engine. Full-text / attribute-indexed search (the reader filters on typed columns + scans bodies).
Cross-namespace aggregation beyond the operator's all-namespaces view.

## Constraints & Decision drivers

- **Pure-Go, no cgo** (ADR-0065) — read Parquet with `parquet-go` (already a dep, ADR-0083), decode raw OTLP-JSONL
  with `pdata/plog`; **no DuckDB**, no cgo, no lakehouse service.
- **Tenant isolation is mandatory and identity-derived** (ADR-0018 / blueprint default-deny) — the namespace a
  caller may read is the **authenticated** one (RBAC), never a client-asserted parameter; operator (admin) sees all.
- **Reuse the existing surfaces** — the `blob.Bucket` port, the control-plane huma server + authn/RBAC, the
  `funcdctl`/`pkg/sdk` CLI — no new server, no new auth path.
- **Read the live tail** — a freshly-emitted log must be visible **before** its window is compacted, so the reader
  merges the not-yet-compacted raw JSONL with the compacted Parquet.
- **Thin** — a bounded scan (list prefix → read matching objects → filter → sort → cap), not an index/engine.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Thin in-daemon pure-Go reader** (parquet-go + plog over `blob.Bucket`) — **chosen** | No cgo, no extra process, reuses the F53 schema + the control-plane auth; bounded scan is fine for log-tail at the ~100-agent scale. The refinement of the original F54 sketch. |
| **DuckDB serving function** (the original FEAT-0004 F54 — an add-on provider running DuckDB-over-Parquet) | SQL power + Parquet pushdown — but pulls **cgo/DuckDB** into the serving path, needs a pinned min-replica function + `funcd-system` bootstrapping, and is far heavier than log-tail needs. Rejected for V1; revisit if rich ad-hoc SQL over logs is ever required (it can read the same Parquet). |
| **Serve logs from the data plane / a worker-node API** | Closer to the function path — but log reading is an **operator/dev** concern (authenticated, RBAC, kubectl-style), which is the **control plane**, where `funcdctl` already talks. |
| **Client-asserted namespace filter** (the caller passes the ns to read) | Trivial — but a **tenant-isolation hole** (a caller could read another tenant). Rejected: the namespace is the RBAC-authorized path segment; the PDP decides. |
| **Read only compacted Parquet** (skip the raw tail) | Simpler reader — but recent logs (the current window, up to ~Window old) would be **invisible** until compacted, defeating `logs` as a tailing tool. Rejected: merge the tail. |
| **A new logs `Handlers` method on the big control-plane interface** | Uniform with CRUD — but ripples the whole `Handlers` interface + every stub. Rejected: register a **dedicated** logs operation with its own small querier dep in `NewServer` (lower blast radius). |

## Decision

A **thin pure-Go log reader** (`internal/funclog/logread`) served by a **new control-plane route**, driving
`funcdctl logs`. Concretely:

- **Reader.** `logread.BlobReader` lists `logs/<ns>/<fn>/` on `blob.Bucket`, reads every **`.parquet`** with
  `parquet.Read[compact.Row]` and every **`.otlp.jsonl`** with `compact.DecodeJSONL` (an additive export that wraps
  the compactor's **existing** per-line decode loop verbatim — see Contracts), maps each `compact.Row` to a `Line`,
  applies the filters (`Since`, `MinSeverityNumber`), **sorts ascending by time, then returns the *tail*
  `rows[len-Limit:]`** — the most-recent `Limit`, oldest-first (not the head/oldest-N). Compacted + tail never
  overlap (F53 deletes raw only after the Parquet write), so no dedup is needed; a brief compaction-race duplicate
  is acceptable (at-least-once, logs) and can never *lose* a window (raw is deleted only once its Parquet exists).
- **Validated identity in, never client-asserted.** The `{namespace}`/`{name}` path params are typed
  `v1.NamespaceName`/`v1.ObjectName`, whose DNS-label schema huma enforces at the boundary (no `/` or `..`), so the
  handler passes the **already-validated** values straight into `Query` — the `logs/<ns>/<fn>/` prefix is always
  well-formed and confined to the authorized namespace.
- **Endpoint.** `GET /apis/funcd.io/v1alpha1/namespaces/{namespace}/functions/{name}/logs?since=&severity=&limit=`
  — a huma operation registered by `NewServer` (which binds `api := NewAPI(...)`, today discarded, then
  conditionally `registerLogs(api, d.Logs, d.Authorizer)`) over a small `LogQuerier` (the reader) + the `auth.Authorizer`.
  The handler calls `authorize(get, KindFunction, {namespace})` **first** (the ADR-0018 PEP): admin ⇒ any
  namespace, developer/viewer ⇒ only a bound namespace, else `403`. **This is the query-time tenant scope** — the
  readable namespace is the authenticated/authorized path segment, never a client-asserted filter. Returns
  `{items: []Line}`.
- **`since` / `severity`.** `since` parses as an RFC3339 time **or** a Go duration (`"15m"` ⇒ `now-15m`); empty ⇒
  no lower bound. `severity` is a level name (`trace|debug|info|warn|error|fatal`) mapped to its OTLP
  `SeverityNumber` threshold; empty ⇒ all. `limit` defaults to `1000` and is hard-capped at `10000`.
- **SDK + CLI.** `pkg/sdk` gains `Client.Logs(ctx, ns, fn, LogsOptions) ([]logread.Line, error)`. `funcdctl logs
  <fn> -n <ns> [--since --severity --limit]` calls it and prints one line per record:
  `<RFC3339> [<SEV>] <replica> <body>` — a deliberate subset; the JSON `Line` DTO also carries
  `source`/`inv`/`traceId`/`spanId`/`attrs` for machine consumers.
- **Wiring.** `pkg/funcd` builds `logread.NewBlobReader(c.blob)` when a blob substrate is present and passes it to
  `controlplane.NewServer` (a new optional `Deps.Logs`); absent ⇒ the route is not registered (a `404`, not a crash).

It is **internal substrate serving the log-ingest capability** (like the F53 compactor), not a function-bindable
ADR-0082 provider.

## Temporary workarounds

- **Full per-`(ns,fn)` prefix scan, no index.** The reader lists `logs/<ns>/<fn>/` and reads the matching objects;
  there is no time index beyond the Hive `<date>` partition + Parquet row-group stats. *Exit*: date-prefix pruning
  from `Since` (read only the relevant `<date>` dirs) and Parquet predicate pushdown if scan cost grows — measured
  on the homebox bench, not pre-optimized.
- **`--limit` caps the result, not the scan.** All matching objects are read, then the tail is taken. *Exit*: the
  date-prefix + newest-first object ordering above, so a small `--limit` reads fewer objects.

## Contracts

```go
// Package logread is the thin pure-Go function-log reader (ADR-0084): it merges the compacted Parquet (ADR-0083)
// with the recent raw OTLP-JSONL tail (ADR-0081) over the blob.Bucket port, filters by time/severity/limit, and
// returns a tenant's function logs time-ordered. No DuckDB, no cgo, lakehouse-independent.
package logread

// Line is one log record returned to a caller (the wire + CLI DTO; a friendlier view of compact.Row).
type Line struct {
	Time           time.Time       `json:"time"`
	Severity       string          `json:"severity"`       // severityText, e.g. "INFO"
	SeverityNumber int32           `json:"severityNumber"`
	Body           string          `json:"body"`
	Namespace      string          `json:"namespace"`
	Function       string          `json:"function"`
	Replica        string          `json:"replica"`
	Source         string          `json:"source"`
	Invocation     string          `json:"inv,omitempty"`
	TraceID        string          `json:"traceId,omitempty"`
	SpanID         string          `json:"spanId,omitempty"`
	Attrs          json.RawMessage `json:"attrs,omitempty"` // the compact.Row attrs_json, verbatim
}

// Query selects + bounds a read. Namespace+Function are required; the rest are optional filters.
type Query struct {
	Namespace         string
	Function          string
	Since             time.Time // zero ⇒ no lower bound
	MinSeverityNumber int32     // 0 ⇒ all severities
	Limit             int       // <= 0 ⇒ DefaultLimit; capped at MaxLimit
}

const (
	DefaultLimit = 1000
	MaxLimit     = 10000
)

// Reader returns a function's logs, newest-bounded by Query. Implemented by BlobReader over blob.Bucket.
type Reader interface {
	Read(ctx context.Context, q Query) ([]Line, error)
}

// NewBlobReader builds the reader over the funcd-system observability bucket.
func NewBlobReader(b blob.Bucket) *BlobReader

// SeverityNumber maps a level name (trace|debug|info|warn|error|fatal, case-insensitive) to its OTLP
// SeverityNumber threshold; ok=false for an unknown name. "" ⇒ (0, true) = no filter.
func SeverityNumber(level string) (int32, bool)
```

```go
// internal/funclog/compact — ADDITIVE export (ADR-0084), reused by logread; the compactor's own raw decode.

// DecodeJSONL decodes one raw OTLP-JSON-Lines object's bytes into Rows — EXACTLY the per-line loop the compactor
// already runs: split on '\n', skip blank lines, plog.JSONUnmarshaler.UnmarshalLogs each line (a bad line ⇒
// fault.Invalid), appendRows. ADDITIVE refactor of ADR-0083 (frozen): readRaw becomes
//   for _, key := range keys { data, _ := Get(key); rows = append(rows, DecodeJSONL(data)...) }
// so the rows produced are unchanged byte-for-byte — only a new exported entry point + a per-object unmarshaler
// (instead of one shared across objects, an allocation detail that does not affect output). ADR-0083's scenario
// tests are the regression guard; no decoded-output or behavior change.
func DecodeJSONL(data []byte) ([]Row, error)
```

```go
// internal/controlplane — a dedicated logs operation, NOT a new method on the big Handlers interface.

// LogQuerier is the control-plane's view of the reader (logread.Reader). Optional dep; nil ⇒ no logs route.
type LogQuerier interface {
	Read(ctx context.Context, q logread.Query) ([]logread.Line, error)
}

// Deps gains:
type Deps struct {
	// …existing…
	Logs LogQuerier // ADR-0084: optional; when set, registers GET …/functions/{name}/logs
}
// NewServer: after NewAPI, if d.Logs != nil → registerLogs(api, d.Logs, d.Authorizer).
// The handler authorizes auth.Request{Identity, VerbGet, KindFunction, namespace} BEFORE reading.
```

```go
// pkg/sdk
type LogsOptions struct {
	Since    string // RFC3339 or a Go duration ("15m"); "" ⇒ no bound
	Severity string // level name; "" ⇒ all
	Limit    int
}
func (c *Client) Logs(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, o LogsOptions) ([]logread.Line, error)
```

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | `blob.Bucket` (compacted `.parquet` + raw `.otlp.jsonl` under `logs/<ns>/<fn>/`) · the ADR-0083 `compact.Row` schema + `compact.DecodeJSONL` · the ADR-0018 `auth.Authorizer` (RBAC) for tenant scoping · the control-plane huma server (ADR-0005/0028) · `funcdctl`/`pkg/sdk` (ADR-0042) |
| Exposes | `GET …/functions/{name}/logs` (control-plane) → `{items: []Line}`; `funcdctl logs <fn>` |
| Config surface | none — on whenever a blob substrate is present; CLI flags `--since/--severity/--limit/-n` |
| New deps | **none** (`parquet-go` + `pdata/plog` already direct) |
| Tenant scope | query-time, RBAC `authorize(get, Function, ns)` — admin = all namespaces, developer/viewer = bound namespaces; never client-asserted |

## Implementation plan

- **Files**: `internal/funclog/logread/{logread.go (Line, Query, Reader, BlobReader, SeverityNumber, the
  merge/filter/sort),logread_test.go}`; `internal/funclog/compact/compact.go` (extract + export `DecodeJSONL`,
  have `readRaw` call it); `internal/controlplane/{logs.go (LogQuerier + registerLogs + the authz'd handler +
  since/severity parsing),server.go (Deps.Logs + register), logs_test.go}`; `pkg/sdk/{logs.go,logs_test.go}`;
  `cmd/funcdctl/{logs.go,logs_test.go}` (the `logs` command, registered in `cli.go`); `pkg/funcd/funcd.go`
  (build `logread.NewBlobReader(c.blob)` and pass to `controlplane.NewServer`).
- **go.mod**: none.
- **Test plan** (one acceptance test per Scenario, names echoing `scenario: <name>`):
  - `logread` over a **real in-memory `blob.Bucket`** (seed compacted via `compact`’s writer + raw via
    `plog.JSONMarshaler` at the real keys): `reads-compacted-and-tail`, `filter-since`, `filter-severity`,
    `limit-returns-most-recent`, `empty-when-none`.
  - `controlplane` logs handler over `httptest` + a static-credential authn + RBAC authorizer:
    `tenant-scoped-own-namespace` (developer in A ⇒ A 200 / B 403), `operator-sees-any-namespace` (admin ⇒ 200).
  - `cmd/funcdctl` `funcdctl-logs-prints` via the injected SDK client over the real control-plane on `httptest`
    (the ADR-0042 test seam), asserting the rendered lines.
- **Definition of done**: `go build/test/lint` + `go mod verify` green; one passing test per Scenario; pure-Go (no
  cgo); reads only through `blob.Bucket`; tenant scoping enforced by RBAC (no client-asserted namespace); identity/
  path grep clean; `just ci` green after commit.

## Review checklist

- [ ] `logread` merges compacted `.parquet` (`parquet.Read[compact.Row]`) + raw `.otlp.jsonl`
      (`compact.DecodeJSONL`) under `logs/<ns>/<fn>/`; sorted ascending by time; returns the **tail**
      `rows[len-Limit:]` (most-recent `Limit`, oldest-first — **not** head-N).
- [ ] `Since` / `MinSeverityNumber` / `Limit` filters honored; `Limit` defaulted (1000) + capped (10000 ≈ a few MB
      of lines — bounds a runaway scan).
- [ ] `empty-when-none` returns an empty slice, not an error.
- [ ] The logs route authorizes `auth.Request{Identity, VerbGet, KindFunction, namespace}` **before** reading;
      developer/viewer bound-namespace only, admin all — **no** client-asserted namespace trust.
- [ ] Route registered only when `Deps.Logs != nil`; absent ⇒ no route (404), not a crash.
- [ ] `pkg/sdk.Logs` + `funcdctl logs` print the lines; `--since` accepts RFC3339 or a duration.
- [ ] Pure-Go, no cgo, no DuckDB; **no new deps**; reads only through `blob.Bucket`.
- [ ] `compact.DecodeJSONL` wraps the compactor's **existing** per-line decode loop unchanged (a new exported entry
      point only); `readRaw` calls it; ADR-0083's scenario tests stay green (no decoded-output change).
- [ ] One passing acceptance test per Scenario.

## Consequences

- **(+)** **Closes the observability loop** — captured (F50) → compacted (F53) → **readable** (F54): `funcdctl logs`
  works end to end on funcd's own primitives, no DuckDB, no lakehouse, no cgo.
- **(+)** **Tenant isolation for free + correct** — reuses the ADR-0018 RBAC PEP, so the scope is the authenticated
  identity (Loki/Mimir query-time model); operator gets the platform-wide view, a tenant only its own.
- **(+)** **Live tail** — merging the raw JSONL tail means a just-emitted log is visible before compaction.
- **(+)** **Minimal blast radius** — a dedicated logs operation + a small `LogQuerier` dep, not a change to the
  big `Handlers` interface; one additive `compact` export, no behavior change to F53.
- **(−)** **Bounded scan, no index** — reads all matching objects then filters/caps; fine for log-tail at the
  ~100-agent scale, but not a search engine (date-prefix pruning is the documented exit).
- **(−)** **No `-f`/follow yet** — point-in-time only; streaming is a follow-on.
- **(−)** **Logs only** — traces/metrics reads (F51/F52) are separate, though the reader/endpoint pattern is reusable.
- **Risk**: scan cost at large log volumes — mitigated by `Since`/`Limit`, the Hive `<date>` partition, and the
  documented date-prefix-pruning exit; measured on the homebox bench.

## Open questions

- **`-f`/follow transport** — long-poll vs SSE vs chunked, plus a since-cursor; resolved in the follow-on ADR.
- **Date-prefix pruning from `Since`** — read only the relevant `<date>` dirs; a measured optimization (Temporary
  workarounds), not pre-built.
- **Body search / attribute filters** — beyond typed-column + body-substring; deferred until a real need (F55 may
  bring Loki-shaped label selectors).

## References

- [ADR-0083](0083-funclog-compaction-otlp-jsonl-to-parquet.md) (the `compact.Row` Parquet schema + raw decode),
  [ADR-0081](0081-function-log-capture-side-channel-blob.md) (raw OTLP-JSONL + identity tags),
  [ADR-0018](0018-api-server-authn-rbac-admission.md) (authn/RBAC), [ADR-0042](0042-cobra-cli-framework.md) (funcdctl/SDK),
  [ADR-0007](0007-blob-storage-layer-port.md) (blob port), [FEAT-0004](../feat/0004-feat-platform-observability.md).
- `github.com/parquet-go/parquet-go` `Read[T]` (Apache-2.0, pure-Go) · `go.opentelemetry.io/collector/pdata/plog`
  `JSONUnmarshaler`.
- Loki/Mimir query-time tenant scoping (the model the RBAC-derived namespace scope follows).
- Tracking: Project #4 card *"Function-log reader + funcdctl logs (F54) — ADR-0084 / FEAT-0004"*.
