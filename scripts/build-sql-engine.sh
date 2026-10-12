#!/usr/bin/env bash
# build-sql-engine.sh <os> <arch> — internal/sql/embedengine/rqlited.gz, the rqlited that `funcdctl dev` runs as a
# host subprocess (ADR-0237 Decision 3), built with cgo from the source images/runtime/rqlite/rqlite.pin pins, its Go
# modules checked against rqlite's go.sum and the checksum database. linux builds in the Dockerfile's builder stage,
# as the image does; darwin builds natively and only on macOS (darwin/amd64 on an arm64 Mac through clang -arch).
# Shared by `just build-sql-engine` and .github/workflows/release.yml. Not run by `just ci`: ci stays green on the
# committed placeholder.
set -euo pipefail

os="${1:?usage: build-sql-engine.sh <os> <arch>}"
arch="${2:?usage: build-sql-engine.sh <os> <arch>}"
root="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck disable=SC1091
. "$root/images/runtime/rqlite/rqlite.pin"
out="$root/internal/sql/embedengine/rqlited.gz"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

case "$os/$arch" in
  linux/amd64 | linux/arm64)
    docker build --platform "linux/$arch" --target engine --output "type=local,dest=$work" \
      --build-arg RQLITE_VERSION="$RQLITE_VERSION" --build-arg RQLITE_COMMIT="$RQLITE_COMMIT" \
      -f "$root/images/runtime/rqlite/Dockerfile" "$root/images/runtime/rqlite"
    ;;
  darwin/amd64 | darwin/arm64)
    [ "$(uname -s)" = Darwin ] || { echo "build-sql-engine: darwin builds natively on macOS only" >&2; exit 1; }
    case "$arch" in amd64) carch=x86_64 ;; arm64) carch=arm64 ;; esac
    git -c advice.detachedHead=false clone --quiet --depth 1 --branch "$RQLITE_VERSION" https://github.com/rqlite/rqlite.git "$work/src"
    [ "$(git -C "$work/src" rev-parse HEAD)" = "$RQLITE_COMMIT" ] ||
      { echo "build-sql-engine: $RQLITE_VERSION is not commit $RQLITE_COMMIT" >&2; exit 1; }
    (cd "$work/src" && GOFLAGS=-mod=readonly GOOS=darwin GOARCH="$arch" CGO_ENABLED=1 CC="clang -arch $carch" \
      go build -trimpath \
        -ldflags "-s -w -X github.com/rqlite/rqlite/v10/cmd.Version=$RQLITE_VERSION -X github.com/rqlite/rqlite/v10/cmd.Commit=$RQLITE_COMMIT" \
        -o "$work/rqlited" ./cmd/rqlited)
    ;;
  *) echo "build-sql-engine: unsupported target $os/$arch" >&2; exit 1 ;;
esac

gzip -n -9 -c "$work/rqlited" > "$out"
echo "build-sql-engine: rqlited $RQLITE_VERSION for $os/$arch → internal/sql/embedengine/rqlited.gz ($(du -h "$out" | cut -f1))"
