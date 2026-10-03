"""Claude Code PreToolUse hook: refuse a `git stash` that changes the stash (issue #560).

refs/stash is shared by every worktree of a repository, so with parallel agents one agent's `git stash pop` restores
another agent's changes; in the 2026-10 fix campaign that lost a fix. `git stash list` and `git stash show` stay
allowed. Exit 2 blocks the command and shows stderr to the agent; anything else lets it run.

The command is split into shell words, so `git stash` inside a quoted argument (a commit message, a grep pattern)
is not a command. git counts only in command position: at the start of a segment, after variable assignments, or
after a wrapper such as `scripts/agent/d`, `env`, `sudo` or `nix develop -c`.
"""
import json
import os
import re
import shlex
import sys

READ_ONLY = {"list", "show"}
WRAPPERS = {"d", "env", "sudo", "time", "command", "nice", "nohup", "exec", "nix", "develop"}
SHELLS = {"sh", "bash", "zsh", "dash"}
GIT_VALUE_OPTIONS = {"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env"}
SEPARATORS = set(";&|()")
FALLBACK = re.compile(r"\bgit(?:\s+(?:-C\s+\S+|-c\s+\S+|-\S+))*\s+stash\b(?:\s+([a-z]+))?")


def segments(command):
    lexer = shlex.shlex(command.replace("\n", " ; "), posix=True, punctuation_chars=";&|()")
    lexer.whitespace_split = True
    segment = []
    for token in lexer:
        if set(token) <= SEPARATORS:
            if segment:
                yield segment
            segment = []
        else:
            segment.append(token)
    if segment:
        yield segment


def blocked_segment(words):
    i = 0
    while i < len(words) and (re.match(r"^[A-Za-z_]\w*=", words[i]) or os.path.basename(words[i]) in WRAPPERS
                              or (i > 0 and words[i].startswith("-"))):
        i += 1
    if i >= len(words):
        return False
    name = os.path.basename(words[i])
    if name in SHELLS and "-c" in words[i + 1:]:
        script = words.index("-c", i + 1) + 1
        return script < len(words) and blocked(words[script])
    if name != "git":
        return False
    i += 1
    while i < len(words) and words[i].startswith("-"):
        i += 2 if words[i] in GIT_VALUE_OPTIONS else 1
    if i >= len(words) or words[i] != "stash":
        return False
    return (words[i + 1] if i + 1 < len(words) else "") not in READ_ONLY


def blocked(command):
    try:
        return any(blocked_segment(words) for words in segments(command))
    except ValueError:  # unbalanced quotes: fall back to a plain search, which errs on the side of blocking
        return any(m.group(1) not in READ_ONLY for m in FALLBACK.finditer(command))


def main():
    try:
        event = json.load(sys.stdin)
    except ValueError:
        return 0
    if not isinstance(event, dict) or event.get("tool_name") != "Bash":
        return 0
    command = (event.get("tool_input") or {}).get("command") or ""
    if not blocked(command):
        return 0
    print("git stash is blocked in this repository: refs/stash is shared by every worktree, so a parallel agent's "
          "stash pop restores the wrong changes (issue #560). To compare against a base, write "
          "`git show <base>:<file>` into a scratch file and use `go test -overlay`; to set work aside, commit it on a "
          "branch.", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
