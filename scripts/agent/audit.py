#!/usr/bin/env python3
"""Bloat audit: does a change bloat the codebase or bring in disorder?

  scripts/agent/audit.py [--base origin/main] [--head HEAD]     diff mode: the gate, once per PR
  scripts/agent/audit.py --base <rev> --head <rev> --full       full mode: a whole fix campaign, plus a flake run

Prints a markdown report; --json <path> also writes the data. Exits 1 on a hard flag that no
`audit-allow: <flag-id> <reason>` line waives (in a commit message of the range, or the --pr-body file),
unless --report-only; 2 on a usage or tool error. How to read it: .claude/skills/bloat-audit/SKILL.md.
"""
import argparse
import collections
import json
import os
import re
import shutil
import statistics
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
LINT_CONFIG = os.path.join(HERE, "audit.golangci.yml")
LINT_TAGS = "dev,e2e,integration"
TEST_DIRS = ("tests/", "e2e/", "bench/", "internal/testkit/")
COMMENT_BLOCK = 6
XDUP_WINDOW = 8
NEW_SHARE = 0.5
OUTLIER_MIN = 300
OUTLIER_FACTOR = 3
SHOW = 8

HUNK = re.compile(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@")
SLEEP = re.compile(r"\btime\.Sleep\b|(?:^|[{;])\s*<-\s*time\.After\(")
SKIP = re.compile(r"\.Skip(?:f|Now)?\(")
DISCARD = re.compile(r"^\s*_(?:\s*,\s*_)*\s*=\s*\S.*\(")
DISCARD_IDIOM = re.compile(r"^\s*_\s*=\s*[\w.]+\.Close\(\)\s*$|\bio\.Copy\(io\.Discard\b")
NOLINT = re.compile(r"//nolint\b")
NOLINT_EXPLAINED = re.compile(r"//nolint(?::[\w,-]+)?\s+//\s*\S")
NOLINT_UNEXPLAINED = "should provide explanation"
LEXEME = re.compile(r'"(?:\\.|[^"\\])*"?|\'(?:\\.|[^\'\\])*\'?|`[^`]*`?|//.*|/\*.*')
FUNC_DECL = re.compile(r"^func\s*(?:\(\s*(?:\w+\s+)?\*?\s*(\w+)[^)]*\)\s*)?(\w+)", re.M)
LINT_FUNC = re.compile(r"^(?:\(\*?(\w+)\)\.)?(\w+)$")
LIMIT_TEXT = re.compile(r">\s*(\d+)\)")
RETRY = re.compile(r"^\s*for\b.*\b(?:attempts?|retries|retry|tries)\b", re.I)
TIMEOUTISH = re.compile(r"timeout|deadline|wait|eventually|ttl|time\.", re.I)
GO_DUR = re.compile(r"(?:(\d+(?:\.\d+)?)\s*\*\s*)?time\.(Nanosecond|Microsecond|Millisecond|Second|Minute|Hour)\b")
STR_DUR = re.compile(r"\b(\d+(?:\.\d+)?)(ns|us|ms|s|m|h)\b")
NUMBER = re.compile(r"\b\d+(?:\.\d+)?\b")
UNIT_MS = {"Nanosecond": 1e-6, "Microsecond": 1e-3, "Millisecond": 1, "Second": 1e3, "Minute": 6e4, "Hour": 3.6e6,
           "ns": 1e-6, "us": 1e-3, "ms": 1, "s": 1e3, "m": 6e4, "h": 3.6e6}
GENERATED = re.compile(r"^// Code generated .* DO NOT EDIT\.$", re.M)
WAIVER = re.compile(r"^\s*audit-allow:\s*(\S+)\s+(\S.*?)\s*$", re.M)
NO_GO_FILES = re.compile(r"package (\S+): no go files to analyze")
DUPL_TEXT = re.compile(r"(\d+)-(\d+) lines are duplicate of `(.+):(\d+)-(\d+)`")
METRIC_TEXT = {
    "gocyclo": re.compile(r"cyclomatic complexity (\d+) of func `([^`]+)`"),
    "gocognit": re.compile(r"cognitive complexity (\d+) of func `([^`]+)`"),
    "funlen": re.compile(r"Function '([^']+)' (is too long|has too many statements) \((\d+) >"),
}
TRIVIAL = {"if err != nil {", "return nil, err", "return err", "return nil", "} else {", "return true", "return false",
           "t.Helper()", "t.Parallel()"}
WORD = re.compile(r"\w+")
TRAILING_COMMENT = re.compile(r"\s+//\s.*$")


class AuditError(Exception):
    pass


def sh(cmd, cwd=None, env=None, check=True):
    p = subprocess.run(cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    out, err = p.stdout.decode("utf-8", "replace"), p.stderr.decode("utf-8", "replace")
    if check and p.returncode:
        raise AuditError(f"`{' '.join(cmd[:4])}` exited {p.returncode}: {err.strip()[-600:]}")
    return out if check else (p.returncode, out, err)


def git(*args):
    return sh(["git", *args])


def kind_of(path, generated):
    if path.endswith(".md") or path.startswith("docs/"):
        return "docs"
    if not path.endswith(".go"):
        return "other"
    if path in generated:
        return "generated"
    if path.endswith("_test.go") or "/testdata/" in "/" + path or path.startswith(TEST_DIRS):
        return "test"
    return "prod"


def numstat(base, head):
    toks, i, files = git("diff", "--numstat", "-z", "-M", base, head).split("\0"), 0, []
    while i < len(toks):
        if not toks[i]:
            i += 1
            continue
        added, removed, path = toks[i].split("\t", 2)
        old = path
        if not path:
            old, path, i = toks[i + 1], toks[i + 2], i + 3
        else:
            i += 1
        files.append({"path": path, "old": old, "added": int(added) if added != "-" else 0,
                      "removed": int(removed) if removed != "-" else 0})
    return files


def parse_diff(text):
    files, cur, hunk, header, old, n = {}, None, None, False, None, 0
    for line in text.split("\n"):
        if line.startswith("diff --git "):
            cur, header = None, True
        elif header and line.startswith("--- "):
            old = None if line[4:] == "/dev/null" else line[4:].strip('"')[2:]
        elif header and line.startswith("+++ "):
            new = line[4:].strip('"')
            cur = None if new == "/dev/null" else files.setdefault(new[2:], {"old": old, "added": {}, "hunks": []})
        elif line.startswith("@@"):
            header = False
            m = HUNK.match(line)
            n, hunk = int(m.group(1)), {"removed": [], "added": []}
            if cur is not None:
                cur["hunks"].append(hunk)
        elif cur is None or header:
            continue
        elif line.startswith("+"):
            cur["added"][n] = line[1:]
            hunk["added"].append((n, line[1:]))
            n += 1
        elif line.startswith("-"):
            hunk["removed"].append(line[1:])
    return files


def commit_stats(base, head, generated):
    out = git("log", "--reverse", "--no-merges", "-M", "--numstat", "--format=%x00%H%x1f%s", f"{base}..{head}")
    commits = []
    for chunk in out.split("\0")[1:]:
        header, _, body = chunk.partition("\n")
        sha, subject = header.split("\x1f", 1)
        sizes = collections.Counter()
        for line in body.splitlines():
            parts = line.split("\t")
            if len(parts) != 3:
                continue
            path = re.sub(r"\{[^{}]* => ([^{}]*)\}", r"\1", parts[2]).split(" => ")[-1].replace("//", "/")
            kind = kind_of(path, generated)
            sizes[kind + "+"] += int(parts[0]) if parts[0] != "-" else 0
            sizes[kind + "-"] += int(parts[1]) if parts[1] != "-" else 0
        commits.append({"sha": sha[:10], "subject": subject, "size": dict(sizes)})
    prod = [c["size"].get("prod+", 0) for c in commits if c["size"].get("prod+", 0)]
    limit = max(OUTLIER_MIN, OUTLIER_FACTOR * statistics.median(prod)) if prod else OUTLIER_MIN
    for c in commits:
        c["outlier"] = c["size"].get("prod+", 0) > limit
    return commits, limit


def extract(rev, dest):
    os.makedirs(dest)
    archive = subprocess.Popen(["git", "archive", "--format=tar", rev], stdout=subprocess.PIPE)
    untar = subprocess.run(["tar", "-x", "-C", dest], stdin=archive.stdout, stderr=subprocess.PIPE)
    archive.stdout.close()
    if archive.wait() or untar.returncode:
        raise AuditError(f"cannot extract {rev}: {untar.stderr.decode('utf-8', 'replace')[-300:]}")


def read(tree, path):
    try:
        with open(os.path.join(tree, path), encoding="utf-8", errors="replace") as f:
            return f.read()
    except OSError:
        return ""


def comment_kind(line):
    s = line.strip()
    if not s:
        return "blank"
    if s.startswith("//go:") or s.startswith("//nolint"):
        return "code"
    if s.startswith("//") or s.startswith("/*"):
        return "comment"
    return "code"


def split_comment(line):
    """The line's code with its string and rune literals emptied, and its trailing comment."""
    code, pos = [], 0
    for m in LEXEME.finditer(line):
        code.append(line[pos:m.start()])
        if m.group(0).startswith("/"):
            return "".join(code), m.group(0)
        code.append(m.group(0)[0] * 2)
        pos = m.end()
    return "".join(code) + line[pos:], ""


def scan_lines(diff, kinds, unlinted):
    masking = collections.defaultdict(list)
    comments = {k: collections.Counter() for k in ("prod", "test")}
    blocks = []
    for path, f in sorted(diff.items()):
        kind = kinds.get(path)
        for hunk in f["hunks"] if kind in ("prod", "test", "other") else []:
            masking["raised-timeout"] += raised_timeouts(path, hunk)
        if kind not in ("prod", "test"):
            continue
        run = []
        for n in sorted(f["added"]):
            text, loc = f["added"][n], f"{path}:{n}"
            ck = comment_kind(text)
            comments[kind][ck] += 1
            code, note = split_comment(text)
            if SLEEP.search(code):
                masking["sleep-" + kind].append(loc)
            if kind == "test" and SKIP.search(code):
                masking["skip"].append(loc)
            if DISCARD.search(code) and not DISCARD_IDIOM.search(code):
                masking["discard-" + kind].append(loc)
            if NOLINT.match(note):
                masking["nolint"].append(loc)
                if unlinted and not NOLINT_EXPLAINED.match(note):
                    masking["nolint-unexplained"].append(loc)
            if RETRY.search(code):
                masking["retry-loop"].append(loc)
            if ck == "comment" and run and run[-1] == n - 1:
                run.append(n)
                continue
            blocks += comment_block(path, f, kind, run)
            run = [n] if ck == "comment" else []
        blocks += comment_block(path, f, kind, run)
    return {k: v for k, v in masking.items() if v}, comments, blocks


def comment_block(path, f, kind, run):
    if len(run) < COMMENT_BLOCK:
        return []
    where = "inline" if f["added"][run[0]].startswith(("\t", " ")) else "doc"
    return [{"loc": f"{path}:{run[0]}-{run[-1]}", "lines": len(run), "kind": kind, "where": where}]


def durations(line):
    values = []

    def go(m):
        values.append(float(m.group(1) or 1) * UNIT_MS[m.group(2)])
        return "<D>"

    def text(m):
        values.append(float(m.group(1)) * UNIT_MS[m.group(2)])
        return "<D>"

    def num(m):
        values.append(float(m.group(0)))
        return "<N>"

    skeleton = NUMBER.sub(num, STR_DUR.sub(text, GO_DUR.sub(go, line.strip())))
    return skeleton, values


def raised_timeouts(path, hunk):
    removed = [durations(r) for r in hunk["removed"] if TIMEOUTISH.search(r)]
    found = []
    for n, a in hunk["added"]:
        if not TIMEOUTISH.search(a):
            continue
        skel, new = durations(a)
        for i, (oskel, old) in enumerate(removed):
            if oskel == skel and old and len(old) == len(new) and old != new:
                if all(x >= y for x, y in zip(new, old)):
                    found.append(f"{path}:{n}")
                removed.pop(i)
                break
    return found


def requires(text):
    mods, block = {}, None
    for no, line in enumerate(text.splitlines(), 1):
        s = line.strip()
        if block and s == ")":
            block = None
            continue
        if s in ("require (", "tool ("):
            block = s.split()[0]
            continue
        directive = block
        for d in ("require ", "tool "):
            if not block and s.startswith(d):
                directive, s = d.strip(), s[len(d):].strip()
        if not directive or not s or s.startswith("//"):
            continue
        parts = s.split()
        if directive == "tool":
            mods["tool " + parts[0]] = {"version": "", "indirect": False, "line": no}
        elif len(parts) >= 2:
            mods[parts[0]] = {"version": parts[1], "indirect": "// indirect" in s, "line": no}
    return mods


def dependencies(base, numstats, head_tree):
    deps = {"new": [], "new-indirect": [], "promoted": [], "bumped": []}
    for f in numstats:
        if os.path.basename(f["path"]) != "go.mod":
            continue
        old = requires(sh(["git", "show", f"{base}:{f['old']}"], check=False)[1])
        new = requires(read(head_tree, f["path"]))
        for mod, info in sorted(new.items()):
            loc = f"{f['path']}:{info['line']}"
            if mod not in old:
                deps["new-indirect" if info["indirect"] else "new"].append({"module": mod, "loc": loc})
            elif old[mod]["indirect"] and not info["indirect"]:
                deps["promoted"].append({"module": mod, "loc": loc})
            elif old[mod]["version"] != info["version"]:
                deps["bumped"].append({"module": mod, "loc": loc, "from": old[mod]["version"], "to": info["version"]})
    return deps


def find_linter(explicit):
    if explicit:
        return explicit
    code, out, err = sh(["go", "tool", "-n", "golangci-lint"], cwd=HERE, check=False)
    if code or not out.strip():
        raise AuditError(f"golangci-lint not found ({err.strip()[-200:]}); pass --golangci-lint or --no-lint")
    return out.strip()


def by_module(tree, dirs):
    """Groups repo-relative dirs by the dir of their nearest go.mod (bench/badger is its own module)."""
    mods = collections.defaultdict(list)
    for d in sorted(dirs):
        if not os.path.isdir(os.path.join(tree, d)):
            continue
        mod = d
        while mod != "." and not os.path.exists(os.path.join(tree, mod, "go.mod")):
            mod = os.path.dirname(mod) or "."
        if os.path.exists(os.path.join(tree, mod, "go.mod")):
            rel = os.path.relpath(d, mod)
            mods[mod].append("." if rel == "." else "./" + rel)
    return mods


def lint(linter, tree, dirs):
    env = dict(os.environ, GOOS="linux", GOWORK="off", GOFLAGS="-mod=readonly",
               GOLANGCI_LINT_CACHE=tree + ".lintcache")
    cmd = [linter, "run", "--config", LINT_CONFIG, "--build-tags", LINT_TAGS, "--allow-parallel-runners",
           "--issues-exit-code=0", "--show-stats=false", "--output.json.path=stdout", "--output.text.path=stderr"]
    issues = []
    for mod, pkgs in sorted(by_module(tree, dirs).items()):
        for i in lint_module(cmd, os.path.join(tree, mod), pkgs, env):
            i["Pos"]["Filename"] = os.path.normpath(os.path.join(mod, i["Pos"]["Filename"]))
            m = DUPL_TEXT.search(i["Text"]) if i["FromLinter"] == "dupl" else None
            if m:
                i["Dupl"] = ((i["Pos"]["Filename"], int(m.group(1)), int(m.group(2))),
                             (os.path.normpath(os.path.join(mod, m.group(3))), int(m.group(4)), int(m.group(5))))
            issues.append(i)
    return issues


def lint_module(cmd, cwd, pkgs, env):
    while pkgs:
        code, out, err = sh(cmd + pkgs, cwd=cwd, env=env, check=False)
        if not code:
            return json.loads(out).get("Issues") or []
        empty = set(NO_GO_FILES.findall(err)) & set(pkgs)
        if not empty:
            raise AuditError(f"golangci-lint exited {code}: {err.strip()[-800:]}")
        pkgs = [p for p in pkgs if p not in empty]
    return []


def share_added(added, start, end):
    return sum(1 for n in range(start, end + 1) if n in added) / max(1, end - start + 1)


def lint_deltas(linter, base_tree, head_tree, diff, numstats, kinds):
    go_files = [f for f in numstats if f["path"].endswith(".go") and os.path.exists(os.path.join(head_tree, f["path"]))]
    dirs = {os.path.dirname(f["path"]) or "." for f in go_files}
    head = lint(linter, head_tree, dirs)
    base = lint(linter, base_tree, {os.path.dirname(f["old"]) or "." for f in go_files})
    renamed = {f["old"]: f["path"] for f in numstats}
    old_of = {f["path"]: f["old"] for f in numstats}
    added = {p: f["added"] for p, f in diff.items()}
    base_funcs = {}

    def metrics(issues, rename):
        out = {}
        for i in issues:
            rx = METRIC_TEXT.get(i["FromLinter"])
            m = rx.search(i["Text"]) if rx else None
            if not m:
                continue
            path = rename.get(i["Pos"]["Filename"], i["Pos"]["Filename"])
            if i["FromLinter"] == "funlen":
                linter = "funlen lines" if "long" in m.group(2) else "funlen statements"
                key, value = (linter, path, m.group(1)), int(m.group(3))
            else:
                key, value = (i["FromLinter"], path, m.group(2)), int(m.group(1))
            limit = LIMIT_TEXT.search(i["Text"])
            out[key] = (value, i["Pos"]["Line"], int(limit.group(1)) if limit else None)
        return out

    def at_base(linter, path, func):
        d = os.path.dirname(old_of.get(path, path)) or "."
        if d not in base_funcs:
            full = os.path.join(base_tree, d)
            names = os.listdir(full) if os.path.isdir(full) else []
            base_funcs[d] = {m.groups() for n in names if n.endswith(".go") for m in FUNC_DECL.finditer(read(full, n))}
        m = LINT_FUNC.match(func)
        recv, name = m.groups() if m else (None, func)
        if linter.startswith("funlen"):
            return any(n == name for _, n in base_funcs[d])
        return (recv, name) in base_funcs[d]

    result = {"dupl": [], "complexity": [], "godox": [], "nolintlint": []}
    before, after = metrics(base, renamed), metrics(head, {})
    for key, (value, line, limit) in sorted(after.items()):
        old = before.get(key, (None,))[0]
        if old is None or value > old:
            result["complexity"].append({"linter": key[0], "func": key[2], "loc": f"{key[1]}:{line}", "value": value,
                                         "before": old, "limit": limit, "crossed": old is None and at_base(*key),
                                         "kind": kinds.get(key[1], "prod")})
    seen = set()
    for i in head:
        path, line = i["Pos"]["Filename"], i["Pos"]["Line"]
        if "Dupl" in i:
            a, b = i["Dupl"]
            pair = frozenset((a, b))
            if pair in seen:
                continue
            seen.add(pair)
            new_a = share_added(added.get(a[0], {}), a[1], a[2]) >= NEW_SHARE
            new_b = share_added(added.get(b[0], {}), b[1], b[2]) >= NEW_SHARE
            if new_a or new_b:
                new, other = (a, b) if new_a else (b, a)
                result["dupl"].append({"loc": f"{new[0]}:{new[1]}-{new[2]}", "of": f"{other[0]}:{other[1]}-{other[2]}",
                                       "kind": kinds.get(new[0], "prod")})
        elif i["FromLinter"] in ("godox", "nolintlint") and line in added.get(path, {}):
            result[i["FromLinter"]].append({"loc": f"{path}:{line}", "text": i["Text"]})
    return result


def norm_rows(text):
    rows, in_import, in_comment = [], False, False
    for no, raw in enumerate(text.split("\n"), 1):
        s = raw.strip()
        if in_comment:
            in_comment = "*/" not in s
            continue
        if s.startswith("/*"):
            in_comment = "*/" not in s
            continue
        if not s or s.startswith("//"):
            continue
        if s == "import (":
            in_import = True
            continue
        if in_import:
            in_import = s != ")"
            continue
        s = " ".join(TRAILING_COMMENT.sub("", s).split())
        if len(WORD.findall(s)) >= 2 and s not in TRIVIAL:
            rows.append((s, no))
    return rows


def cross_package_clones(head_tree, diff, kinds):
    index, rows_of = collections.defaultdict(list), {}
    for root, dirnames, names in os.walk(head_tree):
        dirnames[:] = [d for d in dirnames if not d.startswith(".") and d != "node_modules"]
        for name in names:
            path = os.path.relpath(os.path.join(root, name), head_tree)
            if not name.endswith(".go") or kind_of(path, ()) != "prod" or kinds.get(path, "prod") != "prod":
                continue
            rows = rows_of[path] = norm_rows(read(head_tree, path))
            for i in range(len(rows) - XDUP_WINDOW + 1):
                index[tuple(r[0] for r in rows[i:i + XDUP_WINDOW])].append((path, i))
    spans = {}
    for path, f in diff.items():
        rows = rows_of.get(path, [])
        for i in range(len(rows) - XDUP_WINDOW + 1):
            window = rows[i:i + XDUP_WINDOW]
            if sum(1 for _, n in window if n in f["added"]) < XDUP_WINDOW * NEW_SHARE:
                continue
            for other, j in index[tuple(r[0] for r in window)]:
                if os.path.dirname(other) == os.path.dirname(path):
                    continue
                key = (path, other) if (other, path) not in spans else (other, path)
                mine, theirs = (i, j) if key[0] == path else (j, i)
                lo = spans.setdefault(key, [mine, mine, theirs, theirs])
                lo[0], lo[1], lo[2], lo[3] = min(lo[0], mine), max(lo[1], mine), min(lo[2], theirs), max(lo[3], theirs)
    clones = []
    for (a, b), (a0, a1, b0, b1) in sorted(spans.items()):
        ra, rb = rows_of[a], rows_of[b]
        clones.append({"loc": f"{a}:{ra[a0][1]}-{ra[a1 + XDUP_WINDOW - 1][1]}",
                       "of": f"{b}:{rb[b0][1]}-{rb[b1 + XDUP_WINDOW - 1][1]}", "kind": "prod"})
    return clones


def flake_run(tree, dirs, parallel):
    template = "{{if not .Error}}{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}{{end}}"
    env, count, out = dict(os.environ, GOWORK="off"), 0, ""
    for mod, pkgs in sorted(by_module(tree, dirs).items()):
        cwd = os.path.join(tree, mod)
        listed = sh(["go", "list", "-e", "-f", template, *pkgs], cwd=cwd, env=env).split()
        if listed:
            count += len(listed)
            out += sh(["go", "test", "-count=3", "-json", f"-p={parallel}", "-timeout=20m", *listed],
                      cwd=cwd, env=env, check=False)[1]
    outcomes, pkg_fail = collections.defaultdict(set), set()
    output = collections.defaultdict(lambda: collections.deque(maxlen=15))
    for line in out.splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        key = f"{ev.get('Package', '?')} {ev['Test'].split('/')[0] if ev.get('Test') else '(package)'}"
        if ev.get("Action") == "output":
            output[key].append(ev.get("Output", "").rstrip())
        elif ev.get("Action") in ("pass", "fail") and "/" not in ev.get("Test", ""):
            if ev.get("Test"):
                outcomes[key].add(ev["Action"])
            elif ev["Action"] == "fail":
                pkg_fail.add(key)
    flaky = sorted(k for k, o in outcomes.items() if o == {"pass", "fail"})
    failing = sorted(k for k, o in outcomes.items() if o == {"fail"})
    named = {k.split()[0] for k in flaky + failing}
    failing += sorted(k for k in pkg_fail if k.split()[0] not in named)
    return {"packages": count, "flaky": flaky, "failing": failing,
            "output": {k: list(output[k]) for k in flaky + failing}}


def flake_checkout(root, head, tmp):
    if git("rev-parse", "HEAD").strip() == head and not git("status", "--porcelain", "--untracked-files=no").strip():
        return root, None
    tree = os.path.join(tmp, "worktree")
    git("-c", "core.hooksPath=" + os.devnull, "worktree", "add", "--detach", "-q", tree, head)
    return tree, tree


def waivers(base, head, pr_body):
    text = git("log", "--format=%B", f"{base}..{head}")
    if pr_body:
        with open(pr_body, encoding="utf-8") as f:
            text += "\n" + f.read()
    return {m.group(1): m.group(2) for m in WAIVER.finditer(text)}


def hard_flags(data, allowed):
    flags = []
    for d in data["lint"].get("dupl", []):
        if d["kind"] == "prod":
            flags.append(("dupl", d["loc"].split(":")[0], d["loc"], f"duplicates {d['of']}"))
    for loc in data["masking"].get("sleep-prod", []):
        flags.append(("sleep", loc.split(":")[0], loc, "time.Sleep or a bare <-time.After in production code"))
    unexplained = ([x["loc"] for x in data["lint"]["nolintlint"] if NOLINT_UNEXPLAINED in x["text"]] if data["linted"]
                   else data["masking"].get("nolint-unexplained", []))
    for loc in dict.fromkeys(unexplained):
        flags.append(("nolint", loc.split(":")[0], loc, "//nolint with no `// reason`"))
    for d in data["deps"]["new"]:
        flags.append(("dep", d["module"], d["loc"], f"new dependency {d['module']}"))
    out = []
    for kind, subject, loc, text in flags:
        fid = f"{kind}:{subject}"
        reason = allowed.get(fid) or allowed.get(kind)
        out.append({"id": fid, "loc": loc, "text": text, "waived": reason})
    return out


def audit(args):
    timings, t0 = {}, time.time()
    root = git("rev-parse", "--show-toplevel").strip()
    head = git("rev-parse", "--verify", args.head + "^{commit}").strip()
    base = git("merge-base", args.base, head).strip()
    tmp, worktree = tempfile.mkdtemp(prefix="audit-"), None
    try:
        head_tree, base_tree = os.path.join(tmp, "head"), os.path.join(tmp, "base")
        extract(head, head_tree)
        numstats = numstat(base, head)
        generated = {f["path"] for f in numstats
                     if f["path"].endswith(".go") and GENERATED.search(read(head_tree, f["path"])[:2000])}
        kinds = {f["path"]: kind_of(f["path"], generated) for f in numstats}
        diff = parse_diff(git("diff", "-U0", "-M", "--no-color", "--no-ext-diff", base, head))
        size = collections.defaultdict(collections.Counter)
        for f in numstats:
            k = kinds[f["path"]]
            for key in ("total", os.path.dirname(f["path"]) or "."):
                size[key][k + "+"] += f["added"]
                size[key][k + "-"] += f["removed"]
        commits, outlier_limit = commit_stats(base, head, generated)
        masking, comments, blocks = scan_lines(diff, kinds, args.no_lint)
        deps = dependencies(base, numstats, head_tree)
        timings["diff"] = time.time() - t0
        lint_result = {}
        if not args.no_lint:
            t = time.time()
            extract(base, base_tree)
            lint_result = lint_deltas(find_linter(args.golangci_lint), base_tree, head_tree, diff, numstats, kinds)
            timings["lint"] = time.time() - t
        t = time.time()
        xdup = cross_package_clones(head_tree, diff, kinds)
        timings["xdup"] = time.time() - t
        flakes = None
        if args.full:
            t = time.time()
            go_dirs = {os.path.dirname(p) or "." for p, k in kinds.items() if k in ("prod", "test")}
            checkout, worktree = flake_checkout(root, head, tmp)
            flakes = flake_run(checkout, go_dirs, args.parallel)
            timings["flake"] = time.time() - t
    finally:
        if worktree:
            sh(["git", "worktree", "remove", "--force", worktree], check=False)
        shutil.rmtree(tmp, ignore_errors=True)
    data = {
        "mode": "full" if args.full else "diff", "base": base, "head": head, "files": len(numstats),
        "size": {k: dict(v) for k, v in size.items()}, "commits": commits, "outlier_limit": outlier_limit,
        "lint": lint_result, "linted": not args.no_lint, "xdup": xdup, "masking": masking,
        "comments": {k: dict(v) for k, v in comments.items()}, "comment_blocks": blocks, "deps": deps, "flakes": flakes,
    }
    data["hard"] = hard_flags(data, waivers(base, head, args.pr_body))
    timings["total"] = time.time() - t0
    data["timings"] = {k: round(v, 1) for k, v in timings.items()}
    return data


def capped(items):
    more = f" … +{len(items) - SHOW} more" if len(items) > SHOW else ""
    return ", ".join(items[:SHOW]) + more


def locs(items, key=None):
    return capped([f"`{i[key] if key else i}`" for i in items])


def sizes(c):
    return " | ".join(f"+{c.get(k + '+', 0)} −{c.get(k + '-', 0)}" for k in ("prod", "test", "docs", "other"))


def render(d):
    hard = [h for h in d["hard"] if not h["waived"]]
    out = [f"## Bloat audit ({d['mode']} mode): {d['base'][:10]}..{d['head'][:10]}, {len(d['commits'])} commits, "
           f"{d['files']} files",
           f"**{'FAIL' if hard else 'PASS'}**: {len(hard)} hard flags, {len(d['hard']) - len(hard)} waived "
           f"({d['timings']['total']}s)", "", "### Size (added/removed lines)",
           "| scope | production | test | docs | other |", "|---|---|---|---|---|",
           f"| **total** | {sizes(d['size']['total'])} |"]
    pkgs = sorted(((k, v) for k, v in d["size"].items() if k != "total"),
                  key=lambda kv: -sum(n for k, n in kv[1].items() if k.endswith("+")))
    out += [f"| {k} | {sizes(v)} |" for k, v in pkgs[:SHOW]]
    outliers = [c for c in d["commits"] if c["outlier"]]
    if outliers:
        out.append(f"\nCommit outliers (production lines added > {d['outlier_limit']:.0f}): " + "; ".join(
            f"`{c['sha']}` {c['subject']} (+{c['size'].get('prod+', 0)})" for c in outliers))
    out.append("\n### Lint deltas (new code)")
    if not d["linted"]:
        out.append("skipped (--no-lint)")
    else:
        lr = d["lint"]
        for kind in ("prod", "test"):
            dl = [x for x in lr["dupl"] if x["kind"] == kind]
            if dl:
                out.append(f"- dupl, {kind}: " + "; ".join(f"`{x['loc']}` ≈ `{x['of']}`" for x in dl[:SHOW]))
        for c in lr["complexity"][:SHOW * 2]:
            was = f"crossed {c['limit']}" if c["crossed"] else "new" if c["before"] is None else f"was {c['before']}"
            out.append(f"- {c['linter']} {c['value']} ({was}) `{c['func']}` `{c['loc']}` [{c['kind']}]")
        if len(lr["complexity"]) > SHOW * 2:
            out.append(f"- … +{len(lr['complexity']) - SHOW * 2} more complexity deltas in the JSON")
        for kind in ("godox", "nolintlint"):
            if lr[kind]:
                out.append(f"- {kind}: {locs(lr[kind], 'loc')}")
        if not any(lr.values()):
            out.append("none")
    out.append("\n### Cross-package clones (production)")
    out += [f"- `{x['loc']}` ≈ `{x['of']}`" for x in d["xdup"][:SHOW]] or ["none"]
    out.append("\n### Masking patterns (added lines)")
    out += [f"- {k} ({len(v)}): {locs(v)}" for k, v in sorted(d["masking"].items())] or ["none"]
    out.append("\n### Comments (added lines)")
    for kind, c in d["comments"].items():
        code = c.get("code", 0)
        out.append(f"- {kind}: {c.get('comment', 0)} comment / {code} code lines "
                   f"({c.get('comment', 0) / code:.2f})" if code else f"- {kind}: no code added")
    blocks = sorted(d["comment_blocks"], key=lambda b: -b["lines"])
    if blocks:
        out.append(f"- blocks of {COMMENT_BLOCK}+ comment lines ({len(blocks)}): "
                   + capped([f"`{b['loc']}` ({b['where']})" for b in blocks]))
    deps = d["deps"]
    out.append("\n### Dependencies")
    out += [f"- new: `{x['module']}` `{x['loc']}`" for x in deps["new"]]
    out += [f"- new indirect: `{x['module']}`" for x in deps["new-indirect"]]
    out += [f"- promoted from indirect: `{x['module']}` `{x['loc']}`" for x in deps["promoted"]]
    out += [f"- bumped: `{x['module']}` {x['from']} → {x['to']}" for x in deps["bumped"]]
    if not any(deps.values()):
        out.append("none")
    if d["flakes"] is not None:
        f = d["flakes"]
        out.append(f"\n### Flakiness (go test -count=3, {f['packages']} packages)")
        out += [f"- flaky: {x}" for x in f["flaky"]] + [f"- failing every run: {x}" for x in f["failing"]]
        if not f["flaky"] and not f["failing"]:
            out.append("none")
    out.append("\n### Hard flags")
    for h in d["hard"]:
        state = f"waived: {h['waived']}" if h["waived"] else "FAIL"
        out.append(f"- `{h['id']}` `{h['loc']}` {h['text']} [{state}]")
    if not d["hard"]:
        out.append("none")
    out.append("\n**FAIL**: fix each flag, or waive a justified one with `audit-allow: <flag-id> <reason>`"
               if hard else "\n**PASS**")
    return "\n".join(out)


def main():
    p = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    p.add_argument("--base", default="origin/main")
    p.add_argument("--head", default="HEAD")
    p.add_argument("--full", action="store_true", help="whole-campaign mode: adds a go test -count=3 flake run")
    p.add_argument("--json", metavar="PATH")
    p.add_argument("--report-only", action="store_true", help="never exit 1")
    p.add_argument("--pr-body", metavar="FILE", help="also read audit-allow lines from this file")
    p.add_argument("--no-lint", action="store_true", help="skip golangci-lint (lint deltas)")
    p.add_argument("--golangci-lint", metavar="PATH", help="the binary (default: go tool -n golangci-lint)")
    p.add_argument("--parallel", type=int, default=max(1, (os.cpu_count() or 2) // 2), help="go test -p in full mode")
    args = p.parse_args()
    try:
        data = audit(args)
    except AuditError as e:
        print(f"audit: {e}", file=sys.stderr)
        return 2
    print(render(data))
    if args.json:
        with open(args.json, "w", encoding="utf-8") as f:
            json.dump(data, f, indent=1)
    return 1 if any(not h["waived"] for h in data["hard"]) and not args.report_only else 0


if __name__ == "__main__":
    sys.exit(main())
