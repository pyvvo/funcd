#!/usr/bin/env bash
# check-list-only-shrinks.sh <base> <file>: fails when the list <file> gained a line since the commit <base>, and
# names the lines it gained, as a list of what is not done yet only shrinks (ADR-0329 Decision 5, for the operations
# not yet described). A list that <base> does not hold yet passes, and a <base> it cannot read fails.
set -euo pipefail
export LC_ALL=C
base=${1:?usage: check-list-only-shrinks.sh <base commit> <file>}
list=${2:?usage: check-list-only-shrinks.sh <base commit> <file>}
if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
	echo "check-list-only-shrinks: cannot read the base commit ${base}" >&2
	exit 1
fi
if ! git cat-file -e "${base}:${list}" 2>/dev/null || [ ! -f "$list" ]; then
	exit 0
fi
added=$(comm -13 <(git show "${base}:${list}" | sort -u) <(sort -u "$list"))
if [ -n "$added" ]; then
	echo "These lines joined ${list} since ${base}, and the list only shrinks; do what each line waits for instead:" >&2
	echo "$added" >&2
	exit 1
fi
