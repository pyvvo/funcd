#!/usr/bin/env bash
# linux-exec.sh: run Linux test binaries in funcd-linux-tests, a long-lived Lima VM (scripts/lima-linux-tests.yaml).
#
#   scripts/linux-exec.sh --boot     # start the VM unless it runs; the lane lock is held only while it boots
#   scripts/linux-exec.sh --check    # exit 0 when a test can run: a checkout under $HOME, limactl, the VM running
#   GOOS=linux GOARCH=<VM arch> go test -exec "$PWD/scripts/linux-exec.sh" <packages>   # as `just test-linux` does
#
# As the go test -exec program it gets <test binary> [flags] and runs the binary in the VM from the package directory
# as `runner`, a non-root user with a supplementary group, as on CI's runner. The Go toolchain that built the binary
# is on PATH (its golang.org/toolchain module, from the host's module cache), and HOME, GOCACHE and GOMODCACHE are
# writable and persist in the VM. sudo starts the binary with a fresh environment: each LINUX_EXEC_<NAME>=<value> in
# ours sets <NAME>=<value> in it (`just test-linux-e2e` sets CI and GOPROXY this way). go test resolves a relative
# -exec path from the package directory, so pass it whole.
# The VM mounts $HOME read-only at its own path: the binary is staged under .cache/linux-exec, and a checkout outside
# $HOME is refused, because the VM would not see it.
set -euo pipefail
vm=funcd-linux-tests
here=$(cd "$(dirname "$0")" && pwd -P)

running() { [ "$(limactl list --format '{{.Status}}' "$vm" 2>/dev/null)" = Running ]; }

# have_limactl fails, saying why, without limactl on PATH.
have_limactl() {
  command -v limactl >/dev/null && return
  echo "linux-exec: limactl is not on PATH (the dev shell has it on macOS)" >&2
  return 1
}

# up fails, saying why, unless the VM runs.
up() {
  have_limactl || return
  running && return
  echo "linux-exec: the $vm VM is not running; just test-linux boots it" >&2
  return 1
}

# checkout prints the root of the current directory's checkout, or refuses one outside $HOME.
checkout() {
  local src home
  src=$(cd "$(git rev-parse --show-toplevel)" && pwd -P)
  home=$(cd "$HOME" && pwd -P)
  case $src/ in
  "$home"/*) echo "$src" ;;
  *)
    echo "linux-exec: refusing $src: it is outside \$HOME ($home), and the $vm VM mounts only \$HOME, so it" \
      "would not see the package or its testdata. Run from a checkout under \$HOME." >&2
    return 2
    ;;
  esac
}

case ${1:-} in
--boot)
  checkout >/dev/null
  have_limactl
  running && exit 0
  (
    "$here/lane-lock.sh" "$BASHPID"
    running && exit 0
    if limactl list --format '{{.Name}}' "$vm" >/dev/null 2>&1; then
      limactl start --tty=false "$vm"
    else
      limactl start --name "$vm" --tty=false "$here/lima-linux-tests.yaml"
    fi
  )
  exit
  ;;
--check)
  checkout >/dev/null
  up
  exit
  ;;
esac

bin=${1:?usage: linux-exec.sh --boot | --check | <test binary> [flags]}
shift
case $bin in /*) ;; *) bin=$PWD/$bin ;; esac
src=$(checkout)
up
read -r version arch < <(go version -m "$bin" |
  awk 'NR == 1 { v = $2 } $2 ~ /^GOARCH=/ { a = substr($2, 8) } END { print v, a }')
toolchain=golang.org/toolchain@v0.0.1-$version.linux-$arch
(cd / && go mod download "$toolchain")
goroot=$(go env GOMODCACHE)/$toolchain
# A module zip keeps no execute bit. As go's own toolchain switch does, set the tools' bits before bin/go's, which the
# check reads, so a parallel run never finds go executable before its tools.
[ -x "$goroot/bin/go" ] || chmod a+x "$goroot"/pkg/tool/*/* "$goroot"/bin/*
mkdir -p "$src/.cache/linux-exec"
stage=$(mktemp -d "$src/.cache/linux-exec/XXXXXX")
trap 'rm -rf "$stage"' EXIT
# A copy, not a hard link: a test that re-executes its own binary got EACCES from a link to go test's file.
cp -c "$bin" "$stage/" 2>/dev/null || cp "$bin" "$stage/"
vars=()
for name in ${!LINUX_EXEC_@}; do
  vars+=("${name#LINUX_EXEC_}=${!name}")
done
limactl shell --workdir "$(pwd -P)" "$vm" sudo -H -u runner \
  env PATH="$goroot/bin:/usr/local/bin:/usr/bin:/bin" GOTOOLCHAIN=local ${vars[@]+"${vars[@]}"} "$stage/${bin##*/}" "$@"
