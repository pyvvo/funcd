#!/usr/bin/env bash
# build-worker-images.sh <arch> — internal/runtime/embedimg/{nodejs22,python314}.tar and .ref: the worker runtime images
# for linux/<arch>, built with the shims funcd pins (ADR-0141), each saved under :latest and its dev reference,
# funcd/runtime-<runtime>:dev-<12 hex digits of its image ID>, which its .ref holds (ADR-0359 Decision 4). The python314
# image must load its shim with the stdlib alone (ADR-0304), checked on a docker host of its arch. Shared by
# `just build-worker-images` and .github/workflows/release.yml; needs docker and go. Not run by `just ci`: ci stays green
# on the committed placeholders.
set -euo pipefail

arch="${1:?usage: build-worker-images.sh <arch>}"
cd "$(dirname "$0")/.."
out=internal/runtime/embedimg

for pair in nodejs22:github.com/pyvvo/funcd-typescript python314:github.com/pyvvo/funcd-python; do
  rt="${pair%%:*}" module="${pair#*:}"
  docker build --provenance=false --sbom=false --platform "linux/$arch" --build-arg SOURCE_DATE_EPOCH=315532800 \
    --build-context shim="$(scripts/moddir.sh "$module")/shim" -f "images/runtime/$rt/Dockerfile" \
    --output "type=image,name=funcd/runtime-$rt:latest,rewrite-timestamp=true,unpack=false" .
done

host="$(docker version --format '{{.Server.Arch}}')"
if [ "$host" != "$arch" ]; then
  echo "SKIP the python314 load check: a $arch image on a $host docker host"
else
  check="$(docker run --rm --entrypoint /usr/local/bin/python3.14 funcd/runtime-python314:latest \
    -c "import funcd_shim.shim, ssl; print('shim loads', flush=True); import fastjsonschema" 2>&1 || true)"
  case "$check" in
    *"shim loads"*"No module named 'fastjsonschema'"*) ;;
    *) printf 'the python314 image must load its shim and ship no fastjsonschema (ADR-0304):\n%s\n' "$check" >&2; exit 1 ;;
  esac
fi

for rt in nodejs22 python314; do
  id="$(docker image inspect --format '{{.Id}}' "funcd/runtime-$rt:latest")"
  id="${id#sha256:}"
  ref="funcd/runtime-$rt:dev-${id:0:12}"
  docker tag "funcd/runtime-$rt:latest" "$ref"
  docker save "funcd/runtime-$rt:latest" "$ref" | gzip -9n > "$out/$rt.tar"
  printf '%s\n' "$ref" > "$out/$rt.ref"
done
echo "worker images written to $out/ for linux/$arch"
