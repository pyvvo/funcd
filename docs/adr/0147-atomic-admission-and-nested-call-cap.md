# ADR-0147: Atomic admission and a nested-call in-flight cap

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: admission, control-plane, concurrency, links, invoke, worker-node, limits
- **Realizes**: [FEAT-0001/F108](../feat/0001-feat-v1.1.md) (concurrency-safe admission and a nested-call in-flight
  cap, a hardening follow-up to F33). F33 stays `implemented` on ADR-0064. Not user-facing, so no board card.
  Relates to [FEAT-0001/F32, F33, F42](../feat/0001-feat-v1.1.md) and [FEAT-0003/F47](../feat/0003-feat-data-platform.md).
- **Refines** (additions only, no back-link): [ADR-0063](0063-admission-framework.md) Contracts (adds the optional
  `NamespaceReading` marker and `Pipeline.ReadsNamespace`; `Admit` and `Handles` unchanged) ·
  [ADR-0064](0064-fn-to-fn-rpc-links.md) Decision 3 and its `Invoker` comment (`POST /invoke/{alias}` gains a 429
  `fault.ResourceExhausted` from the wrapping nested-cap Invoker; the `Invoker` interface unchanged).
- **Relates to**: [ADR-0146](0146-workflowrun-drive-model.md) (`maxStepsInFlight` off switch) · [ADR-0121](0121-declarative-referential-integrity-admission.md),
  ADR-0170 (conform; 0170's `deleteObjIf` takes this lock) · [ADR-0112](0112-ingress-protection-limits.md) (#87) ·
  [ADR-0073](0073-kv-bindings-and-subdomains.md)/[ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (quotas) · [ADR-0061](0061-funcd-daemon-config-file.md) (gains a key) ·
  [ADR-0148](0148-size-caps-answer-413.md) (413) · [ADR-0151](0151-external-invoke-deadline.md)/[ADR-0163](0163-retry-times-in-config.md) (also add `invoke` keys)
- **Supersedes**: None

## Context & Need

Issue #29 (CHAOS-015), re-verified on `1193be6`: write helpers admit, then write, with nothing spanning the two
(admissions read an unlocked `List()`). Concurrently, a 2-cycle was stored in 38–39/40 pairs, a dangling link in
36–37/40, 4 objects under a quota of 3 in 38–39/40. Nested calls are unbounded (fresh 30 s link timeout per hop):
one request into a stored 2-cycle ran 5,600–6,400 nested calls in 2 s.

**Purpose.** (1) Every admission rule a concurrent write could break holds under concurrent writes. (2) The daemon
bounds nested fn-to-fn calls (`POST /invoke/{alias}`) in flight to one Function.

## Scenarios

Races: 40 concurrent pairs through the API, each with a sequential control giving the same rejection.
- **scenario: link-cycle-race-rejected** — `PUT` link `aN → bN` and `PUT` link `bN → aN` at once: exactly one
  succeeds, the other fails `Invalid` ("would create a dependency cycle"); 0/40 stored cycles.
- **scenario: dangling-link-race-rejected** — `PUT` link `aN → bN` and `DELETE bN` at once: link stored and delete
  fails `Conflict`, or delete succeeds and link fails `Invalid`; 0/40 links to a missing Function.
- **scenario: quota-race-rejected** — quota 3, 2 KVStores (separately 2 Buckets) per namespace, two creates at once:
  one fails `Invalid` ("already holds the maximum 3"); none holds more than 3 — memory and Badger metastores.
- **scenario: inflight-loop-stopped** — A↔B cycle written straight to the store, cap 10, one external request to A:
  the 21st nested call is refused, non-2xx within 10 s, ≤ 20 reached a handler; a second request stops the same (every count returned to 0).
- **scenario: inflight-external-load-through-link** — cap 10, F calls H once (H holds 2 s): 10 external requests to
  F all succeed; with 11, F's handler gets one 429 from `context.invoke`, catches it, and all 11 callers get 200,
  exactly one body `{refused: 1}`.
- **scenario: inflight-fanout-at-cap** — cap 10, F calls H (holds 2 s) 10 times concurrently: all succeed.
- **scenario: inflight-fanout-over-cap** — 11 times: 10 succeed, 1 fails 429
  `urn:funcd:problem:resource-exhausted`, no `Retry-After`, detail `workernode.local.nested-cap: …` naming
  `default/H`, `10`, `invoke.maxNestedInFlight`; H ran 10 times; one Warn line;
  `funcd.invoke.nested.refused{namespace=default,function=H}` +1.
- **scenario: inflight-cap-raised** — `invoke.maxNestedInFlight: 20`, 11 concurrent calls: all succeed.
- **scenario: inflight-external-not-counted** — cap 2, F holds 2 nested calls to H, 5 external requests to H on the
  data-plane listener: all 7 succeed.

## Scope

**In**: the per-namespace admission lock (stops a stored cycle; quotas ride on it); the per-target nested cap (bounds
a cycle stored anyway); a fan-out example in `pyvvo/funcd-typescript`. Both parts close #29 and guard F33's acyclic
graph; they share no code, and one PR lands both.

**Out**: hop/depth counter; deadline propagation; reconciler-owned writes; multi-node (FEAT-0002); per-Function
cap override; migration (Decision 5).

## Constraints & Decision drivers

One daemon owns its metastore ([ADR-0065](0065-metastore-badger-engine.md)); the store stays below admission
(ADR-0063); keep ADR-0064, ADR-0121 and #87; the edge in-flight cap is off by default; no shim contract or behaviour change, no new dep.

## Alternatives considered

- Rejected: store-global write lock (all writes wait; inverts layering) · save-then-undo (bad state invokable) ·
  lock every namespace write (serializes unrelated writes) · hop counter (shim contract change) · reuse the ADR-0112
  limiter (#87 deadlock) · default 100 (decider wants a tight bound) · cap off (unbounded) · 503 `fault.Unavailable`
  (the cap is a per-target quota; nested 503 keeps meaning outage or timeout; ADR-0112's edge 429/503 split unchanged).
- Chosen: per-namespace lock on marked writes; default cap 10 (a loop of k Functions stops at k × 10 in flight).

## Decision

1. **Atomic admission.** `storeHandlers` holds one in-process lock per namespace. `createObj`, `replaceObj` and
   the delete helper (`deleteObj`, or `deleteObjIf` once ADR-0170 lands) take it, when `Pipeline.ReadsNamespace(gvk,
   op)` is true, before the `Old` fetch and admission, and release it after the store write returns. Marked: `link-validity` (Function Create/Update),
   `link-deletion-protection` (Function Delete), `kvstore-quota` (KVStore Create), `bucket-count` (Bucket Create);
   a quota is marked only while enabled (`maxPerNamespace > 0`). Waiting honours the request context; a cancelled
   wait is `fault.Unavailable` and writes nothing. Namespaces never wait on each other; cluster-scoped kinds have no
   marked admission.
2. **Relation to accepted ADRs.** Criterion: an admission is marked when two concurrent writes it admits can store a
   state no sequential order of them allows.
   - **ADR-0063**: additions only; `Admit`, `Handles` and Decision 4's wiring unchanged; the lock wraps the pipeline.
     `NamespaceReading` is a marker the pipeline reports, not a read seam; cross-resource admissions stay ordinary
     `Admission`s closing over their own store reader (ADR-0063 Alternatives). **ADR-0064**: rules unchanged, now hold under concurrency.
   - **ADR-0121**: no existence check added (`link-validity`'s target check is ADR-0064's, kept by its Decision 4);
     the bindings and owner references ADR-0121 moved to reconcile time stay admitted and reconciled.
   - **Unmarked, though they read other objects**: `kvstore-deletion-protection`, `bucket-deletion-protection` (a
     binding racing a delete ends `Ready=False`, as sequential delete-then-bind does); `workflowrun-contract` (checked
     against the old or new contract, as in a sequential order; run start stays the backstop).
3. **Nested-call in-flight cap.** The daemon's one `local.Invoker` (built once in `pkg/funcd`, shared by every
   sandbox's local API) is wrapped by `local.NewNestedCapInvoker`. It counts calls in flight per target Function
   (namespace + name) across all callers, from before the data-plane hand-off until return (cold wake included). At
   the cap the next call is refused at once with `fault.ResourceExhausted` (429), Op `workernode.local.nested-cap`;
   no `Retry-After` (a slot frees on return, not on a clock); Warn log and counter (Contracts). External requests
   never pass the Invoker, so are not counted; nested calls still skip the edge limiter (#87). The shim passes the
   error to the handler unchanged.
4. **Setting.** Daemon config key `invoke.maxNestedInFlight` (env `FUNCD_INVOKE_MAX_NESTED_IN_FLIGHT`), default 10;
   0 means the default, like `kvstore.maxStoresPerNamespace` and `WithPoolLimit` (unlike `workflow.maxStepsInFlight`:
   an off switch would bring back #29's loop); negative is `Invalid` at load. Library option `funcd.WithNestedInFlightCap`.
5. **Existing state.** No funcd runs in production, so no stored cycle, dangling link or over-quota namespace
   exists: no migration, no repair pass.

## Temporary workarounds

None.

## Contracts

### `internal/controlplane/admission` (new marker, new Pipeline method)

```go
// NamespaceReading marks an admission that needs the namespace's admission lock (ADR-0147).
type NamespaceReading interface {
	ReadsNamespace() bool
}
// ReadsNamespace: true when any admission handling (gvk, op) implements NamespaceReading and returns true.
func (p *Pipeline) ReadsNamespace(gvk v1.GroupVersionKind, op Operation) bool
func (linkValidity) ReadsNamespace() bool           { return true }
func (linkDeletionProtection) ReadsNamespace() bool { return true }
func (a kvStoreQuota) ReadsNamespace() bool         { return a.maxPerNamespace > 0 }
func (a bucketQuota) ReadsNamespace() bool          { return a.maxPerNamespace > 0 }
```

### `internal/controlplane` (new unexported lock; `NewStoreHandlers` signature unchanged)

```go
// nsLocks: per-namespace admission lock (ADR-0147); an entry lives while it has a holder or waiter.
type nsLocks struct {
	mu sync.Mutex
	m  map[v1.NamespaceName]*nsLock
}
type nsLock struct {
	slot chan struct{} // capacity 1; holding the slot is holding the lock
	refs int           // holder + waiters; guarded by nsLocks.mu
}

// lock waits for ns's slot or ctx's end. On ctx's end it returns
// fault.Wrapf(ctx.Err(), fault.Unavailable, "controlplane.admit", "wait for the admission lock of namespace %q", ns).
func (l *nsLocks) lock(ctx context.Context, ns v1.NamespaceName) (unlock func(), err error)

type storeHandlers struct {
	store store.Store
	authz auth.Authorizer
	admit *admission.Pipeline
	locks *nsLocks // ADR-0147
}
```

Write order, per helper: authorize → (createObj: stamp, name generation) → **lock if `admit.ReadsNamespace`** →
`Get` Old (update, delete) → `Admit` → store write → unlock.

### `internal/workernode/local` (new decorator; `Invoker`, `NewInvoker`, `NewHandler` signatures unchanged)

```go
// NestedCapOp is the fault Op of a nested call refused by the per-target in-flight cap (ADR-0147).
const NestedCapOp = "workernode.local.nested-cap"
// NewNestedCapInvoker: counts in one map under a mutex, entry deleted at 0; maxPerTarget ≥ 1. Over the cap,
// without calling inner: fault.ResourceExhaustedf(NestedCapOp,
// "%s has %d nested calls in flight, the cap set by invoke.maxNestedInFlight", target, maxPerTarget),
// and Int64Counter "funcd.invoke.nested.refused" +1 (attributes namespace, function).
func NewNestedCapInvoker(inner Invoker, maxPerTarget int, meter metric.Meter) Invoker
```

`NewHandler`'s error branch logs a refusal (`*fault.Error`, `Op == NestedCapOp`) at Warn as `fn-to-fn invoke
refused: nested in-flight cap` with `caller`, `alias`, `target`, `err`, **instead of** `fn-to-fn invoke failed` (never
both), and writes it with `fault.WriteProblem` (429, no `Retry-After`). Other errors keep the existing line.

### `pkg/funcd`, `internal/platform/config`, `cmd/funcd`

```go
// WithNestedInFlightCap (ADR-0147): 0 ⇒ the default (10); negative ⇒ fault.Invalid from New.
func WithNestedInFlightCap(n int) Option
const defaultNestedInFlightCap = 10

// internal/platform/config/config.go — the `invoke` group gains MaxNestedInFlight; the first of
// ADR-0147, ADR-0151 and ADR-0163 to land creates the group, the others add their keys to it:
type Config struct {
	// ...
	Invoke struct {
		MaxNestedInFlight int `json:"maxNestedInFlight,omitempty" env:"FUNCD_INVOKE_MAX_NESTED_IN_FLIGHT" validate:"min=0"`
	} `json:"invoke,omitempty"`
}
```

`cmd/funcd/main.go` passes `funcd.WithNestedInFlightCap(cfg.Invoke.MaxNestedInFlight)`. `pkg/funcd` wires
`local.NewNestedCapInvoker(local.NewInvoker(dpHolder), cap, meter)` into `local.NewManager`, with `meter` from
`c.telemetry.MeterProvider().Meter("funcd.invoke")`, or a no-op meter when telemetry is nil.

```json
{"type":"urn:funcd:problem:resource-exhausted","title":"Too Many Requests","status":429,
 "detail":"workernode.local.nested-cap: default/H has 10 nested calls in flight, the cap set by invoke.maxNestedInFlight"}
```

New dependencies: none (OTel `metric.Meter` is already one).

## Implementation plan

Three steps, so no funcd PR pins a funcd-typescript tag whose example config names a key funcd lacks:

1. **funcd PR, no `go.mod` change.** Part 1: `admission/{admission,pipeline,links,kvstore,bucket}.go`;
   `internal/controlplane/nslocks.go` (new) + test; `handlers.go` (the lock; `NewStoreHandlers` builds `locks`).
   Part 2: `internal/workernode/local/nestedcap.go` (new) + test; `local.go` (log line); `config.go` (+ test; `Invoke`
   merges in any order with ADR-0151's `defaultTimeout`, ADR-0163's `activationTimeout`/`reclaimInterval`);
   `cmd/funcd/main.go`; `pkg/funcd/{options,funcd}.go`; `examples/funcdconfig.yaml` (commented
   `maxNestedInFlight: 10  # 0 = default 10; cannot be disabled` under the shared `# invoke:` block, added if absent).
2. **`pyvvo/funcd-typescript` PR + release**, `examples/fn-to-fn`: `src/hold.ts` (waits `data.ms`, returns
   `{held: ms}`), `src/fanout.ts` (links `peer → hold`, calls it `data.n` times concurrently, returns
   `{ok, refused, detail}`, a refusal counted by `workernode.local.nested-cap`), built `.mjs`, manifests, and
   `invoke.maxNestedInFlight: 2` in its `funcdconfig.yaml`; `shim/src/invoke.ts` doc lists 429.
3. **funcd PR**: `go get` that tag (the only `go.mod` change); `scripts/lanes.yaml` (`fn-to-fn` gains `fanout`,
   `hold`); `e2e/fn-to-fn.venom.yml` (two cases). These depend on step 2's example files.

**Test plan** (each scenario is a test of the same name; races use 40 pairs released on a closed channel)

| Scenario | Unit | `pkg/funcd` (through the SDK) | Lima `fn-to-fn` lane (cap 2) |
|---|---|---|---|
| link-cycle-race-rejected | `internal/controlplane`: `NewStoreHandlers` + the real admissions over `store/memory` | memory + Badger metastores | — |
| dangling-link-race-rejected | as above | memory + Badger | — |
| quota-race-rejected | as above (KVStore and Bucket) | memory + Badger, KVStore and Bucket | — |
| inflight-loop-stopped | `local`: a fake inner that recurses A↔B — exactly 20 admitted, twice | e2e: `newShimRig` + `nodeFn`, cycle via `store.Update`; goroutine count back within a tolerance of baseline | — |
| inflight-external-load-through-link | — | e2e: 10 then 11 external POSTs to F, H held 2 s | — |
| inflight-fanout-at-cap | `local`: blocking fake inner | e2e: `nodeFn` F and H | `fanout` n=2 → ok 2 |
| inflight-fanout-over-cap | `local`: error kind, Op, message, counter (manual reader) | e2e: 429 body, no `Retry-After`, log line, H ran 10 | `fanout` n=3 → ok 2, refused 1 |
| inflight-cap-raised | `local`: cap 20 | e2e: `WithNestedInFlightCap(20)` | — |
| inflight-external-not-counted | — | e2e: 5 external POSTs during 2 held nested calls | — |

Contract tests: `nsLocks` (namespaces independent; cancelled wait → `Unavailable`, holder keeps the slot; map empties);
`Pipeline.ReadsNamespace` (true for the four; false for ConfigMap Create, KVStore Delete, WorkflowRun Create, and
disabled quotas); the cap's map empties; config (file and env, negative rejected); `New` rejects negative, 0 → 10.

**Definition of done**: build, lint, `go test ./...`, `go mod verify`, `just ci-full`, `just lima-example fn-to-fn`
green; every scenario a named passing test; no identity or path leak.

## Review checklist

- [ ] Lock as in Decision 1 (namespace scope, taken before the `Old` fetch) and released on every return path;
      exactly four admissions marked; no existence admission; store imports nothing from `controlplane`.
- [ ] Cap wraps only the local API Invoker; `dpHandler` unchanged; no `limit.Chain` on the nested chain (#87);
      refusal, counter, Warn line, config match this ADR.
- [ ] Every scenario a test of the same name; race tests keep sequential controls; YAML block style.

## Consequences

- (−) Marked writes in one namespace serialize. The cap limits ordinary traffic too (raise `invoke.maxNestedInFlight`,
  or set `server.limits.maxInFlight` for an edge 503); one busy caller can use a target's budget. A 2-Function loop
  still reaches 20 in flight. Risk accepted: both guarantees assume one daemon.

## Open questions

- Multi-node (FEAT-0002): the multi-node metastore and lattice ADRs. Fair or per-Function cap: a board idea if hit.

## References

- [#29](https://github.com/pyvvo/funcd/issues/29) (CHAOS-015) · [#87](https://github.com/pyvvo/funcd/issues/87) · ADR-0072, ADR-0141; others in the header.
