#!/usr/bin/env python3
"""Stress run: the Test functions a branch adds or changes, many times under -race, so a timing flake shows before
its PR lands (#1144; 13 of the 38 flakes in tracker #1142 were timing budgets that fail only under load).

  scripts/agent/stress.py [--base origin/main] [--head HEAD]    run them; the gate's stress step
  scripts/agent/stress.py --list                                 print `<pkg> <tags> <test>` per test, run nothing

A test is changed when a line of its doc comment, its signature or its body is added or changed (the diff's hunks,
against the merge base). Each test runs alone, with the build tags its file needs (dev, e2e,
integration): once to time it, then again with -count up to --count in all, the count scaled down so the run stays
within --budget seconds. The host check (host-check.sh) runs before each test. Exits 1 when a run fails, 2 on a
usage or tool error.
"""
import argparse
import json
import os
import re
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
TAGS = ("dev", "e2e", "integration")
HUNK = re.compile(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@")
TEST_FUNC = re.compile(r"^func (Test\w*)\(\w+ \*testing\.T\) \{$")
BUILD = re.compile(r"^//go:build (.+)$")
MIN_RUN = 0.05


def git(*args):
    return subprocess.run(["git", *args], capture_output=True, text=True, check=True).stdout


def changed_lines(base, head):
    """Maps each *_test.go the branch adds or modifies to the head-side lines its hunks touch."""
    out, path = {}, None
    # R too: a renamed test file's edited lines are changes; a pure rename has no hunk and lists nothing
    for line in git("diff", "-U0", "--diff-filter=AMR", f"{base}...{head}", "--", "*_test.go").splitlines():
        if line.startswith("+++ "):
            path = line[6:] if line.startswith("+++ b/") else None
        elif path and (m := HUNK.match(line)):
            start, count = int(m.group(1)), int(m.group(2) or "1")
            # a deletion-only hunk (+N,0) sits after head line N: it changes the function that holds N
            out.setdefault(path, set()).update(range(start, start + count) if count else [start])
    return out


def in_raw_string(lines):
    """For each line, whether it starts inside a raw string literal: a test fixture may hold Go source whose lines
    read as top-level functions."""
    out, raw = [], False
    for line in lines:
        out.append(raw)
        i, quote = 0, None
        while i < len(line):
            c = line[i]
            if raw:
                raw = c != "`"
            elif quote:
                if c == "\\":
                    i += 1
                elif c == quote:
                    quote = None
            elif c == "`":
                raw = True
            elif c in "\"'":
                quote = c
            elif line.startswith("//", i):
                break
            i += 1
    return out


def tests_in(source):
    """The top-level Test functions of a gofmt'd file, each with its first line (its doc comment's) and last."""
    lines, out = source.splitlines(), []
    raw = in_raw_string(lines)
    for i, line in enumerate(lines):
        if not raw[i] and (m := TEST_FUNC.match(line)):
            first = i
            while first > 0 and lines[first - 1].startswith("//"):
                first -= 1
            last = next((j for j in range(i + 1, len(lines)) if lines[j] == "}" and not raw[j]), len(lines) - 1)
            out.append((m.group(1), first + 1, last + 1))
    return out


def build_tags(source):
    for line in source.splitlines():
        if m := BUILD.match(line):
            return [t for t in TAGS if re.search(rf"(?<![!\w]){t}\b", m.group(1))]
        if line.startswith("package "):
            break
    return []


def changed_tests(base, head):
    found = []
    for path, touched in sorted(changed_lines(base, head).items()):
        source = git("show", f"{head}:{path}")
        pkg = "./" + (os.path.dirname(path) or ".")
        tags = ",".join(build_tags(source))
        found += [(pkg, tags, name) for name, first, last in tests_in(source) if touched & set(range(first, last + 1))]
    return found


def go_test(pkg, tags, name, count, timeout):
    """Runs name count times; returns (passed, the runs' results and seconds from -json, output, exit code)."""
    cmd = ["go", "test", "-race", "-json", f"-count={count}", f"-timeout={int(timeout)}s", "-run", f"^{name}$"]
    if tags:
        cmd.append(f"-tags={tags}")
    proc = subprocess.run([*cmd, pkg], capture_output=True, text=True)
    runs, text = [], []
    for line in proc.stdout.splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            text.append(line)
            continue
        if ev.get("Test") == name and ev.get("Action") in ("pass", "fail"):
            runs.append((ev["Action"], ev.get("Elapsed", 0.0)))
        if ev.get("Action") == "output":
            text.append(ev.get("Output", "").rstrip("\n"))
    ok = proc.returncode == 0 and bool(runs) and all(a == "pass" for a, _ in runs)
    return ok, runs, "\n".join(text + proc.stderr.splitlines()), proc.returncode


def stress(tests, budget, count):
    failed, start = [], time.monotonic()
    for i, (pkg, tags, name) in enumerate(tests):
        label = f"{name} {pkg}" + (f" [{tags}]" if tags else "")
        check = subprocess.run([os.path.join(HERE, "host-check.sh")], capture_output=True, text=True)
        if check.returncode != 0:
            print(f"{label}: not run, the host check failed:\n{check.stdout}{check.stderr}")
            return 2
        ok, runs, out, code = go_test(pkg, tags, name, 1, max(60, budget))
        if not runs:
            if code == 0:
                print(f"{label}: not run here (no such test builds on this platform)")
                continue
            print(f"{label}: does not build or run: FAIL\n" + "\n".join("    " + x for x in out.splitlines()[-40:]))
            failed.append(label)
            continue
        single = max(runs[0][1], MIN_RUN)
        share = max(0.0, (budget - (time.monotonic() - start)) / (len(tests) - i))
        more = min(count - 1, int(share / single)) if ok else 0
        if more > 0:
            ok, extra, out, _ = go_test(pkg, tags, name, more, max(60, more * single * 4 + 60))
            runs += extra
        bad = sum(a == "fail" for a, _ in runs)
        times = [e for _, e in runs]
        print(f"{label}: {len(runs)} runs, {bad} failed, one run {single:.2f} s, longest {max(times):.2f} s, "
              f"{sum(times):.1f} s in all: {'ok' if ok else 'FAIL'}")
        if not ok:
            failed.append(label)
            print("\n".join("    " + line for line in out.splitlines()[-40:]))
    print(f"{len(tests)} tests in {time.monotonic() - start:.0f} s (budget {budget} s); {len(failed)} failed"
          + (": " + ", ".join(failed) if failed else ""))
    return 1 if failed else 0


def main():
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--base", default="origin/main")
    p.add_argument("--head", default="HEAD")
    p.add_argument("--list", action="store_true", help="print the changed tests and run nothing")
    p.add_argument("--budget", type=float, default=300, help="seconds for the whole run (about 5 min)")
    p.add_argument("--count", type=int, default=20, help="runs per test at most")
    args = p.parse_args()
    try:
        tests = changed_tests(args.base, args.head)
    except subprocess.CalledProcessError as e:
        print(f"stress: {' '.join(e.cmd)}: {e.stderr.strip()}", file=sys.stderr)
        return 2
    if args.list:
        for pkg, tags, name in tests:
            print(pkg, tags or "-", name)
        return 0
    if not tests:
        print("stress: the branch adds or changes no test")
        return 0
    return stress(tests, args.budget, args.count)


if __name__ == "__main__":
    sys.exit(main())
