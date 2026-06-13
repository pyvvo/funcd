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

# tidy go.mod and go.sum
tidy:
    go mod tidy

# CI pipeline (fmt check + lint + test + build + tidy-diff check)
ci: tidy
    go fmt ./...
    @if [ -n "$(git diff --name-only -- '*.go')" ]; then echo "Run just fmt and commit the result" && exit 1; fi
    @if [ -n "{{_has-packages}}" ]; then go tool golangci-lint run ./...; fi
    @if [ -n "{{_has-packages}}" ]; then go test ./...; fi
    go build ./...
    go mod verify
    @if [ -n "$(git diff --name-only -- go.mod go.sum)" ]; then echo "go.mod or go.sum is not tidy — run just tidy and commit the result" && exit 1; fi
