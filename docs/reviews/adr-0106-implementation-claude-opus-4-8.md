# ADR-0106 implementation review — `claude-opus-4-8`

## Verdict: pass — 0 blockers, 0 majors  (ADR-0106 implementation, model: claude-opus-4-8)

Run-scoped log read (`funcdctl workflow logs <run>`) — a `TraceID` filter + namespace-wide mode on
the funclog reader, a run-scoped control-plane route that resolves `status.traceId` (ADR-0100) and
does the trace-filtered read (RBAC on the namespace), plus `pkg/sdk.RunLogs` and the CLI verb. The
implementation matches the ADR's Contracts, all six Scenarios have named passing tests, and the full
verification is green.

### Verification (evidence)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | exit 0 |
| unit tests | `go test ./internal/funclog/... ./internal/controlplane/... ./pkg/sdk/... ./cmd/funcdctl/...` | exit 0 — all `ok` |
| lint | `go tool golangci-lint run` (touched pkgs) | exit 0 |
| go mod | `go mod verify` | `all modules verified`; `go.mod`/`go.sum` diff empty |
| OpenAPI golden | `go test ./internal/controlplane/ -run 'TestOpenAPI\|TestSpec\|Golden'` | exit 0 (spec regenerated + stub registered) |
| in-process e2e | `go test ./pkg/funcd/ -run 'TestScenarioE2EWorkflowRunLogs'` | exit 0 — **ran, not skipped** (node present) |
| containerd Venom | workflow lane | 12/12, 0 failures, incl. the run-scoped-read ADR-0106 testcase (PASS) |

### Scenario → test coverage (all six)

- **reader-filters-by-traceid** → `TestScenarioReaderFiltersByTraceID` (logread) — two trace-ids in
  one function, only the queried trace returns.
- **namespace-wide-by-traceid** → `TestScenarioNamespaceWideByTraceID` (logread) — `Function==""`
  spans both step functions, foreign trace excluded.
- **run-logs-resolves-trace** → `TestScenarioRunLogsResolvesTrace` (controlplane) — server resolves
  `status.traceId`, reads namespace-wide (Function empty).
- **run-logs-filters** → covered by the in-process e2e (`--severity error` narrows to error+ lines)
  and the Venom lane (`e<a`).
- **run-logs-step** → `TestScenarioRunLogsStepResolves` (controlplane, image step ⇒ `wf-s1`, trace
  filter kept) + the e2e `--step ingest` narrowing.
- **rbac-scoped** → `TestScenarioRunLogsRBACScoped` (controlplane) — a caller bound to another
  namespace gets 403.

Extra guard/edge tests strengthen the surface: `TestScenarioBareNamespaceScanRejected` (the guard),
`TestScenarioRunLogsEmptyTrace` (legacy/traceless run ⇒ empty, not error), `TestRunLogsRouteAbsentWhenUnset`
(route 404s when no querier configured).

### ✅ Verified correct (keep it)

- **The guard is exactly the ADR's Decision.** `internal/funclog/logread/logread.go:84` requires
  `Namespace != "" && (Function != "" || TraceID != "")` — a bare namespace scan stays `Invalid`, so
  the namespace-wide mode is only ever reachable *with* a trace-id and can never become an unfiltered
  full-namespace dump. The trace filter is a post-decode predicate (`row.TraceID == q.TraceID`,
  line 134), and the prefix widens to `logs/<ns>/` only when `Function == ""` (line 97-100) — a pure
  prefix change reusing the existing merge/time-order/limit logic. Additive: empty `TraceID` is the
  ADR-0084 behavior unchanged.
- **Security claim holds.** `authorizeRunLogs` (logs.go:213) runs the ADR-0018 PEP for
  `get/WorkflowRun` in the path namespace *before* any read; the path params are typed
  `NamespaceName`/`ObjectName` so huma confines the prefix at the boundary; the querier sets
  `q.Namespace`/`q.TraceID` server-side (logs.go:120-121) — a client-supplied trace-id never widens
  scope. A trace-id selects within the authorized namespace, never grants.
- **Field ownership matches the Contract.** The querier owns `Namespace`+`TraceID`; the caller
  supplies `Since`/`MinSeverityNumber`/`Limit` and (for `--step`) a step *name* in `Function`,
  resolved via the run's pinned workflow (`resolveStepFunction`: `Ref`⇒the ref, `Image`⇒
  `<workflow>-<step>`, builtin/sub-workflow ⇒ Invalid with a clear reason) while keeping the trace
  filter — so `--step` returns *this* run's lines for that function.
- **Empty-trace path** returns `nil` (empty result), not an error (logs.go:117-119) — matches the ADR.
- **Additive & non-breaking**: `funcdctl logs <fn>` path (RegisterLogs) untouched; `renderLogLines`
  extracted and shared by both verbs; `go.mod` unchanged; the new route is registered only when a
  querier is wired, else 404. specgen + api_test register the stub so the route is in the committed
  OpenAPI (golden test green).
- **Conventions (ADR-0002)**: no `any`/`interface{}` in exported/port signatures; `api/fault` used
  throughout; ctx-first; `WorkflowRunLogQuerier`/`RunLogGetter`/`LogQuerier` are minimal ports.
- **ADR substance unchanged**: `git diff HEAD` on the ADR file is empty apart from the status being
  at `Reviewing` — no Context/Scenarios/Decision/Contracts mutation.

### Definition of Done

6/6 ADR Review-checklist items hold, plus the generic phase DoD (build/test/lint/mod green, every
Scenario a passing test, real behavior no stubs in the shipped path, tree matches the ADR surface,
additive, tracking consistent). No misses.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0106 (implementation) → pass, 0/0/0, 0 model-attributed,
DoD 6/6. See docs/reviews/model-scorecard.md.

### Recommendation

Sign off. Stamp ADR-0106 `Reviewing → Implemented`, advance the FEAT-0005 F67 row, and move the
board card to Done. Nothing loops back to the builder.
