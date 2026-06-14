# V1 delivery plan — ADR sequencing to ship FEAT-0000

* **Status**: Active (living — update as ADRs are created/accepted/implemented)
* **Date**: 2026-06-13 (refreshed 2026-06-14 — ADR-0003 + ADR-0005 + **ADR-0006 (store) implemented →
  tier 0**; P-C graduated to ADR-0006; tiers recomputed, critical path still 7 items)
* **Realizes**: [FEAT-0000 (V1 — Agent-ready core)](../feat/0000-feat-v1.md)
* **Process**: [ADR-0000](../adr/0000-adr-process.md) · skills `/adr` → `/adr-judge` → `/adr-impl` → `/adr-impl-review`

## Purpose & how to read this

FEAT-0000 lists 24 features (numbering runs F01–F23 + F25; **F24 is unused** — a gap, not a missing
row); this file sequences the **ADRs** that realize them so V1 becomes implementable without
dead-ends or stalls. It is a *plan*, not a decision — it commits to no architecture (that is the
ADRs' job). It answers three questions: in what order must things be built, what can run in
parallel, and which ADRs must be created+accepted ahead of time so a build step never waits on an
undecided question.

**Two tracks — the core idea** (this design/build framing is this plan's own lens, not ADR-0000
vocabulary). Keep them distinct:

* **Design track** — create + judge + accept ADRs. Cheap, and ADRs for independent topics can be
  drafted in parallel; the real serialization point is *your* review/acceptance bandwidth.
* **Build track** — implement (`/adr-impl`) + review (`/adr-impl-review`). Serialized by **hard
  compile/runtime dependencies** (you can't build the controller before the store port exists).

The whole strategy: **keep the design track \~one tier ahead of the build track**, so every time the
build track finishes a tier, the next tier's ADRs are already Accepted and ready to implement. An ADR
only needs to be Accepted before *its own* feature is implemented — not before anything else.

> **ADR numbers here are proposed placeholders** (`P-D … P-T` remain unassigned). The `/adr` skill
> assigns the real sequential number at creation time, so if you create them in a different order the
> numbers differ. The stable identifiers are the **feature codes** (`F0x`) — track by those.
> `ADR-0001`/`ADR-0002`/`ADR-0003`/`ADR-0005`/`ADR-0006` are real and **Implemented** (tier 0, built).

## Proposed ADR slate

This table **is** the build-dependency input — the *Build-depends on* column lists plan-item ids and
is mirrored verbatim in `v1-plan.json`, which the analyzer consumes. Keep the two in
sync; re-run the analyzer (below) after any edit.

| Plan id | Proposed ADR working title | Realizes | Build-depends on (items) |
|----|----|----|----|
| ADR-0001 ✓ | Project setup, structure, Nix | F01 | — |
| ADR-0002 ✓ | Source-code conventions (`api/fault`, facade, lint graph) | F25 | ADR-0001 |
| ADR-0003 ✓ | Resource model & API typing (v1alpha1 kinds, `ObjectMeta` w/ resourceGroup+tags, spec/status, typed IDs/enums) | F03, F22 | ADR-0002 |
| ADR-0005 ✓ | API surface — code-first via huma (Go types → generated OpenAPI 3.1; SDK client deferred to P-R) | F02 | ADR-0003 |
| [ADR-0006](../adr/0006-store-database-layer-port.md) ✓ | Store / database-layer port (`store.Store`: **slatedb** UniFFI/cgo + a pure-Go memory engine; watch, generations, optimistic concurrency, `Encryptor` seam, contract suite) | F05, F21(db) | ADR-0003 |
| **P-D** | Blob / storage-layer port (`blob.Bucket`: gocloud mem/file/s3, contract suite) | F21(blob) | ADR-0002 |
| **P-E** | Messaging / bus port (`bus.Bus`: embedded NATS/JetStream + in-mem, accounts/ns, contract suite) | F06 | ADR-0002 |
| **P-F** | Observability — logger root (slog construction + injection from config) | F17 (logger) | ADR-0002 |
| **P-F2** | Observability — OTel metrics/traces + audit channel | F17 (telemetry) | ADR-0002 |
| **P-G** | Function runtime port (`runtime.Runtime`: process + containerd/runc curated runtimes; netns wiring + nftables lateral-deny; worker/shim seam) | F12 | ADR-0002 |
| **P-H** | Gateway port (embedded Lura + dev driver; route programming) | F10 | ADR-0002 |
| **P-H2** | Activator & scale-to-zero (request buffer + wake; idle reclaim driven by `scaling` spec) | F11 | P-H, P-G, P-J |
| **P-I** | Platform facade & lifecycle (`pkg/funcd` New/options/presets; composition root; crash-only boot reconcile; **the** `InMemory()` e2e-harness slice) | F04 | ADR-0006, P-D, P-E, P-F, P-G, P-H |
| **P-J** | Controller engine & feature-slice pattern (watch→diff→act→status; workqueue/backoff; registry) | F08 | ADR-0003, ADR-0006, P-E |
| **P-K** | Scheduler (trivial single-node placement behind a pluggable iface) | F09 | P-J |
| **P-L** | API server (authn: tokens+API keys; namespace RBAC; admission validate/default/quota; problem+json) | F07 | ADR-0005, ADR-0003, ADR-0006, P-J |
| **P-M** | Function contract, shape & lifecycle (CloudEvents handler; Knative-func shape; source-artifact deploys; shape-validation pipeline; apply→Revision→deploy→invoke→logs; manual replicas) | F13 | P-G, P-H, ADR-0003, ADR-0006, P-J |
| **P-N** | Service facade pattern + KV service (Service resource reconcile + binding + facade; KV as first instance). *V1 authz is namespace-scoped RBAC only — the* `auth.Authorizer` PDP/`Grant` enforcement is stubbed, deferred to V2. | F14 | P-E, ADR-0006, P-J, ADR-0003 |
| **P-O** | Blob service (storage layer exposed function-facing, on the service pattern) | F23 | P-D, P-N |
| **P-P** | Secrets service (Secret resource; encrypted at rest; env/tmpfs delivery; mem + s3-encryption drivers). *Same V1 authz note as P-N — namespace-scoped, no PDP yet.* | F15 | ADR-0006, P-N, P-G |
| **P-Q** | Eventing core (trigger capture → CloudEvents normalization; HTTP triggers via gateway; timer/cron EventSource) | F16 | P-E, P-H, P-G, P-M, P-J |
| **P-R** | CLI & SDK (`funcdcli` kubectl-style + Go SDK over generated client) | F18 | ADR-0005, P-L |
| **P-S** | Testing strategy & e2e harness (contract suites per port; e2e on `funcd.InMemory()`; per-driver + Linux-VM lanes; CI pipeline) | F20 | P-I, P-Q, P-H2 |
| **P-T** | Packaging & release (static binary, systemd units, install script, version stamping) | F19 | P-R, P-S |

Beyond the bootstrap, the slate held 22 ADRs (P-F/P-H were each split — see below); **ADR-0003, ADR-0005,
and ADR-0006 are now built (tier 0); P-C graduated to ADR-0006, so 19 placeholders remain (P-D…P-T)**. Merge candidates if the count feels heavy: fold **P-K (scheduler)**
into **P-J (controller)** (single-node placement is trivial); fold **P-O (blob service)** into **P-D
(blob port)** or **P-N (service pattern)**. Don't over-merge the big integrative ones (P-M, P-Q) —
they each carry real, separable decisions.

**Two deliberate splits** (from review): **P-H → P-H (gateway port, F10) + P-H2 (activator/scale-to-
zero, F11)** because F10 builds at tier 1 but F11 at tier 3 — one ADR straddling build tiers breaks
the one-ADR-one-implementation model, and the data-plane-ownership decision (gateway) is separable from the
drain/wake decision (scale-to-zero). **P-F → P-F (logger root) + P-F2 (OTel+audit)** because the
logger root is a real prerequisite of the composition root P-I (it builds the root `*slog.Logger`
from `internal/observability`), whereas OTel/audit is heavier and off the critical path. Note: the
*ports* in tier 1 do not depend on P-F — they take a stdlib `*slog.Logger` in their `Deps`; only
**P-I** imports `internal/observability` to construct it.

## Build dependency graph

`X → Y` = X must be *built* before Y. This graph is **generated from the slate table by**
`plan_waves.py` (`--mermaid-only`) and mermaid-validated — it cannot drift from the table, and it
shows every edge (no hand-pruning). Regenerate it whenever the slate / `v1-plan.json` changes.

```mermaid
flowchart TB
    ADR_0001["ADR-0001 ✓"]
    ADR_0002["ADR-0002 ✓"]
    ADR_0003["ADR-0003 ✓"]
    ADR_0005["ADR-0005 ✓"]
    ADR_0006["ADR-0006 ✓"]
    P_D["P-D · F21blob<br/>blob / storage port"]
    P_E["P-E · F06<br/>bus / messaging port"]
    P_F["P-F · F17a<br/>logger root"]
    P_F2["P-F2 · F17b<br/>OTel + audit"]
    P_G["P-G · F12<br/>runtime port"]
    P_H["P-H · F10<br/>gateway port"]
    P_H2["P-H2 · F11<br/>activator / scale-to-zero"]
    P_I["P-I · F04<br/>facade + lifecycle + harness"]
    P_J["P-J · F08<br/>controller engine"]
    P_K["P-K · F09<br/>scheduler"]
    P_L["P-L · F07<br/>API server"]
    P_M["P-M · F13<br/>function contract/lifecycle"]
    P_N["P-N · F14<br/>service pattern + KV"]
    P_O["P-O · F23<br/>blob service"]
    P_P["P-P · F15<br/>secrets service"]
    P_Q["P-Q · F16<br/>eventing core"]
    P_R["P-R · F18<br/>funcdcli + SDK"]
    P_S["P-S · F20<br/>testing + full e2e"]
    P_T["P-T · F19<br/>packaging"]

    ADR_0002 --> P_D
    ADR_0002 --> P_E
    ADR_0002 --> P_F
    ADR_0002 --> P_F2
    ADR_0002 --> P_G
    ADR_0002 --> P_H
    P_H --> P_H2
    P_G --> P_H2
    P_J --> P_H2
    ADR_0006 --> P_I
    P_D --> P_I
    P_E --> P_I
    P_F --> P_I
    P_G --> P_I
    P_H --> P_I
    ADR_0003 --> P_J
    ADR_0006 --> P_J
    P_E --> P_J
    P_J --> P_K
    ADR_0005 --> P_L
    ADR_0003 --> P_L
    ADR_0006 --> P_L
    P_J --> P_L
    P_G --> P_M
    P_H --> P_M
    ADR_0003 --> P_M
    ADR_0006 --> P_M
    P_J --> P_M
    P_E --> P_N
    ADR_0006 --> P_N
    P_J --> P_N
    ADR_0003 --> P_N
    P_D --> P_O
    P_N --> P_O
    ADR_0006 --> P_P
    P_N --> P_P
    P_G --> P_P
    P_E --> P_Q
    P_H --> P_Q
    P_G --> P_Q
    P_M --> P_Q
    P_J --> P_Q
    ADR_0005 --> P_R
    P_L --> P_R
    P_I --> P_S
    P_Q --> P_S
    P_H2 --> P_S
    P_R --> P_T
    P_S --> P_T
```

## Build waves (computed — `plan_waves.py`)

These are the **computed earliest tiers**: within a tier there are *zero* dependency edges, so every
item in it is genuinely parallel-safe. (An earlier hand-grouped version of this section was *wrong* —
it placed dependents in the same wave as their dependencies (P-K with P-J; P-O/P-P with P-N; P-T with
P-R/P-S), silently contradicting "parallel within a wave". The analyzer's `--check-waves` mode now
catches exactly that; the table below is regenerated, not hand-drawn.) A coarser capacity-based
grouping is fine **iff** it passes `--check-waves`.

| Tier | Items | Gate / why |
|----|----|----|
| **0 (done)** | ADR-0001, ADR-0002, ADR-0003, ADR-0005, ADR-0006 | Bootstrap + conventions + resource model + API surface + **store/metastore**. **All five Implemented.** Nothing compiles, is conventional, typed, served, or persisted without them. |
| **1** | P-D · P-E · P-F · P-F2 · P-G · P-H | Everything that needs only conventions + the resource model. The big parallel tier — sequence within it by capacity. (The store ADR-0006 built here then graduated to tier 0; the rest need only ADR-0002.) |
| **2** | P-I · P-J | Facade wires the tier-1 ports + logger root (**and ships the** `InMemory()` e2e-harness slice); controller needs store+bus+types. |
| **3** | P-H2 · P-K · P-L · P-M · P-N | Activator needs gateway+runtime+controller; scheduler needs controller; API server needs codegen+store+controller; function-lifecycle (first integrative feature) needs runtime+gateway+store+controller; service-pattern+KV needs bus+store+controller. |
| **4** | P-O · P-P · P-Q · P-R | blob/secrets services reuse the service pattern (P-N); eventing needs function-lifecycle+gateway+bus; CLI needs the API server. |
| **5** | P-S | Full testing + CI lanes — needs the harness (P-I), eventing (P-Q), and scale-to-zero (P-H2) to exercise. |
| **6** | P-T | Packaging — the terminal deliverable; everything must compile and run. |

**Critical path** (longest chain, from the analyzer): `ADR-0002 → P-E → P-J → P-M → P-Q → P-S → P-T`
(7 items). With ADR-0003 now built (tier 0), the live spine is **P-J → P-M → P-Q → P-S → P-T**, fed
by **P-E (bus)** — the remaining tier-1 port that gates P-J (the **store ADR-0006 is now built**, so its
arm of the P-J join is already satisfied; P-E is the live tier-1 critical feeder).
Protect P-E and the spine.

### Test sequencing (reconciling with the ADR-0000 "Implement" gate)

ADR-0000 requires each implementation to ship a *passing* test per Scenario. But the e2e harness
(`funcd.InMemory()`) does not exist until **P-I (tier 2)**, so a port implemented in tier 1 cannot
ship a *passing e2e* test — and the depguard graph forbids `tests/e2e/**` from importing
`internal/**` anyway. Resolution, applied per tier:

* **Tier 1 (ports, pre-harness)**: ship **contract/unit tests only** — port-local, available
  immediately, passing. A Scenario that is inherently end-to-end is recorded in the ADR and its
  acceptance test is deferred to the harness (note the deferral in the implementation report; the
  review gate attributes the gap to sequencing, not the model). *(ADR-0003 itself needed no
  deferral — its scenarios are pure type-model checks, all green now.)*
* **Tier 2**: **P-I ships the minimal** `InMemory()` e2e-harness slice as a first-class deliverable.
  From here, e2e tests run and the deferred Scenario tests from tier 1 are added against the
  public surface.
* **Tier 5**: **P-S** is the full testing strategy — CI lanes, the Linux-VM containerd lane,
  coverage. Read ADR-0000's "one passing test per Scenario" as "one passing test *at the right level*
  per Scenario; the e2e test once the harness exists."

## Design track — what to create + accept ahead

Stay one tier ahead. Acceptance (judge + your sign-off) is the bottleneck, so **batch the next
tier's ADR drafting** while the current tier builds:

* **Now** (tier 0 done — ADR-0001/0002/0003/0005 **+ ADR-0006 (store)** built): draft + accept
  the rest of the **tier-1 set** — **P-D, P-E, P-F, P-F2, P-G, P-H**. All are mutually independent
  (they need only ADR-0002), so they draft in parallel
  and are judged as they land. **Prioritize the remaining critical-path entry — P-E (bus), which gates the
  controller P-J** (the store ADR-0006 is now built) — then the two heavyweight ports **P-G (runtime)** and
  **P-H (gateway)** (Kata-deferral already settled for P-G; the data-plane-ownership reversal for
  P-H), which carry the most decision weight and deserve the most review attention.
* **While building tier 1**: accept the **tier-2 set** (**P-I, P-J**); and draft **P-S (testing
  strategy) early** — even though it builds at tier 5, its conventions shape how every feature writes
  tests, and the P-I harness slice (tier 2) should be designed against them.
* **While building tier 2**: accept the **tier-3 batch** (**P-H2, P-K, P-L, P-M, P-N**).
* Continue rolling one tier ahead through P-O…P-T.

## Critical path & the V1 exit-criterion spine

The longest build chain (computed by the analyzer) is
`ADR-0002 → P-E → P-J → P-M → P-Q → P-S → P-T` — 7 items. Protect it: a slip on any of these slips V1.
**P-E (bus)** is the live tier-1 critical feeder of P-J (the store **ADR-0006 is built**), and **P-M
(function-lifecycle)** and **P-Q (eventing)** are both on the path *and* the riskiest ADRs.

Map to the [FEAT-0000 exit criterion](../feat/0000-feat-v1.md) ("deploy a JS/Python function from a
source artifact whose handler consumes CloudEvents, reads a secret, persists KV, is invoked over
HTTP + timer, scales to zero, wakes"):

| Exit-criterion clause | Needs (build) |
|----|----|
| deploy JS/Python from artifact | P-G (runtime/curated) + P-M (shape/artifact/lifecycle) |
| handler consumes CloudEvents | P-M (contract) + P-Q (normalization) |
| reads a secret | P-P (secrets) |
| persists KV | P-N (KV) |
| invoked over HTTP | P-H (gateway) + P-Q (HTTP trigger) |
| invoked by timer | P-Q (timer EventSource) |
| scales to zero / wakes | P-H2 (activator) + P-J + P-G |
| all via API/CLI, observable | P-L (API) + P-R (CLI) + P-F (logger) |
| proven | P-S (e2e on `InMemory()` and full Linux-VM lane) |

So the exit criterion is satisfied by the **end of tier 4** (its features span tiers 1–4), **proven
at tier 5** (P-S), with **tier 6 (P-T packaging)** the final polish — not part of proving the feature
works.

## Parallelization & sequencing notes

* **Two remaining ports are the parallel win** (P-D blob, P-E bus) — independent after the
  conventions + types; the store (ADR-0006) is already built. (P-E is the critical-path feeder;
  P-D is off-path.)
* **Parallelizable leaves** (per the analyzer — nothing depends on them *and* off the critical path,
  so hand to a parallel builder or defer): **P-F2** (OTel/audit), **P-K** (scheduler), **P-O** (blob
  service), **P-P** (secrets). The blob *service* isn't on the exit-criterion path (which uses KV +
  secrets), so it can trail furthest. **P-T (packaging) is a leaf but the terminal sink** — the final
  deliverable, *not* deferrable.
* **The two riskiest ADRs** are P-M (function contract/shape/lifecycle — it ties runtime+gateway+
  types together and has open threads on streaming + the Python ASGI-vs-wrapped shape) and P-H
  (gateway — owns the data-path-ownership reversal). P-M is on the critical path; P-H is a tier-1
  feeder of both P-M and P-Q (critical-adjacent). Budget extra design + review on both.
* **P-F (logger root) is a hard prerequisite of P-I, not of tier-1 ports.** The composition root
  (`internal/app`, in P-I) imports `internal/observability` to build the root `*slog.Logger`; the
  ports only take that stdlib `*slog.Logger` in their `Deps` (no import of P-F). So P-F must land
  before P-I — but it does *not* gate the other tier-1 ports. P-F2 (OTel/audit) is fully off-path.
* **Worker** (`internal/worker`) and **network manager lateral-deny** fold into P-G (runtime) for
  V1 — no separate ADR. Full egress control is V2.

## Reproducing & maintaining this plan

The dependency table, graph, waves, and critical path are **computed**, not hand-drawn — the input is
`v1-plan.json` (mirrors the slate table's *Build-depends on* column):

```bash
python3 .claude/skills/roadmap-planner/scripts/plan_waves.py docs/roadmap/v1-plan.json
python3 .claude/skills/roadmap-planner/scripts/plan_waves.py docs/roadmap/v1-plan.json --check-waves
```

After any edit to the slate/`v1-plan.json`: re-run to refresh tiers + critical path, paste the
regenerated `--mermaid-only` graph (after mermaid-validating it), and run `--check-waves` if you
present any coarser grouping. The `--check-waves` mode is what would have caught the false-parallelism
this section originally shipped with. **When an ADR reaches** `Implemented`, move it from `items` to
`accepted` in `v1-plan.json` (it pins to tier 0) and re-run — that is what graduated ADR-0003 here.

## Caveats (living doc)

* Real ADR numbers are assigned by `/adr` at creation; reconcile this table's `P-x` ids to actual
  numbers as ADRs land, and link them. `P-A` reconciled to **ADR-0003** (now built); `P-B` → **ADR-0004** → **superseded by ADR-0005** (code-first via huma, now built); `P-C` → **ADR-0006** (store/database-layer port — slatedb via UniFFI/cgo; **Implemented** 2026-06-14 — built, graduated to tier 0).
* Tiers are dependency floors, not a schedule — within a tier, sequence by review bandwidth.
* If an ADR, once drafted, reveals a dependency this plan missed, update `v1-plan.json`, re-run the
  analyzer, re-validate the graph (newest accepted ADR still wins for architecture; this plan just
  tracks ordering).
* V2/V3 (egress enforcement, gVisor/WASM, Kata microVM, IAM, Terraform provider, …) get their own
  delivery plan when FEAT-0001 is scoped.


