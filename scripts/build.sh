#!/bin/sh
# build.sh — build the funcd single binary with version stamping (ADR-0026).
#
# The funcd binary is a pure-Go static build (CGO_ENABLED=0) for BOTH dev and release —
# ADR-0065 made the metastore engine pure-Go Badger, so there is no cgo lane anymore.
#
# Override any of VERSION / COMMIT / DATE / OUT / CGO_ENABLED via the environment.
set -eu

PKG="github.com/pyvvo/funcd/internal/platform/version"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
DATE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
OUT="${OUT:-dist/funcd}"

LDFLAGS="-X ${PKG}.Version=${VERSION} -X ${PKG}.Commit=${COMMIT} -X ${PKG}.Date=${DATE}"

mkdir -p "$(dirname "$OUT")"

# Default: pure-Go single binary (CI-buildable, no cgo).
CGO_ENABLED="${CGO_ENABLED:-0}" go build -ldflags "${LDFLAGS}" -o "${OUT}" ./cmd/funcd

echo "built ${OUT}"
"${OUT}" version || echo "  (version: ${VERSION})"

# --- Release build: pure-Go static (ADR-0065) -------------------------------
# The release IS the default build above: CGO_ENABLED=0, a pure-Go static binary.
# ADR-0065 replaced ADR-0006's slatedb/cgo metastore engine with pure-Go Badger, so
# there is no native archive to static-link and no `-tags slatedb` lane anymore.
#
# --- Self-contained runtime release (ADR-0054) ------------------------------
# A SELF-CONTAINED release is PER-ARCH: the funcd binary embeds the matching-arch
# curated image tars (internal/runtime/embedimg/{nodejs22,python314}.tar) so it runs
# functions with no registry. The release flow (a real Linux release lane, not run here):
#
#   1. Build + embed the matching-arch curated images (replaces the placeholders):
#        ARCH=${ARCH:-$(go env GOARCH)} just build-runtime-images   # docker; gzip -9 OCI tars
#   2. Build the per-arch funcd binary (the go:embed picks up the just-written tars):
#        GOARCH=${ARCH} CGO_ENABLED=0 go build -ldflags "${LDFLAGS}" -o dist/funcd-${ARCH} ./cmd/funcd
#   3. Bundle the two runtime BINARIES funcd execs — containerd + crun — alongside the
#      binary (the ADR-0054 "ship-in-release-tarball" option; crun stays a SEPARATE
#      execed binary so its GPL never links into funcd — the ADR-0011 boundary):
#        dist/funcd-${ARCH}.tar.gz := { funcd-${ARCH}, bin/containerd, bin/crun }
#      `funcd install` then lays these onto PATH + writes the single funcd.service unit.
#
# Fetching/pinning the containerd+crun binaries is a release concern handled by the release
# pipeline (pinned upstream tags, per-arch) — this script only documents the structure; it
# does not fetch them. The image is embedded either way; the tarball keeps funcd ~90 MB +
# two sibling binaries vs the ~150 MB embed-everything ceiling (ADR-0054 Open question).
