#!/usr/bin/env bash
# check-generated.sh: fails when a generated file differs from the commit after `just generate`, the API schema
# (ADR-0005) or a Spec page of the user guide (ADR-0323), and prints those files. `just ci` runs it after `generate`.
set -euo pipefail
stale=$(git status --porcelain -- api/openapi ':(glob)docs/guide/develop/**/spec.md' docs/guide/develop/common-definitions.md)
if [ -n "$stale" ]; then
	echo "Run just generate and commit the result:" >&2
	echo "$stale" >&2
	exit 1
fi
