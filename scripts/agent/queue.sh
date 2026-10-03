#!/usr/bin/env bash
# queue.sh <state-dir> <pr>...: enqueue each green funcd PR in the merge queue, or hold it when it needs a Lima lane
# first (issue #566). A PR needs a lane when its diff touches internal/runtime/containerd, internal/network, e2e/ or
# scripts/lanes.yaml, or when its number is listed in <state-dir>/lane-hold.txt (a lane covers it although its paths
# do not say so). A held PR is recorded in <state-dir>/handled.json, so scripts/agent/watch-prs.py stops reporting it.
# Once its lanes pass, add "<pr>:<head sha, 12 chars>" to <state-dir>/lane-passed.txt and rerun: a pass counts for
# that head only, so a new push needs its lanes again. A PR whose details or diff cannot be read is held, never queued.
# Repo auto-merge is off (gh pr merge fails), so this uses the enqueuePullRequest mutation; read every check first.
set -uo pipefail
state=${1:?usage: queue.sh <state-dir> <pr>...}
shift
mkdir -p "$state"
for n in "$@"; do
  if ! info=$(gh pr view "$n" --repo pyvvo/funcd --json id,headRefOid) || ! files=$(gh pr diff "$n" --repo pyvvo/funcd --name-only); then
    echo "#$n held: cannot read the PR or its diff, rerun"
    continue
  fi
  id=$(jq -r .id <<<"$info")
  sha=$(jq -r .headRefOid <<<"$info" | cut -c1-12)
  if grep -qx "$n:$sha" "$state/lane-passed.txt" 2>/dev/null; then
    :
  elif grep -qE '^(internal/runtime/containerd|internal/network|e2e/|scripts/lanes\.yaml)' <<<"$files" ||
    grep -qx "$n" "$state/lane-hold.txt" 2>/dev/null; then
    python3 - "$state/handled.json" "$n:$sha:green" <<'EOF'
import json, os, sys
path, key = sys.argv[1], sys.argv[2]
handled = json.load(open(path)) if os.path.exists(path) else {}
handled[key] = "hold: lane pending"
json.dump(handled, open(path, "w"), indent=1)
EOF
    echo "#$n held: run its lanes first (scripts/agent/lanes.sh), then add $n:$sha to $state/lane-passed.txt"
    continue
  fi
  echo "#$n $(gh api graphql -f query='mutation($id:ID!){enqueuePullRequest(input:{pullRequestId:$id}){mergeQueueEntry{state position}}}' \
      -f id="$id" --jq '.data.enqueuePullRequest.mergeQueueEntry | "\(.state) \(.position)"' 2>&1 | tail -1)"
done
