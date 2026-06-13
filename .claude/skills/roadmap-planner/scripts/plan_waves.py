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
  ]
}

`depends_on` is the BUILD dependency (X must be *built* before this item). Every entry
must reference an `accepted` id or another item id.

Usage:
    python3 plan_waves.py plan.json
    python3 plan_waves.py plan.json --mermaid-only   # just the validated-ready graph block
"""
from __future__ import annotations

import json
import sys
from collections import defaultdict


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
    title: dict[str, str] = {a: a for a in accepted}
    feats: dict[str, list[str]] = {a: [] for a in accepted}
    for it in items:
        d = it.get("depends_on", [])
        for x in d:
            if x not in ids:
                die(f"item {it['id']} depends on unknown id {x!r}")
        deps[it["id"]] = list(d)
        title[it["id"]] = it.get("title", it["id"])
        feats[it["id"]] = it.get("features", [])
    return accepted, items, deps, title, feats


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
        d = deps.get(n, [])
        wave[n] = 1 + max((w(x) for x in d), default=0)
        return wave[n]

    for n in deps:
        w(n)
    return wave


def critical_path(deps: dict[str, list[str]]) -> list[str]:
    """Longest dependency chain (by item count). Returns ids from root to leaf."""
    best_len: dict[str, int] = {}
    best_next: dict[str, str | None] = {}
    # reverse edges: who depends on n
    rdeps: dict[str, list[str]] = defaultdict(list)
    for n, ds in deps.items():
        for d in ds:
            rdeps[d].append(n)

    def length(n: str) -> int:
        if n in best_len:
            return best_len[n]
        nxt = rdeps.get(n, [])
        if not nxt:
            best_len[n] = 1
            best_next[n] = None
            return 1
        ln = 0
        chosen = None
        for m in nxt:
            cand = length(m)
            if cand > ln:
                ln, chosen = cand, m
        best_len[n] = 1 + ln
        best_next[n] = chosen
        return best_len[n]

    for n in deps:
        length(n)
    start = max(deps, key=lambda n: best_len[n])
    chain = []
    cur: str | None = start
    while cur is not None:
        chain.append(cur)
        cur = best_next[cur]
    return chain


def mermaid(accepted, items, deps, title, feats) -> str:
    lines = ["flowchart TB"]
    safe = lambda s: s.replace('"', "'")
    for n in accepted:
        lines.append(f'    {n.replace("-", "_")}["{n} ✓"]')
    for it in items:
        f = "+".join(it.get("features", [])) or "—"
        lines.append(f'    {it["id"].replace("-", "_")}["{it["id"]} · {f}<br/>{safe(it.get("title", ""))}"]')
    lines.append("")
    for n, ds in deps.items():
        for d in ds:
            lines.append(f'    {d.replace("-", "_")} --> {n.replace("-", "_")}')
    return "\n".join(lines)


def main() -> None:
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    flags = {a for a in sys.argv[1:] if a.startswith("--")}
    path = args[0] if args else None
    data = load(path)
    accepted, items, deps, title, feats = build(data)

    cyc = find_cycle(deps)
    if cyc:
        die("dependency CYCLE — a roadmap with a cycle is unbuildable:\n  " + " -> ".join(cyc))

    mer = mermaid(accepted, items, deps, title, feats)
    if "--mermaid-only" in flags:
        print(mer)
        return

    wv = waves(accepted, deps)
    cp = critical_path(deps)
    by_wave: dict[int, list[str]] = defaultdict(list)
    for n, w in wv.items():
        if n not in accepted:
            by_wave[w].append(n)

    rdeps = defaultdict(list)
    for n, ds in deps.items():
        for d in ds:
            rdeps[d].append(n)
    leaves = sorted(n for n in deps if n not in accepted and not rdeps.get(n))

    print(f"VALID — no cycles. {len(items)} items, {max(wv.values())} build waves "
          f"(wave 0 = {len(accepted)} accepted/pinned).\n")
    print("## Build waves (computed — earliest tier each item can be built)\n")
    print("| Wave | Items |\n|---|---|")
    print(f"| 0 (done) | {', '.join(accepted) or '—'} |")
    for w in range(1, max(wv.values()) + 1):
        print(f"| {w} | {', '.join(sorted(by_wave[w]))} |")
    print(f"\n## Critical path ({len(cp)} items — the longest build chain; protect it)\n")
    print("  " + " → ".join(cp))
    print(f"\n## Leaf items (nothing depends on them — safe to parallelize / defer)\n")
    print("  " + (", ".join(leaves) or "—"))
    print("\n## Mermaid graph (generated from the edges — graph ≡ table by construction)\n")
    print("```mermaid")
    print(mer)
    print("```")
    print("\nValidate this block with the mermaid tool before pasting it into the plan.")


if __name__ == "__main__":
    main()
