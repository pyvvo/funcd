#!/usr/bin/env bash
# gate.sh — every repo-wide check, once, for a branch that is ready to PR: `just ci-full` (the canonical CI set,
# e2e included), the Linux build/vet/lint, and that the run changed no file (tidy/generate/fmt had nothing to do).
# One PASS/FAIL line per step; full logs in .cache/gate/. Run it once per PR, not per change.
#
#   scripts/agent/gate.sh
set -uo pipefail
root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
d="$root/scripts/agent/d"
logs="$root/.cache/gate"
mkdir -p "$logs"
cd "$root" || exit 2
fail=0
step() {
  local name=$1 t0=$SECONDS
  shift
  if "$@" >"$logs/$name.log" 2>&1; then
    echo "PASS $name ($((SECONDS - t0))s)"
  else
    echo "FAIL $name ($((SECONDS - t0))s); tail of .cache/gate/$name.log:"
    tail -15 "$logs/$name.log" | sed 's/^/    /'
    fail=1
  fi
}
linux() {
  local lint
  lint=$("$d" go tool -n golangci-lint) &&
    GOOS=linux "$d" go build ./... &&
    GOOS=linux "$d" go vet ./internal/... &&
    GOOS=linux "$d" "$lint" run ./internal/...
}
tree() { git status --porcelain && git diff; }
before=$(tree)
unchanged() { [ "$(tree)" = "$before" ] || { git status --short; return 1; }; }
step ci-full "$d" just ci-full
step linux linux
step tree-unchanged unchanged
[ "$fail" = 0 ] && echo "GATE PASS" || echo "GATE FAIL"
exit "$fail"
