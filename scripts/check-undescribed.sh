#!/usr/bin/env bash
# check-undescribed.sh <base>: fails when the list of fields without a description gained a line since the commit
# <base> (ADR-0323 Decision 5); a field that is added, renamed or moved gets a doc tag instead. It passes when <base>
# has no list yet, and fails when it cannot read <base>.
set -euo pipefail
export LC_ALL=C
list=internal/controlplane/kindref/testdata/undescribed.txt
base=${1:?usage: check-undescribed.sh <base commit>}
if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
	echo "check-undescribed: cannot read the base commit ${base}" >&2
	exit 1
fi
if ! git cat-file -e "${base}:${list}" 2>/dev/null || [ ! -f "$list" ]; then
	exit 0
fi
added=$(comm -13 <(git show "${base}:${list}" | sort -u) <(sort -u "$list"))
if [ -n "$added" ]; then
	echo "These fields joined ${list} since ${base}; give each a doc tag written for users instead:" >&2
	echo "$added" >&2
	exit 1
fi
