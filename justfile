# funcd justfile — the single task runner for all operations.
# Recipes: help, fmt, lint, test, build, tidy, ci.
# e2e and integration are reserved for FEAT-0000/F20 (testing strategy).

# ---- helpers ----
_has-packages := `go list ./... 2>/dev/null`

# ---- recipes ----

# default recipe: list all available recipes
default:
    @just --list

# show this help
help:
    @just --list

# format Go source files
fmt:
    go fmt ./...

# run the linter (golangci-lint via go tool); no-op when no Go packages exist
lint:
    @if [ -n "{{_has-packages}}" ]; then go tool golangci-lint run ./...; fi

# run all tests; no-op when no Go packages exist
test:
    @if [ -n "{{_has-packages}}" ]; then go test ./...; fi

# compile all packages
build:
    go build ./...

# regenerate the OpenAPI spec from Go types (via huma reflection)
generate:
    go run ./internal/controlplane/cmd/specgen/ -out api/openapi/funcd.v1alpha1.yaml

# tidy go.mod and go.sum
tidy:
    go mod tidy

# CI pipeline (generate staleness + fmt check + lint + test + build + tidy-diff check)
ci: tidy generate
    go fmt ./...
    @if [ -n "$(git diff --name-only -- '*.go')" ]; then echo "Run just fmt and commit the result" && exit 1; fi
    @if [ -n "{{_has-packages}}" ]; then go tool golangci-lint run ./...; fi
    @if [ -n "{{_has-packages}}" ]; then go test ./...; fi
    go build ./...
    go mod verify
    @if [ -n "$(git diff --name-only -- go.mod go.sum)" ]; then echo "go.mod or go.sum is not tidy — run just tidy and commit the result" && exit 1; fi

# build the slatedb_uniffi native lib (cgo) from pinned source — required for the slatedb engine lane (ADR-0006 §5)
slatedb-lib:
    #!/usr/bin/env bash
    set -euo pipefail
    src=".cache/slatedb"
    if [ ! -d "$src" ]; then
      git clone --depth 1 --branch bindings/go/v0.13.1 https://github.com/slatedb/slatedb "$src"
    fi
    cargo build --release --manifest-path "$src/Cargo.toml" -p slatedb-uniffi
    echo "built $src/target/release/libslatedb_uniffi.*"

# run the slatedb (cgo) engine lane — run `just slatedb-lib` first. The default `just ci` stays pure-Go.
test-slatedb:
    #!/usr/bin/env bash
    set -euo pipefail
    lib="$(pwd)/.cache/slatedb/target/release"
    if [ ! -d "$lib" ]; then echo "run 'just slatedb-lib' first (native lib not built)"; exit 1; fi
    CGO_ENABLED=1 CGO_LDFLAGS="-L$lib" DYLD_LIBRARY_PATH="$lib" LD_LIBRARY_PATH="$lib" \
      go test -tags slatedb ./internal/store/slatedb/...
