#!/usr/bin/env python3
"""
project-summary state reconciler — the driver behind the `project-summary` skill.

It reads the APPEND-ONLY sources of truth (the ADRs, the roadmap, the resource
kinds) and reports exactly how docs/PROJECT-SUMMARY.md drifts from them, so the
agent makes only minimal, ADR-driven edits. It NEVER edits the summary itself.

The contract this enforces:
  * ADRs are the source of truth and are append-only / immutable once Implemented.
  * A NEW ADR  -> APPEND a row to the summary's "Decision log" table. Never rewrite
    or reorder existing rows.
  * A SUPERSEDED ADR -> the one allowed edit to an existing row (mark it superseded).
  * the "Build status" section (any "(V1)/(V2)" version suffix) is recomputed every
    run from the roadmap.
  * Sections that a new ADR made stale (resource kinds, components) are surfaced
    so the agent can reconcile only what actually changed.

Usage:
  python3 .claude/skills/project-summary/state.py            # full drift report
  python3 .claude/skills/project-summary/state.py --check    # exit 1 if drifted (CI)
  python3 .claude/skills/project-summary/state.py --root DIR  # explicit repo root
"""
from __future__ import annotations
import argparse
import json
import re
import sys
from pathlib import Path

SUMMARY_REL = "docs/PROJECT-SUMMARY.md"
ADR_GLOB = "docs/adr/[0-9][0-9][0-9][0-9]-*.md"
PLAN_REL = "docs/roadmap/v1-plan.json"
KINDS_GLOB = "api/types/v1alpha1/*.go"
# Headings are matched by NAME (no section numbers) so inserting a section above
# them never breaks detection. Build status is matched by its STEM — the "(V1)"/"(V2)"
# version suffix is not hardcoded, so the heading can carry the current version.
BUILD_STATUS_HEADING = "## Build status"
DECISION_LOG_HEADING = "## Decision log (ADRs)"


def find_root(start: Path) -> Path:
    d = start.resolve()
    while True:
        if (d / "docs" / "adr").is_dir():
            return d
        if d.parent == d:
            sys.exit("error: could not locate repo root (no docs/adr/ found above %s)" % start)
        d = d.parent


def parse_adrs(root: Path) -> list[dict]:
    adrs = []
    for p in sorted(root.glob(ADR_GLOB)):
        num = p.name[:4]
        text = p.read_text(encoding="utf-8", errors="replace")
        title = ""
        m = re.search(r"^#\s*ADR-\d+:\s*(.+)$", text, re.MULTILINE)
        if m:
            title = m.group(1).strip()
        status = ""
        ms = re.search(r"^[-*]\s*\*\*Status\*\*:\s*(.+)$", text, re.MULTILINE)
        if ms:
            status = ms.group(1).strip()
        # Supersession is authoritative ONLY on this ADR's own Status line (a title's
        # "supersedes ADR-XXXX" is the OTHER direction and must not count).
        superseded_by = ""
        word = status.split()[0] if status else "?"
        if word.lower() == "superseded":
            msup = re.search(r"Superseded by \[?ADR-(\d+)", status, re.IGNORECASE)
            if msup and msup.group(1) != num:
                superseded_by = msup.group(1)
            word = "Superseded"
        adrs.append({"num": num, "file": p.name, "title": title,
                     "status": word, "superseded_by": superseded_by})
    return adrs


def summary_adr_nums(root: Path) -> set[str]:
    sp = root / SUMMARY_REL
    if not sp.exists():
        return set()
    text = sp.read_text(encoding="utf-8", errors="replace")
    # only count rows in the decision-log section, but linking anywhere counts as "present"
    return set(re.findall(r"adr/(\d{4})-[a-z0-9-]+\.md", text))


def load_plan(root: Path) -> dict | None:
    pp = root / PLAN_REL
    if not pp.exists():
        return None
    try:
        return json.loads(pp.read_text(encoding="utf-8"))
    except Exception as e:  # noqa: BLE001
        return {"_error": str(e)}


def resource_kinds(root: Path) -> list[str]:
    kinds: set[str] = set()
    for p in root.glob(KINDS_GLOB):
        if p.name.endswith("_test.go"):
            continue
        for m in re.finditer(r"\bKind([A-Z][A-Za-z0-9]+)\b", p.read_text(encoding="utf-8", errors="replace")):
            name = m.group(1)
            # drop obvious test/fixture kinds
            if "Roundtrip" in name or "EveryKind" in name or name in {"From", "Name"}:
                continue
            kinds.add(name)
    return sorted(kinds)


def summary_has_heading(root: Path, heading: str) -> bool:
    sp = root / SUMMARY_REL
    return sp.exists() and heading in sp.read_text(encoding="utf-8", errors="replace")


def main() -> int:
    ap = argparse.ArgumentParser(description="report PROJECT-SUMMARY drift vs the ADRs/roadmap")
    ap.add_argument("--root", default=None, help="repo root (default: search upward)")
    ap.add_argument("--check", action="store_true", help="exit 1 if drift exists")
    args = ap.parse_args()

    root = Path(args.root).resolve() if args.root else find_root(Path(__file__).parent)
    summary = root / SUMMARY_REL
    fresh = not summary.exists()

    adrs = parse_adrs(root)
    on_disk = {a["num"] for a in adrs}
    in_summary = summary_adr_nums(root)
    plan = load_plan(root)
    kinds = resource_kinds(root)

    new_adrs = sorted(on_disk - in_summary)
    stale_links = sorted(in_summary - on_disk)  # referenced but file gone (shouldn't happen)
    superseded = [a for a in adrs if a["status"] == "Superseded"]
    not_implemented = [a for a in adrs if a["status"] not in ("Implemented", "Superseded")]

    drift = bool(fresh or new_adrs or stale_links)

    print("=" * 72)
    print("PROJECT-SUMMARY reconcile report   (root: %s)" % root)
    print("=" * 72)

    if fresh:
        print("\n[CREATE] %s does NOT exist — author it from scratch (all sections)." % SUMMARY_REL)
    else:
        print("\nSummary present: %s" % SUMMARY_REL)
        print("  Build-status heading present  : %s" % summary_has_heading(root, BUILD_STATUS_HEADING))
        print("  Decision-log heading present  : %s" % summary_has_heading(root, DECISION_LOG_HEADING))

    print("\n-- ADRs --------------------------------------------------------------")
    print("  on disk: %d   in summary: %d" % (len(on_disk), len(in_summary)))
    if new_adrs:
        drift = True
        print("\n  [APPEND] new ADR(s) missing from the Decision log table (append-only — add a row each,")
        print("           and reconcile any section the ADR changed):")
        for a in adrs:
            if a["num"] in new_adrs:
                print("    + ADR-%s  [%s]  %s" % (a["num"], a["status"], a["title"]))
                print("        link: adr/%s" % a["file"])
    else:
        print("  [OK] every ADR on disk is already in the summary.")

    if superseded:
        print("\n  [VERIFY] superseded ADRs — the row may carry a 'superseded' note (the one allowed edit):")
        for a in superseded:
            print("    ~ ADR-%s superseded by ADR-%s — %s" % (a["num"], a["superseded_by"], a["title"]))

    if not_implemented:
        print("\n  [NOTE] ADRs not yet Implemented (their recap should say so, not claim built):")
        for a in not_implemented:
            print("    ? ADR-%s  status=%s  — %s" % (a["num"], a["status"], a["title"]))

    if stale_links:
        drift = True
        print("\n  [ERROR] summary links ADR(s) that no longer exist on disk: %s" % ", ".join(stale_links))

    print("\n-- Build status (recompute from the roadmap) ------------------------")
    if plan is None:
        print("  [WARN] %s not found — write Build status from the ADR statuses instead." % PLAN_REL)
    elif "_error" in plan:
        print("  [ERROR] %s is invalid JSON: %s" % (PLAN_REL, plan["_error"]))
    else:
        accepted = plan.get("accepted", [])
        items = plan.get("items", [])
        impl = [a for a in adrs if a["status"] == "Implemented"]
        print("  accepted/pinned (tier-0): %d  -> %s" % (len(accepted), ", ".join(accepted) or "—"))
        print("  ADRs Implemented        : %d" % len(impl))
        print("  remaining roadmap items : %d" % len(items))
        for it in items:
            print("    - %s :: %s :: deps %s" % (it.get("id"), it.get("title", ""), it.get("depends_on", [])))
        print("  -> Build status must state: ADR-0001…%s, %d accepted; remaining = %s."
              % (max(on_disk), len(accepted),
                 ", ".join(it.get("id", "?") for it in items) or "none"))

    print("\n-- Resource kinds (reconcile the kind list + the feature rows) ------")
    print("  %d kinds: %s" % (len(kinds), ", ".join(kinds)))

    print("\n" + "=" * 72)
    if fresh:
        print("VERDICT: CREATE — no summary yet; author all sections (see SKILL.md, 'Create from scratch').")
    elif drift:
        print("VERDICT: DRIFT — apply the [APPEND]/[VERIFY] edits above, then recompute the Build status section.")
    else:
        print("VERDICT: IN SYNC — every ADR is logged; only recompute the Build status section if the roadmap moved.")
    print("=" * 72)

    if args.check and drift:
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
