"""Claude Code PreToolUse hook: refuse a `git stash` that changes the stash (issue #560).

refs/stash is shared by every worktree of a repository, so with parallel agents one agent's `git stash pop` restores
another agent's changes; in the 2026-10 fix campaign that lost a fix. `git stash list` and `git stash show` stay
allowed. Exit 2 blocks the command and shows stderr to the agent; anything else lets it run.
"""
import json
import re
import sys

STASH = re.compile(r"\bgit(?:\s+(?:-C\s+\S+|-c\s+\S+|--[\w-]+(?:=\S+)?))*\s+stash\b(?:\s+([a-z]+))?")
READ_ONLY = {"list", "show"}


def blocked(command):
    return any(m.group(1) not in READ_ONLY for m in STASH.finditer(command))


def main():
    try:
        event = json.load(sys.stdin)
    except ValueError:
        return 0
    command = (event.get("tool_input") or {}).get("command") or ""
    if event.get("tool_name") != "Bash" or not blocked(command):
        return 0
    print("git stash is blocked in this repository: refs/stash is shared by every worktree, so a parallel agent's "
          "stash pop restores the wrong changes (issue #560). To compare against a base, write "
          "`git show <base>:<file>` into a scratch file and use `go test -overlay`; to set work aside, commit it on a "
          "branch. If `git stash` only appears inside text (a commit message, an echo), put that text in a file.",
          file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
