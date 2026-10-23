"""Checks the design specs under docs/design (ADR-0272) and writes their diagrams.

  specs.py check [spec...]     each spec passes within its bounds and the state budget, each mutant fails the
                               assertion it names, and each diagram is current; with no spec named, also each oracle
                               marker names an assertion that exists
  specs.py diagrams [spec...]  rewrite each spec's diagram, <spec>.md

A spec is a FizzBee file whose front matter sets max_actions and max_concurrent_actions. Comment lines drive the rest:
  # diagram: <var>[, <var>]      a Mermaid state machine over these globals, or over one role's <Role>.<field>
  # mutant <name>: <assertion>   with MUTANT = "<name>" the spec, which declares MUTANT = "", fails <assertion>
                                 (or `liveness`)

A test that checks an assertion's property carries `oracle: docs/design/<part>/<topic>.fizz#<Assertion>` above it.
FIZZ names the fizz command (default: `fizz` from the dev shell, which the flake pins); SPECS_MAX_STATES overrides the
state budget. The diagram is computed from
the state graph FizzBee writes (proto/graph.proto in its repo): nodes_*.pb holds one JSON state per node,
adjacency_lists_*.pb the links between them.
"""

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
from collections import Counter, defaultdict
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MAX_STATES = int(os.environ.get("SPECS_MAX_STATES", "100000"))
DIAGRAM = re.compile(r"^#\s*diagram:\s*(.+)$", re.MULTILINE)
MUTANT = re.compile(r"^#\s*mutant\s+([\w-]+):\s*(\S+)\s*$", re.MULTILINE)
DECLARED = re.compile(r'^MUTANT = ""\s*$', re.MULTILINE)
FRONT = re.compile(r"\A\s*---\n(.*?)\n---", re.DOTALL)
ASSERTION = re.compile(r"^(?:always|exists|transition|always eventually|eventually always)\s+assertion\s+(\w+)\s*:",
                       re.MULTILINE)
ORACLE = re.compile(r"oracle: (docs/design/[\w/-]+\.fizz)#(\w+)")


class Run:
    """One FizzBee run of a spec in a scratch directory (fizz writes <spec>.json beside the spec)."""

    def __init__(self, fizz: str, spec: Path, hook: str = "", graph: bool = False):
        args = [fizz] + (["--preinit-hook", hook] if hook else [])
        if graph:
            # Without symmetry reduction a role instance keeps its identity from one state to the next.
            args.append("--no-symmetry-reduction")
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            (work / spec.name).write_text(spec.read_text())
            start = time.monotonic()
            proc = subprocess.run(args + ["--output-dir", "out", spec.name], cwd=work, capture_output=True, text=True,
                                  timeout=600)
            self.seconds = time.monotonic() - start
            self.output = proc.stdout + proc.stderr
            self.verdict = verdict(self.output)
            if graph and self.verdict == "pass":
                self.nodes, self.links = read_graph(work / "out")
                options = json.loads((work / "out" / "state_config.json").read_text()).get("options", {})
                self.bound = int(options.get("maxActions", 0))

    def states(self) -> int:
        m = re.search(r"Unique states: (\d+)", self.output) or re.search(r"Nodes: (\d+)", self.output)
        return int(m.group(1)) if m else 0


def verdict(output: str) -> str:
    if "PASSED: Model checker completed successfully" in output:
        return "pass"
    if m := re.search(r"FAILED: Model checker failed\. Invariant:\s+(\S+)", output):
        return m.group(1)
    if "FAILED: Liveness check failed" in output:
        return "liveness"
    if "DEADLOCK detected" in output:
        return "deadlock"
    return "error"


def varint(buf: bytes, i: int) -> tuple[int, int]:
    shift = value = 0
    while True:
        b = buf[i]
        i += 1
        value |= (b & 0x7F) << shift
        if not b & 0x80:
            return value, i
        shift += 7


def fields(buf: bytes):
    """Yields (field number, value) for a protobuf message: an int for a varint, bytes for a length-delimited field."""
    i = 0
    while i < len(buf):
        key, i = varint(buf, i)
        number, wire = key >> 3, key & 7
        if wire == 0:
            value, i = varint(buf, i)
        elif wire == 1:
            value, i = buf[i:i + 8], i + 8
        elif wire == 2:
            size, i = varint(buf, i)
            value, i = buf[i:i + size], i + size
        elif wire == 5:
            value, i = buf[i:i + 4], i + 4
        else:
            raise ValueError(f"specs: unexpected protobuf wire type {wire}")
        yield number, value


def read_graph(out: Path) -> tuple[list[dict], list[dict]]:
    nodes = [json.loads(v) for f in sorted(out.glob("nodes_*.pb")) for n, v in fields(f.read_bytes()) if n == 1]
    links = []
    for f in sorted(out.glob("adjacency_lists_*.pb")):
        for n, v in fields(f.read_bytes()):
            if n != 2:
                continue
            link = {"src": 0, "dest": 0, "name": "", "type": ""}
            for fn, fv in fields(v):
                if fn in (1, 2):
                    link["src" if fn == 1 else "dest"] = fv
                elif fn in (3, 9):
                    link["name" if fn == 3 else "type"] = fv.decode()
            links.append(link)
    return nodes, links


def plain(action: str) -> str:
    """A role action as its threads name it: <role>.<action> for <role>#<n>.<action>."""
    return re.sub(r"#\d+", "", action)


def action_of(link: dict, src: dict, dest: dict) -> str | None:
    """The action a link belongs to: its name for an action start or a crash, else the action of the thread that ran.

    Neither a thread-<n> link name nor a thread id identifies the thread across two stored states (both are
    canonicalized), so the thread that ran is the stack that is in the first state and no longer in the second.
    """
    if link["type"] == "action" or link["name"] == "crash":
        return link["name"]
    gone = Counter(stacks(src)) - Counter(stacks(dest))
    names = {json.loads(stack)[0]["name"] for stack in gone}
    names = names or {t["stack"][0]["name"] for t in src.get("threads") or []}
    return names.pop() if len(names) == 1 else None


def stacks(node: dict) -> list[str]:
    return [json.dumps(t["stack"], sort_keys=True) for t in node.get("threads") or []]


def running(node: dict, action: str) -> bool:
    return any(plain(t["stack"][0]["name"]) == plain(action) for t in node.get("threads") or [])


def show(value) -> str:
    return value if isinstance(value, str) else json.dumps(value, sort_keys=True)


def views(variables: list[str], node: dict) -> dict[str, tuple]:
    """The node as each diagram instance sees it: one view for globals, one per instance of the role named."""
    roles = {v.split(".", 1)[0] for v in variables if "." in v}
    if not roles:
        return {"": tuple(show(node["state"].get(v)) for v in variables)}
    if len(roles) > 1 or any("." not in v for v in variables):
        raise SystemExit(f"specs: {', '.join(variables)} mixes roles or globals; one diagram follows one role")
    role = roles.pop()
    return {r["ref_string"]: tuple(show(r["fields"].get(v.split(".", 1)[1])) for v in variables)
            for r in node.get("roles") or [] if r["name"] == role}


def machine(variables: list[str], run: Run) -> tuple[set, dict, set]:
    role = variables[0].split(".", 1)[0] if "." in variables[0] else ""
    # Role actions are named <role>#<n>.<action> when they start and <role>.<action> while they run.
    strip = re.compile(rf"^{re.escape(role)}(#\d+)?\.") if role else None

    def label(action: str) -> str:
        return strip.sub("", action) if strip else plain(action)

    out = defaultdict(list)
    for link in run.links:
        out[link["src"]].append(link)

    def ends(start: dict) -> list[int]:
        """Where an action that starts on this link ends when it runs whole: in the same step, or after its choices.

        An action that yields between steps has no such end: its moves are the edges of the steps that make them.
        """
        action, first = start["name"], start["dest"]
        if not running(run.nodes[first], action):
            return [first]
        found, frontier, seen = [], [first], set()
        while frontier:
            node = frontier.pop()
            if node in seen:
                continue
            seen.add(node)
            for link in out[node]:
                if link["name"].startswith("Any:"):
                    (frontier if running(run.nodes[link["dest"]], action) else found).append(link["dest"])
        return found

    # The states where Init ends are the initial ones: an Init that chooses (oneof) starts before its choices.
    initial = set()
    for link in run.links:
        src, dest = run.nodes[link["src"]], run.nodes[link["dest"]]
        if action_of(link, src, dest) == "Init" and not running(dest, "Init"):
            initial.update(views(variables, dest).values())
    if not initial:
        initial = set(views(variables, run.nodes[0]).values())
    edges: dict[tuple, set] = defaultdict(set)
    final = set()
    for link in run.links:
        src, dest = run.nodes[link["src"]], run.nodes[link["dest"]]
        before, after = views(variables, src), views(variables, dest)
        if action_of(link, src, dest) == "Init":
            continue
        if link["name"] == "end":
            # FizzBee ends both a state where no action is enabled and one at the max_actions bound; only the first is
            # an end of the design.
            if not run.bound or src["stats"]["totalActions"] < run.bound:
                final.update(before.values())
            continue
        action = action_of(link, src, dest)
        for ref, value in after.items():
            if ref not in before:
                initial.add(value)
            elif before[ref] != value:
                if action is None:
                    raise SystemExit(f"specs: no single action moved {', '.join(variables)} from {before[ref]} to {value}")
                edges[(before[ref], value)].add(label(action))
        if link["type"] != "action":
            continue
        # A self-loop: an action that runs whole where it starts and leaves the variables as they were (for a role
        # instance, only its own actions).
        for end in ends(link):
            for ref, value in views(variables, run.nodes[end]).items():
                if before.get(ref) == value and (not ref or link["name"].startswith(ref + ".")):
                    edges[(value, value)].add(label(link["name"]))
    return initial, edges, final


def mermaid(title: str, variables: list[str], initial: set, edges: dict, final: set) -> str:
    states = sorted(initial | {s for e in edges for s in e} | final)
    ids = {s: f"s{i}" for i, s in enumerate(states)}
    lines = ["```mermaid", "---", f"title: {title} ({', '.join(variables)})", "---", "stateDiagram-v2"]
    for s in states:
        name = s[0] if len(s) == 1 else " · ".join(f"{v.rsplit('.', 1)[-1]}={x}" for v, x in zip(variables, s))
        lines.append(f'    state "{name}" as {ids[s]}')
    lines += [f"    [*] --> {ids[s]}" for s in sorted(initial)]
    lines += [f"    {ids[a]} --> {ids[b]} : {', '.join(sorted(acts))}" for (a, b), acts in sorted(edges.items())]
    lines += [f"    {ids[s]} --> [*]" for s in sorted(final)]
    return "\n".join(lines + ["```"])


def diagram(fizz: str, spec: Path) -> tuple[str | None, str]:
    """The diagram file of a spec, or None and why there is none."""
    directives = [[v.strip() for v in d.split(",")] for d in DIAGRAM.findall(spec.read_text())]
    if not directives:
        return None, "names no variable to draw (# diagram: <var>)"
    run = Run(fizz, spec, graph=True)
    if run.verdict != "pass":
        return None, f"does not pass ({run.verdict}), so it has no diagram"
    parts = [f"<!-- Generated by scripts/specs.py from {spec.name} (ADR-0272); do not edit. -->", "",
             f"# {spec.stem}", ""]
    for variables in directives:
        parts += [mermaid(spec.stem, variables, *machine(variables, run)), ""]
    return "\n".join(parts), ""


def check(fizz: str, spec: Path) -> bool:
    name = spec.relative_to(ROOT) if spec.is_relative_to(ROOT) else spec.name
    text = spec.read_text()
    front = FRONT.match(text)
    unset = [k for k in ("max_actions", "max_concurrent_actions")
             if not front or not re.search(rf"^\s*{k}:\s*\d+", front.group(1), re.MULTILINE)]
    if unset:
        print(f"{name}: sets no {' or '.join(unset)}; a spec names its bounds")
        return False
    run = Run(fizz, spec)
    if run.verdict != "pass":
        print(f"{name}: FAILS {run.verdict} ({run.seconds:.1f} s)\n{trace(run.output)}")
        return False
    print(f"{name}: pass ({run.states():,} states, {run.seconds:.1f} s)")
    ok = run.states() <= MAX_STATES
    if not ok:
        print(f"{name}: over the {MAX_STATES:,}-state budget; narrow its bounds and record a wider run in its ADR")
    mutants = MUTANT.findall(text)
    if mutants and not DECLARED.search(text):
        print(f'{name}: names mutants but declares no MUTANT = ""')
        ok = False
    for mutant, expected in mutants:
        got = Run(fizz, spec, hook=f'MUTANT = "{mutant}"').verdict
        if got == expected:
            print(f"{name} mutant {mutant}: fails {expected}")
        elif got == "pass":
            print(f"{name} mutant {mutant}: PASSES, so the guard it skips protects nothing {spec.name} asserts")
            ok = False
        else:
            print(f"{name} mutant {mutant}: fails {got}, not {expected}")
            ok = False
    want, why = diagram(fizz, spec)
    md = spec.with_suffix(".md")
    if want is None:
        print(f"{name}: {why}")
        return False
    if not md.exists() or md.read_text() != want:
        print(f"{md.name}: STALE, run `just spec-diagrams`")
        return False
    print(f"{md.name}: current")
    return ok


def oracles() -> tuple[dict[tuple[str, str], list[str]], list[str]]:
    """The oracle markers in code, by (spec, assertion), and those that name no assertion of an existing spec."""
    out = subprocess.run(["git", "grep", "--untracked", "-n", "-F", "oracle: docs/design/", "--", "*.go", "*.py", "*.ts",
                          "*.js"], cwd=ROOT, capture_output=True, text=True).stdout
    found: dict[tuple[str, str], list[str]] = defaultdict(list)
    dangling = []
    for line in out.splitlines():
        where, number, text = line.split(":", 2)
        for spec, assertion in ORACLE.findall(text):
            path = ROOT / spec
            if not path.exists() or assertion not in ASSERTION.findall(path.read_text()):
                dangling.append(f"{where}:{number}: oracle marker names no assertion {spec}#{assertion}")
            found[(spec, assertion)].append(f"{where}:{number}")
    return found, dangling


def trace(output: str) -> str:
    tail = output.split("FAILED", 1)[-1] if "FAILED" in output else output
    return "\n".join(tail.strip().splitlines()[:60])


def main() -> None:
    if len(sys.argv) < 2 or sys.argv[1] not in ("check", "diagrams"):
        raise SystemExit(__doc__)
    fizz = os.environ.get("FIZZ") or shutil.which("fizz")
    if not fizz:
        raise SystemExit("specs: no fizz command; run inside the dev shell (nix develop, or scripts/agent/d)")
    specs = [Path(p).resolve() for p in sys.argv[2:]] or sorted((ROOT / "docs" / "design").glob("**/*.fizz"))
    if sys.argv[1] == "diagrams":
        for spec in specs:
            text, why = diagram(fizz, spec)
            if text is None:
                raise SystemExit(f"specs: {spec.name} {why}")
            spec.with_suffix(".md").write_text(text)
            print(f"{spec.with_suffix('.md').name}: written")
        return
    failed = [spec.name for spec in specs if not check(fizz, spec)]
    if len(sys.argv) == 2:
        found, dangling = oracles()
        for spec in specs:
            rel = spec.relative_to(ROOT).as_posix()
            for assertion in ASSERTION.findall(spec.read_text()):
                if (rel, assertion) not in found:
                    print(f"{rel}#{assertion}: no oracle test yet")
        for line in dangling:
            print(line)
        failed += ["oracle markers"] if dangling else []
    print(f"{len(specs)} specs, {len(failed)} failing" + (f": {', '.join(failed)}" if failed else ""))
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
