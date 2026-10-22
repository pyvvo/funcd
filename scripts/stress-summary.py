#!/usr/bin/env python3
"""Summarize `go test -json` logs from the weekly stress run (#1167): a Markdown table of the tests that failed, with
how many of their runs failed, or a line saying that none failed. A subtest's parent is left out when the subtest
failed, and a package that failed outside any test is listed by name.

  scripts/stress-summary.py <title> <go-test.json>...
"""
import json
import sys
from collections import defaultdict


def main(title, paths):
    runs = defaultdict(lambda: [0, 0])  # (package, test) -> [failed, total]
    broken = set()
    for path in paths:
        with open(path, errors="replace") as f:
            for line in f:
                try:
                    ev = json.loads(line)
                except json.JSONDecodeError:
                    continue
                pkg, test, action = ev.get("Package", ""), ev.get("Test"), ev.get("Action")
                if action not in ("pass", "fail"):
                    continue
                if test is None:
                    if action == "fail":
                        broken.add(pkg)
                    continue
                runs[(pkg, test)][1] += 1
                if action == "fail":
                    runs[(pkg, test)][0] += 1
    failed = {k: v for k, v in runs.items() if v[0]}
    leaves = sorted(k for k in failed if not any(o[0] == k[0] and o[1].startswith(k[1] + "/") for o in failed))
    print(f"### {title}\n")
    if not leaves and not broken - {k[0] for k in failed}:
        print(f"No test failed in {sum(v[1] for v in runs.values())} test runs.")
        return
    print("| Package | Test | Failed runs |\n|---|---|---|")
    for pkg, test in leaves:
        fails, total = failed[(pkg, test)]
        print(f"| `{pkg.removeprefix('github.com/pyvvo/funcd/')}` | `{test}` | {fails} of {total} |")
    for pkg in sorted(broken - {k[0] for k in failed}):
        print(f"| `{pkg.removeprefix('github.com/pyvvo/funcd/')}` | (the package failed outside its tests) | |")


if __name__ == "__main__":
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2:])
