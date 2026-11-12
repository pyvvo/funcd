#!/usr/bin/env bash
# check-undescribed.sh <base>: fails when a list of fields or config keys without a description gained a line since
# the commit <base> (ADR-0323 Decision 5, ADR-0324 Decision 2); an entry that is added, renamed or moved gets a doc tag
# instead. A list that <base> does not hold yet passes, and a <base> it cannot read fails.
set -euo pipefail
export LC_ALL=C
lists=(internal/controlplane/kindref/testdata/undescribed.txt internal/reference/testdata/undescribed-config.txt)
base=${1:?usage: check-undescribed.sh <base commit>}
if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
	echo "check-undescribed: cannot read the base commit ${base}" >&2
	exit 1
fi
grew=0
for list in "${lists[@]}"; do
	if ! git cat-file -e "${base}:${list}" 2>/dev/null || [ ! -f "$list" ]; then
		continue
	fi
	added=$(comm -13 <(git show "${base}:${list}" | sort -u) <(sort -u "$list"))
	if [ -n "$added" ]; then
		echo "These entries joined ${list} since ${base}; give each a doc tag written for users instead:" >&2
		echo "$added" >&2
		grew=1
	fi
done
exit "$grew"
