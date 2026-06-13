# Roadmap methodology — how to make the plan *realistic*

A roadmap is only worth writing if it's true: the dependencies are real, the waves are derivable,
and following it actually delivers the feature. This file is the discipline that gets you there. The
mechanics (compute waves/critical-path/graph) are in `scripts/plan_waves.py`; this is the judgment
that feeds it.

## The two tracks (the core lens)

- **Design track** — create + judge + accept ADRs. Cheap; ADRs for independent topics draft in
  parallel. The real serialization point is **human acceptance bandwidth**, not drafting.
- **Build track** — implement + review. Serialized by **hard compile/runtime
  dependencies** (you can't build the controller before the store *interface* exists).

The plan's job is to keep design **one wave ahead** of build, so build never stalls on an
undecided question. An ADR need only be Accepted before *its own* feature is built.

## Realism rule #1 — every dependency edge must be a real *build* dependency, justified

A build edge `X → Y` means: Y's code will not compile or cannot run without X already built.
Ground each edge in the blueprint or an accepted ADR, and be able to state the reason in one line:

- "API server *implements the generated server interface*" → depends on the codegen item.
- "controller *watches* desired state and reacts to *change events*" → depends on store + bus.
- "facade's `WithRuntime`/`WithGateway` options *reference those port types*" → depends on runtime + gateway.
- "activator *wakes* a function" → depends on runtime.

Do **not** invent edges from vibes ("feels related"), and do **not** encode *design/acceptance*
order as a build edge. If an edge isn't groundable in code reality, drop it or ask the user. Soft
"nice to have first" is a design-track note, not a build edge.

## Realism rule #2 — compute the graph/waves/critical-path, never hand-draw them

Hand-drawn graphs drift from the dependency table (real edges missing, redundant edges kept),
waves get guessed, and cycles hide. Instead: write the dependency table as `plan.json` and run
`scripts/plan_waves.py`. It proves there are no cycles, derives the **earliest build tier** per
item, computes the **critical path** (longest chain), flags **leaf items** (parallelizable), and
emits a Mermaid graph that equals the table by construction. The table is authoritative; the graph
is generated from it. (You may present coarser hand-grouped "phases" in prose, but they must never
violate the computed tiers — a phase can merge adjacent tiers, never reorder them.)

## Realism rule #3 — one ADR = one coherent decision, and it must not straddle build tiers

If an ADR-item bundles features that the tool places in different tiers (e.g. a gateway port in
tier 1 and its scale-to-zero activator in tier 4), it would be implemented piecemeal — its port
early but its behavior much later — which breaks the one-ADR-one-implementation model. **Split it.**
Conversely, merge items so
trivial they don't carry a real decision (a single-node scheduler can ride inside the controller
ADR). The tool reveals straddles: when one item's features clearly belong to different tiers, split
and re-run.

## Realism rule #4 — plan to deliver the *feature*, not to "finish all ADRs"

Build the **exit-criterion spine**: take the feature-version doc's exit criterion, break it into
clauses, and map **every clause** to the item(s) that satisfy it. A clause with no item means the
plan (or the feat doc) is incomplete — stop and flag it. This guarantees the roadmap ends in a
working feature, and it identifies which items are *off* the exit path (defer/parallelize them).

## Realism rule #5 — sequence cross-cutting prerequisites before their dependents

Some things everything leans on: the error kernel, the logger root, the test harness. They are easy
to assume "already there" and thereby plan an impossible early step. Two that bite specifically here:

- **Test harness**: the process wants a passing test per scenario at implementation time, but an
  *e2e* harness (`funcd.InMemory()`) doesn't exist until the facade item is built. So pre-harness
  items ship **contract/unit tests only**; the e2e-harness slice is an explicit deliverable of the
  facade item; deferred e2e scenarios attach from then on. Make this a stated constraint, not a
  silent contradiction.
- **Logger root / error kernel**: a thing that *constructs* the root logger (or defines `api/fault`)
  is a prerequisite of the composition root — but the leaf components only depend on the stdlib
  type, not on the constructor package. Get this distinction right or you'll over-constrain the
  early waves.

## Realism failure modes (the checklist of what goes wrong)

- ❏ Edges invented, not grounded in the blueprint/ADRs.
- ❏ Hand-drawn graph contradicts the dependency table (missing real edges, kept redundant ones).
- ❏ A cycle (unbuildable) hiding in the table.
- ❏ An ADR-item straddling build tiers (port-now, behavior-much-later).
- ❏ A step that assumes a harness/prerequisite that its own wave hasn't built yet.
- ❏ "All ADRs done" mistaken for "feature delivered" — no exit-criterion spine.
- ❏ Ignoring the human-acceptance bottleneck (design not kept a wave ahead).
- ❏ Over-merging (an ADR with two real decisions) or over-splitting (an ADR with no decision).
- ❏ Hard-coded ADR numbers (they're assigned at creation — use feature codes + placeholders).

## Placeholder numbering

`/adr` assigns the real sequential ADR number at creation, so any number you write now is a guess.
Track by **feature code** (stable) and use placeholders (`P-A`, `P-B`, …) for unborn ADRs; mark
accepted ones with their real number. Note in the plan that placeholders reconcile to real numbers
as ADRs land.
