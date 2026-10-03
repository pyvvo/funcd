#!/usr/bin/env bash
# triage.sh <pr>...: a one-screen view of a funcd PR before it is queued (issue #566): its checks, its size per file,
# the lines of its body that report the gate, the audit, lanes, parked issues and Fixes, and the count of masking
# patterns among its added Go lines (production and test). It counts added lines only, so read moved code by hand.
set -uo pipefail
for n in "$@"; do
  gh pr view "$n" --repo pyvvo/funcd --json title,statusCheckRollup,files,body --jq '
    "#'"$n"' \(.title)",
    ([.statusCheckRollup[] | "\(.name)=\(.conclusion // .status)"] | join(" ")),
    (.files[] | "  \(.path) +\(.additions) -\(.deletions)"),
    (.body | split("\n") | map(select(test("(?i)audit|gate|lane|parked|Fixes #"))) | .[])'
  diff=$(gh pr diff "$n" --repo pyvvo/funcd)
  prod=$(printf '%s\n' "$diff" | awk '/^\+\+\+ b\//{f=$2; next} /^\+[^+]/{ if (f ~ /\.go$/ && f !~ /_test\.go$/) print }')
  test=$(printf '%s\n' "$diff" | awk '/^\+\+\+ b\//{f=$2; next} /^\+[^+]/{ if (f ~ /_test\.go$/) print }')
  count() { printf '%s\n' "$1" | grep -cE "$2"; }
  echo "  added lines: production sleep=$(count "$prod" 'time\.Sleep\(') nolint=$(count "$prod" '//nolint')" \
    "discard=$(count "$prod" '^\+\s*_ = [a-zA-Z_.]+\(') | test sleep=$(count "$test" 'time\.Sleep\(') skip=$(count "$test" 't\.Skip')"
done
