#!/usr/bin/env python3
"""Run the adr-judge gate on a BYOK model through the GitHub Copilot CLI and time it.

Each run judges one ADR in a fresh detached worktree at a fixed ref, so runs are
comparable across models. It writes the raw event stream, the usage file, the
transcript and a summary.json (wall time, time to first output, turns, tool calls,
tokens, verdict line, files the judge changed) under .cache/byok-judge/.

    DEEPSEEK_API_KEY=... scripts/agent/byok-judge.py 0230
    scripts/agent/byok-judge.py 0230 --provider anthropic --model claude-sonnet-5-5
    scripts/agent/byok-judge.py --compare    # table of every summary.json
"""

import argparse
import datetime
import json
import os
import pathlib
import re
import subprocess
import sys
import time
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
OUT = ROOT / ".cache" / "byok-judge"

PROVIDERS = {
    "deepseek": {
        "type": "openai",
        "base_url": "https://api.deepseek.com/v1",
        "key_env": "DEEPSEEK_API_KEY",
        "model": "deepseek-flash",
    },
    "openrouter": {
        "type": "openai",
        "base_url": "https://openrouter.ai/api/v1",
        "key_env": "OPENROUTER_API_KEY",
        "model": "deepseek/deepseek-v4.1-flash",
    },
    # Claude on the Anthropic wire: Copilot then sends cache_control breakpoints and
    # adaptive thinking, which the OpenAI wire drops.
    "openrouter-anthropic": {
        "type": "anthropic",
        "base_url": "https://openrouter.ai/api",
        "key_env": "OPENROUTER_API_KEY",
        "bearer": True,
        "model": "anthropic/claude-sonnet-5.5",
    },
    "anthropic": {
        "type": "anthropic",
        "base_url": "https://api.anthropic.com",
        "key_env": "ANTHROPIC_API_KEY",
        "model": "claude-sonnet-5-5",
    },
}

PROMPT = """You are running the funcd ADR judge gate (ADR-0000 gate 3), non-interactively.
Target: {adr}. Follow the skill below exactly. It evaluates and reports; it never edits
any file. Print the full report as your final answer and end it with one line of the form
`VERDICT: <accept|accept-with-fixes|revise|reject> · blockers=<n> majors=<n> minors=<n>`.

<skill name="adr-judge">
{skill}
</skill>
"""


def sh(*cmd, cwd=ROOT, check=True):
    return subprocess.run(cmd, cwd=cwd, check=check, text=True, capture_output=True).stdout.strip()


def resolve_adr(arg, cwd):
    p = cwd / arg
    if p.is_file():
        return p.relative_to(cwd).as_posix()
    hits = sorted((cwd / "docs" / "adr").glob(f"{int(arg):04d}-*.md"))
    if len(hits) != 1:
        sys.exit(f"cannot resolve ADR {arg!r} at this ref: {[h.name for h in hits]}")
    return hits[0].relative_to(cwd).as_posix()


def check_model(prov, base_url, key, model):
    """Fail before the run when an OpenAI-compatible provider does not list the model."""
    if prov["type"] != "openai" or not key:
        return
    req = urllib.request.Request(base_url.rstrip("/") + "/models", headers={"Authorization": f"Bearer {key}"})
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            ids = [m["id"] for m in json.load(r).get("data", [])]
    except Exception as e:  # a provider without /models still gets its run
        print(f"warn: could not list models: {e}", file=sys.stderr)
        return
    if ids and model not in ids:
        sys.exit(f"model {model!r} not served by {base_url}; it lists: {', '.join(ids)}")


def ts(ev):
    return datetime.datetime.fromisoformat(ev["timestamp"].replace("Z", "+00:00"))


def summarize(events_path, wall, first_out, usage_path, worktree):
    events, kinds = [], {}
    for line in events_path.read_text().splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        events.append(ev)
        k = ev.get("type") or ev.get("event") or "?"
        kinds[k] = kinds.get(k, 0) + 1
    # Only the final answer: later snapshots echo the prompt, whose template line also starts with VERDICT:.
    answers = [ev["data"].get("content", "") for ev in events if ev.get("type") == "assistant.message"]
    verdict = re.findall(r"VERDICT:.*", answers[-1] if answers else "")
    usage = json.loads(usage_path.read_text()) if usage_path.exists() else None
    changed = sh("git", "status", "--porcelain", cwd=worktree, check=False)
    model_s, started = 0.0, None
    for ev in events:
        if ev.get("type") == "model.call_start":
            started = ts(ev)
        elif ev.get("type") == "model.call_finished" and started:
            model_s += (ts(ev) - started).total_seconds()
            started = None
    return {
        "wall_s": round(wall, 1),
        "first_output_s": round(first_out, 1) if first_out is not None else None,
        "model_s": round(model_s, 1),
        "model_calls": kinds.get("model.call_start", 0),
        "tool_calls": sum(v for k, v in kinds.items() if k.startswith("tool.") and k.endswith("start")),
        "event_kinds": kinds,
        "usage": usage,
        "verdict": verdict[-1].strip() if verdict else None,
        "files_changed_by_judge": changed.splitlines(),
    }


def run(args):
    prov = dict(PROVIDERS[args.provider])
    base_url = args.base_url or prov["base_url"]
    model = args.model or prov["model"]
    key = os.environ.get(prov["key_env"], "")
    if not key and not args.base_url:
        sys.exit(f"set {prov['key_env']}")
    check_model(prov, base_url, key, model)

    ref = sh("git", "rev-parse", "--short", args.ref)
    stamp = datetime.datetime.now().strftime("%Y%m%dT%H%M%S")
    run_dir = OUT / f"{stamp}-{model.replace('/', '_')}-{args.adr}"
    worktree = run_dir / "wt"
    run_dir.mkdir(parents=True)
    sh("git", "worktree", "add", "--detach", "-q", str(worktree), ref)
    try:
        adr = resolve_adr(args.adr, worktree)
        skill = (worktree / ".claude" / "skills" / "adr-judge" / "SKILL.md").read_text()
        env = {
            **os.environ,
            "COPILOT_PROVIDER_BASE_URL": base_url,
            "COPILOT_PROVIDER_TYPE": prov["type"],
            "COPILOT_PROVIDER_BEARER_TOKEN" if prov.get("bearer") else "COPILOT_PROVIDER_API_KEY": key,
            "COPILOT_MODEL": model,
        }
        if args.model_id:
            env["COPILOT_PROVIDER_MODEL_ID"] = args.model_id
            env["COPILOT_PROVIDER_WIRE_MODEL"] = model
        cmd = [
            "copilot", "-p", PROMPT.format(adr=adr, skill=skill),
            "--output-format", "json", "--stream", "on",
            "--allow-all-tools", "--deny-tool=create", "--deny-tool=edit", "--no-ask-user", "--disable-builtin-mcps", "--sync-session=off",
            "--secret-env-vars=COPILOT_PROVIDER_API_KEY,COPILOT_PROVIDER_BEARER_TOKEN," + prov["key_env"],
            "--usage-output-file", str(run_dir / "usage.json"),
            f"--share={run_dir / 'transcript.md'}",
            "--log-dir", str(run_dir / "logs"),
        ]
        if args.effort:
            cmd += ["--effort", args.effort]
        events_path = run_dir / "events.jsonl"
        t0, first_out = time.monotonic(), None
        with open(events_path, "w") as f:
            proc = subprocess.Popen(cmd, cwd=worktree, env=env, stdout=subprocess.PIPE, stderr=open(run_dir / "stderr.log", "w"), text=True)
            for line in proc.stdout:
                if first_out is None and '"assistant.message_delta"' in line:
                    first_out = time.monotonic() - t0
                f.write(line)
            try:
                rc = proc.wait(timeout=args.timeout)
            except subprocess.TimeoutExpired:
                proc.kill()
                rc = "timeout"
        wall = time.monotonic() - t0
        summary = {
            "provider": args.provider, "model": model, "base_url": base_url, "adr": adr,
            "ref": ref, "effort": args.effort, "exit": rc,
            **summarize(events_path, wall, first_out, run_dir / "usage.json", worktree),
        }
        (run_dir / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
        print(json.dumps(summary, indent=2))
        print(f"run dir: {run_dir.relative_to(ROOT)}")
    finally:
        if not args.keep:
            sh("git", "worktree", "remove", "--force", str(worktree), check=False)


def compare():
    rows = [json.loads(p.read_text()) for p in sorted(OUT.glob("*/summary.json"))]
    print(f"{'model':32} {'adr':40} {'wall_s':>7} {'model_s':>7} {'calls':>5} {'tools':>5}  verdict")
    for r in rows:
        print(f"{r['model'][:32]:32} {r['adr'][-40:]:40} {r['wall_s']:>7} {r.get('model_s', '-'):>7} {r.get('model_calls', '-'):>5} {r['tool_calls']:>5}  {r['verdict']}")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("adr", nargs="?", help="ADR number or path")
    ap.add_argument("--provider", choices=PROVIDERS, default="deepseek")
    ap.add_argument("--model", help="wire model name (default per provider)")
    ap.add_argument("--model-id", help="well-known Copilot model id for token limits, when the wire name is unknown to it")
    ap.add_argument("--base-url", help="override the provider endpoint")
    ap.add_argument("--effort", choices=["none", "minimal", "low", "medium", "high", "xhigh", "max"])
    ap.add_argument("--ref", default="origin/main", help="commit to judge at (default origin/main)")
    ap.add_argument("--timeout", type=int, default=1800)
    ap.add_argument("--keep", action="store_true", help="keep the worktree")
    ap.add_argument("--compare", action="store_true")
    args = ap.parse_args()
    if args.compare:
        return compare()
    if not args.adr:
        ap.error("adr is required")
    run(args)


if __name__ == "__main__":
    main()
