#!/usr/bin/env bash
# Fetch + bundle the DuckDB+Quack CATALOG ENGINE (the standalone duckdb CLI + the pinned
# httpfs/ducklake/quack/sqlite extensions) for ONE target os/arch into
# internal/catalog/embedengine/engine.tar.gz — the process-mode engine `funcdctl dev` embeds
# (ADR-0125 Decision 5, FEAT-0001/F90, -tags dev).
#
# SINGLE SOURCE OF TRUTH for `just build-catalog-engine` and .github/workflows/release.yml, so the
# CI build step and the local recipe never drift. Fetches PER-ARCH (no docker): the CLI from DuckDB's
# GitHub release and the extensions from extensions.duckdb.org, so it works for darwin as well as
# linux. Each file must match its SHA-256 in images/runtime/duckdb/duckdb.sha256 before it is used;
# a DuckDB version with no pins there stops the script. Needs: curl, unzip, gunzip, tar, awk,
# sha256sum (all standard on the CI runner and in the dev shell). NOT run by `just ci` — ci stays
# green on the committed placeholder.
#
# Usage: scripts/fetch-catalog-engine.sh <os> <arch> [duckdb-version]
set -euo pipefail

os="${1:?usage: fetch-catalog-engine.sh <os> <arch> [duckdb-version]}"
arch="${2:?usage: fetch-catalog-engine.sh <os> <arch> [duckdb-version]}"
ver="${3:-1.5.4}" # pinned DuckDB (ADR-0086: 1.5.4 Variegata, all MIT)

# The CLI ships one universal macOS binary; the loadable extensions are per-arch. Map the funcdctl
# target onto the duckdb.org CLI asset + the extension platform token.
case "${os}/${arch}" in
  linux/amd64)  cli="linux-amd64"   ; extplat="linux_amd64"  ;;
  linux/arm64)  cli="linux-arm64"   ; extplat="linux_arm64"  ;;
  darwin/amd64) cli="osx-universal" ; extplat="osx_amd64"    ;;
  darwin/arm64) cli="osx-universal" ; extplat="osx_arm64"    ;;
  *) echo "fetch-catalog-engine: unsupported target ${os}/${arch}" >&2; exit 1 ;;
esac

out="internal/catalog/embedengine/engine.tar.gz"
sums="$(cd "$(dirname "$0")/.." && pwd)/images/runtime/duckdb/duckdb.sha256"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
# DuckDB resolves an extension_directory as <dir>/v<ver>/<platform>/<ext>.duckdb_extension, so the
# bundle mirrors that nested layout — the dev driver just points extension_directory at extensions/.
extdir="${work}/extensions/v${ver}/${extplat}"
mkdir -p "${extdir}" "${work}/v${ver}"

# pinned <path>: the line of duckdb.sha256 that pins <path>, for sha256sum -c; it fails when none does.
pinned() {
  awk -v p="$1" '$2 == p { print; found = 1 } END { exit !found }' "${sums}" ||
    { echo "fetch-catalog-engine: no SHA-256 pinned for $1 in images/runtime/duckdb/duckdb.sha256" >&2; return 1; }
}

echo "fetch-catalog-engine: duckdb ${ver} CLI (${cli}) + extensions (${extplat}) for ${os}/${arch}"
zip="v${ver}/duckdb_cli-${cli}.zip"
curl -fsSL -o "${work}/${zip}" "https://github.com/duckdb/duckdb/releases/download/v${ver}/duckdb_cli-${cli}.zip"
pinned "${zip}" | (cd "${work}" && sha256sum -c -)
unzip -qo "${work}/${zip}" -d "${work}"

# The DuckLake catalog can persist its metadata in SQLite; the artifact is `sqlite_scanner`
# (DuckDB's `INSTALL sqlite` is an alias for it). httpfs = S3/HTTP fs, quack = the client-server
# protocol, ducklake = the lakehouse format.
for ext in httpfs ducklake quack sqlite_scanner; do
  curl -fsSL "http://extensions.duckdb.org/v${ver}/${extplat}/${ext}.duckdb_extension.gz" \
    | gunzip > "${extdir}/${ext}.duckdb_extension"
  pinned "v${ver}/${extplat}/${ext}.duckdb_extension" | (cd "${work}/extensions" && sha256sum -c -)
done

printf 'duckdb %s\nplatform %s/%s\n' "${ver}" "${os}" "${arch}" > "${work}/VERSION"
chmod 0755 "${work}/duckdb"
# Owner names, xattrs and macOS metadata stay out: the placeholder archive is committed.
meta=(--numeric-owner --no-xattrs)
tar --version 2>/dev/null | grep -q bsdtar && meta+=(--no-mac-metadata)
COPYFILE_DISABLE=1 tar "${meta[@]}" -C "${work}" -czf "${out}" duckdb extensions VERSION
echo "fetch-catalog-engine: bundled ${os}/${arch} → ${out} ($(du -h "${out}" | cut -f1))"
