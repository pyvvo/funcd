# Verdict: changes requested — 0 blockers, 1 major (ADR-0045 implementation, model: claude-opus-4-8)

ADR-0045 is a behavior-preserving two-axis rename: `sandbox`→`worker` (the process) and
`Worker`→`WorkerNode` (the compute node). The build/lint/test gates are all green, both of the
ADR's authoritative grep gates are clean, the OpenAPI is regenerated, the blueprint vocabulary is
swept, and the supersession + feat-row bookkeeping is correct and additive-only. The work is ~95%
complete and genuinely behavior-preserving. The single Major is an **incompletely-applied axis-B
rename**: one of the five controlplane CRUD verbs — `DeleteWorker` — was left node-named while its
four siblings became `*WorkerNode`, and the gate-2 regex structurally cannot catch it. Two Minors
(public REST path/operationIds; one blueprint layout entry) are node-"Worker" residue the gate also
can't see.

## Verification run (evidence)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | `BUILD_OK` (exit 0) |
| tests | `go test ./...` | `TEST_EXIT=0` — all packages ok, **0 skips** in affected suites |
| lint | `go tool golangci-lint run ./...` | `0 issues.` (`LINT_EXIT=0`) |
| mod | `go mod verify` | `all modules verified` (exit 0) |
| grep gate 1 (no unit-"sandbox" in Go) | `grep -rin "sandbox" --include='*.go' .` | **empty** (exit 1) ✅ |
| grep gate 2 (no node-"Worker" in Go) | `grep -rn "\bWorker\b\|KindWorker\b\|GetWorker\b\|ListWorkers\b\|WorkerStatus\b" --include='*.go' . \| grep -vE "WorkerNode\|WorkerSpec\|worker_threads"` | **empty** (exit 1) ✅ — but see Major 1: the regex cannot match `DeleteWorker` |
| OpenAPI regen | `just generate` → `git diff --stat api/openapi/` | spec already regenerated; re-running produces no new change (working-tree spec has 14 `WorkerNode` refs, no stale `Worker:`/`WorkerStatus:` schema) ✅ |
| reports | `grep -rn "sandbox\|perSandboxMB" docs/reports/` | **empty** (exit 1) ✅ |
| identity | grepped the implementation surface (internal/, api/, blueprint.md, docs/feat, docs/reports, docs/adr/0045) for the local username / home path / email | **empty** (exit 1) ✅ |

## 🟡 Major 1 — `DeleteWorker` controlplane method left node-named · attribution: model

Four of the five controlplane CRUD verbs were renamed to `*WorkerNode`
(`GetWorkerNode`/`CreateWorkerNode`/`ListWorkerNodes`/`ReplaceWorkerNode`), but the fifth —
`DeleteWorker` — was left node-named across the interface, both drivers, and the call site:

- `internal/controlplane/controlplane.go:119` — `DeleteWorker(ctx context.Context, name v1.ObjectName) error` (the `Handlers` interface)
- `internal/controlplane/handlers.go:728` — `func (h *storeHandlers) DeleteWorker(...)`
- `internal/controlplane/stubs.go:827` — `func (s *StubHandlers) DeleteWorker(...)`
- `internal/controlplane/routes_rest.go:591` — `h.DeleteWorker(ctx, in.Name)`

This is a node-sense "Worker" **Go identifier** surviving in production code — precisely what scenario
`no-homonym-left` exists to eliminate ("no production identifier names a compute node 'Worker'"). It
escaped grep gate 2 because `\bWorker\b` has **no word boundary** inside `DeleteWorker`
(`echo DeleteWorker | grep -E '\bWorker\b'` → no match), and the gate's prefixed enumeration
(`GetWorker\b|ListWorkers\b|WorkerStatus\b`) happens to omit the `Delete` form. So a clean gate 2
does **not** prove this case — it can't see it.

**Attribution = model.** The Contracts rename-map row "controlplane `*Worker(...)` → `*WorkerNode(...)`"
unambiguously covers *all* controlplane `*Worker` methods, and the implementer demonstrably knew the
pattern (applied it correctly to the other four). Leaving one is an inconsistent application of an
unambiguous rule. (Secondary `adr` note: the ADR's Decision item 3 and Contract block both enumerate
only `Get/Create/List/Replace` and omit `Delete` — a non-exhaustive enumeration — but the rename-map
row and the scenario make the intent exhaustive, so the primary fault is the implementer's.)

**Fix (builder):** rename `DeleteWorker` → `DeleteWorkerNode` at all four sites; `just ci` stays green
(pure symbol rename).

## Minor

- **Public REST surface still names the node "Worker"** · attribution: model (with `adr` mitigation) ·
  `internal/controlplane/routes_rest.go:538` path `base := "/apis/funcd.io/v1alpha1/workers"` and the
  five operationIds `listWorkers`/`createWorker`/`getWorker`/`replaceWorker`/`deleteWorker`
  (routes_rest.go:541–588), faithfully reflected in `api/openapi/funcd.v1alpha1.yaml`
  (`/v1alpha1/workers`, `operationId: createWorker`, …). By the repo's own convention — path =
  lowercased plural of the kind, operationId = verb+Kind, as in `runtimeclasses`/`gateways` and
  `createRuntimeClass` — these should be `/workernodes` and `*WorkerNode`. The node-sense "Worker"
  thus survives on the **public, externally-visible** API. *Mitigation:* the ADR's Decision/Contract
  enumerated only the Go *methods* and the *schema/kind*, not the URL path or operationId strings, and
  the authoritative grep gate (string literals, not tokens) doesn't target them — so this is a
  gray-area the ADR under-specified. Worth fixing for consistency, but not a contract breach as
  written. (Renaming the path is an externally-visible API change; the builder may prefer to fold it
  here while the platform is pre-release, or note it explicitly as deliberately deferred.)
- **Blueprint node-agent layout entry left as `worker/`** · attribution: model · `blueprint.md:777`
  `│   ├── worker/   # node agent` and `:879` "Scheduler promoted out of `worker/`" / "the worker is
  a node agent". ADR Decision item 4 + Implementation-plan item 6 say to rename the `worker/`
  future-layout entry → `workernode/`. The file *inside* it was renamed (`worker.go`→`workernode.go`,
  blueprint.md:778) and `worker.proto`→`workernode.proto` (blueprint.md:686), but the directory entry
  and the §879 prose were not. Cosmetic (a future-layout tree for a package that doesn't exist yet),
  hence Minor, but it is a stale node-"Worker" layout entry the `blueprint-vocab` scenario flags.
- **`docs/PROJECT-SUMMARY.md` and `docs/install.md` are stale** · attribution: **not model** (out of
  ADR scope) · PROJECT-SUMMARY still says the `Worker` kind, links the **deleted** file
  `../api/types/v1alpha1/worker.go` (PROJECT-SUMMARY.md:144), and uses "sandbox" as the unit
  throughout (lines 100/101/113/121/141/163/167/178); install.md:43 says "function-sandbox lane".
  Neither file is in ADR-0045's "In" scope (code + blueprint + OpenAPI + reports + supersessions); the
  ADR's authoritative grep gates are Go-only + blueprint + reports. These are derived docs maintained
  by the `project-summary` skill and should be regenerated as a follow-up — *not* counted against the
  model on this ADR. Noted so it isn't lost.

## ✅ Verified correct (keep it)

- **Axis A complete in Go** — grep gate 1 empty; `internal/runtime/runtime.go:55` `WorkerSpec` (no
  `SandboxSpec`); `Create(ctx, spec WorkerSpec)` (runtime.go:85); `Instance` **kept** with all fields
  incl. `IP` (line 75) + `Port` (line 76) — no field lost, behavior-preserving. `bench.PerWorkerMB`
  + JSON `perWorkerMB` (bench.go:49) + report label "per-worker RSS (MB)" (report.go:58); test
  assertions carry only the symbol rename (bench_test.go:46-55), no value/logic change.
- **Axis B complete for the schema, kind, scheduler, and 4/5 controlplane verbs** —
  `api/types/v1alpha1/workernode.go` (`WorkerNode`/`WorkerNodeStatus`, `KindWorkerNode = "WorkerNode"`);
  `metadata.go` scope lists + dispatch (lines 33/43/54/265/294); `scheduler.go:26`
  `Placement.WorkerNode`; controlplane `Get/Create/List/Replace WorkerNode` + Tags `WorkerNode`.
- **OpenAPI regenerated** — schemas `WorkerNode`/`WorkerNodeStatus`, tags `WorkerNode`, `$ref`s all
  updated; `just generate` produces no further diff against the working tree.
- **Tests behavior-preserving** — `status_test`/`types_test`/`rbac_test`/`singlenode_test` diffs are
  pure `KindWorker`→`KindWorkerNode` / `p.Worker`→`p.WorkerNode` symbol renames; comparison values
  (`"local"`, `"node-0"`) unchanged; no assertion weakened or deleted; **0 skips**. All affected
  suites pass.
- **Blueprint vocabulary swept** (apart from the one Minor layout entry) — unit "sandbox"→"worker"
  everywhere; "Worker" component → "worker node"; `worker.proto`→`workernode.proto`; the **isolation
  noun** "WASM sandbox" correctly **preserved** at blueprint.md:444; crun/wasm/microVM prose kept.
- **Supersession bookkeeping correct and additive-only** — `git diff` of ADR-0011/0003/0017 shows
  **only** the inserted `Superseded in part by: ADR-0045` back-link lines; nothing else in those
  frozen ADRs' bodies changed. Each back-link is naming-only and maps old≡new.
- **Feat rows** F12/F03/F22/F09 each link ADR-0045 and stay `implemented`; F03's scope prose `Worker`
  kind → `WorkerNode` renamed (docs/feat/0000-feat-v1.md:42).
- **Out-of-scope correctly untouched** — ADR-0044's `shim/nodejs/pool.mjs` byte-identical (empty
  diff); `FUNCD_POOL_MANIFEST` literal intact (bench/pool.go:104, pool.mjs:3944); `worker_threads`
  preserved (it's the Node API, not the compute concept). Reports regenerated (no "sandbox"/"perSandboxMB").
- **Hygiene** — no identity leak in the implementation surface; ADR-0045 at `Reviewing`; module path
  `github.com/green-0-rabbit/funcd` throughout.

## Definition of Done

**5 / 7** ADR Review-checklist items hold:
1. Axis A (`WorkerSpec`, `Create(WorkerSpec)`, `Instance` kept with IP+Port) — ✅
2. Axis B (`v1alpha1.WorkerNode`, `KindWorkerNode`, `Placement.WorkerNode`, controlplane `*WorkerNode`,
   OpenAPI regen) — **partial**: `DeleteWorker` not renamed (Major 1); public path/operationIds still
   "Worker" (Minor).
3. No node-"Worker"/unit-"sandbox" identifier; both grep gates clean — **partial**: gates clean but
   `DeleteWorker` is a node-"Worker" identifier the gate cannot see (Major 1).
4. `bench.PerWorkerMB` + JSON + labels "per-worker" — ✅
5. Blueprint vocab (worker / worker node / function) — **mostly** (one stale `worker/` layout entry, Minor).
6. ADR-0011/0003/0017 carry the back-link; F12/F03/F09(/F22) link ADR-0045, stay `implemented` — ✅
7. Behavior unchanged; pre-existing tests pass on symbol rename only; `just ci` green; no new dep; no
   identity leak — ✅

Misses: items 2 & 3 (Major 1, model-attributed) — the same `DeleteWorker` gap; item 5 partial (Minor,
model). The two out-of-tree stale docs are **not** ADR-0045 DoD items (out of scope).

## Model scorecard

Recorded: claude-opus-4-8 on ADR-0045 (implementation) → changes-requested, 0 blockers / 1 major /
3 minors, 3 model-attributed (Major 1 + the path/operationId Minor + the blueprint-layout Minor; the
PROJECT-SUMMARY/install Minor is out-of-scope, not model), DoD 5/7. See
docs/reviews/model-scorecard.md.

## Recommendation

A tight, almost-complete behavior-preserving rename — the gates are green, the bookkeeping is
exemplary, and the substance is correct. It falls just short of a clean pass on **one** issue:
`DeleteWorker` is the lone unrenamed controlplane verb, leaving a node-"Worker" Go identifier the
gate-2 regex structurally can't catch. Loop back to the builder (`adr-impl`) to:

1. **(Major, must-fix)** rename `DeleteWorker` → `DeleteWorkerNode` at controlplane.go:119,
   handlers.go:728, stubs.go:827, routes_rest.go:591.
2. **(Minor, recommended)** rename the public REST path `/workers` → `/workernodes` and the five
   operationIds to `*WorkerNode` (then `just generate`), or explicitly record the deferral — to keep
   the public node surface consistent with the convention and free of node-"Worker".
3. **(Minor)** rename the blueprint `worker/  # node agent` layout entry → `workernode/` and update
   the §879 prose.
4. **(Follow-up, separate)** regenerate `docs/PROJECT-SUMMARY.md` (the dead `worker.go` link + the
   unit-"sandbox" prose) via the `project-summary` skill — not part of this ADR's DoD.

A re-review after (1) (and ideally 2–3) should pass. The ADR's `DeleteWorker` enumeration gap and the
gate-2 regex blind spot are worth a one-line note in any future "rename" ADR so the next one doesn't
inherit the same hole.

---

## Re-review (post-fix) — 2026-06-16 — verdict: PASS

The builder folded every actionable finding; re-verified mechanically:

- 🟡 **Major (DeleteWorker)** → **fixed**: `DeleteWorker`→`DeleteWorkerNode` across controlplane.go/handlers.go/stubs.go/routes_rest.go.
- **Minor (REST path/operationIds)** → **fixed**: `/workers`→`/workernodes`, `listWorkers`/`createWorker`/… → `*WorkerNode`, internal `createWorkerInput`/`workerOutput`/`workers` map → `*WorkerNode`; OpenAPI regenerated.
- **Minor (blueprint layout)** → **fixed**: `worker/`→`workernode/` (+ §879 prose).
- **Minor (PROJECT-SUMMARY.md / install.md)** → **out of ADR-0045 scope**; tracked as a `project-summary`-skill follow-up.

All gates re-run green: `go build` 0 · `go test ./...` exit 0 (36 ok) · `golangci-lint` 0 issues · `go mod verify` ok · grep gate A (unit "sandbox") **0** · grep gate B (node `Worker` identifiers) **0** (remaining lowercase "workers" are legitimate goroutine-workers + multi-node "worker nodes", not the renamed concepts) · OpenAPI staleness test passes · reports clean · blueprint `worker/` entry gone · identity clean. DoD **7/7**. **Verdict: pass.**
