#!/usr/bin/env bash
# lanes.sh <branch>:<lane|all>...: run Lima lanes for pushed branches, one after the other (issue #566). Each spec gets
# a detached checkout of its own under the main checkout's .claude/worktrees/ (colima shares only $HOME, so a scratch
# worktree elsewhere cannot be mounted), runs `just lima-example <lane>` or `just lima-example-all` there through the
# lane lock, prints one PASS/FAIL line, and removes the checkout. The checkout is lane-<branch>-<lane>.<random>, so two
# runs at once never share one, and only the run that made it removes it, also when the run is stopped. Full logs:
# .cache/lanes/<branch>-<lane>.log in the main checkout. Both names turn the slashes of the branch into underscores. A
# full lane run takes about 12 minutes per spec.
set -u
[ $# -gt 0 ] || { echo "usage: lanes.sh <branch>:<lane|all>..." >&2; exit 2; }
root=$(cd "$(git -C "$(dirname "$0")" rev-parse --path-format=absolute --git-common-dir)/.." && pwd)
case "$root/" in "$HOME"/*) ;; *) echo "lanes: the main checkout $root is not under \$HOME, which colima cannot mount" >&2; exit 2 ;; esac
logs="$root/.cache/lanes"
mkdir -p "$logs" "$root/.claude/worktrees"
git -C "$root" fetch -q origin
fail=0
wt=
trap '[ -z "$wt" ] || git -C "$root" worktree remove --force "$wt"' EXIT
for spec in "$@"; do
  case "$spec" in *:?*) ;; *) echo "$spec: expected <branch>:<lane|all>"; fail=1; continue ;; esac
  br=${spec%%:*}
  lane=${spec##*:}
  name=${br//\//_}-$lane
  log="$logs/$name.log"
  ref=origin/$br
  git -C "$root" rev-parse -q --verify "$ref^{commit}" >/dev/null || ref=$br
  wt=$(mktemp -d "$root/.claude/worktrees/lane-$name.XXXXXX") || { fail=1; continue; }
  git -C "$root" worktree add -q --detach "$wt" "$ref" || { echo "$spec: no such branch"; rmdir "$wt"; wt=; fail=1; continue; }
  sha=$(git -C "$wt" rev-parse --short HEAD)
  t0=$SECONDS
  if [ "$lane" = all ]; then recipe=(just lima-example-all); else recipe=(just lima-example "$lane"); fi
  if (cd "$wt" && scripts/agent/d "${recipe[@]}") >"$log" 2>&1; then r=PASS; else r=FAIL; fail=1; fi
  echo "$spec @ $sha: $r ($((SECONDS - t0))s); $(grep -E 'final status|FAILED:|lane-lock' "$log" | tail -1)"
  git -C "$root" worktree remove --force "$wt"
  wt=
done
exit "$fail"
