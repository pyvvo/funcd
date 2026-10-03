"""Claude Code PreToolUse hook: refuse a `git stash` that changes the stash (issue #560).

refs/stash is shared by every worktree of a repository, so with parallel agents one agent's `git stash pop` restores
another agent's changes; in the 2026-10 fix campaign that lost a fix. `git stash list` and `git stash show` stay
allowed. Exit 2 blocks the command and shows stderr to the agent; anything else lets it run.

Each line is split into shell words, so `git stash` inside a quoted argument (a commit message, a grep pattern) is
not a command. git counts in command position: at the start of a segment, after assignments, reserved words
(`if`, `do`, `!`, `{`, ...) or a wrapper and its arguments (`scripts/agent/d`, `env`, `sudo`, `timeout 60`,
`nix develop -c`, ...), inside `$(...)` or backticks, and in the script of `bash -c`.
"""
import json
import os
import re
import shlex
import sys

READ_ONLY = {"list", "show"}
WRAPPERS = {"d", "env", "sudo", "time", "command", "nice", "nohup", "exec", "nix", "develop", "timeout", "xargs",
            "watch", "caffeinate", "stdbuf"}
RESERVED = {"if", "then", "elif", "else", "do", "while", "until", "!", "{", "time"}
SHELLS = {"sh", "bash", "zsh", "dash"}
GIT_VALUE_OPTIONS = {"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env"}
SEPARATORS = set(";&|()")
SUBSTITUTION = re.compile(r"\$\(((?:[^()]|\([^()]*\))*)\)|`([^`]*)`")
FALLBACK = re.compile(r"\bgit(?:\s+(?:-C\s+\S+|-c\s+\S+|-\S+))*\s+stash\b(?:\s+([a-z]+))?")


def strip_comment(line):
    """Cut a bash comment: a # outside quotes at the start of a word."""
    quote, i = None, 0
    while i < len(line):
        ch = line[i]
        if ch == "\\" and quote != "'":
            i += 2
            continue
        if quote:
            if ch == quote:
                quote = None
        elif ch in "'\"":
            quote = ch
        elif ch == "#" and (i == 0 or line[i - 1] in " \t;&|()"):
            return line[:i]
        i += 1
    return line


def segments(command):
    for line in command.replace("\\\n", " ").split("\n"):
        lexer = shlex.shlex(strip_comment(line), posix=True, punctuation_chars=";&|()")
        lexer.whitespace_split = True
        lexer.commenters = ""
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
    i, wrapped = 0, False
    while i < len(words):
        name = os.path.basename(words[i])
        if re.match(r"^[A-Za-z_]\w*=", words[i]) or words[i] in RESERVED:
            i += 1
        elif name in WRAPPERS:
            wrapped = True
            i += 1
        elif wrapped and name != "git" and name not in SHELLS:
            i += 1  # a wrapper's options and arguments
        else:
            break
    if i >= len(words):
        return False
    name = os.path.basename(words[i])
    if name in SHELLS:
        for j in range(i + 1, len(words) - 1):
            if re.match(r"^-[A-Za-z]*c[A-Za-z]*$", words[j]):
                return blocked(words[j + 1])
        return False
    if name != "git":
        return False
    i += 1
    while i < len(words) and words[i].startswith("-"):
        i += 2 if words[i] in GIT_VALUE_OPTIONS else 1
    if i >= len(words) or words[i] != "stash":
        return False
    return (words[i + 1] if i + 1 < len(words) else "") not in READ_ONLY


def blocked(command):
    if any(blocked(m.group(1) or m.group(2) or "") for m in SUBSTITUTION.finditer(command)):
        return True
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
          "branch. If `git stash` only appears inside text (a heredoc, a message), put that text in a file.",
          file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
