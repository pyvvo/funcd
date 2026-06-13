#!/usr/bin/env python3
"""plan_waves.py — turn a hand-written build-dependency table into a *computed*,
internally-consistent roadmap: cycle check, build waves (topological tiers),
critical path, and a Mermaid graph generated FROM the edges.

Why this exists: a roadmap's realism lives in its dependency edges. If the graph is
drawn by hand it drifts from the table (missing/contradictory edges), waves get guessed,
and cycles hide. This script makes the graph ≡ the table by construction, proves there
are no cycles, and derives waves + critical path instead of asserting them.

Input: a JSON file (or stdin) of this shape — see references for a worked instance.

{
  "version": "v1",
  "accepted": ["ADR-0001", "ADR-0002"],      # already-done / pinned to wave 0
  "items": [
    {"id": "P-A", "title": "resource model",  "features": ["F03","F22"], "depends_on": ["ADR-0002"]},
    {"id": "P-C", "title": "store port",       "features": ["F05"],       "depends_on": ["P-A"]},
    ...
  ],
  "waves": {                                  # OPTIONAL — only for --check-waves
    "1": ["P-A", "P-D"], "2": ["P-C"], ...    # the author's hand-grouped presentation waves
  }
}

`depends_on` is the BUILD dependency (X must be *built* before this item). Every entry
must reference an `accepted` id or another item id.

Usage:
    python3 plan_waves.py plan.json                  # compute tiers, critical path, graph
    python3 plan_waves.py plan.json --mermaid-only    # just the validated-ready graph block
    python3 plan_waves.py plan.json --check-waves      # validate the author's "waves" grouping
"""
from __future__ import annotations

import json
import sys
from collections import defaultdict

sys.setrecursionlimit(10000)  # roadmaps are small, but harden against deep V2/V3 graphs


def die(msg: str) -> None:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(1)


def load(path: str | None) -> dict:
    raw = sys.stdin.read() if path in (None, "-") else open(path, encoding="utf-8").read()
    try:
        return json.loads(raw)
    except json.JSONDecodeError as e:
        die(f"input is not valid JSON: {e}")


def build(data: dict):
    """Return (accepted, items, deps, item_by_id). Validates ids + references."""
    accepted = list(data.get("accepted", []))
    items = data.get("items", [])
    if not items:
        die("no items in plan")

    ids = set(accepted)
    for it in items:
        if "id" not in it:
            die(f"item missing id: {it}")
        if it["id"] in ids:
            die(f"duplicate id: {it['id']}")
        ids.add(it["id"])

    deps: dict[str, list[str]] = {a: [] for a in accepted}
    item_by_id: dict[str, dict] = {a: {"id": a, "features": [], "title": a} for a in accepted}
    for it in items:
        for x in it.get("depends_on", []):
            if x not in ids:
                die(f"item {it['id']} depends on unknown id {x!r}")
        deps[it["id"]] = list(it.get("depends_on", []))
        item_by_id[it["id"]] = it
    return accepted, items, deps, item_by_id


def find_cycle(deps: dict[str, list[str]]) -> list[str] | None:
    WHITE, GRAY, BLACK = 0, 1, 2
    color = defaultdict(int)
    stack: list[str] = []

    def visit(n: str):
        color[n] = GRAY
        stack.append(n)
        for m in deps.get(n, []):
            if color[m] == GRAY:
                return stack[stack.index(m):] + [m]
            if color[m] == WHITE:
                c = visit(m)
                if c:
                    return c
        stack.pop()
        color[n] = BLACK
        return None

    for n in deps:
        if color[n] == WHITE:
            c = visit(n)
            if c:
                return c
    return None


def waves(accepted: list[str], deps: dict[str, list[str]]) -> dict[str, int]:
    """Earliest-possible tier: wave = 0 for accepted, else 1 + max(dep wave)."""
    wave: dict[str, int] = {}

    def w(n: str) -> int:
        if n in wave:
            return wave[n]
        if n in accepted:
            wave[n] = 0
            return 0
        wave[n] = 1 + max((w(x) for x in deps.get(n, [])), default=0)
        return wave[n]

    for n in deps:
        w(n)
    return wave


def critical_path(deps: dict[str, list[str]]) -> list[str]:
    """Longest dependency chain (by item count). Returns ids from root to leaf."""
    best_len: dict[str, int] = {}
    best_next: dict[str, str | None] = {}
    rdeps: dict[str, list[str]] = defaultdict(list)
    for n, ds in deps.items():
        for d in ds:
            rdeps[d].append(n)

    def length(n: str) -> int:
        if n in best_len:
            return best_len[n]
        ln, chosen = 0, None
        for m in rdeps.get(n, []):
            cand = length(m)
            if cand > ln:
                ln, chosen = cand, m
        best_len[n] = 1 + ln
        best_next[n] = chosen
        return best_len[n]

    for n in deps:
        length(n)
    start = max(deps, key=lambda n: best_len[n])
    chain, cur = [], start
    while cur is not None:
        chain.append(cur)
        cur = best_next[cur]
    return chain


def mermaid(accepted: list[str], items: list[dict], deps: dict[str, list[str]]) -> str:
    safe = lambda s: str(s).replace('"', "'")
    nid = lambda s: s.replace("-", "_")
    lines = ["flowchart TB"]
    for n in accepted:
        lines.append(f'    {nid(n)}["{n} ✓"]')
    for it in items:
        f = "+".join(it.get("features", [])) or "—"
        lines.append(f'    {nid(it["id"])}["{it["id"]} · {f}<br/>{safe(it.get("title", ""))}"]')
    lines.append("")
    for n, ds in deps.items():
        for d in ds:
            lines.append(f"    {nid(d)} --> {nid(n)}")
    return "\n".join(lines)


def check_waves(data: dict, deps: dict[str, list[str]], accepted: list[str]) -> None:
    """Validate the author's hand-grouped `waves`: no edge may sit inside a wave or point
    from a later wave to an earlier one. This is what stops a coarsened presentation from
    re-introducing the false-parallelism the earliest-tier computation prevents."""
    grouping = data.get("waves")
    if not grouping:
        die("--check-waves needs a top-level \"waves\" object mapping wave → [item ids]")

    wave_of: dict[str, int] = {a: 0 for a in accepted}
    for label, members in grouping.items():
        try:
            w = int(label)
        except ValueError:
            die(f"wave label {label!r} is not an integer")
        for m in members:
            wave_of[m] = w

    item_ids = [n for n in deps if n not in accepted]
    unassigned = [n for n in item_ids if n not in wave_of]
    intra: list[str] = []   # dependent shares a wave with its dependency
    backward: list[str] = []  # dependency sits in a LATER wave than its dependent (impossible)
    for n, ds in deps.items():
        if n not in wave_of:
            continue
        for d in ds:
            if d not in wave_of:
                continue
            if wave_of[d] == wave_of[n]:
                intra.append(f"{d} → {n} (both in wave {wave_of[n]}): {n} depends on {d}, so they cannot be built in parallel")
            elif wave_of[d] > wave_of[n]:
                backward.append(f"{d} (wave {wave_of[d]}) → {n} (wave {wave_of[n]}): dependency is in a LATER wave — impossible")

    ok = True
    if unassigned:
        print(f"WARN: {len(unassigned)} item(s) not placed in any wave: {', '.join(sorted(unassigned))}")
        ok = False
    if backward:
        print("VIOLATION — dependency scheduled after its dependent:")
        for v in backward:
            print(f"  ✗ {v}")
        ok = False
    if intra:
        print('VIOLATION — intra-wave edge (contradicts "everything within a wave is parallel"):')
        for v in intra:
            print(f"  ✗ {v}")
        ok = False
    if ok:
        print("WAVES OK — every dependency edge crosses from an earlier wave to a later one; the grouping is parallel-safe.")
        return
    print("\nFix: move the dependent to a later wave (or split the item). Re-run to confirm.")
    sys.exit(1)


def main() -> None:
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    flags = {a for a in sys.argv[1:] if a.startswith("--")}
    data = load(args[0] if args else None)
    accepted, items, deps, _ = build(data)

    cyc = find_cycle(deps)
    if cyc:
        die("dependency CYCLE — a roadmap with a cycle is unbuildable:\n  " + " -> ".join(cyc))

    if "--check-waves" in flags:
        check_waves(data, deps, accepted)
        return

    mer = mermaid(accepted, items, deps)
    if "--mermaid-only" in flags:
        print(mer)
        return

    wv = waves(accepted, deps)
    cp = critical_path(deps)
    cp_set = set(cp)
    by_wave: dict[int, list[str]] = defaultdict(list)
    for n, w in wv.items():
        if n not in accepted:
            by_wave[w].append(n)

    rdeps = defaultdict(list)
    for n, ds in deps.items():
        for d in ds:
            rdeps[d].append(n)
    sinks = [n for n in deps if n not in accepted and not rdeps.get(n)]
    terminal = [n for n in sinks if n in cp_set]            # final deliverable(s)
    parallel_leaves = [n for n in sinks if n not in cp_set]  # genuinely deferrable

    print(f"VALID — no cycles. {len(items)} items, {max(wv.values())} build tiers "
          f"(tier 0 = {len(accepted)} accepted/pinned).\n")
    print("## Build waves (computed earliest tier — within a tier, items are parallel-safe)\n")
    print("| Tier | Items |\n|---|---|")
    print(f"| 0 (done) | {', '.join(accepted) or '—'} |")
    for w in range(1, max(wv.values()) + 1):
        print(f"| {w} | {', '.join(sorted(by_wave[w]))} |")
    print(f"\n## Critical path ({len(cp)} items — the longest build chain; protect it)\n")
    print("  " + " → ".join(cp))
    print("\n## Terminal sink (final deliverable — NOT deferrable)\n")
    print("  " + (", ".join(terminal) or "—"))
    print("\n## Parallelizable leaves (nothing depends on them AND off the critical path — safe to defer)\n")
    print("  " + (", ".join(sorted(parallel_leaves)) or "—"))
    print("\n## Mermaid graph (generated from the edges — graph ≡ table by construction)\n")
    print("```mermaid")
    print(mer)
    print("```")
    print("\nValidate this block with the mermaid tool before pasting it into the plan.")
    print("Tip: add a \"waves\" object and re-run with --check-waves to prove any coarser grouping stays parallel-safe.")


if __name__ == "__main__":
    main()
