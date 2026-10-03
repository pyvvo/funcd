#!/usr/bin/env bash
# wave-check.sh <branch>...: before any PR of a fix wave is queued, merge all of the wave's group branches onto the
# base in a scratch worktree, in the given order, report each branch that conflicts with the ones merged before it,
# then run the gate once on the merged result (issue #563). The merge queue would find the same conflict only when
# that PR's turn comes, one entry at a time. Fix a reported pair by rebasing the later branch on the earlier one.
#
# A branch is a name on origin (origin/<branch> is used when it exists) or any local ref.
# WAVE_REPO, WAVE_BASE (default origin/main) and WAVE_GATE (default the merged tree's scripts/agent/gate.sh) are
# for tests.
set -uo pipefail
[ $# -gt 0 ] || { echo "usage: wave-check.sh <branch>..." >&2; exit 2; }
repo=${WAVE_REPO:-$(git -C "$(dirname "$0")" rev-parse --show-toplevel)}
base=${WAVE_BASE:-origin/main}
if git -C "$repo" remote get-url origin >/dev/null 2>&1; then
  git -C "$repo" fetch -q origin
fi
wt=$(mktemp -d "${TMPDIR:-/tmp}/wave-check.XXXXXX")
git -C "$repo" worktree add -q --detach "$wt" "$base" || exit 2
trap 'git -C "$repo" worktree remove --force "$wt" >/dev/null 2>&1; rm -rf "$wt"' EXIT

merged="$base"
conflicts=0
for b in "$@"; do
  ref=origin/$b
  git -C "$wt" rev-parse -q --verify "$ref^{commit}" >/dev/null || ref=$b
  if git -C "$wt" -c core.hooksPath=/dev/null merge -q --no-edit --no-ff "$ref" >/dev/null 2>&1; then
    echo "merged    $b"
    merged="$merged + $b"
  else
    files=$(git -C "$wt" diff --name-only --diff-filter=U | tr '\n' ' ')
    echo "CONFLICT  $b against $merged: ${files:-(no file conflict: the merge itself failed)}"
    git -C "$wt" merge --abort >/dev/null 2>&1
    conflicts=1
  fi
done
if [ "$conflicts" = 1 ]; then
  echo "WAVE FAIL: rebase each conflicting branch on the branches before it, push it, and rerun"
  exit 1
fi
gate=${WAVE_GATE:-$wt/scripts/agent/gate.sh}
echo "gating the merged wave ($merged)"
(cd "$wt" && "$gate")
status=$?
[ "$status" = 0 ] && echo "WAVE PASS" || echo "WAVE FAIL: the merged wave fails the gate; the failing step names the package"
exit "$status"
