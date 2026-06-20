#!/usr/bin/env python3
"""Driver for the funcd GitHub Project board (Project #4, owner green-0-rabbit).

Every project coordinate is baked in below, so a caller never has to discover the
project, its node id, the Status field id, or the option ids — and never has to
hand-assemble a `gh project ...` invocation. Shells out to the `gh` CLI (system
PATH) + python3 only; the Nix dev shell is NOT required for this skill.

Operations (the three the skill supports, plus read helpers):
  driver.py list                                      # every item: [status] title (id)
  driver.py show   <query>                             # full item incl. body + url
  driver.py status <query> "<Backlog|In Progress|Done>"   # move a card
  driver.py create --title T [--body B] [--status S]  # new draft item (default Backlog)
  driver.py refine <query> [--title T] [--body B]      # edit an existing item's title/body
  driver.py ids                                        # print baked-in coords + verify vs live

<query> is EITHER a case-insensitive substring of an item's title OR an exact item
node id (starts with `PVTI_`). Resolution is strict: 0 matches or >1 match is an
error that lists the candidates — the driver never acts on an ambiguous match.

If `ids` reports DRIFT (the live option ids no longer match the baked-in ones,
e.g. the board was recreated), update the constants below from its output.
"""
import argparse
import json
import subprocess
import sys

# --- baked-in project coordinates (verified against the live board) ---------------
OWNER = "green-0-rabbit"
NUMBER = "4"
PROJECT_ID = "PVT_kwHOBMTWh84BbERr"
STATUS_FIELD_ID = "PVTSSF_lAHOBMTWh84BbERrzhV3cDE"
# The Status single-select options. The board currently exposes exactly these three.
STATUS_OPTIONS = {
    "Backlog": "0401eb5a",
    "In Progress": "47fc9ee4",
    "Done": "98236657",
}


def gh(*args):
    """Run a gh command; exit with its stderr on failure."""
    r = subprocess.run(["gh", *args], capture_output=True, text=True)
    if r.returncode != 0:
        sys.exit(f"error: gh {' '.join(args)}\n{r.stderr.strip()}")
    return r.stdout


def items():
    out = gh("project", "item-list", NUMBER, "--owner", OWNER, "--format", "json", "-L", "200")
    return json.loads(out).get("items", [])


def status_of(it):
    return it.get("status") or "No Status"


def body_of(it):
    # For draft items the body + url live under `content`, not at the top level.
    return (it.get("content") or {}).get("body", "")


def url_of(it):
    return (it.get("content") or {}).get("url") or "(draft — no url)"


def content_id_of(it):
    # gh requires the draft-issue CONTENT id (DI_...) to edit title/body — distinct from the
    # project-ITEM id (PVTI_...) used to set field values like Status.
    cid = (it.get("content") or {}).get("id")
    if not cid:
        sys.exit(f"error: item {it.get('title','')!r} has no draft content id (not a draft issue?)")
    return cid


def resolve(query):
    """Return the single item matching query (exact PVTI_ id or title substring), else exit."""
    pool = items()
    if query.startswith("PVTI_"):
        for it in pool:
            if it.get("id") == query:
                return it
        sys.exit(f"error: no item with id {query}")
    q = query.lower()
    matches = [it for it in pool if q in it.get("title", "").lower()]
    if not matches:
        sys.exit(f"error: no item title matches {query!r} — run `driver.py list` to see titles")
    if len(matches) > 1:
        lst = "\n".join(f"  - {it.get('title','')}  ({it['id']})" for it in matches)
        sys.exit(f"error: {query!r} is ambiguous ({len(matches)} matches):\n{lst}\n"
                 "re-run with a more specific substring or the exact PVTI_ id")
    return matches[0]


def set_status(item_id, status):
    if status not in STATUS_OPTIONS:
        sys.exit(f"error: unknown status {status!r}; valid: {', '.join(STATUS_OPTIONS)}")
    gh("project", "item-edit", "--id", item_id, "--project-id", PROJECT_ID,
       "--field-id", STATUS_FIELD_ID, "--single-select-option-id", STATUS_OPTIONS[status])


def cmd_list(_):
    for it in items():
        print(f"[{status_of(it):<11}] {it.get('title','')}  ({it['id']})")


def cmd_show(a):
    it = resolve(a.query)
    print(f"title:  {it.get('title','')}")
    print(f"status: {status_of(it)}")
    print(f"id:     {it['id']}")
    print(f"url:    {url_of(it)}")
    print("body:")
    print(body_of(it))


def cmd_status(a):
    it = resolve(a.query)
    set_status(it["id"], a.status)
    print(f"ok: set [{a.status}] {it.get('title','')}")


def cmd_create(a):
    gh("project", "item-create", NUMBER, "--owner", OWNER, "--title", a.title, "--body", a.body or "")
    # Locate the just-created item by EXACT title (robust to item-create's return shape),
    # then set Status via the project-ITEM id so it never lands in "No Status".
    matches = [it for it in items() if it.get("title", "") == a.title]
    if not matches:
        sys.exit(f"error: created but could not re-locate item titled {a.title!r} to set status")
    if len(matches) > 1:
        sys.exit(f"error: created, but {len(matches)} items now share the exact title {a.title!r}; "
                 "set its status manually with `status`")
    set_status(matches[0]["id"], a.status)
    print(f"ok: created [{a.status}] {a.title}  ({matches[0]['id']})")


def cmd_refine(a):
    it = resolve(a.query)
    # Title/body edits target the draft CONTENT id (DI_...), not the project-item id.
    args = ["project", "item-edit", "--id", content_id_of(it)]
    if a.title:
        args += ["--title", a.title]
    if a.body is not None:
        args += ["--body", a.body]
    if len(args) == 4:
        sys.exit("error: refine needs --title and/or --body")
    gh(*args)
    print(f"ok: refined {it.get('title','')}  ({it['id']})")


def cmd_ids(_):
    print(f"OWNER           = {OWNER}")
    print(f"NUMBER          = {NUMBER}")
    print(f"PROJECT_ID      = {PROJECT_ID}")
    print(f"STATUS_FIELD_ID = {STATUS_FIELD_ID}")
    for k, v in STATUS_OPTIONS.items():
        print(f"  status option {k!r} = {v}")
    out = gh("project", "field-list", NUMBER, "--owner", OWNER, "--format", "json")
    live = {}
    for f in json.loads(out).get("fields", []):
        if f.get("name") == "Status":
            live = {o["name"]: o["id"] for o in f.get("options", [])}
    print(f"live Status options = {live}")
    print("MATCH" if live == STATUS_OPTIONS else
          "DRIFT — update STATUS_OPTIONS in this driver to the live values above")


def main():
    p = argparse.ArgumentParser(prog="driver.py", description="funcd Project #4 board driver")
    sub = p.add_subparsers(dest="cmd", required=True)

    sub.add_parser("list").set_defaults(fn=cmd_list)

    s = sub.add_parser("show")
    s.add_argument("query")
    s.set_defaults(fn=cmd_show)

    s = sub.add_parser("status")
    s.add_argument("query")
    s.add_argument("status", help="Backlog | In Progress | Done")
    s.set_defaults(fn=cmd_status)

    s = sub.add_parser("create")
    s.add_argument("--title", required=True)
    s.add_argument("--body", default="")
    s.add_argument("--status", default="Backlog", help="default Backlog")
    s.set_defaults(fn=cmd_create)

    s = sub.add_parser("refine")
    s.add_argument("query")
    s.add_argument("--title")
    s.add_argument("--body")
    s.set_defaults(fn=cmd_refine)

    sub.add_parser("ids").set_defaults(fn=cmd_ids)

    a = p.parse_args()
    a.fn(a)


if __name__ == "__main__":
    main()
