"""Claude Code PreToolUse hook: refuse a `git stash` that changes the stash (issue #560).

refs/stash is shared by every worktree of a repository, so with parallel agents one agent's `git stash pop` restores
another agent's changes; in the 2026-10 fix campaign that lost a fix. `git stash list` and `git stash show` stay
allowed. Exit 2 blocks the command and shows stderr to the agent; anything else lets it run.

Each command line is split into shell words, so `git stash` inside a quoted argument (a commit message, a grep
pattern) is not a command; a quote that spans lines joins them, as in the shell. git counts in command position: at
the start of a segment, after assignments, reserved words (`if`, `do`, `!`, `{`, `function f`, ...) or a wrapper and
its options (`scripts/agent/d`, `env`, `sudo -u me`, `timeout 60`, `nix develop -c`, ...), inside `$(...)` or
backticks, in the script of `bash -c` or `watch`, and in a heredoc that feeds a shell. Any other heredoc is data, but
for the substitutions in one whose delimiter is unquoted.
"""
import json
import os
import re
import shlex
import sys

READ_ONLY = {"list", "show"}
# A wrapper runs the command that follows its options; each maps to its options that take a value. timeout also takes
# a duration, nix runs the command after -c or --command, and watch runs its arguments as a script.
WRAPPERS = {"d": "", "env": "-u -C", "sudo": "-u -g -h -p -r -t -C -D -R -T -U", "time": "-f -o", "command": "",
            "nice": "-n", "nohup": "", "exec": "-a", "timeout": "-s -k --signal --kill-after",
            "xargs": "-a -d -E -I -L -n -P -s", "caffeinate": "-t -w", "stdbuf": "-i -o -e"}
RESERVED = {"if", "then", "elif", "else", "do", "while", "until", "!", "{", "time"}
SHELLS = {"sh", "bash", "zsh", "dash"}
GIT_VALUE_OPTIONS = {"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env"}
SEPARATORS = set(";&|()")
SUBSTITUTION = re.compile(r"\$\(((?:[^()]|\([^()]*\))*)\)|`([^`]*)`")
FALLBACK = re.compile(r"\bgit(?:\s+(?:-C\s+\S+|-c\s+\S+|-\S+))*\s+stash\b(?:\s+([a-z]+))?")
HEREDOC = re.compile(r"<<(-?)(.*)")


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


def lex(text):
    lexer = shlex.shlex(text, posix=True, punctuation_chars=";&|()")
    lexer.whitespace_split = True
    lexer.commenters = ""
    return list(lexer)


def command_lines(command):
    """Yield each command line as (its text, its words, its heredoc bodies as (body, whether the delimiter is quoted)).

    A heredoc whose delimiter never comes is read as commands, which errs on the side of blocking."""
    lines = command.replace("\\\n", " ").split("\n")
    i = 0
    while i < len(lines):
        raw, i = lines[i], i + 1
        while True:
            text = strip_comment(raw)
            try:
                tokens = lex(text)
                break
            except ValueError:  # a quote that spans lines
                if i == len(lines):
                    raise
                raw, i = raw + "\n" + lines[i], i + 1
        bodies = []
        for k, token in enumerate(tokens):
            heredoc = HEREDOC.fullmatch(token)
            if not heredoc or token.startswith("<<<"):
                continue
            word = heredoc.group(2) or (tokens[k + 1] if k + 1 < len(tokens) else "")
            tabs = heredoc.group(1) == "-"
            end = next((j for j in range(i, len(lines)) if (lines[j].lstrip("\t") if tabs else lines[j]) == word), None)
            if word and end is not None:
                quoted = re.search(rf"<<-?\s*(?:(['\"]){re.escape(word)}\1|\\{re.escape(word)})", text)
                bodies.append(("\n".join(lines[i:end]), quoted is not None))
                i = end + 1
        yield text, tokens, bodies


def segments(tokens):
    segment = []
    for token in tokens:
        if set(token) <= SEPARATORS:
            if segment:
                yield segment
            segment = []
        else:
            segment.append(token)
    if segment:
        yield segment


def options_end(words, i, valued):
    """The index of the first word from i on that is neither an option nor the value of one in valued."""
    while i < len(words) and words[i].startswith("-"):
        if words[i] == "--":
            return i + 1
        i += 2 if words[i] in valued else 1
    return i


def blocked_segment(words):
    i = 0
    while i < len(words):
        name = os.path.basename(words[i])
        if re.match(r"^[A-Za-z_]\w*=", words[i]) or words[i] in RESERVED:
            i += 1
        elif words[i] == "function":
            i += 2
        elif name == "watch":
            return blocked(" ".join(words[options_end(words, i + 1, {"-n", "--interval"}):]))
        elif name == "nix":
            i = next((j + 1 for j in range(i + 1, len(words)) if words[j] in ("-c", "--command")), len(words))
        elif name in WRAPPERS:
            i = options_end(words, i + 1, WRAPPERS[name].split()) + (name == "timeout")
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
    i = options_end(words, i + 1, GIT_VALUE_OPTIONS)
    if i >= len(words) or words[i] != "stash":
        return False
    return (words[i + 1] if i + 1 < len(words) else "") not in READ_ONLY


def substituted(text):
    return any(blocked(m.group(1) or m.group(2) or "") for m in SUBSTITUTION.finditer(text))


def blocked(command):
    try:
        lines = list(command_lines(command))
    except ValueError:  # unbalanced quotes: fall back to a plain search, which errs on the side of blocking
        return any(m.group(1) not in READ_ONLY for m in FALLBACK.finditer(command))
    for text, tokens, bodies in lines:
        if substituted(text) or any(blocked_segment(words) for words in segments(tokens)):
            return True
        shell = any(os.path.basename(token) in SHELLS for token in tokens)
        if any(blocked(body) if shell else not quoted and substituted(body) for body, quoted in bodies):
            return True
    return False


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
