#!/usr/bin/env bash
# lanes.sh <branch>:<lane|all>...: run Lima lanes for pushed branches, one after the other (issue #566). Each spec gets
# a detached checkout under the main checkout's .claude/worktrees/ (colima shares only $HOME, so a scratch worktree
# elsewhere cannot be mounted), runs `just lima-example <lane>` or `just lima-example-all` there through the lane lock,
# prints one PASS/FAIL line, and removes the checkout. Full logs: .cache/lanes/<lane>.log in the main checkout.
set -u
root=$(cd "$(git -C "$(dirname "$0")" rev-parse --git-common-dir)/.." && pwd)
case "$root/" in "$HOME"/*) ;; *) echo "lanes: the main checkout $root is not under \$HOME, which colima cannot mount" >&2; exit 2 ;; esac
logs="$root/.cache/lanes"
mkdir -p "$logs"
git -C "$root" fetch -q origin
fail=0
for spec in "$@"; do
  br=${spec%%:*}
  lane=${spec##*:}
  wt="$root/.claude/worktrees/lane-$lane"
  git -C "$root" worktree remove --force "$wt" 2>/dev/null
  ref=origin/$br
  git -C "$root" rev-parse -q --verify "$ref^{commit}" >/dev/null || ref=$br
  git -C "$root" worktree add -q --detach "$wt" "$ref" || { echo "$spec: no such branch"; fail=1; continue; }
  sha=$(git -C "$wt" rev-parse --short HEAD)
  t0=$SECONDS
  if [ "$lane" = all ]; then recipe=(just lima-example-all); else recipe=(just lima-example "$lane"); fi
  if (cd "$wt" && scripts/agent/d "${recipe[@]}") >"$logs/$lane.log" 2>&1; then r=PASS; else r=FAIL; fail=1; fi
  echo "$spec @ $sha: $r ($((SECONDS - t0))s); $(grep -E 'final status|FAILED:|lane-lock' "$logs/$lane.log" | tail -1)"
  git -C "$root" worktree remove --force "$wt"
done
exit "$fail"
