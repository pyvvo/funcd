#!/usr/bin/env sh
# moddir.sh <module> — print the root of a Go module funcd pins (ADR-0141): its module-cache dir, or
# a go.work override. The module cache is read-only: copy a dir before writing into it.
set -eu
go mod download "$1"
go list -m -f '{{.Dir}}' "$1"
