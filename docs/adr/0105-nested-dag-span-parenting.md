# ADR-0105: Nested DAG span parenting — steps nest along their edges, fan-in via span links

- **Status**: Implemented
- **Date**: 2026-07-07 (Accepted 2026-07-07 after the judge pass; Implemented 2026-07-07 after the review-gate pass — no Blockers; folded 2 Majors (Resume restores
  the *in-flight* step's span-id **unconditionally before the Running→Pending reset**, with a mid-flight
  `resume-keeps-span-ids` test; a **Python** shim acceptance test `python-step-uses-provided-id`) + 5 Minors
  (edges read `n.dependsOn` **post-implicit-chaining** so only the first spec step is a true root; predecessor
  span-ids resolved from `rec.Steps[]`; the onFailure handler is **not** pre-minted; softened the retry-dedup
  wording; a not-a-security-boundary note))
- **Deciders**: green-0-rabbit
- **Tags**: workflow, observability, traces, otel, span-links, dag, dispatch, trace-context
- **Realizes**: [FEAT-0005/F67](../feat/0005-feat-workflow-engine.md) — completing the trace side: a **fully
  DAG-shaped waterfall** (step spans nest along their dependency edges)
- **Relates to**: [ADR-0101](0101-trace-capture-invocation-span.md) (the shim's per-invocation span — **extended
  here** to *use* an engine-provided span-id + attach links, additively) · [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md)
  (the dispatch traceparent this repoints from the run root to the predecessor) · [ADR-0103](0103-engine-emitted-run-root-span.md)
  (the run-root span root steps still parent on) · [ADR-0094](0094-workflow-engine-core.md) (the engine/dispatch/DAG)

## Context & Need

**Purpose**: make each step span **nest under the step it depends on**, so a run's trace mirrors its actual DAG —
`ingest → score → report` renders as a nested chain, not three siblings under the run root. **Callers**: whoever
reads a run's trace to see causality/latency along the pipeline (operator / agent author).

**Why now**: ADR-0102/0103/0104 give one trace per composition, but every step span parents on the **run root**
(flat, [engine.go dispatchStep sets ParentSpanID = rec.RootSpanID](../../internal/workflow/engine.go)) — the
waterfall is one level deep and doesn't show that `score` waited for `ingest`. Nested parent-child is *the* OTel
convention (a trace **is** a span tree); the flat model was the ADR-0102 simplification because the **engine did
not own step span-ids** — F51's shim mints them inside the sandbox. This ADR closes that: the engine **pre-mints**
a span-id per step and the shim **uses** it, so the engine can parent each step on its predecessor. A DAG is not a
tree, so **fan-in** joins (a step with >1 dependency) use the OTel-standard mechanism — **span links** — for the
non-primary edges (a span has exactly one parent).

## Scenarios

Each becomes a named acceptance test.

- **scenario: root-step-parents-on-run-root** — *Given* a **true** root step (no dependency **after implicit
  chaining** — the first spec-order step), *Then* its span's `ParentID` = the run's `RootSpanID` (ADR-0103 root),
  unchanged.
- **scenario: step-parents-on-predecessor** — *Given* `a → b` (b depends on a), *Then* b's span's `ParentID` =
  **a's** span-id (b nests under a), not the run root.
- **scenario: fan-in-links-non-primary** — *Given* `a, b → merge` (merge depends on a then b), *Then* merge's span
  `ParentID` = **a's** span-id (the primary/first edge) and it carries an OTel **span link** to **b's** span-id.
- **scenario: engine-owns-span-id** — *Given* a step dispatch, *Then* the step function's emitted span uses the
  **engine-provided** span-id (the shim uses the `X-Funcd-Span-Id` header rather than minting its own).
- **scenario: retries-share-step-span** — *Given* a step that retries, *Then* every attempt dispatches the **same**
  engine-minted span-id (a step is one span; the attempt count is an attribute), so a successor's parent is stable.
- **scenario: resume-keeps-span-ids** — *Given* a run resumes after a crash with a step that was **mid-flight**
  (`Running`), *Then* the re-dispatched step **and its successors** keep the **persisted** per-step span-ids
  (restored, never re-minted), so no parent edge dangles across the restart.
- **scenario: direct-invoke-unchanged** — *Given* a plain (non-workflow) function invoke with no `X-Funcd-Span-Id`
  header, *Then* the shim **mints** its span-id as before (ADR-0101 behavior is unchanged).
- **scenario: python-step-uses-provided-id** — *Given* the **Python** shim, *When* a step is dispatched with an
  `X-Funcd-Span-Id` header (+ links), *Then* its emitted span uses that id and attaches the links — parity with Node.
- **scenario: e2e-dag-shaped-waterfall** — *Given* a real `a → b`, and a real `a, b → merge` fan-in over the shim
  platform, *Then* the captured spans in blob nest along the edges: b parents on a; merge parents on a and links b.

## Scope

**In**: (a) the engine **pre-mints a span-id per DAG step** at run start, persisted in `StepState` (restored on
Resume, never re-minted); (b) each step dispatch carries its **own** span-id (`X-Funcd-Span-Id`), a **parent** =
the **primary predecessor**'s span-id (the first `dependsOn`; the run root for a root step) on the `traceparent`,
and **links** = the other predecessors' span-ids (`X-Funcd-Span-Links`); (c) the **shim uses** the provided
span-id (else mints, additive) and attaches the links to its span; (d) `funclog.Span` gains `Links` and the
`ptrace` marshaler emits OTel span links; (e) the run-root span (ADR-0103) and the run trace (0102) are unchanged
— root steps still parent on the run root.

**Out** (deferred / unchanged):
- **A "best" primary-parent policy** — V1 picks the **first** `dependsOn` (spec order) as the primary parent and
  links the rest. Data-flow-aware primary selection (e.g. the edge whose output dominates the input) is a later
  refinement; first-edge is deterministic and correct-as-a-tree.
- **Per-attempt step spans** — a step is **one** span across retries (shared engine-minted id); representing each
  attempt as its own span is out (the attempt count rides as a span attribute, ADR-0101 already stamps it).
- **Sub-workflow step edges** — a `workflow:` step is represented by the child **run** span (ADR-0104); its DAG
  position (parent = its predecessor) is honored via the same span-id, but the child's *internal* steps parent on
  the child run root (ADR-0104), not across the boundary. Cross-boundary step nesting stays a follow-on.
- Metrics; the describe-lineage increment (ADR-0100).

## Constraints & Decision drivers

- **Follow the OTel convention** — parent-child is the trace tree; fan-in uses **span links** (a span has one
  parent). No invented mechanism; `ptrace` already supports links.
- **The engine must own step span-ids** — nesting `b` under `a` requires knowing `a`'s span-id. F51 mints inside
  the sandbox, so the engine **provides** the id and the shim uses it. This is an **additive** extension of
  ADR-0101 (header absent ⇒ the shim mints, as today — direct invokes are unchanged).
- **Stable across retries + Resume** — one span-id per step, minted once, persisted; every attempt and every
  resumed step reuse it, so a successor's parent edge never dangles.
- **Additive / non-breaking** — a workflow with only root steps is unchanged (all parent on the run root); the new
  dispatch headers and the `Span.Links` field are additive; `go.mod` unchanged.
- **Not a security boundary** — the engine-provided span-id grants nothing; the `Resource` identity stays
  host-stamped and spec-as-grant (declared-target authorization, `dispatch.go` `grant.Allow`) is unchanged.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Shim returns its minted span-id** (response header), engine records it, parents successors on it | Keeps the shim minting — but the engine learns `a`'s id only *after* `a` completes, complicating pre-computation and Resume, and per-attempt ids make a successor's parent ambiguous. Rejected: the engine **pre-minting** and providing the id is simpler (the full edge graph is known up front) and gives one stable span per step. |
| **Multiple parents at a fan-in** | An OTel span has exactly **one** parent; two parents is not representable. Span **links** are the standard fan-in mechanism. Rejected as non-conformant. |
| **Keep flat (run-root parent) — status quo** | The waterfall is one level deep; it doesn't show the DAG. Exactly the gap this closes. |
| **Emit step spans from the engine** (not the shim) | The engine would have to fabricate the function's execution span without being in the sandbox (no real timing/status). Rejected: the shim owns the invocation span (ADR-0101); the engine only supplies its id + edges. |

## Decision

The engine owns per-step span-ids and the edge graph; the shim uses the provided id and links.

1. **Pre-mint (engine).** At run start (`execute`), after building the scheduling state, the engine mints a
   span-id (`crypto/rand`, hex16) for **each** DAG step, stored on the `stepNode` and persisted to
   `StepState.SpanID`. `rebuildState` (Resume/recovery) **restores** `n.spanID` from the record
   **unconditionally — at the top of its per-step loop, *before* the `Running → Pending` reset** — so the very
   step being re-dispatched keeps its id and its successors' parent edges never dangle; ids are never re-minted.
   (The **onFailure handler is not pre-minted** — nothing parents on it; it mints its own id and parents on the
   run root, unchanged.)
2. **Edges (engine).** `dispatchStep` reads the step's **post-implicit-chaining** dependencies `n.dependsOn` (the
   same set the DAG scheduler uses — `newRunState` chains a spec step with no explicit `dependsOn` to the previous
   step, so only the **first** spec step is a true root). It computes: the step's own `SpanID` = its minted id;
   the **primary parent** = the span-id of the **first element** of `n.dependsOn`, or the run `RootSpanID` when
   `n.dependsOn` is empty (a true root); the **links** = the remaining `n.dependsOn` steps' span-ids. A
   predecessor's name→span-id is resolved from `rec.Steps[].SpanID` (populated by the pre-mint `persist` before
   `drive`).
3. **Propagate (dispatch).** `DispatchRequest` carries `SpanID` and `Links` (and the existing `ParentSpanID`, now
   the primary predecessor). `Dispatch` sets `traceparent: 00-<trace>-<primaryParent>-01` (as today), plus
   `X-Funcd-Span-Id: <spanId>` and, when non-empty, `X-Funcd-Span-Links: <id>,<id>,…`.
4. **Use + link (shim).** The per-invocation span (ADR-0101) **uses** `X-Funcd-Span-Id` as its span-id when
   present (else mints — direct invokes unchanged), and attaches each `X-Funcd-Span-Links` id as an OTel span
   link (same trace). Logs emitted in the step correlate to the engine-provided span-id.
5. **Marshal (funclog).** `funclog.Span` gains `Links []string`; the NDJSON span wire gains `links`; `ptrace`'s
   marshaler emits `span.Links()` (each `TraceID` = the span's trace, `SpanID` = the linked step's id).

## Temporary workarounds

- **First-edge primary parent** — the primary parent is the first `dependsOn` (deterministic); a data-flow-aware
  choice is a later refinement. *Exit*: none required for correctness (the trace is a valid tree + links).
- **One span per step across retries** — retries reuse the step span-id (duplicate `(traceId, spanId)` span
  records in blob; OTLP backends **typically collapse them to one span per id, though behavior varies** —
  duplicate ids are not strictly conformant). *Exit*: per-attempt spans if operators need to see each attempt distinctly.

## Contracts

```go
// runstate.StepState gains (ADR-0105): the engine-minted span-id for this step, so a successor parents on it and
// Resume keeps the edge stable across a restart.
SpanID string `json:"spanId,omitempty"` // 16 hex; minted once at run start, restored on Resume

// stepNode (engine-internal) gains: spanID string  // the step's pre-minted span-id (mirrors StepState.SpanID)

// DispatchRequest gains (engine.go): the step's own span-id + its fan-in links (ParentSpanID already exists,
// now the PRIMARY predecessor's span-id rather than the run root).
SpanID string   // 16 hex; the step function uses this as its span-id (X-Funcd-Span-Id)
Links  []string // 16-hex span-ids of the non-primary fan-in predecessors (X-Funcd-Span-Links)

// funclog.Span gains: Links []string  // 16-hex span-ids linked in the SAME trace (fan-in edges)

// mintSpanID returns a fresh 8-byte span-id as hex16 (crypto/rand); "" on the near-impossible rand error.
func mintSpanID() string
```

**Dispatch headers** (added to the existing `traceparent` + `X-Funcd-Attempt`):

```
X-Funcd-Span-Id:    <hex16>            # the step's engine-minted span-id; the shim uses it as its span-id
X-Funcd-Span-Links: <hex16>,<hex16>,…  # non-primary fan-in predecessors (omitted when none)
```

**Trace shape** — a fan-in `a, b → merge`:

```
run                    (INTERNAL root, ADR-0103)
 ├─ a                  parent = run root
 ├─ b                  parent = run root
 └─ merge              parent = a (primary edge) ; link → b (span link)
```

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | the ADR-0094 engine (DAG `dependsOn`, dispatch, run-state, Resume) · the ADR-0102 traceparent + ADR-0103 run root · `crypto/rand` |
| Exposes | step spans nested along their DAG edges (parent = predecessor; fan-in via OTel span links) — a fully DAG-shaped run waterfall |
| New deps | **none** (`crypto/rand` stdlib; `ptrace` links already in the vendored `pdata`); no `go.mod` change |

## Implementation plan

- **Files (Go)**: `internal/workflow/state.go` (`stepNode.spanID`); `internal/workflow/engine.go` (`mintSpanID`;
  pre-mint per-step span-ids in `execute`; `persist` writes `StepState.SpanID`; `rebuildState` restores it;
  `dispatchStep` computes own-id/primary-parent/links and sets them on `DispatchRequest`); `internal/workflow/runstate/runstate.go`
  (`StepState.SpanID`); `internal/workflow/dispatch.go` (`DispatchRequest.SpanID`/`Links`; set `X-Funcd-Span-Id`
  + `X-Funcd-Span-Links`); `internal/funclog/span.go` (`Span.Links`); `internal/funclog/route.go` (span wire
  `links`); `internal/funclog/tracesink.go` (`marshalTraceOTLP` emits `s.Links()`). No `go.mod` change.
- **Files (shims)**: `shim/nodejs/src/tracespan.ts` (`startSpan` accepts a provided span-id + links; the wire
  record carries `links`) + `shim.ts` (pass `X-Funcd-Span-Id`/`X-Funcd-Span-Links` headers) + `pool.ts`;
  `shim/python/src/funcd_shim/tracespan.py` + the invoke handlers. Rebuild `shim.mjs`/`pool.mjs`.
- **Test plan** (one named test per Scenario):
  - engine unit (capturing dispatcher): `root-step-parents-on-run-root` (the first spec step → ParentSpanID ==
    run root), `step-parents-on-predecessor` (b's DispatchRequest ParentSpanID == a's SpanID),
    `fan-in-links-non-primary` (merge ParentSpanID == a, Links == [b]), `retries-share-step-span` (both attempts
    carry the same SpanID), `resume-keeps-span-ids` (a persisted record with a **`Running`** step → Resume
    re-dispatches it with the **persisted** SpanID, and its successors keep theirs).
  - dispatch unit: the `X-Funcd-Span-Id`/`X-Funcd-Span-Links` headers are set from a DispatchRequest; a plain
    invoke (no SpanID) sets no span-id header.
  - funclog unit: a `Span` with `Links` marshals to a ptrace span with `span.Links()` carrying the ids (same trace).
  - shim unit: **Node** `engine-owns-span-id` (a provided `X-Funcd-Span-Id` → the span uses it) + links attached +
    `direct-invoke-unchanged` (no header → mints); **Python** `python-step-uses-provided-id` (parity — the provided
    id is used + links attached).
  - **in-process e2e** (`pkg/funcd`): a real `a → b` and an `a, b → merge` workflow → read `blob/traces`, assert
    b's span parents on a's span-id, and merge's span parents on a with a link to b (`e2e-dag-shaped-waterfall`).
  - **Venom** (the containerd workflow lane): assert a step span's `parentSpanId` equals a predecessor step's
    `spanId` (the orders `score` step nests under `ingest`).
- **Definition of done**: `go build/test/lint` + `go mod verify` green; every Scenario a passing test; the
  in-process e2e + the workflow Venom lane green; the ADR-0101/0102/0103/0104 traces e2es still pass (direct
  invoke + run/composition nesting unchanged); shim bundles rebuilt; identity/path grep clean; `just ci` green.

## Review checklist

- [ ] A root step's span parents on the run root; a dependent step's span parents on its **primary predecessor**'s
      span-id (not the run root).
- [ ] A fan-in step parents on the **first** `dependsOn`'s span and carries OTel **span links** to the others.
- [ ] The step function's span uses the **engine-provided** `X-Funcd-Span-Id`; a direct invoke (no header) mints
      (ADR-0101 unchanged).
- [ ] One span-id per step across retries; per-step span-ids **persisted** and **restored** on Resume (never
      re-minted).
- [ ] `funclog.Span.Links` marshals to `ptrace` span links (same trace); the NDJSON wire carries `links`.
- [ ] Additive: new dispatch headers + `Span.Links` + `StepState.SpanID`; the run root / run trace / composition
      nesting (0102/0103/0104) unchanged; `go.mod` unchanged; no logs regression.

## Consequences

- **(+)** **A fully DAG-shaped run waterfall** — step spans nest along their edges; an operator reads causality and
  per-edge latency down the pipeline, with fan-in joins expressed as span links (the OTel convention).
- **(+)** **Completes the trace arc** — with ADR-0101/0102/0103/0104, a composition is one trace whose shape is the
  actual (sub-)workflow DAG.
- **(+)** **No new dependency, additive** — engine-provided ids + links; direct invokes and the prior nesting are
  unchanged.
- **(−)** **The shim now depends on an engine-provided id** for workflow steps — a thin new coupling on the private
  dispatch headers (still additive: absent ⇒ mint).
- **(−)** **First-edge primary parent** (not data-flow-aware) and **one span per step** across retries — both
  documented V1 simplifications with clean exits.

## Open questions

- **Primary-parent policy** — first-edge is deterministic; a data-flow-aware choice (or making fan-in *all*-links
  under the run root) is a display refinement, revisitable once real traces are viewed.
- **Per-attempt step spans** — if operators want each retry attempt as a distinct span, the engine would mint per
  attempt and parent the successor on the successful one; deferred.

## References

- [ADR-0101](0101-trace-capture-invocation-span.md) — the shim span extended (use a provided id + links).
- [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md)/[ADR-0103](0103-engine-emitted-run-root-span.md)/[ADR-0104](0104-cross-subworkflow-trace-linking.md)
  — the run trace, run-root span, and composition nesting this completes.
- [W3C Trace Context](https://www.w3.org/TR/trace-context/); [OTel span links](https://opentelemetry.io/docs/concepts/signals/traces/#span-links).
- FEAT-0005/F67 (one run = one trace → the full DAG waterfall).
