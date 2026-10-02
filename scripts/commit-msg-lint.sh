#!/usr/bin/env bash
# Enforce Conventional Commits (https://www.conventionalcommits.org) on the commit SUBJECT line.
# Invoked by the lefthook `commit-msg` hook with the commit-message file path as its argument.
set -euo pipefail

msg_file="${1:?usage: commit-msg-lint.sh <commit-msg-file>}"
# First non-comment, non-blank line = the subject. One sed that quits at it: a pipe into `head -n1` breaks under
# pipefail once the body is long, because the writer gets SIGPIPE (#248).
subject="$(sed -n '/^#/d; /^[[:space:]]*$/d; p; q' "${msg_file}")"

# git generates these itself (merge/revert/fixup/squash/amend) — not author-authored, so exempt.
case "${subject}" in
  "Merge "* | "Revert "* | "fixup! "* | "squash! "* | "amend! "*) exit 0 ;;
esac

# <type>[(scope)][!]: <description> — types per the spec; scope + breaking `!` optional; description non-empty.
pattern='^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-z0-9._/-]+\))?!?: .+'
if [[ "${subject}" =~ ${pattern} ]]; then
  exit 0
fi

cat >&2 <<EOF
✗ commit message is not a Conventional Commit:
    "${subject}"

  Format: <type>[optional scope][!]: <description>
  Types:  feat fix docs style refactor perf test build ci chore revert
  e.g.:   feat(funcdctl): add --gport flag
          fix: correct 422 on empty body
          docs(examples): document funcdctl dev
EOF
exit 1
