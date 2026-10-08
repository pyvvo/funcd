"""Wait until an open funcd PR, whatever its branch, needs action (green, red or conflicting), then print it and exit.

Usage: watch-prs.py <handled.json> [max_minutes]
handled.json maps "<number>:<head sha>:<class>" -> true for events already acted on.
"""
import json
import os
import subprocess
import sys
import time

handled_path = sys.argv[1]
max_minutes = float(sys.argv[2]) if len(sys.argv) > 2 else 45


def classify(pr):
    checks = pr.get("statusCheckRollup") or []
    states = [((c.get("conclusion") or c.get("status") or "").upper()) for c in checks]
    if pr.get("isInMergeQueue"):
        return "queued"
    if pr.get("mergeStateStatus") == "DIRTY":
        return "conflict"
    if any(s in ("FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE") for s in states):
        return "red"
    if not checks or any(s in ("", "QUEUED", "IN_PROGRESS", "PENDING", "WAITING", "REQUESTED", "EXPECTED") for s in states):
        return "pending"
    if pr.get("mergeStateStatus") in ("CLEAN", "HAS_HOOKS", "UNSTABLE"):
        return "green"
    return "pending"


QUERY = """query{repository(owner:"pyvvo",name:"funcd"){pullRequests(states:OPEN,first:100){nodes{
number title headRefName headRefOid mergeStateStatus isInMergeQueue
commits(last:1){nodes{commit{statusCheckRollup{contexts(first:50){nodes{
... on CheckRun{name conclusion status} ... on StatusContext{context state}}}}}}}}}}}"""


def snapshot():
    out = subprocess.run(["gh", "api", "graphql", "-f", f"query={QUERY}"], capture_output=True, text=True)
    if out.returncode:
        return None
    prs = []
    for n in json.loads(out.stdout)["data"]["repository"]["pullRequests"]["nodes"]:
        roll = (n["commits"]["nodes"] or [{}])[0].get("commit", {}).get("statusCheckRollup") or {}
        ctx = (roll.get("contexts") or {}).get("nodes") or []
        n["statusCheckRollup"] = [{"conclusion": c.get("conclusion") or c.get("state"), "status": c.get("status")} for c in ctx]
        prs.append(n)
    return prs


start = time.time()
while True:
    handled = json.load(open(handled_path)) if os.path.exists(handled_path) else {}
    prs = snapshot()
    if prs is not None:
        events = []
        for p in prs:
            c = classify(p)
            key = f"{p['number']}:{p['headRefOid'][:12]}:{c}"
            if c in ("green", "red", "conflict") and key not in handled:
                events.append(f"{c.upper():8s} #{p['number']} [{p['headRefName']}] {p['title']}  (key {key})")
        if events:
            print("\n".join(events))
            sys.exit(0)
    if time.time() - start > max_minutes * 60:
        summary = ", ".join(f"#{p['number']}:{classify(p)}" for p in (prs or []))
        print(f"HEARTBEAT after {max_minutes:.0f} min; open PRs: {summary}")
        sys.exit(0)
    time.sleep(60)
