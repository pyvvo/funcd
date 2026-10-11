#!/usr/bin/env bash
# linux-exec.sh: run Linux test binaries in funcd-linux-tests, a long-lived Lima VM (scripts/lima-linux-tests.yaml).
#
#   scripts/linux-exec.sh --run <cmd...>  # run cmd holding the run lock, after booting the VM unless it runs
#   scripts/linux-exec.sh --boot          # start the VM unless it runs; the lane lock is held only while it boots
#   scripts/linux-exec.sh --recreate      # after a change to the template: delete the VM and boot a new one
#   scripts/linux-exec.sh --check         # exit 0 when a test can run: a checkout under $HOME, limactl, the VM running
#   GOOS=linux GOARCH=<VM arch> scripts/linux-exec.sh --run go test -exec "$PWD/scripts/linux-exec.sh" <packages>
#
# One run at a time: the runs in the VM share runner's home directory and /tmp, and two at once failed each other's
# tests. Every mode but --check holds the run lock, a flock on ~/.cache/funcd-lima/linux-tests.lock, waiting while
# another run holds it. --run holds it for its whole command, whose test binaries then run at once, as `just test-linux`
# and `just test-linux-e2e` do; a binary run without --run holds it alone.
#
# As the go test -exec program it gets <test binary> [flags] and runs the binary in the VM from the package directory
# as `runner`, a non-root user with a supplementary group, as on CI's runner. The Go toolchain that built the binary
# is on PATH (its golang.org/toolchain module, from the host's module cache), and HOME, GOCACHE and GOMODCACHE are
# writable and persist in the VM. sudo starts the binary with a fresh environment: each LINUX_EXEC_<NAME>=<value> in
# ours sets <NAME>=<value> in it (`just test-linux-e2e` sets CI and GOPROXY this way). A VM that has not finished
# booting is refused. go test resolves a relative -exec path from the package directory, so pass it whole.
# The VM mounts $HOME read-only at its own path, where the package dir and its testdata resolve, so a checkout outside
# $HOME is refused. The binary itself goes to the VM's own disk over the shell's stdin and runs from there: an exec
# through the mount failed now and then with EACCES, also for a fresh copy of the binary.
set -euo pipefail
vm=funcd-linux-tests
here=$(cd "$(dirname "$0")" && pwd -P)
lock=${FUNCD_LINUX_TESTS_LOCK:-$HOME/.cache/funcd-lima/linux-tests.lock}

# hold <lock file> <cmd...>: run cmd holding an exclusive flock on the lock file, and pass on its exit status and the
# signals that end it. It writes its pid into the file, which a waiter names, and gives up after 30 minutes.
hold='
import fcntl, os, signal, subprocess, sys, time
fd = os.open(sys.argv[1], os.O_RDWR | os.O_CREAT, 0o644)
deadline, said = time.monotonic() + 1800, None
while True:
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        break
    except BlockingIOError:
        holder, now = os.pread(fd, 32, 0).decode().strip() or "?", time.monotonic()
        if now >= deadline:
            sys.exit(f"linux-exec: the run of pid {holder} still holds {sys.argv[1]}; gave up after 30 minutes")
        if said is None or now - said >= 60:
            print(f"linux-exec: waiting for the run of pid {holder} to finish (one run at a time in the VM)",
                  file=sys.stderr, flush=True)
            said = now
        time.sleep(1)
os.ftruncate(fd, 0)
os.pwrite(fd, f"{os.getpid()}\n".encode(), 0)
child = subprocess.Popen(sys.argv[2:])
signal.signal(signal.SIGINT, signal.SIG_IGN)
for s in (signal.SIGTERM, signal.SIGHUP, signal.SIGQUIT):
    signal.signal(s, lambda n, _: child.send_signal(n))
rc = child.wait()
sys.exit(128 - rc if rc < 0 else rc)
'

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

# boot starts the VM unless it runs, holding the lane lock while it boots.
boot() {
  have_limactl
  running && return
  (
    "$here/lane-lock.sh" "$BASHPID"
    running && exit 0
    if limactl list --format '{{.Name}}' "$vm" >/dev/null 2>&1; then
      limactl start --tty=false "$vm"
    else
      limactl start --name "$vm" --tty=false "$here/lima-linux-tests.yaml"
    fi
  )
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

if [ "${1:-}" = --check ]; then
  checkout >/dev/null
  up
  exit
fi
checkout >/dev/null
if [ -z "${FUNCD_LINUX_TESTS_LOCKED:-}" ]; then
  mkdir -p "$(dirname "$lock")"
  FUNCD_LINUX_TESTS_LOCKED=1 exec python3 -c "$hold" "$lock" "$0" "$@"
fi
case ${1:-} in
--boot)
  boot
  exit
  ;;
--recreate)
  have_limactl
  if limactl list --format '{{.Name}}' "$vm" >/dev/null 2>&1; then limactl delete -f "$vm"; fi
  boot
  exit
  ;;
--run)
  shift
  [ $# -gt 0 ] || { echo "usage: linux-exec.sh --run <cmd...>" >&2; exit 2; }
  boot
  exec "$@"
  ;;
esac

bin=${1:?usage: linux-exec.sh --run <cmd...> | --boot | --recreate | --check | <test binary> [flags]}
shift
case $bin in /*) ;; *) bin=$PWD/$bin ;; esac
up
read -r version arch < <(go version -m "$bin" |
  awk 'NR == 1 { v = $2 } $2 ~ /^GOARCH=/ { a = substr($2, 8) } END { print v, a }')
toolchain=golang.org/toolchain@v0.0.1-$version.linux-$arch
(cd / && go mod download "$toolchain")
goroot=$(go env GOMODCACHE)/$toolchain
# A module zip keeps no execute bit. As go's own toolchain switch does, set the tools' bits before bin/go's, which the
# check reads, so a parallel run never finds go executable before its tools.
[ -x "$goroot/bin/go" ] || chmod a+x "$goroot"/pkg/tool/*/* "$goroot"/bin/*
vars=()
for name in ${!LINUX_EXEC_@}; do
  vars+=("${name#LINUX_EXEC_}=${!name}")
done
# Lima's boot script removes /run/lima-boot-done when a boot starts and writes it once the provisioning has run.
booted='[ -e /run/lima-boot-done ] && exec "$@"
echo "linux-exec: the funcd-linux-tests VM has not finished booting" >&2
exit 125'
# As runner: write stdin to a new temp dir, run it there with the flags, and remove the dir.
run='d=$(mktemp -d) && cat >"$d/$1" && chmod 755 "$d/$1" || exit 125
b=$d/$1
shift
"$b" "$@" </dev/null
rc=$?
rm -r "$d"
exit $rc'
limactl shell --workdir "$(pwd -P)" "$vm" sh -c "$booted" linux-exec sudo -H -u runner \
  env PATH="$goroot/bin:/usr/local/bin:/usr/bin:/bin" GOTOOLCHAIN=local ${vars[@]+"${vars[@]}"} \
  sh -c "$run" linux-exec "${bin##*/}" "$@" <"$bin"
