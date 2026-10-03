#!/usr/bin/env bash
# wave-check.sh <branch>...: before any PR of a fix wave is queued, merge all of the wave's group branches onto the
# base in a scratch worktree, in the given order, and report each branch that conflicts with the ones merged before
# it (issue #563). When none conflicts, gate the merged result once. The merge queue would find the same conflict only
# when that PR's turn comes, one entry at a time. Fix a reported pair by rebasing the later branch on the earlier one.
#
# A branch is a name on origin (origin/<branch> is used when it exists) or any local ref. The gate's logs are kept in
# .cache/wave-gate/ of the repository. WAVE_REPO, WAVE_BASE (default origin/main) and WAVE_GATE (default the merged
# tree's scripts/agent/gate.sh) are for tests.
set -uo pipefail
[ $# -gt 0 ] || { echo "usage: wave-check.sh <branch>..." >&2; exit 2; }
repo=${WAVE_REPO:-$(git -C "$(dirname "$0")" rev-parse --show-toplevel)}
base=${WAVE_BASE:-origin/main}
if git -C "$repo" remote get-url origin >/dev/null 2>&1; then
  git -C "$repo" fetch -q origin || { echo "wave-check: git fetch origin failed; refusing to check stale branches" >&2; exit 2; }
fi
wt=$(mktemp -d "${TMPDIR:-/tmp}/wave-check.XXXXXX")
trap 'git -C "$repo" worktree remove --force "$wt" >/dev/null 2>&1; rm -rf "$wt"' EXIT
git -C "$repo" worktree add -q --detach "$wt" "$base" || exit 2

merged="$base"
failed=0
for b in "$@"; do
  ref=origin/$b
  git -C "$wt" rev-parse -q --verify "$ref^{commit}" >/dev/null || ref=$b
  if ! git -C "$wt" rev-parse -q --verify "$ref^{commit}" >/dev/null; then
    echo "MISSING   $b: no such branch on origin or locally"
    failed=1
    continue
  fi
  if git -C "$wt" -c core.hooksPath=/dev/null merge -q --no-edit --no-ff "$ref" >/dev/null 2>&1; then
    echo "merged    $b"
    merged="$merged + $b"
  else
    echo "CONFLICT  $b against $merged: $(git -C "$wt" diff --name-only --diff-filter=U | tr '\n' ' ')"
    git -C "$wt" merge --abort >/dev/null 2>&1
    failed=1
  fi
done
if [ "$failed" = 1 ]; then
  echo "WAVE FAIL: rebase each conflicting branch on the branches before it, push it, and rerun"
  exit 1
fi
gate=${WAVE_GATE:-$wt/scripts/agent/gate.sh}
echo "gating the merged wave ($merged)"
(cd "$wt" && "$gate")
status=$?
if [ -d "$wt/.cache/gate" ]; then
  rm -rf "$repo/.cache/wave-gate" && mkdir -p "$repo/.cache" && cp -R "$wt/.cache/gate" "$repo/.cache/wave-gate"
fi
[ "$status" = 0 ] && echo "WAVE PASS" || echo "WAVE FAIL: the merged wave fails the gate; logs in .cache/wave-gate/"
exit "$status"
