#!/usr/bin/env bash
# example-copy.sh <project> — print a WRITABLE dir holding a language-repo example (ADR-0141), for recipes
# that write beside it (`funcdctl dev` state, generated inputs). <project> is `js/<name>`, `python/<name>`,
# a bare unique <name>, or an existing dir (used as-is, e.g. a sibling clone). The whole module is copied
# to .modcopy/<module> (so an example's relative references, like a uv path dep on ../../shim, resolve)
# and refreshed from the pin on every call; files the module lacks (dev state, generated inputs) are kept.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
p="$1"
if [ -d "$p" ]; then echo "$p"; exit 0; fi
ts=github.com/pyvvo/funcd-typescript
py=github.com/pyvvo/funcd-python
case "$p" in
  js/*) mods="$ts"; name="${p#js/}" ;;
  python/*) mods="$py"; name="${p#python/}" ;;
  *) mods="$ts $py"; name="$p" ;;
esac
found=""; n=0
for m in $mods; do
  root="$(scripts/moddir.sh "$m")"
  if [ -d "$root/examples/$name" ]; then found="$m $root"; n=$((n + 1)); fi
done
if [ "$n" -eq 0 ]; then
  echo "example '$p' is not in the pinned language modules (qualify it: js/<name> or python/<name>)" >&2; exit 1
elif [ "$n" -gt 1 ]; then
  echo "ambiguous example '$p' — qualify it: js/$name or python/$name" >&2; exit 1
fi
mod="${found%% *}"; root="${found#* }"
dst=".modcopy/$(basename "$mod")"
mkdir -p "$dst"
chmod -R u+w "$dst"
cp -R "$root/." "$dst/"
chmod -R u+w "$dst"
echo "$dst/examples/$name"
